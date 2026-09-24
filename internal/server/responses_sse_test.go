package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCompleteResponsesSSEStream(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantOutput    int
		wantID        string
		wantCallID    string
		wantArguments string
		unchanged     bool
	}{
		{
			name:       "missing output uses completed message",
			input:      "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n",
			wantOutput: 1,
			wantID:     "msg_1",
		},
		{
			name:          "tool call retains call id and arguments",
			input:         "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"shell\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
			wantOutput:    1,
			wantID:        "fc_1",
			wantCallID:    "call_1",
			wantArguments: `{"command":"pwd"}`,
		},
		{
			name:       "existing final output is untouched",
			input:      "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"msg_original\",\"type\":\"message\"}]}}\n\n",
			wantOutput: 1,
			wantID:     "msg_original",
			unchanged:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst bytes.Buffer
			repaired, err := CompleteResponsesSSEStream(strings.NewReader(tt.input), &dst)
			if err != nil {
				t.Fatal(err)
			}
			if repaired == tt.unchanged {
				t.Fatalf("repaired = %v, unchanged = %v", repaired, tt.unchanged)
			}
			if tt.unchanged && dst.String() != tt.input {
				t.Fatalf("untouched stream changed: %q", dst.String())
			}
			if !strings.Contains(dst.String(), "event: response.completed\n") {
				t.Fatalf("completion event missing: %q", dst.String())
			}
			var completed map[string]json.RawMessage
			for _, frame := range strings.Split(dst.String(), "\n\n") {
				if !strings.HasPrefix(frame, "event: response.completed\n") {
					continue
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.SplitN(frame, "\n", 2)[1], "data: ")), &completed); err != nil {
					t.Fatal(err)
				}
			}
			var response struct {
				Output []struct {
					ID        string `json:"id"`
					CallID    string `json:"call_id"`
					Arguments string `json:"arguments"`
				} `json:"output"`
			}
			if err := json.Unmarshal(completed["response"], &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Output) != tt.wantOutput || response.Output[0].ID != tt.wantID || response.Output[0].CallID != tt.wantCallID || response.Output[0].Arguments != tt.wantArguments {
				t.Fatalf("output = %+v, want %d items starting with %s (call ID %s)", response.Output, tt.wantOutput, tt.wantID, tt.wantCallID)
			}
		})
	}
}
