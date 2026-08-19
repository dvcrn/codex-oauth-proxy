package server

const (
	modelGPT5            = "gpt-5"
	modelGPT5Codex       = "gpt-5-codex"
	modelGPT51           = "gpt-5.1"
	modelGPT51Codex      = "gpt-5.1-codex"
	modelGPT51CodexMax   = "gpt-5.1-codex-max"
	modelGPT52           = "gpt-5.2"
	modelGPT54           = "gpt-5.4"
	modelGPT52Codex      = "gpt-5.2-codex"
	modelGPT53Codex      = "gpt-5.3-codex"
	modelGPT53CodexSpark = "gpt-5.3-codex-spark"
	modelGPT55           = "gpt-5.5"
	modelGPT5Sol         = "gpt-5.6-sol"

	modelGPT5CodexMini  = "gpt-5-codex-mini"
	modelGPT51CodexMini = "gpt-5.1-codex-mini"
)

// modelAllowedEfforts defines which reasoning effort levels are valid for each
// canonical backend model. Keys are canonical model IDs used in upstream
// requests (after normalization).
var modelAllowedEfforts = map[string][]string{
	modelGPT5:            {"minimal", "low", "medium", "high"},
	modelGPT52:           {"low", "medium", "high", "xhigh"},
	modelGPT54:           {"low", "medium", "high", "xhigh"},
	modelGPT52Codex:      {"low", "medium", "high", "xhigh"},
	modelGPT53Codex:      {"low", "medium", "high", "xhigh"},
	modelGPT53CodexSpark: {"low", "medium", "high", "xhigh"},
	modelGPT55:           {"low", "medium", "high", "xhigh"},
	modelGPT5Sol:         {"low", "medium", "high", "xhigh"},
	modelGPT5Codex:       {"minimal", "low", "medium", "high"},
	modelGPT51:           {"low", "medium", "high"},
	modelGPT51Codex:      {"low", "medium", "high"},
	modelGPT51CodexMax:   {"low", "medium", "high", "xhigh"},
	modelGPT5CodexMini:   {"medium", "high"},
	modelGPT51CodexMini:  {"medium", "high"},
}

// modelDefaultEffort defines the default reasoning effort to apply when the
// user does not explicitly specify an effort for the given model.
var modelDefaultEffort = map[string]string{
	modelGPT51:           "low",
	modelGPT52:           "medium",
	modelGPT54:           "medium",
	modelGPT52Codex:      "medium",
	modelGPT53Codex:      "medium",
	modelGPT53CodexSpark: "high",
	modelGPT55:           "medium",
	modelGPT5Sol:         "medium",
	modelGPT51Codex:      "low",
	modelGPT51CodexMax:   "low",
	modelGPT5CodexMini:   "medium",
	modelGPT51CodexMini:  "medium",
}

// modelMetadata mirrors the JSON schema required by the OpenAI-compatible
// /v1/models endpoint. The structure intentionally keeps nested fields as
// generic maps to simplify aligning with the upstream payload shape and to
// avoid bespoke types for every nested object.
type modelMetadata struct {
	Capabilities        map[string]interface{} `json:"capabilities"`
	ID                  string                 `json:"id"`
	ModelPickerCategory string                 `json:"model_picker_category,omitempty"`
	ModelPickerEnabled  bool                   `json:"model_picker_enabled"`
	Name                string                 `json:"name"`
	Object              string                 `json:"object"`
	Policy              *modelPolicy           `json:"policy,omitempty"`
	Preview             bool                   `json:"preview"`
	SupportedEndpoints  []string               `json:"supported_endpoints,omitempty"`
	Vendor              string                 `json:"vendor"`
	Version             string                 `json:"version"`
}

type modelPolicy struct {
	State string `json:"state"`
	Terms string `json:"terms"`
}

type modelsResponse struct {
	Object string          `json:"object"`
	Data   []modelMetadata `json:"data"`
}

var modelMetadataByID = map[string]modelMetadata{
	modelGPT5: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 264000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT5,
		ModelPickerCategory: "versatile",
		ModelPickerEnabled:  true,
		Name:                "GPT-5",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to the latest GPT-5 model from OpenAI. [Learn more about how GitHub Copilot serves GPT-5](https://gh.io/copilot-openai).",
		},
		Preview: false,
		Vendor:  "Azure OpenAI",
		Version: "gpt-5",
	},
	modelGPT52: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.2",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 264000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT52,
		ModelPickerCategory: "versatile",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.2",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.2 from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.2](https://gh.io/copilot-openai).",
		},
		Preview: false,
		Vendor:  "Azure OpenAI",
		Version: "gpt-5.2",
	},
	modelGPT54: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.4",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 264000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT54,
		ModelPickerCategory: "versatile",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.4",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.4 from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.4](https://gh.io/copilot-openai).",
		},
		Preview: false,
		Vendor:  "Azure OpenAI",
		Version: "gpt-5.4",
	},
	modelGPT52Codex: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.2-codex",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 200000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT52Codex,
		ModelPickerCategory: "powerful",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.2-Codex (Preview)",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.2-Codex from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.2-Codex](https://gh.io/copilot-openai).",
		},
		Preview:            true,
		SupportedEndpoints: []string{"/responses"},
		Vendor:             "OpenAI",
		Version:            "gpt-5.2-codex",
	},
	modelGPT53Codex: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.3-codex",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 200000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT53Codex,
		ModelPickerCategory: "powerful",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.3-Codex (Preview)",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.3-Codex from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.3-Codex](https://gh.io/copilot-openai).",
		},
		Preview:            true,
		SupportedEndpoints: []string{"/responses"},
		Vendor:             "OpenAI",
		Version:            "gpt-5.3-codex",
	},
	modelGPT53CodexSpark: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.3-codex-spark",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 200000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT53CodexSpark,
		ModelPickerCategory: "powerful",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.3-Codex Spark (Preview)",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.3-Codex Spark from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.3-Codex Spark](https://gh.io/copilot-openai).",
		},
		Preview:            true,
		SupportedEndpoints: []string{"/responses"},
		Vendor:             "OpenAI",
		Version:            "gpt-5.3-codex-spark",
	},
	modelGPT55: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.5",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 1050000,
				"max_output_tokens":         128000,
				"max_prompt_tokens":         922000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT55,
		ModelPickerCategory: "versatile",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.5",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.5 from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.5](https://gh.io/copilot-openai).",
		},
		Preview: false,
		Vendor:  "OpenAI",
		Version: "gpt-5.5",
	},
	modelGPT5Sol: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.6-sol",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 1050000,
				"max_output_tokens":         128000,
				"max_prompt_tokens":         922000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT5Sol,
		ModelPickerCategory: "powerful",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.6 Sol",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "GPT-5.6 Sol via ChatGPT Codex subscription.",
		},
		Preview:            true,
		SupportedEndpoints: []string{"/responses"},
		Vendor:             "OpenAI",
		Version:            "gpt-5.6-sol",
	},
	modelGPT5Codex: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5-codex",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 200000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT5Codex,
		ModelPickerCategory: "powerful",
		ModelPickerEnabled:  true,
		Name:                "GPT-5-Codex (Preview)",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to the latest GPT-5-Codex model from OpenAI. [Learn more about how GitHub Copilot serves GPT-5-Codex](https://gh.io/copilot-openai).",
		},
		Preview:            true,
		SupportedEndpoints: []string{"/responses"},
		Vendor:             "OpenAI",
		Version:            "gpt-5-codex",
	},
	modelGPT51: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.1",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 264000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT51,
		ModelPickerCategory: "versatile",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.1",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.1 from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.1](https://gh.io/copilot-openai).",
		},
		Preview: false,
		Vendor:  "Azure OpenAI",
		Version: "gpt-5.1",
	},
	modelGPT51Codex: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.1-codex",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 200000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT51Codex,
		ModelPickerCategory: "powerful",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.1-Codex (Preview)",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.1-Codex from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.1-Codex](https://gh.io/copilot-openai).",
		},
		Preview:            true,
		SupportedEndpoints: []string{"/responses"},
		Vendor:             "OpenAI",
		Version:            "gpt-5.1-codex",
	},
	modelGPT51CodexMax: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.1-codex-max",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 200000,
				"max_output_tokens":         64000,
				"max_prompt_tokens":         128000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT51CodexMax,
		ModelPickerCategory: "powerful",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.1-Codex Max (Preview)",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.1-Codex Max from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.1-Codex Max](https://gh.io/copilot-openai).",
		},
		Preview:            true,
		SupportedEndpoints: []string{"/responses"},
		Vendor:             "OpenAI",
		Version:            "gpt-5.1-codex-max",
	},
	modelGPT5CodexMini: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5-codex-mini",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 128000,
				"max_output_tokens":         32000,
				"max_prompt_tokens":         64000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT5CodexMini,
		ModelPickerCategory: "fast",
		ModelPickerEnabled:  true,
		Name:                "GPT-5-Codex Mini (Preview)",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5-Codex Mini from OpenAI. [Learn more about how GitHub Copilot serves GPT-5-Codex Mini](https://gh.io/copilot-openai).",
		},
		Preview:            true,
		SupportedEndpoints: []string{"/responses"},
		Vendor:             "OpenAI",
		Version:            "gpt-5-codex-mini",
	},
	modelGPT51CodexMini: {
		Capabilities: map[string]interface{}{
			"family": "gpt-5.1-codex-mini",
			"limits": map[string]interface{}{
				"max_context_window_tokens": 128000,
				"max_output_tokens":         32000,
				"max_prompt_tokens":         64000,
				"vision": map[string]interface{}{
					"max_prompt_image_size": 3145728,
					"max_prompt_images":     1,
					"supported_media_types": []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
				},
			},
			"object":    "model_capabilities",
			"supports":  map[string]interface{}{"parallel_tool_calls": true, "streaming": true, "structured_outputs": true, "tool_calls": true, "vision": true},
			"tokenizer": "o200k_base",
			"type":      "chat",
		},
		ID:                  modelGPT51CodexMini,
		ModelPickerCategory: "fast",
		ModelPickerEnabled:  true,
		Name:                "GPT-5.1-Codex Mini (Preview)",
		Object:              "model",
		Policy: &modelPolicy{
			State: "enabled",
			Terms: "Enable access to GPT-5.1-Codex Mini from OpenAI. [Learn more about how GitHub Copilot serves GPT-5.1-Codex Mini](https://gh.io/copilot-openai).",
		},
		Preview:            true,
		SupportedEndpoints: []string{"/responses"},
		Vendor:             "OpenAI",
		Version:            "gpt-5.1-codex-mini",
	},
}

var supportedModelIDs = []string{
	modelGPT5,
	modelGPT52,
	modelGPT54,
	modelGPT52Codex,
	modelGPT53Codex,
	modelGPT53CodexSpark,
	modelGPT55,
	modelGPT5Sol,
	modelGPT5Codex,
	modelGPT51,
	modelGPT51Codex,
	modelGPT51CodexMax,
	modelGPT5CodexMini,
	modelGPT51CodexMini,
}

func supportedModels() []modelMetadata {
	models := make([]modelMetadata, 0, len(supportedModelIDs))
	for _, id := range supportedModelIDs {
		base, ok := modelMetadataByID[id]
		if !ok {
			continue
		}
		// Always include the base model
		models = append(models, base)

		// Also expose reasoning-effort suffix variants (e.g., gpt-5-high) so
		// clients that encode effort in the model name can discover them from
		// /v1/models.
		if efforts, ok := modelAllowedEfforts[id]; ok {
			for _, effort := range efforts {
				variant := base
				variant.ID = id + "-" + effort
				variant.Name = base.Name + " (" + effort + " reasoning)"
				models = append(models, variant)
			}
		}
	}
	return models
}
