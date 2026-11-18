package server

// codexClientVersion is the Codex CLI version this proxy identifies as when
// talking to the ChatGPT backend. It is sent as the "version" header on
// upstream requests and as the client_version query parameter when listing
// models; the backend gates both model availability and protocol features on
// it, so the two must stay in sync.
const codexClientVersion = "0.170.0"

// modelCatalog is the live upstream model listing. A nil or empty catalog is
// valid when the upstream listing is unavailable.
type modelCatalog []upstreamModel

func (c modelCatalog) lookup(slug string) (upstreamModel, bool) {
	for _, model := range c {
		if model.Slug == slug {
			return model, true
		}
	}
	return upstreamModel{}, false
}

func (c modelCatalog) lists(model string) bool {
	_, ok := c.lookup(model)
	return ok
}

// modelEffortPolicy returns the reasoning efforts a backend model accepts and
// the effort to use when none is requested, based on the upstream catalog.
func modelEffortPolicy(model string, catalog modelCatalog) ([]string, string) {
	upstream, listed := catalog.lookup(model)
	if !listed {
		return nil, ""
	}
	defaultEffort := upstream.DefaultReasoningLevel
	if unsupportedWireEfforts[defaultEffort] {
		defaultEffort = ""
	}
	return upstream.efforts(), defaultEffort
}

// modelMetadata mirrors the JSON schema required by the OpenAI-compatible
// /v1/models endpoint. The structure intentionally keeps nested fields as
// generic maps to simplify aligning with the upstream payload shape and to
// avoid bespoke types for every nested object.
type modelMetadata struct {
	Capabilities        map[string]interface{} `json:"capabilities,omitempty"`
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

// modelsFromUpstream converts the live entitlement listing into the
// OpenAI-compatible metadata /v1/models serves.
func modelsFromUpstream(upstream []upstreamModel) []modelMetadata {
	baseSlugs := make(map[string]bool, len(upstream))
	for _, model := range upstream {
		baseSlugs[model.Slug] = true
	}

	models := make([]modelMetadata, 0, len(upstream))
	for _, model := range upstream {
		name := model.DisplayName
		if name == "" {
			name = model.Slug
		}
		base := modelMetadata{
			ID:                 model.Slug,
			Name:               name,
			Object:             "model",
			Vendor:             "openai",
			Version:            model.Slug,
			ModelPickerEnabled: true,
		}
		models = append(models, base)

		for _, effort := range model.efforts() {
			variantID := model.Slug + "-" + effort
			if baseSlugs[variantID] {
				continue
			}
			variant := base
			variant.ID = variantID
			variant.Name = base.Name + " (" + effort + " reasoning)"
			models = append(models, variant)
		}
	}
	return models
}
