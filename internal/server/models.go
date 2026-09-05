package server

// codexClientVersion is the Codex CLI version this proxy identifies as when
// talking to the ChatGPT backend. It is sent as the "version" header on
// upstream requests and as the client_version query parameter when listing
// models; the backend gates both model availability and protocol features on
// it, so the two must stay in sync.
const codexClientVersion = "0.153.0"

// Models currently served by the ChatGPT Codex backend. The authoritative list
// is the /backend-api/codex/models endpoint; these constants exist for request
// normalization and model-specific reasoning behavior.
const (
	modelGPT53Spark   = "gpt-5.3-codex-spark"
	modelGPT54Mini    = "gpt-5.4-mini"
	modelGPT55        = "gpt-5.5"
	modelGPT5Sol      = "gpt-5.6-sol"
	modelGPT5Terra    = "gpt-5.6-terra"
	modelGPT5Luna     = "gpt-5.6-luna"
	modelGPT6Astra    = "gpt-6-astra"
	modelDaybreakBlue = "gpt-daybreak-blue-latest"

	// modelDefault is used when a request names no model, or names one that is
	// no longer served.
	modelDefault = modelGPT55
)

// modelAllowedEfforts defines which reasoning effort levels are valid for each
// canonical backend model. The upstream models endpoint omits "none" for models
// that accept it, so that capability is maintained explicitly here.
var modelAllowedEfforts = map[string][]string{
	modelGPT53Spark:   {"low", "medium", "high", "xhigh"},
	modelGPT54Mini:    {"none", "low", "medium", "high", "xhigh"},
	modelGPT55:        {"none", "low", "medium", "high", "xhigh"},
	modelGPT5Sol:      {"none", "low", "medium", "high", "xhigh", "max"},
	modelGPT5Terra:    {"none", "low", "medium", "high", "xhigh", "max"},
	modelGPT5Luna:     {"none", "low", "medium", "high", "xhigh", "max"},
	modelGPT6Astra:    {"low", "medium", "high", "xhigh", "max"},
	modelDaybreakBlue: {"none", "low", "medium", "high", "xhigh", "max"},
}

// modelDefaultEffort defines the default reasoning effort to apply when the
// user does not explicitly specify an effort for the given model.
var modelDefaultEffort = map[string]string{
	modelGPT53Spark:   "high",
	modelGPT54Mini:    "medium",
	modelGPT55:        "medium",
	modelGPT5Sol:      "low",
	modelGPT5Terra:    "medium",
	modelGPT5Luna:     "medium",
	modelGPT6Astra:    "medium",
	modelDaybreakBlue: "low",
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
}

// modelsFromUpstream converts the live entitlement listing into the
// OpenAI-compatible metadata /v1/models serves. Known slugs reuse the built-in
// table's richer capability metadata; unrecognized ones (newly launched
// models) are still listed, with defaults.
func modelsFromUpstream(upstream []upstreamModel) []modelMetadata {
	models := make([]modelMetadata, 0, len(upstream))
	for _, model := range upstream {
		base, ok := modelMetadataByID[model.Slug]
		if !ok {
			base = modelMetadata{
				ID:                 model.Slug,
				Object:             "model",
				Vendor:             "openai",
				Version:            model.Slug,
				ModelPickerEnabled: true,
			}
		}
		if model.DisplayName != "" {
			base.Name = model.DisplayName
		}
		if base.Name == "" {
			base.Name = model.Slug
		}
		base.ID = model.Slug

		models = append(models, base)

		efforts := model.efforts()
		if modelSupportsReasoningEffort(model.Slug, "none") {
			efforts = append([]string{"none"}, efforts...)
		}
		for _, effort := range efforts {
			variant := base
			variant.ID = model.Slug + "-" + effort
			variant.Name = base.Name + " (" + effort + " reasoning)"
			models = append(models, variant)
		}
	}
	return models
}
