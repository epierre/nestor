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
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/tools"
)

// ErrNotCritical is returned when Propose is given a tool that does not need approval.
var ErrNotCritical = errors.New("tool is not critical")

// Gate stores proposed actions for their users to decide on.
type Gate struct {
	store store.Store
	ttl   time.Duration
}

// New returns a Gate that stores actions in st and expires them ttl after proposal.
func New(st store.Store, ttl time.Duration) *Gate {
	return &Gate{store: st, ttl: ttl}
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
	// Microseconds match what the database keeps, so the stored times equal the returned ones.
	created := time.Now().UTC().Truncate(time.Microsecond)
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
	action.UserID = user
	return action, nil
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
