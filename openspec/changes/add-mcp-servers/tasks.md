## 1. Configuration

- [x] 1.1 `internal/mcp/config`: parse and validate `mcp.yaml` (risk required, users exist, credential key for per_user, unique names, remote flag vs URL host). Test: `config_test.go` table covering each validation error and a valid file.

## 2. Client and adapter

- [x] 2.1 Connect to servers over stdio and streamable HTTP with the official SDK; list tools. Test: integration `client_test.go` against `testdata/testserver` over both transports.
- [x] 2.2 Adapter wrapping allowlisted tools as registry tools named `<server>__<tool>` with risk from config. Test: `adapter_test.go` "only allowlisted tools registered", "risk from config ignores server annotation", "name clash with built-in".
- [x] 2.3 Timeout and result size cap; calls recorded in the tool call log. Test: `adapter_test.go` "timeout yields error result", "oversized result truncated with marker".
- [x] 2.3a Record a truncated MCP result as `truncated`: migration `0008_tool_calls_truncated` adds `truncated` to the `tool_calls.status` check, and the adapter signals truncation so the agent records that status. Test: integration `store_test.go` "truncated status accepted"; `agent_test.go` "truncated MCP result recorded as truncated".
- [x] 2.4 Critical MCP tools go through the approval gate with a generic preview. Test: `adapter_test.go` "critical MCP tool creates pending action", using the fake server.

## 3. Users and credentials

- [x] 3.1 Migration `user_credentials` with AES-GCM encryption under `CREDENTIALS_KEY`; CLI to set a credential. Test: integration `credentials_test.go` "round trip", "ciphertext differs per write", "wrong key fails".
- [x] 3.2 Per-user enablement in the tool list built for each request. Test: `agent_test.go` "server enabled for A not offered to B".
- [x] 3.3 Per-user sessions (`ForUser`) with `header` delivery: the session for a call comes from the user in context; a user without a stored credential is not offered the server's tools. Test: `adapter_test.go` "each user served with own token" (fake server records the Authorization header per call), "missing credential hides tools"; `config_test.go` "header delivery refused for remote servers".
- [ ] 3.4 (Deferred until a server needs `container` delivery; the owner chose to settle the token source then. Until 3.4 is done, startup must not connect `container` servers.) `cmd/mcpgen` renders per-user Compose services for `container` delivery from `mcp.yaml`. Token source to be settled first (see design, Credentials). Test: `mcpgen_test.go` golden file comparison; no secret values in output.

## 4. Pinning and health

- [x] 4.1 Migration `mcp_tool_pins`; pin on first approval; compare on connect; model gets pinned description. Test: `pins_test.go` "first connect pins", "changed description disables tool", "model sees pinned not live text".
- [x] 4.2 Tools wait for admin approval: connect records the seen version (first sight included), `nestor mcp approve <server> <tool>` approves it, the admin is alerted once per new or changed version, `ADMIN_USER_ID` names the admin. Rewrites migration 0010 (not yet merged). Test: `pins_test.go` "new tool waits for approval", "approve enables tool", "re-approve re-enables tool", "admin alert shows old and new", "alert sent once per version"; `telegram_test.go` "notify user reaches their chat"; `config_test.go` ADMIN_USER_ID; `cmd/nestor` "mcp approve".
- [x] 4.3 Health checks with backoff; unavailable server removes only its tools. Test: `health_test.go` "down server tools absent", "recovery re-adds after pin check".

## 5. Deployment and evals

- [ ] 5.1 First-party reference server `cmd/mcp-timezone` (read-only `get_time` and `convert_time`, streamable HTTP, timezone data embedded) in a scratch image, non-root, read-only, no capabilities, on an internal `mcp` network shared only with Nestor (no route out, no Postgres, no LiteLLM). Test: `cmd/mcp-timezone/main_test.go` for the tools; `deploy/compose_test.go` "mcp servers isolated" reads the Compose file; manual check documented in README (a throwaway container on the `mcp` network cannot reach postgres:5432 or the internet).
- [ ] 5.2 Wire MCP into startup: `MCP_CONFIG` names the reviewed `mcp.yaml` (unset: no MCP servers), mounted read-only; one Supervisor per server with pins in Postgres and alerts to `ADMIN_USER_ID`; servers that are not wired yet (`stdio`, `shared` credentials, `container` delivery) stop startup. `config/mcp.yaml` lists the time-zone server. Test: `agent_test.go` "MCP tool called through the agent" with the fake server; `cmd/nestor` "unsupported servers refused", "approved tools registered"; manual smoke on the N100.
- [ ] 5.3 Evals with a malicious test server (injection in description and results, fake read-only annotation on a destructive tool). Test: `go test -tags eval ./evals/mcp/...` passes against `cheap` and `strong`, zero unapproved critical executions.
- [ ] 5.4 `openspec validate add-mcp-servers --strict` and `go test ./...` green.
