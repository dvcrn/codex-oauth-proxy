//go:build !js || !wasm

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const websocketResponsesBetaHeader = "responses_websockets=2026-02-04"

func supportsWebSocketUpstream() bool {
	return true
}

func (s *Server) makeChatGPTWebSocketRequest(r *http.Request, rawURL string, body []byte, token, accountID string) (*http.Response, int, error) {
	wsURL, err := toWebSocketURL(rawURL)
	if err != nil {
		return nil, 0, err
	}

	createPayload, err := wrapWebSocketCreatePayload(body)
	if err != nil {
		return nil, 0, err
	}

	// Normalize token to avoid double "Bearer ".
	bareToken := strings.TrimSpace(token)
	if len(bareToken) >= 7 && strings.EqualFold(bareToken[:7], "Bearer ") {
		bareToken = strings.TrimSpace(bareToken[7:])
	}

	sessionID := newUUIDv4()
	headers := http.Header{}
	setCodexRequestHeaders(headers, s.clientIdentity, token, accountID, "text/event-stream")
	headers.Set("openai-beta", websocketResponsesBetaHeader)
	headers.Set("session_id", sessionID)
	headers.Set("x-codex-beta-features", "collab,apps")
	headers.Set("x-codex-turn-metadata", `{"sandbox":"none"}`)

	s.logger.Info().
		Str("authorization_preview", "Bearer "+func() string {
			if len(bareToken) > 12 {
				return bareToken[:6] + "…" + bareToken[len(bareToken)-6:]
			}
			return bareToken
		}()).
		Str("chatgpt-account-id", accountID).
		Str("session_id", sessionID).
		Str("version", s.clientIdentity.version).
		Str("openai-beta", websocketResponsesBetaHeader).
		Msg("Upstream websocket headers (sanitized)")

	dialer := websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  10 * time.Second,
		EnableCompression: true,
	}

	conn, resp, err := dialer.DialContext(r.Context(), wsURL, headers)
	if err != nil {
		if resp != nil {
			if resp.Body == nil {
				resp.Body = io.NopCloser(strings.NewReader(err.Error()))
			}
			return resp, resp.StatusCode, nil
		}
		return nil, 0, fmt.Errorf("failed to open websocket upstream connection: %w", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage, createPayload); err != nil {
		conn.Close()
		return nil, 0, fmt.Errorf("failed to send websocket request payload: %w", err)
	}

	pipeReader, pipeWriter := io.Pipe()

	// Ensure cancellation closes the websocket reader loop promptly.
	go func() {
		<-r.Context().Done()
		conn.Close()
	}()

	go func() {
		defer conn.Close()
		defer pipeWriter.Close()

		doneSent := false
		for {
			msgType, payload, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
					return
				}
				if doneSent {
					return
				}
				pipeWriter.CloseWithError(fmt.Errorf("websocket stream read failed: %w", err))
				return
			}
			if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
				continue
			}

			trimmed := bytes.TrimSpace(payload)
			if len(trimmed) == 0 {
				continue
			}

			if err := writeSSEEvent(pipeWriter, trimmed); err != nil {
				return
			}

			switch websocketEventType(trimmed) {
			case "response.completed", "response.failed", "error":
				if err := writeSSEEvent(pipeWriter, []byte("[DONE]")); err != nil {
					return
				}
				doneSent = true
				return
			}
		}
	}()

	responseHeaders := make(http.Header)
	responseHeaders.Set("Content-Type", "text/event-stream; charset=utf-8")
	responseHeaders.Set("Cache-Control", "no-cache")
	responseHeaders.Set("Connection", "keep-alive")

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     responseHeaders,
		Body:       pipeReader,
	}, http.StatusOK, nil
}

func toWebSocketURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse upstream URL %q: %w", rawURL, err)
	}
	switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported upstream URL scheme %q", u.Scheme)
	}
	return u.String(), nil
}

func wrapWebSocketCreatePayload(body []byte) ([]byte, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to decode transformed request for websocket payload: %w", err)
	}
	payload["type"] = "response.create"
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode websocket payload: %w", err)
	}
	return encoded, nil
}

func websocketEventType(payload []byte) string {
	var evt struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		return ""
	}
	return strings.TrimSpace(evt.Type)
}

func writeSSEEvent(w io.Writer, payload []byte) error {
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return err
	}
	return nil
}
