# Design: Google Maps MCP server and shared credentials

## Configuration schema (`internal/mcp/config/config.go`)

Extend `config.Server` with three optional YAML fields decoded strictly via `yaml.Decoder.KnownFields(true)`:

- **`CredentialEnv string` (`yaml:"credential_env"`):** Name of the host environment variable containing the shared secret when `credentials: shared`. Must match `^[A-Z_][A-Z0-9_]*$`. Rejected if set on servers with `credentials: none` or `credentials: per_user`.
- **`CredentialHeader string` (`yaml:"credential_header"`):** Optional HTTP header name for `credentials: shared` (for example, `X-Goog-Api-Key`). When omitted/empty, Nestor sends `Authorization: Bearer <secret>`. When set, Nestor sends `<credential_header>: <secret>` verbatim. Must be a valid HTTP token per RFC 9110 (non-empty ASCII visible token characters `[!#$%&'*+\-.^_`|~0-9A-Za-z]`, no whitespace, `:`, or control characters — implemented with a small stdlib helper, no new dependencies). Rejected if set on `credentials: none` or `credentials: per_user`.
- **`HealthInterval time.Duration` (`yaml:"health_interval"`):** Optional health check and pin-refresh cadence for `mcp.Supervisor`. Zero means the existing `30s` default (`defaultHealthInterval`). Negative durations are rejected at startup.

Additional validation rules for `credentials: shared`:

- `transport` MUST be `"http"`.
- `credential_key` and `credential_delivery` MUST be empty (those belong only to `per_user`).
- `credential_env` MUST be set and valid.
- When `remote: true`, `url` MUST use the `https` scheme so host-wide secrets never traverse plaintext HTTP across public networks.

Also update the stale doc comment on `config.Validate` that still references `openspec/changes/add-mcp-servers` to point to `openspec/specs/mcp-integration/spec.md`.

## HTTP client & header transport (`internal/mcp/client.go`)

Add `DialHTTPWithHeader(ctx context.Context, url, headerName, headerValue string) (*Session, error)`:

- When `headerName == ""`, connect using `&sdk.StreamableClientTransport{Endpoint: url, DisableStandaloneSSE: true}` directly.
- When `headerName != ""`, supply an `http.Client` wrapping a small `staticHeader` `http.RoundTripper` (cloning each request and calling `r.Header.Set(h.name, h.value)`).
- `DialHTTP(ctx, url)` delegates to `DialHTTPWithHeader(ctx, url, "", "")`.
- Never log `headerValue` or include it in wrapped errors.

## Startup wiring (`cmd/nestor/mcpstart.go`, `cmd/nestor/main.go`)

- Extend `mcpDeps` with:
  - `Getenv func(string) string` (defaults to `os.Getenv` when nil).
  - `DialWithHeader func(ctx context.Context, url, headerName, headerValue string) (*mcp.Session, error)` (defaults to `mcp.DialHTTPWithHeader` when nil).
- Remove the `case s.Credentials == "shared"` rejection in `supported()`.
- When `server.Credentials == "shared"`:
  - Read `val := strings.TrimSpace(getenv(server.CredentialEnv))`.
  - If `val == ""`, log `slog.WarnContext(ctx, "mcp shared credential env var not set; server disabled", "server", server.Name, "credential_env", server.CredentialEnv)` (never logging any value) and skip starting a supervisor for that server. Other configured servers and built-in tools continue normally.
  - Otherwise, choose `(headerName, headerValue)`:
    - If `server.CredentialHeader == ""`: `("Authorization", "Bearer " + val)`.
    - Otherwise: `(server.CredentialHeader, val)`.
  - Wire `sup.Dial` to call `dialWithHeader(ctx, url, headerName, headerValue)`.
- Set `sup.Interval = server.HealthInterval` for every server supervisor (`0` falls back to `30s` inside `Supervisor.Run`).
- Operational note: because `Supervisor` refreshes pins on each `Interval` tick, running `nestor mcp approve google-maps <tool>` takes effect within one `health_interval` (up to `5m` for Maps) or immediately upon restarting Nestor.

## Reviewed Maps configuration (`config/mcp.yaml`, `deploy/docker-compose.yml`, `deploy/.env.example`)

Add to `config/mcp.yaml`:

```yaml
  - name: google-maps
    transport: http
    url: https://mapstools.googleapis.com/mcp
    remote: true
    credentials: shared
    credential_env: GOOGLE_MAPS_API_KEY
    credential_header: X-Goog-Api-Key
    users: ["*"]
    timeout: 15s
    health_interval: 5m
    max_result_bytes: 65536
    tools:
      search_places:  { risk: read }
      lookup_weather: { risk: read }
      compute_routes: { risk: read }
```

In `deploy/docker-compose.yml`, pass `GOOGLE_MAPS_API_KEY: ${GOOGLE_MAPS_API_KEY:-}` **only** to the `nestor` service (never to `litellm`, `postgres`, `mcp-timezone`, or `sandboxd`), verified by `deploy/compose_test.go`. Document `GOOGLE_MAPS_API_KEY=` (empty by default, optional) in `deploy/.env.example`.

## Failure modes

| Failure mode | Behaviour |
|---|---|
| `GOOGLE_MAPS_API_KEY` unset or empty at startup | `google-maps` supervisor is not started; a warning naming only server and env var is logged; all other MCP servers and built-in tools run normally. |
| Google Maps MCP unreachable / network down / 5xx | Supervisor removes only `google-maps__*` tools from the registry and retries with exponential backoff (`1s`..`5m`); the agent keeps working. |
| Invalid/revoked API key or Maps MCP API not enabled (401/403) | Dial or ping fails; error is logged without secret values; tools remain absent and supervisor retries with backoff. |
| Maps API quota or rate limit (429) | Tool call returns an error result to the model and is recorded in `tool_calls` with `status = error`. |
| Tool call exceeds `timeout` (`15s`) | Context cancels; tool returns a timeout error result and is recorded with `status = error`. |
| Response exceeds `max_result_bytes` (`65536`) | Output is UTF-8 truncated with `[truncated: N bytes omitted]` and recorded with `status = truncated`. |
| Google updates tool description or schema | Pin hash mismatch disables only the changed tool, logs an error, and sends a one-time Telegram alert to `ADMIN_USER_ID` until re-approved via `nestor mcp approve`. |
| Postgres down | Pin recording/lookup fails; supervisor does not register tools and retries with backoff. |
| Telegram down | Admin pin alert delivery failure is logged; unapproved tools stay disabled safely. |

## Dependencies

- **Zero new external Go dependencies.** Uses Go standard library (`net/http`, `strings`, `time`), existing `github.com/modelcontextprotocol/go-sdk` v1.8.0, and existing `gopkg.in/yaml.v3` v3.0.1.

## Testing approach

- **Config unit tests (`internal/mcp/config`):** Table-driven tests in `config_test.go` and `load_test.go` covering valid/invalid `shared` configurations, `credential_env` syntax, RFC 9110 `credential_header` validation, `remote: true` requiring `https://`, rejection of `credential_env`/`credential_header` on `none`/`per_user`, and `health_interval` validation (positive, zero, negative).
- **Client unit tests (`internal/mcp`):** `httptest.NewServer` backed by the official MCP Go SDK server verifying `DialHTTPWithHeader` sends `X-Goog-Api-Key` (without `Authorization`), sends `Authorization: Bearer ...`, and omits auth headers when empty.
- **Startup unit tests (`cmd/nestor`):** `httptest.NewServer` / `httptest.NewTLSServer` tests for custom header shared servers, default Bearer shared servers, missing env var disabling only that server without failing startup, custom `health_interval` wiring, and `TestShippedMCPConfigIsValid` + `TestShippedMCPConfigStartsWithoutGoogleMapsKey`.
- **Compose test (`deploy/compose_test.go`):** Verify `GOOGLE_MAPS_API_KEY` is passed only to `nestor` and not to other services.
- **Manual live smoke test (not in automated CI):** After setting `GOOGLE_MAPS_API_KEY` in `deploy/.env`, start Nestor, inspect admin alerts for the 3 seen Maps tools, approve with `nestor mcp approve google-maps <tool>`, and exercise `search_places`, `lookup_weather`, and `compute_routes`.
