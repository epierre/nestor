package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

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
	got := agentOptions(config.Config{LLMModel: "strong", HistoryLimit: 12, MaxLoopIterations: 3}, nil)

	want := agent.Options{Model: "strong", SystemPrompt: systemPrompt, HistoryLimit: 12, MaxIterations: 3}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
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

func TestDemoToolsOfferedOnlyWhenEnabled(t *testing.T) {
	names := func(reg *tools.Registry) []string {
		var got []string
		for _, def := range reg.Tools() {
			got = append(got, def.Name)
		}
		return got
	}

	on := toolRegistry(true, &fakeNotifier{})
	if got := names(on); !reflect.DeepEqual(got, []string{"current_time", "notify_self"}) {
		t.Errorf("enabled: tools %v, want current_time and notify_self", got)
	}
	notify, err := on.Lookup("notify_self")
	if err != nil {
		t.Fatalf("Lookup notify_self: %v", err)
	}
	if notify.Risk() != tools.RiskCritical {
		t.Errorf("notify_self risk %v, want critical", notify.Risk())
	}

	if got := names(toolRegistry(false, &fakeNotifier{})); len(got) != 0 {
		t.Errorf("disabled: tools %v, want none", got)
	}
}

func TestDemoToolsReachTheModel(t *testing.T) {
	cfg := config.Config{EnableDemoTools: true, LLMModel: "cheap", HistoryLimit: 5, MaxLoopIterations: 2}
	reg := toolRegistry(true, &fakeNotifier{})
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{Content: "ok"}, nil
	}}
	a := agent.New(client, testutil.NewFakeStore(), agentOptions(cfg, reg))

	if _, err := a.HandleMessage(identity.WithUser(context.Background(), 1), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	var offered []string
	for _, def := range client.Requests()[0].Tools {
		offered = append(offered, def.Name)
	}
	if !reflect.DeepEqual(offered, []string{"current_time", "notify_self"}) {
		t.Errorf("model was offered %v, want the two demo tools", offered)
	}
}

func TestNotifySelfSendsToTheRequestChat(t *testing.T) {
	notifier := &fakeNotifier{}
	reg := toolRegistry(true, notifier)
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
