package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dvcrn/codex-oauth-proxy/internal/credentials"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type deviceAuthTestStore struct {
	session      []byte
	accessToken  string
	refreshToken string
	accountID    string
	expiresAt    int64
}

func (s *deviceAuthTestStore) LoadDeviceAuthSession() ([]byte, error) {
	return bytes.Clone(s.session), nil
}

func (s *deviceAuthTestStore) SaveDeviceAuthSession(session []byte) error {
	s.session = bytes.Clone(session)
	return nil
}

func (s *deviceAuthTestStore) StoreCredentials(accessToken, refreshToken string, expiresAt int64, accountID string) error {
	s.accessToken = accessToken
	s.refreshToken = refreshToken
	s.expiresAt = expiresAt
	s.accountID = accountID
	return nil
}

func (s *deviceAuthTestStore) CompleteDeviceAuth(accessToken, refreshToken string, expiresAt int64, accountID string) error {
	if err := s.StoreCredentials(accessToken, refreshToken, expiresAt, accountID); err != nil {
		return err
	}
	s.session = []byte(`{"status":"authenticated"}`)
	return nil
}

type deviceAuthTestClient struct {
	responses []*http.Response
	requests  []*http.Request
	bodies    [][]byte
}

func (c *deviceAuthTestClient) Do(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	c.requests = append(c.requests, req.Clone(context.Background()))
	c.bodies = append(c.bodies, body)
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func deviceAuthTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}
}

func TestDeviceAuthCompletesLoginAndPersistsCredentials(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	tokenExpiresAt := now.Add(time.Hour)
	claims, err := json.Marshal(map[string]any{
		"exp":                         tokenExpiresAt.Unix(),
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account-1"},
	})
	require.NoError(t, err)
	accessToken := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	store := &deviceAuthTestStore{}
	client := &deviceAuthTestClient{responses: []*http.Response{
		deviceAuthTestResponse(http.StatusOK, `{"device_auth_id":"private-device","user_code":"ABCD-EFGH","interval":"5"}`),
		deviceAuthTestResponse(http.StatusBadRequest, `{"error":{"code":"deviceauth_authorization_pending"}}`),
		deviceAuthTestResponse(http.StatusOK, `{"authorization_code":"private-code","code_verifier":"private-verifier"}`),
		deviceAuthTestResponse(http.StatusOK, `{"access_token":"`+accessToken+`","refresh_token":"new-refresh"}`),
	}}
	auth := newDeviceAuth(store, client)
	auth.now = func() time.Time { return now }

	started, err := auth.Start(context.Background())
	require.NoError(t, err)
	assert.Equal(t, deviceAuthStatus{
		Status:            "pending",
		VerificationURL:   "https://auth.openai.com/codex/device",
		UserCode:          "ABCD-EFGH",
		ExpiresAt:         now.Add(15 * time.Minute).Format(time.RFC3339Nano),
		RetryAfterSeconds: 5,
	}, started)
	assert.NotContains(t, string(mustJSON(t, started)), "private-device")

	now = now.Add(5 * time.Second)
	pending, err := auth.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "pending", pending.Status)

	now = now.Add(5 * time.Second)
	completed, err := auth.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, deviceAuthStatus{Status: "authenticated"}, completed)
	assert.Equal(t, accessToken, store.accessToken)
	assert.Equal(t, "new-refresh", store.refreshToken)
	assert.Equal(t, "account-1", store.accountID)
	assert.Equal(t, tokenExpiresAt.UnixMilli(), store.expiresAt)

	require.Len(t, client.requests, 4)
	assert.Equal(t, []string{
		"https://auth.openai.com/api/accounts/deviceauth/usercode",
		"https://auth.openai.com/api/accounts/deviceauth/token",
		"https://auth.openai.com/api/accounts/deviceauth/token",
		"https://auth.openai.com/oauth/token",
	}, []string{
		client.requests[0].URL.String(),
		client.requests[1].URL.String(),
		client.requests[2].URL.String(),
		client.requests[3].URL.String(),
	})
	assert.Equal(t, "application/json", client.requests[0].Header.Get("Content-Type"))
	assert.JSONEq(t, `{"client_id":"app_EMoamEEZ73f0CkXaXp7hrann"}`, string(client.bodies[0]))
	assert.JSONEq(t, `{"device_auth_id":"private-device","user_code":"ABCD-EFGH"}`, string(client.bodies[1]))
	form, err := url.ParseQuery(string(client.bodies[3]))
	require.NoError(t, err)
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, deviceAuthClientID, form.Get("client_id"))
	assert.Equal(t, "private-code", form.Get("code"))
	assert.Equal(t, "private-verifier", form.Get("code_verifier"))
	assert.Equal(t, "https://auth.openai.com/deviceauth/callback", form.Get("redirect_uri"))
}

type deviceAuthCredentialsFetcher struct {
	credentials *credentials.OAuthCredentials
}

func (f *deviceAuthCredentialsFetcher) GetCredentials() (string, string, error) {
	return f.credentials.AccessToken, f.credentials.UserID, nil
}

func (f *deviceAuthCredentialsFetcher) RefreshCredentials() error {
	return nil
}

func (f *deviceAuthCredentialsFetcher) GetFullCredentials() (*credentials.OAuthCredentials, error) {
	return f.credentials, nil
}

func (f *deviceAuthCredentialsFetcher) UpdateTokens(accessToken, refreshToken string, expiresAt int64) error {
	f.credentials.AccessToken = accessToken
	f.credentials.RefreshToken = refreshToken
	f.credentials.ExpiresAt = expiresAt
	return nil
}

func TestManualTokenRoutesUseSourceContract(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", "test-admin-token-with-at-least-32-characters")
	expiresAt := time.Now().Add(time.Hour).Truncate(time.Second)
	claims, err := json.Marshal(map[string]any{
		"exp":                         expiresAt.Unix(),
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account-from-token"},
	})
	require.NoError(t, err)
	accessToken := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	store := &deviceAuthTestStore{}
	fetcher := &deviceAuthCredentialsFetcher{credentials: &credentials.OAuthCredentials{
		RefreshToken: "preserved-refresh",
	}}
	server := New(zerolog.Nop(), fetcher, WithDeviceAuth(store))

	request := httptest.NewRequest(http.MethodPost, "https://worker.test/admin/tokens", strings.NewReader(`{"accessToken":"`+accessToken+`","idToken":"ignored","lastRefresh":"2026-09-07T12:00:00Z"}`))
	request.Header.Set("Authorization", "Bearer test-admin-token-with-at-least-32-characters")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"stored":true}`, response.Body.String())
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, accessToken, store.accessToken)
	assert.Equal(t, "preserved-refresh", store.refreshToken)
	assert.Equal(t, "account-from-token", store.accountID)
	assert.Equal(t, expiresAt.UnixMilli(), store.expiresAt)

	statusRequest := httptest.NewRequest(http.MethodGet, "https://worker.test/admin/status", nil)
	statusRequest.Header.Set("Authorization", "Bearer test-admin-token-with-at-least-32-characters")
	statusResponse := httptest.NewRecorder()
	server.ServeHTTP(statusResponse, statusRequest)
	assert.Equal(t, http.StatusOK, statusResponse.Code)
	assert.JSONEq(t, `{"configured":false}`, statusResponse.Body.String())
}

func TestDeviceAuthThrottlesAndExpires(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store := &deviceAuthTestStore{}
	client := &deviceAuthTestClient{responses: []*http.Response{
		deviceAuthTestResponse(http.StatusOK, `{"device_auth_id":"device","user_code":"CODE","interval":5}`),
	}}
	auth := newDeviceAuth(store, client)
	auth.now = func() time.Time { return now }

	_, err := auth.Start(context.Background())
	require.NoError(t, err)
	status, err := auth.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "pending", status.Status)
	require.Len(t, client.requests, 1)

	now = now.Add(deviceAuthLifetime)
	status, err = auth.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, deviceAuthStatus{Status: "expired"}, status)
	require.Len(t, client.requests, 1)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}
