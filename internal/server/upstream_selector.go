package server

import "strings"

// websocketPreferredModels lists the normalized backend models routed to the
// WebSocket upstream transport. It is empty by design: the models endpoint
// reports prefer_websockets=true for every model, but the HTTP transport
// serves them all correctly, so routing here stays an explicit opt-in.
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
