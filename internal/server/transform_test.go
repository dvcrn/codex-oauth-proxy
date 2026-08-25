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
		// Currently-served models normalize to themselves.
		{"gpt-5.4 base", "gpt-5.4", "gpt-5.4"},
		{"gpt-5.4 with effort suffix", "gpt-5.4-high", "gpt-5.4"},
		{"gpt-5.4-mini base", "gpt-5.4-mini", "gpt-5.4-mini"},
		{"gpt-5.4-mini with effort suffix", "gpt-5.4-mini-xhigh", "gpt-5.4-mini"},
		{"gpt-5.5 base", "gpt-5.5", "gpt-5.5"},
		{"gpt-5.5 with suffix", "gpt-5.5-xhigh", "gpt-5.5"},
		{"gpt-5.6-sol base", "gpt-5.6-sol", "gpt-5.6-sol"},
		{"gpt-5.6-sol with effort suffix", "gpt-5.6-sol-high", "gpt-5.6-sol"},
		{"gpt-5.6-sol with max suffix", "gpt-5.6-sol-max", "gpt-5.6-sol"},
		{"gpt-5.6-terra base", "gpt-5.6-terra", "gpt-5.6-terra"},
		{"gpt-5.6-luna base", "gpt-5.6-luna", "gpt-5.6-luna"},
		{"daybreak blue", "gpt-daybreak-blue-latest", "gpt-daybreak-blue-latest"},
		{"uppercase is normalized", "GPT-5.5", "gpt-5.5"},

		// The GPT-5.0 to GPT-5.3 generation was retired upstream; those IDs now
		// collapse onto the current default rather than failing at call time.
		{"retired gpt-5", "gpt-5", modelDefault},
		{"retired gpt-5-codex", "gpt-5-codex", modelDefault},
		{"retired gpt-5.1", "gpt-5.1", modelDefault},
		{"retired gpt-5.1-codex", "gpt-5.1-codex", modelDefault},
		{"retired gpt-5.1-codex-max", "gpt-5.1-codex-max", modelDefault},
		{"retired gpt-5.2", "gpt-5.2", modelDefault},
		{"retired gpt-5.2-codex", "gpt-5.2-codex", modelDefault},
		{"retired gpt-5.3-codex", "gpt-5.3-codex", modelDefault},
		{"retired gpt-5.3-codex-spark", "gpt-5.3-codex-spark", modelDefault},
		{"unknown model", "some-other-model", modelDefault},
		{"empty", "", modelDefault},

		// Retired "mini" variants map onto the current mini model.
		{"retired gpt-5-codex-mini", "gpt-5-codex-mini", "gpt-5.4-mini"},
		{"retired gpt-5.1-codex-mini", "gpt-5.1-codex-mini", "gpt-5.4-mini"},
		{"gpt-4o mini", "gpt-4o-mini", "gpt-5.4-mini"},
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
		{"gpt-5.4 allows low", modelGPT54, "low", "low"},
		{"gpt-5.4 allows medium", modelGPT54, "medium", "medium"},
		{"gpt-5.4 allows high", modelGPT54, "high", "high"},
		{"gpt-5.4 allows xhigh", modelGPT54, "xhigh", "xhigh"},
		{"gpt-5.4 disallows minimal -> medium", modelGPT54, "minimal", "medium"},
		{"gpt-5.4 default when empty -> medium", modelGPT54, "", "medium"},
		{"gpt-5.4-mini allows xhigh", modelGPT54Mini, "xhigh", "xhigh"},
		{"gpt-5.4-mini default when empty -> medium", modelGPT54Mini, "", "medium"},
		{"gpt-5.5 allows xhigh", modelGPT55, "xhigh", "xhigh"},
		{"gpt-5.5 default -> medium", modelGPT55, "", "medium"},
		{"gpt-5.5 disallows max -> model default", modelGPT55, "max", "medium"},
		{"gpt-5.6-sol allows max", modelGPT5Sol, "max", "max"},
		{"gpt-5.6-sol rejects wire-unsupported ultra -> default", modelGPT5Sol, "ultra", "low"},
		{"gpt-5.6-sol default -> low", modelGPT5Sol, "", "low"},
		{"gpt-5.6-luna allows max", modelGPT5Luna, "max", "max"},
		{"gpt-5.6-terra rejects wire-unsupported ultra -> default", modelGPT5Terra, "ultra", "medium"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := clampReasoningEffortForModel(normalizeReasoningEffort(tc.inputEffort), tc.model)
			assert.Equal(t, tc.expected, actual)
		})
	}
}
