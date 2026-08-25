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

// stubCredentialsFetcher satisfies credentials.CredentialsFetcher without
// touching disk or the network. The MCP protocol tests never reach an upstream
// call.
type stubCredentialsFetcher struct{}

func (stubCredentialsFetcher) GetCredentials() (string, string, error) {
	return "test-token", "test-account", nil
}
func (stubCredentialsFetcher) RefreshCredentials() error { return nil }

// stubModelsHTTPClient serves a canned /backend-api/codex/models payload so the
// model-listing tests do not depend on the network or a live account.
type stubModelsHTTPClient struct {
	body       string
	statusCode int
	err        error
	calls      int
}

func (c *stubModelsHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	status := c.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// upstreamModelsFixture mirrors the shape the ChatGPT backend returns, including
// a hidden model that must be filtered out of the advertised list.
const upstreamModelsFixture = `{"models":[
  {"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list",
   "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"}]},
  {"slug":"gpt-5.4","display_name":"GPT-5.4","visibility":"list",
   "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
  {"slug":"codex-auto-review","display_name":"Codex Auto Review","visibility":"hide",
   "supported_reasoning_levels":[{"effort":"low"}]}
]}`

const testAdminKey = "test-admin-key"

func newMCPTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("ADMIN_API_KEY", testAdminKey)
	return New(zerolog.Nop(), stubCredentialsFetcher{})
}

// postMCP sends one JSON-RPC message to /mcp and returns the decoded response.
func postMCP(t *testing.T, srv *Server, payload map[string]interface{}, authorized bool) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authorized {
		req.Header.Set("Authorization", "Bearer "+testAdminKey)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		return rec, nil
	}

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	return rec, decoded
}

func TestMCPEndpointRequiresAdminKey(t *testing.T) {
	srv := newMCPTestServer(t)

	rec, _ := postMCP(t, srv, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}, false)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMCPInitialize(t *testing.T) {
	srv := newMCPTestServer(t)

	rec, resp := postMCP(t, srv, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "test-client", "version": "0.0.1"},
		},
	}, true)

	require.Equal(t, http.StatusOK, rec.Code)
	result, ok := resp["result"].(map[string]interface{})
	require.True(t, ok, "expected a result object, got %v", resp)

	serverInfo, ok := result["serverInfo"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, mcpServerName, serverInfo["name"])
	assert.Equal(t, mcpServerVersion, serverInfo["version"])
}

func TestMCPToolsList(t *testing.T) {
	srv := newMCPTestServer(t)

	rec, resp := postMCP(t, srv, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}, true)

	require.Equal(t, http.StatusOK, rec.Code)
	result, ok := resp["result"].(map[string]interface{})
	require.True(t, ok, "expected a result object, got %v", resp)

	rawTools, ok := result["tools"].([]interface{})
	require.True(t, ok)

	tools := make(map[string]map[string]interface{}, len(rawTools))
	for _, rawTool := range rawTools {
		tool, ok := rawTool.(map[string]interface{})
		require.True(t, ok)
		name, ok := tool["name"].(string)
		require.True(t, ok)
		tools[name] = tool
	}

	require.Contains(t, tools, "ask_codex")
	require.Contains(t, tools, "ask_codex_models")

	// Both descriptions must tell the caller the answers come from Codex.
	for name, tool := range tools {
		description, ok := tool["description"].(string)
		require.True(t, ok, "tool %s has no description", name)
		assert.Contains(t, description, "ChatGPT Codex CLI", "tool %s", name)
	}

	schema, ok := tools["ask_codex"]["inputSchema"].(map[string]interface{})
	require.True(t, ok)
	assert.ElementsMatch(t, []interface{}{"model", "prompt"}, schema["required"])
}

func TestMCPAskCodexRejectsBlankInput(t *testing.T) {
	srv := newMCPTestServer(t)

	testCases := []struct {
		name        string
		args        map[string]interface{}
		wantMessage string
	}{
		{
			name:        "blank prompt",
			args:        map[string]interface{}{"model": modelGPT55, "prompt": "   "},
			wantMessage: "prompt is required",
		},
		{
			name:        "blank model",
			args:        map[string]interface{}{"model": "  ", "prompt": "hello"},
			wantMessage: "model is required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rec, resp := postMCP(t, srv, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "tools/call",
				"params":  map[string]interface{}{"name": "ask_codex", "arguments": tc.args},
			}, true)

			require.Equal(t, http.StatusOK, rec.Code)
			result, ok := resp["result"].(map[string]interface{})
			require.True(t, ok, "expected a result object, got %v", resp)

			// Tool-level failures come back as isError, not a JSON-RPC error.
			assert.Equal(t, true, result["isError"])
			assert.Contains(t, mcpResultText(t, result), tc.wantMessage)
		})
	}
}

func TestMCPAskCodexModels(t *testing.T) {
	srv := newMCPTestServer(t)
	stub := &stubModelsHTTPClient{body: upstreamModelsFixture}
	srv.httpClient = stub

	out, err := srv.mcpAskCodexModels(t.Context(), askCodexModelsInput{})
	require.NoError(t, err)

	require.Len(t, out.Models, 2)

	byID := make(map[string]askCodexModel, len(out.Models))
	var previousID string
	for _, model := range out.Models {
		assert.Greater(t, model.ID, previousID, "models must be sorted by ID")
		previousID = model.ID
		byID[model.ID] = model
	}

	_, hidden := byID["codex-auto-review"]
	assert.False(t, hidden, "hidden models must not be advertised")

	gpt55, ok := byID["gpt-5.5"]
	require.True(t, ok)
	assert.Equal(t, "GPT-5.5", gpt55.DisplayName)
	assert.Equal(t, []string{"low", "medium", "high", "xhigh"}, gpt55.ReasoningEfforts)

	_, err = srv.mcpAskCodexModels(t.Context(), askCodexModelsInput{})
	require.NoError(t, err)
	assert.Equal(t, 1, stub.calls, "expected the model list to be cached")
}

func TestMCPAskCodexModelsUpstreamFailure(t *testing.T) {
	srv := newMCPTestServer(t)
	srv.httpClient = &stubModelsHTTPClient{
		statusCode: http.StatusUnauthorized,
		body:       `{"detail":"Could not parse your authentication token."}`,
	}

	_, err := srv.mcpAskCodexModels(t.Context(), askCodexModelsInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream returned 401")
}

func mcpResultText(t *testing.T, result map[string]interface{}) string {
	t.Helper()

	content, ok := result["content"].([]interface{})
	require.True(t, ok)

	var text string
	for _, rawPart := range content {
		part, ok := rawPart.(map[string]interface{})
		require.True(t, ok)
		if partText, ok := part["text"].(string); ok {
			text += partText
		}
	}
	return text
}

func TestExtractCompletionText(t *testing.T) {
	testCases := []struct {
		name       string
		completion *ChatCompletionResponse
		want       string
	}{
		{
			name:       "nil completion",
			completion: nil,
			want:       "",
		},
		{
			name:       "no choices",
			completion: &ChatCompletionResponse{},
			want:       "",
		},
		{
			name: "joins choice content",
			completion: &ChatCompletionResponse{
				Choices: []ChatCompletionChoice{
					{Message: ChatMessage{Role: "assistant", Content: "Hello, "}},
					{Message: ChatMessage{Role: "assistant", Content: "world!"}},
				},
			},
			want: "Hello, world!",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractCompletionText(tc.completion))
		})
	}
}

// TestMCPAllowsNonLoopbackHost guards the DisableLocalhostProtection option.
// The SDK only applies DNS rebinding protection when the request carries a
// loopback http.LocalAddrContextKey, which a real listener sets and
// httptest.NewRequest does not - so this has to go through httptest.NewServer
// to exercise the check at all.
func TestMCPAllowsNonLoopbackHost(t *testing.T) {
	srv := newMCPTestServer(t)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+testAdminKey)
	// A public hostname forwarded by a tunnel, which the rebinding check would
	// otherwise reject with 403 because the listener itself is loopback.
	req.Host = "proxy.example.com"

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", respBody)

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(respBody, &decoded))
	result, ok := decoded["result"].(map[string]interface{})
	require.True(t, ok, "expected a result object, got %v", decoded)
	require.NotEmpty(t, result["tools"])
}
