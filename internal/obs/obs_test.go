package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/epierre/nestor/internal/identity"
)

func decode(t *testing.T, logs *bytes.Buffer) map[string]any {
	t.Helper()
	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("decode %q: %v", logs, err)
	}
	return line
}

func newLogger(logs *bytes.Buffer) *slog.Logger {
	return slog.New(NewHandler(slog.NewJSONHandler(logs, nil)))
}

func TestHandlerAddsIDsFromContext(t *testing.T) {
	tests := []struct {
		name    string
		ctx     context.Context
		request any
		user    any
	}{
		{"neither", context.Background(), nil, nil},
		{"request only", WithRequestID(context.Background(), "r1"), "r1", nil},
		{"user only", identity.WithUser(context.Background(), 7), nil, float64(7)},
		{"both", identity.WithUser(WithRequestID(context.Background(), "r1"), 7), "r1", float64(7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer

			newLogger(&logs).InfoContext(tt.ctx, "hello", "key", "value")

			line := decode(t, &logs)
			if line["request_id"] != tt.request || line["user_id"] != tt.user {
				t.Errorf("got request %v, user %v in %v", line["request_id"], line["user_id"], line)
			}
			if line["msg"] != "hello" || line["key"] != "value" {
				t.Errorf("line lost its own fields: %v", line)
			}
		})
	}
}

func TestHandlerKeepsWorkingAfterWith(t *testing.T) {
	ctx := identity.WithUser(WithRequestID(context.Background(), "r1"), 7)

	t.Run("attrs", func(t *testing.T) {
		var logs bytes.Buffer

		newLogger(&logs).With("component", "test").InfoContext(ctx, "hello")

		line := decode(t, &logs)
		if line["request_id"] != "r1" || line["component"] != "test" {
			t.Errorf("got %v", line)
		}
	})
	t.Run("group keeps IDs at the top level", func(t *testing.T) {
		var logs bytes.Buffer

		newLogger(&logs).WithGroup("g").InfoContext(ctx, "hello", "key", "value")

		line := decode(t, &logs)
		if _, nested := line["g"].(map[string]any)["request_id"]; nested {
			t.Errorf("request ID moved into the group: %v", line)
		}
		if line["request_id"] != "r1" || line["user_id"] != float64(7) {
			t.Errorf("got %v", line)
		}
	})
}

func TestRequestIDsDiffer(t *testing.T) {
	first, second := NewRequestID(), NewRequestID()

	if first == "" || first == second {
		t.Errorf("got %q and %q", first, second)
	}
}
