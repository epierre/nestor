package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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

	var maps *mcpconfig.Server
	for i := range file.Servers {
		if file.Servers[i].Name == "google-maps" {
			maps = &file.Servers[i]
			break
		}
	}
	if maps == nil {
		t.Fatal("config/mcp.yaml missing google-maps server")
	}
	if maps.URL != "https://mapstools.googleapis.com/mcp" {
		t.Errorf("google-maps URL = %q", maps.URL)
	}
	if !maps.Remote {
		t.Error("google-maps Remote = false, want true")
	}
	if maps.Credentials != "shared" {
		t.Errorf("google-maps Credentials = %q, want shared", maps.Credentials)
	}
	if maps.CredentialEnv != "GOOGLE_MAPS_API_KEY" {
		t.Errorf("google-maps CredentialEnv = %q, want GOOGLE_MAPS_API_KEY", maps.CredentialEnv)
	}
	if maps.CredentialHeader != "X-Goog-Api-Key" {
		t.Errorf("google-maps CredentialHeader = %q, want X-Goog-Api-Key", maps.CredentialHeader)
	}
	if maps.HealthInterval != 5*time.Minute {
		t.Errorf("google-maps HealthInterval = %s, want 5m", maps.HealthInterval)
	}
	if !mcpconfig.UsersInclude(maps.Users, 1) {
		t.Errorf("google-maps Users = %v, want wildcard or user 1", maps.Users)
	}
	wantTools := []string{"search_places", "lookup_weather", "compute_routes"}
	if len(maps.Tools) != len(wantTools) {
		t.Errorf("google-maps has %d tools, want %d", len(maps.Tools), len(wantTools))
	}
	for _, name := range wantTools {
		tool, ok := maps.Tools[name]
		if !ok {
			t.Errorf("google-maps missing tool %q", name)
			continue
		}
		if tool.Risk != tools.RiskRead {
			t.Errorf("google-maps tool %q risk = %q, want read", name, tool.Risk)
		}
	}

	byName := make(map[string]mcpconfig.Server, len(file.Servers))
	for _, s := range file.Servers {
		byName[s.Name] = s
	}
	wantWorkspace := []struct {
		name  string
		url   string
		tools map[string]tools.Risk
	}{
		{
			name: "google-contacts",
			url:  "https://people.googleapis.com/mcp/v1",
			tools: map[string]tools.Risk{
				"search_contacts":  tools.RiskRead,
				"get_user_profile": tools.RiskRead,
			},
		},
		{
			name: "google-calendar",
			url:  "https://calendarmcp.googleapis.com/mcp/v1",
			tools: map[string]tools.Risk{
				"list_calendars":   tools.RiskRead,
				"list_events":      tools.RiskRead,
				"get_event":        tools.RiskRead,
				"suggest_time":     tools.RiskRead,
				"create_event":     tools.RiskCritical,
				"update_event":     tools.RiskCritical,
				"delete_event":     tools.RiskCritical,
				"respond_to_event": tools.RiskCritical,
			},
		},
		{
			name: "google-docs",
			url:  "https://docsmcp.googleapis.com/mcp/v1",
			tools: map[string]tools.Risk{
				"read_doc":   tools.RiskRead,
				"update_doc": tools.RiskCritical,
			},
		},
		{
			name: "google-drive",
			url:  "https://drivemcp.googleapis.com/mcp/v1",
			tools: map[string]tools.Risk{
				"search_files":         tools.RiskRead,
				"read_file_content":    tools.RiskRead,
				"list_recent_files":    tools.RiskRead,
				"get_file_metadata":    tools.RiskRead,
				"get_file_permissions": tools.RiskRead,
				"create_file":          tools.RiskCritical,
				"copy_file":            tools.RiskCritical,
			},
		},
		{
			name: "google-workspace-search",
			url:  "https://workspacemcp.googleapis.com/mcp/v1",
			tools: map[string]tools.Risk{
				"search_corpus": tools.RiskRead,
			},
		},
	}

	for _, want := range wantWorkspace {
		s, ok := byName[want.name]
		if !ok {
			t.Errorf("config/mcp.yaml missing %q server", want.name)
			continue
		}
		if s.URL != want.url {
			t.Errorf("%s URL = %q, want %q", want.name, s.URL, want.url)
		}
		if !s.Remote {
			t.Errorf("%s Remote = false, want true", want.name)
		}
		if s.Credentials != "per_user" || s.CredentialKey != "google_oauth" || s.CredentialDelivery != "oauth2" {
			t.Errorf("%s credentials = (%q, %q, %q), want (per_user, google_oauth, oauth2)",
				want.name, s.Credentials, s.CredentialKey, s.CredentialDelivery)
		}
		if s.OAuthTokenURL != "https://oauth2.googleapis.com/token" ||
			s.OAuthClientIDEnv != "GOOGLE_OAUTH_CLIENT_ID" ||
			s.OAuthClientSecretEnv != "GOOGLE_OAUTH_CLIENT_SECRET" {
			t.Errorf("%s oauth settings = (%q, %q, %q)",
				want.name, s.OAuthTokenURL, s.OAuthClientIDEnv, s.OAuthClientSecretEnv)
		}
		if s.HealthInterval != 5*time.Minute {
			t.Errorf("%s HealthInterval = %s, want 5m", want.name, s.HealthInterval)
		}
		if len(s.Tools) != len(want.tools) {
			t.Errorf("%s has %d tools, want %d", want.name, len(s.Tools), len(want.tools))
		}
		for toolName, wantRisk := range want.tools {
			toolCfg, ok := s.Tools[toolName]
			if !ok {
				t.Errorf("%s missing tool %q", want.name, toolName)
				continue
			}
			if toolCfg.Risk != wantRisk {
				t.Errorf("%s tool %q risk = %q, want %q", want.name, toolName, toolCfg.Risk, wantRisk)
			}
		}
	}
}

func TestShippedMCPConfigStartsWithoutGoogleMapsKey(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "tz", Version: "0"}, nil)
	okHandler := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "17:05"}}}, nil
	}
	srv.AddTool(&sdk.Tool{Name: "get_time", Description: "Current time.", InputSchema: map[string]any{"type": "object"}}, okHandler)
	srv.AddTool(&sdk.Tool{Name: "convert_time", Description: "Convert time.", InputSchema: map[string]any{"type": "object"}}, okHandler)
	tzTS := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(tzTS.Close)

	d := testDeps(t, "../../config/mcp.yaml")
	d.Getenv = func(string) string { return "" }
	d.Dial = func(ctx context.Context, url string) (*mcp.Session, error) {
		if url == "http://mcp-timezone:8080/mcp" {
			return mcp.DialHTTP(ctx, tzTS.URL+"/mcp")
		}
		t.Errorf("unexpected Dial(%q)", url)
		return mcp.DialHTTP(ctx, url)
	}
	d.DialWithHeader = func(ctx context.Context, url, headerName, headerValue string) (*mcp.Session, error) {
		t.Errorf("DialWithHeader unexpectedly called for %q with header %q", url, headerName)
		return mcp.DialHTTPWithHeader(ctx, url, headerName, headerValue)
	}

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
	if _, err := d.Registry.Lookup("google-maps__search_places"); err == nil {
		t.Fatal("google-maps__search_places registered without GOOGLE_MAPS_API_KEY")
	}
}

func TestShippedMCPConfigStartsWithoutOptionalExternalKeys(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "tz", Version: "0"}, nil)
	okHandler := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "17:05"}}}, nil
	}
	srv.AddTool(&sdk.Tool{Name: "get_time", Description: "Current time.", InputSchema: map[string]any{"type": "object"}}, okHandler)
	srv.AddTool(&sdk.Tool{Name: "convert_time", Description: "Convert time.", InputSchema: map[string]any{"type": "object"}}, okHandler)
	tzTS := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(tzTS.Close)

	d := testDeps(t, "../../config/mcp.yaml")
	d.Config.CredentialsKey = nil
	d.Getenv = func(string) string { return "" }
	d.Dial = func(ctx context.Context, url string) (*mcp.Session, error) {
		if url == "http://mcp-timezone:8080/mcp" {
			return mcp.DialHTTP(ctx, tzTS.URL+"/mcp")
		}
		t.Errorf("unexpected Dial(%q)", url)
		return mcp.DialHTTP(ctx, url)
	}
	d.DialWithHeader = func(ctx context.Context, url, headerName, headerValue string) (*mcp.Session, error) {
		t.Errorf("DialWithHeader unexpectedly called for %q", url)
		return mcp.DialHTTPWithHeader(ctx, url, headerName, headerValue)
	}

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
	for _, absent := range []string{
		"google-maps__search_places",
		"google-contacts__search_contacts",
		"google-calendar__list_events",
		"google-docs__read_doc",
		"google-drive__search_files",
		"google-workspace-search__search_corpus",
	} {
		if _, err := d.Registry.Lookup(absent); err == nil {
			t.Errorf("%s registered without external credentials", absent)
		}
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

func TestStartMCPSharedCredentialCustomHeader(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "maps", Version: "0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "search_places", Description: "Search places.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "zurich"}}}, nil
		})
	var mu sync.Mutex
	var seenAuth, seenKey []string
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		seenKey = append(seenKey, r.Header.Get("X-Goog-Api-Key"))
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	d := testDeps(t, writeMCPConfig(t, `servers:
  - name: google-maps
    transport: http
    url: `+ts.URL+`/mcp
    credentials: shared
    credential_env: GOOGLE_MAPS_API_KEY
    credential_header: X-Goog-Api-Key
    users: ["*"]
    tools:
      search_places: { risk: read }
`))
	d.Getenv = func(k string) string {
		if k == "GOOGLE_MAPS_API_KEY" {
			return "test-maps-key"
		}
		return ""
	}
	d.DialWithHeader = mcp.DialHTTPWithHeader

	ctx, cancel := context.WithCancel(context.Background())
	wait, err := startMCP(ctx, d)
	if err != nil {
		t.Fatalf("startMCP: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		wait()
	})

	var tool tools.Tool
	deadline := time.Now().Add(3 * time.Second)
	for {
		var lookupErr error
		tool, lookupErr = d.Registry.Lookup("google-maps__search_places")
		if lookupErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("google-maps__search_places never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	res, err := tool.Execute(identity.WithUser(ctx, 1), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Content != "zurich" {
		t.Fatalf("Execute content = %q, want zurich", res.Content)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenKey) == 0 {
		t.Fatal("server saw zero HTTP requests")
	}
	for i := range seenKey {
		if seenAuth[i] != "" {
			t.Errorf("request %d Authorization = %q, want empty", i, seenAuth[i])
		}
		if seenKey[i] != "test-maps-key" {
			t.Errorf("request %d X-Goog-Api-Key = %q, want test-maps-key", i, seenKey[i])
		}
	}
}

func TestStartMCPSharedCredentialDefaultBearer(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "example", Version: "0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "ping_tool", Description: "Ping.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil
		})
	var mu sync.Mutex
	var seenAuth []string
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	d := testDeps(t, writeMCPConfig(t, `servers:
  - name: example
    transport: http
    url: `+ts.URL+`/mcp
    credentials: shared
    credential_env: EXAMPLE_TOKEN
    users: ["*"]
    tools:
      ping_tool: { risk: read }
`))
	d.Getenv = func(k string) string {
		if k == "EXAMPLE_TOKEN" {
			return "test-bearer-key"
		}
		return ""
	}
	d.DialWithHeader = mcp.DialHTTPWithHeader

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
		if _, err := d.Registry.Lookup("example__ping_tool"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("example__ping_tool never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenAuth) == 0 {
		t.Fatal("server saw zero HTTP requests")
	}
	for i, auth := range seenAuth {
		if auth != "Bearer test-bearer-key" {
			t.Errorf("request %d Authorization = %q, want Bearer test-bearer-key", i, auth)
		}
	}
}

func TestStartMCPSharedCredentialMissingEnvDisablesOnlyThatServer(t *testing.T) {
	tzSrv := sdk.NewServer(&sdk.Implementation{Name: "tz", Version: "0"}, nil)
	tzSrv.AddTool(&sdk.Tool{Name: "get_time", Description: "Current time.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "17:05"}}}, nil
		})
	tzTS := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return tzSrv }, nil))
	t.Cleanup(tzTS.Close)

	var mapsHits int
	var mu sync.Mutex
	mapsTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		mapsHits++
		mu.Unlock()
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(mapsTS.Close)

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	d := testDeps(t, writeMCPConfig(t, `servers:
  - name: google-maps
    transport: http
    url: `+mapsTS.URL+`/mcp
    credentials: shared
    credential_env: GOOGLE_MAPS_API_KEY
    credential_header: X-Goog-Api-Key
    users: ["*"]
    tools:
      search_places: { risk: read }
  - name: timezone
    transport: http
    url: `+tzTS.URL+`/mcp
    credentials: none
    users: ["*"]
    tools:
      get_time: { risk: read }
`))
	d.Getenv = func(string) string { return "   " }
	d.DialWithHeader = mcp.DialHTTPWithHeader

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

	if _, err := d.Registry.Lookup("google-maps__search_places"); err == nil {
		t.Fatal("google-maps__search_places registered despite missing env var")
	}
	mu.Lock()
	hits := mapsHits
	mu.Unlock()
	if hits != 0 {
		t.Errorf("maps server received %d requests, want 0", hits)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "google-maps") || !strings.Contains(logs, "GOOGLE_MAPS_API_KEY") {
		t.Errorf("warning log = %q, want server name and GOOGLE_MAPS_API_KEY", logs)
	}
}

type togglePinStore struct {
	mu       sync.Mutex
	approved bool
	seen     bool
}

func (p *togglePinStore) RecordToolSeen(_ context.Context, live store.ToolPin) (store.ToolPinState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	first := !p.seen
	p.seen = true
	var approved *store.ToolPin
	if p.approved {
		cp := live
		approved = &cp
	}
	return store.ToolPinState{Approved: approved, Seen: live, NewlySeen: first}, nil
}

func (p *togglePinStore) approve() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.approved = true
}

func (p *togglePinStore) wasSeen() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen
}

func TestStartMCPUsesConfiguredHealthInterval(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "tz", Version: "0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "get_time", Description: "Current time.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "17:05"}}}, nil
		})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	pins := &togglePinStore{}
	d := testDeps(t, writeMCPConfig(t, `servers:
  - name: timezone
    transport: http
    url: `+ts.URL+`/mcp
    credentials: none
    health_interval: 20ms
    users: ["*"]
    tools:
      get_time: { risk: read }
`))
	d.Pins = pins

	ctx, cancel := context.WithCancel(context.Background())
	wait, err := startMCP(ctx, d)
	if err != nil {
		t.Fatalf("startMCP: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		wait()
	})

	deadline := time.Now().Add(2 * time.Second)
	for !pins.wasSeen() {
		if time.Now().After(deadline) {
			t.Fatal("tool was never seen on connect")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := d.Registry.Lookup("timezone__get_time"); err == nil {
		t.Fatal("tool registered before approval")
	}

	pins.approve()
	approveDeadline := time.Now().Add(500 * time.Millisecond)
	for {
		if _, err := d.Registry.Lookup("timezone__get_time"); err == nil {
			break
		}
		if time.Now().After(approveDeadline) {
			t.Fatal("approved tool not picked up within 500ms; health_interval override not applied")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStartMCPOAuth2RegistersAndRefreshes(t *testing.T) {
	const (
		clientID     = "oauth-client-id"
		clientSecret = "oauth-client-secret-sentinel"
		refreshTok   = "refresh-tok-sentinel"
	)

	tokenTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.FormValue("refresh_token") != refreshTok {
			http.Error(w, "bad refresh", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh-access-tok","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenTS.Close)

	srv := sdk.NewServer(&sdk.Implementation{Name: "cal", Version: "0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "list_events", Description: "List events.", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			auth := ""
			if req.Extra != nil && req.Extra.Header != nil {
				auth = req.Extra.Header.Get("Authorization")
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: auth}}}, nil
		})
	mcpTS := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(mcpTS.Close)

	key := bytes.Repeat([]byte{3}, credentials.KeySize)
	sealer, err := credentials.NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	payload, err := mcp.MarshalOAuthCredential(refreshTok)
	if err != nil {
		t.Fatalf("MarshalOAuthCredential: %v", err)
	}
	ct, nonce, err := sealer.Seal(1, "google_oauth", payload)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	st := testutil.NewFakeStore()
	if err := st.SetCredential(identity.WithUser(context.Background(), 1), 1, "google_oauth", ct, nonce); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}

	d := testDeps(t, writeMCPConfig(t, `servers:
  - name: google-calendar
    transport: http
    url: `+mcpTS.URL+`/mcp
    credentials: per_user
    credential_key: google_oauth
    credential_delivery: oauth2
    oauth_token_url: `+tokenTS.URL+`
    oauth_client_id_env: GOOGLE_OAUTH_CLIENT_ID
    oauth_client_secret_env: GOOGLE_OAUTH_CLIENT_SECRET
    users: ["*"]
    tools:
      list_events: { risk: read }
`))
	d.Config.CredentialsKey = key
	d.Credentials = st
	d.Getenv = func(name string) string {
		switch name {
		case "GOOGLE_OAUTH_CLIENT_ID":
			return clientID
		case "GOOGLE_OAUTH_CLIENT_SECRET":
			return clientSecret
		default:
			return ""
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	wait, err := startMCP(ctx, d)
	if err != nil {
		t.Fatalf("startMCP: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		wait()
	})

	var tool tools.Tool
	deadline := time.Now().Add(3 * time.Second)
	for {
		var lookupErr error
		tool, lookupErr = d.Registry.Lookup("google-calendar__list_events")
		if lookupErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("google-calendar__list_events never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	res, err := tool.Execute(identity.WithUser(ctx, 1), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Content != "Bearer fresh-access-tok" {
		t.Errorf("Execute content = %q, want Bearer fresh-access-tok", res.Content)
	}
}

func TestStartMCPOAuth2MissingClientEnvDisablesOnlyOAuthServers(t *testing.T) {
	tzSrv := sdk.NewServer(&sdk.Implementation{Name: "tz", Version: "0"}, nil)
	tzSrv.AddTool(&sdk.Tool{Name: "get_time", Description: "Current time.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "17:05"}}}, nil
		})
	tzTS := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return tzSrv }, nil))
	t.Cleanup(tzTS.Close)

	var oauthHits int
	var mu sync.Mutex
	oauthTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		oauthHits++
		mu.Unlock()
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(oauthTS.Close)

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	d := testDeps(t, writeMCPConfig(t, `servers:
  - name: google-calendar
    transport: http
    url: `+oauthTS.URL+`/mcp
    credentials: per_user
    credential_key: google_oauth
    credential_delivery: oauth2
    oauth_token_url: `+oauthTS.URL+`/token
    oauth_client_id_env: GOOGLE_OAUTH_CLIENT_ID
    oauth_client_secret_env: GOOGLE_OAUTH_CLIENT_SECRET
    users: ["*"]
    tools:
      list_events: { risk: read }
  - name: timezone
    transport: http
    url: `+tzTS.URL+`/mcp
    credentials: none
    users: ["*"]
    tools:
      get_time: { risk: read }
`))
	// CredentialsKey is intentionally nil and OAuth env vars are empty.
	d.Getenv = func(string) string { return "" }

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

	if _, err := d.Registry.Lookup("google-calendar__list_events"); err == nil {
		t.Fatal("google-calendar__list_events registered despite missing OAuth env vars")
	}
	mu.Lock()
	hits := oauthHits
	mu.Unlock()
	if hits != 0 {
		t.Errorf("oauth server received %d requests, want 0", hits)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "google-calendar") || !strings.Contains(logs, "GOOGLE_OAUTH_CLIENT_ID") || !strings.Contains(logs, "GOOGLE_OAUTH_CLIENT_SECRET") {
		t.Errorf("warning log = %q, want server name and missing env var names", logs)
	}
}

func TestStartMCPOAuth2WithEnvRequiresCredentialsKey(t *testing.T) {
	d := testDeps(t, writeMCPConfig(t, `servers:
  - name: google-calendar
    transport: http
    url: https://calendarmcp.googleapis.com/mcp/v1
    remote: true
    credentials: per_user
    credential_key: google_oauth
    credential_delivery: oauth2
    oauth_token_url: https://oauth2.googleapis.com/token
    oauth_client_id_env: GOOGLE_OAUTH_CLIENT_ID
    oauth_client_secret_env: GOOGLE_OAUTH_CLIENT_SECRET
    users: ["*"]
    tools:
      list_events: { risk: read }
`))
	d.Getenv = func(name string) string {
		switch name {
		case "GOOGLE_OAUTH_CLIENT_ID":
			return "id"
		case "GOOGLE_OAUTH_CLIENT_SECRET":
			return "secret"
		default:
			return ""
		}
	}
	_, err := startMCP(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "CREDENTIALS_KEY") {
		t.Fatalf("startMCP error = %v, want CREDENTIALS_KEY error", err)
	}
}
