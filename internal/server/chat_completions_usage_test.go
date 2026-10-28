package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionsHandlerPreservesUsage(t *testing.T) {
	const zeroUsage = `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`
	for _, tc := range []struct {
		name  string
		usage string
		want  string
	}{
		{
			name: "responses totals and details",
			usage: `,"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,
				"input_tokens_details":{"cached_tokens":80,"audio_tokens":2},
				"output_tokens_details":{"reasoning_tokens":12,"audio_tokens":1}}`,
			want: `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,
				"prompt_tokens_details":{"cached_tokens":80,"audio_tokens":2},
				"completion_tokens_details":{"reasoning_tokens":12,"audio_tokens":1}}`,
		},
		{
			name:  "derive missing total",
			usage: `,"usage":{"input_tokens":100,"output_tokens":20}`,
			want:  `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}`,
		},
		{
			name:  "explicit zero usage is preserved",
			usage: `,"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}`,
			want:  zeroUsage,
		},
		{
			name:  "derive zero total",
			usage: `,"usage":{"input_tokens":0,"output_tokens":0}`,
			want:  zeroUsage,
		},
		{
			name:  "partial input usage is unavailable",
			usage: `,"usage":{"input_tokens":3}`,
		},
		{
			name:  "partial output usage is unavailable",
			usage: `,"usage":{"output_tokens":2}`,
		},
		{
			name:  "total only usage is unavailable",
			usage: `,"usage":{"total_tokens":5}`,
		},
		{
			name:  "responses details without counters are unavailable",
			usage: `,"usage":{"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":1}}`,
		},
		{
			name:  "chat details without counters are unavailable",
			usage: `,"usage":{"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}`,
		},
		{
			name:  "details and a partial counter are unavailable",
			usage: `,"usage":{"input_tokens":3,"total_tokens":5,"input_tokens_details":{"cached_tokens":2}}`,
		},
		{
			name:  "null counter is unavailable",
			usage: `,"usage":{"input_tokens":null,"output_tokens":2}`,
		},
		{
			name:  "incorrectly typed counter is unavailable",
			usage: `,"usage":{"input_tokens":"3","output_tokens":2}`,
		},
		{
			name: "chat totals and details",
			usage: `,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,
				"prompt_tokens_details":{"cached_tokens":4},
				"completion_tokens_details":{"reasoning_tokens":2,"accepted_prediction_tokens":1}}`,
			want: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,
				"prompt_tokens_details":{"cached_tokens":4},
				"completion_tokens_details":{"reasoning_tokens":2,"accepted_prediction_tokens":1}}`,
		},
		{
			name: "chat fields take precedence including explicit zeros",
			usage: `,"usage":{"prompt_tokens":0,"input_tokens":100,"completion_tokens":0,"output_tokens":20,"total_tokens":0,
				"prompt_tokens_details":{"cached_tokens":0},"input_tokens_details":{"cached_tokens":80},
				"completion_tokens_details":{"reasoning_tokens":0},"output_tokens_details":{"reasoning_tokens":12}}`,
			want: `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,
				"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}`,
		},
		{
			name: "preserve zero details",
			usage: `,"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,
				"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`,
			want: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,
				"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}`,
		},
		{
			name:  "omit null details",
			usage: `,"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":null,"output_tokens_details":null}`,
			want:  `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`,
		},
		{
			name: "preserve empty detail objects",
			usage: `,"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,
				"input_tokens_details":{},"output_tokens_details":{}}`,
			want: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,
				"prompt_tokens_details":{},"completion_tokens_details":{}}`,
		},
		{
			name:  "explicit total is preserved",
			usage: `,"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":16}`,
			want:  `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":16}`,
		},
		{name: "missing usage"},
		{name: "null usage", usage: `,"usage":null`},
		{name: "empty usage", usage: `,"usage":{}`},
		{name: "non-object usage", usage: `,"usage":[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []struct {
				name   string
				stream string
				sse    bool
			}{
				{name: "stream omitted"},
				{name: "stream false", stream: `,"stream":false`},
				{name: "stream true", stream: `,"stream":true`, sse: true},
			} {
				t.Run(mode.name, func(t *testing.T) {
					upstream := "data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_usage\"}}\n\n" +
						"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"delta\":\"OK\"}\n\n" +
						fmt.Sprintf("data: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"status\":\"completed\"%s}}\n\n", strings.ReplaceAll(tc.usage, "\n", "")) +
						"data: [DONE]\n\n"
					s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
					s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
						var outbound map[string]interface{}
						require.NoError(t, json.NewDecoder(r.Body).Decode(&outbound))
						require.Equal(t, true, outbound["stream"], "upstream always streams")
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": {"text/event-stream"}},
							Body:       io.NopCloser(strings.NewReader(upstream)),
						}, nil
					})
					body := `{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"Reply with OK."}]` + mode.stream + `}`
					w := httptest.NewRecorder()
					s.chatCompletionsHandler(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
					require.Equal(t, http.StatusOK, w.Code, w.Body.String())

					var usage json.RawMessage
					if mode.sse {
						require.Equal(t, "text/event-stream; charset=utf-8", w.Header().Get("Content-Type"))
						require.Contains(t, w.Body.String(), "data: [DONE]\n\n")
						for _, line := range strings.Split(w.Body.String(), "\n") {
							data, ok := strings.CutPrefix(line, "data: ")
							if !ok || data == "[DONE]" {
								continue
							}
							var chunk struct {
								Usage json.RawMessage `json:"usage"`
							}
							require.NoError(t, json.Unmarshal([]byte(data), &chunk))
							if len(chunk.Usage) > 0 {
								usage = chunk.Usage
							}
						}
					} else {
						require.Equal(t, "application/json", w.Header().Get("Content-Type"))
						var response struct {
							Usage json.RawMessage `json:"usage"`
						}
						require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
						usage = response.Usage
					}
					want := tc.want
					if mode.sse && want == "" {
						// Streaming retains its zero-usage compatibility fallback.
						want = zeroUsage
					}
					if want == "" {
						require.Empty(t, usage, "unavailable buffered usage must be omitted: %s", w.Body.String())
					} else {
						require.NotEmpty(t, usage, "usage missing from response: %s", w.Body.String())
						require.JSONEq(t, want, string(usage))
					}
				})
			}
		})
	}
}

func TestBufferChatCompletionUsageAtEOF(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\n\n", "\n\ndata: [DONE]\n\n"} {
		t.Run(fmt.Sprintf("suffix=%q", suffix), func(t *testing.T) {
			body := `data: {"type":"response.completed","sequence_number":1,"response":{"usage":{"input_tokens":3,"output_tokens":2}}}` + suffix
			response, err := bufferChatCompletionFromSSE(strings.NewReader(body), "gpt-5.6-luna")
			require.NoError(t, err)
			encoded, err := json.Marshal(response)
			require.NoError(t, err)
			var result struct {
				Usage json.RawMessage `json:"usage"`
			}
			require.NoError(t, json.Unmarshal(encoded, &result))
			require.NotEmpty(t, result.Usage)
			require.JSONEq(t, `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`, string(result.Usage))
		})
	}
}

func TestBufferChatCompletionOmitsUsageWithoutCompletion(t *testing.T) {
	response, err := bufferChatCompletionFromSSE(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"OK\"}\n\ndata: [DONE]\n\n"), "gpt-5.6-luna")
	require.NoError(t, err)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &result))
	require.NotContains(t, result, "usage")
}
