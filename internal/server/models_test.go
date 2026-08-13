package server

import (
	"testing"
)

func TestSupportedModelsIncludesBaseAndSuffixVariants(t *testing.T) {
	models := supportedModels()
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		seen[m.ID] = true
	}

	// Base models
	for _, id := range []string{
		modelGPT5,
		modelGPT5Codex,
		modelGPT51,
		modelGPT51Codex,
		modelGPT51CodexMax,
		modelGPT52,
		modelGPT54,
		modelGPT52Codex,
		modelGPT53Codex,
		modelGPT53CodexSpark,
		modelGPT55,
		modelGPT5Sol,
		modelGPT5CodexMini,
		modelGPT51CodexMini,
	} {
		if !seen[id] {
			t.Fatalf("expected base model %q to be present in supported models", id)
		}
	}

	// Suffix variants derived from allowed efforts
	expectedVariants := []string{
		"gpt-5-high",
		"gpt-5-minimal",
		"gpt-5.1-high",
		"gpt-5.1-low",
		"gpt-5.2-medium",
		"gpt-5.2-xhigh",
		"gpt-5.4-low",
		"gpt-5.4-medium",
		"gpt-5.4-high",
		"gpt-5.4-xhigh",
		"gpt-5.2-codex-medium",
		"gpt-5.2-codex-xhigh",
		"gpt-5.3-codex-low",
		"gpt-5.3-codex-medium",
		"gpt-5.3-codex-high",
		"gpt-5.3-codex-xhigh",
		"gpt-5.3-codex-spark-low",
		"gpt-5.3-codex-spark-medium",
		"gpt-5.3-codex-spark-high",
		"gpt-5.3-codex-spark-xhigh",
		"gpt-5.5-low",
		"gpt-5.5-medium",
		"gpt-5.5-high",
		"gpt-5.5-xhigh",
		"gpt-5.6-sol-low",
		"gpt-5.6-sol-medium",
		"gpt-5.6-sol-high",
		"gpt-5.6-sol-xhigh",
		"gpt-5-codex-high",
		"gpt-5-codex-minimal",
		"gpt-5.1-codex-medium",
		"gpt-5.1-codex-max-low",
		"gpt-5.1-codex-max-xhigh",
		"gpt-5-codex-mini-medium",
		"gpt-5-codex-mini-high",
		"gpt-5.1-codex-mini-medium",
		"gpt-5.1-codex-mini-high",
	}

	for _, id := range expectedVariants {
		if !seen[id] {
			t.Fatalf("expected variant model %q to be present in supported models", id)
		}
	}
}
