package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp/config"
	"github.com/epierre/nestor/internal/store"
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

// PinStore records the description and schema an MCP tool was first trusted with.
// postgres.Store implements it.
type PinStore interface {
	// PinTool stores pin unless the tool is already pinned, and returns the stored pin.
	PinTool(ctx context.Context, pin store.ToolPin) (store.ToolPin, error)
}

// connector gives the session a call runs on.
type connector interface {
	sessionFor(ctx context.Context) (*Session, error)
}

// credentialChecker is implemented by a connector whose server needs a per-user credential.
type credentialChecker interface {
	HasCredential(ctx context.Context, user identity.UserID) bool
}

// fixedSession serves every call on one session: a server without per-user credentials.
type fixedSession struct{ s *Session }

func (f fixedSession) sessionFor(context.Context) (*Session, error) { return f.s, nil }

// RegisterTools adds every tool allowlisted for server to reg, named <server>__<tool>, with every
// call running on s. Risk comes from the configuration; annotations the server sends are ignored.
// A tool that the server does not offer, or whose name is already registered, is an error.
func RegisterTools(ctx context.Context, reg *tools.Registry, pins PinStore, server config.Server, s *Session) error {
	return registerTools(ctx, reg, pins, server, s, fixedSession{s})
}

// RegisterUserTools is RegisterTools for a per_user server with header delivery: each call runs on
// the session of the user in its context. The tool list is read as the user in ctx, who must have
// a stored credential.
func RegisterUserTools(ctx context.Context, reg *tools.Registry, pins PinStore, server config.Server, users *UserConnector) error {
	listing, err := users.sessionFor(ctx)
	if err != nil {
		return fmt.Errorf("server %q: %w", server.Name, err)
	}
	return registerTools(ctx, reg, pins, server, listing, users)
}

// registerTools pins each allowlisted tool on first sight and registers it with the pinned
// description and schema. A tool whose live description or schema no longer matches its pin is
// left out and logged; the other tools of the server are still registered.
func registerTools(ctx context.Context, reg *tools.Registry, pins PinStore, server config.Server, listing *Session, conn connector) error {
	offered, err := listing.ListTools(ctx)
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
		live := store.ToolPin{
			Server:      server.Name,
			Tool:        name,
			Description: remote.Description,
			SchemaJSON:  schema,
			Hash:        pinHash(remote.Description, schema),
		}
		pin, err := pins.PinTool(ctx, live)
		if err != nil {
			return fmt.Errorf("server %q tool %q: pin: %w", server.Name, name, err)
		}
		if pin.Hash != live.Hash {
			slog.ErrorContext(ctx, "mcp tool pin mismatch: tool disabled until re-approved",
				"server", server.Name, "tool", name, "pinned_hash", pin.Hash, "live_hash", live.Hash)
			continue
		}
		adapted := &adapterTool{
			server:      server.Name,
			remote:      name,
			name:        server.Name + namespaceSep + name,
			risk:        cfg.Risk,
			description: pin.Description,
			schema:      pin.SchemaJSON,
			conn:        conn,
			timeout:     orDefault(server.Timeout, defaultTimeout),
			maxBytes:    orDefault(server.MaxResultBytes, defaultMaxResultBytes),
			users:       server.Users,
		}
		if err := reg.Register(adapted); err != nil {
			return fmt.Errorf("register %q: %w", adapted.name, err)
		}
	}
	return nil
}

// adapterTool exposes one allowlisted MCP tool as a registry tool.
type adapterTool struct {
	server string
	remote string // the tool's name on the server
	name   string // the registered name, <server>__<remote>
	risk   tools.Risk
	// description and schema are the pinned ones; the live text is never sent to the model.
	description string
	schema      json.RawMessage
	conn        connector
	timeout     time.Duration
	maxBytes    int
	users       []string // the server's users list from mcp.yaml: user IDs, or "*"
}

// EnabledFor reports whether the server is enabled for user by the users list in mcp.yaml and, for
// a per_user server, whether user has a stored credential for it.
func (t *adapterTool) EnabledFor(ctx context.Context, user identity.UserID) bool {
	if !t.listed(user) {
		return false
	}
	if checker, ok := t.conn.(credentialChecker); ok {
		return checker.HasCredential(ctx, user)
	}
	return true
}

func (t *adapterTool) listed(user identity.UserID) bool {
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
func (t *adapterTool) Description() string     { return t.description }
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

	session, err := t.conn.sessionFor(callCtx)
	if err != nil {
		return tools.Result{}, fmt.Errorf("mcp tool %s: %w", t.name, err)
	}
	res, err := session.CallTool(callCtx, t.remote, args)
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

// pinHash identifies a tool's description and schema together.
func pinHash(description string, schema []byte) string {
	h := sha256.New()
	h.Write([]byte(description))
	h.Write([]byte{0})
	h.Write(schema)
	return hex.EncodeToString(h.Sum(nil))
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
