//go:build js && wasm

package main

import (
	"net/http"
	"reflect"

	"github.com/dvcrn/codex-oauth-proxy/internal/app"
	"github.com/dvcrn/codex-oauth-proxy/internal/auth"
	"github.com/dvcrn/codex-oauth-proxy/internal/credentials"
	"github.com/dvcrn/codex-oauth-proxy/internal/logger"
	"github.com/dvcrn/codex-oauth-proxy/internal/server"
	"github.com/syumai/workers"
)

func main() {
	log := logger.New()

	log.Info().Msg("📦 Using Cloudflare KV credentials fetcher with OAuth refresh")
	kvFetcher, err := credentials.NewCloudflareKVFetcher()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Cloudflare KV fetcher")
	}

	oauthFetcher := auth.NewOAuthFetcher(kvFetcher, &log)
	srv := app.NewServer(oauthFetcher, log, server.WithDeviceAuth(kvFetcher))

	workers.ServeNonBlock(keepAliveHandler(srv))
	workers.Ready()
	select {}
}

// keepAliveHandler strips syumai/workers's single-request appCloser wrapper
// from ResponseWriter.Reader before ReadyCh unblocks ToJSResponse, so closing a
// response body does not close the package-global doneCh and terminate the
// shared WASM instance.
func keepAliveHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detachResponseCloser(w)
		next.ServeHTTP(w, r)
	})
}

func detachResponseCloser(w http.ResponseWriter) {
	rv := reflect.ValueOf(w)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return
	}
	readerField := rv.Elem().FieldByName("Reader")
	if !readerField.IsValid() || !readerField.CanSet() || readerField.IsNil() {
		return
	}
	elem := readerField.Elem()
	if elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	if elem.Kind() != reflect.Struct {
		return
	}
	inner := elem.FieldByName("ReadCloser")
	if inner.IsValid() && inner.Type().AssignableTo(readerField.Type()) {
		readerField.Set(inner)
	}
}
