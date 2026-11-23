package tools

import (
	"context"
	"encoding/json"

	"github.com/epierre/nestor/internal/identity"
)

// Risk classifies how consequential a tool's execution is.
type Risk string

const (
	RiskRead       Risk = "read"       // no side effects
	RiskReversible Risk = "reversible" // side effects the user can undo (create task, draft)
	RiskCritical   Risk = "critical"   // external or irreversible (send email, delete, accept invite)
)

// Result is the output of executing a tool. Truncated is set when Content is a cut-down form of
// the full output, so the tool call log can say so.
type Result struct {
	Content   string
	Truncated bool
}

func (r Result) String() string {
	return r.Content
}

// Tool is a capability the model may call.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Risk() Risk
	Preview(args json.RawMessage) (string, error)
	Execute(ctx context.Context, args json.RawMessage) (Result, error)
}

// UserScoped is implemented by a tool that only some users may use, such as an MCP tool whose
// server is enabled for a subset of users. Tools that do not implement it are available to all.
// EnabledFor may read user data, such as whether the user has a stored credential, so ctx must
// carry user.
type UserScoped interface {
	EnabledFor(ctx context.Context, user identity.UserID) bool
}

// Enabled reports whether t may be offered to or run for user.
func Enabled(ctx context.Context, t Tool, user identity.UserID) bool {
	scoped, ok := t.(UserScoped)
	return !ok || scoped.EnabledFor(ctx, user)
}

// ContextPreviewer is implemented by a tool whose approval preview needs the stored data that its
// arguments refer to, such as the title of a task. It is called with the requesting user in the
// context, so it can read only that user's data.
type ContextPreviewer interface {
	PreviewFor(ctx context.Context, args json.RawMessage) (string, error)
}

// MarkData wraps tool output in a marker stating it is untrusted data.
func MarkData(content string) string {
	return "[data]\n" + content
}
