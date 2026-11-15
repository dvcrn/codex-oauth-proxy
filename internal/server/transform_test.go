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
		{"base model", "gpt-6-luna", "gpt-6-luna"},
		{"effort suffix low", "gpt-6-luna-low", "gpt-6-luna"},
		{"effort suffix medium", "gpt-6-luna-medium", "gpt-6-luna"},
		{"effort suffix high", "gpt-6-luna-high", "gpt-6-luna"},
		{"effort suffix xhigh", "gpt-6-luna-xhigh", "gpt-6-luna"},
		{"effort suffix max", "gpt-6.1-terra-max", "gpt-6.1-terra"},
		{"effort suffix none", "gpt-5.6-sol-none", "gpt-5.6-sol"},
		{"uppercase and whitespace", " GPT-6.1-TERRA-XHIGH ", "gpt-6.1-terra"},
		{"unknown model is not rewritten", "some-other-model", "some-other-model"},
		{"empty stays empty", "", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, normalizeModel(tc.input, nil))
		})
	}
}

func TestNormalizeModelKeepsCatalogModels(t *testing.T) {
	catalog := newModelCatalog([]upstreamModel{
		{Slug: "gpt-7-nova"},
		{Slug: "gpt-7-codex-max"},
	})
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{"listed model", "gpt-7-nova", "gpt-7-nova"},
		{"listed model with effort suffix", " GPT-7-NOVA-HIGH ", "gpt-7-nova"},
		{"listed slug ending in an effort word", "gpt-7-codex-max", "gpt-7-codex-max"},
		{"unlisted model is not rewritten", "gpt-7-luna-high", "gpt-7-luna"},
		{"empty stays empty", "", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, normalizeModel(tc.input, catalog))
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
		{"explicit none", "none", "none"},
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

func TestBuildCodexInputMessagesPreservesImageURLs(t *testing.T) {
	dataURL := "data:image/png;base64,AQID"
	request := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "describe this"},
					map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url":    dataURL,
							"detail": "high",
						},
					},
				},
			},
		},
	}

	input := buildCodexInputMessages(request)
	require.Len(t, input, 1)
	userMessage, ok := input[0].(map[string]interface{})
	require.True(t, ok)
	contents, ok := userMessage["content"].([]interface{})
	require.True(t, ok)
	require.Len(t, contents, 2)
	assert.Equal(t, map[string]interface{}{
		"type": "input_text",
		"text": "describe this",
	}, contents[0])
	assert.Equal(t, map[string]interface{}{
		"type":      "input_image",
		"image_url": dataURL,
		"detail":    "high",
	}, contents[1])
}

func TestBuildCodexRequestBodyUsesOnlyCallerInstructions(t *testing.T) {
	request := map[string]interface{}{
		"model": "gpt-6-luna",
		"messages": []interface{}{
			map[string]interface{}{"role": "system", "content": "Keep answers brief."},
			map[string]interface{}{"role": "user", "content": "Hello"},
		},
	}

	body := buildCodexRequestBody(request, nil)
	assert.Equal(t, "Keep answers brief.", body["instructions"])

	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "You are a coding agent running in the Codex CLI")
	assert.NotContains(t, string(encoded), "Priority: CRITICAL")
}

func TestBuildCodexRequestBodyOmitsSyntheticDeveloperMessage(t *testing.T) {
	request := map[string]interface{}{
		"model": "gpt-6-luna",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "Hello"},
		},
	}

	body := buildCodexRequestBody(request, nil)
	assert.Equal(t, "", body["instructions"])
	input, ok := body["input"].([]interface{})
	require.True(t, ok)
	require.Len(t, input, 1)
	message, ok := input[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "user", message["role"])
}

func TestCollectUserContentPreservesHTTPImageURL(t *testing.T) {
	contents := collectUserContent([]interface{}{
		map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]interface{}{
				"url": "https://example.com/image.png",
			},
		},
	})

	assert.Equal(t, []interface{}{map[string]interface{}{
		"type":      "input_image",
		"image_url": "https://example.com/image.png",
	}}, contents)
}

func TestClampReasoningEffortForCatalogModel(t *testing.T) {
	levels := []struct {
		Effort string `json:"effort"`
	}{{"low"}, {"medium"}, {"high"}, {"ultra"}}
	catalog := newModelCatalog([]upstreamModel{
		{Slug: "gpt-7-nova", DefaultReasoningLevel: "high", SupportedReasoningLevel: levels},
		{Slug: "gpt-7-ultra", DefaultReasoningLevel: "ultra", SupportedReasoningLevel: levels},
	})
	tests := []struct {
		name        string
		model       string
		inputEffort string
		expected    string
	}{
		{"upstream default when empty", "gpt-7-nova", "", "high"},
		{"listed effort kept", "gpt-7-nova", "low", "low"},
		{"unlisted effort -> upstream default", "gpt-7-nova", "max", "high"},
		{"wire-unsupported upstream default is dropped", "gpt-7-ultra", "", ""},
		{"unlisted model passes effort through", "gpt-unlisted", "medium", "medium"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, clampReasoningEffortForModel(tc.inputEffort, tc.model, catalog))
		})
	}
}

func TestBuildCodexRequestBodyForwardsServiceTier(t *testing.T) {
	body := buildCodexRequestBody(map[string]interface{}{
		"model":        "gpt-5",
		"service_tier": "priority",
		"messages":     []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	}, nil)

	if body["service_tier"] != "priority" {
		t.Fatalf("expected service_tier priority, got %v", body["service_tier"])
	}
}

func TestBuildCodexRequestBodyOmitsEmptyServiceTier(t *testing.T) {
	body := buildCodexRequestBody(map[string]interface{}{
		"model":        "gpt-5",
		"service_tier": "   ",
		"messages":     []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	}, nil)

	if _, ok := body["service_tier"]; ok {
		t.Fatalf("expected service_tier to be omitted, got %v", body["service_tier"])
	}
}
