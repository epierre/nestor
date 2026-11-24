# Add Google Maps MCP server

## Why

Nestor needs geospatial grounding (finding places, checking local weather, computing travel routes) and wants to integrate Google's official managed remote MCP servers starting with **Google Maps** (`https://mapstools.googleapis.com/mcp`, Maps Experience agent tools / Maps Grounding Lite).

Today, adding `google-maps` via `config/mcp.yaml` is blocked by three gaps in the MCP integration:

1. `cmd/nestor/mcpstart.go` refuses `credentials: shared` at startup (part of `nestor-910`), even though `internal/mcp/config` already accepts the value without specifying where the secret comes from.
2. `internal/mcp` only sends `Authorization: Bearer <token>` headers for per-user tokens, whereas Google Maps MCP authenticates with a Google Cloud API key in `X-Goog-Api-Key` (and has no per-user private data).
3. `internal/mcp/health.go` polls every MCP server every 30s (`defaultHealthInterval`) with no per-server override, which is too frequent for remote, metered or rate-limited HTTP endpoints.

## What changes

- **Shared-credential HTTP MCP servers (`internal/mcp/config`, `internal/mcp`, `cmd/nestor/mcpstart.go`):**
  - Add `credential_env` (the host environment variable name holding the secret) and optional `credential_header` (custom HTTP header name, defaulting to `Authorization: Bearer <secret>`) to `config.Server`.
  - Validate that `credentials: shared` uses `transport: http`, specifies a valid `credential_env` (`^[A-Z_][A-Z0-9_]*$`), leaves `credential_key` and `credential_delivery` unset, uses a valid HTTP token name if `credential_header` is set, and uses an `https://` URL when `remote: true`.
  - Remove the `credentials == "shared"` refusal in `supported()`, read the env var at startup, dial using a static-header Streamable HTTP transport (`DialHTTPWithHeader`), and disable only that server with a secret-free warning if the env var is unset or empty.
- **Configurable health interval (`internal/mcp/config`, `cmd/nestor/mcpstart.go`):**
  - Add optional `health_interval` per server in `mcp.yaml` (default `30s` when omitted/zero; negative durations rejected at startup) and wire it to `mcp.Supervisor.Interval`.
- **Reviewed Google Maps configuration (`config/mcp.yaml`, `deploy/docker-compose.yml`, `deploy/.env.example`):**
  - Add `google-maps` to `config/mcp.yaml` pointing at `https://mapstools.googleapis.com/mcp` with `remote: true`, `credentials: shared`, `credential_env: GOOGLE_MAPS_API_KEY`, `credential_header: X-Goog-Api-Key`, `users: ["*"]`, `timeout: 15s`, `health_interval: 5m`, `max_result_bytes: 65536`, and allowlisted read-only tools `search_places`, `lookup_weather`, and `compute_routes`.
  - Pass `GOOGLE_MAPS_API_KEY` only to the `nestor` service in `deploy/docker-compose.yml` and document it in `deploy/.env.example`.

## Capabilities

- `mcp-integration` (modified)

## Constitution principles touched

- **1. Approval is enforced in code:** All allowlisted Google Maps tools are explicitly declared `risk: read` in `config/mcp.yaml`; server-provided annotations continue to be ignored, and any future write tool would require reviewed config.
- **2. Untrusted content:** Google Maps tool descriptions and input schemas require explicit administrator approval and pinning via `nestor mcp approve`; tool outputs enter the conversation only as capped untrusted tool-role messages.
- **4. Personal data stays on the host:** `remote: true` makes egress explicit so location queries sent to Google Maps Platform are reviewed in `mcp.yaml`. No user credentials or unrelated personal data are transmitted.
- **5. Everything is observable:** Every Google Maps MCP tool call is recorded in `tool_calls` with inputs, outputs/truncation state, latency, and status.
- **6. User-scoped by design:** Maps grounding carries no per-user private data (`users: ["*"]`) and never mixes user data across turns or contexts.
- **7. Secrets never live in the repo:** `config/mcp.yaml` names only the environment variable (`GOOGLE_MAPS_API_KEY`), never the key itself. The secret lives in host env (`deploy/.env`), is passed only to `nestor` in Compose, is permitted over the wire only via `https://` for `remote: true`, and is never logged or echoed in errors.
- **8. Small, reversible steps:** Delivers shared-header remote MCP + Google Maps cleanly without OAuth complexity, validating Nestor's Streamable HTTP client, pin checking, and health supervisor against a live Google MCP endpoint before tackling per-user OAuth Workspace MCP servers (Contacts, Docs, Calendar, Drive, Search).

## Non-goals

- Per-user OAuth 2.0 refresh-token flows and Google Workspace MCP servers (Contacts, Docs, Calendar, Drive, Search — planned as the follow-up change).
- Allowing `remote: true` with `per_user` + `header` credential delivery.
- `stdio` transport or `container` credential delivery (still tracked under `nestor-910`).
- Automatic approval of newly seen MCP tool descriptions (remains manual via `nestor mcp approve`).
- Cost/quota metering of Google Maps Platform API calls inside Nestor (monitored in Google Cloud Platform).

## Impact

- Packages/files modified: `internal/mcp/config`, `internal/mcp`, `cmd/nestor`, `config/mcp.yaml`, `deploy/docker-compose.yml`, `deploy/.env.example`, `deploy/compose_test.go`.
- New optional environment variable: `GOOGLE_MAPS_API_KEY`.
- Zero new external Go dependencies, zero database migrations.
- Partially resolves `nestor-910` (`shared` credentials implemented; `stdio` and `container` remain open).
