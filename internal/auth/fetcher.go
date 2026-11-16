package auth

import (
	"fmt"
	"sync"
	"time"

	"github.com/dvcrn/codex-oauth-proxy/internal/credentials"
	"github.com/rs/zerolog"
)

// OAuthFetcher wraps a credentials fetcher with OAuth token refresh capability
type OAuthFetcher struct {
	baseFetcher credentials.OAuthCredentialsFetcher
	logger      *zerolog.Logger
	mu          sync.RWMutex
	stopCh      chan struct{}
}

// NewOAuthFetcher creates a new OAuth credentials fetcher that wraps an existing fetcher
func NewOAuthFetcher(baseFetcher credentials.OAuthCredentialsFetcher, logger *zerolog.Logger) *OAuthFetcher {
	f := &OAuthFetcher{
		baseFetcher: baseFetcher,
		logger:      logger,
		stopCh:      make(chan struct{}),
	}
	// Start background refresh goroutine
	go f.backgroundRefresh()
	return f
}

// GetCredentials returns the access token and user ID, refreshing if necessary
func (o *OAuthFetcher) GetCredentials() (string, string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Get full credentials including refresh token and expiry
	creds, err := o.baseFetcher.GetFullCredentials()
	if err != nil {
		return "", "", fmt.Errorf("failed to get full credentials: %w", err)
	}

	// Check if token needs refresh
	if TokenExpired(creds.ExpiresAt) {
		if o.logger != nil {
			minutesUntilExpiry := (creds.ExpiresAt - UnixMillis()) / 1000 / 60
			o.logger.Info().
				Int64("minutes_until_expiry", minutesUntilExpiry).
				Msg("🔄 OAuth token expired or expiring soon, refreshing...")
		}

		// Perform token refresh
		newTokens, err := RefreshToken(creds.RefreshToken)
		if err != nil {
			if o.logger != nil {
				o.logger.Error().Err(err).Msg("❌ Failed to refresh OAuth token")
			}
			// Return existing credentials even if refresh failed
			// The server will handle 401 responses
			return creds.AccessToken, creds.UserID, nil
		}

		// Calculate new expiry time
		expiresAt := CalculateExpiresAt(newTokens.ExpiresIn)

		// Update tokens in the underlying storage
		if err := o.baseFetcher.UpdateTokens(newTokens.AccessToken, newTokens.RefreshToken, expiresAt); err != nil {
			if o.logger != nil {
				o.logger.Error().Err(err).Msg("❌ Failed to update tokens in storage")
			}
			// Return new tokens even if storage update failed
			return newTokens.AccessToken, creds.UserID, nil
		}

		if o.logger != nil {
			o.logger.Info().Msg("✅ OAuth token refreshed successfully")
		}

		return newTokens.AccessToken, creds.UserID, nil
	}

	// Token is still valid
	if o.logger != nil {
		minutesUntilExpiry := (creds.ExpiresAt - UnixMillis()) / 1000 / 60
		o.logger.Debug().
			Int64("minutes_until_expiry", minutesUntilExpiry).
			Msg("✅ OAuth token is still valid")
	}

	return creds.AccessToken, creds.UserID, nil
}

// RefreshCredentials forces a token refresh
func (o *OAuthFetcher) RefreshCredentials() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Get current credentials
	creds, err := o.baseFetcher.GetFullCredentials()
	if err != nil {
		return fmt.Errorf("failed to get full credentials: %w", err)
	}

	// Perform token refresh
	newTokens, err := RefreshToken(creds.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	// Calculate new expiry time
	expiresAt := CalculateExpiresAt(newTokens.ExpiresIn)

	// Update tokens in the underlying storage
	if err := o.baseFetcher.UpdateTokens(newTokens.AccessToken, newTokens.RefreshToken, expiresAt); err != nil {
		return fmt.Errorf("failed to update tokens: %w", err)
	}

	return nil
}

// GetFullCredentials passes through to the base fetcher
func (o *OAuthFetcher) GetFullCredentials() (*credentials.OAuthCredentials, error) {
	return o.baseFetcher.GetFullCredentials()
}

// UpdateTokens passes through to the base fetcher
func (o *OAuthFetcher) UpdateTokens(accessToken, refreshToken string, expiresAt int64) error {
	return o.baseFetcher.UpdateTokens(accessToken, refreshToken, expiresAt)
}

// UnixMillis returns the current time in milliseconds
func UnixMillis() int64 {
	return UnixNano() / 1e6
}

// UnixNano returns the current time in nanoseconds
func UnixNano() int64 {
	return int64(time.Now().UnixNano())
}

// backgroundRefresh periodically checks and refreshes tokens if they're expiring soon
func (o *OAuthFetcher) backgroundRefresh() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			o.checkAndRefreshToken()
		case <-o.stopCh:
			if o.logger != nil {
				o.logger.Debug().Msg("Background token refresh stopped")
			}
			return
		}
	}
}

// checkAndRefreshToken checks if the token needs refresh and performs it if necessary
func (o *OAuthFetcher) checkAndRefreshToken() {
	o.mu.Lock()
	defer o.mu.Unlock()

	creds, err := o.baseFetcher.GetFullCredentials()
	if err != nil {
		if o.logger != nil {
			o.logger.Error().Err(err).Msg("Background refresh: failed to get credentials")
		}
		return
	}

	// Check if token needs refresh
	if !TokenExpired(creds.ExpiresAt) {
		if o.logger != nil {
			minutesUntilExpiry := (creds.ExpiresAt - UnixMillis()) / 1000 / 60
			o.logger.Debug().
				Int64("minutes_until_expiry", minutesUntilExpiry).
				Msg("Background refresh: token still valid")
		}
		return
	}

	if o.logger != nil {
		minutesUntilExpiry := (creds.ExpiresAt - UnixMillis()) / 1000 / 60
		o.logger.Info().
			Int64("minutes_until_expiry", minutesUntilExpiry).
			Msg("🔄 Background refresh: token expiring soon, refreshing...")
	}

	// Perform token refresh
	newTokens, err := RefreshToken(creds.RefreshToken)
	if err != nil {
		if o.logger != nil {
			o.logger.Error().Err(err).Msg("❌ Background refresh: failed to refresh token")
		}
		return
	}

	// Calculate new expiry time
	expiresAt := CalculateExpiresAt(newTokens.ExpiresIn)

	// Update tokens in the underlying storage
	if err := o.baseFetcher.UpdateTokens(newTokens.AccessToken, newTokens.RefreshToken, expiresAt); err != nil {
		if o.logger != nil {
			o.logger.Error().Err(err).Msg("❌ Background refresh: failed to update tokens in storage")
		}
		return
	}

	if o.logger != nil {
		minutesUntilExpiry := (expiresAt - UnixMillis()) / 1000 / 60
		o.logger.Info().
			Int64("new_expiry_minutes", minutesUntilExpiry).
			Msg("✅ Background refresh: token refreshed successfully")
	}
}

// Close stops the background refresh goroutine
func (o *OAuthFetcher) Close() {
	close(o.stopCh)
}
