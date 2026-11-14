package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestResponsesStringInputIsPreserved(t *testing.T) {
	const text = "Compare Microsoft and Cursor"
	body := map[string]interface{}{"input": text}
	transformResponsesRequestBody(body, modelGPT61Sol, "low")
	want := []interface{}{map[string]interface{}{"role": "user", "content": text}}
	if !reflect.DeepEqual(body["input"], want) {
		t.Fatalf("input = %#v, want %#v", body["input"], want)
	}
	if body["store"] != false || body["stream"] != true {
		t.Fatal("Codex requests must disable storage and enable streaming")
	}
}

func TestChatRequestPreservesNames(t *testing.T) {
	const text = "Zed Cline Roo GitHub Copilot Copilot Cursor Microsoft"
	for _, role := range []string{"system", "user", "assistant", "tool"} {
		for _, format := range []string{"string", "parts"} {
			t.Run(role+"/"+format, func(t *testing.T) {
				var content interface{} = text
				if format == "parts" {
					content = []interface{}{map[string]interface{}{"type": "text", "text": text}}
				}
				body := buildCodexRequestBody(map[string]interface{}{
					"model": modelGPT61Sol,
					"messages": []interface{}{map[string]interface{}{
						"role": role, "content": content, "tool_call_id": "call_example",
					}},
				})
				input, _ := body["input"].([]interface{})
				if len(input) != 1 {
					t.Fatalf("expected one input item, got %#v", body["input"])
				}
				item := input[0].(map[string]interface{})
				var got interface{}
				if role == "tool" {
					got = item["output"]
				} else {
					got = item["content"].([]interface{})[0].(map[string]interface{})["text"]
				}
				if got != text {
					t.Fatalf("content = %q, want %q", got, text)
				}
				if role == "system" && body["instructions"] != text {
					t.Fatalf("instructions = %q, want %q", body["instructions"], text)
				}
			})
		}
	}
}

func TestResponsesRequestPreservesNames(t *testing.T) {
	const text = "Zed Cline Roo GitHub Copilot Copilot Cursor Microsoft"
	for _, instructions := range []string{"", "Keep the caller's instructions"} {
		t.Run(instructions, func(t *testing.T) {
			user := map[string]interface{}{"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "input_text", "text": text},
			}}
			toolResult := map[string]interface{}{
				"type": "function_call_output", "call_id": "call_example", "output": text,
			}
			body := map[string]interface{}{
				"instructions": instructions,
				"input": []interface{}{
					map[string]interface{}{"role": "system", "content": []interface{}{
						map[string]interface{}{"type": "input_text", "text": text},
					}},
					user, toolResult,
				},
			}
			transformResponsesRequestBody(body, modelGPT61Sol, "low")
			want := []interface{}{
				map[string]interface{}{"role": "user", "content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": text},
				}},
				map[string]interface{}{"type": "function_call_output", "call_id": "call_example", "output": text},
			}
			if instructions == "" {
				if body["instructions"] != text {
					t.Fatalf("instructions = %q, want %q", body["instructions"], text)
				}
			} else {
				if body["instructions"] != instructions {
					t.Fatalf("instructions = %q, want %q", body["instructions"], instructions)
				}
				want = append([]interface{}{map[string]interface{}{"role": "developer", "content": text}}, want...)
			}
			if !reflect.DeepEqual(body["input"], want) {
				t.Fatalf("input = %#v, want %#v", body["input"], want)
			}
		})
	}
}

func TestStreamingResponseHeaders(t *testing.T) {
	const stream = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: [DONE]\n\n"
	for _, convert := range []bool{false, true} {
		for _, contentType := range []string{"", "text/plain; charset=utf-8", "text/event-stream", "text/event-stream; charset=utf-8"} {
			t.Run(strconv.FormatBool(convert)+"/"+contentType, func(t *testing.T) {
				writer := httptest.NewRecorder()
				reader := strings.NewReader(stream)
				body := io.NopCloser(headerReadFunc(func(p []byte) (int, error) {
					if !writer.Flushed {
						t.Error("stream headers must be flushed before reading upstream data")
					}
					return reader.Read(p)
				}))
				headers := http.Header{
					"Content-Length": {strconv.Itoa(len(stream))},
					"Cache-Control":  {"public, max-age=3600"}, "Connection": {"close"},
				}
				if contentType != "" {
					headers.Set("Content-Type", contentType)
				}
				server := &Server{logger: zerolog.Nop()}
				server.writeResponse(writer, &http.Response{Header: headers, Body: body}, http.StatusOK, modelGPT61Sol, convert, !convert)
				response := writer.Result()
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("status = %d", response.StatusCode)
				}
				for key, want := range map[string]string{
					"Content-Type":  "text/event-stream; charset=utf-8",
					"Cache-Control": "no-cache", "Connection": "keep-alive", "Content-Length": "",
				} {
					if got := response.Header.Get(key); got != want {
						t.Errorf("%s = %q, want %q", key, got, want)
					}
				}
				if !convert && writer.Body.String() != stream {
					t.Fatal("native Responses SSE payload changed")
				}
				if convert && (!strings.Contains(writer.Body.String(), `"content":"OK"`) || !strings.Contains(writer.Body.String(), "data: [DONE]")) {
					t.Fatal("Chat Completions SSE conversion failed")
				}
			})
		}
	}
}

type headerReadFunc func([]byte) (int, error)

func (f headerReadFunc) Read(p []byte) (int, error) { return f(p) }

func TestStreamingErrorLogMessageExcludesErrorDetails(t *testing.T) {
	var output bytes.Buffer
	server := &Server{logger: zerolog.New(&output)}
	body := io.NopCloser(headerReadFunc(func([]byte) (int, error) {
		return 0, errors.New("private-stream-error")
	}))
	server.writeResponse(httptest.NewRecorder(), &http.Response{
		Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body,
	}, http.StatusOK, modelGPT61Sol, true, false)

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
			if event.Message != "Error rewriting SSE stream" || !strings.Contains(event.Error, "private-stream-error") {
				t.Fatalf("error details must stay in the filterable error field: %+v", event)
			}
		}
	}
	if !found {
		t.Fatal("stream read failure was not logged")
	}
}

func TestUpstreamErrorResponseUnchanged(t *testing.T) {
	for _, convert := range []bool{false, true} {
		for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
			for _, contentType := range []string{"application/json", "text/plain; charset=utf-8"} {
				t.Run(strconv.FormatBool(convert)+"/"+strconv.Itoa(status)+"/"+contentType, func(t *testing.T) {
					payload := `{"error":{"message":"upstream rejected request"}}`
					if strings.HasPrefix(contentType, "text/plain") {
						payload = "upstream rejected request"
					}
					headers := http.Header{
						"Content-Type": {contentType}, "Content-Length": {strconv.Itoa(len(payload))},
						"Cache-Control": {"no-store"}, "Retry-After": {"7"},
					}
					writer := httptest.NewRecorder()
					server := &Server{logger: zerolog.Nop()}
					server.writeResponse(writer, &http.Response{Header: headers, Body: io.NopCloser(strings.NewReader(payload))}, status, modelGPT61Sol, convert, !convert)
					response := writer.Result()
					defer response.Body.Close()
					if response.StatusCode != status || writer.Body.String() != payload || !reflect.DeepEqual(response.Header, headers) {
						t.Fatal("upstream error status, headers or body changed")
					}
				})
			}
		}
	}
}
