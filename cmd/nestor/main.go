// Command nestor runs the Nestor personal assistant.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/epierre/nestor/internal/agent"
	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/channel/telegram"
	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/obs"
	"github.com/epierre/nestor/internal/store/postgres"
	"github.com/epierre/nestor/internal/tools"
)

const (
	platformTelegram = "telegram"
	systemPrompt     = "You are Nestor, a helpful personal assistant. Be concise."
)

func main() {
	slog.SetDefault(slog.New(obs.NewHandler(slog.NewJSONHandler(os.Stdout, nil))))
	if err := run(); err != nil {
		slog.Error("nestor stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	if err := st.SyncAllowlist(ctx, platformTelegram, allowedIdentities(cfg.AllowedTelegramUsers)); err != nil {
		return err
	}

	// Stale approvals are failed at startup and then every stale-after interval, so none stays
	// approved for longer than about two intervals.
	go approval.RunSweeps(ctx, st, cfg.ApprovalStaleAfter, cfg.ApprovalStaleAfter)

	client := model.NewClient(cfg.LLMBaseURL, cfg.LLMAPIKey)
	bot := telegram.New(cfg.TelegramBotToken, st)
	// No tools are registered yet, so the gate can only decide actions the agent proposes, and it
	// proposes none. The wiring is in place for when critical tools arrive.
	gate := approval.New(st, tools.NewRegistry(), cfg.ApprovalTTL)
	bot.SetDecider(gate)
	opts := agentOptions(cfg)
	opts.Approvals = gate
	opts.Cards = bot
	assistant := agent.New(client, st, opts)

	slog.Info("nestor started", "model", cfg.LLMModel, "allowed_users", len(cfg.AllowedTelegramUsers))
	err = bot.Run(ctx, assistant)
	slog.Info("nestor stopping")
	return err
}

func agentOptions(cfg config.Config) agent.Options {
	return agent.Options{
		Model:         cfg.LLMModel,
		SystemPrompt:  systemPrompt,
		HistoryLimit:  cfg.HistoryLimit,
		MaxIterations: cfg.MaxLoopIterations,
	}
}

func allowedIdentities(users []config.TelegramUser) []postgres.AllowedIdentity {
	identities := make([]postgres.AllowedIdentity, 0, len(users))
	for _, user := range users {
		identities = append(identities, postgres.AllowedIdentity{
			ExternalID:  strconv.FormatInt(user.ID, 10),
			DisplayName: user.DisplayName,
		})
	}
	return identities
}
