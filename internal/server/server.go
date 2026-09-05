package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dvcrn/codex-oauth-proxy/internal/credentials"
	"github.com/dvcrn/codex-oauth-proxy/internal/env"
	"github.com/rs/zerolog"
)

// sseFlushWriter wraps a ResponseWriter to flush after each write.
type sseFlushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw sseFlushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if err == nil {
		fw.f.Flush()
	}
	return n, err
}

// HTTPClient is an interface for making HTTP requests
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Server struct {
	credsFetcher      credentials.CredentialsFetcher
	httpClient        HTTPClient
	mux               *http.ServeMux
	logger            zerolog.Logger
	disableHealthLogs bool

	// Cached result of the upstream model listing, guarded by modelsCacheMu.
	modelsCacheMu     sync.Mutex
	modelsCache       []upstreamModel
	modelsCacheExpiry time.Time
}

func New(logger zerolog.Logger, credsFetcher credentials.CredentialsFetcher) *Server {
	disableHealthLogs, _ := strconv.ParseBool(env.GetOrDefault("DISABLE_HEALTH_LOGS", "false"))

	s := &Server{
		credsFetcher:      credsFetcher,
		httpClient:        NewHTTPClient(),
		mux:               http.NewServeMux(),
		logger:            logger,
		disableHealthLogs: disableHealthLogs,
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/v1/chat/completions", s.adminMiddleware(s.chatCompletionsHandler))
	s.mux.HandleFunc("/v1/responses", s.adminMiddleware(s.responsesHandler))
	s.mux.HandleFunc("/v1/models", s.modelsHandler)
	s.mux.HandleFunc("/health", s.healthHandler)
	s.mux.HandleFunc("/admin/credentials", s.adminMiddleware(s.credentialsHandler))
	s.mux.HandleFunc("/admin/credentials/status", s.adminMiddleware(s.credentialsStatusHandler))

	// MCP endpoint. The handler is built once so the tool set is shared across
	// requests; the session itself is stateless.
	mcpHandler := s.adminMiddleware(s.mcpHandler())
	s.mux.HandleFunc("/mcp", mcpHandler)
	s.mux.HandleFunc("/mcp/", mcpHandler)

	s.mux.HandleFunc("/", s.notFoundHandler)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.loggingMiddleware(s.mux).ServeHTTP(w, r)
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.disableHealthLogs && r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		s.logger.Info().
			Str("method", r.Method).
			Str("uri", r.RequestURI).
			Str("remote_addr", r.RemoteAddr).
			Str("user_agent", r.UserAgent()).
			Msg("Incoming request")
		next.ServeHTTP(w, r)
		s.logger.Info().
			Str("method", r.Method).
			Str("uri", r.RequestURI).
			Dur("duration", time.Since(start)).
			Msg("Finished request")
	})
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "ok"}`))
}

func (s *Server) modelsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	upstream, err := s.fetchUpstreamModels(r.Context())
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to list upstream models")
		var authErr *upstreamAuthError
		if errors.As(err, &authErr) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			if encodeErr := json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"message": "Codex upstream authentication failed. Refresh the OAuth credentials and retry.",
					"type":    "authentication_error",
					"code":    "token_expired",
				},
			}); encodeErr != nil {
				s.logger.Error().Err(encodeErr).Msg("Failed to encode authentication error response")
			}
			return
		}
		http.Error(w, "Failed to list models: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := modelsResponse{
		Object: "list",
		Data:   modelsFromUpstream(upstream),
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error().Err(err).Msg("Failed to encode models response")
	}
}

func (s *Server) notFoundHandler(w http.ResponseWriter, r *http.Request) {
	s.logger.Warn().
		Str("method", r.Method).
		Str("uri", r.RequestURI).
		Str("remote_addr", r.RemoteAddr).
		Str("user_agent", r.UserAgent()).
		Msg("Unhandled route")
	http.NotFound(w, r)
}

func (s *Server) chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	requestBodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error().Err(err).Msg("Error reading request body")
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// Parse the request body into a map
	var requestData map[string]interface{}
	if err := json.Unmarshal(requestBodyBytes, &requestData); err != nil {
		s.logger.Error().Err(err).Msg("Error unmarshalling request body")
		http.Error(w, "Failed to parse request body", http.StatusBadRequest)
		return
	}

	// Determine whether the client requested streaming.
	// OpenAI's default is non-streaming when "stream" is omitted, so
	// we treat absence as false and only stream when explicitly true.
	stream := false
	if v, ok := requestData["stream"]; ok {
		if b, ok := v.(bool); ok {
			stream = b
		}
	}

	// Extract request parameters for logging
	requestedModel := resolveRequestModel(requestData)
	normalizedModel := normalizeModel(requestedModel)
	reasoningEffort := resolveReasoningEffort(requestData)
	normalizedReasoningEffort := normalizeReasoningEffort(reasoningEffort)

	messageCount := 0
	if messages, ok := requestData["messages"].([]interface{}); ok {
		messageCount = len(messages)
	}

	logToolCallInteractions(s.logger, requestData)

	// Build target body for ChatGPT Codex Responses
	target := buildCodexRequestBody(requestData)

	// Debug: log inbound and outbound (sanitized previews)
	inboundPreview := string(requestBodyBytes)
	if len(inboundPreview) > 1200 {
		inboundPreview = inboundPreview[:1200] + "…(truncated)"
	}
	outboundBytes, _ := json.Marshal(target)
	outboundPreview := string(outboundBytes)
	if len(outboundPreview) > 1200 {
		outboundPreview = outboundPreview[:1200] + "…(truncated)"
	}
	// Add extra debug for instructions and input count
	instructions := target["instructions"]
	instrStr, _ := instructions.(string)
	instrPreview := instrStr
	if len(instrPreview) > 200 {
		instrPreview = instrPreview[:200] + "…"
	}
	inputCount := 0
	if in, ok := target["input"].([]interface{}); ok {
		inputCount = len(in)
	}

	s.logger.Debug().
		Str("inbound_body_preview", inboundPreview).
		Str("outbound_body_preview", outboundPreview).
		Int("instructions_len", len(instrStr)).
		Str("instructions_preview", instrPreview).
		Int("input_count", inputCount).
		Msg("Transform debug: body previews")

	// Marshal target body
	modifiedBodyBytes, err := json.Marshal(target)
	if err != nil {
		s.logger.Error().Err(err).Msg("Error marshalling modified request body")
		http.Error(w, "Failed to prepare modified request", http.StatusInternalServerError)
		return
	}

	upstreamURL := "https://chatgpt.com/backend-api/codex/responses"

	transport := upstreamTransportForModel(normalizedModel)

	// Log request details
	logEvent := s.logger.Info().
		Str("requested_model", requestedModel).
		Str("normalized_model", normalizedModel).
		Str("upstream_transport", transport).
		Str("requested_reasoning_effort", reasoningEffort).
		Str("normalized_reasoning_effort", normalizedReasoningEffort).
		Int("message_count", messageCount).
		Str("user_agent", r.UserAgent()).
		Str("endpoint", upstreamURL).
		Str("prompt_cache_key", func() string {
			if key, ok := target["prompt_cache_key"].(string); ok {
				return key
			}
			return ""
		}())

	logEvent.Msg("Processing chat completion request")

	// Make upstream request with automatic retry on 401
	responseData, statusCode, err := s.makeChatGPTRequestWithRetry(r, upstreamURL, modifiedBodyBytes, normalizedModel)
	if err != nil {
		s.logger.Error().Err(err).Msg("Error making request to ChatGPT backend")
		http.Error(w, "Failed to communicate with upstream API: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// If the client requested streaming, reuse the existing SSE rewriting path.
	if stream {
		s.writeResponse(w, responseData, statusCode, normalizedModel, true)
		return
	}

	// Non-streaming path: buffer the upstream SSE stream and synthesize a single
	// chat completion response for clients that expect the classic JSON shape.
	if statusCode != http.StatusOK {
		s.writeResponse(w, responseData, statusCode, normalizedModel, false)
		return
	}

	defer responseData.Body.Close()
	respObj, err := bufferChatCompletionFromSSE(responseData.Body, normalizedModel)
	if err != nil {
		s.logger.Error().Err(err).Msg("Error buffering SSE stream for non-streaming client")
		http.Error(w, "Failed to process streaming response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(respObj); err != nil {
		s.logger.Error().Err(err).Msg("Error encoding buffered chat completion response")
	}
}

func (s *Server) responsesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	requestBodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error().Err(err).Msg("Error reading request body")
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	var requestData map[string]interface{}
	if err := json.Unmarshal(requestBodyBytes, &requestData); err != nil {
		s.logger.Error().Err(err).Msg("Error unmarshalling request body")
		http.Error(w, "Failed to parse request body", http.StatusBadRequest)
		return
	}

	requestedModel := resolveRequestModel(requestData)
	requestedEffort := resolveReasoningEffort(requestData)
	inputCount := 0
	if input, ok := requestData["input"].([]interface{}); ok {
		inputCount = len(input)
	}

	// Transform request body
	normalizedModel, normalizedEffort := transformResponsesRequestBody(requestData, requestedModel, requestedEffort)
	cacheKey, _ := requestData["prompt_cache_key"].(string)

	modifiedBodyBytes, err := json.Marshal(requestData)
	if err != nil {
		s.logger.Error().Err(err).Msg("Error marshalling modified request body")
		http.Error(w, "Failed to prepare modified request", http.StatusInternalServerError)
		return
	}

	// Debug previews
	inboundPreview := string(requestBodyBytes)
	if len(inboundPreview) > 1200 {
		inboundPreview = inboundPreview[:1200] + "…(truncated)"
	}
	outboundPreview := string(modifiedBodyBytes)
	if len(outboundPreview) > 1200 {
		outboundPreview = outboundPreview[:1200] + "…(truncated)"
	}
	instructions := ""
	if instr, ok := requestData["instructions"].(string); ok {
		instructions = instr
	}
	instrPreview := instructions
	if len(instrPreview) > 200 {
		instrPreview = instrPreview[:200] + "…"
	}

	s.logger.Debug().
		Str("inbound_body_preview", inboundPreview).
		Str("outbound_body_preview", outboundPreview).
		Int("instructions_len", len(instructions)).
		Str("instructions_preview", instrPreview).
		Int("input_count", inputCount).
		Msg("Responses transform debug: body previews")

	upstreamURL := "https://chatgpt.com/backend-api/codex/responses"
	transport := upstreamTransportForModel(normalizedModel)
	logEvent := s.logger.Info().
		Str("requested_model", requestedModel).
		Str("normalized_model", normalizedModel).
		Str("upstream_transport", transport).
		Str("requested_reasoning_effort", requestedEffort).
		Str("normalized_reasoning_effort", normalizedEffort).
		Str("prompt_cache_key", cacheKey).
		Int("input_count", inputCount).
		Str("user_agent", r.UserAgent()).
		Str("endpoint", upstreamURL)
	logEvent.Msg("Processing responses request")

	responseData, statusCode, err := s.makeChatGPTRequestWithRetry(r, upstreamURL, modifiedBodyBytes, normalizedModel)
	if err != nil {
		s.logger.Error().Err(err).Msg("Error making request to ChatGPT backend")
		http.Error(w, "Failed to communicate with upstream API: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	if statusCode >= 400 {
		preview := previewResponseBody(responseData)
		shape := describeResponsesInputShape(requestData)
		inboundPreview := string(requestBodyBytes)
		if len(inboundPreview) > 600 {
			inboundPreview = inboundPreview[:600] + "…(truncated)"
		}
		outboundPreview := string(modifiedBodyBytes)
		if len(outboundPreview) > 600 {
			outboundPreview = outboundPreview[:600] + "…(truncated)"
		}
		s.logger.Warn().
			Int("status_code", statusCode).
			Str("content_type", responseData.Header.Get("Content-Type")).
			Str("response_body_preview", preview).
			Strs("input_item_types", shape).
			Str("incoming_user_agent", r.UserAgent()).
			Int("input_count", inputCount).
			Str("inbound_body_preview", inboundPreview).
			Str("outbound_body_preview", outboundPreview).
			Msg("Upstream error encountered for responses request")
	}

	s.writeResponse(w, responseData, statusCode, normalizedModel, false)
}

func previewResponseBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return fmt.Sprintf("<error reading body: %v>", err)
	}

	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	preview := string(bodyBytes)
	if len(preview) > 1200 {
		return preview[:1200] + "…(truncated)"
	}
	return preview
}

func describeResponsesInputShape(body map[string]interface{}) []string {
	input, ok := body["input"].([]interface{})
	if !ok {
		return []string{"<missing>"}
	}

	shapes := make([]string, 0, len(input))
	for _, item := range input {
		switch v := item.(type) {
		case map[string]interface{}:
			typ, _ := v["type"].(string)
			if typ == "" {
				typ = "message"
			}
			role, _ := v["role"].(string)
			if role != "" {
				typ = fmt.Sprintf("%s(role=%s)", typ, role)
			}
			shapes = append(shapes, typ)
		case []interface{}:
			shapes = append(shapes, fmt.Sprintf("array(len=%d)", len(v)))
		case string:
			shapes = append(shapes, fmt.Sprintf("string(len=%d)", len(v)))
		case nil:
			shapes = append(shapes, "null")
		default:
			shapes = append(shapes, fmt.Sprintf("%T", v))
		}
	}

	if len(shapes) == 0 {
		return []string{"<empty>"}
	}
	return shapes
}

func (s *Server) makeChatGPTRequest(r *http.Request, url string, body []byte, token, accountID string) (*http.Response, int, error) {
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create proxy request: %w", err)
	}

	// Normalize token to avoid double "Bearer "
	bareToken := strings.TrimSpace(token)
	if len(bareToken) >= 7 && strings.EqualFold(bareToken[:7], "Bearer ") {
		bareToken = strings.TrimSpace(bareToken[7:])
	}

	// Set headers for ChatGPT backend
	proxyReq.Header.Set("authorization", "Bearer "+bareToken)
	proxyReq.Header.Set("version", codexClientVersion)
	proxyReq.Header.Set("openai-beta", "responses=experimental")
	proxyReq.Header.Set("session_id", newUUIDv4())
	proxyReq.Header.Set("accept", "text/event-stream")
	proxyReq.Header.Set("content-type", "application/json")
	proxyReq.Header.Set("chatgpt-account-id", accountID)
	proxyReq.Header.Set("originator", "codex_cli_rs")
	proxyReq.Header.Set("user-agent", "codex_cli_rs/"+codexClientVersion+" (Mac OS 26.3.0; arm64) Apple_Terminal/466")
	proxyReq.Header.Set("x-codex-beta-features", "multi_agent,apps,prevent_idle_sleep")
	// The CLI uses turn_id, so let's mock one
	proxyReq.Header.Set("x-codex-turn-metadata", `{"turn_id":"`+newUUIDv4()+`","sandbox":"none"}`)

	// Log outbound header summary (sanitized)
	s.logger.Info().
		Str("authorization_preview", "Bearer "+func() string {
			if len(bareToken) > 12 {
				return bareToken[:6] + "…" + bareToken[len(bareToken)-6:]
			}
			return bareToken
		}()).
		Str("chatgpt-account-id", accountID).
		Str("session_id", proxyReq.Header.Get("session_id")).
		Str("version", proxyReq.Header.Get("version")).
		Msg("Upstream request headers (sanitized)")

	resp, err := s.httpClient.Do(proxyReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send request: %w", err)
	}

	return resp, resp.StatusCode, nil
}

// makeChatGPTRequestWithRetry makes an upstream request with automatic retry on 401 errors.
func (s *Server) makeChatGPTRequestWithRetry(r *http.Request, url string, body []byte, normalizedModel string) (*http.Response, int, error) {
	makeRequest := s.makeChatGPTRequest
	if shouldUseWebSocketUpstream(normalizedModel) {
		makeRequest = s.makeChatGPTWebSocketRequest
	}

	// Get initial credentials
	token, accountID, err := s.credsFetcher.GetCredentials()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get credentials: %w", err)
	}

	// Make the first request
	resp, statusCode, err := makeRequest(r, url, body, token, accountID)
	if err != nil {
		return nil, 0, err
	}

	// If not a 401 error, return the response as-is
	if statusCode != http.StatusUnauthorized {
		return resp, statusCode, nil
	}

	// Log the 401 error and attempt token refresh
	s.logger.Warn().Msg("Received 401 Unauthorized, attempting token refresh...")

	// Close the first response body since we're going to retry
	resp.Body.Close()

	// Attempt to refresh credentials
	err = s.credsFetcher.RefreshCredentials()
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to refresh credentials after 401 error")
		// Return a 401 response since we couldn't refresh
		return nil, http.StatusUnauthorized, fmt.Errorf("token expired and refresh failed: %w", err)
	}

	s.logger.Info().Msg("Successfully refreshed credentials, retrying request...")

	// Get the new credentials
	token, accountID, err = s.credsFetcher.GetCredentials()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get refreshed credentials: %w", err)
	}

	// Retry the request with new credentials
	resp, statusCode, err = makeRequest(r, url, body, token, accountID)
	if err != nil {
		return nil, 0, fmt.Errorf("retry request failed: %w", err)
	}

	if statusCode == http.StatusUnauthorized {
		s.logger.Error().Msg("Still received 401 after token refresh, giving up")
	} else {
		s.logger.Info().Msg("Request succeeded after token refresh")
	}

	return resp, statusCode, nil
}

func (s *Server) writeResponse(w http.ResponseWriter, resp *http.Response, statusCode int, model string, convertSSE bool) {
	defer resp.Body.Close()

	// Log the response from upstream
	if statusCode != http.StatusOK {
		// For error responses, read and log the body
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			s.logger.Error().Err(err).Msg("Error reading error response body")
		} else {
			s.logger.Warn().
				Int("status_code", statusCode).
				Str("content_type", resp.Header.Get("Content-Type")).
				Str("response_body", string(responseBody)).
				Msg("Received error response from upstream API")
		}

		// Copy headers from the upstream response to our response
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		// Set the status code from the upstream response
		w.WriteHeader(statusCode)

		// Write the error response body
		_, err = w.Write(responseBody)
		if err != nil {
			s.logger.Error().Err(err).Msg("Error writing error response body to client")
		}
	} else {
		// For successful responses, just log basic info
		rawContentType := resp.Header.Get("Content-Type")
		mediaType := rawContentType
		if mt, _, err := mime.ParseMediaType(rawContentType); err == nil {
			mediaType = mt
		}
		s.logger.Info().
			Int("status_code", statusCode).
			Str("content_type", rawContentType).
			Str("content_length", resp.Header.Get("Content-Length")).
			Msg("Received response from upstream API")

		// Copy headers from upstream response to downstream response
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		// Detect streaming responses (handle charset variations)
		isStreaming := mediaType == "text/event-stream"
		if isStreaming {
			w.Header().Del("Content-Length")
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
		}

		// Set the status code from the upstream response
		w.WriteHeader(statusCode)

		flusher, canFlush := w.(http.Flusher)
		if isStreaming && canFlush {
			flusher.Flush()
		}
		if !canFlush {
			s.logger.Warn().Msg("ResponseWriter does not support flushing - streaming may be buffered")
		}

		s.logger.Debug().Msg("Starting streaming response")

		var out io.Writer = w
		if canFlush {
			out = sseFlushWriter{w: w, f: flusher}
		}

		chunkCount := 0
		streamStart := time.Now()

		// Provide lightweight visibility into streaming progress without flooding logs.
		debugFn := func(raw []byte, transformed []byte, done bool) {
			logReasoningEvent(s.logger, raw)
			if done {
				s.logger.Debug().
					Int("chunks", chunkCount).
					Dur("elapsed", time.Since(streamStart)).
					Msg("Streaming response completed")
				return
			}
			if chunkCount == 0 {
				s.logger.Debug().Msg("Streaming response in progress…")
			}
			chunkCount++
		}

		if convertSSE {
			if err := RewriteSSEStreamWithCallback(resp.Body, out, model, debugFn); err != nil {
				s.logger.Error().Err(err).Msg(fmt.Sprintf("Error rewriting SSE stream: %v", err))
				return
			}
		} else {
			if err := PassThroughSSEStream(resp.Body, out); err != nil {
				s.logger.Error().Err(err).Msg(fmt.Sprintf("Error streaming SSE response: %v", err))
				return
			}
		}
	}
}

func logToolCallInteractions(logger zerolog.Logger, requestData map[string]interface{}) {
	messages, ok := requestData["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return
	}

	type callInfo struct {
		name string
	}
	toolCalls := make(map[string]callInfo)
	const previewLimit = 200

	truncate := func(s string) string {
		s = strings.TrimSpace(s)
		if len(s) <= previewLimit {
			return s
		}
		return s[:previewLimit] + "…"
	}

	for _, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "assistant":
			toolCallsRaw, ok := m["tool_calls"].([]interface{})
			if !ok {
				continue
			}
			for _, tc := range toolCallsRaw {
				tcm, ok := tc.(map[string]interface{})
				if !ok {
					continue
				}
				callID, _ := tcm["id"].(string)
				function, _ := tcm["function"].(map[string]interface{})
				name, _ := function["name"].(string)
				args, _ := function["arguments"].(string)
				toolCalls[callID] = callInfo{name: name}
				logger.Info().
					Str("tool_call_id", callID).
					Str("tool_name", name).
					Str("arguments_preview", truncate(args)).
					Msg("LLM requested tool call")
			}
		case "tool":
			callID, _ := m["tool_call_id"].(string)
			if callID == "" {
				continue
			}
			var responseText string
			switch v := m["content"].(type) {
			case string:
				responseText = v
			case []interface{}:
				var parts []string
				for _, seg := range v {
					if sm, ok := seg.(map[string]interface{}); ok {
						if t, _ := sm["text"].(string); t != "" {
							parts = append(parts, t)
						}
					}
				}
				responseText = strings.Join(parts, "\n")
			}
			info := toolCalls[callID]
			logger.Info().
				Str("tool_call_id", callID).
				Str("tool_name", info.name).
				Str("response_preview", truncate(responseText)).
				Msg("Tool call response sent to model")
		}
	}
}

func logReasoningEvent(logger zerolog.Logger, raw []byte) {
	var evt map[string]interface{}
	if err := json.Unmarshal(raw, &evt); err != nil {
		return
	}
	typ, _ := evt["type"].(string)
	if typ == "" {
		return
	}
	isReasoning := strings.HasPrefix(typ, "response.reasoning")
	if !isReasoning && (typ == "response.output_item.added" || typ == "response.output_item.done") {
		if item, ok := evt["item"].(map[string]interface{}); ok {
			if itemType, _ := item["type"].(string); itemType == "reasoning" {
				isReasoning = true
			}
		}
	}
	if !isReasoning {
		return
	}
	preview := extractReasoningPreview(evt)
	if preview != "" {
		if len(preview) > 200 {
			preview = preview[:200] + "…"
		}
		logger.Debug().
			Str("event_type", typ).
			Str("reasoning_preview", preview).
			Msg("Reasoning event")
		return
	}
	logger.Debug().
		Str("event_type", typ).
		Msg("Reasoning event")
}

func extractReasoningPreview(evt map[string]interface{}) string {
	if delta, ok := evt["delta"].(string); ok && delta != "" {
		return delta
	}
	if text, ok := evt["text"].(string); ok && text != "" {
		return text
	}
	if part, ok := evt["part"].(map[string]interface{}); ok {
		if t, ok := part["text"].(string); ok && t != "" {
			return t
		}
	}
	if item, ok := evt["item"].(map[string]interface{}); ok {
		if _, hasEncrypted := item["encrypted_content"]; hasEncrypted {
			return "<encrypted>"
		}
		if summaryArr, ok := item["summary"].([]interface{}); ok {
			for _, entry := range summaryArr {
				if sm, ok := entry.(map[string]interface{}); ok {
					if t, ok := sm["text"].(string); ok && t != "" {
						return t
					}
				}
			}
		}
	}
	if summaryArr, ok := evt["summary"].([]interface{}); ok {
		for _, entry := range summaryArr {
			if sm, ok := entry.(map[string]interface{}); ok {
				if t, ok := sm["text"].(string); ok && t != "" {
					return t
				}
			}
		}
	}
	return ""
}

func newUUIDv4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: return random hex timestamp-like string
		now := time.Now().UnixNano()
		return fmt.Sprintf("fallback-%x", now)
	}
	// Set version (4) and variant (10)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	// Format 8-4-4-4-12
	hexs := hex.EncodeToString(b)
	parts := []string{
		hexs[0:8],
		hexs[8:12],
		hexs[12:16],
		hexs[16:20],
		hexs[20:32],
	}
	return strings.Join(parts, "-")
}

// credentialsHandler handles POST /admin/credentials for setting OAuth credentials
func (s *Server) credentialsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Check if the credentials fetcher supports OAuth
	oauthFetcher, ok := s.credsFetcher.(credentials.OAuthCredentialsFetcher)
	if !ok {
		s.logger.Error().Msg("Credentials fetcher does not support OAuth operations")
		http.Error(w, "OAuth operations not supported by current credential fetcher", http.StatusBadRequest)
		return
	}

	// Parse request body
	var reqBody struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
		UserID       string `json:"userID,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		s.logger.Error().Err(err).Msg("Failed to parse request body")
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if reqBody.AccessToken == "" || reqBody.RefreshToken == "" || reqBody.ExpiresAt == 0 {
		http.Error(w, "Missing required fields: accessToken, refreshToken, expiresAt", http.StatusBadRequest)
		return
	}

	// Update tokens
	if err := oauthFetcher.UpdateTokens(reqBody.AccessToken, reqBody.RefreshToken, reqBody.ExpiresAt); err != nil {
		s.logger.Error().Err(err).Msg("Failed to update OAuth tokens")
		http.Error(w, "Failed to update credentials", http.StatusInternalServerError)
		return
	}

	s.logger.Info().Msg("OAuth credentials updated successfully")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "Credentials updated successfully",
	})
}

// credentialsStatusHandler handles GET /admin/credentials/status
func (s *Server) credentialsStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Check if the credentials fetcher supports OAuth
	oauthFetcher, ok := s.credsFetcher.(credentials.OAuthCredentialsFetcher)
	if !ok {
		// For non-OAuth fetchers, just check if we can get credentials
		_, userID, err := s.credsFetcher.GetCredentials()

		response := map[string]interface{}{
			"type":           "basic",
			"hasCredentials": err == nil,
		}

		if err == nil {
			response["userID"] = userID
		} else {
			response["error"] = err.Error()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// Get full OAuth credentials
	creds, err := oauthFetcher.GetFullCredentials()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type":           "oauth",
			"hasCredentials": false,
			"error":          err.Error(),
		})
		return
	}

	// Calculate time until expiry
	now := time.Now().Unix() * 1000 // Convert to milliseconds
	minutesUntilExpiry := (creds.ExpiresAt - now) / 1000 / 60

	response := map[string]interface{}{
		"type":               "oauth",
		"hasCredentials":     true,
		"userID":             creds.UserID,
		"expiresAt":          creds.ExpiresAt,
		"minutesUntilExpiry": minutesUntilExpiry,
		"isExpired":          minutesUntilExpiry <= 0,
		"needsRefreshSoon":   minutesUntilExpiry <= 60, // Within 60 minute buffer
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
