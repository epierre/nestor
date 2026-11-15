package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
	"github.com/epierre/nestor/internal/tools"
)

const (
	owner    = identity.UserID(1)
	stranger = identity.UserID(2)
)

// harness holds the task tools over a fake store, and the approval gate that runs the critical one.
type harness struct {
	st   *testutil.FakeStore
	reg  *tools.Registry
	gate *approval.Gate
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := testutil.NewFakeStore()
	reg := tools.NewRegistry()
	for _, tool := range Tools(st) {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register %s: %v", tool.Name(), err)
		}
	}
	return &harness{st: st, reg: reg, gate: approval.New(st, reg, time.Hour)}
}

func as(user identity.UserID) context.Context {
	return identity.WithUser(context.Background(), user)
}

// call runs a tool directly, as the agent does for a non-critical tool.
func (h *harness) call(t *testing.T, user identity.UserID, name, args string) (string, error) {
	t.Helper()
	tool, err := h.reg.Lookup(name)
	if err != nil {
		t.Fatalf("Lookup %s: %v", name, err)
	}
	res, err := tool.Execute(as(user), json.RawMessage(args))
	return res.Content, err
}

func TestAddValidatesTitleLength(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name  string
		title string
		ok    bool
	}{
		{"empty", "", false},
		{"one character", "x", true},
		{"200 characters", strings.Repeat("x", 200), true},
		{"201 characters", strings.Repeat("x", 201), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"title": tt.title})
			_, err := h.call(t, owner, "task_add", string(args))
			if (err == nil) != tt.ok {
				t.Errorf("err = %v, want success=%v", err, tt.ok)
			}
		})
	}
}

func TestDueDayStoredAsGiven(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, owner, "task_add", `{"title": "Renew passport", "due": "2026-12-01"}`); err != nil {
		t.Fatalf("task_add: %v", err)
	}
	tasks, _ := h.st.ListTasks(as(owner), owner, false)
	if len(tasks) != 1 || tasks[0].DueDate != "2026-12-01" {
		t.Errorf("stored %+v, want due day 2026-12-01", tasks)
	}
}

func TestInvalidDueDayRefused(t *testing.T) {
	h := newHarness(t)
	for _, due := range []string{"2026-02-30", "tomorrow", "2026-12-01T10:00"} {
		args, _ := json.Marshal(map[string]string{"title": "x", "due": due})
		if _, err := h.call(t, owner, "task_add", string(args)); err == nil {
			t.Errorf("due %q accepted", due)
		}
	}
	if tasks, _ := h.st.ListTasks(as(owner), owner, true); len(tasks) != 0 {
		t.Errorf("tasks stored despite invalid days: %+v", tasks)
	}
}

func TestListSortsByDueDayUndatedLast(t *testing.T) {
	h := newHarness(t)
	for _, args := range []string{
		`{"title": "later", "due": "2026-12-05"}`,
		`{"title": "undated"}`,
		`{"title": "soonest", "due": "2026-12-01"}`,
	} {
		if _, err := h.call(t, owner, "task_add", args); err != nil {
			t.Fatalf("task_add %s: %v", args, err)
		}
	}
	out, err := h.call(t, owner, "task_list", `{}`)
	if err != nil {
		t.Fatalf("task_list: %v", err)
	}
	soonest, later, undated := strings.Index(out, "soonest"), strings.Index(out, "later"), strings.Index(out, "undated")
	if !(soonest >= 0 && soonest < later && later < undated) {
		t.Errorf("list order wrong:\n%s", out)
	}
}

func TestListHidesDoneTasksByDefault(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, owner, "task_add", `{"title": "Buy stamps"}`); err != nil {
		t.Fatalf("task_add: %v", err)
	}
	if _, err := h.call(t, owner, "task_complete", `{"id": 1}`); err != nil {
		t.Fatalf("task_complete: %v", err)
	}

	open, _ := h.call(t, owner, "task_list", `{}`)
	if strings.Contains(open, "Buy stamps") {
		t.Errorf("open list shows a done task:\n%s", open)
	}
	all, _ := h.call(t, owner, "task_list", `{"include_done": true}`)
	if !strings.Contains(all, "Buy stamps") {
		t.Errorf("list with include_done hides the done task:\n%s", all)
	}
}

func TestCompleteOtherUsersTaskRefusedLikeUnknown(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, owner, "task_add", `{"title": "Mine"}`); err != nil {
		t.Fatalf("task_add: %v", err)
	}
	_, theirs := h.call(t, stranger, "task_complete", `{"id": 1}`)
	_, unknown := h.call(t, stranger, "task_complete", `{"id": 999}`)
	for _, err := range []error{theirs, unknown} {
		if !errors.Is(err, store.ErrTaskNotFound) {
			t.Errorf("err = %v, want ErrTaskNotFound for both cases", err)
		}
	}
	if tasks, _ := h.st.ListTasks(as(owner), owner, false); len(tasks) != 1 || tasks[0].DoneAt != nil {
		t.Errorf("owner's task changed: %+v", tasks)
	}
}

func TestDeleteProposesAndDoesNotDeleteUntilApproved(t *testing.T) {
	h := newHarness(t)
	if _, err := h.call(t, owner, "task_add", `{"title": "Renew passport"}`); err != nil {
		t.Fatalf("task_add: %v", err)
	}
	del, err := h.reg.Lookup("task_delete")
	if err != nil {
		t.Fatalf("Lookup task_delete: %v", err)
	}

	action, err := h.gate.Propose(as(owner), owner, del, json.RawMessage(`{"id": 1}`))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if !strings.Contains(action.Preview, "Renew passport") {
		t.Errorf("card %q does not name the task", action.Preview)
	}
	if _, err := h.st.GetTask(as(owner), owner, 1); err != nil {
		t.Errorf("task gone before approval: %v", err)
	}

	if _, err := h.gate.Decide(as(owner), owner, action.ID, approval.Approve); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if _, err := h.st.GetTask(as(owner), owner, 1); !errors.Is(err, store.ErrTaskNotFound) {
		t.Errorf("task still there after approval: %v", err)
	}
	if _, err := h.gate.Decide(as(owner), owner, action.ID, approval.Approve); !errors.Is(err, approval.ErrNoPendingAction) {
		t.Errorf("second approval: err = %v, want ErrNoPendingAction", err)
	}
}

func TestDeleteIsCritical(t *testing.T) {
	h := newHarness(t)
	tool, _ := h.reg.Lookup("task_delete")
	if tool.Risk() != tools.RiskCritical {
		t.Errorf("task_delete risk %v, want critical", tool.Risk())
	}
	for _, name := range []string{"task_add", "task_complete"} {
		if tool, _ := h.reg.Lookup(name); tool.Risk() != tools.RiskReversible {
			t.Errorf("%s risk %v, want reversible", name, tool.Risk())
		}
	}
	if tool, _ := h.reg.Lookup("task_list"); tool.Risk() != tools.RiskRead {
		t.Errorf("task_list risk %v, want read", tool.Risk())
	}
}
