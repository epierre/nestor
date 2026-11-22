package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/epierre/nestor/internal/mcp/config"
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
	if !utf8.ValidString(res.Content) {
		t.Error("truncation split a multi-byte character")
	}
}
