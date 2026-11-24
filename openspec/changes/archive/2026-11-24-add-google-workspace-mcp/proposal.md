# Add Google Workspace MCP servers

## Why

Nestor wants to connect to Google's official managed remote Workspace MCP servers for **Contacts** (`people.googleapis.com/mcp/v1`), **Calendar** (`calendarmcp.googleapis.com/mcp/v1`), **Docs** (`docsmcp.googleapis.com/mcp/v1`), **Drive** (`drivemcp.googleapis.com/mcp/v1`), and **Workspace Universal Search** (`workspacemcp.googleapis.com/mcp/v1`), and replace the planned native Go `add-calendar` integration in `docs/vision.md` with Google Calendar MCP.

Today Nestor's `per_user` MCP support (`internal/mcp/user.go`) only injects static Bearer tokens into `remote: false` local containers. Google Workspace MCP servers are remote HTTPS endpoints that require per-user OAuth 2.0 access tokens refreshed from encrypted per-user refresh tokens (as Google access tokens expire hourly) using a host-wide Google OAuth client.

## What changes

- **Per-user OAuth 2.0 MCP delivery (`internal/mcp/config`, `internal/mcp`):**
  - Add `credential_delivery: oauth2` along with `oauth_token_url`, `oauth_client_id_env`, and `oauth_client_secret_env` on `config.Server`. Validate that `oauth2` uses `credentials: per_user`, `transport: http`, non-empty `credential_key`, no `{user}` URL placeholder, valid env var names (`^[A-Z_][A-Z0-9_]*$`), and `https://` for both `url` and `oauth_token_url` when `remote: true`. Keep `credential_delivery: header` restricted to `remote: false`.
  - Extend `internal/mcp.UserConnector` (using `golang.org/x/oauth2`, promoted from indirect to direct in `go.mod`) to decrypt per-user OAuth refresh tokens (`{"refresh_token":"..."}`) from `user_credentials` and supply each user an isolated `oauth2.ReuseTokenSource` + `http.RoundTripper` that transparently refreshes short-lived access tokens without leaking secrets into logs or errors.
- **CLI onboarding (`cmd/nestor/credentials.go`, `cmd/nestor/google_auth.go`):**
  - Add `nestor credentials google-auth <user-id>` supporting PKCE (`S256`) and random `state`, `--manual` mode by default (headless/Docker-friendly with zero published ports, prompting for the redirected URL on stdin), optional `--listen 127.0.0.1:<port>` loopback listener, `--key` (default `google_oauth`), and `--scopes` (defaulting to Contacts, Calendar, Docs, and Drive scopes), storing the encrypted refresh token in `user_credentials`.
- **Startup wiring (`cmd/nestor/mcpstart.go`):**
  - Wire `credential_delivery: oauth2`, disabling only OAuth servers with a secret-free warning if either OAuth client env var is unset at startup (without requiring `CREDENTIALS_KEY` when OAuth env vars are absent; failing fast if OAuth env vars are set while `CREDENTIALS_KEY` is missing).
- **Reviewed Google Workspace config, Compose, and roadmap (`config/mcp.yaml`, `deploy/docker-compose.yml`, `deploy/.env.example`, `docs/vision.md`, `openspec/config.yaml`):**
  - Add reviewed entries for `google-contacts`, `google-calendar`, `google-docs`, `google-drive`, and `google-workspace-search` in `config/mcp.yaml` (`remote: true`, `health_interval: 5m`, shared `credential_key: google_oauth`, read tools at `risk: read`, all write tools at `risk: critical`).
  - Pass `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` only to `nestor` in `deploy/docker-compose.yml` and document them as optional in `deploy/.env.example`.
  - Update `docs/vision.md` and `openspec/config.yaml` so Calendar MCP supersedes the planned native `add-calendar` change.

## Capabilities

- `mcp-integration` (modified)

## Constitution principles touched

- **1. Approval is enforced in code:** Every Calendar, Docs, and Drive write tool (`create_event`, `update_event`, `delete_event`, `respond_to_event`, `update_doc`, `create_file`, `copy_file`) is explicitly configured `risk: critical` in `config/mcp.yaml`; read tools are `risk: read`; server annotations continue to be ignored.
- **2. Untrusted content:** Calendar invites, Docs bodies, Drive files, contact notes, and search snippets enter only as capped tool-role messages; tool descriptions and schemas require manual admin pinning via `nestor mcp approve`.
- **4. Personal data stays on the host:** All five servers are explicitly marked `remote: true`; their data already resides in the user's Google account, and only tool calls for that user reach Google over HTTPS.
- **5. Everything is observable:** Every Workspace MCP tool call is recorded in `tool_calls` with inputs, outputs/truncation, latency, and status.
- **6. User-scoped by design:** Each user's refresh token is encrypted and bound to `(user_id, credential_key)` via AES-256-GCM; each user gets an isolated `oauth2.TokenSource` and MCP session; users without a stored `google_oauth` credential are never offered Workspace tools.
- **7. Secrets never live in the repo:** `config/mcp.yaml` names only env var names (`GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET`) and the DB credential key (`google_oauth`). Client credentials live in host env scoped only to `nestor` in Compose; refresh tokens are encrypted at rest; access/refresh tokens and client secrets are never logged or echoed in errors or CLI output.
- **8. Small, reversible steps:** Builds cleanly on `add-mcp-servers` and `add-google-maps-mcp` with zero database migrations and zero new transitive dependencies.

## Non-goals

- Gmail MCP integration or replacing `add-email` (the owner uses Proton Mail; Gmail remains covered by `add-email`).
- Google Sheets, Slides, Chat, or Classroom MCP servers (can be added declaratively in `config/mcp.yaml` later using the same `oauth2` delivery).
- Web UI or Telegram-initiated OAuth callback server (onboarding uses the `nestor credentials google-auth` CLI; Nestor publishes no inbound ports).
- Organization-only `search_directory_people` tool (personal `@gmail.com` target; can be allowlisted later for Workspace users) or binary `download_file_content`.
- Allowing `credential_delivery: header` (static tokens) with `remote: true`.
- Database schema migrations (reuses existing `user_credentials` and `mcp_tool_pins` tables).
- Fixing `nestor-c9s` or `nestor-o5b` (tracked separately as standalone bug fixes).

## Impact

- Packages/files modified: `go.mod`, `internal/mcp/config`, `internal/mcp`, `cmd/nestor`, `config/mcp.yaml`, `deploy/docker-compose.yml`, `deploy/.env.example`, `deploy/compose_test.go`, `docs/vision.md`, `openspec/config.yaml`.
- New optional environment variables: `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET`.
- Zero database migrations; promotes existing indirect dependency `golang.org/x/oauth2` v0.35.0 to direct.
