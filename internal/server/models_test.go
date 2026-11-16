package server

import (
	"testing"
)

func TestModelsFromUpstreamIncludesBaseAndSuffixVariants(t *testing.T) {
	upstream := []upstreamModel{
		{
			Slug:        "gpt-6-luna",
			DisplayName: "GPT-6 Luna",
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
		{
			Slug:        "gpt-7-codex",
			DisplayName: "GPT-7 Codex",
			SupportedReasoningLevel: []struct {
				Effort string `json:"effort"`
			}{{Effort: "max"}, {Effort: "xlow"}},
		},
		{
			Slug:        "gpt-7-codex-max",
			DisplayName: "GPT-7 Codex Max",
			SupportedReasoningLevel: []struct {
				Effort string `json:"effort"`
			}{{Effort: "medium"}},
		},
	}

	models := modelsFromUpstream(upstream)
	seen := make(map[string]int, len(models))
	for _, m := range models {
		seen[m.ID]++
		if seen[m.ID] > 1 {
			t.Fatalf("duplicate model ID %q in listing", m.ID)
		}
	}

	// "ultra" is rejected by the Responses API, so it must not be advertised.
	if seen["gpt-5.6-sol-ultra"] > 0 {
		t.Fatal("ultra must be filtered out of the advertised efforts")
	}

	catalog := modelCatalog(upstream)
	for _, id := range []string{
		"gpt-6-luna",
		"gpt-5.6-sol",
		"gpt-6-luna-low",
		"gpt-6-luna-medium",
		"gpt-6-luna-high",
		"gpt-6-luna-xhigh",
		"gpt-5.6-sol-low",
		"gpt-5.6-sol-max",
		"gpt-7-codex-xlow",
		"gpt-7-codex-max",
	} {
		if seen[id] == 0 {
			t.Fatalf("expected model %q to be present in the listing", id)
		}
	}
	if got := normalizeModel("gpt-7-codex-xlow", catalog); got != "gpt-7-codex" {
		t.Fatalf("normalizeModel(gpt-7-codex-xlow) = %q, want gpt-7-codex", got)
	}

	for _, id := range []string{"gpt-6-luna-none", "gpt-5.6-sol-none"} {
		if seen[id] > 0 {
			t.Fatalf("unexpected unlisted variant %q in model listing", id)
		}
	}
}

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
