# Design: MCP server integration

## Configuration

```yaml
# config/mcp.yaml (reviewed in git; loaded at startup)
servers:
  - name: weather
    transport: http                       # http | stdio
    url: http://mcp-weather:8080/mcp
    image: ghcr.io/example/weather-mcp:1.2.0   # pinned; Compose uses the same tag
    remote: false                         # true = hosted by a third party, leaves the host
    credentials: none                     # none | shared | per_user
    users: ["*"]                          # internal user IDs or "*"
    timeout: 10s
    max_result_bytes: 65536
    tools:
      get_forecast: { risk: read }

  - name: todoist
    transport: http
    url: http://mcp-todoist-{user}:8080/mcp   # per-user instance
    image: ghcr.io/example/todoist-mcp:1.4.2
    credentials: per_user
    credential_key: todoist_token
    users: [1]
    tools:
      get_tasks:   { risk: read }
      add_task:    { risk: reversible }
      delete_task: { risk: critical }
```

Validation at startup (process refuses to start on failure): every listed tool has a risk;
`per_user` servers name a `credential_key`; `users` refers to existing users; names are unique.

## Tool naming

Registry name = `<server>__<tool>` (e.g. `todoist__add_task`) so MCP tools cannot collide with
or shadow built-in tools. The model sees that name and the pinned description.

## Adapter

```go
type mcpTool struct {
    server  *serverConn
    name    string            // tool name on the server
    risk    tools.Risk        // from mcp.yaml
    pin     Pin               // description + schema hash recorded at approval
}

func (t *mcpTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
    userID := identity.MustUser(ctx)
    conn, err := t.server.ForUser(ctx, userID)   // per-user instance or shared
    ...                                          // call with timeout, cap result size
}
```

`Preview` for critical MCP tools renders the server name, tool name and arguments as a readable
list, since a generic server cannot provide a trustworthy preview itself.

## Credentials

Two options were considered for per-user servers:

1. One server instance per user, credential injected as an environment variable at container start.
2. Shared instance, credential sent per request (HTTP header).

Option 1 is the default: it is the only one that guarantees a server never holds two users'
tokens, and most community servers only support env-var configuration. Option 2 is allowed only
for servers we control. Credentials live in `user_credentials(user_id, key, ciphertext, nonce,
updated_at)`, encrypted with AES-GCM under `CREDENTIALS_KEY` from the environment.

For option 1, the Compose file contains one service per (server, user). A small generator
(`cmd/mcpgen`) renders those services from `mcp.yaml`, reading secrets at deploy time from
Docker secrets rather than writing them into the Compose file.

## Description pinning

On first approval of a tool, its description and input schema are hashed and stored in
`mcp_tool_pins(server, tool, description, schema_json, hash, approved_at)`. At every connect
the adapter lists the server's tools and compares. A mismatch disables that tool (it disappears
from model requests), logs an error, and sends the host admin a Telegram message showing the old
and new description. `nestor mcp approve <server> <tool>` re-pins after review.

The model always receives the pinned description, never a live one.

The "host admin" is the internal user ID in `ADMIN_USER_ID` (the owner in v1). Admin is an
operational role for alerts and CLI commands only; it grants no access to other users' data.

## Network isolation

Each MCP container joins an `mcp` Docker network that can reach the agent but not Postgres or
LiteLLM. Egress to the internet is allowed per server via a separate network only where the
server needs it (documented per server in `mcp.yaml` comments). Containers run as non-root with
a read-only filesystem where the image allows.

## Failure modes

- Server down at startup or mid-session: its tools are removed from model requests; the agent
  keeps working; health check retries with backoff; recovery re-adds them after the pin check.
- Server call times out: tool result "the tool timed out", recorded with status error. For a
  critical tool after approval: action becomes `failed`, no automatic retry.
- Result over the size limit: truncated with a marker, recorded as truncated.
- Pin mismatch: tool disabled as above, never silently re-enabled.
- Missing credential for a user: the server's tools are not offered to that user, and the
  startup log lists which user lacks which credential.

## Dependencies

- `github.com/modelcontextprotocol/go-sdk`: official MCP Go SDK (client side).
- No new runtime services beyond the MCP server containers themselves.

## Testing approach

- Unit: adapter against an in-process fake MCP server (the SDK supports in-memory transports).
- Integration: a tiny test MCP server binary in `testdata/` started over stdio and HTTP.
- Evals: a malicious test server whose description and results contain injection attempts.
