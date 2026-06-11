package main

import (
	"flag"
	"net/http"
	"os"

	"github.com/dvcrn/codex-oauth-proxy/internal/app"
	"github.com/dvcrn/codex-oauth-proxy/internal/auth"
	"github.com/dvcrn/codex-oauth-proxy/internal/credentials"
	"github.com/dvcrn/codex-oauth-proxy/internal/logger"
	"github.com/rs/zerolog"
)

func main() {
	credsStore := flag.String("creds-store", "auto", "Credential store mode: auto|xdg|legacy|keychain|env")
	credsPath := flag.String("creds-path", "", "Override path for filesystem credentials (for xdg/legacy modes)")
	disableRefresh := flag.Bool("disable-migrate-refresh", false, "Skip immediate token refresh after migration")
	flag.Parse()

	log := logger.New()

	log.Info().
		Str("creds_store", *credsStore).
		Str("creds_path", *credsPath).
		Msg("🚀 Starting codex-oauth-proxy with credential configuration")

	var credsFetcher credentials.CredentialsFetcher
	var fsPath string

	switch *credsStore {
	case "auto", "xdg":
		fsPath = *credsPath
		if fsPath == "" {
			fsPath = credentials.DefaultCredsPath()
			log.Info().
				Str("xdg_config_path", fsPath).
				Msg("📂 Using XDG config path for credentials")
		} else {
			log.Info().
				Str("custom_path", fsPath).
				Msg("📂 Using custom path for credentials")
		}

		if *credsStore == "auto" {
			if err := maybeMigrateCredentials(fsPath, *disableRefresh, log); err != nil {
				log.Error().
					Err(err).
					Str("target_path", fsPath).
					Msg("❌ Migration failed, will attempt to use existing credentials if available")
			}
		}

		fsFetcher := credentials.NewFSCredentialsFetcher(fsPath)
		oauthFetcher := auth.NewOAuthFetcher(fsFetcher, &log)
		credsFetcher = oauthFetcher

		log.Info().
			Str("path", fsPath).
			Msg("📄 Using filesystem credentials fetcher with OAuth token refresh")

	case "legacy":
		fsPath = *credsPath
		if fsPath == "" {
			fsPath = credentials.LegacyCredsPath()
			log.Info().
				Str("legacy_path", fsPath).
				Msg("📂 Using legacy credentials path")
		}

		fsFetcher := credentials.NewFSCredentialsFetcher(fsPath)
		credsFetcher = auth.NewOAuthFetcher(fsFetcher, &log)

		log.Info().
			Str("path", fsPath).
			Msg("📄 Using legacy filesystem credentials fetcher with OAuth token refresh")

	case "keychain":
		keychainFetcher := credentials.NewKeychainCredentialsFetcherWithLogger(log)
		credsFetcher = auth.NewOAuthFetcher(keychainFetcher, &log)
		log.Info().Msg("🔑 Using keychain credentials fetcher with OAuth token refresh")

	case "env":
		credsFetcher = credentials.NewEnvCredentialsFetcher()
		log.Info().Msg("📝 Using environment credentials fetcher")

	default:
		log.Fatal().
			Str("creds_store", *credsStore).
			Msg("❌ Invalid creds-store mode. Valid options: auto|xdg|legacy|keychain|env")
	}

	// Validate credentials at startup
	validateCredentialsAtStartup(credsFetcher, log)

	// Create server using shared setup
	srv := app.NewServer(credsFetcher, log)

	port := os.Getenv("PORT")
	if port == "" {
		port = "9879"
	}

	log.Info().Str("port", port).Msg("Starting server")
	log.Fatal().Err(http.ListenAndServe(":"+port, srv)).Msg("Server failed to start")
}

func maybeMigrateCredentials(targetPath string, disableRefresh bool, log zerolog.Logger) error {
	log.Info().
		Str("target_path", targetPath).
		Msg("🔍 Checking if credentials migration is needed")

	if credentials.FileExists(targetPath) {
		log.Info().
			Str("target_path", targetPath).
			Msg("✅ Credentials already exist at target path, skipping migration")
		return nil
	}

	log.Info().
		Str("target_path", targetPath).
		Msg("📦 Target credentials file not found, attempting migration")

	// Source files to check in priority order before falling back to keychain:
	//  1. the old XDG path used before the codex-proxy -> codex-oauth-proxy rename
	//  2. the legacy ~/.codex/auth.json path
	fileSources := []struct {
		path  string
		label string
	}{
		{credentials.OldDefaultCredsPath(), "old XDG file"},
		{credentials.LegacyCredsPath(), "legacy file"},
	}

	var migratedCreds *credentials.OAuthCredentials
	var sourceType string

	for _, src := range fileSources {
		if src.path == "" || !credentials.FileExists(src.path) {
			log.Info().
				Str("source_path", src.path).
				Str("source", src.label).
				Msg("🔍 No credentials at this path, continuing")
			continue
		}

		log.Info().
			Str("source_path", src.path).
			Str("source", src.label).
			Msg("📄 Found credentials file, reading OAuth tokens")

		fsFetcher := credentials.NewFSCredentialsFetcher(src.path)
		creds, err := fsFetcher.GetFullCredentials()
		if err != nil {
			log.Error().
				Err(err).
				Str("source_path", src.path).
				Msg("❌ Failed to read credentials file")
			return err
		}

		migratedCreds = creds
		sourceType = src.label

		log.Info().
			Str("user_id", creds.UserID).
			Int64("expires_at", creds.ExpiresAt).
			Str("source", sourceType).
			Msg("✅ Successfully read credentials from file")
		break
	}

	if migratedCreds == nil {
		log.Info().
			Msg("⚠️  No credentials files found, trying keychain")

		keychainCreds, err := credentials.ReadOAuthFromKeychain()
		if err != nil {
			log.Error().
				Err(err).
				Msg("❌ Failed to read credentials from keychain")
			return err
		}

		migratedCreds = keychainCreds
		sourceType = "keychain"

		log.Info().
			Str("user_id", keychainCreds.UserID).
			Int64("expires_at", keychainCreds.ExpiresAt).
			Str("source", sourceType).
			Msg("✅ Successfully read credentials from keychain")
	}

	log.Info().
		Str("target_path", targetPath).
		Str("source", sourceType).
		Msg("💾 Writing migrated credentials to target file")

	if err := credentials.InitFromOAuth(targetPath, migratedCreds); err != nil {
		log.Error().
			Err(err).
			Str("target_path", targetPath).
			Msg("❌ Failed to write credentials to target file")
		return err
	}

	info, err := os.Stat(targetPath)
	if err == nil {
		log.Info().
			Str("target_path", targetPath).
			Str("permissions", info.Mode().String()).
			Int64("size_bytes", info.Size()).
			Msg("✅ Credentials file created successfully")
	}

	if disableRefresh {
		log.Info().Msg("⏭️  Skipping immediate token refresh (disabled by flag)")
		return nil
	}

	log.Info().
		Str("source", sourceType).
		Msg("🔄 Performing immediate token refresh to establish independent token chain")

	fsFetcher := credentials.NewFSCredentialsFetcher(targetPath)
	oauthFetcher := auth.NewOAuthFetcher(fsFetcher, &log)

	if err := oauthFetcher.RefreshCredentials(); err != nil {
		log.Warn().
			Err(err).
			Msg("⚠️  Failed to refresh tokens after migration; will retry on first request")
		return nil
	}

	log.Info().Msg("✅ Token refresh successful, independent token chain established")

	refreshedCreds, err := fsFetcher.GetFullCredentials()
	if err == nil {
		now := auth.UnixMillis()
		minutesUntilExpiry := (refreshedCreds.ExpiresAt - now) / 1000 / 60
		log.Info().
			Int64("minutes_until_expiry", minutesUntilExpiry).
			Msg("🕐 New token expiry status")
	}

	return nil
}

func validateCredentialsAtStartup(credsFetcher credentials.CredentialsFetcher, log zerolog.Logger) {
	// Try to get basic credentials
	token, userID, err := credsFetcher.GetCredentials()
	if err != nil {
		log.Error().Err(err).Msg("⚠️  Failed to validate credentials at startup")
		return
	}

	log.Info().
		Str("user_id", userID).
		Int("token_length", len(token)).
		Msg("✅ Credentials loaded successfully")

	// Check if this is an OAuth fetcher with expiry information
	if oauthFetcher, ok := credsFetcher.(credentials.OAuthCredentialsFetcher); ok {
		creds, err := oauthFetcher.GetFullCredentials()
		if err != nil {
			log.Warn().Err(err).Msg("⚠️  Could not get full OAuth credentials for validation")
			return
		}

		// Calculate time until expiry
		now := auth.UnixMillis()
		minutesUntilExpiry := (creds.ExpiresAt - now) / 1000 / 60

		if minutesUntilExpiry <= 0 {
			log.Warn().
				Int64("minutes_expired", -minutesUntilExpiry).
				Msg("⚠️  Token is already expired, will attempt refresh on first request")
		} else if minutesUntilExpiry <= 60 {
			log.Warn().
				Int64("minutes_until_expiry", minutesUntilExpiry).
				Msg("⚠️  Token expires soon, will refresh shortly")
		} else {
			log.Info().
				Int64("minutes_until_expiry", minutesUntilExpiry).
				Msg("✅ Token is valid and not expiring soon")
		}
	}
}
