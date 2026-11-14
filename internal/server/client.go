//go:build !js || !wasm

package server

import (
	"net"
	"net/http"
	"time"
)

// shareFetchesAcrossRequests reports whether one request may wait on an
// upstream fetch another request started. Workers forbids sharing I/O between
// requests and may cancel a request's subrequests once it returns.
const shareFetchesAcrossRequests = true

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
