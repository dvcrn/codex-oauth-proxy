package server

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

var namesToReplace = []string{"Zed", "Cline", "Roo", "GitHub Copilot", "Copilot", "Cursor", "Microsoft", "Copilot"}

func replaceNames(input string) string {
	for _, name := range namesToReplace {
		input = strings.Replace(input, name, "Codex", -1)
	}
	return input
}

func transformSystemPrompt(requestData map[string]interface{}) ([]map[string]interface{}, error) {
	var messages []map[string]interface{}

	systemPromptRaw, exists := requestData["system"]
	if !exists || systemPromptRaw == nil {
		// No system prompt provided
		return []map[string]interface{}{}, nil
	}

	// Case 1: system prompt is a non-empty string
	if systemPrompt, ok := systemPromptRaw.(string); ok {
		trimmed := strings.TrimSpace(systemPrompt)
		if trimmed == "" {
			return []map[string]interface{}{}, nil
		}
		message := map[string]interface{}{
			"type": "text",
			"text": replaceNames(trimmed),
		}
		messages = append(messages, message)
	}

	// Case 2: system prompt is already an array of objects
	if systemPromptArray, ok := systemPromptRaw.([]interface{}); ok {
		for _, item := range systemPromptArray {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid system prompt item format")
			}

			// Ensure it has the required structure
			if _, hasType := itemMap["type"]; !hasType {
				itemMap["type"] = "text"
			}

			if _, hasText := itemMap["text"]; !hasText {
				return nil, fmt.Errorf("system prompt item missing text field")
			}

			itemMap["text"] = replaceNames(itemMap["text"].(string))

			// Preserve any existing cache_control as-is; do not add new ones
			messages = append(messages, itemMap)
		}
	}

	return messages, nil
}

// validateModel checks if the provided model is in the list of permitted models
// and returns the model if valid, or falls back to the first permitted model
// validateModel removed. We no longer rewrite models; upstream requires gpt-5.

func transformMessages(requestData map[string]interface{}) ([]interface{}, error) {
	amountOfEphemerals := 0
	transformedMessages := []interface{}{}

	messagesRaw, ok := requestData["messages"]
	if !ok {
		return nil, fmt.Errorf("no messages found")
	}

	messagesSlice, ok := messagesRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("messages field is not an array")
	}

	for _, msg := range messagesSlice {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		contentRaw, ok := msgMap["content"]
		if !ok {
			continue
		}
		contentSlice, ok := contentRaw.([]interface{})
		if !ok {
			continue
		}
		for _, contentItem := range contentSlice {
			contentItemMap, ok := contentItem.(map[string]interface{})
			if !ok {
				continue
			}
			// Replace names in text
			text, ok := contentItemMap["text"].(string)
			if ok {
				contentItemMap["text"] = replaceNames(text)
			}
			// Check for ephemeral cache_control
			if cacheControlRaw, hasCacheControl := contentItemMap["cache_control"]; hasCacheControl {
				cacheControlMap, ok := cacheControlRaw.(map[string]interface{})
				if ok {
					if cacheType, hasType := cacheControlMap["type"]; hasType && cacheType == "ephemeral" {
						amountOfEphemerals++
						if amountOfEphemerals > 2 {
							delete(contentItemMap, "cache_control")
						}
					}
				}
			}
		}

		transformedMessages = append(transformedMessages, msgMap)
	}

	return transformedMessages, nil
}

// buildCodexRequestBody transforms an OpenAI Chat Completions style request
// into the ChatGPT Codex backend body. This should be kept aligned with
// recorded requests under Raw_*/[11] Request - chatgpt.com_backend-api_codex_responses.txt
func buildCodexRequestBody(requestData map[string]interface{}) map[string]interface{} {
	instructions := extractInstructions(requestData)

	resolvedModel := resolveRequestModel(requestData)
	normalizedModel := normalizeModel(resolvedModel)
	body := map[string]interface{}{}
	body["model"] = normalizedModel
	body["instructions"] = instructions
	body["store"] = false
	body["stream"] = true

	// Build input messages array in codex format
	if inputMsgs := buildCodexInputMessages(requestData); len(inputMsgs) > 0 {
		body["input"] = inputMsgs
	}

	// Tools mapping (OpenAI tools -> Codex tools). Always include, even if empty.
	body["tools"] = mapToolsToCodex(requestData)

	// Tool choice
	if tc, ok := requestData["tool_choice"].(string); ok && tc != "" {
		body["tool_choice"] = tc
	} else {
		body["tool_choice"] = "auto"
	}

	// Parallel tool calls
	if ptc, ok := requestData["parallel_tool_calls"].(bool); ok {
		body["parallel_tool_calls"] = ptc
	} else {
		body["parallel_tool_calls"] = false
	}

	// Reasoning settings (default effort none -> medium equivalent)
	body["reasoning"] = buildReasoningSettings(requestData)

	// Include fields requested in capture
	body["include"] = []interface{}{"reasoning.encrypted_content"}

	if _, ok := body["prompt_cache_key"].(string); !ok {
		if key := derivePromptCacheKey(normalizedModel, instructions, extractFirstUserText(body)); key != "" {
			body["prompt_cache_key"] = key
		}
	}

	return body
}

// extractUserText concatenates user role message text to aid upstream mapping
func extractUserText(requestData map[string]interface{}) string {
	msgs, _ := requestData["messages"].([]interface{})
	var parts []string
	for _, m := range msgs {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		if role != "user" {
			continue
		}
		content := mm["content"]
		switch v := content.(type) {
		case string:
			if v != "" {
				parts = append(parts, v)
			}
		case []interface{}:
			for _, ci := range v {
				if cm, ok := ci.(map[string]interface{}); ok {
					if t, _ := cm["text"].(string); t != "" {
						parts = append(parts, t)
					}
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func extractInstructions(requestData map[string]interface{}) string {
	msgs, _ := requestData["messages"].([]interface{})
	var parts []string
	for _, m := range msgs {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		if role != "system" {
			continue
		}
		content := mm["content"]
		switch v := content.(type) {
		case string:
			if v != "" {
				parts = append(parts, replaceNames(v))
			}
		case []interface{}:
			var segs []string
			for _, ci := range v {
				if cm, ok := ci.(map[string]interface{}); ok {
					if t, _ := cm["text"].(string); t != "" {
						segs = append(segs, replaceNames(t))
					}
				}
			}
			if len(segs) > 0 {
				parts = append(parts, strings.Join(segs, "\n"))
			}
		}
	}

	// Prepend Codex CLI identity instructions as requested
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func resolveRequestModel(requestData map[string]interface{}) string {
	if model, ok := requestData["model"].(string); ok {
		model = strings.TrimSpace(model)
		if model != "" {
			return model
		}
	}
	return modelDefault
}

func normalizeModel(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	// Longest-first so "-xhigh" is not mistaken for "-high". Note this assumes
	// no served model's name ends in one of these words.
	for _, effort := range []string{"-minimal", "-medium", "-xhigh", "-high", "-none", "-low", "-max"} {
		if strings.HasSuffix(lower, effort) {
			lower = strings.TrimSuffix(lower, effort)
			break
		}
	}
	if lower == "" {
		return modelDefault
	}

	// Exact matches on currently-served models first, so a valid ID is never
	// rewritten by the looser prefix matching below.
	switch lower {
	case modelGPT53Spark, modelGPT54Mini, modelGPT55, modelGPT5Sol, modelGPT5Terra, modelGPT5Luna, modelGPT6Astra, modelDaybreakBlue:
		return lower
	}

	if strings.Contains(lower, "gpt-5.6-sol") {
		return modelGPT5Sol
	}
	if strings.Contains(lower, "gpt-5.6-terra") {
		return modelGPT5Terra
	}
	if strings.Contains(lower, "gpt-5.6-luna") {
		return modelGPT5Luna
	}
	if strings.Contains(lower, "daybreak") {
		return modelDaybreakBlue
	}
	if strings.Contains(lower, "gpt-5.5") {
		return modelGPT55
	}
	if strings.Contains(lower, "gpt-5.4-mini") {
		return modelGPT54Mini
	}
	if strings.Contains(lower, "gpt-6-astra") {
		return modelGPT6Astra
	}
	if strings.Contains(lower, "gpt-5.3-codex-spark") {
		return modelGPT53Spark
	}

	// Unrecognized models, including the retired GPT-5.0 to GPT-5.3 family,
	// map onto the current default so older callers keep working instead of
	// getting a hard 400 from the backend.
	if strings.Contains(lower, "mini") {
		return modelGPT54Mini
	}

	return modelDefault
}

func normalizeReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal":
		return "minimal"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	case "max":
		return "max"
	case "none":
		return "none"
	default:
		return ""
	}
}

func resolveReasoningEffort(requestData map[string]interface{}) string {
	if effort, ok := requestData["reasoning_effort"].(string); ok {
		effort = strings.TrimSpace(effort)
		if effort != "" {
			return effort
		}
	}
	if reasoningMap, ok := requestData["reasoning"].(map[string]interface{}); ok {
		if effort, ok := reasoningMap["effort"].(string); ok {
			effort = strings.TrimSpace(effort)
			if effort != "" {
				return effort
			}
		}
	}

	if model, ok := requestData["model"].(string); ok {
		lowerModel := strings.ToLower(strings.TrimSpace(model))
		for _, effort := range []string{"minimal", "medium", "xhigh", "high", "none", "low", "max"} {
			if strings.HasSuffix(lowerModel, "-"+effort) {
				return effort
			}
		}
	}

	return ""
}

func resolveReasoningSummary(requestData map[string]interface{}) interface{} {
	if reasoningMap, ok := requestData["reasoning"].(map[string]interface{}); ok {
		if summary, ok := reasoningMap["summary"]; ok {
			return summary
		}
	}
	return "auto"
}

func buildReasoningSettings(requestData map[string]interface{}) map[string]interface{} {
	requestedEffort := resolveReasoningEffort(requestData)
	normalizedEffort := normalizeReasoningEffort(requestedEffort)
	backendModel := normalizeModel(resolveRequestModel(requestData))
	clampedEffort := clampReasoningEffortForModel(normalizedEffort, backendModel)
	summary := resolveReasoningSummary(requestData)
	settings := map[string]interface{}{}
	if clampedEffort != "" {
		settings["effort"] = clampedEffort
	}
	if summary != nil {
		settings["summary"] = summary
	}
	return settings
}

func modelSupportsReasoningEffort(backendModel, effort string) bool {
	for _, allowedEffort := range modelAllowedEfforts[backendModel] {
		if effort == allowedEffort {
			return true
		}
	}
	return false
}

// clampReasoningEffortForModel enforces per-model reasoning effort limits and
// applies model-specific defaults when no explicit effort is provided.
func clampReasoningEffortForModel(effort, backendModel string) string {
	effort = strings.TrimSpace(effort)
	backendModel = strings.TrimSpace(backendModel)

	// If nothing specified, fall back to a model default (if any).
	if effort == "" {
		if def, ok := modelDefaultEffort[backendModel]; ok {
			return def
		}
		return ""
	}

	allowed, ok := modelAllowedEfforts[backendModel]
	if !ok || len(allowed) == 0 {
		return effort
	}
	if modelSupportsReasoningEffort(backendModel, effort) {
		return effort
	}

	if def, ok := modelDefaultEffort[backendModel]; ok && def != "" {
		return def
	}
	return effort
}

func derivePromptCacheKey(model, instructions, firstUserText string) string {
	model = strings.TrimSpace(model)
	instructions = strings.TrimSpace(instructions)
	firstUserText = strings.TrimSpace(firstUserText)
	if model == "" && instructions == "" && firstUserText == "" {
		return ""
	}
	payload := model + "\n" + instructions + "\n" + firstUserText
	sum := sha256.Sum256([]byte(payload))
	uuidBytes := make([]byte, 16)
	copy(uuidBytes, sum[:16])
	uuidBytes[6] = (uuidBytes[6] & 0x0f) | 0x50 // set version 5
	uuidBytes[8] = (uuidBytes[8] & 0x3f) | 0x80 // set variant 10
	return formatUUID(uuidBytes)
}

func formatUUID(b []byte) string {
	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf)
}

func extractFirstUserText(body map[string]interface{}) string {
	inputVal, ok := body["input"]
	if !ok {
		return ""
	}
	switch input := inputVal.(type) {
	case []interface{}:
		for _, entry := range input {
			entryMap, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := entryMap["role"].(string)
			if role != "user" {
				continue
			}
			if contentSlice, ok := entryMap["content"].([]interface{}); ok {
				for _, item := range contentSlice {
					if itemMap, ok := item.(map[string]interface{}); ok {
						if text, _ := itemMap["text"].(string); strings.TrimSpace(text) != "" {
							return replaceNames(text)
						}
					}
				}
			}
		}
	case []map[string]interface{}:
		for _, entryMap := range input {
			role, _ := entryMap["role"].(string)
			if role != "user" {
				continue
			}
			if contentSlice, ok := entryMap["content"].([]interface{}); ok {
				for _, item := range contentSlice {
					if itemMap, ok := item.(map[string]interface{}); ok {
						if text, _ := itemMap["text"].(string); strings.TrimSpace(text) != "" {
							return replaceNames(text)
						}
					}
				}
			}
		}
	}

	// Fallback for chat/completions style messages
	if msgs, ok := body["messages"].([]interface{}); ok {
		for _, msg := range msgs {
			mm, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := mm["role"].(string)
			if role != "user" {
				continue
			}
			switch v := mm["content"].(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					return replaceNames(v)
				}
			case []interface{}:
				for _, ci := range v {
					if cm, ok := ci.(map[string]interface{}); ok {
						if text, _ := cm["text"].(string); strings.TrimSpace(text) != "" {
							return replaceNames(text)
						}
					}
				}
			}
		}
	}

	return ""
}

// buildCodexInputMessages converts OpenAI messages to Codex "input" messages
func buildCodexInputMessages(requestData map[string]interface{}) []interface{} {
	systemPrompt := extractInstructions(requestData)

	msgs, _ := requestData["messages"].([]interface{})
	var input []interface{}
	if systemPrompt != "" {
		input = append(input, map[string]interface{}{
			"type": "message",
			"id":   nil,
			"role": "developer",
			"content": []interface{}{
				map[string]interface{}{
					"type": "input_text",
					"text": systemPrompt,
				},
			},
		})
	}

	for _, m := range msgs {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)

		switch role {
		case "user":
			contents := collectUserContent(mm["content"])
			if len(contents) == 0 {
				continue
			}
			input = append(input, map[string]interface{}{
				"type":    "message",
				"id":      mm["id"],
				"role":    "user",
				"content": contents,
			})
		case "assistant":
			texts := collectTextSegments(mm["content"], true)
			if len(texts) > 0 {
				contents := make([]interface{}, 0, len(texts))
				for _, t := range texts {
					contents = append(contents, map[string]interface{}{
						"type": "output_text",
						"text": t,
					})
				}
				input = append(input, map[string]interface{}{
					"type":    "message",
					"id":      mm["id"],
					"role":    "assistant",
					"content": contents,
				})
			}
			if toolCalls, ok := mm["tool_calls"].([]interface{}); ok {
				for _, tc := range toolCalls {
					tcm, ok := tc.(map[string]interface{})
					if !ok {
						continue
					}
					callID, _ := tcm["id"].(string)
					funcMap, _ := tcm["function"].(map[string]interface{})
					name, _ := funcMap["name"].(string)
					arguments := extractArgumentsString(funcMap["arguments"])
					input = append(input, map[string]interface{}{
						"type":      "function_call",
						"name":      name,
						"call_id":   callID,
						"arguments": arguments,
					})
				}
			}
		case "tool":
			callID, _ := mm["tool_call_id"].(string)
			if callID == "" {
				continue
			}
			output := collectToolOutput(mm["content"])
			input = append(input, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  output,
			})
		}
	}
	return input
}

func collectUserContent(content interface{}) []interface{} {
	switch value := content.(type) {
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil
		}
		return []interface{}{map[string]interface{}{
			"type": "input_text",
			"text": replaceNames(text),
		}}
	case []interface{}:
		contents := make([]interface{}, 0, len(value))
		for _, item := range value {
			part, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch partType, _ := part["type"].(string); partType {
			case "text", "input_text":
				text, _ := part["text"].(string)
				if text != "" {
					contents = append(contents, map[string]interface{}{
						"type": "input_text",
						"text": replaceNames(text),
					})
				}
			case "image_url":
				image, _ := part["image_url"].(map[string]interface{})
				url, _ := image["url"].(string)
				if url == "" {
					continue
				}
				inputImage := map[string]interface{}{
					"type":      "input_image",
					"image_url": url,
				}
				if detail, _ := image["detail"].(string); detail != "" {
					inputImage["detail"] = detail
				}
				contents = append(contents, inputImage)
			case "input_image":
				if url, _ := part["image_url"].(string); url != "" {
					contents = append(contents, part)
				}
			}
		}
		return contents
	default:
		return nil
	}
}

func collectTextSegments(content interface{}, applyReplace bool) []string {
	switch v := content.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil
		}
		if applyReplace {
			text = replaceNames(text)
		}
		return []string{text}
	case []interface{}:
		var texts []string
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			text, _ := m["text"].(string)
			if text == "" {
				continue
			}
			if applyReplace {
				text = replaceNames(text)
			}
			texts = append(texts, text)
		}
		return texts
	default:
		return nil
	}
}

func extractArgumentsString(arg interface{}) string {
	switch v := arg.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return ""
	}
}

func collectToolOutput(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			text, _ := m["text"].(string)
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return ""
	}
}

// mapToolsToCodex maps OpenAI tools (type:function) to Codex tools format
func mapToolsToCodex(requestData map[string]interface{}) []interface{} {
	toolsRaw, ok := requestData["tools"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]interface{}, 0, len(toolsRaw))
	for _, t := range toolsRaw {
		tm, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		if tm["type"] != "function" {
			continue
		}
		fn, _ := tm["function"].(map[string]interface{})
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params := fn["parameters"]
		out = append(out, map[string]interface{}{
			"type":        "function",
			"name":        name,
			"description": desc,
			"strict":      false,
			"parameters":  params,
		})
	}
	return out
}

// ===== SSE Response Transformation =====

type SSETransformer struct {
	model      string
	responseID string
	// roleSent indicates whether we've emitted the assistant role chunk yet (for either text or tool calls)
	roleSent bool
	// tool call tracking
	toolIndexByItemID map[string]int    // fc_* -> index in tool_calls
	toolIDByItemID    map[string]string // fc_* -> call_id (OpenAI id)
	toolNameByItemID  map[string]string // fc_* -> function name
	nextToolIndex     int
	// whether we saw any tool calls in this response (affects finish_reason)
	sawToolCalls bool
}

func NewSSETransformer(model string) *SSETransformer {
	model = strings.TrimSpace(model)
	if model == "" {
		model = modelDefault
	}
	return &SSETransformer{
		model:             model,
		toolIndexByItemID: make(map[string]int),
		toolIDByItemID:    make(map[string]string),
		toolNameByItemID:  make(map[string]string),
	}
}

func (t *SSETransformer) Transform(dataLine []byte) (out []byte, done bool, err error) {
	trimmed := bytes.TrimSpace(dataLine)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil, true, nil
	}

	// fmt.Println left here commented to avoid overwhelming logs with raw Codex events.
	// fmt.Println(string(dataLine))

	var upstream map[string]interface{}
	if err := json.Unmarshal(trimmed, &upstream); err != nil {
		return nil, false, fmt.Errorf("invalid upstream JSON chunk: %w", err)
	}

	eventType, _ := upstream["type"].(string)

	sendRole := func(seq interface{}) ([]byte, error) {
		if t.roleSent {
			return nil, nil
		}
		roleChunk := map[string]interface{}{
			"id":      t.responseID,
			"object":  "chat.completion.chunk",
			"created": seq,
			"model":   t.model,
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"delta": map[string]interface{}{
						"role": "assistant",
					},
					"finish_reason": nil,
				},
			},
		}
		b, err := json.Marshal(roleChunk)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal role chunk: %w", err)
		}
		t.roleSent = true
		return b, nil
	}

	if strings.HasPrefix(eventType, "response.reasoning") {
		// The upstream API can send multiple reasoning items (with incrementing
		// output_index) in a single response stream. This can result in multiple
		// "Thinking" bubbles appearing in the client UI for a single turn, which
		// can be confusing. To simplify the UI, we only process the first
		// reasoning item (output_index: 0) and explicitly ignore any subsequent
		// reasoning items in the same stream.
		if outputIndex, ok := upstream["output_index"].(float64); ok && outputIndex > 0 {
			return nil, false, nil
		}

		if !strings.Contains(eventType, ".delta") {
			return nil, false, nil
		}
		reasoningText := extractReasoningContent(upstream)
		if reasoningText == "" {
			return nil, false, nil
		}
		var chunks [][]byte
		if rb, err := sendRole(upstream["sequence_number"]); err != nil {
			return nil, false, err
		} else if len(rb) > 0 {
			chunks = append(chunks, rb)
		}
		reasoningChunk := map[string]interface{}{
			"id":      t.responseID,
			"object":  "chat.completion.chunk",
			"created": upstream["sequence_number"],
			"model":   t.model,
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"delta": map[string]interface{}{
						"reasoning_content": reasoningText,
					},
					"finish_reason": nil,
				},
			},
		}
		b, err := json.Marshal(reasoningChunk)
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal reasoning chunk: %w", err)
		}
		chunks = append(chunks, b)
		return bytes.Join(chunks, []byte("\n")), false, nil
	}

	switch eventType {
	case "response.created":
		if resp, ok := upstream["response"].(map[string]interface{}); ok {
			if id, ok := resp["id"].(string); ok {
				t.responseID = "chatcmpl-" + id
			}
		}
		return nil, false, nil

	case "response.output_item.added":
		// Start of a tool/function call
		item, _ := upstream["item"].(map[string]interface{})
		if item == nil {
			return nil, false, nil
		}
		if typ, _ := item["type"].(string); typ != "function_call" {
			return nil, false, nil
		}
		fcID, _ := item["id"].(string)        // fc_*
		callID, _ := item["call_id"].(string) // call_*
		name, _ := item["name"].(string)
		// assign tool index if first time
		idx, ok := t.toolIndexByItemID[fcID]
		if !ok {
			idx = t.nextToolIndex
			t.nextToolIndex++
			t.toolIndexByItemID[fcID] = idx
		}
		if callID == "" {
			// fall back to a synthetic id based on fc id
			callID = "call_" + fcID
		}
		t.toolIDByItemID[fcID] = callID
		t.toolNameByItemID[fcID] = name
		t.sawToolCalls = true

		var chunks [][]byte
		// Emit role if not yet sent
		if rb, err := sendRole(upstream["sequence_number"]); err != nil {
			return nil, false, err
		} else if len(rb) > 0 {
			chunks = append(chunks, rb)
		}
		// Emit initial tool_call delta with id, type and function name
		toolStart := map[string]interface{}{
			"id":      t.responseID,
			"object":  "chat.completion.chunk",
			"created": upstream["sequence_number"],
			"model":   t.model,
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"delta": map[string]interface{}{
						"tool_calls": []interface{}{
							map[string]interface{}{
								"index": idx,
								"id":    callID,
								"type":  "function",
								"function": map[string]interface{}{
									"name":      name,
									"arguments": "",
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		}
		b, err := json.Marshal(toolStart)
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal tool start chunk: %w", err)
		}
		chunks = append(chunks, b)
		return bytes.Join(chunks, []byte("\n")), false, nil

	case "response.function_call_arguments.delta":
		// Stream arguments for a given function call
		itemID, _ := upstream["item_id"].(string) // fc_*
		idx, ok := t.toolIndexByItemID[itemID]
		if !ok {
			return nil, false, nil
		}
		argDelta, _ := upstream["delta"].(string)
		var chunks [][]byte
		// Ensure role
		if rb, err := sendRole(upstream["sequence_number"]); err != nil {
			return nil, false, err
		} else if len(rb) > 0 {
			chunks = append(chunks, rb)
		}
		toolArgs := map[string]interface{}{
			"id":      t.responseID,
			"object":  "chat.completion.chunk",
			"created": upstream["sequence_number"],
			"model":   t.model,
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"delta": map[string]interface{}{
						"tool_calls": []interface{}{
							map[string]interface{}{
								"index": idx,
								"function": map[string]interface{}{
									"arguments": argDelta,
								},
							},
						},
					},
					"finish_reason": nil,
				},
			},
		}
		b, err := json.Marshal(toolArgs)
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal tool args chunk: %w", err)
		}
		chunks = append(chunks, b)
		return bytes.Join(chunks, []byte("\n")), false, nil

	case "response.function_call_arguments.done":
		// No specific emission needed; final finish will be sent on response.completed
		return nil, false, nil

	case "response.output_item.done":
		// Nothing to emit; could be used to track per-call completion if needed
		return nil, false, nil

	case "response.output_text.delta":
		var chunks [][]byte
		// Emit role if not yet sent
		if rb, err := sendRole(upstream["sequence_number"]); err != nil {
			return nil, false, err
		} else if len(rb) > 0 {
			chunks = append(chunks, rb)
		}
		// Send content delta
		delta, _ := upstream["delta"].(string)

		// Debug logging for whitespace content (disabled by default)
		// Uncomment for debugging whitespace issues:
		// if delta == "\n" || delta == "\r\n" || delta == " " || delta == "\t" {
		// 	fmt.Printf("[DEBUG] Whitespace delta detected: %q (len=%d)\n", delta, len(delta))
		// }

		contentChunk := map[string]interface{}{
			"id":      t.responseID,
			"object":  "chat.completion.chunk",
			"created": upstream["sequence_number"],
			"model":   t.model,
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"delta": map[string]interface{}{
						"content": delta,
					},
					"finish_reason": nil,
				},
			},
		}
		contentBytes, err := json.Marshal(contentChunk)
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal content chunk: %w", err)
		}
		chunks = append(chunks, contentBytes)
		return bytes.Join(chunks, []byte("\n")), false, nil

	case "response.completed":
		finish := "stop"
		if t.sawToolCalls {
			finish = "tool_calls"
		}

		// Map upstream usage (if present) into an OpenAI-style usage object.
		// Upstream usage is typically nested under response.usage with fields like
		// input_tokens / output_tokens / total_tokens. We convert these into
		// prompt_tokens / completion_tokens / total_tokens. If nothing is
		// available, fall back to zeros so clients that expect a usage object
		// (like Xcode) still see a well-formed structure.
		var usage map[string]interface{}
		if respObj, ok := upstream["response"].(map[string]interface{}); ok {
			if u, ok := respObj["usage"].(map[string]interface{}); ok {
				outUsage := map[string]interface{}{}
				if pt, ok := u["prompt_tokens"].(float64); ok {
					outUsage["prompt_tokens"] = int(pt)
				} else if it, ok := u["input_tokens"].(float64); ok {
					outUsage["prompt_tokens"] = int(it)
				}
				if ct, ok := u["completion_tokens"].(float64); ok {
					outUsage["completion_tokens"] = int(ct)
				} else if ot, ok := u["output_tokens"].(float64); ok {
					outUsage["completion_tokens"] = int(ot)
				}
				if tt, ok := u["total_tokens"].(float64); ok {
					outUsage["total_tokens"] = int(tt)
				} else {
					if ptVal, ok := outUsage["prompt_tokens"].(int); ok {
						if ctVal, ok2 := outUsage["completion_tokens"].(int); ok2 {
							outUsage["total_tokens"] = ptVal + ctVal
						}
					}
				}
				if len(outUsage) > 0 {
					usage = outUsage
				}
			}
		}
		if usage == nil {
			usage = map[string]interface{}{
				"prompt_tokens":     0,
				"completion_tokens": 0,
				"total_tokens":      0,
			}
		}

		finalChunk := map[string]interface{}{
			"id":      t.responseID,
			"object":  "chat.completion.chunk",
			"created": upstream["sequence_number"],
			"model":   t.model,
			"choices": []interface{}{
				map[string]interface{}{
					"index":         0,
					"delta":         map[string]interface{}{},
					"finish_reason": finish,
				},
			},
			"usage": usage,
		}
		finalBytes, err := json.Marshal(finalChunk)
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal final chunk: %w", err)
		}
		return finalBytes, false, nil

	default:
		// Ignore other event types
		return nil, false, nil
	}
}

// fixReasoningMarkdownHeaders ensures bold markdown headers in reasoning content
// have proper newlines before them for correct rendering (e.g., **Foo** -> \n\n**Foo**)
// Only injects newlines for complete bold headers within a single delta to avoid breaking
// formatting when upstream splits tokens across deltas.
func fixReasoningMarkdownHeaders(text string) string {
	if text == "" {
		return text
	}
	// Only inject newlines if this delta contains a complete bold header: starts with **
	// and contains a closing ** later in the same string. Ignore partials like "**" or "**Header"
	// to avoid adding newlines when upstream splits tokens across multiple deltas.
	if len(text) >= 4 && text[0] == '*' && text[1] == '*' {
		// Look for closing ** after the opening pair
		if strings.Contains(text[2:], "**") {
			// Complete header found, prepend newlines to ensure it renders on its own line
			return "\n\n" + text
		}
	}
	return text
}

func extractReasoningContent(evt map[string]interface{}) string {
	var content string
	if delta, _ := evt["delta"].(string); delta != "" {
		content = delta
	} else if text, _ := evt["text"].(string); text != "" {
		content = text
	} else if part, ok := evt["part"].(map[string]interface{}); ok {
		if t, _ := part["text"].(string); t != "" {
			content = t
		}
	} else if item, ok := evt["item"].(map[string]interface{}); ok {
		if encrypted, ok := item["encrypted_content"].(string); ok && encrypted != "" {
			return ""
		}
		if summaryArr, ok := item["summary"].([]interface{}); ok {
			for _, entry := range summaryArr {
				if sm, ok := entry.(map[string]interface{}); ok {
					if t, _ := sm["text"].(string); t != "" {
						content = t
						break
					}
				}
			}
		}
	} else if summaryArr, ok := evt["summary"].([]interface{}); ok {
		for _, entry := range summaryArr {
			if sm, ok := entry.(map[string]interface{}); ok {
				if t, _ := sm["text"].(string); t != "" {
					content = t
					break
				}
			}
		}
	}

	if content != "" {
		return fixReasoningMarkdownHeaders(content)
	}
	return ""
}

// TransformSSELine transforms a single SSE data payload line.
// - If payload is "[DONE]", returns done=true.
// - If payload is an OpenAI chat chunk (object == chat.completion.chunk), pass through unchanged.
// - Otherwise, interpret as Codex event and convert via SSETransformer.
func TransformSSELine(in []byte) (out []byte, done bool, err error) {
	trimmed := bytes.TrimSpace(in)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil, true, nil
	}
	// Detect OpenAI-style chunk
	var probe map[string]interface{}
	if err := json.Unmarshal(trimmed, &probe); err == nil {
		if obj, _ := probe["object"].(string); obj == "chat.completion.chunk" {
			return trimmed, false, nil
		}
	}
	// Fallback to Codex → OpenAI conversion
	tr := NewSSETransformer("")
	return tr.Transform(trimmed)
}

// RewriteSSEStream reads an upstream SSE stream and writes a transformed SSE
// stream to w. It expects lines in the form 'data: <json>\n' and blank lines
// separating events. The provided model is used when emitting OpenAI chunks.
// The function emits transformed lines preserving SSE framing and a terminal
// 'data: [DONE]\n\n'.
func RewriteSSEStream(r io.Reader, w io.Writer, model string) error {
	return RewriteSSEStreamWithCallback(r, w, model, nil)
}

// RewriteSSEStreamWithCallback aggregates multi-line data: blocks per SSE event,
// transforms each event, writes it out, and invokes onEvent for debug if set.
func RewriteSSEStreamWithCallback(r io.Reader, w io.Writer, model string, onEvent func(raw []byte, out []byte, done bool)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	transformer := NewSSETransformer(model)

	var dataLines [][]byte
	doneSeen := false
	flushEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		// Join multi-line data payload
		raw := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]

		out, done, err := transformer.Transform(raw)
		if onEvent != nil {
			onEvent(raw, out, done)
		}
		if err != nil {
			return err
		}
		if done {
			doneSeen = true
			if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
				return err
			}
			return nil
		}
		if len(out) > 0 {
			// Handle multi-line output from transform
			lines := bytes.Split(out, []byte("\n"))
			for _, line := range lines {
				if _, err := w.Write([]byte("data: ")); err != nil {
					return err
				}
				if _, err := w.Write(line); err != nil {
					return err
				}
				if _, err := w.Write([]byte("\n\n")); err != nil {
					return err
				}
			}
			return nil
		}

		// If no transformed output, pass through OpenAI chunks
		var probe map[string]interface{}
		if err := json.Unmarshal(bytes.TrimSpace(raw), &probe); err == nil {
			if obj, _ := probe["object"].(string); obj == "chat.completion.chunk" {
				if _, err := w.Write([]byte("data: ")); err != nil {
					return err
				}
				if _, err := w.Write(bytes.TrimSpace(raw)); err != nil {
					return err
				}
				if _, err := w.Write([]byte("\n\n")); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		// Blank line indicates end of current event
		if len(bytes.TrimSpace(line)) == 0 {
			if err := flushEvent(); err != nil {
				return err
			}
			continue
		}
		// Handle comment lines or fields
		if bytes.HasPrefix(line, []byte(":")) {
			// Ignore comments
			continue
		}
		// Accept both "data:" and "data: " prefixes
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimPrefix(line, []byte("data:"))
			// SSE spec allows optional single space after colon; trim only that
			if len(payload) > 0 && payload[0] == ' ' {
				payload = payload[1:]
			}
			// Accumulate for this event
			cp := make([]byte, len(payload))
			copy(cp, payload)
			dataLines = append(dataLines, cp)
		}
		// Other fields (event:, id:) are ignored for now.
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// Flush any trailing event without terminating blank line
	if err := flushEvent(); err != nil {
		return err
	}
	// Ensure downstream clients always see a DONE sentinel even if the upstream
	// stream omitted an explicit [DONE] event.
	if !doneSeen {
		if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
			return err
		}
	}
	return nil
}

// PassThroughSSEStream copies upstream SSE events directly to the downstream writer
// without any transformation.
func PassThroughSSEStream(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var dataLines [][]byte
	flushEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		raw := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]

		if bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
			if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
				return err
			}
			return nil
		}

		if len(raw) > 0 {
			if _, err := w.Write([]byte("data: ")); err != nil {
				return err
			}
			if _, err := w.Write(raw); err != nil {
				return err
			}
			if _, err := w.Write([]byte("\n\n")); err != nil {
				return err
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			if err := flushEvent(); err != nil {
				return err
			}
			continue
		}
		if bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimPrefix(line, []byte("data:"))
			// SSE spec allows optional single space after colon; trim only that
			if len(payload) > 0 && payload[0] == ' ' {
				payload = payload[1:]
			}
			cp := make([]byte, len(payload))
			copy(cp, payload)
			dataLines = append(dataLines, cp)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flushEvent()
}
