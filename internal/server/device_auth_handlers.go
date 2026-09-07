package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/dvcrn/codex-oauth-proxy/internal/credentials"
)

func (s *Server) tokensHandler(w http.ResponseWriter, r *http.Request) {
	if !s.deviceAuthRequestAllowed(w, r, http.MethodPost) {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Expected application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, ok := readLimitedRequestBody(w, r, 65536)
	if !ok {
		return
	}
	var input struct {
		AccessToken  string  `json:"accessToken"`
		RefreshToken *string `json:"refreshToken,omitempty"`
		AccountID    *string `json:"accountId,omitempty"`
		IDToken      *string `json:"idToken,omitempty"`
		LastRefresh  *string `json:"lastRefresh,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&input)
	if decodeErr == nil {
		decodeErr = decoder.Decode(&struct{}{})
		if errors.Is(decodeErr, io.EOF) {
			decodeErr = nil
		}
	}
	invalidOptional := emptyOptional(input.RefreshToken) || emptyOptional(input.AccountID) ||
		emptyOptional(input.IDToken) || emptyOptional(input.LastRefresh)
	invalidLastRefresh := input.LastRefresh != nil && !validRFC3339(*input.LastRefresh)
	if decodeErr != nil || strings.TrimSpace(input.AccessToken) == "" || invalidOptional || invalidLastRefresh {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Expected accessToken and optional refreshToken, accountId, idToken, lastRefresh", http.StatusBadRequest)
		return
	}
	claims, err := deviceAuthTokenPayload(input.AccessToken)
	if err != nil || claims.ExpiresAt <= 0 {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Expected a valid Codex accessToken", http.StatusBadRequest)
		return
	}
	accountID := claims.OpenAIAuth.AccountID
	if input.AccountID != nil {
		accountID = strings.TrimSpace(*input.AccountID)
	}
	if accountID == "" {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Expected accountId or an accessToken containing one", http.StatusBadRequest)
		return
	}
	refreshToken := ""
	if input.RefreshToken != nil {
		refreshToken = *input.RefreshToken
	} else if fetcher, ok := s.credsFetcher.(credentials.OAuthCredentialsFetcher); ok {
		if current, currentErr := fetcher.GetFullCredentials(); currentErr == nil {
			refreshToken = current.RefreshToken
		}
	}
	if err := s.deviceAuth.store.StoreCredentials(input.AccessToken, refreshToken, claims.ExpiresAt*1000, accountID); err != nil {
		s.logger.Error().Err(err).Msg("Failed to store OAuth credentials")
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Failed to store credentials", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"stored": true})
}

func emptyOptional(value *string) bool {
	return value != nil && strings.TrimSpace(*value) == ""
}

func validRFC3339(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func (s *Server) tokenStatusHandler(w http.ResponseWriter, r *http.Request) {
	if !s.deviceAuthRequestAllowed(w, r, http.MethodGet) {
		return
	}
	configured := false
	if fetcher, ok := s.credsFetcher.(credentials.OAuthCredentialsFetcher); ok {
		stored, err := fetcher.GetFullCredentials()
		configured = err == nil && stored.AccessToken != ""
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"configured": configured})
}

func (s *Server) deviceAuthStartHandler(w http.ResponseWriter, r *http.Request) {
	if !s.deviceAuthRequestAllowed(w, r, http.MethodPost) {
		return
	}
	if !discardLimitedRequestBody(w, r, 1024) {
		return
	}
	status, err := s.deviceAuth.Start(r.Context())
	s.writeDeviceAuthResponse(w, status, err)
}

func (s *Server) deviceAuthStatusHandler(w http.ResponseWriter, r *http.Request) {
	if !s.deviceAuthRequestAllowed(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	var (
		status deviceAuthStatus
		err    error
	)
	if r.Method == http.MethodGet {
		status, err = s.deviceAuth.Status()
	} else {
		if !discardLimitedRequestBody(w, r, 1024) {
			return
		}
		status, err = s.deviceAuth.Poll(r.Context())
	}
	s.writeDeviceAuthResponse(w, status, err)
}

func (s *Server) deviceAuthRequestAllowed(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	allowed := false
	for _, method := range methods {
		if r.Method == method {
			allowed = true
			break
		}
	}
	if !allowed {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	scheme := r.URL.Scheme
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	if origin != scheme+"://"+r.Host {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Origin is not allowed", http.StatusForbidden)
		return false
	}
	return true
}

func discardLimitedRequestBody(w http.ResponseWriter, r *http.Request, limit int64) bool {
	_, ok := readLimitedRequestBody(w, r, limit)
	return ok
}

func readLimitedRequestBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return nil, false
	}
	if int64(len(body)) > limit {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

func (s *Server) writeDeviceAuthResponse(w http.ResponseWriter, status deviceAuthStatus, err error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err != nil {
		var deviceErr *DeviceAuthError
		if errors.As(err, &deviceErr) {
			http.Error(w, deviceErr.Error(), http.StatusBadGateway)
			return
		}
		s.logger.Error().Err(err).Msg("Device authorization request failed")
		http.Error(w, "Device authorization request failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		s.logger.Error().Err(err).Msg("Failed to encode device authorization response")
	}
}
