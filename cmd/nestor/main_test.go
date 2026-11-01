package main

import (
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/epierre/nestor/internal/agent"
	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/store/postgres"
)

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
	got := agentOptions(config.Config{LLMModel: "strong", HistoryLimit: 12, MaxLoopIterations: 3})

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
