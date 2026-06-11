package app

import (
	"github.com/dvcrn/codex-oauth-proxy/internal/credentials"
	"github.com/dvcrn/codex-oauth-proxy/internal/server"
	"github.com/rs/zerolog"
)

// NewServer creates a new server instance with the given credentials fetcher
func NewServer(credsFetcher credentials.CredentialsFetcher, logger zerolog.Logger) *server.Server {
	// Create server with the credentials fetcher
	return server.New(logger, credsFetcher)
}
