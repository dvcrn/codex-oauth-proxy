package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mutableCredentialsFetcher struct {
	mu        sync.Mutex
	token     string
	accountID string
}

func (f *mutableCredentialsFetcher) GetCredentials() (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token, f.accountID, nil
}

func (f *mutableCredentialsFetcher) RefreshCredentials() error { return nil }

type recordingModelsClient struct {
	mu         sync.Mutex
	calls      int
	statusCode int
	body       string
	err        error
	request    *http.Request
	started    chan struct{}
	release    chan struct{}
}

func (c *recordingModelsClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls++
	c.request = req.Clone(req.Context())
	started := c.started
	release := c.release
	statusCode := c.statusCode
	body := c.body
	err := c.err
	c.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return nil, err
	}
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	return &http.Response{StatusCode: statusCode, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func newModelTestServer(fetcher *mutableCredentialsFetcher, client HTTPClient) *Server {
	server := New(zerolog.Nop(), fetcher)
	server.httpClient = client
	return server
}

func TestConfiguredCodexClientIdentity(t *testing.T) {
	t.Setenv("CODEX_CLIENT_VERSION", "1.2.3")
	identity := configuredCodexClientIdentity()
	assert.Equal(t, "1.2.3", identity.version)
	assert.Equal(t, codexOriginator, identity.originator)
	assert.Equal(t, "codex_cli_rs/1.2.3", identity.userAgent)

	t.Setenv("CODEX_CLIENT_VERSION", "1.2")
	assert.Equal(t, defaultCodexClientVersion, configuredCodexClientIdentity().version)
}

func TestFetchUpstreamModelsSendsIdentityAndNormalizesCatalog(t *testing.T) {
	fetcher := &mutableCredentialsFetcher{token: "Bearer test-token", accountID: "account-a"}
	client := &recordingModelsClient{body: `{"models":[
		{"slug":" new-model ","display_name":" New Model ","description":" Desc ","visibility":"list","supported_in_api":true,"default_reasoning_level":"HIGH","supported_reasoning_levels":[{"effort":"low"},{"effort":"ultra"},{"effort":"low"}]},
		{"slug":"new-model","visibility":"list"},
		{"slug":"hidden","visibility":"hide"},
		{"slug":"unsupported","visibility":"list","supported_in_api":false}
	]}`}
	server := newModelTestServer(fetcher, client)
	server.clientIdentity = codexClientIdentity{version: "1.2.3", originator: codexOriginator, userAgent: "codex_cli_rs/1.2.3"}

	models, err := server.fetchUpstreamModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "new-model", models[0].Slug)
	assert.Equal(t, "New Model", models[0].DisplayName)
	assert.Equal(t, "Desc", models[0].Description)
	assert.Equal(t, "high", models[0].DefaultReasoningLevel)
	assert.Equal(t, []string{"low"}, models[0].efforts())

	require.NotNil(t, client.request)
	assert.Equal(t, "1.2.3", client.request.URL.Query().Get("client_version"))
	assert.Equal(t, "Bearer test-token", client.request.Header.Get("Authorization"))
	assert.Equal(t, "account-a", client.request.Header.Get("Chatgpt-Account-Id"))
	assert.Equal(t, "1.2.3", client.request.Header.Get("Version"))
	assert.Equal(t, codexOriginator, client.request.Header.Get("Originator"))
	assert.Equal(t, "codex_cli_rs/1.2.3", client.request.Header.Get("User-Agent"))
	assert.Equal(t, "application/json", client.request.Header.Get("Accept"))
}

func TestFetchUpstreamModelsUsesScopedStaleFallback(t *testing.T) {
	fetcher := &mutableCredentialsFetcher{token: "token", accountID: "account-a"}
	client := &recordingModelsClient{statusCode: http.StatusServiceUnavailable, body: "temporary"}
	server := newModelTestServer(fetcher, client)
	key := modelCatalogKey{accountID: "account-a", version: server.clientIdentity.version, originator: server.clientIdentity.originator}
	server.modelsCache[key] = modelCatalogSnapshot{models: []upstreamModel{{Slug: "cached-model"}}, fetchedAt: time.Now().Add(-upstreamModelsTTL - time.Minute)}

	models, err := server.fetchUpstreamModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "cached-model", models[0].Slug)
	assert.Equal(t, 1, client.calls)

	client.statusCode = http.StatusForbidden
	_, err = server.fetchUpstreamModels(context.Background())
	var authErr *upstreamAuthError
	require.ErrorAs(t, err, &authErr)
}

func TestFetchUpstreamModelsDoesNotCrossAccountBoundary(t *testing.T) {
	fetcher := &mutableCredentialsFetcher{token: "token", accountID: "account-b"}
	client := &recordingModelsClient{body: `{"models":[{"slug":"account-b-model","visibility":"list"}]}`}
	server := newModelTestServer(fetcher, client)
	server.modelsCache[modelCatalogKey{accountID: "account-a", version: server.clientIdentity.version, originator: server.clientIdentity.originator}] = modelCatalogSnapshot{
		models: []upstreamModel{{Slug: "account-a-model"}}, fetchedAt: time.Now(),
	}

	models, err := server.fetchUpstreamModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "account-b-model", models[0].Slug)
}

func TestFetchUpstreamModelsSingleFlight(t *testing.T) {
	fetcher := &mutableCredentialsFetcher{token: "token", accountID: "account-a"}
	client := &recordingModelsClient{
		body:    `{"models":[{"slug":"model","visibility":"list"}]}`,
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	server := newModelTestServer(fetcher, client)

	errs := make(chan error, 2)
	go func() { _, err := server.fetchUpstreamModels(context.Background()); errs <- err }()
	<-client.started
	go func() { _, err := server.fetchUpstreamModels(context.Background()); errs <- err }()
	time.Sleep(10 * time.Millisecond)
	close(client.release)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	assert.Equal(t, 1, client.calls)
}

func TestFetchUpstreamModelsCallerCancellationDoesNotCancelSharedRefresh(t *testing.T) {
	fetcher := &mutableCredentialsFetcher{token: "token", accountID: "account-a"}
	client := &recordingModelsClient{
		body:    `{"models":[{"slug":"model","visibility":"list"}]}`,
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	server := newModelTestServer(fetcher, client)
	ctx, cancel := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() { _, err := server.fetchUpstreamModels(ctx); firstErr <- err }()
	<-client.started

	secondResult := make(chan error, 1)
	go func() { _, err := server.fetchUpstreamModels(context.Background()); secondResult <- err }()
	cancel()
	require.ErrorIs(t, <-firstErr, context.Canceled)
	close(client.release)
	require.NoError(t, <-secondResult)
	assert.Equal(t, 1, client.calls)
}

func TestFetchUpstreamModelsRejectsMalformedSuccessWithoutStaleFallback(t *testing.T) {
	fetcher := &mutableCredentialsFetcher{token: "token", accountID: "account-a"}
	client := &recordingModelsClient{body: `{`}
	server := newModelTestServer(fetcher, client)
	key := modelCatalogKey{accountID: "account-a", version: server.clientIdentity.version, originator: server.clientIdentity.originator}
	server.modelsCache[key] = modelCatalogSnapshot{models: []upstreamModel{{Slug: "cached-model"}}, fetchedAt: time.Now().Add(-upstreamModelsTTL - time.Minute)}

	_, err := server.fetchUpstreamModels(context.Background())
	require.Error(t, err)
	assert.False(t, errors.Is(err, context.Canceled))
	assert.Contains(t, err.Error(), "failed to parse models response")
}
