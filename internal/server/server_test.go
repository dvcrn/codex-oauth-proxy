package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

type responsesTestClient func(*http.Request) (*http.Response, error)

func (f responsesTestClient) Do(r *http.Request) (*http.Response, error) { return f(r) }

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
