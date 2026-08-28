package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

type responsesRetryHTTPClient struct {
	responses     []*http.Response
	requestBodies [][]byte
}

func (client *responsesRetryHTTPClient) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	client.requestBodies = append(client.requestBodies, body)

	response := client.responses[0]
	client.responses = client.responses[1:]
	return response, nil
}

func TestTransformResponsesRequestBody(t *testing.T) {
	body := map[string]interface{}{
		"instructions": "Please greet Zed.",
		"input": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "input_text",
						"text": "Hello from Zed",
					},
				},
			},
		},
		"reasoning_effort": "none",
	}

	normalizedModel, normalizedEffort := transformResponsesRequestBody(body, "gpt-5-codex-preview", "none")

	if normalizedModel != modelDefault {
		t.Fatalf("expected normalized model %s, got %q", modelDefault, normalizedModel)
	}
	if normalizedEffort != "low" {
		t.Fatalf("expected normalized effort low, got %q", normalizedEffort)
	}

	instr, _ := body["instructions"].(string)
	if instr == "" || containsSubstring(instr, "Please greet Codex.") {
		t.Fatalf("instructions should be canonical prefix and not include user text, got %q", instr)
	}

	input := body["input"].([]interface{})
	found := false
	for _, it := range input {
		msg, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := msg["content"].([]interface{})
		if !ok || len(content) == 0 {
			continue
		}
		if item, ok := content[0].(map[string]interface{}); ok {
			if txt, _ := item["text"].(string); txt == "Hello from Codex" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("expected to find replaced user text 'Hello from Codex' in input messages; got %v", body["input"])
	}

	reasoning, ok := body["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning map to be present")
	}
	if reasoning["effort"] != "low" {
		t.Fatalf("expected reasoning effort low, got %v", reasoning["effort"])
	}

	if store, ok := body["store"].(bool); !ok || store {
		t.Fatalf("expected store to be false, got %v", body["store"])
	}

	include, ok := body["include"].([]interface{})
	if !ok || len(include) == 0 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected include to contain reasoning.encrypted_content, got %v", body["include"])
	}

	if _, exists := body["max_output_tokens"]; exists {
		t.Fatalf("expected max_output_tokens to be removed")
	}
	if _, exists := body["max_tokens"]; exists {
		t.Fatalf("expected max_tokens to be removed")
	}

	if _, exists := body["reasoning_effort"]; exists {
		t.Fatalf("expected reasoning_effort to be removed")
	}

	if body["tool_choice"] != "auto" {
		t.Fatalf("expected tool_choice to default to auto, got %v", body["tool_choice"])
	}
	if body["parallel_tool_calls"] != false {
		t.Fatalf("expected parallel_tool_calls to default to false, got %v", body["parallel_tool_calls"])
	}

	inputMessages := body["input"].([]interface{})
	for idx, item := range inputMessages {
		if _, ok := item.([]interface{}); ok {
			t.Fatalf("input[%d] should be a message map, found nested array", idx)
		}
	}
}

func containsSubstring(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestTransformResponsesRequestBody_ModelSpecificReasoningClamp(t *testing.T) {
	// Mini codex should clamp low effort to medium and default to medium when
	// no explicit effort is provided.
	baseBody := func() map[string]interface{} {
		return map[string]interface{}{
			"instructions": "Do something.",
			"input": []interface{}{
				map[string]interface{}{
					"role": "user",
					"content": []interface{}{
						map[string]interface{}{
							"type": "input_text",
							"text": "Hello",
						},
					},
				},
			},
		}
	}

	// Case 1: an effort the model does not support falls back to its default.
	body1 := baseBody()
	requestedEffort1 := "minimal"
	nModel1, nEffort1 := transformResponsesRequestBody(body1, modelGPT5Sol, requestedEffort1)
	if nModel1 != modelGPT5Sol {
		t.Fatalf("expected normalized model %s, got %q", modelGPT5Sol, nModel1)
	}
	if nEffort1 != "low" {
		t.Fatalf("expected normalized effort low, got %q", nEffort1)
	}
	reasoning1, ok := body1["reasoning"].(map[string]interface{})
	if !ok || reasoning1["effort"] != "low" {
		t.Fatalf("expected reasoning effort medium in body, got %v", body1["reasoning"])
	}

	// Case 2: no effort provided defaults to model-specific default (medium)
	body2 := baseBody()
	nModel2, nEffort2 := transformResponsesRequestBody(body2, modelGPT54Mini, "")
	if nModel2 != modelGPT54Mini {
		t.Fatalf("expected normalized model %s, got %q", modelGPT54Mini, nModel2)
	}
	if nEffort2 != "medium" {
		t.Fatalf("expected normalized effort medium, got %q", nEffort2)
	}
	reasoning2, ok := body2["reasoning"].(map[string]interface{})
	if !ok || reasoning2["effort"] != "medium" {
		t.Fatalf("expected reasoning effort medium in body, got %v", body2["reasoning"])
	}

	// Case 3: gpt-5.6-sol preserves xhigh and defaults to low when unspecified
	body3 := baseBody()
	requestedEffort3 := "xhigh"
	nModel3, nEffort3 := transformResponsesRequestBody(body3, modelGPT5Sol, requestedEffort3)
	if nModel3 != modelGPT5Sol {
		t.Fatalf("expected normalized model %s, got %q", modelGPT5Sol, nModel3)
	}
	if nEffort3 != "xhigh" {
		t.Fatalf("expected normalized effort xhigh, got %q", nEffort3)
	}
	reasoning3, ok := body3["reasoning"].(map[string]interface{})
	if !ok || reasoning3["effort"] != "xhigh" {
		t.Fatalf("expected reasoning effort xhigh in body, got %v", body3["reasoning"])
	}

	body4 := baseBody()
	nModel4, nEffort4 := transformResponsesRequestBody(body4, modelGPT5Sol, "")
	if nModel4 != modelGPT5Sol {
		t.Fatalf("expected normalized model %s, got %q", modelGPT5Sol, nModel4)
	}
	if nEffort4 != "low" {
		t.Fatalf("expected normalized effort low when unspecified, got %q", nEffort4)
	}
	reasoning4, ok := body4["reasoning"].(map[string]interface{})
	if !ok || reasoning4["effort"] != "low" {
		t.Fatalf("expected reasoning effort low in body, got %v", body4["reasoning"])
	}
}

func TestTransformResponsesRequestBody_PreservesPortableResponseState(t *testing.T) {
	longForeignID := strings.Repeat("grok-session-item-", 5)
	validChatGPTID := "rs_chatgpt_reasoning"
	body := map[string]interface{}{
		"input": []interface{}{
			map[string]interface{}{
				"type": "message",
				"id":   longForeignID,
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "input_text",
						"text": "Continue this session",
					},
				},
			},
			map[string]interface{}{
				"type":              "reasoning",
				"id":                validChatGPTID,
				"encrypted_content": "gAAAAAB-chatgpt-private-reasoning",
			},
			map[string]interface{}{
				"type":      "function_call",
				"id":        longForeignID,
				"call_id":   "call_keep_this",
				"name":      "lookup",
				"arguments": "{}",
			},
			map[string]interface{}{
				"type":    "function_call_output",
				"id":      longForeignID,
				"call_id": "call_keep_this",
				"output":  "done",
			},
		},
	}

	transformResponsesRequestBody(body, modelGPT55, "medium")

	input := body["input"].([]interface{})
	if len(input) != 4 {
		t.Fatalf("expected all response items to be preserved, got %d input items", len(input))
	}
	for idx, rawItem := range input {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			t.Fatalf("input[%d] should be a map, got %T", idx, rawItem)
		}
		if id, exists := item["id"]; exists && len(id.(string)) > maxResponsesInputItemIDLength {
			t.Fatalf("input[%d] should not preserve foreign id %q", idx, item["id"])
		}
	}

	reasoning := input[1].(map[string]interface{})
	if reasoning["id"] != validChatGPTID || reasoning["encrypted_content"] == "" {
		t.Fatalf("expected ChatGPT reasoning state to be preserved, got %v", reasoning)
	}

	functionCall := input[2].(map[string]interface{})
	if functionCall["call_id"] != "call_keep_this" {
		t.Fatalf("expected function call_id to be preserved, got %v", functionCall["call_id"])
	}
	functionOutput := input[3].(map[string]interface{})
	if functionOutput["call_id"] != "call_keep_this" {
		t.Fatalf("expected function output call_id to be preserved, got %v", functionOutput["call_id"])
	}
}

func TestRemoveEncryptedReasoningInput(t *testing.T) {
	body := map[string]interface{}{
		"input": []interface{}{
			map[string]interface{}{
				"type":              "reasoning",
				"id":                "rs_foreign",
				"encrypted_content": "ClOk-foreign-private-reasoning",
			},
			map[string]interface{}{
				"type":    "reasoning",
				"id":      "rs_summary_only",
				"summary": []interface{}{},
			},
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_keep_this",
				"name":      "lookup",
				"arguments": "{}",
			},
		},
	}

	removed := removeEncryptedReasoningInput(body)
	if removed != 1 {
		t.Fatalf("expected one encrypted reasoning item removed, got %d", removed)
	}

	input := body["input"].([]interface{})
	if len(input) != 2 {
		t.Fatalf("expected two portable items to remain, got %d", len(input))
	}
	if input[0].(map[string]interface{})["id"] != "rs_summary_only" {
		t.Fatalf("expected non-encrypted reasoning item to remain, got %v", input[0])
	}
	if input[1].(map[string]interface{})["call_id"] != "call_keep_this" {
		t.Fatalf("expected function call to remain, got %v", input[1])
	}
}

func TestResponsesHandler_RetriesUndecryptableReasoning(t *testing.T) {
	client := &responsesRetryHTTPClient{
		responses: []*http.Response{
			{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"code":"invalid_encrypted_content","message":"could not decrypt"}}`,
				)),
			},
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n",
				)),
			},
		},
	}
	server := New(zerolog.Nop(), stubCredentialsFetcher{})
	server.httpClient = client

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5.5",
		"input":[
			{"type":"reasoning","id":"rs_foreign","encrypted_content":"ClOk-foreign"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`))
	recorder := httptest.NewRecorder()

	server.responsesHandler(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected compatibility retry to succeed, got status %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(client.requestBodies) != 2 {
		t.Fatalf("expected two upstream requests, got %d", len(client.requestBodies))
	}
	if !strings.Contains(string(client.requestBodies[0]), `"encrypted_content":"ClOk-foreign"`) {
		t.Fatalf("expected initial request to preserve encrypted reasoning, got %s", client.requestBodies[0])
	}
	if strings.Contains(string(client.requestBodies[1]), `"encrypted_content":"ClOk-foreign"`) {
		t.Fatalf("expected retry to remove encrypted reasoning, got %s", client.requestBodies[1])
	}
	if !strings.Contains(string(client.requestBodies[1]), `"text":"continue"`) {
		t.Fatalf("expected retry to preserve conversation input, got %s", client.requestBodies[1])
	}
}
