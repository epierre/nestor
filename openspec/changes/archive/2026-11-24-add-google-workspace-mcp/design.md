# Design: Google Workspace MCP servers and per-user OAuth 2.0

## Configuration schema (`internal/mcp/config/config.go`)

Extend `config.Server` with three optional YAML fields decoded strictly via `yaml.Decoder.KnownFields(true)`:

- **`OAuthTokenURL string` (`yaml:"oauth_token_url"`):** OAuth 2.0 token endpoint URL (for example `https://oauth2.googleapis.com/token`).
- **`OAuthClientIDEnv string` (`yaml:"oauth_client_id_env"`):** Name of the host environment variable holding the OAuth client ID.
- **`OAuthClientSecretEnv string` (`yaml:"oauth_client_secret_env"`):** Name of the host environment variable holding the OAuth client secret.

Extend `credential_delivery` for `credentials: per_user` to accept `"oauth2"` alongside `"header"` and `"container"`.

Validation rules for `credential_delivery: oauth2`:

- `credentials` MUST be `"per_user"` and `credential_key` MUST be non-empty.
- `transport` MUST be `"http"`.
- `url` MUST NOT contain `{user}` (one shared HTTP endpoint template; sessions are keyed per user inside `UserConnector`).
- `oauth_token_url` MUST be a valid `http://` or `https://` URL with a non-empty host; when `remote: true`, **both** `url` and `oauth_token_url` MUST use `https://`.
- `oauth_client_id_env` and `oauth_client_secret_env` MUST be set and valid env var names (`^[A-Z_][A-Z0-9_]*$`).
- Reject `oauth_token_url`, `oauth_client_id_env`, or `oauth_client_secret_env` whenever `credential_delivery != "oauth2"`.
- Keep `credential_delivery: header` strictly `remote: false` so static tokens are never sent off-host.

## Per-user OAuth 2.0 credential payload & refresh transport (`internal/mcp/oauth.go`, `internal/mcp/user.go`)

- **Encrypted payload format:** Stored in `user_credentials` under `(user_id, credential_key)` (e.g., `google_oauth`) as AES-256-GCM encrypted JSON:
  ```json
  {"refresh_token":"..."}
  ```
  Parsed and encoded via `ParseOAuthCredential` / `MarshalOAuthCredential`; empty or missing `refresh_token` returns a static error without echoing ciphertext or plaintext.
- **`OAuthConfig`:**
  ```go
  type OAuthConfig struct {
      ClientID     string
      ClientSecret string
      TokenURL     string
      HTTPClient   *http.Client // optional; defaults to 10s timeout client
  }
  ```
  Constructed via `NewOAuthUserConnector(server, creds, sealer, oauthCfg)`.
- **Per-user refresh & session isolation (`UserConnector.ForUser`):**
  - Look up `cred` for `(user, server.CredentialKey)` and compare `cred.Nonce` with the cached session's `nonce` (re-running `nestor credentials google-auth` rotates the AES-GCM nonce, so the old session is closed and recreated transparently).
  - Decrypt `refresh_token`, construct an `oauth2.Config` (`ClientID`, `ClientSecret`, `Endpoint: oauth2.Endpoint{TokenURL: oauthCfg.TokenURL}`), and bind a dedicated `tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, httpClient)` (10s timeout) so an individual cancelled tool call cannot poison future token refreshes.
  - Wrap `oauth2.ReuseTokenSource(nil, cfg.TokenSource(tokenCtx, &oauth2.Token{RefreshToken: tok.RefreshToken}))` in a `tokenSourceTransport` `http.RoundTripper` that calls `src.Token()`, clones `r`, sets `Authorization: Bearer <access_token>`, and wraps any refresh error in a sanitized error naming only server and reason (never refresh token, access token, or client secret).
  - Each user keeps a separate `TokenSource`, `http.Client`, and `*Session` in `sessions[user]`.

## CLI onboarding (`cmd/nestor/credentials.go`, `cmd/nestor/google_auth.go`)

Add subcommand:

```sh
nestor credentials google-auth <user-id> [--key google_oauth] [--manual | --listen 127.0.0.1:8085] [--scopes ...]
```

- **Default `--manual` mode (headless / Docker Compose / zero published ports):**
  1. Generate a PKCE verifier (`oauth2.GenerateVerifier()`, `oauth2.S256ChallengeOption`) and a 32-byte random hex `state`.
  2. Print the Google authorization URL (`access_type=offline`, `prompt=consent`, redirect URI `http://127.0.0.1:8085/callback` by default).
  3. Read the pasted callback URL from stdin, parse `code` and `state`, verify constant-time `state` equality, and reject missing/mismatched values without writing to the store.
  4. Exchange `code` + PKCE verifier at `https://oauth2.googleapis.com/token` (configurable via internal options for tests).
  5. Require a non-empty `RefreshToken`, marshal `{"refresh_token": ...}`, seal with `sealer.Seal(user, key, payload)`, and persist via `st.SetCredential`.
- **Optional `--listen 127.0.0.1:<port>` mode:** Starts a one-shot loopback HTTP server on `/callback` to capture the redirect automatically (useful over SSH port-forwarding or local dev).
- **Default scopes** (personal Gmail account — Contacts, Calendar, Docs, Drive; no Gmail mail scopes):
  - `https://www.googleapis.com/auth/userinfo.profile`
  - `https://www.googleapis.com/auth/contacts.readonly`
  - `https://www.googleapis.com/auth/calendar.calendarlist.readonly`
  - `https://www.googleapis.com/auth/calendar.events.freebusy`
  - `https://www.googleapis.com/auth/calendar.events`
  - `https://www.googleapis.com/auth/documents`
  - `https://www.googleapis.com/auth/drive.readonly`
  - `https://www.googleapis.com/auth/drive.file`
- **Secret hygiene:** stdout, stderr, and error messages never print `client_secret`, `code`, `access_token`, or `refresh_token`.

## Startup wiring (`cmd/nestor/mcpstart.go`)

For each server with `server.CredentialDelivery == "oauth2"`:

1. Read `clientID := strings.TrimSpace(getenv(server.OAuthClientIDEnv))` and `clientSecret := strings.TrimSpace(getenv(server.OAuthClientSecretEnv))` **before** checking `CREDENTIALS_KEY`.
2. If either is empty, log `slog.WarnContext(ctx, "mcp oauth2 client env var not set; server disabled", "server", server.Name, "missing_env", ...)` (naming only the server and env var name(s), never values) and `continue` — keeping startup clean when Google OAuth is not configured yet.
3. Require `CREDENTIALS_KEY` (and initialize `Sealer`) only when a `header` server exists or an `oauth2` server has both OAuth env vars set; fail startup if `CREDENTIALS_KEY` is missing in that case.
4. Construct `NewOAuthUserConnector(server, d.Credentials, sealer, oauthCfg)` and wire `sup.Users = users`, `sup.Dial = listAsFirstUser(server, d.Users, users)`, and `sup.Interval = server.HealthInterval`.

## Reviewed `config/mcp.yaml`, Compose, and roadmap updates

Add five Workspace servers to `config/mcp.yaml` (all sharing `credential_key: google_oauth`, `credential_delivery: oauth2`, `oauth_token_url: https://oauth2.googleapis.com/token`, `oauth_client_id_env: GOOGLE_OAUTH_CLIENT_ID`, `oauth_client_secret_env: GOOGLE_OAUTH_CLIENT_SECRET`, `remote: true`, `users: ["*"]`, `timeout: 15s`, `health_interval: 5m`, `max_result_bytes: 65536`):

| Server | URL | Read tools (`risk: read`) | Critical tools (`risk: critical`) |
|---|---|---|---|
| `google-contacts` | `https://people.googleapis.com/mcp/v1` | `search_contacts`, `get_user_profile` | — |
| `google-calendar` | `https://calendarmcp.googleapis.com/mcp/v1` | `list_calendars`, `list_events`, `get_event`, `suggest_time` | `create_event`, `update_event`, `delete_event`, `respond_to_event` |
| `google-docs` | `https://docsmcp.googleapis.com/mcp/v1` | `read_doc` | `update_doc` |
| `google-drive` | `https://drivemcp.googleapis.com/mcp/v1` | `search_files`, `read_file_content`, `list_recent_files`, `get_file_metadata`, `get_file_permissions` | `create_file`, `copy_file` |
| `google-workspace-search` | `https://workspacemcp.googleapis.com/mcp/v1` | `search_corpus` | — |

In `deploy/docker-compose.yml`, pass `GOOGLE_OAUTH_CLIENT_ID: ${GOOGLE_OAUTH_CLIENT_ID:-}` and `GOOGLE_OAUTH_CLIENT_SECRET: ${GOOGLE_OAUTH_CLIENT_SECRET:-}` **only** to `nestor`, and document both as optional in `deploy/.env.example`.

Update `docs/vision.md` and `openspec/config.yaml` to note that `add-google-workspace-mcp` (`google-calendar` MCP) replaces the planned native `add-calendar` change.

## Google Cloud prerequisites & personal `@gmail.com` caveats

- **Developer Preview & APIs:** Enroll the GCP project in the **Google Workspace Developer Preview Program** and enable both base APIs (`people.googleapis.com`, `calendar-json.googleapis.com`, `docs.googleapis.com`, `drive.googleapis.com`) and MCP APIs (`calendarmcp.googleapis.com`, `docsmcp.googleapis.com`, `drivemcp.googleapis.com`, `workspacemcp.googleapis.com`).
- **"AI-powered agent" OAuth flag:** In Google Cloud Console (`Google Auth Platform > Clients`), check **"Use this client for an AI-powered agent"** on the OAuth 2.0 client; Google rejects `*.googleapis.com/mcp/v1` calls without it.
- **7-day Testing-mode refresh token expiry on personal `@gmail.com`:** External OAuth apps in `Testing` mode expire refresh tokens after 7 days. The owner can either publish the app to `Production` (subject to Google app verification/unverified-app consent rules) or re-run `nestor credentials google-auth <user-id>` weekly; rotating the stored credential via CLI automatically updates the live session without restarting Nestor.
- **`workspacemcp.googleapis.com` on personal accounts:** Some Universal Search (`search_corpus`) capabilities target managed Google Workspace accounts and may return `403` on personal `@gmail.com` accounts. Because each MCP server is isolated in its own supervisor, `google-contacts`, `google-calendar`, `google-docs`, and `google-drive` remain unaffected even if `google-workspace-search` is unavailable or returns `403`.

## Failure modes

| Failure mode | Behaviour |
|---|---|
| `GOOGLE_OAUTH_CLIENT_ID` or `GOOGLE_OAUTH_CLIENT_SECRET` unset | OAuth2 MCP servers are skipped with a warning naming server and missing env var(s); `CREDENTIALS_KEY` is not required for skipped servers; other servers and built-ins run normally. |
| User has no stored `google_oauth` credential | Missing credential is logged by key name only; none of the Workspace tools are offered to that user; other users with credentials continue working. |
| Access token expires (~1 hour) | `oauth2.ReuseTokenSource` exchanges the decrypted refresh token transparently on the next request without dropping the session or exposing tokens. |
| Refresh token revoked or expired (e.g. 7-day Testing limit) | Token refresh returns a sanitized error naming only server/reason; the tool call fails cleanly with `status = error` in `tool_calls`; re-running `nestor credentials google-auth <user-id>` rotates the nonce and replaces the session cleanly. |
| `oauth2.googleapis.com` unreachable or slow | Token client's 10s timeout bounds the call; error is sanitized and tool call fails cleanly without poisoning future attempts. |
| One Workspace MCP server down or returns 403 (e.g. `workspacemcp` on personal Gmail, or missing AI-agent flag) | Supervisor removes only that server's tools and retries with backoff (`1s`..`5m`); other Workspace servers and built-ins remain available. |
| Rate limit (429) or tool timeout (>15s) | Tool call returns an error result to the model and is recorded in `tool_calls` with `status = error`. |
| Oversized response (>64 KB) | Truncated with UTF-8 boundary marker and recorded in `tool_calls` as `truncated`. |
| Description or schema change in Developer Preview | Pin mismatch disables only the changed tool, logs an error, and sends a one-time Telegram alert to `ADMIN_USER_ID` until re-approved via `nestor mcp approve`. |
| Postgres down | Credential lookup / pin recording fails; tools are withheld safely and supervisors retry with backoff. |
| Telegram down | Admin pin alert failure is logged; unapproved tools stay disabled safely. |

## Dependencies

- **`golang.org/x/oauth2` v0.35.0:** Promoted from indirect (`go.mod`) to direct dependency for standard OAuth 2.0 PKCE code exchange and thread-safe token refresh (`oauth2.ReuseTokenSource`). Adds zero new transitive dependencies.

## Testing approach

- **Config unit tests (`internal/mcp/config`):** Table-driven `TestValidate` and `TestLoad` cases for `credential_delivery: oauth2`, `oauth_token_url`, `oauth_client_id_env`, `oauth_client_secret_env`, remote HTTPS enforcement on both URLs, rejection on non-`oauth2` servers, and preserving `remote: false` on `header`.
- **OAuth connector tests (`internal/mcp`):** `httptest.NewServer` fake token endpoint + fake SDK MCP server verifying two-user isolation, automatic refresh when an access token expires, session replacement when the stored credential nonce changes, and secret-free refresh error messages.
- **CLI tests (`cmd/nestor`):** Unit tests for `nestor credentials google-auth` verifying PKCE S256 challenge, `state` verification, manual stdin flow, loopback `--listen` flow, rejection of state mismatch / missing refresh token, AES-GCM storage under `google_oauth`, and zero secret leakage in output/errors.
- **Startup & Compose tests (`cmd/nestor`, `deploy`):** Tests for OAuth2 startup registration + refresh, graceful disablement when OAuth client env vars are absent (even without `CREDENTIALS_KEY`), failing when env vars are set without `CREDENTIALS_KEY`, `TestShippedMCPConfigIsValid` covering all 5 Workspace servers + critical write risks, `TestShippedMCPConfigStartsWithoutOptionalExternalKeys`, and `TestGoogleOAuthEnvScopedToNestor`.
