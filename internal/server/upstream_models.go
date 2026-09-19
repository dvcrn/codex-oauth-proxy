package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const upstreamModelsURL = "https://chatgpt.com/backend-api/codex/models"

const upstreamModelsTTL = 5 * time.Minute

type upstreamModel struct {
	Slug                    string `json:"slug"`
	DisplayName             string `json:"display_name"`
	Description             string `json:"description"`
	Visibility              string `json:"visibility"`
	SupportedInAPI          *bool  `json:"supported_in_api"`
	DefaultReasoningLevel   string `json:"default_reasoning_level"`
	SupportedReasoningLevel []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	SupportedEndpoints []string `json:"supported_endpoints"`
}

var unsupportedWireEfforts = map[string]bool{"ultra": true}

func (m upstreamModel) efforts() []string {
	seen := make(map[string]bool)
	efforts := make([]string, 0, len(m.SupportedReasoningLevel))
	for _, level := range m.SupportedReasoningLevel {
		effort := normalizeReasoningEffort(level.Effort)
		if effort == "" || unsupportedWireEfforts[effort] || seen[effort] {
			continue
		}
		seen[effort] = true
		efforts = append(efforts, effort)
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

type modelCatalogKey struct {
	accountID  string
	version    string
	originator string
}

type modelCatalogSnapshot struct {
	models    []upstreamModel
	fetchedAt time.Time
	etag      string
}

type modelCatalogRefresh struct {
	done   chan struct{}
	models []upstreamModel
	err    error
}

type modelCatalogFetchError struct {
	err       error
	transient bool
}

func (e *modelCatalogFetchError) Error() string { return e.err.Error() }
func (e *modelCatalogFetchError) Unwrap() error { return e.err }

func cloneUpstreamModels(models []upstreamModel) []upstreamModel {
	cloned := make([]upstreamModel, len(models))
	for i, model := range models {
		cloned[i] = model
		cloned[i].SupportedReasoningLevel = append([]struct {
			Effort string `json:"effort"`
		}(nil), model.SupportedReasoningLevel...)
		cloned[i].SupportedEndpoints = append([]string(nil), model.SupportedEndpoints...)
	}
	return cloned
}

func normalizeUpstreamModels(models []upstreamModel) []upstreamModel {
	seen := make(map[string]bool)
	visible := make([]upstreamModel, 0, len(models))
	for _, model := range models {
		model.Slug = strings.TrimSpace(model.Slug)
		if model.Slug == "" || model.Visibility != "list" || model.SupportedInAPI != nil && !*model.SupportedInAPI || seen[model.Slug] {
			continue
		}
		seen[model.Slug] = true
		model.DisplayName = strings.TrimSpace(model.DisplayName)
		model.Description = strings.TrimSpace(model.Description)
		model.DefaultReasoningLevel = normalizeReasoningEffort(model.DefaultReasoningLevel)
		efforts := model.efforts()
		model.SupportedReasoningLevel = make([]struct {
			Effort string `json:"effort"`
		}, len(efforts))
		for i, effort := range efforts {
			model.SupportedReasoningLevel[i].Effort = effort
		}
		visible = append(visible, model)
	}
	return visible
}

func (s *Server) cachedModelsForCurrentAccount() []upstreamModel {
	_, accountID, err := s.credsFetcher.GetCredentials()
	if err != nil {
		return nil
	}
	key := modelCatalogKey{accountID: strings.TrimSpace(accountID), version: s.clientIdentity.version, originator: s.clientIdentity.originator}
	s.modelsCacheMu.Lock()
	defer s.modelsCacheMu.Unlock()
	return cloneUpstreamModels(s.modelsCache[key].models)
}

func (s *Server) fetchUpstreamModels(ctx context.Context) ([]upstreamModel, error) {
	token, accountID, err := s.credsFetcher.GetCredentials()
	if err != nil {
		return nil, fmt.Errorf("failed to get credentials: %w", err)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, fmt.Errorf("failed to get credentials: missing account ID")
	}
	key := modelCatalogKey{accountID: accountID, version: s.clientIdentity.version, originator: s.clientIdentity.originator}
	now := time.Now()

	s.modelsCacheMu.Lock()
	stale, hasStale := s.modelsCache[key]
	if hasStale && now.Sub(stale.fetchedAt) < upstreamModelsTTL {
		models := cloneUpstreamModels(stale.models)
		s.modelsCacheMu.Unlock()
		return models, nil
	}
	if refresh, ok := s.modelsRefreshes[key]; ok {
		s.modelsCacheMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-refresh.done:
			return cloneUpstreamModels(refresh.models), refresh.err
		}
	}
	refresh := &modelCatalogRefresh{done: make(chan struct{})}
	s.modelsRefreshes[key] = refresh
	s.modelsCacheMu.Unlock()

	go s.refreshModelCatalog(context.WithoutCancel(ctx), key, token, accountID, stale, hasStale, refresh)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-refresh.done:
		return cloneUpstreamModels(refresh.models), refresh.err
	}
}

func (s *Server) refreshModelCatalog(ctx context.Context, key modelCatalogKey, token, accountID string, stale modelCatalogSnapshot, hasStale bool, refresh *modelCatalogRefresh) {
	models, etag, fetchErr := s.fetchUpstreamModelsWithRetry(ctx, token, accountID)
	servedStale := false
	if fetchErr != nil && fetchErr.transient && hasStale {
		models = cloneUpstreamModels(stale.models)
		servedStale = true
		s.logger.Warn().Err(fetchErr).Str("account_id", accountID).Str("client_version", key.version).Msg("Serving stale upstream model list after transient refresh failure")
		fetchErr = nil
	}

	s.modelsCacheMu.Lock()
	if fetchErr == nil && !servedStale {
		s.modelsCache[key] = modelCatalogSnapshot{models: cloneUpstreamModels(models), fetchedAt: time.Now(), etag: etag}
	}
	refresh.models = cloneUpstreamModels(models)
	if fetchErr != nil {
		refresh.err = fetchErr
	}
	delete(s.modelsRefreshes, key)
	close(refresh.done)
	s.modelsCacheMu.Unlock()

	if fetchErr == nil && !servedStale {
		s.logger.Info().Int("model_count", len(models)).Str("client_version", key.version).Msg("Refreshed upstream model list")
	}
}

func (s *Server) fetchUpstreamModelsWithRetry(ctx context.Context, token, accountID string) ([]upstreamModel, string, *modelCatalogFetchError) {
	parsed, statusCode, detail, etag, err := s.fetchUpstreamModelsOnce(ctx, token, accountID)
	if err != nil {
		return nil, "", &modelCatalogFetchError{err: err, transient: statusCode == 0 && !errors.Is(err, context.Canceled)}
	}
	if statusCode == http.StatusUnauthorized {
		s.logger.Warn().Str("response_body", detail).Msg("Received 401 while listing upstream models, attempting token refresh")
		if err := s.credsFetcher.RefreshCredentials(); err != nil {
			return nil, "", &modelCatalogFetchError{err: &upstreamAuthError{statusCode: statusCode, detail: detail, err: fmt.Errorf("token refresh failed: %w", err)}}
		}
		token, refreshedAccountID, err := s.credsFetcher.GetCredentials()
		if err != nil {
			return nil, "", &modelCatalogFetchError{err: &upstreamAuthError{statusCode: statusCode, detail: detail, err: err}}
		}
		if strings.TrimSpace(refreshedAccountID) != accountID {
			return nil, "", &modelCatalogFetchError{err: fmt.Errorf("account changed while refreshing model catalog")}
		}
		parsed, statusCode, detail, etag, err = s.fetchUpstreamModelsOnce(ctx, token, accountID)
		if err != nil {
			return nil, "", &modelCatalogFetchError{err: err, transient: true}
		}
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return nil, "", &modelCatalogFetchError{err: &upstreamAuthError{statusCode: statusCode, detail: detail}}
	}
	if statusCode != http.StatusOK {
		return nil, "", &modelCatalogFetchError{
			err:       fmt.Errorf("failed to list models: upstream returned %d: %s", statusCode, detail),
			transient: statusCode == http.StatusTooManyRequests || statusCode >= 500,
		}
	}

	models := normalizeUpstreamModels(parsed.Models)
	if len(models) == 0 {
		return nil, "", &modelCatalogFetchError{err: fmt.Errorf("upstream returned no usable models for client_version %s", s.clientIdentity.version)}
	}
	return models, etag, nil
}

func (s *Server) fetchUpstreamModelsOnce(ctx context.Context, token, accountID string) (*upstreamModelsResponse, int, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamModelsURL, nil)
	if err != nil {
		return nil, 0, "", "", fmt.Errorf("failed to create models request: %w", err)
	}
	query := req.URL.Query()
	query.Set("client_version", s.clientIdentity.version)
	req.URL.RawQuery = query.Encode()
	setCodexRequestHeaders(req.Header, s.clientIdentity, token, accountID, "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, "", "", fmt.Errorf("failed to list models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, resp.StatusCode, strings.TrimSpace(string(detail)), resp.Header.Get("ETag"), nil
	}
	var parsed upstreamModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, resp.StatusCode, "", resp.Header.Get("ETag"), fmt.Errorf("failed to parse models response: %w", err)
	}
	return &parsed, resp.StatusCode, "", resp.Header.Get("ETag"), nil
}

func setCodexRequestHeaders(headers http.Header, identity codexClientIdentity, token, accountID, accept string) {
	bareToken := strings.TrimSpace(token)
	if len(bareToken) >= 7 && strings.EqualFold(bareToken[:7], "Bearer ") {
		bareToken = strings.TrimSpace(bareToken[7:])
	}
	headers.Set("authorization", "Bearer "+bareToken)
	headers.Set("version", identity.version)
	headers.Set("chatgpt-account-id", accountID)
	headers.Set("originator", identity.originator)
	headers.Set("user-agent", identity.userAgent)
	headers.Set("accept", accept)
}
