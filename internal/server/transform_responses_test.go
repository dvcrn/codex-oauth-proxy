package server

import (
	"reflect"
	"strings"
	"testing"
)

func TestResponsesRequestPreservesNames(t *testing.T) {
	const text = "Zed Cline Roo GitHub Copilot Copilot Cursor Microsoft"
	for _, instructions := range []string{"", "Keep the caller's instructions"} {
		t.Run(instructions, func(t *testing.T) {
			user := map[string]interface{}{"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "input_text", "text": text},
			}}
			toolResult := map[string]interface{}{
				"type": "function_call_output", "call_id": "call_example", "output": text,
			}
			body := map[string]interface{}{
				"instructions": instructions,
				"input": []interface{}{
					map[string]interface{}{"role": "system", "content": []interface{}{
						map[string]interface{}{"type": "input_text", "text": text},
					}},
					user, toolResult,
				},
			}
			transformResponsesRequestBody(body, "gpt-6.1-sol", "low", nil)
			want := []interface{}{
				map[string]interface{}{"role": "user", "content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": text},
				}},
				map[string]interface{}{"type": "function_call_output", "call_id": "call_example", "output": text},
			}
			if instructions == "" {
				if body["instructions"] != text {
					t.Fatalf("instructions = %q, want %q", body["instructions"], text)
				}
			} else {
				if body["instructions"] != instructions {
					t.Fatalf("instructions = %q, want %q", body["instructions"], instructions)
				}
				want = append([]interface{}{map[string]interface{}{"role": "developer", "content": text}}, want...)
			}
			if !reflect.DeepEqual(body["input"], want) {
				t.Fatalf("input = %#v, want %#v", body["input"], want)
			}
		})
	}
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

	normalizedModel, normalizedEffort := transformResponsesRequestBody(body, "gpt-5.6-sol", "none", nil)

	if normalizedModel != "gpt-5.6-sol" {
		t.Fatalf("expected normalized model gpt-5.6-sol, got %q", normalizedModel)
	}
	if normalizedEffort != "none" {
		t.Fatalf("expected normalized effort none, got %q", normalizedEffort)
	}

	instr, _ := body["instructions"].(string)
	if instr != "Please greet Zed." {
		t.Fatalf("instructions changed, got %q", instr)
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
			if txt, _ := item["text"].(string); txt == "Hello from Zed" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("expected to find unchanged user text 'Hello from Zed' in input messages; got %v", body["input"])
	}

	reasoning, ok := body["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning map to be present")
	}
	if reasoning["effort"] != "none" {
		t.Fatalf("expected reasoning effort none, got %v", reasoning["effort"])
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

func TestTransformResponsesStringInput(t *testing.T) {
	const instructions = "Keep the caller's text"
	for _, text := range []string{"Compare Microsoft and Cursor", "Describe the weather on Mars", "  Keep surrounding whitespace  "} {
		for _, explicitKey := range []string{"", "client-provided"} {
			t.Run(text+"/"+explicitKey, func(t *testing.T) {
				body := map[string]interface{}{"input": text, "instructions": instructions}
				if explicitKey != "" {
					body["prompt_cache_key"] = explicitKey
				}
				model, _ := transformResponsesRequestBody(body, "gpt-6.1-sol", "low", nil)
				want := []interface{}{map[string]interface{}{
					"role": "user", "content": []interface{}{
						map[string]interface{}{"type": "input_text", "text": text},
					},
				}}
				if !reflect.DeepEqual(body["input"], want) {
					t.Errorf("input = %#v, want %#v", body["input"], want)
				}
				if got := extractFirstUserText(body); got != text {
					t.Errorf("first user text = %q, want %q", got, text)
				}
				wantKey := explicitKey
				if wantKey == "" {
					wantKey = derivePromptCacheKey(model, instructions, text)
				}
				if body["prompt_cache_key"] != wantKey {
					t.Errorf("prompt_cache_key = %v, want %q", body["prompt_cache_key"], wantKey)
				}
				if body["store"] != false || body["stream"] != true {
					t.Error("Codex requests must disable storage and enable streaming")
				}
			})
		}
	}
}

func TestTransformResponsesRequestBodyGPT6Models(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-terra"} {
		t.Run(model, func(t *testing.T) {
			body := map[string]interface{}{
				"model": model + "-max",
				"input": []interface{}{map[string]interface{}{
					"role": "user", "content": "Reply OK.",
				}},
			}
			gotModel, gotEffort := transformResponsesRequestBody(body, model+"-max", "max", nil)
			if gotModel != model || body["model"] != model || gotEffort != "max" {
				t.Fatalf("model=%q body.model=%v effort=%q, want %q and max", gotModel, body["model"], gotEffort, model)
			}
		})
	}
}

func TestTransformResponsesRequestBody_ModelSpecificReasoningClamp(t *testing.T) {
	levels := []struct {
		Effort string `json:"effort"`
	}{{"low"}, {"medium"}, {"high"}, {"xhigh"}, {"max"}}
	catalog := modelCatalog{
		{Slug: "gpt-5.6-sol", DefaultReasoningLevel: "low", SupportedReasoningLevel: levels},
		{Slug: "gpt-5.4-mini", DefaultReasoningLevel: "medium", SupportedReasoningLevel: levels[:4]},
	}
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
	nModel1, nEffort1 := transformResponsesRequestBody(body1, "gpt-5.6-sol", "minimal", catalog)
	if nModel1 != "gpt-5.6-sol" {
		t.Fatalf("expected normalized model gpt-5.6-sol, got %q", nModel1)
	}
	if nEffort1 != "low" {
		t.Fatalf("expected normalized effort low, got %q", nEffort1)
	}
	reasoning1, ok := body1["reasoning"].(map[string]interface{})
	if !ok || reasoning1["effort"] != "low" {
		t.Fatalf("expected reasoning effort low in body, got %v", body1["reasoning"])
	}

	// Case 2: no effort provided defaults to model-specific default (medium)
	body2 := baseBody()
	nModel2, nEffort2 := transformResponsesRequestBody(body2, "gpt-5.4-mini", "", catalog)
	if nModel2 != "gpt-5.4-mini" {
		t.Fatalf("expected normalized model gpt-5.4-mini, got %q", nModel2)
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
	nModel3, nEffort3 := transformResponsesRequestBody(body3, "gpt-5.6-sol", "xhigh", catalog)
	if nModel3 != "gpt-5.6-sol" {
		t.Fatalf("expected normalized model gpt-5.6-sol, got %q", nModel3)
	}
	if nEffort3 != "xhigh" {
		t.Fatalf("expected normalized effort xhigh, got %q", nEffort3)
	}
	reasoning3, ok := body3["reasoning"].(map[string]interface{})
	if !ok || reasoning3["effort"] != "xhigh" {
		t.Fatalf("expected reasoning effort xhigh in body, got %v", body3["reasoning"])
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

	transformResponsesRequestBody(body, "gpt-6-luna", "medium", nil)

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

func TestTransformResponsesRequestBodyPreservesServiceTier(t *testing.T) {
	body := map[string]interface{}{
		"model":        "gpt-5",
		"service_tier": "priority",
	}
	transformResponsesRequestBody(body, "gpt-5", "", nil)

	if body["service_tier"] != "priority" {
		t.Fatalf("expected service_tier priority, got %v", body["service_tier"])
	}
}

func TestTransformResponsesRequestBodyDropsEmptyServiceTier(t *testing.T) {
	body := map[string]interface{}{
		"model":        "gpt-5",
		"service_tier": "",
	}
	transformResponsesRequestBody(body, "gpt-5", "", nil)

	if _, ok := body["service_tier"]; ok {
		t.Fatalf("expected service_tier to be dropped, got %v", body["service_tier"])
	}
}
