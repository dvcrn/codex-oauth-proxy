package server

import (
	"bytes"
	"encoding/json"
)

type ChatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ChatFunctionCall `json:"function"`
}

type ChatMessage struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	ToolCalls []ChatToolCall `json:"tool_calls,omitempty"`
}

type ChatCompletionRequest struct {
	Model       string                 `json:"model"`
	Messages    []ChatMessage          `json:"messages"`
	Temperature *float64               `json:"temperature,omitempty"`
	TopP        *float64               `json:"top_p,omitempty"`
	N           *int                   `json:"n,omitempty"`
	Stream      *bool                  `json:"stream,omitempty"`
	User        *string                `json:"user,omitempty"`
	OtherParams map[string]interface{} `json:"-"`
}

func (r *ChatCompletionRequest) UnmarshalJSON(data []byte) error {
	type Alias ChatCompletionRequest
	aux := &struct {
		*Alias
	}{Alias: (*Alias)(r)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	// Capture all other fields
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "model")
	delete(raw, "messages")
	delete(raw, "temperature")
	delete(raw, "top_p")
	delete(raw, "n")
	delete(raw, "stream")
	delete(raw, "user")
	r.OtherParams = raw
	return nil
}

type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   json.RawMessage        `json:"usage,omitempty"`
}

// streamingToolCall identifies which call an incremental update belongs to.
type streamingToolCall struct {
	Index int `json:"index"`
	ChatToolCall
}

type bufferedToolCall struct {
	call      ChatToolCall
	arguments bytes.Buffer
}

type bufferedUpstreamError struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
}
