//go:build !js || !wasm

package auth

import (
	"net/http"
	"time"
)

// newHTTPClient returns the HTTP client used for OAuth token refresh.
func newHTTPClient() httpDoer {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
