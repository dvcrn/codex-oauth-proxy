//go:build !js || !wasm

package server

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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

type realtimeRetryCredentials struct {
	refreshes  atomic.Int32
	refreshErr error
}

func (f *realtimeRetryCredentials) GetCredentials() (string, string, error) {
	if f.refreshes.Load() > 0 {
		return "fresh-token", "", nil
	}
	return "stale-token", "stale-account", nil
}

func (f *realtimeRetryCredentials) RefreshCredentials() error {
	f.refreshes.Add(1)
	return f.refreshErr
}

func TestRealtimeTranscriptionHandshakeRetry(t *testing.T) {
	for _, test := range []struct {
		name        string
		firstStatus int
		retryStatus int
		refreshErr  error
		wantStatus  int
		wantCalls   int32
		wantRefresh int32
	}{
		{"refresh and upgrade", 401, 101, nil, 101, 2, 1},
		{"stop after second unauthorized", 401, 401, nil, 401, 2, 1},
		{"forbidden is not retried", 403, 0, nil, 403, 1, 0},
		{"refresh failure preserves status", 401, 0, errors.New("refresh failed"), 401, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			creds := &realtimeRetryCredentials{refreshErr: test.refreshErr}
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := upstreamCalls.Add(1)
				if call == 1 {
					assert.Equal(t, "Bearer stale-token", r.Header.Get("Authorization"))
					assert.Equal(t, "stale-account", r.Header.Get("ChatGPT-Account-ID"))
					w.WriteHeader(test.firstStatus)
					return
				}
				if !assert.Equal(t, int32(2), call) || !assert.NotZero(t, test.retryStatus) {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get("ChatGPT-Account-ID"))
				if test.retryStatus != http.StatusSwitchingProtocols {
					w.WriteHeader(test.retryStatus)
					return
				}
				upgrader := websocket.Upgrader{}
				conn, err := upgrader.Upgrade(w, r, nil)
				if assert.NoError(t, err) {
					conn.Close()
				}
			}))
			defer upstream.Close()

			target := realtimeTranscriptionUpstream
			target.Scheme = "http"
			target.Host = strings.TrimPrefix(upstream.URL, "http://")
			server := New(zerolog.Nop(), creds)
			downstream := httptest.NewServer(server.realtimeTranscriptionHandler(target))
			defer downstream.Close()

			dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
			conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/realtime?intent=transcription", nil)
			if test.wantStatus == http.StatusSwitchingProtocols {
				require.NoError(t, err)
				defer conn.Close()
			} else {
				require.ErrorIs(t, err, websocket.ErrBadHandshake)
			}
			require.NotNil(t, response)
			assert.Equal(t, test.wantStatus, response.StatusCode)
			assert.Equal(t, test.wantCalls, upstreamCalls.Load())
			assert.Equal(t, test.wantRefresh, creds.refreshes.Load())
		})
	}
}

func TestRealtimeTranscriptionSanitizesProxyErrors(t *testing.T) {
	const rawDetail = "private-proxy-detail"
	var logs bytes.Buffer
	server := New(zerolog.New(&logs), stubCredentialsFetcher{})
	handler := server.realtimeTranscriptionHandler(url.URL{Scheme: rawDetail, Host: "example.test"})
	request := httptest.NewRequest(http.MethodGet, "/v1/realtime?intent=transcription", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusBadGateway, response.Code)
	assert.Contains(t, logs.String(), "Realtime transcription proxy request failed")
	assert.NotContains(t, logs.String(), rawDetail)

	errorLog := realtimeProxyErrorLog{logger: server.logger}
	count, err := errorLog.Write([]byte(rawDetail))
	require.NoError(t, err)
	assert.Equal(t, len(rawDetail), count)
	assert.Contains(t, logs.String(), "Realtime transcription proxy response copy failed")
	assert.NotContains(t, logs.String(), rawDetail)
}
