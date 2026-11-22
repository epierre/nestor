package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp/config"
	"github.com/epierre/nestor/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// namespaceSep joins the server name and the remote tool name, so an MCP tool can never take the
// name of a built-in or of another server's tool.
const namespaceSep = "__"

// Limits used when mcp.yaml leaves timeout or max_result_bytes at zero.
const (
	defaultTimeout        = 10 * time.Second
	defaultMaxResultBytes = 64 << 10
)

// RegisterTools adds every tool allowlisted for server to reg, named <server>__<tool>. Risk comes
// from the configuration; annotations the server sends are ignored. A tool that the server does
// not offer, or whose name is already registered, is an error.
func RegisterTools(ctx context.Context, reg *tools.Registry, server config.Server, s *Session) error {
	offered, err := s.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("server %q: %w", server.Name, err)
	}
	byName := make(map[string]*sdk.Tool, len(offered))
	for _, t := range offered {
		byName[t.Name] = t
	}

	for name, cfg := range server.Tools {
		remote, ok := byName[name]
		if !ok {
			return fmt.Errorf("server %q does not offer allowlisted tool %q", server.Name, name)
		}
		schema, err := json.Marshal(remote.InputSchema)
		if err != nil {
			return fmt.Errorf("server %q tool %q: marshal input schema: %w", server.Name, name, err)
		}
		adapted := &adapterTool{
			server:   server.Name,
			remote:   name,
			name:     server.Name + namespaceSep + name,
			risk:     cfg.Risk,
			schema:   schema,
			session:  s,
			timeout:  orDefault(server.Timeout, defaultTimeout),
			maxBytes: orDefault(server.MaxResultBytes, defaultMaxResultBytes),
			users:    server.Users,
		}
		if err := reg.Register(adapted); err != nil {
			return fmt.Errorf("register %q: %w", adapted.name, err)
		}
	}
	return nil
}

// adapterTool exposes one allowlisted MCP tool as a registry tool.
type adapterTool struct {
	server   string
	remote   string // the tool's name on the server
	name     string // the registered name, <server>__<remote>
	risk     tools.Risk
	schema   json.RawMessage
	session  *Session
	timeout  time.Duration
	maxBytes int
	users    []string // the server's users list from mcp.yaml: user IDs, or "*"
}

// EnabledFor reports whether the server is enabled for user, by the users list in mcp.yaml.
func (t *adapterTool) EnabledFor(user identity.UserID) bool {
	for _, u := range t.users {
		if u == "*" {
			return true
		}
		if id, err := strconv.ParseInt(u, 10, 64); err == nil && identity.UserID(id) == user {
			return true
		}
	}
	return false
}

func (t *adapterTool) Name() string            { return t.name }
func (t *adapterTool) Description() string     { return "MCP tool " + t.remote + " on server " + t.server }
func (t *adapterTool) Schema() json.RawMessage { return t.schema }
func (t *adapterTool) Risk() tools.Risk        { return t.risk }

// Preview is generic for now: it names the tool and shows nothing the server controls.
func (t *adapterTool) Preview(json.RawMessage) (string, error) {
	return "MCP tool " + t.name, nil
}

// Execute calls the tool under the server's timeout. A timeout or a transport failure is an error;
// a result longer than the size cap is cut and marked as truncated.
func (t *adapterTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	callCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	res, err := t.session.CallTool(callCtx, t.remote, args)
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return tools.Result{}, fmt.Errorf("mcp tool %s timed out after %s", t.name, t.timeout)
		}
		return tools.Result{}, err
	}
	text := resultText(res)
	if res.IsError {
		return tools.Result{}, errors.New(text)
	}
	content, truncated := capResult(text, t.maxBytes)
	return tools.Result{Content: content, Truncated: truncated}, nil
}

// capResult returns text unchanged when it fits in max bytes. Otherwise it returns the longest
// valid UTF-8 prefix that fits, followed by a marker giving the number of bytes left out, and
// reports that the text was truncated.
func capResult(text string, max int) (string, bool) {
	if len(text) <= max {
		return text, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\n[truncated: %d bytes omitted]", text[:cut], len(text)-cut), true
}

func orDefault[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}
	return v
}

// resultText joins the text parts of a tool result. Non-text parts are noted, not passed on.
func resultText(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*sdk.TextContent); ok {
			b.WriteString(text.Text)
			continue
		}
		b.WriteString("[non-text content omitted]")
	}
	return b.String()
}
