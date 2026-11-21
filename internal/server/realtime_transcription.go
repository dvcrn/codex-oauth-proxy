package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/gorilla/websocket"
)

var realtimeTranscriptionUpstream = url.URL{
	Scheme:   "https",
	Host:     "api.openai.com",
	Path:     "/v1/realtime",
	RawQuery: "intent=transcription",
}

func (s *Server) realtimeTranscriptionHandler(upstream url.URL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := r.URL.Query()
		if intents := query["intent"]; len(intents) != 1 || intents[0] != "transcription" || query.Has("model") || query.Has("call_id") {
			http.Error(w, "Expected a transcription-only realtime session", http.StatusBadRequest)
			return
		}
		if !websocket.IsWebSocketUpgrade(r) {
			w.Header().Set("Upgrade", "websocket")
			http.Error(w, "WebSocket upgrade required", http.StatusUpgradeRequired)
			return
		}

		token, accountID, err := s.credsFetcher.GetCredentials()
		if err != nil || token == "" {
			http.Error(w, "Codex OAuth credentials unavailable", http.StatusServiceUnavailable)
			return
		}

		proxy := httputil.ReverseProxy{
			Rewrite: func(request *httputil.ProxyRequest) {
				*request.Out.URL = upstream
				request.Out.Host = upstream.Host
				// Forward the WebSocket handshake without downstream credentials or cookies.
				request.Out.Header = make(http.Header)
				request.Out.Header.Set("Connection", "Upgrade")
				request.Out.Header.Set("Upgrade", "websocket")
				request.Out.Header.Set("Sec-WebSocket-Key", r.Header.Get("Sec-WebSocket-Key"))
				request.Out.Header.Set("Sec-WebSocket-Version", r.Header.Get("Sec-WebSocket-Version"))
				request.Out.Header.Set("Authorization", "Bearer "+token)
				if accountID != "" {
					request.Out.Header.Set("ChatGPT-Account-ID", accountID)
				}
			},
			ModifyResponse: func(response *http.Response) error {
				response.Header.Del("Set-Cookie")
				return nil
			},
		}
		proxy.ServeHTTP(w, r)
	}
}
