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
	"time"
	// The runtime image has no zoneinfo files, so the time zone database is compiled in.
	_ "time/tzdata"

	"github.com/epierre/nestor/internal/agent"
	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/channel/telegram"
	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/obs"
	"github.com/epierre/nestor/internal/reminders"
	"github.com/epierre/nestor/internal/skills"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/store/postgres"
	"github.com/epierre/nestor/internal/tasks"
	"github.com/epierre/nestor/internal/tools"
)

const (
	platformTelegram = "telegram"
	systemPrompt     = "You are Nestor, a helpful personal assistant. Be concise."
)

func main() {
	slog.SetDefault(slog.New(obs.NewHandler(slog.NewJSONHandler(os.Stdout, nil))))
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "credentials":
		err = runCredentials(context.Background(), os.Args[2:], os.Getenv, os.Stdin, os.Stdout)
	case len(os.Args) > 1 && os.Args[1] == "mcp":
		err = runMCP(context.Background(), os.Args[2:], os.Getenv, os.Stdout)
	case len(os.Args) > 1 && os.Args[1] == "skills":
		err = skillsCommand(skillsDir, os.Args[2:], os.Stdout)
	default:
		err = run()
	}
	if err != nil {
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
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("load timezone: %w", err)
	}
	registry := toolRegistry(cfg.EnableDemoTools, bot, st, loc)
	active, err := st.ListActiveUsers(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	loadedSkills, err := loadSkills(cfg, active, slog.Default())
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}
	slog.Info("skills loaded", "count", len(loadedSkills))
	// MCP supervisors get their own context, so that whatever ends run, they are stopped before
	// it returns rather than waited on while still running.
	mcpCtx, stopMCP := context.WithCancel(ctx)
	waitMCP, err := startMCP(mcpCtx, mcpDeps{
		Config: cfg, Registry: registry, Pins: st, Credentials: st, Notifier: bot, Users: active, Dial: mcp.DialHTTP,
	})
	if err != nil {
		stopMCP()
		return fmt.Errorf("start mcp servers: %w", err)
	}
	defer func() {
		stopMCP()
		waitMCP()
	}()
	gate := approval.New(st, registry, cfg.ApprovalTTL)
	bot.SetDecider(gate)
	go reminders.Run(ctx, st, bot, reminderTick)
	opts := agentOptions(cfg, registry, loc)
	opts.Skills = skills.NewCatalog(loadedSkills)
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

// reminderTick is how often due reminders are looked for.
const reminderTick = 30 * time.Second

// toolRegistry returns the tools the agent may call: the task and reminder tools always, and the
// demo tools when enabled.
func toolRegistry(enableDemo bool, notifier chatNotifier, st store.Store, loc *time.Location) *tools.Registry {
	reg := tools.NewRegistry()
	// Each name is registered once, so registration cannot fail.
	for _, tool := range tasks.Tools(st) {
		_ = reg.Register(tool)
	}
	for _, tool := range reminders.Tools(st, loc, time.Now) {
		_ = reg.Register(tool)
	}
	_ = tools.RegisterDemoTools(reg, enableDemo, notifySelf(notifier))
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

// agentOptions turns the configuration into agent options, offering the model the registry and
// local time zone.
func agentOptions(cfg config.Config, reg *tools.Registry, loc *time.Location) agent.Options {
	return agent.Options{
		Model:         cfg.LLMModel,
		SystemPrompt:  systemPrompt,
		HistoryLimit:  cfg.HistoryLimit,
		MaxIterations: cfg.MaxLoopIterations,
		Tools:         reg,
		Location:      loc,
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
