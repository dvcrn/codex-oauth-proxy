package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	deviceAuthBaseURL  = "https://auth.openai.com"
	deviceAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	deviceAuthLifetime = 15 * time.Minute
	deviceAuthTimeout  = 30 * time.Second
)

type DeviceAuthStore interface {
	LoadDeviceAuthSession() ([]byte, error)
	SaveDeviceAuthSession([]byte) error
	StoreCredentials(accessToken, refreshToken string, expiresAt int64, accountID string) error
	CompleteDeviceAuth(accessToken, refreshToken string, expiresAt int64, accountID string) error
}

type DeviceAuthError struct {
	message string
}

func (e *DeviceAuthError) Error() string {
	return e.message
}

type DeviceAuth struct {
	store  DeviceAuthStore
	client HTTPClient
	now    func() time.Time
	mu     sync.Mutex
}

type deviceAuthSession struct {
	Status     string `json:"status"`
	DeviceID   string `json:"deviceId,omitempty"`
	UserCode   string `json:"userCode,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
	IntervalMS int64  `json:"intervalMs,omitempty"`
	NextPollAt int64  `json:"nextPollAt,omitempty"`
}

type deviceAuthStatus struct {
	Status            string `json:"status"`
	VerificationURL   string `json:"verificationUrl,omitempty"`
	UserCode          string `json:"userCode,omitempty"`
	ExpiresAt         string `json:"expiresAt,omitempty"`
	RetryAfterSeconds int64  `json:"retryAfterSeconds,omitempty"`
}

type deviceAuthStartResponse struct {
	DeviceID string `json:"device_auth_id"`
	UserCode string `json:"user_code"`
	Interval any    `json:"interval"`
}

type deviceAuthApproval struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

type deviceAuthCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type deviceAuthJWTPayload struct {
	ExpiresAt  int64 `json:"exp"`
	OpenAIAuth struct {
		AccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

func newDeviceAuth(store DeviceAuthStore, client HTTPClient) *DeviceAuth {
	return &DeviceAuth{store: store, client: client, now: time.Now}
}

func (a *DeviceAuth) Start(ctx context.Context) (deviceAuthStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	current, err := a.status()
	if err != nil {
		return deviceAuthStatus{}, err
	}
	if current.Status == "pending" {
		return current, nil
	}

	statusCode, body, err := a.postJSON(ctx, "/api/accounts/deviceauth/usercode", map[string]string{
		"client_id": deviceAuthClientID,
	})
	if err != nil {
		return deviceAuthStatus{}, err
	}
	if statusCode < 200 || statusCode >= 300 {
		return deviceAuthStatus{}, &DeviceAuthError{message: fmt.Sprintf("Device authorization could not be started (HTTP %d)", statusCode)}
	}

	var started deviceAuthStartResponse
	if err := json.Unmarshal(body, &started); err != nil || started.DeviceID == "" || started.UserCode == "" {
		return deviceAuthStatus{}, &DeviceAuthError{message: "Invalid device authorization response"}
	}
	interval := parseDeviceAuthInterval(started.Interval)
	now := a.now()
	session := deviceAuthSession{
		Status:     "pending",
		DeviceID:   started.DeviceID,
		UserCode:   started.UserCode,
		ExpiresAt:  now.Add(deviceAuthLifetime).UnixMilli(),
		IntervalMS: interval.Milliseconds(),
		NextPollAt: now.Add(interval).UnixMilli(),
	}
	if err := a.saveSession(session); err != nil {
		return deviceAuthStatus{}, err
	}
	return a.view(session), nil
}

func (a *DeviceAuth) Status() (deviceAuthStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status()
}

func (a *DeviceAuth) status() (deviceAuthStatus, error) {
	session, ok, err := a.loadSession()
	if err != nil {
		return deviceAuthStatus{}, err
	}
	if !ok {
		return deviceAuthStatus{Status: "idle"}, nil
	}
	if session.Status == "pending" && a.now().UnixMilli() >= session.ExpiresAt {
		return a.finish("expired")
	}
	return a.view(session), nil
}

func (a *DeviceAuth) Poll(ctx context.Context) (deviceAuthStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	session, ok, err := a.loadSession()
	if err != nil {
		return deviceAuthStatus{}, err
	}
	if !ok {
		return deviceAuthStatus{Status: "idle"}, nil
	}
	now := a.now().UnixMilli()
	if session.Status == "pending" && now >= session.ExpiresAt {
		return a.finish("expired")
	}
	if session.Status != "pending" || now < session.NextPollAt {
		return a.view(session), nil
	}

	session.NextPollAt = now + session.IntervalMS
	if err := a.saveSession(session); err != nil {
		return deviceAuthStatus{}, err
	}
	statusCode, body, err := a.postJSON(ctx, "/api/accounts/deviceauth/token", map[string]string{
		"device_auth_id": session.DeviceID,
		"user_code":      session.UserCode,
	})
	if err != nil {
		return deviceAuthStatus{}, err
	}

	session.NextPollAt = a.now().UnixMilli() + session.IntervalMS
	if err := a.saveSession(session); err != nil {
		return deviceAuthStatus{}, err
	}
	if statusCode == http.StatusForbidden || statusCode == http.StatusNotFound || statusCode >= 500 {
		return a.view(session), nil
	}
	if !json.Valid(body) {
		return a.finish("failed")
	}
	if statusCode < 200 || statusCode >= 300 {
		code := deviceAuthErrorCode(body)
		if code == "deviceauth_authorization_pending" {
			return a.view(session), nil
		}
		if code == "slow_down" || statusCode == http.StatusTooManyRequests {
			session.IntervalMS += int64(5 * time.Second / time.Millisecond)
			session.NextPollAt = a.now().UnixMilli() + session.IntervalMS
			if err := a.saveSession(session); err != nil {
				return deviceAuthStatus{}, err
			}
			return a.view(session), nil
		}
		return a.finish("failed")
	}

	var approval deviceAuthApproval
	if err := json.Unmarshal(body, &approval); err != nil || approval.AuthorizationCode == "" || approval.CodeVerifier == "" {
		return a.finish("failed")
	}
	if _, err := a.finish("failed"); err != nil {
		return deviceAuthStatus{}, err
	}
	return a.exchange(ctx, approval)
}

func (a *DeviceAuth) exchange(ctx context.Context, approval deviceAuthApproval) (deviceAuthStatus, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {deviceAuthClientID},
		"code":          {approval.AuthorizationCode},
		"code_verifier": {approval.CodeVerifier},
		"redirect_uri":  {deviceAuthBaseURL + "/deviceauth/callback"},
	}
	ctx, cancel := context.WithTimeout(ctx, deviceAuthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceAuthBaseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return deviceAuthStatus{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := a.client.Do(req)
	if err != nil {
		return deviceAuthStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return deviceAuthStatus{Status: "failed"}, nil
	}
	var credentials deviceAuthCredentials
	if err := decodeLimitedJSON(response.Body, &credentials); err != nil || credentials.AccessToken == "" || credentials.RefreshToken == "" {
		return deviceAuthStatus{Status: "failed"}, nil
	}
	accountID, expiresAt, err := deviceAuthTokenClaims(credentials.AccessToken)
	if err != nil {
		return deviceAuthStatus{Status: "failed"}, nil
	}
	if err := a.store.CompleteDeviceAuth(credentials.AccessToken, credentials.RefreshToken, expiresAt, accountID); err != nil {
		return deviceAuthStatus{}, err
	}
	return deviceAuthStatus{Status: "authenticated"}, nil
}

func (a *DeviceAuth) postJSON(ctx context.Context, path string, body map[string]string) (int, []byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, deviceAuthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceAuthBaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, responseBody, nil
}

func (a *DeviceAuth) loadSession() (deviceAuthSession, bool, error) {
	encoded, err := a.store.LoadDeviceAuthSession()
	if err != nil {
		return deviceAuthSession{}, false, err
	}
	if len(encoded) == 0 {
		return deviceAuthSession{}, false, nil
	}
	var session deviceAuthSession
	if err := json.Unmarshal(encoded, &session); err != nil {
		return deviceAuthSession{}, false, fmt.Errorf("invalid stored device auth session: %w", err)
	}
	return session, true, nil
}

func (a *DeviceAuth) saveSession(session deviceAuthSession) error {
	encoded, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return a.store.SaveDeviceAuthSession(encoded)
}

func (a *DeviceAuth) finish(status string) (deviceAuthStatus, error) {
	session := deviceAuthSession{Status: status}
	if err := a.saveSession(session); err != nil {
		return deviceAuthStatus{}, err
	}
	return a.view(session), nil
}

func (a *DeviceAuth) view(session deviceAuthSession) deviceAuthStatus {
	if session.Status != "pending" {
		return deviceAuthStatus{Status: session.Status}
	}
	retry := int64(math.Ceil(float64(session.NextPollAt-a.now().UnixMilli()) / 1000))
	if retry < 1 {
		retry = 1
	}
	return deviceAuthStatus{
		Status:            "pending",
		VerificationURL:   deviceAuthBaseURL + "/codex/device",
		UserCode:          session.UserCode,
		ExpiresAt:         time.UnixMilli(session.ExpiresAt).UTC().Format(time.RFC3339Nano),
		RetryAfterSeconds: retry,
	}
}

func parseDeviceAuthInterval(raw any) time.Duration {
	seconds := 5.0
	switch value := raw.(type) {
	case float64:
		seconds = value
	case string:
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			seconds = parsed
		}
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		seconds = 5
	}
	duration := time.Duration(seconds * float64(time.Second))
	if duration < time.Second {
		return time.Second
	}
	if duration > deviceAuthLifetime {
		return deviceAuthLifetime
	}
	return duration
}

func deviceAuthErrorCode(body []byte) string {
	var response struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil {
		return ""
	}
	var code string
	if json.Unmarshal(response.Error, &code) == nil {
		return code
	}
	var nested struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(response.Error, &nested) == nil {
		return nested.Code
	}
	return ""
}

func deviceAuthTokenClaims(token string) (string, int64, error) {
	claims, err := deviceAuthTokenPayload(token)
	if err != nil || claims.OpenAIAuth.AccountID == "" || claims.ExpiresAt <= 0 {
		return "", 0, errors.New("invalid access token claims")
	}
	return claims.OpenAIAuth.AccountID, claims.ExpiresAt * 1000, nil
}

func deviceAuthTokenPayload(token string) (deviceAuthJWTPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return deviceAuthJWTPayload{}, errors.New("invalid access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return deviceAuthJWTPayload{}, errors.New("invalid access token")
	}
	var claims deviceAuthJWTPayload
	if err := json.Unmarshal(payload, &claims); err != nil {
		return deviceAuthJWTPayload{}, errors.New("invalid access token")
	}
	return claims, nil
}

func decodeLimitedJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	return decoder.Decode(target)
}
