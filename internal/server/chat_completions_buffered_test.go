package server

import (
	"bytes"
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

const (
	bufferedChatCompleted  = `{"type":"response.completed","sequence_number":20}`
	bufferedChatIncomplete = `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`
	bufferedChatFailed     = `{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"context limit exceeded","param":"messages"}}}`
)

func bufferedChatTestStream(terminal string, events ...string) string {
	events = append([]string{`{"type":"response.created","sequence_number":1,"response":{"id":"resp_buffered"}}`}, events...)
	if terminal != "" {
		events = append(events, terminal, "[DONE]")
	}
	return "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
}

func bufferedToolCallTestStream(terminal string) string {
	return bufferedChatTestStream(terminal,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":1,"item":{"id":"fc_sum","type":"function_call","call_id":"call_sum","name":"sum_two_numbers","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_sum","output_index":1,"delta":"{\"a\":1,"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc_sum","output_index":1,"delta":"\"b\":2}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":5,"item_id":"fc_sum","output_index":1,"arguments":"{\"a\":1,\"b\":2}"}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":1,"item":{"id":"fc_sum","type":"function_call","call_id":"call_sum","name":"sum_two_numbers","arguments":"{\"a\":1,\"b\":2}"}}`,
	)
}

func TestBufferChatCompletionFromSSE(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stream  string
		message string
		finish  string
	}{
		{
			name:    "empty text remains an empty string",
			stream:  bufferedChatTestStream(bufferedChatCompleted),
			message: `{"role":"assistant","content":""}`,
			finish:  "stop",
		},
		{
			name: "text only omits tool_calls",
			stream: bufferedChatTestStream(bufferedChatCompleted,
				`{"type":"response.output_text.delta","sequence_number":2,"delta":"Hello"}`,
				`{"type":"response.output_text.delta","sequence_number":3,"delta":"\n world"}`,
			),
			message: `{"role":"assistant","content":"Hello\n world"}`,
			finish:  "stop",
		},
		{
			name:    "tool call with fragmented arguments",
			stream:  bufferedToolCallTestStream(bufferedChatCompleted),
			message: `{"role":"assistant","content":null,"tool_calls":[{"id":"call_sum","type":"function","function":{"name":"sum_two_numbers","arguments":"{\"a\":1,\"b\":2}"}}]}`,
			finish:  "tool_calls",
		},
		{
			name: "text and interleaved tool calls",
			stream: bufferedChatTestStream(bufferedChatCompleted,
				`{"type":"response.output_text.delta","sequence_number":2,"delta":"Checking both."}`,
				`{"type":"response.output_item.added","sequence_number":3,"output_index":2,"item":{"id":"fc_sum","type":"function_call","call_id":"call_sum","name":"sum_two_numbers","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc_sum","output_index":2,"delta":"{\"a\":1,"}`,
				`{"type":"response.output_item.added","sequence_number":5,"output_index":4,"item":{"id":"fc_weather","type":"function_call","call_id":"call_weather","name":"get_weather","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","sequence_number":6,"item_id":"fc_weather","output_index":4,"delta":"{\"city\":\"Montr"}`,
				`{"type":"response.function_call_arguments.done","sequence_number":7,"item_id":"fc_weather","output_index":4,"arguments":"{\"city\":\"Montréal\\nNorth\"}"}`,
				`{"type":"response.function_call_arguments.delta","sequence_number":8,"item_id":"fc_sum","output_index":2,"delta":"\"b\":2}"}`,
			),
			message: `{"role":"assistant","content":"Checking both.","tool_calls":[{"id":"call_sum","type":"function","function":{"name":"sum_two_numbers","arguments":"{\"a\":1,\"b\":2}"}},{"id":"call_weather","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Montréal\\nNorth\"}"}}]}`,
			finish:  "tool_calls",
		},
		{
			name: "arguments.done without argument deltas",
			stream: bufferedChatTestStream(bufferedChatCompleted,
				`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"fc_ping","type":"function_call","call_id":"call_ping","name":"ping","arguments":""}}`,
				`{"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_ping","arguments":"{}"}`,
			),
			message: `{"role":"assistant","content":null,"tool_calls":[{"id":"call_ping","type":"function","function":{"name":"ping","arguments":"{}"}}]}`,
			finish:  "tool_calls",
		},
		{
			name: "output_item.done without argument deltas",
			stream: bufferedChatTestStream(bufferedChatCompleted,
				`{"type":"response.output_item.added","sequence_number":2,"item":{"id":"fc_ping","type":"function_call","call_id":"call_ping","name":"ping","arguments":""}}`,
				`{"type":"response.output_item.done","sequence_number":3,"item":{"id":"fc_ping","type":"function_call","call_id":"call_ping","name":"ping","arguments":"{}"}}`,
			),
			message: `{"role":"assistant","content":null,"tool_calls":[{"id":"call_ping","type":"function","function":{"name":"ping","arguments":"{}"}}]}`,
			finish:  "tool_calls",
		},
		{
			name: "final arguments replace partial deltas",
			stream: bufferedChatTestStream(bufferedChatCompleted,
				`{"type":"response.output_item.added","item":{"id":"fc_ping","type":"function_call","call_id":"call_ping","name":"ping","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","item_id":"fc_ping","delta":"{\"value\":"}`,
				`{"type":"response.function_call_arguments.done","item_id":"fc_ping","arguments":"{\"value\":1}"}`,
				`{"type":"response.output_item.done","item":{"id":"fc_ping","type":"function_call","call_id":"call_ping","name":"ping","arguments":"{\"value\":1}"}}`,
			),
			message: `{"role":"assistant","content":null,"tool_calls":[{"id":"call_ping","type":"function","function":{"name":"ping","arguments":"{\"value\":1}"}}]}`,
			finish:  "tool_calls",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := bufferChatCompletionFromSSE(strings.NewReader(tc.stream), "gpt-5")
			require.NoError(t, err)
			assert.Equal(t, "chatcmpl-resp_buffered", response.ID)
			assert.Equal(t, "chat.completion", response.Object)
			assert.Equal(t, "gpt-5", response.Model)
			require.Len(t, response.Choices, 1)
			assert.Equal(t, 0, response.Choices[0].Index)
			assert.Equal(t, tc.finish, response.Choices[0].FinishReason)
			if tc.finish == "stop" {
				assert.Nil(t, response.Choices[0].Message.ToolCalls, "text-only replies should leave tool calls nil")
			}
			message, err := json.Marshal(response.Choices[0].Message)
			require.NoError(t, err)
			assert.JSONEq(t, tc.message, string(message))
		})
	}
}

func TestBufferChatCompletionFromSSERejectsUnfinishedToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal string
	}{
		{name: "EOF without response completion"},
		{name: "DONE without response completion", terminal: "[DONE]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Individual calls are complete, but the overall generation is not.
			stream := bufferedToolCallTestStream(tc.terminal)
			response, err := bufferChatCompletionFromSSE(strings.NewReader(stream), "gpt-5")
			require.ErrorContains(t, err, "upstream response ended before tool calls completed")
			assert.Nil(t, response, "unsuccessful generations must not publish executable calls")
		})
	}
}

func TestChatCompletionsHandlerNonStreamingToolRoundTrip(t *testing.T) {
	for _, explicitStream := range []bool{false, true} {
		name := "stream omitted"
		if explicitStream {
			name = "stream false"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
			s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
				calls++
				var outbound map[string]interface{}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&outbound))
				assert.Equal(t, true, outbound["stream"], "Codex upstream must still receive streaming requests")
				body := bufferedToolCallTestStream(bufferedChatCompleted)
				if calls == 2 {
					input, ok := outbound["input"].([]interface{})
					require.True(t, ok)
					var toolHistory []interface{}
					for _, item := range input {
						message, ok := item.(map[string]interface{})
						require.True(t, ok)
						if message["type"] == "function_call" || message["type"] == "function_call_output" {
							toolHistory = append(toolHistory, message)
						}
					}
					historyJSON, err := json.Marshal(toolHistory)
					require.NoError(t, err)
					assert.JSONEq(t, `[{"type":"function_call","call_id":"call_sum","name":"sum_two_numbers","arguments":"{\"a\":1,\"b\":2}"},{"type":"function_call_output","call_id":"call_sum","output":"3"}]`, string(historyJSON))
					body = bufferedChatTestStream(bufferedChatCompleted, `{"type":"response.output_text.delta","sequence_number":2,"delta":"3"}`)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			})

			userMessage := map[string]interface{}{"role": "user", "content": "Use sum_two_numbers to add 1 and 2."}
			request := map[string]interface{}{
				"model":    "gpt-5",
				"messages": []interface{}{userMessage},
				"tools": []interface{}{
					map[string]interface{}{
						"type": "function",
						"function": map[string]interface{}{
							"name": "sum_two_numbers",
							"parameters": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"a": map[string]interface{}{"type": "integer"},
									"b": map[string]interface{}{"type": "integer"},
								},
								"required": []string{"a", "b"},
							},
						},
					},
				},
			}
			if explicitStream {
				request["stream"] = false
			}
			send := func() *httptest.ResponseRecorder {
				payload, err := json.Marshal(request)
				require.NoError(t, err)
				w := httptest.NewRecorder()
				s.chatCompletionsHandler(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload)))
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
				return w
			}

			first := send()
			type completionResponse struct {
				Choices []struct {
					Message      map[string]interface{} `json:"message"`
					FinishReason string                 `json:"finish_reason"`
				} `json:"choices"`
			}
			var response completionResponse
			require.NoError(t, json.Unmarshal(first.Body.Bytes(), &response))
			require.Len(t, response.Choices, 1)
			assert.Equal(t, "tool_calls", response.Choices[0].FinishReason)
			toolCalls, ok := response.Choices[0].Message["tool_calls"].([]interface{})
			require.True(t, ok, "non-streaming response must include tool_calls: %s", first.Body.String())
			require.Len(t, toolCalls, 1)
			toolCall, ok := toolCalls[0].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, "call_sum", toolCall["id"])

			request["messages"] = []interface{}{
				userMessage,
				response.Choices[0].Message,
				map[string]interface{}{"role": "tool", "tool_call_id": toolCall["id"], "content": "3"},
			}
			second := send()
			var finalResponse completionResponse
			require.NoError(t, json.Unmarshal(second.Body.Bytes(), &finalResponse))
			require.Len(t, finalResponse.Choices, 1)
			assert.Equal(t, "stop", finalResponse.Choices[0].FinishReason)
			assert.Equal(t, "3", finalResponse.Choices[0].Message["content"])
			assert.NotContains(t, finalResponse.Choices[0].Message, "tool_calls")
			assert.Equal(t, 2, calls)
		})
	}
}

func TestBufferChatCompletionFromSSEFraming(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream string
	}{
		{
			name: "CRLF and comments",
			stream: ": heartbeat\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\r\n\r\n" +
				"data: " + bufferedChatCompleted + "\r\n\r\n",
		},
		{
			name: "multiline data",
			stream: "data: {\"type\":\"response.output_text.delta\",\n" +
				"data: \"delta\":\"Hello\"}\n\n" +
				"data: " + bufferedChatCompleted + "\n\n",
		},
		{
			name: "final event without trailing newline",
			stream: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n" +
				"data: " + bufferedChatCompleted,
		},
		{
			name: "ignore unrelated event payload shapes",
			stream: bufferedChatTestStream(bufferedChatCompleted,
				`{"type":"response.future_event","delta":{"value":"ignored"},"item":[1,2]}`,
				`{"type":"response.output_text.delta","delta":"Hello"}`,
			),
		},
		{
			name: "ignore events after the terminal response",
			stream: bufferedChatTestStream(bufferedChatCompleted, `{"type":"response.output_text.delta","delta":"Hello"}`) +
				"data: malformed trailing event\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := bufferChatCompletionFromSSE(strings.NewReader(tc.stream), modelGPT55)
			require.NoError(t, err)
			assert.Equal(t, "Hello", extractCompletionText(response))
			assert.Equal(t, "stop", response.Choices[0].FinishReason)
		})
	}
}

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
		errorType string
	}{
		{name: "token limit", terminal: bufferedChatIncomplete, finish: "length"},
		{name: "content filter", terminal: `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"}}}`, finish: "content_filter"},
		{name: "unknown incomplete reason", terminal: `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"unknown_limit"}}}`, errorCode: "unknown_limit"},
		{name: "incomplete without details", terminal: `{"type":"response.incomplete","response":{}}`, errorCode: "incomplete"},
		{name: "failed response", terminal: bufferedChatFailed, errorCode: "context_length_exceeded"},
		{name: "incomplete with explicit error", terminal: `{"type":"response.incomplete","response":{"error":{"code":"context_length_exceeded","message":"context limit"},"incomplete_details":{"reason":"max_output_tokens"}}}`, errorCode: "context_length_exceeded"},
		{name: "completed event with explicit error", terminal: `{"type":"response.completed","response":{"error":{"code":"server_error","message":"model failed"}}}`, errorCode: "server_error"},
		{name: "failed without details", terminal: `{"type":"response.failed","response":{}}`, errorCode: "upstream_error", errorType: "server_error"},
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
						if tc.errorType != "" {
							var upstreamError *bufferedUpstreamError
							require.ErrorAs(t, err, &upstreamError)
							assert.Equal(t, tc.errorType, upstreamError.Type)
						}
						assert.Nil(t, response)
						return
					}
					require.NoError(t, err)
					assert.Equal(t, tc.finish, response.Choices[0].FinishReason)
					assert.Equal(t, "partial answer", extractCompletionText(response))
					assert.Nil(t, response.Choices[0].Message.ToolCalls, "incomplete generations must not publish calls")
				})
			}
		})
	}
}

func TestChatCompletionsHandlerBufferedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal string
		status   int
		code     string
		message  string
		kind     string
	}{
		{name: "token limit", terminal: bufferedChatIncomplete, status: http.StatusOK},
		{name: "context limit", terminal: bufferedChatFailed, status: http.StatusBadRequest, code: "context_length_exceeded", message: "context limit exceeded", kind: "invalid_request_error"},
		{name: "rate limit", terminal: `{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`, status: http.StatusTooManyRequests, code: "rate_limit_exceeded", message: "slow down", kind: "rate_limit_error"},
		{name: "server error", terminal: `{"type":"response.failed","response":{"error":{"code":"server_error","message":"model failed"}}}`, status: http.StatusInternalServerError, code: "server_error", message: "model failed", kind: "server_error"},
		{name: "failed without details", terminal: `{"type":"response.failed","response":{}}`, status: http.StatusInternalServerError, code: "upstream_error", message: "upstream response failed", kind: "server_error"},
		{name: "flat failed response", terminal: `{"type":"response.failed","code":"context_length_exceeded","message":"flat context limit","param":"messages"}`, status: http.StatusBadRequest, code: "context_length_exceeded", message: "flat context limit", kind: "invalid_request_error"},
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
				if tc.status == http.StatusOK {
					var response ChatCompletionResponse
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
					require.Len(t, response.Choices, 1)
					assert.Equal(t, "length", response.Choices[0].FinishReason)
					assert.Equal(t, "partial answer", extractCompletionText(&response))
					assert.Nil(t, response.Choices[0].Message.ToolCalls)
					continue
				}
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
