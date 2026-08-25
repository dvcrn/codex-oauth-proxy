package server

import "testing"

func TestUpstreamTransportForModel_DefaultsToHTTP(t *testing.T) {
	if got := upstreamTransportForModel(modelGPT55); got != "http" {
		t.Fatalf("expected model to use http transport, got %q", got)
	}
}

// The WebSocket transport is opt-in per model. gpt-5.3-codex-spark, previously
// the only model routed this way, has been retired upstream, so the set is
// currently empty; this guards the routing logic itself.
func TestUpstreamTransportForModel_PreferredModelUsesWebSocket(t *testing.T) {
	if !supportsWebSocketUpstream() {
		t.Skip("websocket upstream is not available in this build")
	}

	const model = "gpt-websocket-test"
	websocketPreferredModels[model] = true
	t.Cleanup(func() { delete(websocketPreferredModels, model) })

	if got := upstreamTransportForModel(model); got != "websocket" {
		t.Fatalf("expected preferred model to use websocket transport, got %q", got)
	}
}
