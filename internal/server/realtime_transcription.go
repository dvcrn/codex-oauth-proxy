package server

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

var realtimeTranscriptionUpstream = url.URL{
	Scheme:   "https",
	Host:     "api.openai.com",
	Path:     "/v1/realtime",
	RawQuery: "intent=transcription",
}

type realtimeRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip realtimeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type realtimeProxyErrorLog struct {
	logger zerolog.Logger
}

func (w realtimeProxyErrorLog) Write(message []byte) (int, error) {
	w.logger.Error().Msg("Realtime transcription proxy response copy failed")
	return len(message), nil
}

func (s *Server) realtimeTranscriptionHandler(upstream url.URL) http.HandlerFunc {
	errorLog := log.New(realtimeProxyErrorLog{logger: s.logger}, "", 0)
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
			s.logger.Error().Msg("Realtime transcription credentials unavailable")
			http.Error(w, "Codex OAuth credentials unavailable", http.StatusServiceUnavailable)
			return
		}

		proxy := httputil.ReverseProxy{
			Transport: realtimeRoundTripper(s.roundTripRealtimeHandshake),
			ErrorLog:  errorLog,
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				s.logger.Error().Msg("Realtime transcription proxy request failed")
				w.WriteHeader(http.StatusBadGateway)
			},
			Rewrite: func(request *httputil.ProxyRequest) {
				*request.Out.URL = upstream
				request.Out.Host = upstream.Host
				// Forward the WebSocket handshake without downstream credentials or cookies.
				request.Out.Header = make(http.Header)
				request.Out.Header.Set("Connection", "Upgrade")
				request.Out.Header.Set("Upgrade", "websocket")
				request.Out.Header.Set("Sec-WebSocket-Key", r.Header.Get("Sec-WebSocket-Key"))
				request.Out.Header.Set("Sec-WebSocket-Version", r.Header.Get("Sec-WebSocket-Version"))
				setRealtimeCredentials(request.Out.Header, token, accountID)
			},
			ModifyResponse: func(response *http.Response) error {
				response.Header.Del("Set-Cookie")
				return nil
			},
		}
		proxy.ServeHTTP(w, r)
	}
}

func (s *Server) roundTripRealtimeHandshake(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	// Retry only a bodyless handshake before switching to the WebSocket protocol.
	if request.Body != nil || request.Context().Err() != nil {
		return response, nil
	}

	if err := s.credsFetcher.RefreshCredentials(); err != nil {
		s.logger.Warn().Msg("Realtime transcription credential refresh failed")
		return response, nil
	}
	token, accountID, err := s.credsFetcher.GetCredentials()
	if err != nil || token == "" {
		s.logger.Error().Msg("Realtime transcription credentials unavailable after refresh")
		return response, nil
	}

	response.Body.Close()
	retry := request.Clone(request.Context())
	setRealtimeCredentials(retry.Header, token, accountID)
	return http.DefaultTransport.RoundTrip(retry)
}

func setRealtimeCredentials(headers http.Header, token, accountID string) {
	headers.Set("Authorization", "Bearer "+token)
	if accountID == "" {
		headers.Del("ChatGPT-Account-ID")
		return
	}
	headers.Set("ChatGPT-Account-ID", accountID)
}
