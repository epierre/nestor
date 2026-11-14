// Command nestor runs the Nestor personal assistant.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/epierre/nestor/internal/agent"
	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/channel/telegram"
	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/identity"
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

	client := model.NewClient(cfg.LLMBaseURL, cfg.LLMAPIKey)
	bot := telegram.New(cfg.TelegramBotToken, st)
	// Stale approvals are failed at startup and then every stale-after interval, so none stays
	// approved for longer than about two intervals. The owner is told through the bot.
	go approval.RunSweeps(ctx, st, bot, cfg.ApprovalStaleAfter, cfg.ApprovalStaleAfter)
	registry := toolRegistry(cfg.EnableDemoTools, bot)
	gate := approval.New(st, registry, cfg.ApprovalTTL)
	bot.SetDecider(gate)
	opts := agentOptions(cfg, registry)
	opts.Approvals = gate
	opts.Cards = bot
	opts.Notices = bot
	assistant := agent.New(client, st, opts)

	slog.Info("nestor started", "model", cfg.LLMModel, "allowed_users", len(cfg.AllowedTelegramUsers))
	err = bot.Run(ctx, assistant)
	slog.Info("nestor stopping")
	return err
}

// chatNotifier sends a message to a chat in one attempt. The Telegram channel implements it.
type chatNotifier interface {
	Notify(ctx context.Context, chatID int64, text string) error
}

// toolRegistry returns the tools the agent may call. The demo tools are registered only when
// enabled; the registry is empty otherwise.
func toolRegistry(enableDemo bool, notifier chatNotifier) *tools.Registry {
	reg := tools.NewRegistry()
	if !enableDemo {
		return reg
	}
	// Registering fresh names into an empty registry cannot fail.
	_ = reg.Register(tools.NewCurrentTimeTool())
	_ = reg.Register(tools.NewNotifySelfTool(notifySelf(notifier)))
	return reg
}

// notifySelf sends the text to the chat of the request that called the tool. A call without a chat
// fails, so that nothing is sent to an unknown place.
func notifySelf(notifier chatNotifier) tools.MessageSender {
	return func(ctx context.Context, _ identity.UserID, text string) error {
		chatID, ok := channel.ChatFrom(ctx)
		if !ok {
			return errors.New("notify_self: no chat in the request")
		}
		return notifier.Notify(ctx, chatID, text)
	}
}

// agentOptions turns the configuration into agent options. The registry is offered to the model
// only when the demo tools are enabled, so that the default configuration keeps the agent tool-free.
func agentOptions(cfg config.Config, reg *tools.Registry) agent.Options {
	opts := agent.Options{
		Model:         cfg.LLMModel,
		SystemPrompt:  systemPrompt,
		HistoryLimit:  cfg.HistoryLimit,
		MaxIterations: cfg.MaxLoopIterations,
	}
	if cfg.EnableDemoTools {
		opts.Tools = reg
	}
	return opts
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
