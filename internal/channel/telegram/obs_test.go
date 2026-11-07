package telegram

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/epierre/nestor/internal/agent"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/obs"
	"github.com/epierre/nestor/internal/testutil"
)

// lockedBuffer lets the test read logs while the channel is still writing them.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var lines []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(b.buf.Bytes()), []byte("\n")) {
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

func jsonLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(obs.NewHandler(slog.NewJSONHandler(logs, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

func newAssistant(t *testing.T, st *testutil.FakeStore) *agent.Agent {
	t.Helper()
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{Content: "secret reply"}, nil
	}}
	return agent.New(client, st, agent.Options{Model: "cheap", HistoryLimit: 10, MaxIterations: 2})
}

func messageLines(lines []map[string]any, msg string) []map[string]any {
	var found []map[string]any
	for _, line := range lines {
		if line["msg"] == msg {
			found = append(found, line)
		}
	}
	return found
}

func TestLogLinesShareRequestIDAndUserID(t *testing.T) {
	logs := jsonLogs(t)
	st := knownSender()
	api := newBotAPI(t, textUpdate(1, senderID, "secret question"))

	runUntil(t, newChannel(api, st), newAssistant(t, st), func() bool { return api.confirmed(1) })

	lines := logs.lines(t)
	var requestIDs []any
	for _, msg := range []string{"message received", "model call finished", "reply sent"} {
		found := messageLines(lines, msg)
		if len(found) != 1 {
			t.Fatalf("got %d %q lines in %v", len(found), msg, lines)
		}
		if found[0]["user_id"] != float64(internalID) {
			t.Errorf("%q has user_id %v, want %d", msg, found[0]["user_id"], internalID)
		}
		requestIDs = append(requestIDs, found[0]["request_id"])
	}
	if requestIDs[0] == nil || requestIDs[0] == "" || requestIDs[0] != requestIDs[1] || requestIDs[1] != requestIDs[2] {
		t.Errorf("request IDs differ: %v", requestIDs)
	}
}

func TestEachMessageGetsItsOwnRequestID(t *testing.T) {
	logs := jsonLogs(t)
	st := knownSender()
	api := newBotAPI(t, textUpdate(1, senderID, "one"), textUpdate(2, senderID, "two"))

	runUntil(t, newChannel(api, st), newAssistant(t, st), func() bool { return api.confirmed(2) })

	received := messageLines(logs.lines(t), "message received")
	if len(received) != 2 {
		t.Fatalf("got %d lines, want 2", len(received))
	}
	if received[0]["request_id"] == received[1]["request_id"] {
		t.Errorf("both messages have request ID %v", received[0]["request_id"])
	}
}

func TestLogsCarryNoMessageContent(t *testing.T) {
	logs := jsonLogs(t)
	st := knownSender()
	api := newBotAPI(t, textUpdate(1, senderID, "secret question"))

	runUntil(t, newChannel(api, st), newAssistant(t, st), func() bool { return api.confirmed(1) })

	for _, line := range logs.lines(t) {
		encoded, _ := json.Marshal(line)
		if strings.Contains(string(encoded), "secret") {
			t.Errorf("log line carries message content: %s", encoded)
		}
	}
}

func TestStrangerLineHasRequestIDButNoUserID(t *testing.T) {
	logs := jsonLogs(t)
	api := newBotAPI(t, textUpdate(1, 666, "hello"))

	runUntil(t, newChannel(api, knownSender()), &handled{}, func() bool { return api.confirmed(1) })

	found := messageLines(logs.lines(t), "ignored message from unknown sender")
	if len(found) != 1 {
		t.Fatalf("got %d lines, want 1", len(found))
	}
	if found[0]["request_id"] == nil {
		t.Errorf("line %v has no request ID", found[0])
	}
	if _, ok := found[0]["user_id"]; ok {
		t.Errorf("line %v has a user ID", found[0])
	}
}
