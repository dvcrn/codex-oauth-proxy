package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// upstreamModelsURL lists the models the authenticated ChatGPT account is
// actually entitled to. The backend gates the result on client_version, so the
// same account sees a different set depending on which CLI version is claimed.
const upstreamModelsURL = "https://chatgpt.com/backend-api/codex/models"

// upstreamModelsTTL is how long a successful listing is reused.
const upstreamModelsTTL = 5 * time.Minute

// upstreamModel is the subset of the backend's model description this proxy
// uses; the full payload is mostly CLI-specific execution settings.
type upstreamModel struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	// Visibility is "list" for models meant to be shown to users. Anything
	// else (e.g. "hide" for codex-auto-review) is an internal model that
	// should not be advertised.
	Visibility              string `json:"visibility"`
	DefaultReasoningLevel   string `json:"default_reasoning_level"`
	SupportedReasoningLevel []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

// unsupportedWireEfforts are reasoning levels the models endpoint advertises
// but the Responses API rejects. "ultra" drives automatic task delegation in
// the Codex CLI rather than being a wire-level reasoning.effort value, so
// sending it upstream fails with:
//
//	Invalid value: 'ultra'. Supported values are: 'none', 'minimal', 'low',
//	'medium', 'high', 'xhigh', and 'max'.
var unsupportedWireEfforts = map[string]bool{"ultra": true}

// efforts returns the reasoning effort suffixes this model accepts, limited to
// the values the Responses API actually honours.
func (m upstreamModel) efforts() []string {
	efforts := make([]string, 0, len(m.SupportedReasoningLevel))
	for _, level := range m.SupportedReasoningLevel {
		if level.Effort != "" && !unsupportedWireEfforts[level.Effort] {
			efforts = append(efforts, level.Effort)
		}
	}
	return efforts
}

type upstreamModelsResponse struct {
	Models []upstreamModel `json:"models"`
}

type upstreamAuthError struct {
	statusCode int
	detail     string
	err        error
}

func (e *upstreamAuthError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("upstream authentication failed: %v", e.err)
	}
	if e.detail != "" {
		return fmt.Sprintf("upstream authentication failed: %s", e.detail)
	}
	return "upstream authentication failed"
}

func (e *upstreamAuthError) Unwrap() error {
	return e.err
}

// fetchUpstreamModels asks the ChatGPT backend which models the current
// account can use, returning only the user-visible ones. Results are cached
// for upstreamModelsTTL.
//
// A failure is returned rather than falling back to the built-in model table,
// which drifts out of date as models are retired and would advertise IDs that
// fail at call time.
func (s *Server) fetchUpstreamModels(ctx context.Context) ([]upstreamModel, error) {
	s.modelsCacheMu.Lock()
	defer s.modelsCacheMu.Unlock()

	if s.modelsCache != nil && time.Now().Before(s.modelsCacheExpiry) {
		return s.modelsCache, nil
	}

	parsed, err := s.fetchUpstreamModelsWithRetry(ctx)
	if err != nil {
		return nil, err
	}

	visible := make([]upstreamModel, 0, len(parsed.Models))
	for _, model := range parsed.Models {
		if model.Slug == "" || model.Visibility != "list" {
			continue
		}
		visible = append(visible, model)
	}

	if len(visible) == 0 {
		return nil, fmt.Errorf("upstream returned no usable models for client_version %s", codexClientVersion)
	}

	s.modelsCache = visible
	s.modelsCacheExpiry = time.Now().Add(upstreamModelsTTL)

	s.logger.Info().
		Int("model_count", len(visible)).
		Str("client_version", codexClientVersion).
		Msg("Refreshed upstream model list")

	return visible, nil
}

func (s *Server) fetchUpstreamModelsWithRetry(ctx context.Context) (*upstreamModelsResponse, error) {
	parsed, statusCode, detail, err := s.fetchUpstreamModelsOnce(ctx)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusUnauthorized {
		if statusCode != http.StatusOK {
			return nil, fmt.Errorf("failed to list models: upstream returned %d: %s",
				statusCode, detail)
		}
		return parsed, nil
	}

	s.logger.Warn().
		Str("response_body", detail).
		Msg("Received 401 while listing upstream models, attempting token refresh")

	if err := s.credsFetcher.RefreshCredentials(); err != nil {
		return nil, &upstreamAuthError{statusCode: statusCode, detail: detail, err: fmt.Errorf("token refresh failed: %w", err)}
	}

	s.logger.Info().Msg("Successfully refreshed credentials, retrying upstream model list")

	parsed, statusCode, detail, err = s.fetchUpstreamModelsOnce(ctx)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusUnauthorized {
		return nil, &upstreamAuthError{statusCode: statusCode, detail: detail}
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list models: upstream returned %d: %s",
			statusCode, detail)
	}

	return parsed, nil
}

func (s *Server) fetchUpstreamModelsOnce(ctx context.Context) (*upstreamModelsResponse, int, string, error) {
	token, accountID, err := s.credsFetcher.GetCredentials()
	if err != nil {
		return nil, 0, "", fmt.Errorf("failed to get credentials: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamModelsURL, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("failed to create models request: %w", err)
	}

	query := req.URL.Query()
	query.Set("client_version", codexClientVersion)
	req.URL.RawQuery = query.Encode()

	bareToken := strings.TrimSpace(token)
	if len(bareToken) >= 7 && strings.EqualFold(bareToken[:7], "Bearer ") {
		bareToken = strings.TrimSpace(bareToken[7:])
	}

	req.Header.Set("authorization", "Bearer "+bareToken)
	req.Header.Set("version", codexClientVersion)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("user-agent", "codex_cli_rs/"+codexClientVersion+" (Mac OS 26.3.0; arm64) Apple_Terminal/466")
	req.Header.Set("accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("failed to list models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, resp.StatusCode, strings.TrimSpace(string(detail)), nil
	}

	var parsed upstreamModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, resp.StatusCode, "", fmt.Errorf("failed to parse models response: %w", err)
	}

	return &parsed, resp.StatusCode, "", nil
}
