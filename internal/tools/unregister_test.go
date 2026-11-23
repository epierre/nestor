package tools_test

import (
	"errors"
	"testing"

	"github.com/epierre/nestor/internal/tools"
)

func TestUnregister(t *testing.T) {
	r := tools.NewRegistry()
	if err := r.Register(tools.NewCurrentTimeTool()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !r.Unregister("current_time") {
		t.Fatal("Unregister reported the tool missing")
	}
	if _, err := r.Lookup("current_time"); !errors.Is(err, tools.ErrNotFound) {
		t.Errorf("Lookup after Unregister: err = %v, want ErrNotFound", err)
	}
	if len(r.Tools()) != 0 {
		t.Errorf("Tools() = %v, want none", r.Tools())
	}
	if r.Unregister("current_time") {
		t.Error("second Unregister reported success")
	}
	if err := r.Register(tools.NewCurrentTimeTool()); err != nil {
		t.Errorf("Register after Unregister: %v", err)
	}
}
