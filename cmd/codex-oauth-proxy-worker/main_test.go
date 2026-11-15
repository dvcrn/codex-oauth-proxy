//go:build js && wasm

package main

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"syscall/js"
	"testing"

	"github.com/syumai/workers"
)

func awaitPromise(t *testing.T, promise js.Value) js.Value {
	t.Helper()
	resCh := make(chan js.Value, 1)
	errCh := make(chan string, 1)
	var thenFn, catchFn js.Func
	thenFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer thenFn.Release()
		resCh <- args[0]
		return nil
	})
	catchFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer catchFn.Release()
		errCh <- args[0].Call("toString").String()
		return nil
	})
	promise.Call("then", thenFn).Call("catch", catchFn)
	select {
	case v := <-resCh:
		return v
	case errMsg := <-errCh:
		t.Fatalf("promise rejected: %s", errMsg)
		return js.Undefined()
	}
}

func TestKeepAliveHandlerReusesWasmAcrossRequests(t *testing.T) {
	var count atomic.Int32
	workers.ServeNonBlock(keepAliveHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ok-%d", n)
	})))

	handleRequest := js.Global().Get("context").Get("binding").Get("handleRequest")
	requestClass := js.Global().Get("Request")

	for i := 1; i <= 3; i++ {
		req := requestClass.New("https://example.com/health")
		resp := awaitPromise(t, handleRequest.Invoke(req))
		if status := resp.Get("status").Int(); status != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, status)
		}
		text := awaitPromise(t, resp.Call("text")).String()
		if want := fmt.Sprintf("ok-%d", i); text != want {
			t.Fatalf("request %d body = %q, want %q", i, text, want)
		}
	}

	select {
	case <-workers.Done():
		t.Fatal("workers.Done() closed after serving requests; instance would exit")
	default:
	}
}
