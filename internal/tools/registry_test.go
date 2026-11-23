package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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

func TestValidateStrictJSON(t *testing.T) {
	r := tools.NewRegistry()
	withSchema := &dummyTool{
		name: "with_schema",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "integer"},
				"outer": {"type": "object"},
				"items": {"type": "array"}
			},
			"additionalProperties": true
		}`),
		risk: tools.RiskRead,
	}
	noSchema := &dummyTool{
		name: "no_schema",
		risk: tools.RiskRead,
	}
	if err := r.Register(withSchema); err != nil {
		t.Fatalf("Register withSchema: %v", err)
	}
	if err := r.Register(noSchema); err != nil {
		t.Fatalf("Register noSchema: %v", err)
	}

	tests := []struct {
		name    string
		tool    string
		args    json.RawMessage
		wantErr bool
	}{
		{
			name:    "top-level duplicate keys",
			tool:    "with_schema",
			args:    json.RawMessage(`{"secret_key":"secret_val","secret_key":"other"}`),
			wantErr: true,
		},
		{
			name:    "nested object duplicate keys",
			tool:    "with_schema",
			args:    json.RawMessage(`{"outer":{"secret_key":1,"secret_key":2}}`),
			wantErr: true,
		},
		{
			name:    "object inside array duplicate keys",
			tool:    "with_schema",
			args:    json.RawMessage(`{"items":[{"secret_key":1,"secret_key":2}]}`),
			wantErr: true,
		},
		{
			name:    "unicode-escaped duplicate keys",
			tool:    "with_schema",
			args:    json.RawMessage(`{"id":1,"\u0069d":2}`),
			wantErr: true,
		},
		{
			name:    "non-object root array",
			tool:    "with_schema",
			args:    json.RawMessage(`["secret_val"]`),
			wantErr: true,
		},
		{
			name:    "non-object root string",
			tool:    "with_schema",
			args:    json.RawMessage(`"secret_val"`),
			wantErr: true,
		},
		{
			name:    "non-object root number",
			tool:    "with_schema",
			args:    json.RawMessage(`123`),
			wantErr: true,
		},
		{
			name:    "non-object root bool",
			tool:    "with_schema",
			args:    json.RawMessage(`true`),
			wantErr: true,
		},
		{
			name:    "non-object root null",
			tool:    "with_schema",
			args:    json.RawMessage(`null`),
			wantErr: true,
		},
		{
			name:    "trailing object token",
			tool:    "with_schema",
			args:    json.RawMessage(`{}{}`),
			wantErr: true,
		},
		{
			name:    "trailing scalar token",
			tool:    "with_schema",
			args:    json.RawMessage(`{"id":1} 2`),
			wantErr: true,
		},
		{
			name:    "trailing garbage token",
			tool:    "with_schema",
			args:    json.RawMessage(`{"id":1} trailing`),
			wantErr: true,
		},
		{
			name:    "malformed unclosed",
			tool:    "with_schema",
			args:    json.RawMessage(`{`),
			wantErr: true,
		},
		{
			name:    "malformed missing value",
			tool:    "with_schema",
			args:    json.RawMessage(`{"secret_key":}`),
			wantErr: true,
		},
		{
			name:    "malformed trailing comma",
			tool:    "with_schema",
			args:    json.RawMessage(`{"secret_key":1,}`),
			wantErr: true,
		},
		{
			name:    "nil schema rejects duplicate keys",
			tool:    "no_schema",
			args:    json.RawMessage(`{"secret_key":"secret_val","secret_key":"other"}`),
			wantErr: true,
		},
		{
			name:    "nil schema rejects non-object root",
			tool:    "no_schema",
			args:    json.RawMessage(`["secret_val"]`),
			wantErr: true,
		},
		{
			name:    "nil schema rejects malformed JSON",
			tool:    "no_schema",
			args:    json.RawMessage(`{bad`),
			wantErr: true,
		},
		{
			name:    "nil schema rejects trailing tokens",
			tool:    "no_schema",
			args:    json.RawMessage(`{}{}`),
			wantErr: true,
		},
		{
			name:    "nil args normalized with schema",
			tool:    "with_schema",
			args:    nil,
			wantErr: false,
		},
		{
			name:    "empty args normalized with schema",
			tool:    "with_schema",
			args:    json.RawMessage(``),
			wantErr: false,
		},
		{
			name:    "whitespace args normalized with schema",
			tool:    "with_schema",
			args:    json.RawMessage("  \n\t "),
			wantErr: false,
		},
		{
			name:    "empty args normalized without schema",
			tool:    "no_schema",
			args:    json.RawMessage(" \t "),
			wantErr: false,
		},
		{
			name:    "valid nested JSON passes",
			tool:    "with_schema",
			args:    json.RawMessage(`{"id":42,"outer":{"a":1,"b":[true,null,"x",{"k":"v"}]},"items":[{"x":1},{"x":2}]}`),
			wantErr: false,
		},
		{
			name:    "valid nested JSON passes without schema",
			tool:    "no_schema",
			args:    json.RawMessage(`{"a":[{"b":1},{"b":2}]}`),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.Validate(tt.tool, tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected validation error, got nil")
				}
				if !errors.Is(err, tools.ErrValidation) {
					t.Errorf("err = %v, want ErrValidation", err)
				}
				for _, secret := range []string{"secret_key", "secret_val"} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("validation error %q leaked argument content %q", err.Error(), secret)
					}
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
