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

# Build and push Docker image to GitHub Container Registry
docker-build:
	#!/usr/bin/env bash
	# Use multiarch builder if available
	if docker buildx ls | grep -q "^multiarch "; then
		docker buildx use multiarch
	fi

	GITHUB_TOKEN=$(chainenv get CLAUDE_CODE_PROXY_GITHUB_REGISTRY_TOKEN) \
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--secret id=github_token,env=GITHUB_TOKEN \
		-t ghcr.io/dvcrn/codex-oauth-proxy:latest \
		. --push

# Build for Cloudflare Workers
build-worker:
	go run github.com/syumai/workers/cmd/workers-assets-gen -mode=go
	GOOS=js GOARCH=wasm go build -o ./build/app.wasm cmd/codex-oauth-proxy-worker/main.go

# Show help
help:
	@just --list
