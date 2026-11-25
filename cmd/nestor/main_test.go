package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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
	got := agentOptions(config.Config{LLMModel: "strong", HistoryLimit: 12, MaxLoopIterations: 3}, reg, loc)

	if got.Model != "strong" || got.SystemPrompt != systemPrompt || got.HistoryLimit != 12 ||
		got.MaxIterations != 3 || got.Tools != reg || got.Location != loc {
		t.Errorf("got %+v, want strong/12/3 with registry and Europe/Zurich", got)
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
	a := agent.New(client, st, agentOptions(cfg, reg, time.UTC))

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
	a := agent.New(client, st, agentOptions(cfg, reg, time.UTC))

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
