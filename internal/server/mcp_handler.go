package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpServerName    = "ask-codex"
	mcpServerVersion = "0.1.0"
)

// askCodexInput is the input for the ask_codex tool.
type askCodexInput struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// askCodexOutput is the structured result of the ask_codex tool. Model is the
// model that actually served the request, which can differ from the requested
// one because model IDs are normalized before they reach the Codex backend.
type askCodexOutput struct {
	RequestedModel string `json:"requested_model"`
	Model          string `json:"model"`
	Text           string `json:"text"`
}

// askCodexModelsInput is the (empty) input for the ask_codex_models tool.
type askCodexModelsInput struct{}

type askCodexModel struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"display_name,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
}

type askCodexModelsOutput struct {
	Models []askCodexModel `json:"models"`
}

// newMCPServer builds the MCP server exposed at /mcp. Tools call the ChatGPT
// Codex backend in-process through the same request pipeline the
// /v1/chat/completions and /v1/responses endpoints use.
func (s *Server) newMCPServer() *mcpsdk.Server {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    mcpServerName,
		Version: mcpServerVersion,
	}, nil)

	addMCPTool(srv, &mcpsdk.Tool{
		Name: "ask_codex",
		Description: "Ask a GPT model a single self-contained question and get its answer back as text. " +
			"Requests are served by the ChatGPT Codex CLI backend using the local Codex OAuth " +
			"credentials, so no separate OpenAI API key is involved. Each call is one-shot: there is no " +
			"conversation history, so put everything the model needs into the prompt. Call ask_codex_models " +
			"first if you are unsure which model IDs are available.",
		InputSchema: mcpObjectSchema(map[string]any{
			"model": mcpStringSchema("Model ID to ask, e.g. gpt-5.2-codex. Append a reasoning effort " +
				"suffix (e.g. gpt-5.2-codex-high) to override the model's default effort. " +
				"Use ask_codex_models to list the IDs the ChatGPT Codex CLI backend currently offers."),
			"prompt": mcpStringSchema("The full question or instruction to send to the model."),
		}, "model", "prompt"),
	}, s.mcpAskCodex)

	addMCPTool(srv, &mcpsdk.Tool{
		Name: "ask_codex_models",
		Description: "List the model IDs that can be passed to ask_codex, along with the reasoning effort " +
			"levels each one accepts as a suffix. Models are served by the ChatGPT Codex CLI backend and " +
			"what the current ChatGPT account is actually entitled to can be narrower than this list.",
		InputSchema: mcpObjectSchema(map[string]any{}),
	}, s.mcpAskCodexModels)

	return srv
}

// mcpHandler returns the stateless streamable HTTP handler for /mcp. The server
// keeps no per-session state, so every request is served with a fresh session
// and plain JSON responses instead of an SSE stream.
func (s *Server) mcpHandler() http.HandlerFunc {
	mcpServer := s.newMCPServer()
	handler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return mcpServer },
		&mcpsdk.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			// The SDK auto-enables DNS rebinding protection when the listener is
			// loopback, rejecting any request whose Host is not also loopback. The
			// proxy is served on a public hostname through a tunnel that forwards
			// the original Host, so that check rejects every remote client. Access
			// is already gated by the admin bearer token in adminMiddleware.
			DisableLocalhostProtection: true,
		},
	)
	return handler.ServeHTTP
}

func (s *Server) mcpAskCodex(ctx context.Context, in askCodexInput) (askCodexOutput, error) {
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return askCodexOutput{}, fmt.Errorf("prompt is required")
	}

	requestedModel := strings.TrimSpace(in.Model)
	if requestedModel == "" {
		return askCodexOutput{}, fmt.Errorf("model is required; call ask_codex_models to list available models")
	}

	// Reuse the same request shape the chat completions handler builds from, so
	// model normalization, reasoning effort resolution and the Codex CLI
	// instructions all behave exactly as they do on /v1/chat/completions.
	requestData := map[string]interface{}{
		"model": requestedModel,
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": prompt},
		},
	}

	normalizedModel := normalizeModel(requestedModel)
	body, err := json.Marshal(buildCodexRequestBody(requestData))
	if err != nil {
		return askCodexOutput{}, fmt.Errorf("failed to prepare request for model %q: %w", requestedModel, err)
	}

	s.logger.Info().
		Str("requested_model", requestedModel).
		Str("normalized_model", normalizedModel).
		Int("prompt_len", len(prompt)).
		Msg("MCP ask_codex request received")

	// The upstream helpers only take an *http.Request for its context, so a
	// bare request carrying the tool call's context is enough here.
	upstreamURL := "https://chatgpt.com/backend-api/codex/responses"
	carrier, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, nil)
	if err != nil {
		return askCodexOutput{}, fmt.Errorf("failed to create upstream request: %w", err)
	}

	apiCallStart := time.Now()
	resp, statusCode, err := s.makeChatGPTRequestWithRetry(carrier, upstreamURL, body, normalizedModel)
	if err != nil {
		s.logger.Error().
			Err(err).
			Str("requested_model", requestedModel).
			Str("normalized_model", normalizedModel).
			Dur("api_call_duration", time.Since(apiCallStart)).
			Msg("MCP ask_codex upstream request failed")
		return askCodexOutput{}, fmt.Errorf("ask_codex failed for model %q: %w", requestedModel, err)
	}
	defer resp.Body.Close()

	if statusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		s.logger.Error().
			Int("status_code", statusCode).
			Str("requested_model", requestedModel).
			Str("normalized_model", normalizedModel).
			Dur("api_call_duration", time.Since(apiCallStart)).
			Msg("MCP ask_codex upstream returned an error")
		return askCodexOutput{}, fmt.Errorf("ask_codex failed for model %q: upstream returned %d: %s",
			requestedModel, statusCode, strings.TrimSpace(string(detail)))
	}

	completion, err := bufferChatCompletionFromSSE(resp.Body, normalizedModel)
	if err != nil {
		return askCodexOutput{}, fmt.Errorf("ask_codex failed for model %q: %w", requestedModel, err)
	}

	// The buffered response carries the model the stream reported, which can
	// differ from the one we asked for after normalization.
	servedModel := completion.Model
	if servedModel == "" {
		servedModel = normalizedModel
	}

	text := extractCompletionText(completion)
	if text == "" {
		return askCodexOutput{}, fmt.Errorf("model %q returned no text", servedModel)
	}

	s.logger.Info().
		Str("requested_model", requestedModel).
		Str("model", servedModel).
		Str("normalized_model", normalizedModel).
		Int("text_len", len(text)).
		Dur("api_call_duration", time.Since(apiCallStart)).
		Msg("MCP ask_codex completed")

	return askCodexOutput{
		RequestedModel: requestedModel,
		Model:          servedModel,
		Text:           text,
	}, nil
}

func (s *Server) mcpAskCodexModels(ctx context.Context, _ askCodexModelsInput) (askCodexModelsOutput, error) {
	upstream, err := s.fetchUpstreamModels(ctx)
	if err != nil {
		s.logger.Error().Err(err).Msg("MCP ask_codex_models failed to list upstream models")
		return askCodexModelsOutput{}, err
	}

	models := make([]askCodexModel, 0, len(upstream))
	for _, model := range upstream {
		models = append(models, askCodexModel{
			ID:               model.Slug,
			DisplayName:      model.DisplayName,
			ReasoningEfforts: model.efforts(),
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return askCodexModelsOutput{Models: models}, nil
}

// extractCompletionText joins the assistant text across the buffered choices.
func extractCompletionText(completion *ChatCompletionResponse) string {
	if completion == nil {
		return ""
	}

	var b strings.Builder
	for _, choice := range completion.Choices {
		b.WriteString(choice.Message.Content)
	}
	return b.String()
}

// addMCPTool registers a tool whose handler returns a structured result, which
// the SDK marshals into both the structured content and the text fallback.
func addMCPTool[In, Out any](srv *mcpsdk.Server, tool *mcpsdk.Tool, handler func(context.Context, In) (Out, error)) {
	mcpsdk.AddTool(srv, tool, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input In) (*mcpsdk.CallToolResult, Out, error) {
		output, err := handler(ctx, input)
		return nil, output, err
	})
}

func mcpObjectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func mcpStringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
