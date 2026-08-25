package server

import "testing"

func TestUpstreamTransportForModel_DefaultsToHTTP(t *testing.T) {
	if got := upstreamTransportForModel(modelGPT55); got != "http" {
		t.Fatalf("expected model to use http transport, got %q", got)
	}
}

// The preferred-model set is empty by default, so this registers one to guard
// the routing logic itself.
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
