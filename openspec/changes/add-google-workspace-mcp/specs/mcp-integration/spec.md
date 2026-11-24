## ADDED Requirements

### Requirement: Per-user OAuth 2.0 MCP credentials
The system SHALL support `credential_delivery: oauth2` on `credentials: per_user` HTTP MCP servers configured with `oauth_token_url`, `oauth_client_id_env`, and `oauth_client_secret_env`, requiring `https://` for both `url` and `oauth_token_url` when `remote` is true, refreshing per-user access tokens transparently from each user's encrypted refresh token without sharing tokens across users or leaking secrets, and disabling only OAuth servers when their client environment variables are unset.

#### Scenario: Per-user OAuth server refreshes and isolates tokens across users
- **WHEN** users A and B each have an encrypted OAuth refresh token stored under the server's `credential_key` and user A's initial access token expires before a subsequent tool call
- **THEN** Nestor refreshes user A's access token automatically via `oauth_token_url`, sends only user A's Bearer token on A's requests and only user B's Bearer token on B's requests, and completes both calls without exposing secrets

#### Scenario: Re-authorizing a user updates the session cleanly
- **WHEN** a user's stored OAuth credential row is replaced with a new refresh token (rotating its encryption nonce) while a cached session exists
- **THEN** Nestor closes the user's previous MCP session and opens a new session using the updated refresh token on the next call

#### Scenario: Missing OAuth client env vars disable only OAuth servers
- **WHEN** `mcp.yaml` configures `credential_delivery: oauth2` servers alongside other servers and `oauth_client_id_env` or `oauth_client_secret_env` is unset or empty at startup (even when `CREDENTIALS_KEY` is unset)
- **THEN** Nestor logs a warning naming the server and missing environment variable name(s) without any secret value, disables only the OAuth servers, and starts remaining servers normally

#### Scenario: Invalid OAuth server configuration fails startup
- **WHEN** `mcp.yaml` configures `credential_delivery: oauth2` without valid `oauth_token_url`, `oauth_client_id_env`, or `oauth_client_secret_env`, with `remote: true` and a non-`https://` `url` or `oauth_token_url`, with `{user}` in `url`, with `transport: stdio`, or sets `oauth_*` fields on a non-`oauth2` server
- **THEN** configuration validation fails at startup naming the server without contacting any server

#### Scenario: Token refresh failure does not leak secrets
- **WHEN** exchanging a user's refresh token at `oauth_token_url` fails with an error or non-2xx response
- **THEN** the returned error names the server or failure without containing the refresh token, access token, or client secret

### Requirement: Google OAuth CLI onboarding
The system SHALL provide a `nestor credentials google-auth <user-id>` CLI command using OAuth 2.0 Authorization Code flow with PKCE (`S256`) and random `state` verification (defaulting to headless `--manual` stdin callback entry and optionally supporting `--listen 127.0.0.1:<port>`) to exchange an authorization code for a refresh token and store it encrypted under the user's credential key.

#### Scenario: Manual OAuth onboarding exchanges code and stores encrypted refresh token
- **WHEN** an operator runs `nestor credentials google-auth 1` in manual mode and pastes a callback URL carrying the matching `state` and authorization `code`
- **THEN** the CLI exchanges the `code` and PKCE verifier at the token endpoint, encrypts `{"refresh_token":...}` bound to user `1` and key `google_oauth`, and persists it in `user_credentials`

#### Scenario: State mismatch or missing refresh token fails without storing
- **WHEN** the callback URL has a mismatched `state` or the token response omits `refresh_token`
- **THEN** the command returns an error and writes nothing to `user_credentials`

#### Scenario: CLI onboarding never outputs secrets
- **WHEN** `nestor credentials google-auth` succeeds or fails
- **THEN** stdout, stderr, and error messages contain no client secret, authorization code, access token, or refresh token

### Requirement: Reviewed Google Workspace MCP configuration
The system SHALL ship reviewed `google-contacts`, `google-calendar`, `google-docs`, `google-drive`, and `google-workspace-search` entries in `config/mcp.yaml` (`remote: true`, `credential_delivery: oauth2`, `credential_key: google_oauth`, `health_interval: 5m`, read tools at `risk: read`, and all Calendar/Docs/Drive write tools at `risk: critical`), and SHALL pass `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` only to the `nestor` service in `deploy/docker-compose.yml`.

#### Scenario: Shipped Google Workspace servers are valid and classify write tools as critical
- **WHEN** `config/mcp.yaml` is loaded and validated at startup
- **THEN** all five Google Workspace entries pass validation with `remote: true` and `https://` URLs, and `create_event`, `update_event`, `delete_event`, `respond_to_event`, `update_doc`, `create_file`, and `copy_file` are configured with `risk: critical`

#### Scenario: Shipped configuration starts cleanly without Google OAuth env vars
- **WHEN** Nestor starts with the shipped `config/mcp.yaml` while `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` are empty
- **THEN** startup succeeds, only the unconfigured OAuth servers are skipped with warnings, and built-in tools and other configured servers remain available

#### Scenario: Google OAuth client credentials are scoped to nestor in Compose
- **WHEN** `deploy/docker-compose.yml` is inspected
- **THEN** `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` are passed to `nestor` and absent from `postgres`, `litellm`, `mcp-timezone`, `sandboxd`, and `skill-runner`
