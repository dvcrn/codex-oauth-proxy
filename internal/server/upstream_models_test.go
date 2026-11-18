package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryModelsCacheStore struct {
	data  []byte
	ttl   time.Duration
	loads int
	saves int
}

func (m *memoryModelsCacheStore) LoadModelsCache() ([]byte, error) {
	m.loads++
	return m.data, nil
}

func (m *memoryModelsCacheStore) SaveModelsCache(data []byte, ttl time.Duration) error {
	m.saves++
	m.data = append([]byte(nil), data...)
	m.ttl = ttl
	return nil
}

func (m *memoryModelsCacheStore) ClearModelsCache() error {
	m.data = nil
	return nil
}

type codexVersionTestClient func(*http.Request) (*http.Response, error)

func (f codexVersionTestClient) Do(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestAdvertisedCodexClientVersion(t *testing.T) {
	for _, tc := range []struct {
		name, override, want string
	}{
		{name: "default", want: defaultCodexClientVersion},
		{name: "override", override: "0.166.0", want: "0.166.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEX_CLIENT_VERSION", tc.override)
			srv := New(zerolog.Nop(), stubCredentialsFetcher{})
			stub := &stubModelsHTTPClient{body: upstreamModelsFixture}
			srv.httpClient = codexVersionTestClient(func(r *http.Request) (*http.Response, error) {
				assert.Equal(t, tc.want, r.Header.Get("version"), "%s version", r.Method)
				assert.True(t, strings.HasPrefix(r.UserAgent(), "codex_cli_rs/"+tc.want+" "), "%s User-Agent: %q", r.Method, r.UserAgent())
				if r.Method == http.MethodGet {
					assert.Equal(t, tc.want, r.URL.Query().Get("client_version"))
				}
				return stub.Do(r)
			})

			_, _, status, _, err := srv.fetchUpstreamModelsOnce(t.Context())
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp, status, err := srv.makeChatGPTRequest(req, "https://chatgpt.com/backend-api/codex/responses", []byte(`{}`), "test-token", "test-account")
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, status)
			assert.Equal(t, 2, stub.calls)
		})
	}
}

func TestModelCatalogForLookupPolicy(t *testing.T) {
	t.Run("prefetched listing serves later lookups", func(t *testing.T) {
		stub := &stubModelsHTTPClient{body: upstreamModelsFixture}
		srv := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}, httpClient: stub}

		srv.PrefetchModels(t.Context())

		assert.True(t, srv.modelCatalogFor(t.Context()).lists("gpt-5.5"))
		assert.Equal(t, 1, stub.calls)
	})

	t.Run("failed lookup backs off", func(t *testing.T) {
		stub := &stubModelsHTTPClient{statusCode: http.StatusBadGateway}
		srv := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}, httpClient: stub}

		catalog := srv.modelCatalogFor(t.Context())
		assert.Nil(t, catalog)
		assert.Equal(t, "gpt-7-nova", normalizeModel("gpt-7-nova-high", catalog))

		assert.Nil(t, srv.modelCatalogFor(t.Context()))
		assert.Equal(t, 1, stub.calls, "a second lookup inside the backoff must not refetch")
	})

	t.Run("fresh cache is used during the backoff", func(t *testing.T) {
		srv := &Server{logger: zerolog.Nop()}
		srv.modelsCache = []upstreamModel{{Slug: "gpt-7-nova"}}
		srv.modelsCacheExpiry = time.Now().Add(time.Minute)
		srv.modelsLookupRetryAt = time.Now().Add(time.Minute)

		assert.True(t, srv.modelCatalogFor(t.Context()).lists("gpt-7-nova"))
	})

	t.Run("expired cache is served while it refreshes", func(t *testing.T) {
		stub := &blockingModelsHTTPClient{release: make(chan struct{})}
		srv := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}, httpClient: stub}
		srv.modelsCache = []upstreamModel{{Slug: "gpt-7-nova"}}
		srv.modelsCacheExpiry = time.Now().Add(-time.Minute)

		assert.True(t, srv.modelCatalogFor(t.Context()).lists("gpt-7-nova"))
		assert.True(t, srv.modelCatalogFor(t.Context()).lists("gpt-7-nova"))

		require.Eventually(t, func() bool { return stub.calls.Load() == 1 }, time.Second, time.Millisecond)
		close(stub.release)
		require.Eventually(t, func() bool {
			srv.modelsCacheMu.Lock()
			defer srv.modelsCacheMu.Unlock()
			return len(srv.modelsCache) == 2 && srv.modelsLookupRetryAt.IsZero()
		}, time.Second, time.Millisecond)
	})

	t.Run("expired cache refresh failure backs off instead of stampeding", func(t *testing.T) {
		var calls atomic.Int32
		failing := &atomicFailingModelsClient{calls: &calls}
		srv := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}, httpClient: failing}
		srv.modelsCache = []upstreamModel{{Slug: "gpt-7-nova"}}
		srv.modelsCacheExpiry = time.Now().Add(-time.Minute)

		assert.True(t, srv.modelCatalogFor(t.Context()).lists("gpt-7-nova"))
		require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, time.Millisecond)

		assert.True(t, srv.modelCatalogFor(t.Context()).lists("gpt-7-nova"))
		assert.Equal(t, int32(1), calls.Load(), "expired cache refresh must respect backoff after failure")
	})

	t.Run("cancelled caller does not start the backoff", func(t *testing.T) {
		stub := &stubModelsHTTPClient{statusCode: http.StatusBadGateway}
		srv := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}, httpClient: stub}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		assert.Nil(t, srv.modelCatalogFor(ctx))
		assert.True(t, srv.modelsLookupRetryAt.IsZero())
		assert.Equal(t, 0, stub.calls, "a cancelled caller must not start a fetch")
	})

	t.Run("failed prefetch starts lookup backoff", func(t *testing.T) {
		stub := &stubModelsHTTPClient{statusCode: http.StatusBadGateway}
		srv := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}, httpClient: stub}

		srv.PrefetchModels(t.Context())
		assert.Equal(t, 1, stub.calls)

		assert.Nil(t, srv.modelCatalogFor(t.Context()))
		assert.Equal(t, 1, stub.calls, "lookup inside prefetch backoff must not refetch")

		srv.invalidateModelsCache()
		assert.True(t, srv.modelsLookupRetryAt.IsZero())
	})

	t.Run("invalidation clears the stored lookup backoff", func(t *testing.T) {
		store := &memoryModelsCacheStore{}
		stub := &stubModelsHTTPClient{statusCode: http.StatusBadGateway}
		srv := &Server{
			logger:           zerolog.Nop(),
			credsFetcher:     stubCredentialsFetcher{},
			httpClient:       stub,
			modelsCacheStore: store,
		}
		srv.PrefetchModels(t.Context())
		require.NotEmpty(t, store.data, "a failed fetch must persist its backoff")

		srv.invalidateModelsCache()
		stub.statusCode = http.StatusOK
		stub.body = upstreamModelsFixture

		assert.True(t, srv.modelCatalogFor(t.Context()).lists("gpt-5.5"))
		assert.Equal(t, 2, stub.calls, "credentials changes must refetch instead of reloading the stored backoff")
	})

	t.Run("external cache store scopes by version and account and persists backoff", func(t *testing.T) {
		const version = "0.166.0"
		t.Setenv("CODEX_CLIENT_VERSION", version)
		store := &memoryModelsCacheStore{}
		newServer := func(client HTTPClient) *Server {
			srv := New(zerolog.Nop(), stubCredentialsFetcher{}, WithModelsCache(store))
			srv.httpClient = client
			return srv
		}
		encoded, err := json.Marshal(cachedModelsSnapshot{
			ClientVersion: version,
			AccountID:     "test-account",
			Models:        []upstreamModel{{Slug: "gpt-7-cached", Visibility: "list"}},
		})
		require.NoError(t, err)
		store.data = encoded

		stub := &stubModelsHTTPClient{body: upstreamModelsFixture}
		assert.True(t, newServer(stub).modelCatalogFor(t.Context()).lists("gpt-7-cached"))
		assert.Equal(t, 0, stub.calls, "matching stored snapshot must skip upstream fetch")
		assert.Equal(t, 1, store.loads)

		// A snapshot for a different account must be ignored and overwritten.
		otherAcct, err := json.Marshal(cachedModelsSnapshot{
			ClientVersion: version,
			AccountID:     "acct-other",
			Models:        []upstreamModel{{Slug: "gpt-other", Visibility: "list"}},
		})
		require.NoError(t, err)
		store.data = otherAcct

		catalog := newServer(stub).modelCatalogFor(t.Context())
		assert.True(t, catalog.lists("gpt-5.5"))
		assert.False(t, catalog.lists("gpt-other"))
		assert.Equal(t, 1, stub.calls)
		assert.Equal(t, 1, store.saves)
		assert.Equal(t, upstreamModelsTTL, store.ttl)
		var saved cachedModelsSnapshot
		require.NoError(t, json.Unmarshal(store.data, &saved))
		assert.Equal(t, version, saved.ClientVersion)

		t.Setenv("CODEX_CLIENT_VERSION", "0.169.0")
		assert.True(t, newServer(stub).modelCatalogFor(t.Context()).lists("gpt-5.5"))
		assert.Equal(t, 2, stub.calls, "a version change must refetch even for the same account")

		// A failed cold lookup persists backoff so subsequent stateless instances skip refetching.
		store.data = nil
		failingStub := &stubModelsHTTPClient{statusCode: http.StatusBadGateway}
		assert.Nil(t, newServer(failingStub).modelCatalogFor(t.Context()))
		assert.Equal(t, 1, failingStub.calls)
		assert.Nil(t, newServer(failingStub).modelCatalogFor(t.Context()))
		assert.Equal(t, 1, failingStub.calls, "stored backoff must prevent cold-instance refetch")
	})
}

type atomicFailingModelsClient struct {
	calls *atomic.Int32
}

func (c *atomicFailingModelsClient) Do(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader("upstream down")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// blockingModelsHTTPClient holds every models request until release is closed.
type blockingModelsHTTPClient struct {
	release chan struct{}
	calls   atomic.Int32
}

func (c *blockingModelsHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	<-c.release
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(upstreamModelsFixture)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestFetchUpstreamModelsSharesOneFetch(t *testing.T) {
	stub := &blockingModelsHTTPClient{release: make(chan struct{})}
	srv := &Server{logger: zerolog.Nop(), credsFetcher: stubCredentialsFetcher{}, httpClient: stub}

	cancelled, cancel := context.WithCancel(t.Context())
	cancelledErr := make(chan error, 1)
	go func() {
		_, err := srv.fetchUpstreamModels(cancelled)
		cancelledErr <- err
	}()
	require.Eventually(t, func() bool { return stub.calls.Load() == 1 }, time.Second, time.Millisecond)

	const waiters = 3
	results := make(chan []upstreamModel, waiters)
	for range waiters {
		go func() {
			models, err := srv.fetchUpstreamModels(t.Context())
			assert.NoError(t, err)
			results <- models
		}()
	}

	cancel()
	assert.ErrorIs(t, <-cancelledErr, context.Canceled)
	close(stub.release)

	for range waiters {
		assert.Len(t, <-results, 2)
	}
	assert.Equal(t, int32(1), stub.calls.Load())
	assert.Len(t, srv.modelsCache, 2, "the fetch must outlive the caller that started it")
}
