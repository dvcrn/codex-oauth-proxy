# Codex OAuth Proxy

Codex OAuth Proxy exposes the models included with your ChatGPT Codex access through OpenAI-compatible HTTP endpoints. It handles Codex OAuth credentials, forwards requests to the ChatGPT Codex backend, and translates streaming responses for clients such as OpenCode, editors, and agent tools.

It also provides an MCP server at `/mcp`. Agents can use it to discover the Codex models available to your account and send one-shot prompts without configuring an OpenAI API client.

```text
  ┌───────────────┐          ┌───────────────────┐          ┌───────────────────────┐
  │ External Tool │          │    Local Proxy    │          │    Codex Backend      │
  │ (OpenCode/etc)│          │                   │          │ (ChatGPT Responses)   │
  └───────┬───────┘          └─────────┬─────────┘          └───────────┬───────────┘
          │                            │                                │
          │  API or MCP request        │    Codex API request           │
          │ ─────────────────────────▶ │ ─────────────────────────────▶ │
          │                            │    OAuth access token          │
          │                            │                                │
          │  API or MCP response       │    Codex API response          │
          │ ◀───────────────────────── │ ◀───────────────────────────── │
          │    JSON or SSE stream      │                                │
          │                            │                                │
          ▼                            ▼                                ▼
```

Use it when a client supports the OpenAI Chat Completions or Responses API but cannot sign in to Codex directly.

## Quick start

You need a ChatGPT account with Codex access and an existing Codex CLI login.

Install the proxy with npm:

```bash
npm install -g codex-oauth-proxy
```

Other installation options:

```bash
# mise
mise use -g go:github.com/dvcrn/codex-oauth-proxy/cmd/codex-oauth-proxy@latest

# Go
go install github.com/dvcrn/codex-oauth-proxy/cmd/codex-oauth-proxy@latest
```

Sign in with the Codex CLI if you have not already, then start the proxy with a key of your choice:

```bash
codex login
ADMIN_API_KEY="replace-with-a-long-random-value" codex-oauth-proxy
```

The server listens on `http://localhost:9879` by default. Send the admin key as the bearer token:

```bash
curl http://localhost:9879/v1/chat/completions \
  -H "Authorization: Bearer replace-with-your-admin-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.5",
    "messages": [{"role": "user", "content": "Explain this repository in one sentence."}],
    "stream": false
  }'
```

Point OpenAI-compatible clients at `http://localhost:9879/v1` and use the same admin key as their API key.

## Authentication

There are two separate credentials:

- Codex OAuth credentials authenticate the proxy with the upstream ChatGPT Codex service. The default `auto` store imports an existing Codex CLI login into `$XDG_CONFIG_HOME/codex-oauth-proxy/auth.json` (or `~/.config/codex-oauth-proxy/auth.json` when `XDG_CONFIG_HOME` is unset), keeps its token chain separate from the CLI, and refreshes it when needed.
- `ADMIN_API_KEY` authenticates clients with your proxy. Protected endpoints accept either `Authorization: Bearer <key>` or `X-API-Key: <key>`.

The default credential mode should work for most local installations. Other modes are available when you need an explicit source:

```bash
codex-oauth-proxy --creds-store=xdg
codex-oauth-proxy --creds-store=xdg --creds-path=/path/to/auth.json
codex-oauth-proxy --creds-store=legacy
```

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI-compatible chat completions |
| `POST /v1/responses` | OpenAI-compatible Responses API |
| `GET /v1/models` | Models available to the signed-in account, including reasoning-effort variants |
| `POST /mcp` | Stateless MCP server with `ask_codex` and `ask_codex_models` |
| `GET /health` | Health check |

The model list comes from the Codex backend. Query `/v1/models` instead of hard-coding model IDs. Clients that cannot set reasoning effort separately can append a suffix such as `-low`, `-medium`, `-high`, `-xhigh`, or `-max` when supported by that model.

## MCP clients

The `/mcp` endpoint lets MCP clients use your Codex account through two tools. It uses stateless streamable HTTP with JSON responses, so the server keeps no conversation or session state between calls.

MCP requests use the proxy's Codex OAuth credentials upstream and the same `ADMIN_API_KEY` as the protected API endpoints. No separate OpenAI API key is required.

MCP configuration varies by client. Configure a streamable HTTP server with:

| Setting | Value |
| --- | --- |
| URL | `http://localhost:9879/mcp` |
| Header | `Authorization: Bearer replace-with-your-admin-key` |

For clients that use an `mcpServers` JSON object:

```json
{
  "mcpServers": {
    "ask-codex": {
      "type": "http",
      "url": "http://localhost:9879/mcp",
      "headers": {
        "Authorization": "Bearer replace-with-your-admin-key"
      }
    }
  }
}
```

The client discovers these tools after it connects:

| Tool | Input | Result |
| --- | --- | --- |
| `ask_codex_models` | None | Available model IDs, display names, and supported reasoning efforts |
| `ask_codex` | `model`, `prompt` | The requested model, model that served the request, and response text |

Call `ask_codex_models` first when the model ID is not already known. Reasoning effort can be selected with a model suffix such as `gpt-5.5-high` when that effort appears in the model listing.

`ask_codex` is one-shot. It does not retain conversation history, so `prompt` must include all context needed for that call. The returned `model` may differ from `requested_model` when the proxy normalizes a model ID.

## Configuration

| Setting | Default | Description |
| --- | --- | --- |
| `ADMIN_API_KEY` | required | Key used to protect generation, admin, and MCP requests |
| `PORT` | `9879` | Listening port |
| `ENV` | `development` | Use `production` for JSON logs |
| `DISABLE_HEALTH_LOGS` | `false` | Disable request logs for `/health` |
| `--creds-store` | `auto` | Credential source for normal Codex CLI logins: `auto`, `xdg`, or `legacy` |
| `--creds-path` | platform default | Credential file for `xdg` or `legacy` mode |

## Development

```bash
mise run test
mise run build
```
