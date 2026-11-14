package credentials

import (
	"encoding/json"
	"fmt"
	"os"
)

type fsAuth struct {
	Tokens struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
		ExpiresAt    int64  `json:"expiresAt,omitempty"`
	} `json:"tokens"`
}

type FSCredentialsFetcher struct {
	Path string
}

func NewFSCredentialsFetcher(path string) *FSCredentialsFetcher {
	return &FSCredentialsFetcher{Path: path}
}

func InitFromOAuth(path string, creds *OAuthCredentials) error {
	if err := EnsureParentDir(path); err != nil {
		return fmt.Errorf("failed to ensure parent directory: %w", err)
	}

	var auth fsAuth
	auth.Tokens.AccessToken = creds.AccessToken
	auth.Tokens.RefreshToken = creds.RefreshToken
	auth.Tokens.ExpiresAt = creds.ExpiresAt
	auth.Tokens.AccountID = creds.UserID

	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	return nil
}

func (f *FSCredentialsFetcher) GetCredentials() (string, string, error) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return "", "", fmt.Errorf("failed to read credentials file: %w", err)
	}
	var a fsAuth
	if err := json.Unmarshal(b, &a); err != nil {
		return "", "", fmt.Errorf("failed to parse credentials file: %w", err)
	}
	// Prefer OAuth access token for upstream API; fall back to ID token
	token := a.Tokens.AccessToken
	if token == "" {
		token = a.Tokens.IDToken
	}
	if token == "" || a.Tokens.AccountID == "" {
		return "", "", fmt.Errorf("missing token or account_id in credentials file")
	}
	return token, a.Tokens.AccountID, nil
}

func (f *FSCredentialsFetcher) RefreshCredentials() error {
	return nil
}

// GetFullCredentials returns full OAuth credentials including refresh token and expiry
func (f *FSCredentialsFetcher) GetFullCredentials() (*OAuthCredentials, error) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	var a fsAuth
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("failed to parse credentials file: %w", err)
	}

	// Prefer OAuth access token for upstream API; fall back to ID token
	token := a.Tokens.AccessToken
	if token == "" {
		token = a.Tokens.IDToken
	}

	if token == "" || a.Tokens.AccountID == "" {
		return nil, fmt.Errorf("missing token or account_id in credentials file")
	}

	return &OAuthCredentials{
		AccessToken:  token,
		RefreshToken: a.Tokens.RefreshToken,
		ExpiresAt:    a.Tokens.ExpiresAt,
		UserID:       a.Tokens.AccountID,
	}, nil
}

// UpdateTokens updates the OAuth tokens in the filesystem credentials file
func (f *FSCredentialsFetcher) UpdateTokens(accessToken, refreshToken string, expiresAt int64) error {
	// Read current file
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return fmt.Errorf("failed to read credentials file: %w", err)
	}

	var a fsAuth
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("failed to parse credentials file: %w", err)
	}

	// Update tokens
	a.Tokens.AccessToken = accessToken
	a.Tokens.RefreshToken = refreshToken
	a.Tokens.ExpiresAt = expiresAt

	// Write back to file
	updatedData, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal updated credentials: %w", err)
	}

	if err := os.WriteFile(f.Path, updatedData, 0600); err != nil {
		return fmt.Errorf("failed to write updated credentials file: %w", err)
	}

	return nil
}
