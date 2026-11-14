//go:build integration

package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/store"
)

func TestTasksScopedToUser(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	task, err := st.AddTask(ctxA, a, store.Task{Title: "Renew passport"})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	if got, _ := st.ListTasks(ctxB, b, true); len(got) != 0 {
		t.Errorf("other user lists %+v, want none", got)
	}
	if _, err := st.GetTask(ctxB, b, task.ID); !errors.Is(err, store.ErrTaskNotFound) {
		t.Errorf("other user GetTask: err = %v, want ErrTaskNotFound", err)
	}
	if err := st.CompleteTask(ctxB, b, task.ID, time.Now()); !errors.Is(err, store.ErrTaskNotFound) {
		t.Errorf("other user CompleteTask: err = %v, want ErrTaskNotFound", err)
	}
	if err := st.DeleteTask(ctxB, b, task.ID); !errors.Is(err, store.ErrTaskNotFound) {
		t.Errorf("other user DeleteTask: err = %v, want ErrTaskNotFound", err)
	}
	if _, err := st.GetTask(ctxA, a, task.ID); err != nil {
		t.Errorf("owner lost the task: %v", err)
	}
}

func TestTaskDueDaysOrderedUndatedLast(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	for _, tt := range []store.Task{
		{Title: "later", DueDate: "2026-12-05"},
		{Title: "undated"},
		{Title: "soonest", DueDate: "2026-12-01"},
	} {
		if _, err := st.AddTask(ctxA, a, tt); err != nil {
			t.Fatalf("AddTask %s: %v", tt.Title, err)
		}
	}

	got, err := st.ListTasks(ctxA, a, false)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var titles, days []string
	for _, task := range got {
		titles = append(titles, task.Title)
		days = append(days, task.DueDate)
	}
	if want := []string{"soonest", "later", "undated"}; !equalStrings(titles, want) {
		t.Errorf("order %v, want %v", titles, want)
	}
	if want := []string{"2026-12-01", "2026-12-05", ""}; !equalStrings(days, want) {
		t.Errorf("due days %v, want %v", days, want)
	}
}

func TestCompleteHidesTaskUnlessDoneRequested(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	task, _ := st.AddTask(ctxA, a, store.Task{Title: "Buy stamps"})
	done := time.Now().UTC().Truncate(time.Microsecond)
	if err := st.CompleteTask(ctxA, a, task.ID, done); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	if open, _ := st.ListTasks(ctxA, a, false); len(open) != 0 {
		t.Errorf("open tasks %+v, want none after completing", open)
	}
	all, _ := st.ListTasks(ctxA, a, true)
	if len(all) != 1 || all[0].DoneAt == nil || !all[0].DoneAt.Equal(done) {
		t.Errorf("all tasks %+v, want the done task with its completion time", all)
	}
}

func TestDeleteTaskRemovesIt(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	task, _ := st.AddTask(ctxA, a, store.Task{Title: "Gone soon"})
	if err := st.DeleteTask(ctxA, a, task.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if _, err := st.GetTask(ctxA, a, task.ID); !errors.Is(err, store.ErrTaskNotFound) {
		t.Errorf("after delete: err = %v, want ErrTaskNotFound", err)
	}
}

func TestInvalidTaskValuesRejected(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	tests := []struct {
		name string
		task store.Task
	}{
		{"empty title", store.Task{Title: ""}},
		{"title over 200 characters", store.Task{Title: string(make([]rune, 201))}},
		{"impossible day", store.Task{Title: "x", DueDate: "2026-02-30"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := st.AddTask(ctxA, a, tt.task); err == nil {
				t.Error("AddTask accepted an invalid task")
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
