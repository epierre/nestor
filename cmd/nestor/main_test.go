package main

import (
	"reflect"
	"testing"

	"github.com/epierre/nestor/internal/agent"
	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/store/postgres"
)

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
