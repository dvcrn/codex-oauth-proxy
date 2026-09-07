package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/dvcrn/codex-oauth-proxy/internal/env"
)

// adminMiddleware checks for valid admin API key from either
// 'Authorization: Bearer <key>' or 'X-API-Key: <key>' headers.
func (s *Server) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminKey, ok := env.Get("ADMIN_API_KEY")
		if !ok || adminKey == "" {
			s.logger.Error().Msg("ADMIN_API_KEY environment variable not set")
			http.Error(w, "Admin API not configured", http.StatusInternalServerError)
			return
		}

		var providedToken string
		authHeader := r.Header.Get("Authorization")
		xAPIKeyHeader := r.Header.Get("X-API-Key")

		if authHeader != "" {
			// Expect "Bearer <token>" format, case-insensitive
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				s.logger.Warn().
					Str("method", r.Method).
					Str("uri", r.RequestURI).
					Str("remote_addr", r.RemoteAddr).
					Msg("Invalid Authorization header format for admin endpoint")
				writeUnauthorized(w, "Invalid Authorization header format")
				return
			}
			providedToken = parts[1]
		} else if xAPIKeyHeader != "" {
			// Use the key from X-API-Key header directly
			providedToken = xAPIKeyHeader
		} else {
			s.logger.Warn().
				Str("method", r.Method).
				Str("uri", r.RequestURI).
				Str("remote_addr", r.RemoteAddr).
				Msg("Missing required Authorization or X-API-Key header for admin endpoint")
			writeUnauthorized(w, "Unauthorized")
			return
		}

		providedHash := sha256.Sum256([]byte(providedToken))
		expectedHash := sha256.Sum256([]byte(adminKey))
		if subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) != 1 {
			s.logger.Warn().
				Str("method", r.Method).
				Str("uri", r.RequestURI).
				Str("remote_addr", r.RemoteAddr).
				Msg("Invalid admin API key provided")
			writeUnauthorized(w, "Unauthorized")
			return
		}

		// Admin authorized
		s.logger.Info().
			Str("method", r.Method).
			Str("uri", r.RequestURI).
			Str("remote_addr", r.RemoteAddr).
			Msg("Admin request authorized")

		next(w, r)
	}
}

func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, message, http.StatusUnauthorized)
}
