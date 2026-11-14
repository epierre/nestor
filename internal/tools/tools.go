package tools

import (
	"context"
	"encoding/json"
)

// Risk classifies how consequential a tool's execution is.
type Risk string

const (
	RiskRead       Risk = "read"       // no side effects
	RiskReversible Risk = "reversible" // side effects the user can undo (create task, draft)
	RiskCritical   Risk = "critical"   // external or irreversible (send email, delete, accept invite)
)

// Result is the output of executing a tool.
type Result struct {
	Content string
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

// MarkData wraps tool output in a marker stating it is untrusted data.
func MarkData(content string) string {
	return "[data]\n" + content
}
