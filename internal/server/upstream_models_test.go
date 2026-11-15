package server

import (
	"context"
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

		require.Eventually(t, func() bool { return stub.calls.Load() == 1 }, time.Second, time.Millisecond)
		close(stub.release)
		require.Eventually(t, func() bool {
			srv.modelsCacheMu.Lock()
			defer srv.modelsCacheMu.Unlock()
			return len(srv.modelsCache) == 2
		}, time.Second, time.Millisecond)
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
