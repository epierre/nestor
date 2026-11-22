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
    url: http://mcp-todoist:8080/mcp        # one shared instance
    image: ghcr.io/example/todoist-mcp:1.4.2
    credentials: per_user
    credential_key: todoist_token
    credential_delivery: header             # token sent per call, see Credentials
    users: [1]
    tools:
      get_tasks:   { risk: read }
      add_task:    { risk: reversible }
      delete_task: { risk: critical }

  - name: notes                             # third-party server that only reads an env var
    transport: http
    url: http://mcp-notes-{user}:8080/mcp   # one instance per user
    image: ghcr.io/example/notes-mcp:0.9.0
    credentials: per_user
    credential_key: notes_token
    credential_delivery: container
    users: [1, 2]
    tools:
      list_notes: { risk: read }
```

Validation at startup (process refuses to start on failure): every listed tool has a risk;
`per_user` servers name a `credential_key` and a `credential_delivery`; `users` refers to existing
users; names are unique; `header` delivery requires `remote: false` and a URL without `{user}`;
`container` delivery requires `{user}` in the URL.

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

A server with `credentials: per_user` gets a user's token one of two ways, chosen per server by
`credential_delivery` in `mcp.yaml`.

- **`header`** (servers we run): one shared container. On each call Nestor sends the calling
  user's token in an `Authorization` header, so the container only ever sees the token of the
  user making the call. Only allowed when `remote: false`, so a token never leaves the host.
- **`container`** (third-party servers that only accept an environment variable): one container
  per (server, user), started with that user's token. Used only where a server cannot take a
  header. Its token source is an open question (see below).

In both cases the session used for a call is chosen from the user in the context, never from
tool arguments. `ForUser(ctx, user)` returns the connection for that user, and a user with no
stored credential for a server is not offered that server's tools (the missing credential is
logged without its value).

Credentials live in `user_credentials(user_id, key, ciphertext, nonce, updated_at)`, encrypted
with AES-GCM under `CREDENTIALS_KEY` from the environment and bound to the user ID and key. They
are entered with `nestor credentials set`.

Open question for `container` servers: the container needs the token when it starts. Docker
secrets (an earlier idea here) would keep a second copy outside the encrypted store. Settle the
token source before task 3.4 is implemented.

## Description pinning

`mcp_tool_pins(server, tool, ...)` keeps two versions of each allowlisted tool: the **approved**
one (description, input schema, their hash, `approved_at`; empty until the first approval) and
the latest one **seen** on connect. At every connect the adapter records what it sees:

- Never approved: the tool stays out of model requests. The host admin gets a Telegram message
  with its description and the approve command.
- Approved and the seen hash matches: the tool is registered with the approved description and
  schema.
- Approved but the seen hash differs: the tool is disabled, an error is logged, and the admin
  gets a message with the old and new description.

The admin is messaged once per newly seen version, not on every connect.
`nestor mcp approve <server> <tool>` approves the version last seen; the tool is offered from the
next connect on (startup, or reconnect after a health check).

The model always receives the approved description and schema, never live ones.

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

- `github.com/modelcontextprotocol/go-sdk` v1.8.0 (released 2026-07-27): official MCP Go SDK (client side). Its own dependencies (`golang-jwt/jwt`, `segmentio/asm`, `x/sys`) come in transitively.
- `gopkg.in/yaml.v3` v3.0.1: decodes `mcp.yaml`. The standard library has no YAML parser, and
  v3 supports strict decoding (`KnownFields`) so a misspelled key such as `risks` fails at startup.
- No new runtime services beyond the MCP server containers themselves.

## Testing approach

- Unit: adapter against an in-process fake MCP server (the SDK supports in-memory transports).
- Integration: a tiny test MCP server binary in `testdata/` started over stdio and HTTP.
- Evals: a malicious test server whose description and results contain injection attempts.
