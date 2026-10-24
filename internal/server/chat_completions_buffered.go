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
		var event bufferedCodexEvent
		if err := json.Unmarshal(bytes.Join(dataLines, []byte("\n")), &event); err != nil {
			return fmt.Errorf("failed to decode upstream SSE event: %w", err)
		}
		dataLines = dataLines[:0]
		if event.Type == "" {
			event.Type = eventType
		}
		eventType = ""
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
	case "response.completed":
		b.terminal = event.Type
		// Some Codex streams omit the final output array. Otherwise the final
		// snapshot is authoritative, not something to append to the deltas.
		if event.Response != nil && len(event.Response.Output) > 0 {
			b.content.Reset()
			b.tools = nil
			b.toolIndex = nil
			for _, item := range event.Response.Output {
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
	case "response.failed", "response.incomplete", "error":
		b.terminal = event.Type
	}
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
	finishReason := "stop"
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
				Content:   b.content.String(),
				ToolCalls: toolCalls,
			},
			FinishReason: finishReason,
		}},
	}
}
