//go:build js && wasm

package server

import (
	"net/http"
	"syscall/js"

	"github.com/syumai/workers/cloudflare"
	"github.com/syumai/workers/cloudflare/fetch"
)

// WorkersHTTPClient implements HTTPClient for Cloudflare Workers
type WorkersHTTPClient struct {
	client *fetch.Client
}

// shareFetchesAcrossRequests reports whether one request may wait on an
// upstream fetch another request started. Workers forbids sharing I/O between
// requests and may cancel a request's subrequests once it returns.
const shareFetchesAcrossRequests = false

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
