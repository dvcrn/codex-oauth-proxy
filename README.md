# Codex OAuth Proxy

Codex OAuth Proxy exposes the models included with your ChatGPT Codex access through OpenAI-compatible HTTP endpoints. It handles Codex OAuth credentials, forwards requests to the ChatGPT Codex backend, and translates streaming responses for clients such as OpenCode, editors, and agent tools.

```text
  ┌───────────────┐          ┌───────────────────┐          ┌───────────────────────┐
  │ External Tool │          │    Local Proxy    │          │    Codex Backend      │
  │ (OpenCode/etc)│          │                   │          │ (ChatGPT Responses)   │
  └───────┬───────┘          └─────────┬─────────┘          └───────────┬───────────┘
          │                            │                                │
          │  OpenAI-compatible request │    Codex API request           │
          │ ─────────────────────────▶ │ ─────────────────────────────▶ │
          │                            │    OAuth access token          │
          │                            │                                │
          │  OpenAI-compatible response│    Codex API response          │
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

The `/mcp` endpoint uses streamable HTTP and the same admin key as the API endpoints:

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

It exposes two tools:

- `ask_codex(model, prompt)` sends one self-contained prompt to a model.
- `ask_codex_models()` lists the model IDs and reasoning levels available to the account.

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
