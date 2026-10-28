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
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
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
	)

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
					Role:    role,
					Content: contentBuilder.String(),
				},
				FinishReason: finishReason,
			},
		},
	}
	return resp, nil
}
