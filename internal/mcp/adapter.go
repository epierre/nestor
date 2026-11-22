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

// PinStore records the versions of each MCP tool seen on connect and the one an administrator
// approved. postgres.Store implements it.
type PinStore interface {
	RecordToolSeen(ctx context.Context, live store.ToolPin) (store.ToolPinState, error)
}

// Alerter tells the host administrator about MCP tools that wait for approval.
type Alerter interface {
	AlertAdmin(ctx context.Context, text string) error
}

// UserNotifier sends a message to one user. telegram.Channel implements it.
type UserNotifier interface {
	NotifyUser(ctx context.Context, user identity.UserID, text string) error
}

// AdminAlerts sends MCP alerts to the host administrator, the user in ADMIN_USER_ID. The role is
// for alerts only and grants no access to other users' data.
type AdminAlerts struct {
	Notifier UserNotifier
	Admin    identity.UserID
}

// AlertAdmin implements Alerter.
func (a AdminAlerts) AlertAdmin(ctx context.Context, text string) error {
	return a.Notifier.NotifyUser(ctx, a.Admin, text)
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

// Registrar adds the allowlisted tools of MCP servers to Registry, named <server>__<tool>. A tool
// is registered only while the version it is served with is the one an administrator approved;
// the model is sent the approved description and schema. Risk comes from the configuration, and
// annotations the server sends are ignored. Alerts may be nil, in which case alerts are only
// logged.
type Registrar struct {
	Registry *tools.Registry
	Pins     PinStore
	Alerts   Alerter
}

// Register adds server's approved tools, with every call running on s. A tool that the server
// does not offer, or whose name is already registered, is an error.
func (r *Registrar) Register(ctx context.Context, server config.Server, s *Session) error {
	_, err := r.register(ctx, server, s, fixedSession{s})
	return err
}

// RegisterUser is Register for a per_user server with header delivery: each call runs on the
// session of the user in its context. The tool list is read as the user in ctx, who must have a
// stored credential.
func (r *Registrar) RegisterUser(ctx context.Context, server config.Server, users *UserConnector) error {
	listing, err := users.sessionFor(ctx)
	if err != nil {
		return fmt.Errorf("server %q: %w", server.Name, err)
	}
	_, err = r.register(ctx, server, listing, users)
	return err
}

// register adds server's approved tools and returns the names it registered, also on error, so
// a caller can remove them again.
func (r *Registrar) register(ctx context.Context, server config.Server, listing *Session, conn connector) (names []string, err error) {
	offered, err := listing.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("server %q: %w", server.Name, err)
	}
	byName := make(map[string]*sdk.Tool, len(offered))
	for _, t := range offered {
		byName[t.Name] = t
	}

	for name, cfg := range server.Tools {
		remote, ok := byName[name]
		if !ok {
			return names, fmt.Errorf("server %q does not offer allowlisted tool %q", server.Name, name)
		}
		schema, err := json.Marshal(remote.InputSchema)
		if err != nil {
			return names, fmt.Errorf("server %q tool %q: marshal input schema: %w", server.Name, name, err)
		}
		live := store.ToolPin{
			Server:      server.Name,
			Tool:        name,
			Description: remote.Description,
			SchemaJSON:  schema,
			Hash:        pinHash(remote.Description, schema),
		}
		state, err := r.Pins.RecordToolSeen(ctx, live)
		if err != nil {
			return names, fmt.Errorf("server %q tool %q: record pin: %w", server.Name, name, err)
		}
		pin := state.Approved
		switch {
		case pin == nil:
			slog.WarnContext(ctx, "mcp tool waiting for approval", "server", server.Name, "tool", name)
			if state.NewlySeen {
				r.alert(ctx, newToolText(live))
			}
			continue
		case pin.Hash != live.Hash:
			slog.ErrorContext(ctx, "mcp tool pin mismatch: tool disabled until re-approved",
				"server", server.Name, "tool", name, "pinned_hash", pin.Hash, "live_hash", live.Hash)
			if state.NewlySeen {
				r.alert(ctx, changedToolText(*pin, live))
			}
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
		if err := r.Registry.Register(adapted); err != nil {
			return names, fmt.Errorf("register %q: %w", adapted.name, err)
		}
		names = append(names, adapted.name)
	}
	return names, nil
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

func (r *Registrar) alert(ctx context.Context, text string) {
	if r.Alerts == nil {
		return
	}
	if err := r.Alerts.AlertAdmin(ctx, text); err != nil {
		slog.ErrorContext(ctx, "mcp admin alert not sent", "error", err)
	}
}

// newToolText asks the administrator to review a tool seen for the first time.
func newToolText(live store.ToolPin) string {
	return fmt.Sprintf("New MCP tool %s%s%s is waiting for approval.\n\nDescription:\n%s\n\n%s",
		live.Server, namespaceSep, live.Tool, live.Description, approveHint(live))
}

// changedToolText shows the administrator what changed in a tool that is now disabled.
func changedToolText(approved, live store.ToolPin) string {
	text := fmt.Sprintf("MCP tool %s%s%s changed and is disabled until re-approved.\n\nOld description:\n%s\n\nNew description:\n%s\n\n",
		live.Server, namespaceSep, live.Tool, approved.Description, live.Description)
	if approved.Description == live.Description {
		text += "Only the input schema changed.\n\n"
	}
	return text + approveHint(live)
}

func approveHint(live store.ToolPin) string {
	return fmt.Sprintf("Approve after review with: nestor mcp approve %s %s", live.Server, live.Tool)
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
