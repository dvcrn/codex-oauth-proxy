package server

import "strings"

// websocketPreferredModels lists the normalized backend models routed to the
// WebSocket upstream transport.
//
// This used to be gated on gpt-5.3-codex-spark, which has since been retired.
// The upstream models endpoint now reports prefer_websockets=true for every
// model, but the HTTP transport still serves them correctly, so this stays an
// explicit opt-in rather than following that flag: switching every request to
// WebSockets is a behavioural change that deserves its own testing.
var websocketPreferredModels = map[string]bool{}

// shouldUseWebSocketUpstream determines whether a normalized backend model
// should be routed to the WebSocket upstream transport.
func shouldUseWebSocketUpstream(normalizedModel string) bool {
	if !supportsWebSocketUpstream() {
		return false
	}
	return websocketPreferredModels[strings.TrimSpace(normalizedModel)]
}

func upstreamTransportForModel(normalizedModel string) string {
	if shouldUseWebSocketUpstream(normalizedModel) {
		return "websocket"
	}
	return "http"
}
