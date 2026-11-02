# justfile for codex-oauth-proxy

# Build the Go server (Codex proxy)
build: format
	go build -o codex-oauth-proxy ./cmd/codex-oauth-proxy

wrangler-dev: 
	bunx wrangler dev

# Run the Go server
run:
	go run ./cmd/codex-oauth-proxy

# Install the binary to GOPATH/bin
install:
	go install ./cmd/codex-oauth-proxy
	@echo "codex-oauth-proxy installed to GOPATH/bin"

# Uninstall the binary from GOPATH/bin
uninstall:
	rm -f $(go env GOPATH)/bin/codex-oauth-proxy
	@echo "codex-oauth-proxy uninstalled from GOPATH/bin"

# Format code with gofmt
fmt:
	gofmt -w .

# Format code with goimports (organizes imports and formats)
format:
	find . -name "*.go" -type f -exec go tool goimports -w {} +

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -f codex-oauth-proxy

# Build and load a local Docker image; GitHub Actions handles publishing
docker-build:
	docker buildx build --load --tag codex-oauth-proxy:local .

# Build for Cloudflare Workers
build-worker:
	go run github.com/syumai/workers/cmd/workers-assets-gen -mode=go
	GOOS=js GOARCH=wasm go build -o ./build/app.wasm cmd/codex-oauth-proxy-worker/main.go

# Show help
help:
	@just --list
