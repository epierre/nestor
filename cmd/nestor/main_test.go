package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/agent"
	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/prompt"
	"github.com/epierre/nestor/internal/store/postgres"
	"github.com/epierre/nestor/internal/testutil"
	"github.com/epierre/nestor/internal/tools"
)

// fakeNotifier records the messages sent to chats.
type fakeNotifier struct {
	chats []int64
	texts []string
}

func (f *fakeNotifier) Notify(_ context.Context, chatID int64, text string) error {
	f.chats = append(f.chats, chatID)
	f.texts = append(f.texts, text)
	return nil
}

func TestMissingBotTokenExitsNonZero(t *testing.T) {
	const secret = "api-key-sentinel"
	if os.Getenv("NESTOR_TEST_RUN_MAIN") == "1" {
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMissingBotTokenExitsNonZero$")
	cmd.Env = []string{
		"NESTOR_TEST_RUN_MAIN=1",
		"ALLOWED_TELEGRAM_USERS=123:Owner",
		"DATABASE_URL=postgres://nestor:test-password@127.0.0.1:1/nestor",
		"LLM_BASE_URL=http://127.0.0.1:1/v1",
		"LLM_API_KEY=" + secret,
	}

	out, err := cmd.CombinedOutput()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("got error %v, want exit status 1; output: %s", err, out)
	}
	if !strings.Contains(string(out), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("output %s does not name the variable", out)
	}
	if strings.Contains(string(out), secret) || strings.Contains(string(out), "test-password") {
		t.Errorf("output contains a secret: %s", out)
	}
}

func TestAgentOptionsComeFromConfig(t *testing.T) {
	reg := tools.NewRegistry()
	loc, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	const wantPrompt = "You are Nestor."
	got := agentOptions(config.Config{LLMModel: "strong", HistoryLimit: 12, MaxLoopIterations: 3}, wantPrompt, reg, loc)

	if got.Model != "strong" || got.SystemPrompt != wantPrompt || got.HistoryLimit != 12 ||
		got.MaxIterations != 3 || got.Tools != reg || got.Location != loc {
		t.Errorf("got %+v, want strong/12/3 with prompt, registry and Europe/Zurich", got)
	}
}

func TestAllowedIdentities(t *testing.T) {
	got := allowedIdentities([]config.TelegramUser{
		{ID: 123, DisplayName: "Owner"},
		{ID: 456, DisplayName: "Ana"},
	})

	want := []postgres.AllowedIdentity{
		{ExternalID: "123", DisplayName: "Owner"},
		{ExternalID: "456", DisplayName: "Ana"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestBuiltInToolsAlwaysRegistered(t *testing.T) {
	st := testutil.NewFakeStore()
	reg := toolRegistry(false, &fakeNotifier{}, st, &testutil.FakeModel{}, "web-search", time.UTC)
	var got []string
	for _, def := range reg.Tools() {
		got = append(got, def.Name)
	}
	want := []string{"task_add", "task_list", "task_complete", "task_delete", "reminder_add", "reminder_list", "reminder_cancel", "web_search"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tools %v, want %v", got, want)
	}
	ws, err := reg.Lookup("web_search")
	if err != nil {
		t.Fatalf("Lookup web_search: %v", err)
	}
	if ws.Risk() != tools.RiskRead {
		t.Errorf("web_search risk %v, want read", ws.Risk())
	}
}

func TestDemoToolsOfferedOnlyWhenEnabled(t *testing.T) {
	names := func(reg *tools.Registry) []string {
		var got []string
		for _, def := range reg.Tools() {
			got = append(got, def.Name)
		}
		return got
	}

	st := testutil.NewFakeStore()
	on := toolRegistry(true, &fakeNotifier{}, st, &testutil.FakeModel{}, "web-search", time.UTC)
	if got := names(on); !slices.Contains(got, "current_time") || !slices.Contains(got, "notify_self") {
		t.Errorf("enabled: tools %v, want current_time and notify_self", got)
	}
	notify, err := on.Lookup("notify_self")
	if err != nil {
		t.Fatalf("Lookup notify_self: %v", err)
	}
	if notify.Risk() != tools.RiskCritical {
		t.Errorf("notify_self risk %v, want critical", notify.Risk())
	}

	if got := names(toolRegistry(false, &fakeNotifier{}, st, &testutil.FakeModel{}, "web-search", time.UTC)); slices.Contains(got, "notify_self") || slices.Contains(got, "current_time") {
		t.Errorf("disabled: tools %v, want no demo tools", got)
	}
}

func TestDemoToolsReachTheModel(t *testing.T) {
	cfg := config.Config{EnableDemoTools: true, LLMModel: "cheap", WebSearchModel: "web-search", HistoryLimit: 5, MaxLoopIterations: 2}
	st := testutil.NewFakeStore()
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{Content: "ok"}, nil
	}}
	reg := toolRegistry(true, &fakeNotifier{}, st, client, cfg.WebSearchModel, time.UTC)
	a := agent.New(client, st, agentOptions(cfg, "test prompt", reg, time.UTC))

	if _, err := a.HandleMessage(identity.WithUser(context.Background(), 1), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	var offered []string
	for _, def := range client.Requests()[0].Tools {
		offered = append(offered, def.Name)
	}
	if !slices.Contains(offered, "notify_self") || !slices.Contains(offered, "task_add") {
		t.Errorf("model was offered %v, want the demo and task tools", offered)
	}
}

func TestNotifySelfSendsToTheRequestChat(t *testing.T) {
	notifier := &fakeNotifier{}
	reg := toolRegistry(true, notifier, testutil.NewFakeStore(), &testutil.FakeModel{}, "web-search", time.UTC)
	tool, err := reg.Lookup("notify_self")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	args := json.RawMessage(`{"text": "Dentist at 3pm"}`)

	ctx := channel.WithChat(identity.WithUser(context.Background(), 1), 777)
	if _, err := tool.Execute(ctx, args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !reflect.DeepEqual(notifier.chats, []int64{777}) || !strings.Contains(notifier.texts[0], "Dentist at 3pm") {
		t.Errorf("sent to %v %q, want the text to chat 777", notifier.chats, notifier.texts)
	}

	if _, err := tool.Execute(identity.WithUser(context.Background(), 1), args); err == nil {
		t.Error("Execute without a chat succeeded, want an error")
	}
	if len(notifier.chats) != 1 {
		t.Errorf("a message was sent without a chat: %v", notifier.chats)
	}
}

func TestWebSearchEndToEndWithAgent(t *testing.T) {
	st := testutil.NewFakeStore()
	cfg := config.Config{
		LLMModel:          "cheap",
		WebSearchModel:    "custom-search",
		HistoryLimit:      10,
		MaxLoopIterations: 4,
	}
	client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
		if req.Model == "custom-search" {
			return model.CompletionResponse{
				Content:   "Grounded answer for " + req.Messages[1].Content,
				Citations: []model.Citation{{Title: "Official Docs", URL: "https://example.org/docs"}},
				Usage:     model.Usage{PromptTokens: 11, CompletionTokens: 7},
			}, nil
		}
		last := req.Messages[len(req.Messages)-1]
		if last.Role == model.RoleTool {
			return model.CompletionResponse{
				Content: "Reply from tool: " + last.Content,
				Usage:   model.Usage{PromptTokens: 30, CompletionTokens: 12},
			}, nil
		}
		return model.CompletionResponse{
			ToolCalls: []model.ToolCall{{
				ID:        "call_ws_" + last.Content,
				Name:      "web_search",
				Arguments: json.RawMessage(`{"query":"` + last.Content + `"}`),
			}},
			Usage: model.Usage{PromptTokens: 20, CompletionTokens: 8},
		}, nil
	}}

	reg := toolRegistry(false, &fakeNotifier{}, st, client, cfg.WebSearchModel, time.UTC)
	a := agent.New(client, st, agentOptions(cfg, "test prompt", reg, time.UTC))

	reply1, err := a.HandleMessage(identity.WithUser(context.Background(), 1), "alpha")
	if err != nil {
		t.Fatalf("HandleMessage user 1: %v", err)
	}
	reply2, err := a.HandleMessage(identity.WithUser(context.Background(), 2), "beta")
	if err != nil {
		t.Fatalf("HandleMessage user 2: %v", err)
	}

	if !strings.Contains(reply1, "[data]") || !strings.Contains(reply1, "Grounded answer for alpha") || !strings.Contains(reply1, "Official Docs (https://example.org/docs)") {
		t.Errorf("reply1 = %q", reply1)
	}
	if !strings.Contains(reply2, "Grounded answer for beta") || strings.Contains(reply2, "alpha") {
		t.Errorf("reply2 = %q", reply2)
	}

	// Each user should have 3 model calls: main turn 1 -> web_search (custom-search) -> main turn 2,
	// and 1 web_search tool call under their own user ID and conversation.
	for _, tc := range []struct {
		user  identity.UserID
		query string
	}{
		{1, "alpha"},
		{2, "beta"},
	} {
		mcalls := st.ModelCalls(tc.user)
		if len(mcalls) != 3 {
			t.Fatalf("user %d model calls = %d, want 3: %+v", tc.user, len(mcalls), mcalls)
		}
		if mcalls[0].Model != "cheap" || mcalls[1].Model != "custom-search" || mcalls[2].Model != "cheap" {
			t.Errorf("user %d model sequence = [%s, %s, %s]", tc.user, mcalls[0].Model, mcalls[1].Model, mcalls[2].Model)
		}
		if mcalls[1].ConversationID != mcalls[0].ConversationID {
			t.Errorf("user %d search conversation %d != main %d", tc.user, mcalls[1].ConversationID, mcalls[0].ConversationID)
		}
		tcalls := st.ToolCalls(tc.user)
		if len(tcalls) != 1 || tcalls[0].ToolName != "web_search" || tcalls[0].Status != "executed" {
			t.Errorf("user %d tool calls = %+v", tc.user, tcalls)
		}
	}

	// Verify search request had no tools and no conversation history from the other user.
	reqs := client.Requests()
	if len(reqs) != 6 {
		t.Fatalf("total model requests = %d, want 6", len(reqs))
	}
	for _, idx := range []int{1, 4} {
		if len(reqs[idx].Tools) != 0 || len(reqs[idx].Messages) != 2 {
			t.Errorf("search req[%d] not isolated: tools=%d msgs=%d", idx, len(reqs[idx].Tools), len(reqs[idx].Messages))
		}
	}
}

func TestLoadSystemPromptAndAgentOptions(t *testing.T) {
	const sentinel = "SENTINEL-PROMPT-SECRET-TEXT"
	raw := []byte("\n  You are Nestor. " + sentinel + "  \n")
	sum := sha256.Sum256(raw)
	wantSHA := "sha256:" + hex.EncodeToString(sum[:])

	promptPath := filepath.Join(t.TempDir(), "system_prompt.md")
	if err := os.WriteFile(promptPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	cfg := config.Config{
		LLMModel:          "cheap",
		HistoryLimit:      10,
		MaxLoopIterations: 2,
		SystemPromptPath:  promptPath,
	}
	loaded, err := loadSystemPrompt(cfg)
	if err != nil {
		t.Fatalf("loadSystemPrompt: %v", err)
	}
	if loaded.Text != "You are Nestor. "+sentinel || loaded.Bytes != len(raw) || loaded.SHA256 != wantSHA {
		t.Fatalf("loaded = %+v, want trimmed text, bytes=%d, sha256=%s", loaded, len(raw), wantSHA)
	}

	logOutput := logBuf.String()
	if strings.Contains(logOutput, sentinel) {
		t.Errorf("startup log leaked prompt text: %s", logOutput)
	}
	var logEntry map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logOutput)), &logEntry); err != nil {
		t.Fatalf("unmarshal log line %q: %v", logOutput, err)
	}
	if logEntry["msg"] != "system prompt loaded" {
		t.Errorf("msg = %v, want %q", logEntry["msg"], "system prompt loaded")
	}
	if logEntry["path"] != promptPath {
		t.Errorf("path = %v, want %q", logEntry["path"], promptPath)
	}
	if int(logEntry["bytes"].(float64)) != len(raw) {
		t.Errorf("bytes = %v, want %d", logEntry["bytes"], len(raw))
	}
	if logEntry["sha256"] != wantSHA {
		t.Errorf("sha256 = %v, want %q", logEntry["sha256"], wantSHA)
	}

	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{Content: "Bonjour."}, nil
	}}
	st := testutil.NewFakeStore()
	reg := tools.NewRegistry()
	a := agent.New(client, st, agentOptions(cfg, loaded.Text, reg, time.UTC))
	if _, err := a.HandleMessage(identity.WithUser(context.Background(), 1), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	reqs := client.Requests()
	if len(reqs) != 1 || len(reqs[0].Messages) == 0 {
		t.Fatalf("unexpected requests: %+v", reqs)
	}
	sysMsg := reqs[0].Messages[0]
	if sysMsg.Role != model.RoleSystem || !strings.HasPrefix(sysMsg.Content, loaded.Text) {
		t.Errorf("system message = %+v, want RoleSystem starting with %q", sysMsg, loaded.Text)
	}
}

func TestMissingOrInvalidSystemPromptExitsNonZero(t *testing.T) {
	const (
		botTokenSecret = "bot-token-sentinel"
		apiKeySecret   = "api-key-sentinel"
		dbSecret       = "test-password"
	)
	if os.Getenv("NESTOR_TEST_RUN_MAIN") == "1" {
		main()
		return
	}

	cases := []struct {
		name  string
		setup func(t *testing.T) (set bool, val string)
	}{
		{
			name:  "unset",
			setup: func(*testing.T) (bool, string) { return false, "" },
		},
		{
			name: "missing file",
			setup: func(t *testing.T) (bool, string) {
				return true, filepath.Join(t.TempDir(), "missing.md")
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T) (bool, string) {
				return true, t.TempDir()
			},
		},
		{
			name: "whitespace only",
			setup: func(t *testing.T) (bool, string) {
				p := filepath.Join(t.TempDir(), "blank.md")
				if err := os.WriteFile(p, []byte(" \t\n "), 0o600); err != nil {
					t.Fatal(err)
				}
				return true, p
			},
		},
		{
			name: "nul byte",
			setup: func(t *testing.T) (bool, string) {
				p := filepath.Join(t.TempDir(), "nul.md")
				if err := os.WriteFile(p, []byte("hello\x00world"), 0o600); err != nil {
					t.Fatal(err)
				}
				return true, p
			},
		},
		{
			name: "invalid utf8",
			setup: func(t *testing.T) (bool, string) {
				p := filepath.Join(t.TempDir(), "badutf8.md")
				if err := os.WriteFile(p, []byte{0xff, 0xfe, 'a'}, 0o600); err != nil {
					t.Fatal(err)
				}
				return true, p
			},
		},
		{
			name: "oversized 4097 bytes",
			setup: func(t *testing.T) (bool, string) {
				p := filepath.Join(t.TempDir(), "big.md")
				if err := os.WriteFile(p, []byte(strings.Repeat("a", prompt.MaxBytes+1)), 0o600); err != nil {
					t.Fatal(err)
				}
				return true, p
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, val := tc.setup(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestMissingOrInvalidSystemPromptExitsNonZero$")
			env := []string{
				"NESTOR_TEST_RUN_MAIN=1",
				"TELEGRAM_BOT_TOKEN=" + botTokenSecret,
				"ALLOWED_TELEGRAM_USERS=123:Owner",
				"DATABASE_URL=postgres://nestor:" + dbSecret + "@127.0.0.1:1/nestor",
				"LLM_BASE_URL=http://127.0.0.1:1/v1",
				"LLM_API_KEY=" + apiKeySecret,
			}
			if set {
				env = append(env, "SYSTEM_PROMPT_FILE="+val)
			}
			cmd.Env = env

			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("got error %v, want exit status 1; output: %s", err, out)
			}
			if !strings.Contains(string(out), "SYSTEM_PROMPT_FILE") {
				t.Errorf("output %s does not name SYSTEM_PROMPT_FILE", out)
			}
			for _, secret := range []string{botTokenSecret, apiKeySecret, dbSecret} {
				if strings.Contains(string(out), secret) {
					t.Errorf("output leaked secret %q: %s", secret, out)
				}
			}
		})
	}
}

func TestAdminSubcommandsRunWithoutSystemPromptFile(t *testing.T) {
	env := map[string]string{
		"TELEGRAM_BOT_TOKEN":     "bot-token",
		"ALLOWED_TELEGRAM_USERS": "123:Owner",
		"DATABASE_URL":           "postgres://nestor:pw@127.0.0.1:1/nestor?connect_timeout=1",
		"LLM_BASE_URL":           "http://127.0.0.1:1/v1",
		"LLM_API_KEY":            "sk-test",
	}
	getenv := func(k string) string { return env[k] }

	t.Run("credentials", func(t *testing.T) {
		var out bytes.Buffer
		err := runCredentials(context.Background(), []string{"set", "1", "tok"}, getenv, strings.NewReader("val"), &out)
		if err == nil || strings.Contains(err.Error(), "SYSTEM_PROMPT_FILE") {
			t.Fatalf("runCredentials error = %v, should not require SYSTEM_PROMPT_FILE", err)
		}
		if !strings.Contains(err.Error(), "CREDENTIALS_KEY") {
			t.Errorf("runCredentials error = %v, want CREDENTIALS_KEY check after config.Load", err)
		}
	})

	t.Run("mcp", func(t *testing.T) {
		var out bytes.Buffer
		err := runMCP(context.Background(), []string{"approve", "srv", "tool"}, getenv, &out)
		if err == nil || strings.Contains(err.Error(), "SYSTEM_PROMPT_FILE") {
			t.Fatalf("runMCP error = %v, should not require SYSTEM_PROMPT_FILE", err)
		}
		if !strings.Contains(err.Error(), "open store") {
			t.Errorf("runMCP error = %v, want open store error after config.Load", err)
		}
	})

	t.Run("skills", func(t *testing.T) {
		root := t.TempDir()
		skillDir := filepath.Join(root, "demo")
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\ndescription: Demo.\n---\nHi.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := skillsCommand(root, []string{"pin", "demo"}, &out); err != nil {
			t.Fatalf("skillsCommand: %v", err)
		}
		if !strings.Contains(out.String(), "hash: sha256:") {
			t.Errorf("skillsCommand output = %q, want hash", out.String())
		}
	})
}
