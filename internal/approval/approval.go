// Package approval holds critical tool calls until the user they belong to approves them.
package approval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/tools"
)

var (
	// ErrNotCritical is returned when Propose is given a tool that does not need approval.
	ErrNotCritical = errors.New("tool is not critical")
	// ErrNoPendingAction is returned by Decide when the action is unknown, belongs to another
	// user, or is no longer pending. The three are not told apart.
	ErrNoPendingAction = errors.New("this action is no longer pending")
	// ErrExpired is returned when the user decides after the action's approval window closed.
	ErrExpired = errors.New("this action has expired")
	// ErrIntegrity is returned when a pending action's arguments no longer match the hash taken
	// at proposal. The action is marked failed and nothing runs.
	ErrIntegrity = errors.New("pending action arguments changed after proposal")
	// ErrOutcomeUnknown is the error of an approved action that was never recorded as executed.
	// Such an action may or may not have run, so it is failed and never retried.
	ErrOutcomeUnknown = errors.New("outcome unknown: may or may not have run; not retried")
)

// Outcome notes are system messages in the user's open conversation, so the model reads each
// outcome on its next turn. They name the action and its outcome only: never its arguments or its
// result, which are untrusted content.
const (
	noteRejected  = "Pending action %s was rejected by the user. It did not run."
	noteExecuted  = "Pending action %s was approved and ran."
	noteExpired   = "Pending action %s expired before the user decided. It did not run."
	noteIntegrity = "Pending action %s did not run: its stored details no longer matched the proposal."
	noteFailed    = "Pending action %s was approved but failed while running."
)

// Decision is the user's answer to a pending action.
type Decision string

// Values of Decision.
const (
	Approve Decision = "approve"
	Reject  Decision = "reject"
)

// ToolLookup finds the registered tool that a pending action runs. The raw registry implements
// it. The agent's view does not, since it refuses to execute critical tools.
type ToolLookup interface {
	Lookup(name string) (tools.Tool, error)
}

// Gate stores proposed actions and carries out the user's decisions on them.
type Gate struct {
	store store.Store
	tools ToolLookup
	ttl   time.Duration
	now   func() time.Time
}

// New returns a Gate that stores actions in st, runs them through reg, and expires them ttl after
// proposal.
func New(st store.Store, reg ToolLookup, ttl time.Duration) *Gate {
	return &Gate{store: st, tools: reg, ttl: ttl, now: time.Now}
}

// Propose stores a pending action for user, running tool with args once approved. The args must
// already have passed tools.Validate.
func (g *Gate) Propose(ctx context.Context, user identity.UserID, tool tools.Tool, args json.RawMessage) (store.PendingAction, error) {
	if tool.Risk() != tools.RiskCritical {
		return store.PendingAction{}, ErrNotCritical
	}
	preview, err := tool.Preview(args)
	if err != nil {
		return store.PendingAction{}, fmt.Errorf("preview %s: %w", tool.Name(), err)
	}
	id, err := newID()
	if err != nil {
		return store.PendingAction{}, err
	}
	created := g.instant()
	action := store.PendingAction{
		ID:        id,
		ToolName:  tool.Name(),
		ArgsJSON:  args,
		ArgsHash:  ArgsHash(user, tool.Name(), args),
		Preview:   preview,
		State:     store.PendingProposed,
		CreatedAt: created,
		ExpiresAt: created.Add(g.ttl),
	}
	if err := g.store.ProposeAction(ctx, user, action); err != nil {
		return store.PendingAction{}, fmt.Errorf("store pending action: %w", err)
	}
	// One pending action per tool: the new one replaces the earlier ones, which can no longer be
	// approved. If this fails, both stay approvable, and each still needs its own approval.
	if _, err := g.store.SupersedePending(ctx, user, tool.Name(), id); err != nil {
		slog.ErrorContext(ctx, "earlier pending actions not superseded", "action_id", id, "error", err)
	}
	action.UserID = user
	return action, nil
}

// Decide applies decision to the user's pending action. Approval runs the tool at most once, with
// the user in context, and only if the action is unexpired and its arguments still match the hash
// taken at proposal. The returned action shows the state after the decision.
func (g *Gate) Decide(ctx context.Context, user identity.UserID, actionID string, decision Decision) (store.PendingAction, error) {
	if decision != Approve && decision != Reject {
		return store.PendingAction{}, fmt.Errorf("unknown decision %q", decision)
	}
	action, err := g.store.GetPendingAction(ctx, user, actionID)
	if errors.Is(err, store.ErrActionNotFound) {
		return store.PendingAction{}, ErrNoPendingAction
	}
	if err != nil {
		return store.PendingAction{}, fmt.Errorf("load pending action: %w", err)
	}
	if action.State != store.PendingProposed {
		return store.PendingAction{}, ErrNoPendingAction
	}
	now := g.instant()

	if decision == Reject {
		rejected, err := g.move(ctx, user, action.ID, store.PendingProposed, store.PendingUpdate{
			State:     store.PendingRejected,
			DecidedAt: &now,
		})
		if err != nil {
			return store.PendingAction{}, err
		}
		g.note(ctx, user, fmt.Sprintf(noteRejected, rejected.ID))
		return rejected, nil
	}

	if !now.Before(action.ExpiresAt) {
		expired, err := g.move(ctx, user, action.ID, store.PendingProposed, store.PendingUpdate{
			State:     store.PendingExpired,
			DecidedAt: &now,
		})
		if err != nil {
			return store.PendingAction{}, err
		}
		g.note(ctx, user, fmt.Sprintf(noteExpired, expired.ID))
		return expired, ErrExpired
	}

	if ArgsHash(user, action.ToolName, action.ArgsJSON) != action.ArgsHash {
		failed, err := g.move(ctx, user, action.ID, store.PendingProposed, store.PendingUpdate{
			State:     store.PendingFailed,
			DecidedAt: &now,
			Error:     ErrIntegrity.Error(),
		})
		if err != nil {
			return store.PendingAction{}, err
		}
		g.note(ctx, user, fmt.Sprintf(noteIntegrity, failed.ID))
		return failed, ErrIntegrity
	}

	// Claiming the action is the step that makes approval happen once: a second approval finds it
	// already approved and stops here.
	claimed, err := g.move(ctx, user, action.ID, store.PendingProposed, store.PendingUpdate{
		State:     store.PendingApproved,
		DecidedAt: &now,
	})
	if err != nil {
		return store.PendingAction{}, err
	}
	return g.run(ctx, user, claimed)
}

// run executes an approved action and records the outcome.
func (g *Gate) run(ctx context.Context, user identity.UserID, action store.PendingAction) (store.PendingAction, error) {
	tool, err := g.tools.Lookup(action.ToolName)
	if err != nil {
		return g.fail(ctx, user, action.ID, fmt.Errorf("look up %s: %w", action.ToolName, err))
	}
	res, err := tool.Execute(identity.WithUser(ctx, user), action.ArgsJSON)
	if err != nil {
		return g.fail(ctx, user, action.ID, fmt.Errorf("execute %s: %w", action.ToolName, err))
	}
	resultJSON, err := json.Marshal(res.Content)
	if err != nil {
		return g.fail(ctx, user, action.ID, fmt.Errorf("encode result of %s: %w", action.ToolName, err))
	}
	done := g.instant()
	executed, err := g.move(ctx, user, action.ID, store.PendingApproved, store.PendingUpdate{
		State:      store.PendingExecuted,
		ExecutedAt: &done,
		ResultJSON: resultJSON,
	})
	if err != nil {
		return store.PendingAction{}, err
	}
	g.note(ctx, user, fmt.Sprintf(noteExecuted, executed.ID))
	return executed, nil
}

// fail marks an approved action failed and returns the cause as the error.
func (g *Gate) fail(ctx context.Context, user identity.UserID, id string, cause error) (store.PendingAction, error) {
	done := g.instant()
	failed, err := g.move(ctx, user, id, store.PendingApproved, store.PendingUpdate{
		State:      store.PendingFailed,
		ExecutedAt: &done,
		Error:      cause.Error(),
	})
	if err != nil {
		return store.PendingAction{}, err
	}
	g.note(ctx, user, fmt.Sprintf(noteFailed, failed.ID))
	return failed, cause
}

// note records an outcome in the user's open conversation. The decision is already final, so a
// failure to record it is logged rather than returned.
func (g *Gate) note(ctx context.Context, user identity.UserID, text string) {
	conv, err := g.store.ActiveConversation(ctx, user)
	if err == nil {
		_, err = g.store.AppendMessage(ctx, user, conv.ID, model.RoleSystem, text)
	}
	if err != nil {
		slog.ErrorContext(ctx, "outcome not recorded in conversation", "error", err)
	}
}

// move changes an action's state from from to update.State and returns the stored action. It
// returns ErrNoPendingAction when the action is no longer in from.
func (g *Gate) move(ctx context.Context, user identity.UserID, id, from string, update store.PendingUpdate) (store.PendingAction, error) {
	ok, err := g.store.UpdatePendingAction(ctx, user, id, from, update)
	if err != nil {
		return store.PendingAction{}, fmt.Errorf("update pending action: %w", err)
	}
	if !ok {
		return store.PendingAction{}, ErrNoPendingAction
	}
	action, err := g.store.GetPendingAction(ctx, user, id)
	if err != nil {
		return store.PendingAction{}, fmt.Errorf("reload pending action: %w", err)
	}
	return action, nil
}

// instant returns the current time at the precision the database keeps, so stored times compare
// equal to the returned ones.
func (g *Gate) instant() time.Time {
	return g.now().UTC().Truncate(time.Microsecond)
}

// SweepStale fails every action approved more than staleAfter before now, for all users, and
// returns how many it failed. A failed action is never executed again.
func SweepStale(ctx context.Context, st store.Store, now time.Time, staleAfter time.Duration) (int64, error) {
	n, err := st.FailStaleApprovals(ctx, now.Add(-staleAfter), ErrOutcomeUnknown.Error())
	if err != nil {
		return 0, fmt.Errorf("sweep stale approvals: %w", err)
	}
	return n, nil
}

// RunSweeps runs SweepStale now and then every interval until ctx is done. A failed sweep is logged
// and tried again at the next interval.
func RunSweeps(ctx context.Context, st store.Store, staleAfter, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		n, err := SweepStale(ctx, st, time.Now(), staleAfter)
		switch {
		case err != nil && ctx.Err() == nil:
			slog.ErrorContext(ctx, "approval sweep failed", "error", err)
		case n > 0:
			slog.WarnContext(ctx, "stale approvals failed as outcome unknown", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ArgsHash returns the hex SHA-256 that binds an action to its user, tool name and exact argument
// bytes. Each variable part is length-prefixed, so no two inputs share a hash by splitting
// differently.
func ArgsHash(user identity.UserID, tool string, args json.RawMessage) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%d\x00%s\x00", user, len(tool), tool)
	h.Write(args)
	return hex.EncodeToString(h.Sum(nil))
}

// newID returns 128 random bits in hex. Random rather than sequential, so IDs cannot be guessed.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random action ID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
