//go:build !js || !wasm

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealtimeTranscriptionRoute(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", "test-client-key")
	server := New(zerolog.Nop(), stubCredentialsFetcher{})
	for _, test := range []struct {
		name, method, query string
		authenticated       bool
		status              int
	}{
		{"missing-key", http.MethodGet, "?intent=transcription", false, http.StatusUnauthorized},
		{"get-only", http.MethodPost, "?intent=transcription", true, http.StatusMethodNotAllowed},
		{"transcription-only", http.MethodGet, "?intent=realtime", true, http.StatusBadRequest},
		{"no-conversation-model", http.MethodGet, "?intent=transcription&model=gpt-realtime", true, http.StatusBadRequest},
		{"upgrade-required", http.MethodGet, "?intent=transcription", true, http.StatusUpgradeRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/v1/realtime"+test.query, nil)
			if test.authenticated {
				request.Header.Set("Authorization", "Bearer test-client-key")
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			assert.Equal(t, test.status, response.Code)
		})
	}
}

func TestRealtimeTranscriptionRelay(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", "test-client-key")
	const (
		sessionUpdate = `{"type":"session.update","session":{"type":"transcription"}}`
		audioAppend   = `{"type":"input_audio_buffer.append","audio":"AAAAAA=="}`
		transcript    = `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/realtime?intent=transcription", r.URL.RequestURI())
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "test-account", r.Header.Get("ChatGPT-Account-ID"))
		assert.Empty(t, r.Header.Get("X-API-Key"))
		assert.Empty(t, r.Header.Get("Cookie"))
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, http.Header{"Set-Cookie": {"upstream=private"}})
		if !assert.NoError(t, err) {
			return
		}
		defer conn.Close()
		assert.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
		for _, expected := range []string{sessionUpdate, audioAppend} {
			kind, payload, err := conn.ReadMessage()
			if !assert.NoError(t, err) {
				return
			}
			assert.Equal(t, websocket.TextMessage, kind)
			assert.Equal(t, expected, string(payload))
		}
		assert.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(transcript)))
	}))
	defer upstream.Close()
	target := realtimeTranscriptionUpstream
	target.Scheme = "http"
	target.Host = strings.TrimPrefix(upstream.URL, "http://")
	server := New(zerolog.Nop(), stubCredentialsFetcher{})
	downstream := httptest.NewServer(server.adminMiddleware(server.realtimeTranscriptionHandler(target)))
	defer downstream.Close()
	headers := http.Header{
		"Authorization": {"Bearer test-client-key"},
		"X-Api-Key":     {"test-client-key"},
		"Cookie":        {"client=private"},
	}
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/realtime?intent=transcription", headers)
	require.NoError(t, err)
	defer conn.Close()
	assert.Empty(t, response.Header.Get("Set-Cookie"))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	for _, event := range []string{sessionUpdate, audioAppend} {
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(event)))
	}
	kind, payload, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, kind)
	require.Equal(t, transcript, string(payload))
}
