package server

import "strings"

const maxResponsesInputItemIDLength = 64

func transformResponsesRequestBody(body map[string]interface{}, requestedModel string, requestedEffort string) (string, string) {
	normalizedModel := normalizeModel(requestedModel)
	body["model"] = normalizedModel

	// Responses must always disable server-side store per upstream requirements
	body["store"] = false

	var userInstr string
	if existingInstr, ok := body["instructions"].(string); ok {
		userInstr = strings.TrimSpace(existingInstr)
		delete(body, "instructions")
	}

	var systemText string
	var allInstructions []interface{}
	if existingInput, ok := body["input"]; ok {
		if inSlice, ok := existingInput.([]interface{}); ok {
			for _, msg := range inSlice {
				msgMap, ok := msg.(map[string]interface{})
				if !ok {
					allInstructions = append(allInstructions, msg)
					continue
				}
				if role, _ := msgMap["role"].(string); role == "system" {
					var parts []string
					if contents, ok := msgMap["content"].([]interface{}); ok {
						for _, item := range contents {
							if itemMap, ok := item.(map[string]interface{}); ok {
								if text, ok := itemMap["text"].(string); ok && text != "" {
									parts = append(parts, text)
								}
							}
						}
					}
					systemText = strings.Join(parts, "\n\n")
				} else {
					allInstructions = append(allInstructions, msg)
				}
			}
		}
	}

	if userInstr != "" && systemText != "" {
		body["instructions"] = userInstr
		developerMsg := map[string]interface{}{
			"role":    "developer",
			"content": replaceNames(systemText),
		}
		allInstructions = append([]interface{}{developerMsg}, allInstructions...)
	} else if userInstr != "" {
		body["instructions"] = userInstr
	} else if systemText != "" {
		body["instructions"] = replaceNames(systemText)
	} else {
		body["instructions"] = ""
	}

	body["input"] = allInstructions

	sanitizeResponsesInput(body)

	// Always request reasoning encrypted content to match Codex expectations
	body["include"] = []interface{}{"reasoning.encrypted_content"}

	// Ensure tool choice and parallel tool calls defaults
	if _, ok := body["tool_choice"]; !ok {
		body["tool_choice"] = "auto"
	}
	if _, ok := body["parallel_tool_calls"]; !ok {
		body["parallel_tool_calls"] = false
	}

	// Re-map max_output_tokens to max_tokens for upstream compatibility
	if _, ok := body["max_output_tokens"]; ok {
		delete(body, "max_output_tokens")
	}
	if _, ok := body["max_tokens"]; ok {
		delete(body, "max_tokens")
	}

	normalizedEffort := normalizeReasoningEffort(requestedEffort)
	clampedEffort := clampReasoningEffortForModel(normalizedEffort, normalizedModel)
	summary := resolveReasoningSummary(body)
	reasoningSettings := map[string]interface{}{}
	if summary != nil {
		reasoningSettings["summary"] = summary
	}
	if clampedEffort != "" {
		reasoningSettings["effort"] = clampedEffort
	}
	if len(reasoningSettings) > 0 {
		body["reasoning"] = reasoningSettings
	} else {
		delete(body, "reasoning")
	}

	delete(body, "reasoning_effort")

	if _, ok := body["prompt_cache_key"].(string); !ok {
		instructions, _ := body["instructions"].(string)
		firstText := extractFirstUserText(body)
		if key := derivePromptCacheKey(normalizedModel, instructions, firstText); key != "" {
			body["prompt_cache_key"] = key
		}
	}

	return normalizedModel, clampedEffort
}

func sanitizeResponsesInput(body map[string]interface{}) {
	input, ok := body["input"].([]interface{})
	if !ok {
		return
	}
	filtered := input[:0:0]
	for _, msg := range input {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			filtered = append(filtered, msg)
			continue
		}
		if role, _ := msgMap["role"].(string); role == "system" {
			continue
		}
		if id, ok := msgMap["id"].(string); ok && len(id) > maxResponsesInputItemIDLength {
			delete(msgMap, "id")
		}
		contents, ok := msgMap["content"].([]interface{})
		if !ok {
			filtered = append(filtered, msg)
			continue
		}
		for _, item := range contents {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if text, ok := itemMap["text"].(string); ok && text != "" {
				itemMap["text"] = replaceNames(text)
			}
		}
		filtered = append(filtered, msg)
	}
	body["input"] = filtered
}

func removeEncryptedReasoningInput(body map[string]interface{}) int {
	input, ok := body["input"].([]interface{})
	if !ok {
		return 0
	}

	removed := 0
	filtered := input[:0:0]
	for _, item := range input {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			filtered = append(filtered, item)
			continue
		}
		itemType, _ := itemMap["type"].(string)
		_, hasEncryptedContent := itemMap["encrypted_content"]
		if itemType == "reasoning" && hasEncryptedContent {
			removed++
			continue
		}
		filtered = append(filtered, item)
	}
	body["input"] = filtered

	return removed
}
