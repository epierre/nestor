package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp/config"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
	"github.com/epierre/nestor/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer serves tools over an in-memory transport. The annotations are what the server
// claims about each tool; the adapter must not rely on them for risk.
func fakeServer(t *testing.T) *Session {
	t.Helper()
	ctx := context.Background()

	srv := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "0.0.1"}, nil)
	object := map[string]any{"type": "object"}
	ok := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
	}
	srv.AddTool(&sdk.Tool{Name: "get_tasks", InputSchema: object,
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, ok)
	// The server marks this destructive tool read-only. Its config risk is critical.
	srv.AddTool(&sdk.Tool{Name: "delete_task", InputSchema: object,
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, ok)
	srv.AddTool(&sdk.Tool{Name: "delete_project", InputSchema: object}, ok)
	srv.AddTool(&sdk.Tool{Name: "slow", InputSchema: object},
		func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	srv.AddTool(&sdk.Tool{Name: "big", InputSchema: object},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{
				&sdk.TextContent{Text: strings.Repeat("é", 1<<20)},
			}}, nil
		})

	serverT, clientT := sdk.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("fake server connect: %v", err)
	}
	s, err := Connect(ctx, clientT)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func weatherServer(tools map[string]config.Tool) config.Server {
	return config.Server{Name: "weather", Transport: "http", Tools: tools}
}

// executeOne registers the one tool in server, runs it and returns its result.
func executeOne(t *testing.T, server config.Server, name string) (tools.Result, error) {
	t.Helper()
	reg := tools.NewRegistry()
	if err := RegisterTools(context.Background(), reg, server, fakeServer(t)); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, err := reg.Lookup(server.Name + "__" + name)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	return tool.Execute(context.Background(), json.RawMessage(`{}`))
}

// fakeBuiltin is a built-in tool used to provoke a name clash.
type fakeBuiltin struct{ name string }

func (f fakeBuiltin) Name() string                            { return f.name }
func (f fakeBuiltin) Description() string                     { return "built-in" }
func (f fakeBuiltin) Schema() json.RawMessage                 { return json.RawMessage(`{"type":"object"}`) }
func (f fakeBuiltin) Risk() tools.Risk                        { return tools.RiskRead }
func (f fakeBuiltin) Preview(json.RawMessage) (string, error) { return "", nil }
func (f fakeBuiltin) Execute(context.Context, json.RawMessage) (tools.Result, error) {
	return tools.Result{Content: "built-in"}, nil
}

func TestRegisterAllowlistedTools(t *testing.T) {
	ctx := context.Background()
	server := weatherServer(map[string]config.Tool{
		"get_tasks":   {Risk: tools.RiskRead},
		"delete_task": {Risk: tools.RiskCritical},
	})

	tests := []struct {
		name    string
		server  config.Server
		want    []string // registered names, in any order
		wantErr string
	}{
		{
			name:   "only allowlisted tools registered",
			server: server,
			want:   []string{"weather__get_tasks", "weather__delete_task"},
		},
		{
			name:    "allowlisted tool missing on server",
			server:  weatherServer(map[string]config.Tool{"get_forecast": {Risk: tools.RiskRead}}),
			wantErr: `server "weather" does not offer allowlisted tool "get_forecast"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := tools.NewRegistry()
			err := RegisterTools(ctx, reg, tt.server, fakeServer(t))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("RegisterTools error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RegisterTools: %v", err)
			}
			for _, name := range tt.want {
				if _, err := reg.Lookup(name); err != nil {
					t.Errorf("Lookup(%q): %v", name, err)
				}
			}
			if _, err := reg.Lookup("weather__delete_project"); !errors.Is(err, tools.ErrNotFound) {
				t.Errorf("non-allowlisted tool registered: Lookup err = %v", err)
			}
		})
	}
}

func TestRiskFromConfigIgnoresAnnotation(t *testing.T) {
	reg := tools.NewRegistry()
	server := weatherServer(map[string]config.Tool{"delete_task": {Risk: tools.RiskCritical}})
	if err := RegisterTools(context.Background(), reg, server, fakeServer(t)); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	tool, err := reg.Lookup("weather__delete_task")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got := tool.Risk(); got != tools.RiskCritical {
		t.Errorf("Risk() = %q, want %q (server annotated the tool read-only)", got, tools.RiskCritical)
	}
}

func TestNameClashWithBuiltin(t *testing.T) {
	reg := tools.NewRegistry()
	builtin := fakeBuiltin{name: "weather__get_tasks"}
	if err := reg.Register(builtin); err != nil {
		t.Fatalf("Register built-in: %v", err)
	}
	server := weatherServer(map[string]config.Tool{"get_tasks": {Risk: tools.RiskRead}})

	err := RegisterTools(context.Background(), reg, server, fakeServer(t))
	if !errors.Is(err, tools.ErrAlreadyExists) {
		t.Fatalf("RegisterTools error = %v, want ErrAlreadyExists", err)
	}
	got, err := reg.Lookup("weather__get_tasks")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Description() != "built-in" {
		t.Errorf("built-in replaced: description = %q", got.Description())
	}
}

func TestExecuteForwardsCall(t *testing.T) {
	reg := tools.NewRegistry()
	server := weatherServer(map[string]config.Tool{"get_tasks": {Risk: tools.RiskRead}})
	if err := RegisterTools(context.Background(), reg, server, fakeServer(t)); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, err := reg.Lookup("weather__get_tasks")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Content != "ok" {
		t.Errorf("Execute content = %q, want %q", res.Content, "ok")
	}
}

func TestExecuteTimeoutYieldsError(t *testing.T) {
	server := weatherServer(map[string]config.Tool{"slow": {Risk: tools.RiskRead}})
	server.Timeout = 50 * time.Millisecond

	_, err := executeOne(t, server, "slow")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Execute error = %v, want a timeout error", err)
	}
}

func TestExecuteTruncatesOversizedResult(t *testing.T) {
	server := weatherServer(map[string]config.Tool{"big": {Risk: tools.RiskRead}})
	server.MaxResultBytes = 1024

	res, err := executeOne(t, server, "big")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "[truncated") {
		t.Errorf("result has no truncation marker: %.80q...", res.Content)
	}
	if n := len(res.Content); n > 1024+100 {
		t.Errorf("result is %d bytes, want close to the 1024 byte cap", n)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true for an oversized result")
	}
	if !utf8.ValidString(res.Content) {
		t.Error("truncation split a multi-byte character")
	}
}

func TestCriticalToolCreatesPendingAction(t *testing.T) {
	ctx := identity.WithUser(context.Background(), 1)
	st := testutil.NewFakeStore()
	reg := tools.NewRegistry()
	server := weatherServer(map[string]config.Tool{"delete_task": {Risk: tools.RiskCritical}})
	if err := RegisterTools(ctx, reg, server, fakeServer(t)); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, err := reg.Lookup("weather__delete_task")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	gate := approval.New(st, reg, time.Hour)
	args := json.RawMessage(`{"id":"42","note":"secret-sentinel"}`)
	action, err := gate.Propose(ctx, 1, tool, args)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if action.State != store.PendingProposed {
		t.Errorf("state = %q, want %q", action.State, store.PendingProposed)
	}
	if action.ToolName != "weather__delete_task" {
		t.Errorf("tool name = %q, want weather__delete_task", action.ToolName)
	}
	if strings.Contains(action.Preview, "secret-sentinel") {
		t.Errorf("preview shows server-controlled arguments: %q", action.Preview)
	}
}

func TestEnabledForUsers(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		users []string
		user  identity.UserID
		want  bool
	}{
		{"wildcard enables any user", []string{"*"}, 2, true},
		{"listed user enabled", []string{"1"}, 1, true},
		{"unlisted user not enabled", []string{"1"}, 2, false},
		{"no users enables nobody", nil, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := tools.NewRegistry()
			server := weatherServer(map[string]config.Tool{"get_tasks": {Risk: tools.RiskRead}})
			server.Users = tt.users
			if err := RegisterTools(ctx, reg, server, fakeServer(t)); err != nil {
				t.Fatalf("RegisterTools: %v", err)
			}
			tool, err := reg.Lookup("weather__get_tasks")
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			scoped, ok := tool.(tools.UserScoped)
			if !ok {
				t.Fatal("adapted tool does not implement tools.UserScoped")
			}
			if got := scoped.EnabledFor(ctx, tt.user); got != tt.want {
				t.Errorf("EnabledFor(%d) = %v, want %v", tt.user, got, tt.want)
			}
		})
	}
}

// tokenServer serves a "whoami" tool over streamable HTTP. The tool returns the Authorization
// header of the request that carried the call, so a test sees which token the server received.
func tokenServer(t *testing.T) string {
	t.Helper()
	srv := sdk.NewServer(&sdk.Implementation{Name: "tokens", Version: "0.0.1"}, nil)
	srv.AddTool(&sdk.Tool{Name: "whoami", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			got := ""
			if req.Extra != nil {
				got = req.Extra.Header.Get("Authorization")
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: got}}}, nil
		})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

func headerServerConfig(url string) config.Server {
	return config.Server{
		Name:               "todoist",
		Transport:          "http",
		URL:                url,
		Credentials:        "per_user",
		CredentialKey:      "todoist_token",
		CredentialDelivery: "header",
		Users:              []string{"1", "2"},
		Tools:              map[string]config.Tool{"whoami": {Risk: tools.RiskRead}},
	}
}

func testSealer(t *testing.T) *credentials.Sealer {
	t.Helper()
	s, err := credentials.NewSealer(bytes.Repeat([]byte{3}, credentials.KeySize))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return s
}

// storeToken encrypts token for user and stores it as the todoist credential.
func storeToken(t *testing.T, st *testutil.FakeStore, sealer *credentials.Sealer, user identity.UserID, token string) {
	t.Helper()
	ct, nonce, err := sealer.Seal(user, "todoist_token", []byte(token))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := st.SetCredential(identity.WithUser(context.Background(), user), user, "todoist_token", ct, nonce); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
}

func whoami(t *testing.T, tool tools.Tool, user identity.UserID) string {
	t.Helper()
	res, err := tool.Execute(identity.WithUser(context.Background(), user), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute as user %d: %v", user, err)
	}
	return res.Content
}

func TestEachUserServedWithOwnToken(t *testing.T) {
	st := testutil.NewFakeStore()
	sealer := testSealer(t)
	storeToken(t, st, sealer, 1, "test-token-1")
	storeToken(t, st, sealer, 2, "test-token-2")

	server := headerServerConfig(tokenServer(t))
	conn := NewUserConnector(server, st, sealer)
	t.Cleanup(conn.Close)
	reg := tools.NewRegistry()
	if err := RegisterUserTools(identity.WithUser(context.Background(), 1), reg, server, conn); err != nil {
		t.Fatalf("RegisterUserTools: %v", err)
	}
	tool, err := reg.Lookup("todoist__whoami")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	for _, step := range []struct {
		user identity.UserID
		want string
	}{
		{1, "Bearer test-token-1"},
		{2, "Bearer test-token-2"},
		{1, "Bearer test-token-1"}, // back to user 1: no session is shared between users
	} {
		if got := whoami(t, tool, step.user); got != step.want {
			t.Errorf("user %d: server saw %q, want %q", step.user, got, step.want)
		}
	}

	// A new token for user 1 is used from the next call on.
	storeToken(t, st, sealer, 1, "test-token-1b")
	if got := whoami(t, tool, 1); got != "Bearer test-token-1b" {
		t.Errorf("after credential update: server saw %q, want the new token", got)
	}
}

func TestMissingCredentialHidesTools(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	st := testutil.NewFakeStore()
	sealer := testSealer(t)
	storeToken(t, st, sealer, 1, "test-token-1") // user 2 has none

	server := headerServerConfig(tokenServer(t))
	conn := NewUserConnector(server, st, sealer)
	t.Cleanup(conn.Close)
	reg := tools.NewRegistry()
	if err := RegisterUserTools(identity.WithUser(context.Background(), 1), reg, server, conn); err != nil {
		t.Fatalf("RegisterUserTools: %v", err)
	}

	offered := func(user identity.UserID) bool {
		for _, tool := range reg.ToolsFor(identity.WithUser(context.Background(), user), user) {
			if tool.Name == "todoist__whoami" {
				return true
			}
		}
		return false
	}
	if offered(2) {
		t.Error("tools offered to user 2, who has no stored credential")
	}
	if !offered(1) {
		t.Error("tools not offered to user 1, who has a stored credential")
	}

	tool, err := reg.Lookup("todoist__whoami")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, err := tool.Execute(identity.WithUser(context.Background(), 2), json.RawMessage(`{}`)); err == nil {
		t.Error("Execute for user 2 without a credential succeeded")
	}

	if !strings.Contains(logs.String(), "missing credential") || !strings.Contains(logs.String(), "todoist_token") {
		t.Errorf("missing credential not logged with its key: %s", logs.String())
	}
	if strings.Contains(logs.String(), "test-token") {
		t.Errorf("log contains a secret value: %s", logs.String())
	}
}
