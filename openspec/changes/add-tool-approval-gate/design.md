# Design: tool registry and approval gate

## Tool contract

```go
type Risk string
const (
    RiskRead       Risk = "read"       // no side effects
    RiskReversible Risk = "reversible" // side effects the user can undo (create task, draft)
    RiskCritical   Risk = "critical"   // external or irreversible (send email, delete, accept invite)
)

type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage           // JSON Schema for arguments
    Risk() Risk
    Preview(args json.RawMessage) (string, error) // human-readable card text for approval
    Execute(ctx context.Context, args json.RawMessage) (Result, error)
}
```

`Execute` always receives a context carrying the user ID the call belongs to. Tools that need
credentials (later: calendar, email) look them up by that user ID; a tool never takes a user
or account identifier from the model's arguments.

Risk is a property of the tool definition in code, never of the model's request. A future
MCP-backed tool is an adapter implementing this interface; its risk is set in our config,
not taken from the server's self-description.

## Flow

```
model ── tool_call(name,args) ──▶ registry.Lookup ──▶ validate args
                                         │
                       read/reversible ──┼──▶ Execute ──▶ tool message ──▶ model
                                         │
                              critical ──┴──▶ approval.Propose ──▶ pending_actions row
                                                    │                  (args, args_hash, preview)
                                                    ▼
                               model receives tool message "pending approval <id>"
                               action's user receives card [Approve] [Reject]

user taps Approve ──▶ channel resolves tapper to user_id ──▶ approval.Decide(user_id, action_id, decision)
      ──▶ load row WHERE id AND user_id ──▶ check state, expiry, args_hash
      ──▶ Execute(ctx with action's user_id) ──▶ record ──▶ report
```

The only call site of `Execute` for a critical tool is inside `approval.Decide`. A unit test
asserts this structurally (the agent package holds a registry view that refuses to execute
critical tools).

## Pending action states

`proposed → approved → executed | failed`
`proposed → rejected`
`proposed → expired`
`proposed → superseded` (the user asked for an edit and the agent proposed a replacement)

Transitions use a single `UPDATE ... WHERE state = 'proposed'` so a double tap executes once.

## Edit

Telegram has no form fields, so Edit is conversational: while an action is `proposed`, if its
user replies in the chat instead of tapping, the agent receives the reply with the pending
action in context, may propose a replacement, and the old action becomes `superseded`. The new
action gets a new card and needs its own approval.

## Untrusted content handling

Tool results are appended as `tool` role messages wrapped in a marker stating they are data.
This is defence in depth only; the guarantee comes from the fact that no message content of any
role can reach `approval.Decide`, which is reachable only from a Telegram callback query, and
which only finds the action if the tapper's resolved user ID equals the action's user ID.
An action ID belonging to someone else is indistinguishable from a non-existent one, so the
response leaks nothing about other users' actions.

## Data model

- `tool_calls(id, user_id, conversation_id, model_call_id, tool_name, args_json, risk, status, result_json, error, started_at, latency_ms)`
- `pending_actions(id, user_id, tool_call_id, tool_name, args_json, args_hash, preview, state, created_at, expires_at, decided_at, executed_at, result_json, error, superseded_by)`

Action IDs are random (UUIDv7 or similar), not sequential integers, so they cannot be guessed.
`args_hash` covers user ID, tool name and arguments together.

## Dependencies

- `github.com/santhosh-tekuri/jsonschema/v6` (or similar): argument validation against each tool's schema.

## Failure modes

- Execution fails after approval: state `failed`, error stored, user told; no automatic retry
  for critical tools (a retry is a new proposal).
- Process restarts with actions `proposed`: they stay valid until expiry; buttons keep working.
- Callback for an unknown, already-decided or someone else's action: the tapper receives
  "This action is no longer pending."
- Telegram down when the card should be sent: the action is stored; the card is re-sent when
  that user next messages, listing all of their still-pending actions.

## Configuration

`APPROVAL_TTL` (default 24h), `ENABLE_DEMO_TOOLS` (default false).
