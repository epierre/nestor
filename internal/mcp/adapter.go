package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/epierre/nestor/internal/mcp/config"
	"github.com/epierre/nestor/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// namespaceSep joins the server name and the remote tool name, so an MCP tool can never take the
// name of a built-in or of another server's tool.
const namespaceSep = "__"

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
			server:  server.Name,
			remote:  name,
			name:    server.Name + namespaceSep + name,
			risk:    cfg.Risk,
			schema:  schema,
			session: s,
		}
		if err := reg.Register(adapted); err != nil {
			return fmt.Errorf("register %q: %w", adapted.name, err)
		}
	}
	return nil
}

// adapterTool exposes one allowlisted MCP tool as a registry tool.
type adapterTool struct {
	server  string
	remote  string // the tool's name on the server
	name    string // the registered name, <server>__<remote>
	risk    tools.Risk
	schema  json.RawMessage
	session *Session
}

func (t *adapterTool) Name() string            { return t.name }
func (t *adapterTool) Description() string     { return "MCP tool " + t.remote + " on server " + t.server }
func (t *adapterTool) Schema() json.RawMessage { return t.schema }
func (t *adapterTool) Risk() tools.Risk        { return t.risk }

// Preview is generic for now: it names the tool and shows nothing the server controls.
func (t *adapterTool) Preview(json.RawMessage) (string, error) {
	return "MCP tool " + t.name, nil
}

func (t *adapterTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	res, err := t.session.CallTool(ctx, t.remote, args)
	if err != nil {
		return tools.Result{}, err
	}
	text := resultText(res)
	if res.IsError {
		return tools.Result{}, errors.New(text)
	}
	return tools.Result{Content: text}, nil
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
