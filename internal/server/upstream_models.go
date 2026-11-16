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
const upstreamModelsTTL = time.Hour

// modelsFetchTimeout bounds a shared listing fetch, which outlives any single
// caller's context.
const modelsFetchTimeout = 30 * time.Second

// modelLookupTimeout bounds the catalog lookup a proxied request may wait on.
const modelLookupTimeout = 5 * time.Second

// modelLookupBackoff is how long request-path lookups are skipped after one
// fails, so an unavailable models endpoint does not slow every request.
const modelLookupBackoff = 30 * time.Second

// storedBackoffTTL satisfies Cloudflare KV's 60s minimum expiration_ttl while
// RetryAtUnix enforces the exact modelLookupBackoff window.
const storedBackoffTTL = time.Minute

// ModelsCacheStore persists the upstream model listing across stateless
// runtimes such as Cloudflare Workers.
type ModelsCacheStore interface {
	LoadModelsCache() ([]byte, error)
	SaveModelsCache(data []byte, ttl time.Duration) error
}

type cachedModelsSnapshot struct {
	ClientVersion string          `json:"client_version"`
	AccountID     string          `json:"account_id,omitempty"`
	Models        []upstreamModel `json:"models,omitempty"`
	RetryAtUnix   int64           `json:"retry_at_unix,omitempty"`
}

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

var errModelsLookupBackoff = fmt.Errorf("upstream models lookup in backoff")

// fetchUpstreamModels asks the ChatGPT backend which models the current
// account can use, returning only the user-visible ones. Results are cached
// for upstreamModelsTTL.
//
// On long-lived servers, concurrent cache misses share one fetch, which runs
// outside modelsCacheMu and is not cancelled when one waiting caller gives up.
func (s *Server) fetchUpstreamModels(ctx context.Context) ([]upstreamModel, error) {
	return s.loadOrFetchUpstreamModels(ctx, false)
}

func (s *Server) loadOrFetchUpstreamModels(ctx context.Context, honorStoredBackoff bool) ([]upstreamModel, error) {
	s.modelsCacheMu.Lock()
	cached := s.modelsCache
	fresh := cached != nil && time.Now().Before(s.modelsCacheExpiry)
	s.modelsCacheMu.Unlock()
	if fresh {
		return cached, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !detachBackgroundFetches {
		if cached == nil {
			stored, inBackoff := s.loadStoredModelsCache()
			if len(stored) > 0 {
				return stored, nil
			}
			if honorStoredBackoff && inBackoff {
				return nil, errModelsLookupBackoff
			}
		}
		return s.refreshUpstreamModels(ctx)
	}

	fetch := s.modelsFetch.DoChan("models", func() (interface{}, error) {
		s.modelsCacheMu.Lock()
		nowCached := s.modelsCache
		nowFresh := nowCached != nil && time.Now().Before(s.modelsCacheExpiry)
		s.modelsCacheMu.Unlock()
		if nowFresh {
			return nowCached, nil
		}
		if nowCached == nil {
			stored, inBackoff := s.loadStoredModelsCache()
			if len(stored) > 0 {
				return stored, nil
			}
			if honorStoredBackoff && inBackoff {
				return nil, errModelsLookupBackoff
			}
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), modelsFetchTimeout)
		defer cancel()
		return s.refreshUpstreamModels(fetchCtx)
	})
	select {
	case result := <-fetch:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.([]upstreamModel), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// refreshUpstreamModels fetches the listing and stores the visible models in
// the cache.
func (s *Server) refreshUpstreamModels(ctx context.Context) ([]upstreamModel, error) {
	parsed, accountID, err := s.fetchUpstreamModelsWithRetry(ctx)
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

	s.setModelsCache(visible)
	s.saveStoredModelsCache(accountID, visible)

	s.logger.Info().
		Int("model_count", len(visible)).
		Str("client_version", codexClientVersion).
		Msg("Refreshed upstream model list")

	return visible, nil
}

func (s *Server) fetchUpstreamModelsWithRetry(ctx context.Context) (*upstreamModelsResponse, string, error) {
	parsed, accountID, statusCode, detail, err := s.fetchUpstreamModelsOnce(ctx)
	if err != nil {
		return nil, "", err
	}
	if statusCode != http.StatusUnauthorized {
		if statusCode != http.StatusOK {
			return nil, "", fmt.Errorf("failed to list models: upstream returned %d: %s",
				statusCode, detail)
		}
		return parsed, accountID, nil
	}

	s.logger.Warn().
		Str("response_body", detail).
		Msg("Received 401 while listing upstream models, attempting token refresh")

	if err := s.credsFetcher.RefreshCredentials(); err != nil {
		return nil, "", &upstreamAuthError{statusCode: statusCode, detail: detail, err: fmt.Errorf("token refresh failed: %w", err)}
	}

	s.logger.Info().Msg("Successfully refreshed credentials, retrying upstream model list")

	parsed, accountID, statusCode, detail, err = s.fetchUpstreamModelsOnce(ctx)
	if err != nil {
		return nil, "", err
	}
	if statusCode == http.StatusUnauthorized {
		return nil, "", &upstreamAuthError{statusCode: statusCode, detail: detail}
	}
	if statusCode != http.StatusOK {
		return nil, "", fmt.Errorf("failed to list models: upstream returned %d: %s",
			statusCode, detail)
	}

	return parsed, accountID, nil
}

func (s *Server) fetchUpstreamModelsOnce(ctx context.Context) (*upstreamModelsResponse, string, int, string, error) {
	token, accountID, err := s.credsFetcher.GetCredentials()
	if err != nil {
		return nil, "", 0, "", fmt.Errorf("failed to get credentials: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamModelsURL, nil)
	if err != nil {
		return nil, "", 0, "", fmt.Errorf("failed to create models request: %w", err)
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
		return nil, "", 0, "", fmt.Errorf("failed to list models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, accountID, resp.StatusCode, strings.TrimSpace(string(detail)), nil
	}

	var parsed upstreamModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, "", resp.StatusCode, "", fmt.Errorf("failed to parse models response: %w", err)
	}

	return &parsed, accountID, resp.StatusCode, "", nil
}

// modelCatalogFor returns the live model catalog that request normalization
// matches names against, from the cache or a fresh fetch. An expired cache is
// served while one background fetch refreshes it. A failed lookup returns nil
// and pauses request-path lookups for modelLookupBackoff unless another caller
// refreshes the cache first.
func (s *Server) modelCatalogFor(ctx context.Context) modelCatalog {
	s.modelsCacheMu.Lock()
	now := time.Now()
	cached := s.modelsCache
	fresh := cached != nil && now.Before(s.modelsCacheExpiry)
	inBackoff := now.Before(s.modelsLookupRetryAt)
	refreshBg := detachBackgroundFetches && cached != nil && !fresh && !inBackoff
	if refreshBg {
		s.modelsLookupRetryAt = now.Add(modelLookupBackoff)
	}
	s.modelsCacheMu.Unlock()

	if fresh {
		return modelCatalog(cached)
	}
	if cached != nil {
		if refreshBg {
			go s.PrefetchModels(context.WithoutCancel(ctx))
		}
		return modelCatalog(cached)
	}
	if inBackoff || ctx.Err() != nil {
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, modelLookupTimeout)
	defer cancel()
	models, err := s.loadOrFetchUpstreamModels(lookupCtx, true)
	if err != nil {
		if err != errModelsLookupBackoff {
			s.logger.Warn().Err(err).Msg("Failed to list upstream models for request normalization")
			if ctx.Err() == nil {
				s.recordModelsLookupBackoff()
			}
		}
		return nil
	}
	return modelCatalog(models)
}

func (s *Server) setModelsCache(visible []upstreamModel) {
	s.modelsCacheMu.Lock()
	s.modelsCache = visible
	s.modelsCacheExpiry = time.Now().Add(upstreamModelsTTL)
	s.modelsLookupRetryAt = time.Time{}
	s.modelsCacheMu.Unlock()
}

func (s *Server) invalidateModelsCache() {
	s.modelsCacheMu.Lock()
	s.modelsCache = nil
	s.modelsCacheExpiry = time.Time{}
	s.modelsLookupRetryAt = time.Time{}
	s.modelsCacheMu.Unlock()
}

func (s *Server) currentAccountID() string {
	if s.credsFetcher == nil {
		return ""
	}
	_, accountID, err := s.credsFetcher.GetCredentials()
	if err != nil {
		return ""
	}
	return accountID
}

func (s *Server) loadStoredModelsCache() (models []upstreamModel, inBackoff bool) {
	if s.modelsCacheStore == nil {
		return nil, false
	}
	data, err := s.modelsCacheStore.LoadModelsCache()
	if err != nil {
		s.logger.Warn().Err(err).Msg("Failed to load cached upstream models")
		return nil, false
	}
	if len(data) == 0 {
		return nil, false
	}
	var snapshot cachedModelsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.ClientVersion != codexClientVersion {
		return nil, false
	}
	accountID := s.currentAccountID()
	if snapshot.AccountID != "" && accountID != "" && snapshot.AccountID != accountID {
		return nil, false
	}
	if len(snapshot.Models) > 0 {
		s.setModelsCache(snapshot.Models)
		return snapshot.Models, false
	}
	if snapshot.RetryAtUnix > 0 {
		retryAt := time.Unix(snapshot.RetryAtUnix, 0)
		if time.Now().Before(retryAt) {
			s.modelsCacheMu.Lock()
			s.modelsLookupRetryAt = retryAt
			s.modelsCacheMu.Unlock()
			return nil, true
		}
	}
	return nil, false
}

func (s *Server) saveStoredModelsCache(accountID string, visible []upstreamModel) {
	if s.modelsCacheStore == nil {
		return
	}
	encoded, err := json.Marshal(cachedModelsSnapshot{
		ClientVersion: codexClientVersion,
		AccountID:     accountID,
		Models:        visible,
	})
	if err != nil {
		return
	}
	if err := s.modelsCacheStore.SaveModelsCache(encoded, upstreamModelsTTL); err != nil {
		s.logger.Warn().Err(err).Msg("Failed to save upstream models cache")
	}
}

func (s *Server) saveStoredModelsBackoff(retryAt time.Time) {
	if s.modelsCacheStore == nil {
		return
	}
	encoded, err := json.Marshal(cachedModelsSnapshot{
		ClientVersion: codexClientVersion,
		AccountID:     s.currentAccountID(),
		RetryAtUnix:   retryAt.Unix(),
	})
	if err != nil {
		return
	}
	if err := s.modelsCacheStore.SaveModelsCache(encoded, storedBackoffTTL); err != nil {
		s.logger.Warn().Err(err).Msg("Failed to save upstream models lookup backoff")
	}
}

func (s *Server) recordModelsLookupBackoff() {
	var persistRetryAt time.Time
	s.modelsCacheMu.Lock()
	if s.modelsCache == nil || !time.Now().Before(s.modelsCacheExpiry) {
		retryAt := time.Now().Add(modelLookupBackoff)
		s.modelsLookupRetryAt = retryAt
		if s.modelsCache == nil {
			persistRetryAt = retryAt
		}
	}
	s.modelsCacheMu.Unlock()

	if !persistRetryAt.IsZero() {
		s.saveStoredModelsBackoff(persistRetryAt)
	}
}

// PrefetchModels fills the model cache so the first requests don't wait on
// the upstream listing. A failure logs a warning and starts the lookup backoff.
func (s *Server) PrefetchModels(ctx context.Context) {
	if _, err := s.fetchUpstreamModels(ctx); err != nil {
		s.logger.Warn().Err(err).Msg("Failed to prefetch upstream models")
		if ctx.Err() == nil {
			s.recordModelsLookupBackoff()
		}
	}
}
