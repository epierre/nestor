package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp"
	mcpconfig "github.com/epierre/nestor/internal/mcp/config"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
	"github.com/epierre/nestor/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// approvingPins approves every tool version on sight. Approval itself is tested in internal/mcp.
type approvingPins struct{}

func (approvingPins) RecordToolSeen(_ context.Context, live store.ToolPin) (store.ToolPinState, error) {
	approved := live
	return store.ToolPinState{Approved: &approved, Seen: live, NewlySeen: true}, nil
}

func writeMCPConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write mcp.yaml: %v", err)
	}
	return path
}

func testDeps(t *testing.T, path string) mcpDeps {
	t.Helper()
	return mcpDeps{
		Config:      config.Config{MCPConfigPath: path},
		Registry:    tools.NewRegistry(),
		Pins:        approvingPins{},
		Credentials: testutil.NewFakeStore(),
		Users:       []identity.UserID{1},
		Dial:        mcp.DialHTTP,
	}
}

func TestStartMCPWithoutConfigDoesNothing(t *testing.T) {
	wait, err := startMCP(context.Background(), testDeps(t, ""))
	if err != nil {
		t.Fatalf("startMCP: %v", err)
	}
	wait()
}

func TestStartMCPRefusesUnsupportedServers(t *testing.T) {
	server := func(fields string) string {
		return "servers:\n  - name: odd\n" + fields + "    users: [\"*\"]\n    tools:\n      get_time: { risk: read }\n"
	}
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"stdio transport", server("    transport: stdio\n    credentials: none\n"), `server "odd": stdio transport is not supported yet`},
		{"shared credentials", server("    transport: http\n    url: http://mcp-odd:8080/mcp\n    credentials: shared\n"),
			`server "odd": shared credentials are not supported yet`},
		{"container delivery", server("    transport: http\n    url: http://mcp-odd-{user}:8080/mcp\n    credentials: per_user\n    credential_key: odd_token\n    credential_delivery: container\n"),
			`server "odd": container credential delivery is not supported yet`},
		{"header delivery without CREDENTIALS_KEY", server("    transport: http\n    url: http://mcp-odd:8080/mcp\n    credentials: per_user\n    credential_key: odd_token\n    credential_delivery: header\n"),
			"CREDENTIALS_KEY"},
		{"invalid file", "servers:\n  - name: odd\n    transport: http\n    url: http://mcp-odd:8080/mcp\n    credentials: none\n    users: [\"*\"]\n    tools:\n      get_time: {}\n",
			`tool "get_time" has no risk`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := testDeps(t, writeMCPConfig(t, tt.yaml))
			_, err := startMCP(context.Background(), d)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("startMCP error = %v, want containing %q", err, tt.wantErr)
			}
			if len(d.Registry.Tools()) != 0 {
				t.Error("tools registered although startup was refused")
			}
		})
	}
}

func TestStartMCPRegistersApprovedTools(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "tz", Version: "0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "get_time", Description: "Current time.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "17:05"}}}, nil
		})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	d := testDeps(t, writeMCPConfig(t, `servers:
  - name: timezone
    transport: http
    url: `+ts.URL+`/mcp
    credentials: none
    users: ["*"]
    tools:
      get_time: { risk: read }
`))
	ctx, cancel := context.WithCancel(context.Background())
	wait, err := startMCP(ctx, d)
	if err != nil {
		t.Fatalf("startMCP: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		wait()
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := d.Registry.Lookup("timezone__get_time"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timezone__get_time never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestShippedMCPConfigIsValid(t *testing.T) {
	f, err := os.Open("../../config/mcp.yaml")
	if err != nil {
		t.Fatalf("open config/mcp.yaml: %v", err)
	}
	defer f.Close()
	file, err := mcpconfig.Load(f, []identity.UserID{1})
	if err != nil {
		t.Fatalf("config/mcp.yaml: %v", err)
	}
	if err := supported(file); err != nil {
		t.Errorf("config/mcp.yaml uses a server type startup refuses: %v", err)
	}
}

func TestStartMCPHeaderServerListsAsUserWithCredential(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "tasks", Version: "0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "get_tasks", Description: "List tasks.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "none"}}}, nil
		})
	var seen []string
	var mu sync.Mutex
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	key := bytes.Repeat([]byte{4}, credentials.KeySize)
	sealer, err := credentials.NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	st := testutil.NewFakeStore()
	// User 1 has no credential; user 2 has one, so the tool list is read as user 2.
	ct, nonce, err := sealer.Seal(2, "tasks_token", []byte("test-token-2"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := st.SetCredential(identity.WithUser(context.Background(), 2), 2, "tasks_token", ct, nonce); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}

	d := testDeps(t, writeMCPConfig(t, `servers:
  - name: tasks
    transport: http
    url: `+ts.URL+`/mcp
    credentials: per_user
    credential_key: tasks_token
    credential_delivery: header
    users: ["*"]
    tools:
      get_tasks: { risk: read }
`))
	d.Config.CredentialsKey = key
	d.Credentials = st
	d.Users = []identity.UserID{1, 2}

	ctx, cancel := context.WithCancel(context.Background())
	wait, err := startMCP(ctx, d)
	if err != nil {
		t.Fatalf("startMCP: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		wait()
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := d.Registry.Lookup("tasks__get_tasks"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tasks__get_tasks never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range seen {
		if h != "Bearer test-token-2" {
			t.Errorf("server saw Authorization %q, want only user 2's token", h)
		}
	}
}
