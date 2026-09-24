package server

import (
	"bytes"
	"strings"
	"testing"
)

func TestPassThroughSSEStreamPreservesResponsesEvents(t *testing.T) {
	src := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"
	var dst bytes.Buffer
	if err := PassThroughSSEStream(strings.NewReader(src), &dst); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != src {
		t.Fatalf("stream changed:\n got %q\nwant %q", got, src)
	}
}

func TestTransformSSELine_PassThroughChunk(t *testing.T) {
	// A minimal chunk similar to captured responses
	in := []byte(`{"id":"chatcmpl-xyz","object":"chat.completion.chunk","created":1754642367,"model":"gpt-4.1-2025-04-14","choices":[{"index":0,"delta":{"content":"Hello"},"logprobs":null,"finish_reason":null}]}`)
	out, done, err := TransformSSELine(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatalf("done should be false for normal chunk")
	}
	if !bytes.Contains(out, []byte(`"chat.completion.chunk"`)) {
		t.Fatalf("expected object to remain chat.completion.chunk: %s", string(out))
	}
	if !bytes.Contains(out, []byte(`"Hello"`)) {
		t.Fatalf("expected content to be preserved: %s", string(out))
	}
}

func TestTransformSSELine_Done(t *testing.T) {
	out, done, err := TransformSSELine([]byte("[DONE]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatalf("expected done=true for [DONE]")
	}
	if len(out) != 0 {
		t.Fatalf("expected no output for [DONE], got: %s", string(out))
	}
}

func TestRewriteSSEStream_EndToEnd(t *testing.T) {
	src := strings.Join([]string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4.1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"logprobs\":null,\"finish_reason\":null}]}\n",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var dst bytes.Buffer
	if err := RewriteSSEStream(strings.NewReader(src), &dst, "gpt-5"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out := dst.String()
	// Expect two SSE events: one JSON, then DONE, each followed by blank line
	if !strings.Contains(out, "data: [DONE]\n\n") {
		t.Fatalf("missing DONE marker in output: %q", out)
	}
	if !strings.Contains(out, "\"content\":\"Hi\"") {
		t.Fatalf("missing transformed content in output: %q", out)
	}
}

func TestRewriteSSEStream_CodexToOpenAI(t *testing.T) {
	// Simulate a Codex backend SSE stream with created, two deltas, completed, DONE
	src := strings.Join([]string{
		"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_abc\"}}",
		"",
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"delta\":\"Hello\"}",
		"",
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"delta\":\" world\"}",
		"",
		"data: {\"type\":\"response.completed\",\"sequence_number\":4}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var dst bytes.Buffer
	if err := RewriteSSEStream(strings.NewReader(src), &dst, "gpt-5"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out := dst.String()

	// Should include role chunk first
	if !strings.Contains(out, "\"delta\":{\"role\":\"assistant\"}") {
		t.Fatalf("missing assistant role delta: %q", out)
	}
	// Should include our content deltas
	if !strings.Contains(out, "\"content\":\"Hello\"") {
		t.Fatalf("missing first content delta: %q", out)
	}
	if !strings.Contains(out, "\"content\":\" world\"") {
		t.Fatalf("missing second content delta: %q", out)
	}
	// Should include a final stop chunk from response.completed
	if !strings.Contains(out, "\"finish_reason\":\"stop\"") {
		t.Fatalf("missing stop finish_reason: %q", out)
	}
	// And a DONE terminator
	if !strings.Contains(out, "data: [DONE]\n\n") {
		t.Fatalf("missing DONE marker: %q", out)
	}
}

func TestRewriteSSEStream_CodexToolCalls_ToOpenAI(t *testing.T) {
	src := strings.Join([]string{
		// created
		"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_tool\"}}",
		"",
		// tool call added
		"data: {\"type\":\"response.output_item.added\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"id\":\"fc_123\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"call_abc\",\"name\":\"get_weather\"}}",
		"",
		// arguments streamed in parts
		`data: {"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_123","output_index":0,"delta":"{\"location\":\"Pa"}`,
		"",
		`data: {"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc_123","output_index":0,"delta":"ris, France\"}"}`,
		"",
		// done and response completed
		"data: {\"type\":\"response.function_call_arguments.done\",\"sequence_number\":5,\"item_id\":\"fc_123\",\"output_index\":0}",
		"",
		"data: {\"type\":\"response.completed\",\"sequence_number\":6}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var dst bytes.Buffer
	if err := RewriteSSEStream(strings.NewReader(src), &dst, "gpt-5"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out := dst.String()
	// Expect role chunk
	if !strings.Contains(out, "\"delta\":{\"role\":\"assistant\"}") {
		t.Fatalf("missing assistant role: %q", out)
	}
	// Expect initial tool_calls with function name and id
	if !strings.Contains(out, "\"tool_calls\"") || !strings.Contains(out, "\"name\":\"get_weather\"") {
		t.Fatalf("missing tool_calls start/name: %q", out)
	}
	if !strings.Contains(out, "\"id\":\"call_abc\"") {
		t.Fatalf("missing tool call id: %q", out)
	}
	// Expect arguments deltas streamed
	if !strings.Contains(out, "\"arguments\":\"{\\\"location\\\":\\\"Pa") {
		t.Fatalf("missing first args delta: %q", out)
	}
	// Be lenient about escaping differences; accept common encodings
	secondOk := strings.Contains(out, "\"arguments\":\"ris, France\\\"}\"") ||
		strings.Contains(out, "\"arguments\":\"ris, France\\\\\\\"}\\\"\"") ||
		strings.Contains(out, "\"arguments\":\"ris, France\\\"}\"")
	if !secondOk {
		t.Fatalf("missing second args delta: %q", out)
	}
	// Finish reason should be tool_calls
	if !strings.Contains(out, "\"finish_reason\":\"tool_calls\"") {
		t.Fatalf("missing tool_calls finish reason: %q", out)
	}
	if !strings.Contains(out, "data: [DONE]\n\n") {
		t.Fatalf("missing DONE marker: %q", out)
	}
}

func TestRewriteSSEStream_WhitespacePreservation(t *testing.T) {
	// Test that newlines and whitespace-only deltas are preserved through transformation
	// This simulates: text "Foo" followed by newline "\n" followed by markdown "**Bar**"
	src := strings.Join([]string{
		"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_ws\"}}",
		"",
		`data: {"type":"response.output_text.delta","sequence_number":2,"delta":"Foo"}`,
		"",
		`data: {"type":"response.output_text.delta","sequence_number":3,"delta":"\n"}`,
		"",
		`data: {"type":"response.output_text.delta","sequence_number":4,"delta":"**Bar**"}`,
		"",
		"data: {\"type\":\"response.completed\",\"sequence_number\":5}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var dst bytes.Buffer
	if err := RewriteSSEStream(strings.NewReader(src), &dst, "gpt-5"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out := dst.String()

	// Should include role chunk
	if !strings.Contains(out, "\"delta\":{\"role\":\"assistant\"}") {
		t.Fatalf("missing assistant role delta: %q", out)
	}

	// Should include "Foo" content
	if !strings.Contains(out, "\"content\":\"Foo\"") {
		t.Fatalf("missing 'Foo' content delta: %q", out)
	}

	// CRITICAL: Should include newline content as "\n" (JSON-encoded)
	if !strings.Contains(out, "\"content\":\"\\n\"") {
		t.Fatalf("missing newline content delta - whitespace may be stripped: %q", out)
	}

	// Should include "**Bar**" content
	if !strings.Contains(out, "\"content\":\"**Bar**\"") {
		t.Fatalf("missing '**Bar**' content delta: %q", out)
	}

	// Should have stop finish reason
	if !strings.Contains(out, "\"finish_reason\":\"stop\"") {
		t.Fatalf("missing stop finish_reason: %q", out)
	}
}

func TestRewriteSSEStream_ReasoningMarkdownHeaders(t *testing.T) {
	// Test that bold markdown headers in reasoning content get newlines prepended
	// This simulates: reasoning delta "**Analysis**" should become "\n\n**Analysis**"
	src := strings.Join([]string{
		"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_reasoning\"}}",
		"",
		`data: {"type":"response.reasoning.delta","sequence_number":2,"output_index":0,"delta":"Looking at the code"}`,
		"",
		`data: {"type":"response.reasoning.delta","sequence_number":3,"output_index":0,"delta":"**Key Issues**"}`,
		"",
		`data: {"type":"response.reasoning.delta","sequence_number":4,"output_index":0,"delta":"The problem is..."}`,
		"",
		"data: {\"type\":\"response.completed\",\"sequence_number\":5}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var dst bytes.Buffer
	if err := RewriteSSEStream(strings.NewReader(src), &dst, "gpt-5"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out := dst.String()

	// Should include role chunk
	if !strings.Contains(out, "\"delta\":{\"role\":\"assistant\"}") {
		t.Fatalf("missing assistant role delta: %q", out)
	}

	// Should include first reasoning delta
	if !strings.Contains(out, "\"reasoning_content\":\"Looking at the code\"") {
		t.Fatalf("missing first reasoning delta: %q", out)
	}

	// CRITICAL: Should include markdown header with prepended newlines
	if !strings.Contains(out, "\"reasoning_content\":\"\\n\\n**Key Issues**\"") {
		t.Fatalf("markdown header should have newlines prepended: %q", out)
	}

	// Should include third reasoning delta
	if !strings.Contains(out, "\"reasoning_content\":\"The problem is...\"") {
		t.Fatalf("missing third reasoning delta: %q", out)
	}
}

func TestRewriteSSEStream_ReasoningHeaders_NoInjectOnPartialBold(t *testing.T) {
	// Test that partial bold tokens don't trigger newline injection
	// This simulates upstream splitting "**Analyzing user request**" across deltas
	src := strings.Join([]string{
		"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_partial\"}}",
		"",
		`data: {"type":"response.reasoning.delta","sequence_number":2,"output_index":0,"delta":"**Analyzing user request"}`,
		"",
		`data: {"type":"response.reasoning.delta","sequence_number":3,"output_index":0,"delta":"**"}`,
		"",
		`data: {"type":"response.reasoning.delta","sequence_number":4,"output_index":0,"delta":" continues here"}`,
		"",
		"data: {\"type\":\"response.completed\",\"sequence_number\":5}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var dst bytes.Buffer
	if err := RewriteSSEStream(strings.NewReader(src), &dst, "gpt-5"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out := dst.String()

	// Should NOT inject newlines before the opening "**Analyzing user request" (no closing **)
	if strings.Contains(out, "\"reasoning_content\":\"\\n\\n**Analyzing user request\"") {
		t.Fatalf("should not inject newlines before partial bold opening: %q", out)
	}

	// Should include the partial opening as-is
	if !strings.Contains(out, "\"reasoning_content\":\"**Analyzing user request\"") {
		t.Fatalf("missing partial bold opening: %q", out)
	}

	// Should NOT inject newlines before the closing "**" fragment
	if strings.Contains(out, "\"reasoning_content\":\"\\n\\n**\"") {
		t.Fatalf("should not inject newlines before partial bold closing: %q", out)
	}

	// Should include the closing fragment as-is
	if !strings.Contains(out, "\"reasoning_content\":\"**\"") {
		t.Fatalf("missing partial bold closing: %q", out)
	}
}

func TestRewriteSSEStream_ReasoningHeaders_NoDoubleNewline(t *testing.T) {
	// Test that we don't add extra newlines when delta already starts with newline
	src := strings.Join([]string{
		"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_double\"}}",
		"",
		`data: {"type":"response.reasoning.delta","sequence_number":2,"output_index":0,"delta":"Some text"}`,
		"",
		`data: {"type":"response.reasoning.delta","sequence_number":3,"output_index":0,"delta":"\n**Header**"}`,
		"",
		"data: {\"type\":\"response.completed\",\"sequence_number\":4}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var dst bytes.Buffer
	if err := RewriteSSEStream(strings.NewReader(src), &dst, "gpt-5"); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out := dst.String()

	// Should NOT add extra newlines (delta already starts with \n)
	// The delta "\n**Header**" should remain as-is, not become "\n\n\n**Header**"
	if strings.Contains(out, "\"reasoning_content\":\"\\n\\n\\n**Header**\"") {
		t.Fatalf("should not add extra newlines when delta already has newline: %q", out)
	}

	// Should preserve the original delta
	if !strings.Contains(out, "\"reasoning_content\":\"\\n**Header**\"") {
		t.Fatalf("should preserve original delta with single newline: %q", out)
	}
}
