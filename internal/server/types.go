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
}

// Buffered completions read Codex events directly so final arguments and terminal
// outcomes are not lost in the streaming Chat Completions representation.
type bufferedCodexEventHeader struct {
	Type string `json:"type"`
}

type bufferedCodexEvent struct {
	Type      string                 `json:"type"`
	ItemID    string                 `json:"item_id"`
	Delta     string                 `json:"delta"`
	Arguments *string                `json:"arguments"`
	Item      *bufferedCodexItem     `json:"item"`
	Response  *bufferedCodexResponse `json:"response"`
	Error     *bufferedUpstreamError `json:"error"`
	Code      string                 `json:"code"`
	Message   string                 `json:"message"`
	Param     *string                `json:"param"`
}

type bufferedCodexResponse struct {
	ID                string                     `json:"id"`
	Model             string                     `json:"model"`
	CreatedAt         float64                    `json:"created_at"`
	Output            []bufferedCodexItem        `json:"output"`
	Error             *bufferedUpstreamError     `json:"error"`
	IncompleteDetails *bufferedIncompleteDetails `json:"incomplete_details"`
}

type bufferedIncompleteDetails struct {
	Reason string `json:"reason"`
}

type bufferedUpstreamError struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
}

type bufferedCodexItem struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	CallID    string                 `json:"call_id"`
	Name      string                 `json:"name"`
	Arguments *string                `json:"arguments"`
	Content   []bufferedCodexContent `json:"content"`
}

type bufferedCodexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type bufferedToolCall struct {
	call      ChatToolCall
	arguments bytes.Buffer
}

type bufferedChatCompletion struct {
	responseID   string
	model        string
	created      int64
	content      bytes.Buffer
	tools        []bufferedToolCall
	toolIndex    map[string]int
	terminal     string
	finishReason string
	upstreamErr  *bufferedUpstreamError
}
