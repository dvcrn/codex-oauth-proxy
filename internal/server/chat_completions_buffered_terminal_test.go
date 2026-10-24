package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const bufferedChatIncomplete = `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`
const bufferedChatFailed = `{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"context limit exceeded","param":"messages"}}}`

func bufferedTerminalTestStream(terminal string, withTools bool) string {
	events := []string{`{"type":"response.output_text.delta","delta":"partial answer"}`}
	if withTools {
		events = append(events,
			`{"type":"response.output_item.added","item":{"id":"fc_ping","type":"function_call","call_id":"call_ping","name":"ping","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_ping","delta":"{}"}`,
			`{"type":"response.output_item.done","item":{"id":"fc_ping","type":"function_call","call_id":"call_ping","name":"ping","arguments":"{}"}}`,
		)
	}
	return bufferedChatTestStream(terminal, events...)
}

func TestBufferChatCompletionFromSSETerminalOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		terminal  string
		finish    string
		errorCode string
	}{
		{name: "token limit", terminal: bufferedChatIncomplete, finish: "length"},
		{name: "content filter", terminal: `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"}}}`, finish: "content_filter"},
		{name: "unknown incomplete reason", terminal: `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"unknown_limit"}}}`, errorCode: "unknown_limit"},
		{name: "incomplete without details", terminal: `{"type":"response.incomplete","response":{}}`, errorCode: "incomplete"},
		{name: "failed response", terminal: bufferedChatFailed, errorCode: "context_length_exceeded"},
		{name: "incomplete with explicit error", terminal: `{"type":"response.incomplete","response":{"error":{"code":"context_length_exceeded","message":"context limit"},"incomplete_details":{"reason":"max_output_tokens"}}}`, errorCode: "context_length_exceeded"},
		{name: "completed event with explicit error", terminal: `{"type":"response.completed","response":{"error":{"code":"server_error","message":"model failed"}}}`, errorCode: "server_error"},
		{name: "failed without details", terminal: `{"type":"response.failed","response":{}}`, errorCode: "upstream_error"},
		{name: "flat error event", terminal: `{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`, errorCode: "rate_limit_exceeded"},
		{name: "nested error event", terminal: `{"type":"error","error":{"code":"server_error","message":"model failed"}}`, errorCode: "server_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, withTools := range []bool{false, true} {
				name := "text only"
				if withTools {
					name = "with completed tool calls"
				}
				t.Run(name, func(t *testing.T) {
					response, err := bufferChatCompletionFromSSE(strings.NewReader(bufferedTerminalTestStream(tc.terminal, withTools)), modelGPT55)
					if tc.errorCode != "" {
						require.ErrorContains(t, err, tc.errorCode)
						assert.Nil(t, response)
						return
					}
					require.NoError(t, err)
					require.Len(t, response.Choices, 1)
					assert.Equal(t, tc.finish, response.Choices[0].FinishReason)
					assert.Equal(t, "partial answer", extractCompletionText(response))
					assert.Nil(t, response.Choices[0].Message.ToolCalls, "even individually completed calls must not escape an incomplete generation")
				})
			}
		})
	}
}

func TestBufferChatCompletionFromSSEIncompleteSnapshot(t *testing.T) {
	stream := bufferedChatTestStream(`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial answer"}]},{"type":"function_call","id":"fc_ping","call_id":"call_ping","name":"ping","arguments":"{}"}]}}`)
	response, err := bufferChatCompletionFromSSE(strings.NewReader(stream), modelGPT55)
	require.NoError(t, err)
	assert.Equal(t, "length", response.Choices[0].FinishReason)
	assert.Equal(t, "partial answer", extractCompletionText(response))
	assert.Nil(t, response.Choices[0].Message.ToolCalls)
}

func TestChatCompletionsHandlerBufferedIncomplete(t *testing.T) {
	s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
	s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(bufferedTerminalTestStream(bufferedChatIncomplete, true)))}, nil
	})
	for _, streamField := range []string{"", `,"stream":false`} {
		w := httptest.NewRecorder()
		s.chatCompletionsHandler(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.5","messages":[]`+streamField+`}`)))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		var response ChatCompletionResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Len(t, response.Choices, 1)
		assert.Equal(t, "length", response.Choices[0].FinishReason)
		assert.Equal(t, "partial answer", extractCompletionText(&response))
		assert.Nil(t, response.Choices[0].Message.ToolCalls)
	}
}

func TestChatCompletionsHandlerBufferedUpstreamErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal string
		status   int
		code     string
		message  string
		kind     string
	}{
		{name: "context limit", terminal: bufferedChatFailed, status: http.StatusBadRequest, code: "context_length_exceeded", message: "context limit exceeded", kind: "invalid_request_error"},
		{name: "rate limit", terminal: `{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`, status: http.StatusTooManyRequests, code: "rate_limit_exceeded", message: "slow down", kind: "rate_limit_error"},
		{name: "server error", terminal: `{"type":"response.failed","response":{"error":{"code":"server_error","message":"model failed"}}}`, status: http.StatusInternalServerError, code: "server_error", message: "model failed", kind: "server_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
			s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(bufferedTerminalTestStream(tc.terminal, true)))}, nil
			})
			for _, streamField := range []string{"", `,"stream":false`} {
				w := httptest.NewRecorder()
				s.chatCompletionsHandler(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.5","messages":[]`+streamField+`}`)))
				require.Equal(t, tc.status, w.Code, w.Body.String())
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
				var body map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				detail, ok := body["error"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, tc.code, detail["code"])
				assert.Equal(t, tc.message, detail["message"])
				assert.Equal(t, tc.kind, detail["type"])
				if tc.code == "context_length_exceeded" {
					assert.Equal(t, "messages", detail["param"])
				}
			}
		})
	}
}

func TestMCPAskCodexBufferedTerminalOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal string
		isError  bool
		message  string
	}{
		{name: "incomplete keeps text despite unsolicited call", terminal: bufferedChatIncomplete, message: "partial answer"},
		{name: "failure preserves upstream code", terminal: bufferedChatFailed, isError: true, message: "context_length_exceeded"},
		{name: "interrupted calls still fail", isError: true, message: "upstream response ended before tool calls completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newMCPTestServer(t)
			s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
				var outbound map[string]interface{}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&outbound))
				assert.Empty(t, outbound["tools"], "ask_codex must not request tools")
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(bufferedTerminalTestStream(tc.terminal, true)))}, nil
			})
			recorder, response := postMCP(t, s, map[string]interface{}{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]interface{}{"name": "ask_codex", "arguments": map[string]interface{}{"model": modelGPT55, "prompt": "Answer without tools."}},
			}, true)
			require.Equal(t, http.StatusOK, recorder.Code)
			result, ok := response["result"].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, tc.isError, result["isError"] == true)
			assert.Contains(t, mcpResultText(t, result), tc.message)
		})
	}
}
