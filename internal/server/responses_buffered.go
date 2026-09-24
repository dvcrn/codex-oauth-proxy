package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type responsesFinalWriter struct {
	response json.RawMessage
}

func (w *responsesFinalWriter) Write(frame []byte) (int, error) {
	n := len(frame)
	var event, data []byte
	for len(frame) > 0 {
		line, rest, _ := bytes.Cut(frame, []byte("\n"))
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("event:")) {
			event = bytes.TrimSpace(line[len("event:"):])
		} else if bytes.HasPrefix(line, []byte("data:")) {
			data = bytes.TrimSpace(line[len("data:"):])
		}
		frame = rest
	}
	if len(event) != 0 && !bytes.Equal(event, []byte("response.completed")) &&
		!bytes.Equal(event, []byte("response.failed")) &&
		!bytes.Equal(event, []byte("response.incomplete")) &&
		!bytes.Equal(event, []byte("error")) {
		return n, nil
	}
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return n, nil
	}
	var envelope struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return 0, fmt.Errorf("decode upstream Responses event: %w", err)
	}
	if envelope.Type != "" {
		event = []byte(envelope.Type)
	}
	switch string(event) {
	case "response.completed", "response.failed", "response.incomplete":
		if len(envelope.Response) == 0 || bytes.TrimSpace(envelope.Response)[0] != '{' {
			return 0, fmt.Errorf("upstream Responses event has no response object")
		}
		w.response = envelope.Response
	case "error":
		return 0, fmt.Errorf("upstream Responses stream returned an error")
	}
	return n, nil
}

func bufferResponsesFromSSE(body io.Reader) (json.RawMessage, error) {
	var final responsesFinalWriter
	if _, err := CompleteResponsesSSEStream(body, &final); err != nil {
		return nil, fmt.Errorf("read upstream Responses stream: %w", err)
	}
	if len(final.response) == 0 {
		return nil, fmt.Errorf("upstream Responses stream ended without a final response")
	}
	return final.response, nil
}
