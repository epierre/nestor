// Package model defines the provider-agnostic interface to language models.
package model

import (
	"context"
	"encoding/json"
	"time"
)

// Role is the author of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry of the conversation sent to a model.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // set on assistant messages that call tools
	ToolCallID string     // set on tool messages, naming the call they answer
}

// Tool describes a tool the model may call.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// CompletionRequest is one model call. Model is a proxy alias, never a provider model ID.
type CompletionRequest struct {
	Model     string
	Messages  []Message
	Tools     []Tool
	MaxTokens int // 0 means the provider default
}

// Usage reports what a call consumed. CostUSD is nil when the proxy reports no cost.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CostUSD          *float64
}

// Attempt is one HTTP request made for a call, so that a retried call can be recorded as several
// model calls. Request and Response are the bodies as sent and received, with no credentials;
// Response is nil when none arrived and is not always JSON.
type Attempt struct {
	StartedAt time.Time
	Latency   time.Duration
	Request   []byte
	Response  []byte
	Err       error // nil for the attempt that succeeded
}

// CompletionResponse carries either text or tool calls, plus usage. Usage describes the successful
// attempt, the last one in Attempts.
type CompletionResponse struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
	Attempts  []Attempt
}

// ModelClient calls a language model.
type ModelClient interface {
	// Complete returns the model's answer. When it returns an error, the response still holds
	// the Attempts that were made.
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}
