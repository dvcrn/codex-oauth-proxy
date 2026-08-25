package server

import (
	"testing"
)

func TestModelsFromUpstreamIncludesBaseAndSuffixVariants(t *testing.T) {
	upstream := []upstreamModel{
		{
			Slug:        "gpt-5.5",
			DisplayName: "GPT-5.5",
			SupportedReasoningLevel: []struct {
				Effort string `json:"effort"`
			}{{Effort: "low"}, {Effort: "medium"}, {Effort: "high"}, {Effort: "xhigh"}},
		},
		{
			Slug:        "gpt-5.6-sol",
			DisplayName: "GPT-5.6-Sol",
			SupportedReasoningLevel: []struct {
				Effort string `json:"effort"`
			}{{Effort: "low"}, {Effort: "max"}, {Effort: "ultra"}},
		},
	}

	models := modelsFromUpstream(upstream)
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		seen[m.ID] = true
	}

	// "ultra" is rejected by the Responses API, so it must not be advertised.
	if seen["gpt-5.6-sol-ultra"] {
		t.Fatal("ultra must be filtered out of the advertised efforts")
	}

	for _, id := range []string{
		// Base models
		"gpt-5.5",
		"gpt-5.6-sol",
		// Suffix variants derived from the upstream effort levels
		"gpt-5.5-low",
		"gpt-5.5-medium",
		"gpt-5.5-high",
		"gpt-5.5-xhigh",
		"gpt-5.6-sol-low",
		"gpt-5.6-sol-max",
	} {
		if !seen[id] {
			t.Fatalf("expected model %q to be present in the listing", id)
		}
	}
}

// A model the built-in metadata table has never seen must still be listed, so
// newly launched models are not silently dropped from /v1/models.
func TestModelsFromUpstreamHandlesUnknownSlugs(t *testing.T) {
	models := modelsFromUpstream([]upstreamModel{{
		Slug:        "gpt-brand-new",
		DisplayName: "Brand New",
	}})

	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ID != "gpt-brand-new" {
		t.Fatalf("unexpected model ID %q", models[0].ID)
	}
	if models[0].Name != "Brand New" {
		t.Fatalf("unexpected model name %q", models[0].Name)
	}
	if models[0].Object != "model" {
		t.Fatalf("unexpected object %q", models[0].Object)
	}
}
