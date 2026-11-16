package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/rs/zerolog"
)

func TestWriteResponseRepairsFinalResponsesOutput(t *testing.T) {
	const body = "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
	s := &Server{logger: zerolog.Nop()}
	upstream := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	w := httptest.NewRecorder()

	s.writeResponse(w, upstream, http.StatusOK, "gpt-6-luna", false, true)

	if got := w.Result().Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(w.Body.String(), `"output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello"}]`) {
		t.Fatalf("completed output was not repaired: %s", w.Body.String())
	}
}

func TestChatCompletionsHandlerForwardsGPT61TerraModelAndEffort(t *testing.T) {
	var outbound map[string]interface{}
	s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
	s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&outbound); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
		}, nil
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-6.1-terra","messages":[{"role":"user","content":"Reply OK."}],"reasoning_effort":"max","stream":true}`,
	))
	s.chatCompletionsHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if outbound["model"] != "gpt-6.1-terra" {
		t.Fatalf("outbound model = %v, want %q", outbound["model"], "gpt-6.1-terra")
	}
	reasoning, ok := outbound["reasoning"].(map[string]interface{})
	if !ok || reasoning["effort"] != "max" {
		t.Fatalf("outbound reasoning = %v, want effort max", outbound["reasoning"])
	}
}

func TestResponsesHandlerForwardsGPT61TerraModelAndEffort(t *testing.T) {
	var outbound map[string]interface{}
	s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
	s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&outbound); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")),
		}, nil
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"gpt-6.1-terra","input":"Reply OK.","reasoning":{"effort":"max"},"stream":true}`,
	))
	s.responsesHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if outbound["model"] != "gpt-6.1-terra" {
		t.Fatalf("outbound model = %v, want %q", outbound["model"], "gpt-6.1-terra")
	}
	reasoning, ok := outbound["reasoning"].(map[string]interface{})
	if !ok || reasoning["effort"] != "max" {
		t.Fatalf("outbound reasoning = %v, want effort max", outbound["reasoning"])
	}
}

func TestWriteResponseResponsesStreamWithoutUpstreamContentType(t *testing.T) {
	const body = "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"
	s := &Server{logger: zerolog.Nop()}
	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	w := httptest.NewRecorder()

	s.writeResponse(w, upstream, http.StatusOK, "gpt-6-luna", false, true)

	if got := w.Result().Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := w.Body.String(); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

// responsesTestClient fakes the Responses endpoint with f and serves
// responsesTestModelsFixture on /codex/models listing requests.
type responsesTestClient func(*http.Request) (*http.Response, error)

func (f responsesTestClient) Do(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.String(), upstreamModelsURL) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(responsesTestModelsFixture)),
		}, nil
	}
	return f(r)
}

const responsesTestModelsFixture = `{"models":[
  {"slug":"gpt-7-nova","display_name":"GPT-7 Nova","visibility":"list","default_reasoning_level":"high",
   "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
  {"slug":"gpt-7-codex-max","display_name":"GPT-7 Codex Max","visibility":"list","default_reasoning_level":"medium",
   "supported_reasoning_levels":[{"effort":"medium"},{"effort":"max"}]},
  {"slug":"gpt-6.1-terra","display_name":"GPT-6.1 Terra","visibility":"list","default_reasoning_level":"low",
   "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"},{"effort":"max"}]},
  {"slug":"gpt-5.6-luna","display_name":"GPT-5.6 Luna","visibility":"list","default_reasoning_level":"medium",
   "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"},{"effort":"max"}]}
]}`

func TestHandlersKeepModelsListedUpstream(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		body       string
		handler    func(*Server, http.ResponseWriter, *http.Request)
		wantModel  string
		wantEffort string
	}{
		{
			// "max" is not among the listed levels, so the upstream default applies.
			name:       "chat completions with effort suffix",
			path:       "/v1/chat/completions",
			body:       `{"model":"gpt-7-nova-max","messages":[{"role":"user","content":"Reply OK."}],"stream":true}`,
			handler:    (*Server).chatCompletionsHandler,
			wantModel:  "gpt-7-nova",
			wantEffort: "high",
		},
		{
			name:       "responses with effort suffix",
			path:       "/v1/responses",
			body:       `{"model":"gpt-7-nova-max","input":"Reply OK.","stream":true}`,
			handler:    (*Server).responsesHandler,
			wantModel:  "gpt-7-nova",
			wantEffort: "high",
		},
		{
			name:       "slug ending in an effort word is not split",
			path:       "/v1/responses",
			body:       `{"model":"gpt-7-codex-max","input":"Reply OK.","stream":true}`,
			handler:    (*Server).responsesHandler,
			wantModel:  "gpt-7-codex-max",
			wantEffort: "medium",
		},
		{
			name:       "explicit none effort is preserved when omitted by catalog",
			path:       "/v1/responses",
			body:       `{"model":"gpt-7-nova-none","input":"Reply OK.","stream":true}`,
			handler:    (*Server).responsesHandler,
			wantModel:  "gpt-7-nova",
			wantEffort: "none",
		},
		{
			name:       "newly launched model missing from cached catalog is forwarded upstream",
			path:       "/v1/responses",
			body:       `{"model":"gpt-7-unlisted-high","input":"Reply OK.","stream":true}`,
			handler:    (*Server).responsesHandler,
			wantModel:  "gpt-7-unlisted",
			wantEffort: "high",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outbound map[string]interface{}
			s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
			s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
				if err := json.NewDecoder(r.Body).Decode(&outbound); err != nil {
					t.Fatal(err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")),
				}, nil
			})

			w := httptest.NewRecorder()
			tc.handler(s, w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if outbound["model"] != tc.wantModel {
				t.Fatalf("outbound model = %v, want %s", outbound["model"], tc.wantModel)
			}
			reasoning, ok := outbound["reasoning"].(map[string]interface{})
			if !ok || reasoning["effort"] != tc.wantEffort {
				t.Fatalf("outbound reasoning = %v, want effort %s", outbound["reasoning"], tc.wantEffort)
			}
		})
	}
}

func TestHandlersRejectBlankModels(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		body    string
		handler func(*Server, http.ResponseWriter, *http.Request)
	}{
		{
			name:    "chat completions missing model",
			path:    "/v1/chat/completions",
			body:    `{"messages":[{"role":"user","content":"Reply OK."}]}`,
			handler: (*Server).chatCompletionsHandler,
		},
		{
			name:    "chat completions effort suffix without base model",
			path:    "/v1/chat/completions",
			body:    `{"model":"-high","messages":[{"role":"user","content":"Reply OK."}]}`,
			handler: (*Server).chatCompletionsHandler,
		},
		{
			name:    "responses missing model",
			path:    "/v1/responses",
			body:    `{"input":"Reply OK."}`,
			handler: (*Server).responsesHandler,
		},
		{
			name:    "responses effort suffix without base model",
			path:    "/v1/responses",
			body:    `{"model":"-high","input":"Reply OK."}`,
			handler: (*Server).responsesHandler,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
			s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
				t.Fatalf("unexpected upstream request for %s", r.URL)
				return nil, nil
			})

			w := httptest.NewRecorder()
			tc.handler(s, w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"model_not_found"`) {
				t.Fatalf("expected model_not_found error, got %s", w.Body.String())
			}
		})
	}
}

func TestResponsesHandlerClientStreamModes(t *testing.T) {
	const upstreamSSE = "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.6-luna\",\"output\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}}\n\n"
	for _, tc := range []struct {
		name   string
		stream string
		sse    bool
	}{
		{name: "omitted"},
		{name: "false", stream: `,"stream":false`},
		{name: "true", stream: `,"stream":true`, sse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
			s.httpClient = responsesTestClient(func(r *http.Request) (*http.Response, error) {
				var outbound map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&outbound); err != nil {
					t.Fatal(err)
				}
				if outbound["stream"] != true {
					return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"detail":"Stream must be set to true"}`))}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Length": {"9999"}}, Body: io.NopCloser(strings.NewReader(upstreamSSE))}, nil
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-luna","input":[{"role":"user","content":[{"type":"input_text","text":"Reply with OK."}]}],"max_output_tokens":16`+tc.stream+`}`))
			w := httptest.NewRecorder()
			s.responsesHandler(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if tc.sse {
				if got := w.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
					t.Fatalf("Content-Type = %q", got)
				}
				if !strings.Contains(w.Body.String(), "event: response.completed") {
					t.Fatalf("missing completion event: %s", w.Body.String())
				}
				return
			}
			if got := w.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q", got)
			}
			if got := w.Header().Get("Content-Length"); got != "" {
				t.Fatalf("stale Content-Length = %q", got)
			}
			var response struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Output []struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != "resp_1" || response.Status != "completed" || len(response.Output) != 1 || len(response.Output[0].Content) != 1 || response.Output[0].Content[0].Text != "OK" || response.Usage.OutputTokens != 2 {
				t.Fatalf("unexpected buffered response: %s", w.Body.String())
			}
		})
	}
}

func TestResponsesHandlerNonStreamingUpstreamError(t *testing.T) {
	s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
	s.httpClient = responsesTestClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"detail":"invalid request"}`)),
		}, nil
	})
	w := httptest.NewRecorder()
	s.responsesHandler(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-luna","input":"hello"}`)))
	if w.Code != http.StatusBadRequest || w.Body.String() != `{"detail":"invalid request"}` {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestResponsesHandlerNonStreamingMissingFinalResponse(t *testing.T) {
	s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
	s.httpClient = responsesTestClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"))}, nil
	})
	w := httptest.NewRecorder()
	s.responsesHandler(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-luna","input":"hello"}`)))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestResponsesHandlerNonStreamingFailedResponse(t *testing.T) {
	s := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}}
	s.httpClient = responsesTestClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"status\":\"failed\",\"error\":{\"message\":\"model failure\"}}}\n\n"))}, nil
	})
	w := httptest.NewRecorder()
	s.responsesHandler(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-luna","input":"hello"}`)))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || !strings.Contains(w.Body.String(), `"status":"failed"`) {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestStreamingErrorLogMessageExcludesErrorDetails(t *testing.T) {
	var output bytes.Buffer
	server := &Server{logger: zerolog.New(&output)}
	body := io.NopCloser(iotest.ErrReader(errors.New("private-stream-error")))
	server.writeResponse(httptest.NewRecorder(), &http.Response{
		Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body,
	}, http.StatusOK, "gpt-6.1-sol", true, false)

	decoder := json.NewDecoder(&output)
	found := false
	for {
		var event struct{ Level, Message, Error string }
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if event.Level == "error" {
			found = true
			if event.Message == "" || strings.Contains(event.Message, "private-stream-error") || !strings.Contains(event.Error, "private-stream-error") {
				t.Fatalf("error details must stay in the filterable error field: %+v", event)
			}
		}
	}
	if !found {
		t.Fatal("stream read failure was not logged")
	}
}
