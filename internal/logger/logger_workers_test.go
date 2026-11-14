//go:build js && wasm

package logger

import (
	"testing"

	"github.com/dvcrn/codex-oauth-proxy/internal/env"
	"github.com/rs/zerolog"
)

func TestNewUsesWorkersEnvironment(t *testing.T) {
	t.Setenv("ENV", "development")
	if value := env.GetOrDefault("ENV", ""); value != "production" {
		t.Fatalf("Workers binding ENV = %q, want production", value)
	}
	if log := New(); log.GetLevel() != zerolog.InfoLevel {
		t.Fatalf("Workers production logger level = %v, want info", log.GetLevel())
	}
}
