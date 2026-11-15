package reminders

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
	"github.com/epierre/nestor/internal/tools"
)

const (
	owner     = identity.UserID(1)
	ownerChat = int64(555)
)

var zurich = mustLoad("Europe/Zurich")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// now is 10:00 in Zurich on 14 November 2026, which is 09:00 UTC.
var now = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

type harness struct {
	st   *testutil.FakeStore
	reg  *tools.Registry
	gate *approval.Gate
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := testutil.NewFakeStore()
	reg := tools.NewRegistry()
	for _, tool := range Tools(st, zurich, func() time.Time { return now }) {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register %s: %v", tool.Name(), err)
		}
	}
	return &harness{st: st, reg: reg, gate: approval.New(st, reg, time.Hour)}
}

func inChat(user identity.UserID, chat int64) context.Context {
	return channel.WithChat(identity.WithUser(context.Background(), user), chat)
}

// pending returns the user's pending reminders.
func (h *harness) pending(user identity.UserID) []store.Reminder {
	list, _ := h.st.ListReminders(identity.WithUser(context.Background(), user), user)
	return list
}

func (h *harness) call(t *testing.T, ctx context.Context, name, args string) (string, error) {
	t.Helper()
	tool, err := h.reg.Lookup(name)
	if err != nil {
		t.Fatalf("Lookup %s: %v", name, err)
	}
	res, err := tool.Execute(ctx, json.RawMessage(args))
	return res.Content, err
}

func TestDueReadInConfiguredZone(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name string
		due  string
		want time.Time
	}{
		// 15:00 in Zurich in November is 14:00 UTC.
		{"clock time means today", "15:00", time.Date(2026, 11, 14, 14, 0, 0, 0, time.UTC)},
		{"local date and time", "2026-11-15 08:30", time.Date(2026, 11, 15, 7, 30, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"text": "Call the dentist", "due": tt.due})
			if _, err := h.call(t, inChat(owner, ownerChat), "reminder_add", string(args)); err != nil {
				t.Fatalf("reminder_add: %v", err)
			}
			list := h.pending(owner)
			last := list[len(list)-1]
			if !last.DueAt.Equal(tt.want) {
				t.Errorf("due %v, want %v", last.DueAt, tt.want)
			}
		})
	}
}

func TestPastOrUnparsableTimeRefused(t *testing.T) {
	h := newHarness(t)
	for _, due := range []string{"09:00", "tomorrow at three", "2026-11-14T25:00"} {
		args, _ := json.Marshal(map[string]string{"text": "x", "due": due})
		if _, err := h.call(t, inChat(owner, ownerChat), "reminder_add", string(args)); err == nil {
			t.Errorf("due %q accepted", due)
		}
	}
	if n := len(h.pending(owner)); n != 0 {
		t.Errorf("%d reminders stored despite refused times", n)
	}
}

func TestReminderStoresRequestChat(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, inChat(owner, ownerChat), "reminder_add", `{"text": "Call", "due": "15:00"}`); err != nil {
		t.Fatalf("reminder_add: %v", err)
	}
	if got := h.pending(owner)[0].ChatID; got != ownerChat {
		t.Errorf("chat %d, want %d", got, ownerChat)
	}

	if _, err := h.call(t, identity.WithUser(context.Background(), owner), "reminder_add", `{"text": "No chat", "due": "15:00"}`); err == nil {
		t.Error("reminder created without a chat to send it to")
	}
}

func TestReminderListShowsOnlyPending(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, inChat(owner, ownerChat), "reminder_add", `{"text": "Pending one", "due": "15:00"}`); err != nil {
		t.Fatalf("reminder_add: %v", err)
	}
	if _, err := h.call(t, inChat(owner, ownerChat), "reminder_add", `{"text": "Cancelled one", "due": "16:00"}`); err != nil {
		t.Fatalf("reminder_add: %v", err)
	}
	cancel, _ := h.reg.Lookup("reminder_cancel")
	action, err := h.gate.Propose(inChat(owner, ownerChat), owner, cancel, json.RawMessage(`{"id": 2}`))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := h.gate.Decide(inChat(owner, ownerChat), owner, action.ID, approval.Approve); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	out, err := h.call(t, inChat(owner, ownerChat), "reminder_list", `{}`)
	if err != nil {
		t.Fatalf("reminder_list: %v", err)
	}
	if !strings.Contains(out, "Pending one") || strings.Contains(out, "Cancelled one") {
		t.Errorf("list:\n%s", out)
	}
}

func TestCancelProposesAndCancelsOnceApproved(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, inChat(owner, ownerChat), "reminder_add", `{"text": "Call the dentist", "due": "15:00"}`); err != nil {
		t.Fatalf("reminder_add: %v", err)
	}
	cancel, _ := h.reg.Lookup("reminder_cancel")
	if cancel.Risk() != tools.RiskCritical {
		t.Fatalf("reminder_cancel risk %v, want critical", cancel.Risk())
	}

	action, err := h.gate.Propose(inChat(owner, ownerChat), owner, cancel, json.RawMessage(`{"id": 1}`))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if !strings.Contains(action.Preview, "Call the dentist") {
		t.Errorf("card %q does not name the reminder", action.Preview)
	}
	if _, err := h.gate.Decide(inChat(owner, ownerChat), owner, action.ID, approval.Approve); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if n := len(h.pending(owner)); n != 0 {
		t.Errorf("%d pending reminders after cancel, want 0", n)
	}
}

func TestReminderToolRisks(t *testing.T) {
	h := newHarness(t)
	want := map[string]tools.Risk{
		"reminder_add":    tools.RiskReversible,
		"reminder_list":   tools.RiskRead,
		"reminder_cancel": tools.RiskCritical,
	}
	for name, risk := range want {
		tool, err := h.reg.Lookup(name)
		if err != nil || tool.Risk() != risk {
			t.Errorf("%s: risk %v err %v, want %v", name, tool, err, risk)
		}
	}
}
