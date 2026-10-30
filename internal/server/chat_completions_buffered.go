package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// streamingDelta represents the delta portion of a streamed chat completion chunk.
type streamingDelta struct {
	Role      string              `json:"role,omitempty"`
	Content   string              `json:"content,omitempty"`
	ToolCalls []streamingToolCall `json:"tool_calls,omitempty"`
}

// streamingChoice represents a single choice in a streamed chat completion chunk.
type streamingChoice struct {
	Index        int            `json:"index"`
	Delta        streamingDelta `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

// streamingChunk is a minimal view of an OpenAI-style streamed chat completion chunk.
type streamingChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []streamingChoice `json:"choices"`
	Usage   json.RawMessage   `json:"usage,omitempty"`
}

// bufferChatCompletionFromSSE consumes an upstream Codex SSE stream, uses the SSETransformer
// to convert it into OpenAI-style chat.completion.chunk events, and then aggregates those
// chunks into a single non-streaming ChatCompletionResponse suitable for clients that expect
// the classic /v1/chat/completions JSON shape.
func bufferChatCompletionFromSSE(body io.Reader, model string) (*ChatCompletionResponse, error) {
	transformer := NewSSETransformer(model)
	transformer.omitUsageFallback = true

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var (
		dataLines      [][]byte
		responseID     string
		usage          json.RawMessage
		streamModel    string
		created        int64
		role           string
		contentBuilder bytes.Buffer
		finishReason   string
		terminal       bool
		bufferedCalls  []bufferedToolCall
	)

	flushEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		raw := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]

		// Read only the final arguments and outcomes omitted by SSETransformer.
		var event map[string]interface{}
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("invalid upstream SSE event: %w", err)
		}
		eventType, _ := event["type"].(string)
		response, _ := event["response"].(map[string]interface{})
		switch eventType {
		case "response.completed", "response.failed", "response.incomplete", "error":
			terminal = true
			if detail, ok := response["error"].(map[string]interface{}); ok {
				return bufferedStreamError(detail)
			}
		}
		switch eventType {
		case "response.failed", "error":
			detail, ok := event["error"].(map[string]interface{})
			if !ok {
				// The event's type is not an OpenAI error classification.
				detail = map[string]interface{}{
					"code":    event["code"],
					"message": event["message"],
					"param":   event["param"],
				}
			}
			return bufferedStreamError(detail)
		case "response.incomplete":
			details, _ := response["incomplete_details"].(map[string]interface{})
			reason, _ := details["reason"].(string)
			switch reason {
			case "max_output_tokens":
				finishReason = "length"
			case "content_filter":
				finishReason = "content_filter"
			default:
				if reason == "" {
					reason = "incomplete"
				}
				return &bufferedUpstreamError{Code: reason, Message: "upstream response incomplete: " + reason, Type: "server_error"}
			}
			// Even individually completed calls must not escape an incomplete generation.
			bufferedCalls = nil
			return nil
		case "response.function_call_arguments.done", "response.output_item.done":
			itemID, _ := event["item_id"].(string)
			arguments, hasArguments := event["arguments"].(string)
			if item, ok := event["item"].(map[string]interface{}); ok {
				itemID, _ = item["id"].(string)
				arguments, hasArguments = item["arguments"].(string)
			}
			// Reuse the transformer's item-to-index mapping; final payloads replace,
			// rather than append to, arguments already received through deltas.
			if index, ok := transformer.toolIndexByItemID[itemID]; ok && hasArguments && index < len(bufferedCalls) {
				bufferedCalls[index].arguments.Reset()
				bufferedCalls[index].arguments.WriteString(arguments)
			}
			return nil
		}

		out, done, err := transformer.Transform(raw)
		if err != nil {
			return fmt.Errorf("failed to transform SSE event: %w", err)
		}
		if done {
			// DONE is signaled separately via [DONE] marker; nothing to do here.
			return nil
		}
		if len(out) == 0 {
			return nil
		}

		lines := bytes.Split(out, []byte("\n"))
		for _, line := range lines {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var chunk streamingChunk
			if err := json.Unmarshal(line, &chunk); err != nil {
				// If we cannot interpret this as a chunk, skip it rather than failing the whole request.
				continue
			}

			if responseID == "" && chunk.ID != "" {
				responseID = chunk.ID
			}
			if streamModel == "" && chunk.Model != "" {
				streamModel = chunk.Model
			}
			if created == 0 && chunk.Created != 0 {
				created = chunk.Created
			}
			if len(chunk.Usage) > 0 {
				usage = chunk.Usage
			}

			for _, ch := range chunk.Choices {
				if ch.Delta.Role != "" && role == "" {
					role = ch.Delta.Role
				}
				if ch.Delta.Content != "" {
					contentBuilder.WriteString(ch.Delta.Content)
				}
				for _, delta := range ch.Delta.ToolCalls {
					for len(bufferedCalls) <= delta.Index {
						bufferedCalls = append(bufferedCalls, bufferedToolCall{})
					}
					tool := &bufferedCalls[delta.Index]
					if delta.ID != "" {
						tool.call.ID = delta.ID
					}
					if delta.Type != "" {
						tool.call.Type = delta.Type
					}
					if delta.Function.Name != "" {
						tool.call.Function.Name = delta.Function.Name
					}
					tool.arguments.WriteString(delta.Function.Arguments)
				}
				if ch.FinishReason != nil && *ch.FinishReason != "" {
					finishReason = *ch.FinishReason
				}
			}
		}
		return nil
	}

	for !terminal && scanner.Scan() {
		line := scanner.Bytes()
		trimmed := bytes.TrimSpace(line)

		// Blank line terminates current SSE event.
		if len(trimmed) == 0 {
			if err := flushEvent(); err != nil {
				return nil, err
			}
			continue
		}

		// Ignore comment lines.
		if bytes.HasPrefix(trimmed, []byte(":")) {
			continue
		}

		// Only handle "data:" lines.
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			payload := bytes.TrimPrefix(trimmed, []byte("data:"))
			if len(payload) > 0 && payload[0] == ' ' {
				payload = payload[1:]
			}
			// Handle [DONE] terminator.
			if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
				// Flush any pending event and stop; nothing more to read.
				if err := flushEvent(); err != nil {
					return nil, err
				}
				break
			}
			cp := make([]byte, len(payload))
			copy(cp, payload)
			dataLines = append(dataLines, cp)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning SSE stream: %w", err)
	}
	if err := flushEvent(); err != nil {
		return nil, err
	}
	if len(bufferedCalls) > 0 && !terminal {
		return nil, fmt.Errorf("upstream response ended before tool calls completed")
	}

	// Construct a non-streaming ChatCompletionResponse.
	if responseID == "" {
		responseID = "chatcmpl-buffered"
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	if streamModel != "" {
		model = streamModel
	}
	if role == "" {
		role = "assistant"
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	var toolCalls []ChatToolCall
	if len(bufferedCalls) > 0 {
		toolCalls = make([]ChatToolCall, len(bufferedCalls))
		for index := range bufferedCalls {
			toolCalls[index] = bufferedCalls[index].call
			toolCalls[index].Function.Arguments = bufferedCalls[index].arguments.String()
		}
	}
	content := contentBuilder.String()
	messageContent := &content
	if content == "" && len(toolCalls) > 0 {
		messageContent = nil
	}
	resp := &ChatCompletionResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Usage:   usage,
		Choices: []ChatCompletionChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:      role,
					Content:   messageContent,
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
	}
	return resp, nil
}

func (e *bufferedUpstreamError) Error() string {
	return e.Code + ": " + e.Message
}

func bufferedStreamError(detail map[string]interface{}) *bufferedUpstreamError {
	code, _ := detail["code"].(string)
	message, _ := detail["message"].(string)
	kind, _ := detail["type"].(string)
	if code == "" {
		code = "upstream_error"
	}
	if message == "" {
		message = "upstream response failed"
	}
	if kind == "" || kind == "error" {
		switch code {
		case "context_length_exceeded":
			kind = "invalid_request_error"
		case "rate_limit_exceeded":
			kind = "rate_limit_error"
		default:
			kind = "server_error"
		}
	}
	var param *string
	if value, ok := detail["param"].(string); ok {
		param = &value
	}
	return &bufferedUpstreamError{Code: code, Message: message, Type: kind, Param: param}
}
