//go:build !js || !wasm

package server

import (
	"net"
	"net/http"
	"time"
)

// detachBackgroundFetches reports whether upstream model refreshes may outlive
// the request that triggered them.
const detachBackgroundFetches = true

// NewHTTPClient creates a new HTTP client for regular environments
func NewHTTPClient() HTTPClient {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second, // connect timeout
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Do NOT set ResponseHeaderTimeout or a total timeout for SSE streams
	}
	return &http.Client{
		Transport: tr,
		// Timeout: 0 // (default) no total timeout; critical for SSE
	}
}
