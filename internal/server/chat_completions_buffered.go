package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// streamingDelta represents the delta portion of a streamed chat completion chunk.
type streamingDelta struct {
	Role      string              `json:"role,omitempty"`
	Content   string              `json:"content,omitempty"`
	ToolCalls []streamingToolCall `json:"tool_calls,omitempty"`
}

// streamingToolCall identifies which tool call an incremental update belongs to.
type streamingToolCall struct {
	Index int `json:"index"`
	ChatToolCall
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
}

// bufferChatCompletionFromSSE consumes an upstream Codex SSE stream, uses the SSETransformer
// to convert it into OpenAI-style chat.completion.chunk events, and then aggregates those
// chunks into a single non-streaming ChatCompletionResponse suitable for clients that expect
// the classic /v1/chat/completions JSON shape.
func bufferChatCompletionFromSSE(body io.Reader, model string) (*ChatCompletionResponse, error) {
	transformer := NewSSETransformer(model)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var (
		dataLines      [][]byte
		responseID     string
		streamModel    string
		created        int64
		role           string
		contentBuilder bytes.Buffer
		finishReason   string
	)
	type bufferedToolCall struct {
		call      ChatToolCall
		arguments bytes.Buffer
	}
	bufferedToolCalls := make(map[int]*bufferedToolCall)

	flushEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		raw := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]

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

			for _, ch := range chunk.Choices {
				if ch.Delta.Role != "" && role == "" {
					role = ch.Delta.Role
				}
				if ch.Delta.Content != "" {
					contentBuilder.WriteString(ch.Delta.Content)
				}
				for _, delta := range ch.Delta.ToolCalls {
					tool := bufferedToolCalls[delta.Index]
					if tool == nil {
						tool = &bufferedToolCall{}
						bufferedToolCalls[delta.Index] = tool
					}
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

	for scanner.Scan() {
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
				continue
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

	// Keep the tool-call order stable even when argument deltas are interleaved.
	toolIndexes := make([]int, 0, len(bufferedToolCalls))
	for index := range bufferedToolCalls {
		toolIndexes = append(toolIndexes, index)
	}
	sort.Ints(toolIndexes)
	toolCalls := make([]ChatToolCall, 0, len(toolIndexes))
	for _, index := range toolIndexes {
		tool := bufferedToolCalls[index]
		tool.call.Function.Arguments = tool.arguments.String()
		toolCalls = append(toolCalls, tool.call)
	}

	resp := &ChatCompletionResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []ChatCompletionChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:      role,
					Content:   contentBuilder.String(),
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
	}
	return resp, nil
}
