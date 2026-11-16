//go:build js && wasm

package auth

import (
	"net/http"
	"syscall/js"

	"github.com/syumai/workers/cloudflare"
	"github.com/syumai/workers/cloudflare/fetch"
)

// workersHTTPClient routes requests through the Workers fetch API. The stdlib
// transport is unavailable under js/wasm and panics with "Illegal invocation".
type workersHTTPClient struct {
	client *fetch.Client
}

// newHTTPClient returns the HTTP client used for OAuth token refresh.
func newHTTPClient() httpDoer {
	binding := cloudflare.GetBinding("CODEX_EGRESS")
	namespace := js.Global().Get("Object").New()
	namespace.Set("fetch", binding.Get("fetch").Call("bind", binding))
	return &workersHTTPClient{
		client: fetch.NewClient(fetch.WithBinding(namespace)),
	}
}

func (c *workersHTTPClient) Do(req *http.Request) (*http.Response, error) {
	fetchReq, err := fetch.NewRequest(req.Context(), req.Method, req.URL.String(), req.Body)
	if err != nil {
		return nil, err
	}

	for key, values := range req.Header {
		for _, value := range values {
			fetchReq.Header.Set(key, value)
		}
	}

	return c.client.Do(fetchReq, &fetch.RequestInit{Redirect: fetch.RedirectModeManual})
}
