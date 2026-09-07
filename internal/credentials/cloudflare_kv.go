//go:build js && wasm

package credentials

import (
	"encoding/json"
	"fmt"

	"github.com/syumai/workers/cloudflare/kv"
)

// kvCredentials represents the OAuth credentials stored in KV
type kvCredentials struct {
	ClaudeAiOauth struct {
		AccessToken  string   `json:"accessToken"`
		RefreshToken string   `json:"refreshToken"`
		ExpiresAt    int64    `json:"expiresAt"`
		Scopes       []string `json:"scopes"`
	} `json:"claudeAiOauth"`
	UserID string `json:"userID"`
}

// CloudflareKVFetcher retrieves credentials from Cloudflare KV
const deviceAuthSessionKey = "device-auth"

type CloudflareKVFetcher struct {
	kvStore *kv.Namespace
}

// NewCloudflareKVFetcher creates a new Cloudflare KV-based credentials fetcher
func NewCloudflareKVFetcher() (*CloudflareKVFetcher, error) {
	// In Cloudflare Workers, KV namespaces are accessed via bindings
	// The binding name is configured in wrangler.toml
	kvStore, err := kv.NewNamespace("CODEX_AUTH")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize KV namespace: %w", err)
	}
	return &CloudflareKVFetcher{kvStore: kvStore}, nil
}

// GetCredentials retrieves credentials from Cloudflare KV
func (c *CloudflareKVFetcher) GetCredentials() (string, string, error) {
	creds, err := c.getKVCredentials()
	if err != nil {
		return "", "", err
	}

	return creds.ClaudeAiOauth.AccessToken, creds.UserID, nil
}

// GetFullCredentials retrieves full OAuth credentials from Cloudflare KV
func (c *CloudflareKVFetcher) GetFullCredentials() (*OAuthCredentials, error) {
	creds, err := c.getKVCredentials()
	if err != nil {
		return nil, err
	}

	return &OAuthCredentials{
		AccessToken:  creds.ClaudeAiOauth.AccessToken,
		RefreshToken: creds.ClaudeAiOauth.RefreshToken,
		ExpiresAt:    creds.ClaudeAiOauth.ExpiresAt,
		UserID:       creds.UserID,
	}, nil
}

// UpdateTokens updates OAuth tokens in Cloudflare KV
func (c *CloudflareKVFetcher) UpdateTokens(accessToken, refreshToken string, expiresAt int64) error {
	// Try to get current credentials to preserve other fields
	creds, err := c.getKVCredentials()
	if err != nil {
		// If credentials don't exist, create new ones with defaults
		// This handles initial setup case
		// For initial setup, you should use SetInitialCredentials which includes userID
		creds = &kvCredentials{
			UserID: "unknown", // Default for backwards compatibility
		}
		creds.ClaudeAiOauth.Scopes = []string{"user:inference", "user:profile"}
	}

	// Update tokens
	creds.ClaudeAiOauth.AccessToken = accessToken
	creds.ClaudeAiOauth.RefreshToken = refreshToken
	creds.ClaudeAiOauth.ExpiresAt = expiresAt

	// Save back to KV
	return c.setKVCredentials(creds)
}

// SetInitialCredentials sets initial OAuth credentials (used for initial setup)
func (c *CloudflareKVFetcher) SetInitialCredentials(accessToken, refreshToken string, expiresAt int64, userID string, scopes []string) error {
	creds := &kvCredentials{
		UserID: userID,
	}

	creds.ClaudeAiOauth.AccessToken = accessToken
	creds.ClaudeAiOauth.RefreshToken = refreshToken
	creds.ClaudeAiOauth.ExpiresAt = expiresAt

	if scopes != nil {
		creds.ClaudeAiOauth.Scopes = scopes
	} else {
		// Default scopes
		creds.ClaudeAiOauth.Scopes = []string{"user:inference", "user:profile"}
	}

	return c.setKVCredentials(creds)
}

func (c *CloudflareKVFetcher) LoadDeviceAuthSession() ([]byte, error) {
	session, err := c.kvStore.GetString(deviceAuthSessionKey, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get device auth session: %w", err)
	}
	if session == "" || session == "<null>" {
		return nil, nil
	}
	return []byte(session), nil
}

func (c *CloudflareKVFetcher) SaveDeviceAuthSession(session []byte) error {
	if err := c.kvStore.PutString(deviceAuthSessionKey, string(session), nil); err != nil {
		return fmt.Errorf("failed to save device auth session: %w", err)
	}
	return nil
}

func (c *CloudflareKVFetcher) StoreCredentials(accessToken, refreshToken string, expiresAt int64, userID string) error {
	return c.SetInitialCredentials(accessToken, refreshToken, expiresAt, userID, nil)
}

func (c *CloudflareKVFetcher) CompleteDeviceAuth(accessToken, refreshToken string, expiresAt int64, userID string) error {
	if err := c.StoreCredentials(accessToken, refreshToken, expiresAt, userID); err != nil {
		return err
	}
	return c.SaveDeviceAuthSession([]byte(`{"status":"authenticated"}`))
}

// RefreshCredentials is a no-op for Cloudflare KV credentials
// The actual refresh is handled by the OAuthFetcher wrapper
func (c *CloudflareKVFetcher) RefreshCredentials() error {
	return nil
}

// getKVCredentials retrieves and unmarshals credentials from KV
func (c *CloudflareKVFetcher) getKVCredentials() (*kvCredentials, error) {
	// Get credentials JSON from KV
	credsJSON, err := c.kvStore.GetString("claude_oauth_credentials", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get credentials from KV: %w", err)
	}

	if credsJSON == "" {
		return nil, fmt.Errorf("no credentials found in KV")
	}

	// Parse JSON
	var creds kvCredentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials JSON: %w", err)
	}

	return &creds, nil
}

// setKVCredentials marshals and stores credentials to KV
func (c *CloudflareKVFetcher) setKVCredentials(creds *kvCredentials) error {
	// Marshal to JSON
	credsJSON, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	// Store in KV
	if err := c.kvStore.PutString("claude_oauth_credentials", string(credsJSON), nil); err != nil {
		return fmt.Errorf("failed to store credentials in KV: %w", err)
	}

	return nil
}
