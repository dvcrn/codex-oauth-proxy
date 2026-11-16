//go:build js && wasm

package server

import (
	"net/http"
	"syscall/js"

	"github.com/syumai/workers/cloudflare"
	"github.com/syumai/workers/cloudflare/fetch"
)

// detachBackgroundFetches is disabled on Workers because each WASM instance
// exits as soon as its HTTP response body closes.
const detachBackgroundFetches = false

// WorkersHTTPClient implements HTTPClient for Cloudflare Workers
type WorkersHTTPClient struct {
	client *fetch.Client
}

// NewHTTPClient creates a new HTTP client for Workers environment
func NewHTTPClient() HTTPClient {
	binding := cloudflare.GetBinding("CODEX_EGRESS")
	namespace := js.Global().Get("Object").New()
	namespace.Set("fetch", binding.Get("fetch").Call("bind", binding))
	return &WorkersHTTPClient{
		client: fetch.NewClient(fetch.WithBinding(namespace)),
	}
}

// Do performs an HTTP request using Cloudflare Workers fetch
func (c *WorkersHTTPClient) Do(req *http.Request) (*http.Response, error) {
	// Create a new fetch request
	fetchReq, err := fetch.NewRequest(req.Context(), req.Method, req.URL.String(), req.Body)
	if err != nil {
		return nil, err
	}

	// Copy headers from the original request
	for key, values := range req.Header {
		for _, value := range values {
			fetchReq.Header.Set(key, value)
		}
	}

	// Perform the request
	return c.client.Do(fetchReq, &fetch.RequestInit{Redirect: fetch.RedirectModeManual})
}
