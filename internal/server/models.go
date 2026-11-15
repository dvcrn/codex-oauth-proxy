package server

// codexClientVersion is the Codex CLI version this proxy identifies as when
// talking to the ChatGPT backend. It is sent as the "version" header on
// upstream requests and as the client_version query parameter when listing
// models; the backend gates both model availability and protocol features on
// it, so the two must stay in sync.
const codexClientVersion = "0.163.0"

// Built-in models the ChatGPT Codex backend serves. Requests naming them are
// always accepted, and unknown IDs are rewritten to them. The live
// /backend-api/codex/models listing takes precedence for everything else.
const (
	modelGPT53Spark   = "gpt-5.3-codex-spark"
	modelGPT54Mini    = "gpt-5.4-mini"
	modelGPT55        = "gpt-5.5"
	modelGPT5Sol      = "gpt-5.6-sol"
	modelGPT5Terra    = "gpt-5.6-terra"
	modelGPT5Luna     = "gpt-5.6-luna"
	modelGPT6Astra    = "gpt-6-astra"
	modelGPT6Sol      = "gpt-6-sol"
	modelGPT61Sol     = "gpt-6.1-sol"
	modelGPT61Terra   = "gpt-6.1-terra"
	modelGPT6Luna     = "gpt-6-luna"
	modelDaybreakBlue = "gpt-daybreak-blue-latest"

	// modelDefault is used when a request names no model, or names one that is
	// no longer served.
	modelDefault = modelGPT55
)

// modelAllowedEfforts lists the reasoning efforts of models the upstream
// listing does not cover. For listed models only its "none" entry counts,
// since upstream omits "none" for models that accept it.
var modelAllowedEfforts = map[string][]string{
	modelGPT53Spark:   {"low", "medium", "high", "xhigh"},
	modelGPT54Mini:    {"none", "low", "medium", "high", "xhigh"},
	modelGPT55:        {"none", "low", "medium", "high", "xhigh"},
	modelGPT5Sol:      {"none", "low", "medium", "high", "xhigh", "max"},
	modelGPT5Terra:    {"none", "low", "medium", "high", "xhigh", "max"},
	modelGPT5Luna:     {"none", "low", "medium", "high", "xhigh", "max"},
	modelGPT6Astra:    {"low", "medium", "high", "xhigh", "max"},
	modelGPT6Sol:      {"low", "medium", "high", "xhigh", "max"},
	modelGPT61Sol:     {"low", "medium", "high", "xhigh", "max"},
	modelGPT61Terra:   {"low", "medium", "high", "xhigh", "max"},
	modelGPT6Luna:     {"low", "medium", "high", "xhigh", "max"},
	modelDaybreakBlue: {"none", "low", "medium", "high", "xhigh", "max"},
}

// modelDefaultEffort is the reasoning effort applied when a request names none
// and the upstream listing gives no default for the model.
var modelDefaultEffort = map[string]string{
	modelGPT53Spark:   "high",
	modelGPT54Mini:    "medium",
	modelGPT55:        "medium",
	modelGPT5Sol:      "low",
	modelGPT5Terra:    "medium",
	modelGPT5Luna:     "medium",
	modelGPT6Astra:    "medium",
	modelGPT6Sol:      "medium",
	modelGPT61Sol:     "low",
	modelGPT61Terra:   "low",
	modelGPT6Luna:     "medium",
	modelDaybreakBlue: "low",
}

// modelCatalog indexes the live upstream model listing by slug. A nil catalog
// is valid: lookups miss and callers fall back to the built-in model table.
type modelCatalog map[string]upstreamModel

func (c modelCatalog) lists(model string) bool {
	_, ok := c[model]
	return ok
}

func newModelCatalog(models []upstreamModel) modelCatalog {
	catalog := make(modelCatalog, len(models))
	for _, model := range models {
		catalog[model.Slug] = model
	}
	return catalog
}

// modelEffortPolicy returns the reasoning efforts a backend model accepts and
// the effort to use when none is requested. Upstream's levels and default take
// precedence over the built-in table. Upstream omits "none", so it is allowed
// unless the built-in table knows the model rejects it.
func modelEffortPolicy(model string, catalog modelCatalog) ([]string, string) {
	upstream, listed := catalog[model]
	allowed := upstream.efforts()
	if !listed || len(allowed) == 0 {
		return modelAllowedEfforts[model], modelDefaultEffort[model]
	}

	if _, known := modelAllowedEfforts[model]; !known || modelSupportsReasoningEffort(model, "none") {
		allowed = append([]string{"none"}, allowed...)
	}
	defaultEffort := upstream.DefaultReasoningLevel
	if defaultEffort == "" || unsupportedWireEfforts[defaultEffort] {
		defaultEffort = modelDefaultEffort[model]
	}
	return allowed, defaultEffort
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
