- Repo: dvcrn/codex-oauth-proxy

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

This is a Go proxy server that translates OpenAI-compatible chat completions requests into ChatGPT Codex's Responses API format. It accepts requests in the OpenAI Chat Completions / Responses format and forwards them to the upstream ChatGPT Codex backend (`https://chatgpt.com/backend-api/codex/responses`) with appropriate transformations, using Codex CLI OAuth credentials and headers.

The server uses zerolog for structured JSON logging with zero allocations and high performance.

## Common Development Commands

### Building and Running

```bash
# Build the binary
mise run build

# Run tests
mise run test
```

### Releasing

See [RELEASE.md](RELEASE.md) for full step-by-step instructions on cutting and publishing releases across GitHub Releases (GoReleaser), npm, and Docker.

## Architecture

### Package Structure

The codebase follows Go conventions with internal packages:

- `cmd/codex-oauth-proxy/`: Entry point - minimal main function that starts the server
- `internal/server/`: Core server implementation
  - `server.go`: HTTP server setup, routing, and request handling
  - `types.go`: Data structures and JSON marshaling logic
  - `transform.go`: Message and system prompt transformation functions
  - `transform_test.go`: Unit tests for transformation logic
  - `prompts.go`: Codex CLI system prompts and identity messages

### Core Components

- **Server Package** (`internal/server/`): Handles all server logic
  - HTTP server with structured logging middleware using zerolog
  - `/v1/chat/completions` and `/v1/responses` endpoints process incoming requests
  - `/v1/models` lists available models; `/health` for health checks (returns `{"status": "ok"}`)
  - `/admin/credentials` and `/admin/credentials/status` manage stored OAuth credentials
  - `/mcp` exposes an MCP server (stateless streamable HTTP) with the `ask_codex` and `ask_codex_models` tools
  - Transforms the OpenAI-compatible request into the ChatGPT Codex Responses API format
  - Injects the Codex CLI system prompt / instructions
  - Rewrites the upstream SSE stream back into the client-facing format

### API Endpoints

The proxy provides two primary modes of operation via distinct endpoints:

1. **`/v1/responses` (Native Proxy)**
    - This is the primary endpoint for new integrations.
    - It accepts requests in the OpenAI Chat Completions format and forwards them to the upstream `/v1/responses` endpoint after applying necessary transformations.

2. **`/v1/completions` (Legacy Compatibility)**
    - This endpoint provides backward compatibility for clients that still use the legacy `/v1/completions` API.
    - It accepts a legacy request, internally rewrites it into the modern `/v1/responses` format, and then forwards it upstream. This allows older clients to benefit from the new backend without modification.

### Key Transformations

1. **System Prompt Transformation** (`transformSystemPrompt`):

   - Prepends the Codex CLI identity / instructions prompt
   - Replaces competitor names with "Codex"

2. **Message Processing** (`transformMessages` / `buildCodexInputMessages`):

   - Replaces competitor names in message content
   - Maps OpenAI-style messages and tools into the Codex input format

3. **Request Modifications** (`buildCodexRequestBody`):
   - Normalizes the requested model and reasoning effort/summary settings
   - Forces streaming mode and derives a prompt cache key
   - Configures Codex-specific headers (Codex CLI user-agent, ChatGPT account ID, beta feature flags)

### Cross-Provider Session Resumption

- Preserve encrypted reasoning items so native ChatGPT reasoning continuity is not lost.
- Treat provider-specific encrypted reasoning as non-portable and pass upstream compatibility errors through to the client.
- Preserve response item IDs up to the upstream 64-character limit; remove longer foreign IDs before forwarding.

### Environment Requirements

The server reads these environment variables:

- `PORT`: TCP port to listen on (defaults to `9879`)
- `ADMIN_API_KEY`: required by the admin middleware to authorize requests to the proxy and `/admin` endpoints

Upstream authentication uses Codex CLI OAuth credentials, selected via the `--creds-store` flag (`auto|xdg|legacy|keychain|env`); see the README for details. The `env` store mode (`--creds-store=env`) is a legacy path that still reads `ANTHROPIC_API_KEY` and `CLAUDE_USER_ID` — vestigial variable names left over from this project's origins.

### Docker Deployment

The service includes a multi-stage Dockerfile for containerized deployment:

- Builds with Go 1.23.4
- Runs as non-root user (appuser)
- Exposes port 8080
- Includes GitHub Container Registry push configuration

After each code change or task, always run:

```
just build
```

This automatically formats the code with goimports and then builds the Go server, ensuring proper import organization and catching any compile-time errors early. Do not skip this step after any modification.

## Logging

The server uses [zerolog](https://github.com/rs/zerolog) for structured logging:

### Configuration

- UNIX timestamp format for performance (`zerolog.TimeFormatUnix`)
- JSON output to stderr for structured log parsing
- Each server instance maintains its own logger with timestamps

### Log Levels

- `Info`: Request/response lifecycle, server startup
- `Warn`: Unhandled routes, non-critical issues
- `Error`: Request processing errors, API communication failures  
- `Debug`: Detailed request dumps and transformation data (when enabled)
- `Fatal`: Critical startup failures (missing environment variables)

### Structured Fields

Common fields used throughout the application:

- `method`: HTTP method
- `uri`: Request URI
- `remote_addr`: Client IP address
- `user_agent`: Client user agent
- `duration`: Request processing time
- `error`: Error details using `.Err(err)`

### Example Log Output

```json
{"level":"info","time":1516134303,"method":"POST","uri":"/v1/responses","remote_addr":"127.0.0.1:54321","user_agent":"curl/7.68.0","message":"Incoming request"}
{"level":"info","time":1516134304,"method":"POST","uri":"/v1/responses","duration":1023,"message":"Finished request"}
```

## Code Organization Principles

When adding new functionality:

1. Keep the main function minimal - it should only initialize and start the server
2. Use internal packages for implementation details that shouldn't be imported by external packages
3. Separate concerns: types in `types.go`, transformations in `transform.go`, handlers in `server.go`
4. Add new endpoints by updating `setupRoutes()` in `server.go`

## Go Conventions

- Follow Go naming conventions for all code
- Put tests in `*_test.go` files alongside the code they test, not in separate files

## Cloudflare Workers Support

This server supports deployment to Cloudflare Workers using the github.com/syumai/workers package. The Workers build uses WebAssembly (WASM) and has specific requirements:

### Critical Implementation Patterns

1. **Environment Variable Access** (`internal/env/`):
   - Regular Go: Uses `os.Getenv()`
   - Workers: Uses `cloudflare.Getenv()` from syumai/workers
   - Always use the `internal/env` package for environment variables to ensure cross-platform compatibility

2. **HTTP Client Requirements** (`internal/server/client*.go`):
   - Regular Go: Uses standard `http.Client`
   - Workers: Must use `fetch.Client` from syumai/workers to avoid "Illegal invocation" errors
   - The server defines an `HTTPClient` interface to abstract platform differences
   - Platform-specific implementations are selected via build tags

### Build Tags

Files specific to Cloudflare Workers use the build tags:

```go
//go:build js && wasm
```

Files for regular environments use:

```go
//go:build !js || !wasm
```

### Project Structure for Workers

- `cmd/codex-oauth-proxy-worker/`: Workers-specific entry point
- `internal/app/`: Shared application logic between regular and Workers builds
- Platform-specific files use `_workers.go` suffix for Workers implementations

## Post-Task Reflection

After completing tasks where the user has provided feedback about workflow, tools to use, architecture decisions, or development practices, proactively offer to reflect on the current guidelines in this file. When accepted:

1. Analyze the feedback and learnings from the completed task
2. Suggest specific improvements to CLAUDE.md that would help future development
3. Upon approval, update this file with the new guidelines
4. Ensure updates are actionable and specific to this project's needs
