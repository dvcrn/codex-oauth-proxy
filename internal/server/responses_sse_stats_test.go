package server

import (
	"strings"
	"testing"
)

func TestResponsesSSEStatsFinalOutputShape(t *testing.T) {
	var stats responsesSSEStats
	stream := "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n" +
		"event: response.completed\ndata: {\"response\":{\"status\":\"completed\",\"output\":[" +
		"{\"type\":\"reasoning\",\"encrypted_content\":\"" + strings.Repeat("x", 140000) + "\"}," +
		"{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\n"
	for offset := 0; offset < len(stream); offset += 1024 {
		end := min(offset+1024, len(stream))
		if _, err := stats.Write([]byte(stream[offset:end])); err != nil {
			t.Fatal(err)
		}
	}
	if !stats.finalParsed || !stats.finalCompleted || stats.finalMessages != 1 || stats.finalReasoning != 1 || stats.finalTextChars != 5 || stats.deltaChars != 5 {
		t.Fatalf("unexpected final shape: %+v", stats)
	}
}

func TestResponsesSSEStatsCountsSplitEventsWithoutContent(t *testing.T) {
	var stats responsesSSEStats
	parts := []string{
		"event: response.output_text.del",
		"ta\r\ndata: {\"delta\":\"private text\"}\r\n\r\n",
		"event: response.function_call_arguments.done\ndata: {}\n\n",
		"event: response.completed\ndata: {}\n\n",
	}
	var size int
	for _, part := range parts {
		n, err := stats.Write([]byte(part))
		if err != nil || n != len(part) {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(part))
		}
		size += len(part)
	}
	if stats.bytes != int64(size) || stats.dataLines != 3 || stats.textDeltas != 1 || stats.functionCalls != 1 || stats.completed != 1 {
		t.Fatalf("unexpected stream counts: bytes=%d data=%d text=%d function_calls=%d completed=%d", stats.bytes, stats.dataLines, stats.textDeltas, stats.functionCalls, stats.completed)
	}
}
