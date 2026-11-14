package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/tools"
)

type dummyTool struct {
	name        string
	description string
	schema      json.RawMessage
	risk        tools.Risk
}

func (d *dummyTool) Name() string                                 { return d.name }
func (d *dummyTool) Description() string                          { return d.description }
func (d *dummyTool) Schema() json.RawMessage                      { return d.schema }
func (d *dummyTool) Risk() tools.Risk                             { return d.risk }
func (d *dummyTool) Preview(args json.RawMessage) (string, error) { return "", nil }
func (d *dummyTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	return tools.Result{Content: "ok"}, nil
}

func TestRegistry(t *testing.T) {
	t.Run("lists only registered tools", func(t *testing.T) {
		r := tools.NewRegistry()
		t1 := &dummyTool{
			name:        "time",
			description: "returns time",
			schema:      json.RawMessage(`{"type":"object"}`),
			risk:        tools.RiskRead,
		}
		t2 := &dummyTool{
			name:        "notify",
			description: "sends notification",
			schema:      json.RawMessage(`{"type":"object"}`),
			risk:        tools.RiskCritical,
		}

		if err := r.Register(t1); err != nil {
			t.Fatalf("Register t1 failed: %v", err)
		}
		if err := r.Register(t2); err != nil {
			t.Fatalf("Register t2 failed: %v", err)
		}

		got := r.Tools()
		want := []model.Tool{
			{Name: "time", Description: "returns time", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "notify", Description: "sends notification", Parameters: json.RawMessage(`{"type":"object"}`)},
		}

		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("unknown tool lookup fails", func(t *testing.T) {
		r := tools.NewRegistry()
		t1 := &dummyTool{name: "time", risk: tools.RiskRead}
		if err := r.Register(t1); err != nil {
			t.Fatalf("Register t1 failed: %v", err)
		}

		// Known tool succeeds
		got, err := r.Lookup("time")
		if err != nil {
			t.Fatalf("expected time to be found, got %v", err)
		}
		if got.Name() != "time" {
			t.Errorf("got name %q, want 'time'", got.Name())
		}

		// Unknown tool fails
		_, err = r.Lookup("nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent tool lookup, got nil")
		}
		if !errors.Is(err, tools.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("missing required arg returns validation error", func(t *testing.T) {
		r := tools.NewRegistry()
		notifyTool := &dummyTool{
			name:        "notify_self",
			description: "send notification",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"text": {"type": "string"}
				},
				"required": ["text"]
			}`),
			risk: tools.RiskCritical,
		}
		if err := r.Register(notifyTool); err != nil {
			t.Fatalf("Register failed: %v", err)
		}

		// Calling with valid args succeeds
		if err := r.Validate("notify_self", json.RawMessage(`{"text": "hello"}`)); err != nil {
			t.Errorf("expected valid args to pass, got: %v", err)
		}

		// Calling with missing required arg returns validation error
		err := r.Validate("notify_self", json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("expected validation error for missing required arg, got nil")
		}
		if !errors.Is(err, tools.ErrValidation) {
			t.Errorf("expected ErrValidation, got: %v", err)
		}
	})
}
