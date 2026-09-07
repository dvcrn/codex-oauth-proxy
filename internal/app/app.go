package app

import (
	"github.com/dvcrn/codex-oauth-proxy/internal/credentials"
	"github.com/dvcrn/codex-oauth-proxy/internal/server"
	"github.com/rs/zerolog"
)

// NewServer creates a new server instance with the given credentials fetcher
func NewServer(credsFetcher credentials.CredentialsFetcher, logger zerolog.Logger, options ...server.Option) *server.Server {
	return server.New(logger, credsFetcher, options...)
}
