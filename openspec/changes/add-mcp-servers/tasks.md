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
- [ ] 3.3 Per-user sessions (`ForUser`) with `header` delivery: the session for a call comes from the user in context; a user without a stored credential is not offered the server's tools. Test: `adapter_test.go` "each user served with own token" (fake server records the Authorization header per call), "missing credential hides tools"; `config_test.go` "header delivery refused for remote servers".
- [ ] 3.4 `cmd/mcpgen` renders per-user Compose services for `container` delivery from `mcp.yaml`. Token source to be settled first (see design, Credentials). Test: `mcpgen_test.go` golden file comparison; no secret values in output.

## 4. Pinning and health

- [ ] 4.1 Migration `mcp_tool_pins`; pin on first approval; compare on connect; model gets pinned description. Test: `pins_test.go` "first connect pins", "changed description disables tool", "model sees pinned not live text".
- [ ] 4.2 Admin alert on mismatch and `mcp approve` command. Test: `pins_test.go` "re-approve re-enables tool", `telegram_test.go` "admin alert shows old and new".
- [ ] 4.3 Health checks with backoff; unavailable server removes only its tools. Test: `health_test.go` "down server tools absent", "recovery re-adds after pin check".

## 5. Deployment and evals

- [ ] 5.1 Docker networks: `mcp` network without access to Postgres or LiteLLM; non-root, read-only containers. Test: manual check documented in README (`docker exec` into a server container cannot reach postgres:5432).
- [ ] 5.2 Wire one reference read-only server end to end. Test: manual smoke on the N100, plus `agent_test.go` with the fake server.
- [ ] 5.3 Evals with a malicious test server (injection in description and results, fake read-only annotation on a destructive tool). Test: `go test -tags eval ./evals/mcp/...` passes against `cheap` and `strong`, zero unapproved critical executions.
- [ ] 5.4 `openspec validate add-mcp-servers --strict` and `go test ./...` green.
