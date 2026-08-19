package server

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransformSSELine(t *testing.T) {
	// Test case 1: response.created event
	t.Run("handles response.created", func(t *testing.T) {
		input := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_123"}}`)
		transformer := NewSSETransformer("")
		out, done, err := transformer.Transform(input)
		require.NoError(t, err)
		assert.False(t, done)
		assert.Nil(t, out)
		assert.Equal(t, "chatcmpl-resp_123", transformer.responseID)
	})

	// Test case 2: response.output_text.delta event (first delta)
	t.Run("handles first output_text.delta", func(t *testing.T) {
		transformer := NewSSETransformer("")
		transformer.responseID = "chatcmpl-resp_123"
		input := []byte(`{"type":"response.output_text.delta","sequence_number":80,"item_id":"msg_123","output_index":1,"content_index":0,"delta":"Hello"}`)

		// First call should produce two chunks
		out, done, err := transformer.Transform(input)
		require.NoError(t, err)
		assert.False(t, done)

		// There should be two lines
		lines := bytes.Split(out, []byte("\n"))
		require.Len(t, lines, 2)

		// First chunk: role
		var chunk1 map[string]interface{}
		require.NoError(t, json.Unmarshal(lines[0], &chunk1))
		assert.Equal(t, "chat.completion.chunk", chunk1["object"])
		choices, _ := chunk1["choices"].([]interface{})
		choice1, _ := choices[0].(map[string]interface{})
		delta1, _ := choice1["delta"].(map[string]interface{})
		assert.Equal(t, "assistant", delta1["role"])
		assert.NotContains(t, delta1, "content")

		// Second chunk: content
		var chunk2 map[string]interface{}
		require.NoError(t, json.Unmarshal(lines[1], &chunk2))
		choices2, _ := chunk2["choices"].([]interface{})
		choice2, _ := choices2[0].(map[string]interface{})
		delta2, _ := choice2["delta"].(map[string]interface{})
		assert.Equal(t, "Hello", delta2["content"])
	})

	// Test case 3: subsequent response.output_text.delta event
	t.Run("handles subsequent output_text.delta", func(t *testing.T) {
		transformer := NewSSETransformer("")
		transformer.responseID = "chatcmpl-resp_123"
		// Mark that the initial role chunk has already been sent
		transformer.roleSent = true
		input := []byte(`{"type":"response.output_text.delta","sequence_number":81,"item_id":"msg_123","output_index":1,"content_index":0,"delta":" world"}`)
		out, done, err := transformer.Transform(input)
		require.NoError(t, err)
		assert.False(t, done)

		var chunk map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &chunk))
		assert.Equal(t, "chat.completion.chunk", chunk["object"])
		choices, _ := chunk["choices"].([]interface{})
		choice, _ := choices[0].(map[string]interface{})
		delta, _ := choice["delta"].(map[string]interface{})
		assert.Equal(t, " world", delta["content"])
		assert.NotContains(t, delta, "role")
	})

	// Test case 3b: reasoning delta event
	t.Run("handles reasoning delta", func(t *testing.T) {
		transformer := NewSSETransformer("")
		transformer.responseID = "chatcmpl-resp_123"
		input := []byte(`{"type":"response.reasoning_summary_text.delta","sequence_number":5,"item_id":"rs_1","summary_index":0,"delta":"Thinking..."}`)

		out, done, err := transformer.Transform(input)
		require.NoError(t, err)
		assert.False(t, done)

		lines := bytes.Split(out, []byte("\n"))
		require.Len(t, lines, 2)

		var chunk map[string]interface{}
		require.NoError(t, json.Unmarshal(lines[1], &chunk))
		delta, _ := chunk["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
		assert.Equal(t, "Thinking...", delta["reasoning_content"])
	})

	// Test case 4: response.completed event
	t.Run("handles response.completed", func(t *testing.T) {
		transformer := NewSSETransformer("")
		transformer.responseID = "chatcmpl-resp_123"
		input := []byte(`{"type":"response.completed","sequence_number":92,"response":{}}`)
		out, done, err := transformer.Transform(input)
		require.NoError(t, err)
		assert.False(t, done) // This is not the final [DONE]

		var chunk map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &chunk))
		assert.Equal(t, "chat.completion.chunk", chunk["object"])
		choices, _ := chunk["choices"].([]interface{})
		choice, _ := choices[0].(map[string]interface{})
		assert.Equal(t, "stop", choice["finish_reason"])
	})

	// Test case 5: [DONE] marker
	t.Run("handles [DONE]", func(t *testing.T) {
		transformer := NewSSETransformer("")
		input := []byte(`[DONE]`)
		out, done, err := transformer.Transform(input)
		require.NoError(t, err)
		assert.True(t, done)
		assert.Nil(t, out)
	})

	// Test case 6: Other events are ignored
	t.Run("ignores other events", func(t *testing.T) {
		transformer := NewSSETransformer("")
		input := []byte(`{"type":"response.in_progress","sequence_number":1,"response":{}}`)
		out, done, err := transformer.Transform(input)
		require.NoError(t, err)
		assert.False(t, done)
		assert.Nil(t, out)
	})
}

func TestNormalizeModel(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{"codex lowercase", "gpt-5-codex", "gpt-5-codex"},
		{"codex uppercase", "GPT-5-CODEX", "gpt-5-codex"},
		{"codex inside name", "gpt-5-mini-codex-preview", "gpt-5.1-codex-mini"},
		{"non codex", "gpt-5-mini", "gpt-5.1-codex-mini"},
		{"gpt-4o mini", "gpt-4o-mini", "gpt-5.1-codex-mini"},
		{"gpt-4o base", "gpt-4o", "gpt-5.1-codex-mini"},
		{"empty", "", "gpt-5"},
		{"gpt-5.1 base", "gpt-5.1", "gpt-5.1"},
		{"gpt-5.2 base", "gpt-5.2", "gpt-5.2"},
		{"gpt-5.2 with suffix", "gpt-5.2-high", "gpt-5.2"},
		{"gpt-5.2 codex base", "gpt-5.2-codex", "gpt-5.2-codex"},
		{"gpt-5.2 codex with suffix", "gpt-5.2-codex-xhigh", "gpt-5.2-codex"},
		{"gpt-5.3 explicit", "gpt-5.3-codex", "gpt-5.3-codex"},
		{"gpt-5.3 with effort suffix", "gpt-5.3-codex-high", "gpt-5.3-codex"},
		{"gpt-5.3 spark", "gpt-5.3-codex-spark", "gpt-5.3-codex-spark"},
		{"gpt-5.3 spark with effort suffix", "gpt-5.3-codex-spark-xhigh", "gpt-5.3-codex-spark"},
		{"gpt-5.4 base", "gpt-5.4", "gpt-5.4"},
		{"gpt-5.4 with effort suffix", "gpt-5.4-high", "gpt-5.4"},
		{"gpt-5.5 base", "gpt-5.5", "gpt-5.5"},
		{"gpt-5.5 with suffix", "gpt-5.5-xhigh", "gpt-5.5"},
		{"gpt-5.6-sol base", "gpt-5.6-sol", "gpt-5.6-sol"},
		{"gpt-5.6-sol with effort suffix", "gpt-5.6-sol-high", "gpt-5.6-sol"},
		{"gpt-5.1 with suffix", "gpt-5.1-high", "gpt-5.1"},
		{"gpt-5.1 codex", "gpt-5.1-codex", "gpt-5.1-codex"},
		{"gpt-5.1 codex max", "gpt-5.1-codex-max", "gpt-5.1-codex-max"},
		{"gpt-5.1 codex max with suffix", "gpt-5.1-codex-max-xhigh", "gpt-5.1-codex-max"},
		{"gpt-5.1 codex mini", "gpt-5.1-codex-mini", "gpt-5.1-codex-mini"},
		{"gpt-5.1 codex mini with suffix", "gpt-5.1-codex-mini-high", "gpt-5.1-codex-mini"},
		{"gpt-5 codex mini", "gpt-5-codex-mini", "gpt-5-codex-mini"},
		{"gpt-5 codex mini with suffix", "gpt-5-codex-mini-low", "gpt-5-codex-mini"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, normalizeModel(tc.input))
		})
	}
}

func TestNormalizeReasoningEffort(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{"explicit minimal", "minimal", "minimal"},
		{"explicit low", "low", "low"},
		{"explicit medium", "medium", "medium"},
		{"explicit high", "high", "high"},
		{"explicit xhigh", "xhigh", "xhigh"},
		{"none maps to low", "none", "low"},
		{"uppercase", "MEDIUM", "medium"},
		{"empty", "", ""},
		{"invalid", "aggressive", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, normalizeReasoningEffort(tc.input))
		})
	}
}

func TestClampReasoningEffortForModel(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		inputEffort string
		expected    string
	}{
		{"gpt-5 allows minimal", modelGPT5, "minimal", "minimal"},
		{"gpt-5.1 disallows minimal -> low", modelGPT51, "minimal", "low"},
		{"gpt-5.1 default when empty -> low", modelGPT51, "", "low"},
		{"gpt-5.2 allows low", modelGPT52, "low", "low"},
		{"gpt-5.2 allows medium", modelGPT52, "medium", "medium"},
		{"gpt-5.2 allows high", modelGPT52, "high", "high"},
		{"gpt-5.2 allows xhigh", modelGPT52, "xhigh", "xhigh"},
		{"gpt-5.2 default when empty -> medium", modelGPT52, "", "medium"},
		{"gpt-5.4 allows low", modelGPT54, "low", "low"},
		{"gpt-5.4 allows medium", modelGPT54, "medium", "medium"},
		{"gpt-5.4 allows high", modelGPT54, "high", "high"},
		{"gpt-5.4 allows xhigh", modelGPT54, "xhigh", "xhigh"},
		{"gpt-5.4 disallows minimal -> medium", modelGPT54, "minimal", "medium"},
		{"gpt-5.4 default when empty -> medium", modelGPT54, "", "medium"},
		{"gpt-5.2-codex allows low", modelGPT52Codex, "low", "low"},
		{"gpt-5.2-codex allows xhigh", modelGPT52Codex, "xhigh", "xhigh"},
		{"gpt-5.2-codex default when empty -> medium", modelGPT52Codex, "", "medium"},
		{"gpt-5.3-codex allows xhigh", modelGPT53Codex, "xhigh", "xhigh"},
		{"gpt-5.3-codex default -> medium", modelGPT53Codex, "", "medium"},
		{"gpt-5.3-codex-spark allows xhigh", modelGPT53CodexSpark, "xhigh", "xhigh"},
		{"gpt-5.3-codex-spark default -> high", modelGPT53CodexSpark, "", "high"},
		{"gpt-5.5 allows xhigh", modelGPT55, "xhigh", "xhigh"},
		{"gpt-5.5 default -> medium", modelGPT55, "", "medium"},
		{"gpt-5-codex-mini clamps low -> medium", modelGPT5CodexMini, "low", "medium"},
		{"gpt-5-codex-mini default when empty -> medium", modelGPT5CodexMini, "", "medium"},
		{"gpt-5.1-codex-high allowed", modelGPT51Codex, "high", "high"},
		{"gpt-5.1-codex-max allows xhigh", modelGPT51CodexMax, "xhigh", "xhigh"},
		{"gpt-5.1-codex-max minimal -> low", modelGPT51CodexMax, "minimal", "low"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := clampReasoningEffortForModel(normalizeReasoningEffort(tc.inputEffort), tc.model)
			assert.Equal(t, tc.expected, actual)
		})
	}
}
