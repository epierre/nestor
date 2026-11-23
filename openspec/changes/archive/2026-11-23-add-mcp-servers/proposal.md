# Add MCP server integration

## Why

Many useful integrations already exist as MCP servers. Connecting them through the tool
registry lets the assistant gain capabilities without writing a Go tool for each one, and is
one of the main things this project is meant to teach. But an MCP server is third-party code
that describes its own tools, so it must be integrated in a way that cannot weaken the approval
gate, leak one user's credentials to another, or let a server's self-description steer the agent.

## What changes

- An MCP client in the agent (official MCP Go SDK) supporting streamable HTTP and stdio servers.
- A declarative `mcp.yaml` listing servers, their transport, pinned image/version, which users
  get them, how credentials are supplied, and an explicit allowlist of tools with a risk level each.
- An adapter that wraps each allowlisted MCP tool as a registry `Tool`. Risk levels come from
  `mcp.yaml`; server-provided annotations are ignored for enforcement.
- Per-user enablement: a user's model request only lists tools from servers enabled for that user.
- Per-user credentials: tokens for user-scoped servers come from the encrypted per-user
  credential store and are bound to the user ID in context.
- Tool description pinning: the description and schema of every allowlisted tool are recorded
  at approval time; a later change in either disables the tool and alerts the host admin.
- Server health: startup and periodic checks; a down server removes only its own tools.
- Size and time limits on tool results.
- One reference server wired end to end (a read-only server with no credentials, e.g. a
  weather or time-zone server) to exercise the path.

## Capabilities

- `mcp-integration` (new)

## Constitution principles touched

- 1 Approval in code: MCP tools pass through the same registry and gate; unlisted risk is not
  possible because unlisted tools are not exposed, and a listed tool without a risk is a config error.
- 2 Untrusted content: tool descriptions, schemas and results from servers are untrusted;
  descriptions are pinned and reviewed, results enter only as tool messages.
- 4 Data stays on the host: servers run as local containers with restricted egress; a remote
  (third-party hosted) server must be explicitly marked `remote: true` and is listed in the
  proposal review checklist.
- 6 User-scoped: per-user enablement and credentials; no server instance receives two users' tokens.
- 7 Secrets: per-user tokens encrypted at rest, injected per call, never logged.

## Non-goals

- Converting the built-in tools (tasks, calendar, email) into MCP servers.
- Exposing the assistant itself as an MCP server.
- MCP prompts, resources, sampling and elicitation (tools only for now).
- A UI for enabling servers; configuration is a file reviewed in git.
- OAuth flows driven by MCP servers; credentials are provisioned out of band.

## Impact

- New package `internal/mcp`; new config file `config/mcp.yaml`; new Compose services per server.
- New migration: `mcp_tool_pins`, `user_credentials` (if not already created by an earlier change).
- Depends on `add-tool-approval-gate`.
