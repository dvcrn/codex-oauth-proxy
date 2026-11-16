package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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
		store := &memoryModelsCacheStore{}
		encoded, err := json.Marshal(cachedModelsSnapshot{
			ClientVersion: codexClientVersion,
			AccountID:     "test-account",
			Models:        []upstreamModel{{Slug: "gpt-7-cached", Visibility: "list"}},
		})
		require.NoError(t, err)
		store.data = encoded

		stub := &stubModelsHTTPClient{body: upstreamModelsFixture}
		srv := &Server{
			logger:           zerolog.Nop(),
			credsFetcher:     stubCredentialsFetcher{},
			httpClient:       stub,
			modelsCacheStore: store,
		}

		assert.True(t, srv.modelCatalogFor(t.Context()).lists("gpt-7-cached"))
		assert.Equal(t, 0, stub.calls, "matching stored snapshot must skip upstream fetch")
		assert.Equal(t, 1, store.loads)

		// A snapshot for a different account must be ignored and overwritten.
		otherAcct, err := json.Marshal(cachedModelsSnapshot{
			ClientVersion: codexClientVersion,
			AccountID:     "acct-other",
			Models:        []upstreamModel{{Slug: "gpt-other", Visibility: "list"}},
		})
		require.NoError(t, err)
		store.data = otherAcct
		srv2 := &Server{
			logger:           zerolog.Nop(),
			credsFetcher:     stubCredentialsFetcher{},
			httpClient:       stub,
			modelsCacheStore: store,
		}

		catalog := srv2.modelCatalogFor(t.Context())
		assert.True(t, catalog.lists("gpt-5.5"))
		assert.False(t, catalog.lists("gpt-other"))
		assert.Equal(t, 1, stub.calls)
		assert.Equal(t, 1, store.saves)
		assert.Equal(t, upstreamModelsTTL, store.ttl)

		// A failed cold lookup persists backoff so subsequent stateless instances skip refetching.
		store.data = nil
		failingStub := &stubModelsHTTPClient{statusCode: http.StatusBadGateway}
		srv3 := &Server{
			logger:           zerolog.Nop(),
			credsFetcher:     stubCredentialsFetcher{},
			httpClient:       failingStub,
			modelsCacheStore: store,
		}
		assert.Nil(t, srv3.modelCatalogFor(t.Context()))
		assert.Equal(t, 1, failingStub.calls)

		srv4 := &Server{
			logger:           zerolog.Nop(),
			credsFetcher:     stubCredentialsFetcher{},
			httpClient:       failingStub,
			modelsCacheStore: store,
		}
		assert.Nil(t, srv4.modelCatalogFor(t.Context()))
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
