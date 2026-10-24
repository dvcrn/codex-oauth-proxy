package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// bufferChatCompletionFromSSE assembles a non-streaming Chat Completions response
// from Codex events, retaining final arguments that need not appear in deltas.
func bufferChatCompletionFromSSE(body io.Reader, model string) (*ChatCompletionResponse, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = modelDefault
	}
	state := bufferedChatCompletion{model: model}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var dataLines [][]byte
	var eventType string
	flushEvent := func() error {
		if len(dataLines) == 0 {
			eventType = ""
			return nil
		}
		raw := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]
		var header bufferedCodexEventHeader
		if err := json.Unmarshal(raw, &header); err != nil {
			return fmt.Errorf("failed to decode upstream SSE event: %w", err)
		}
		if header.Type == "" {
			header.Type = eventType
		}
		eventType = ""
		// Unrelated events can use different shapes for fields such as delta.
		// Do not decode them as text/function-call payloads.
		switch header.Type {
		case "response.created", "response.output_text.delta",
			"response.output_item.added", "response.output_item.done",
			"response.function_call_arguments.delta", "response.function_call_arguments.done",
			"response.completed", "response.failed", "response.incomplete", "error":
		default:
			return nil
		}
		var event bufferedCodexEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("failed to decode upstream SSE event: %w", err)
		}
		event.Type = header.Type
		state.applyEvent(event)
		return nil
	}

scanLoop:
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		switch {
		case len(line) == 0:
			if err := flushEvent(); err != nil {
				return nil, err
			}
			if state.terminal != "" {
				break scanLoop
			}
		case bytes.HasPrefix(line, []byte("event:")):
			eventType = string(bytes.TrimSpace(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			payload := bytes.TrimPrefix(line, []byte("data:"))
			if len(payload) > 0 && payload[0] == ' ' {
				payload = payload[1:]
			}
			if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
				if err := flushEvent(); err != nil {
					return nil, err
				}
				break scanLoop
			}
			dataLines = append(dataLines, bytes.Clone(payload))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning SSE stream: %w", err)
	}
	if err := flushEvent(); err != nil {
		return nil, err
	}
	if state.upstreamErr != nil {
		return nil, state.upstreamErr
	}
	// Individual calls can be complete even when the overall generation failed.
	if len(state.tools) > 0 && state.terminal != "response.completed" {
		return nil, fmt.Errorf("upstream response ended before tool calls completed")
	}
	return state.completion(), nil
}

func (b *bufferedChatCompletion) applyEvent(event bufferedCodexEvent) {
	if event.Response != nil {
		if event.Response.ID != "" {
			b.responseID = "chatcmpl-" + event.Response.ID
		}
		if event.Response.Model != "" {
			b.model = event.Response.Model
		}
		if event.Response.CreatedAt != 0 {
			b.created = int64(event.Response.CreatedAt)
		}
	}
	switch event.Type {
	case "response.output_text.delta":
		b.content.WriteString(event.Delta)
	case "response.output_item.added", "response.output_item.done":
		if event.Item != nil && event.Item.Type == "function_call" {
			b.applyToolItem(*event.Item)
		}
	case "response.function_call_arguments.delta":
		if index, ok := b.toolIndex[event.ItemID]; ok {
			b.tools[index].arguments.WriteString(event.Delta)
		}
	case "response.function_call_arguments.done":
		if index, ok := b.toolIndex[event.ItemID]; ok && event.Arguments != nil {
			b.tools[index].arguments.Reset()
			b.tools[index].arguments.WriteString(*event.Arguments)
		}
	case "response.completed", "response.incomplete":
		b.terminal = event.Type
		if event.Response != nil {
			b.applyOutput(event.Response.Output)
			if event.Response.Error != nil {
				b.upstreamErr = normalizeBufferedUpstreamError(event.Response.Error)
				return
			}
		}
		if event.Type == "response.incomplete" {
			// Preserve partial text, but never publish calls from an incomplete
			// generation, even when individual calls have already finished.
			b.tools = nil
			b.toolIndex = nil
			reason := "incomplete"
			if event.Response != nil && event.Response.IncompleteDetails != nil && event.Response.IncompleteDetails.Reason != "" {
				reason = event.Response.IncompleteDetails.Reason
			}
			switch reason {
			case "max_output_tokens":
				b.finishReason = "length"
			case "content_filter":
				b.finishReason = "content_filter"
			default:
				b.upstreamErr = normalizeBufferedUpstreamError(&bufferedUpstreamError{Code: reason, Message: "upstream response incomplete: " + reason})
			}
		}
	case "response.failed", "error":
		b.terminal = event.Type
		detail := event.Error
		if event.Response != nil && event.Response.Error != nil {
			detail = event.Response.Error
		}
		if detail == nil {
			detail = &bufferedUpstreamError{Code: event.Code, Message: event.Message, Param: event.Param}
		}
		b.upstreamErr = normalizeBufferedUpstreamError(detail)
	}
}

func (b *bufferedChatCompletion) applyOutput(output []bufferedCodexItem) {
	// Some Codex streams omit the final output array. Otherwise the final
	// snapshot is authoritative, not something to append to the deltas.
	if len(output) == 0 {
		return
	}
	b.content.Reset()
	b.tools = nil
	b.toolIndex = nil
	for _, item := range output {
		switch item.Type {
		case "function_call":
			b.applyToolItem(item)
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					b.content.WriteString(part.Text)
				}
			}
		}
	}
}

func (e *bufferedUpstreamError) Error() string {
	return e.Code + ": " + e.Message
}

func normalizeBufferedUpstreamError(detail *bufferedUpstreamError) *bufferedUpstreamError {
	result := *detail
	if result.Code == "" {
		result.Code = "upstream_error"
	}
	if result.Message == "" {
		result.Message = "upstream response failed"
	}
	if result.Type == "" {
		switch result.Code {
		case "context_length_exceeded":
			result.Type = "invalid_request_error"
		case "rate_limit_exceeded":
			result.Type = "rate_limit_error"
		default:
			result.Type = "server_error"
		}
	}
	return &result
}

func (b *bufferedChatCompletion) applyToolItem(item bufferedCodexItem) {
	index, ok := b.toolIndex[item.ID]
	if !ok {
		if b.toolIndex == nil {
			b.toolIndex = make(map[string]int)
		}
		index = len(b.tools)
		b.toolIndex[item.ID] = index
		b.tools = append(b.tools, bufferedToolCall{})
	}
	tool := &b.tools[index]
	tool.call.Type = "function"
	if item.CallID != "" {
		tool.call.ID = item.CallID
	} else if tool.call.ID == "" {
		tool.call.ID = "call_" + item.ID
	}
	if item.Name != "" {
		tool.call.Function.Name = item.Name
	}
	if item.Arguments != nil {
		tool.arguments.Reset()
		tool.arguments.WriteString(*item.Arguments)
	}
}

func (b *bufferedChatCompletion) completion() *ChatCompletionResponse {
	if b.responseID == "" {
		b.responseID = "chatcmpl-buffered"
	}
	if b.created == 0 {
		b.created = time.Now().Unix()
	}
	content := b.content.String()
	var messageContent *string
	if content != "" || len(b.tools) == 0 {
		messageContent = &content
	}
	finishReason := b.finishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	var toolCalls []ChatToolCall
	if len(b.tools) > 0 {
		finishReason = "tool_calls"
		// Calls remain in first-seen order; the map only resolves Codex item IDs.
		toolCalls = make([]ChatToolCall, len(b.tools))
		for index := range b.tools {
			toolCalls[index] = b.tools[index].call
			toolCalls[index].Function.Arguments = b.tools[index].arguments.String()
		}
	}
	return &ChatCompletionResponse{
		ID:      b.responseID,
		Object:  "chat.completion",
		Created: b.created,
		Model:   b.model,
		Choices: []ChatCompletionChoice{{
			Index: 0,
			Message: ChatMessage{
				Role:      "assistant",
				Content:   messageContent,
				ToolCalls: toolCalls,
			},
			FinishReason: finishReason,
		}},
	}
}
