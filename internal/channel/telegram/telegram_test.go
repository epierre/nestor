package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/testutil"
)

const (
	testToken  = "test-token"
	senderID   = 100
	chatID     = 500
	internalID = identity.UserID(7)
)

// botAPI fakes the Telegram Bot API: it keeps updates until a getUpdates offset confirms them.
type botAPI struct {
	*httptest.Server

	mu        sync.Mutex
	pending   []update
	offsets   []int64
	limits    []int
	sent      []sentMessage
	pollTimes []time.Time
	failPolls int // getUpdates calls left to answer with 502
	failSends int // sendMessage calls left to answer with sendCode
	sendCode  int
}

type sentMessage struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

func newBotAPI(t *testing.T, updates ...update) *botAPI {
	t.Helper()
	api := &botAPI{pending: updates}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bot"+testToken+"/getUpdates", api.getUpdates)
	mux.HandleFunc("POST /bot"+testToken+"/sendMessage", api.sendMessage)
	api.Server = httptest.NewServer(mux)
	t.Cleanup(api.Close)
	return api
}

func (b *botAPI) getUpdates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Offset int64 `json:"offset"`
		Limit  int   `json:"limit"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.pollTimes = append(b.pollTimes, time.Now())
	if b.failPolls > 0 {
		b.failPolls--
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	b.offsets = append(b.offsets, req.Offset)
	b.limits = append(b.limits, req.Limit)
	var kept []update
	for _, u := range b.pending {
		if u.UpdateID >= req.Offset {
			kept = append(kept, u)
		}
	}
	b.pending = kept
	result := kept[:min(len(kept), req.Limit)]
	if len(result) == 0 {
		// Stands in for the long poll, without holding the test up.
		time.Sleep(time.Millisecond)
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": append([]update{}, result...)})
}

func (b *botAPI) sendMessage(w http.ResponseWriter, r *http.Request) {
	var msg sentMessage
	json.NewDecoder(r.Body).Decode(&msg)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failSends > 0 {
		b.failSends--
		w.WriteHeader(b.sendCode)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": b.sendCode, "description": "refused"})
		return
	}
	b.sent = append(b.sent, msg)
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
}

func (b *botAPI) sentTexts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	texts := make([]string, 0, len(b.sent))
	for _, m := range b.sent {
		texts = append(texts, m.Text)
	}
	return texts
}

func (b *botAPI) confirmed(id int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, o := range b.offsets {
		if o > id {
			return true
		}
	}
	return false
}

func textUpdate(id int64, from int64, text string) update {
	return update{UpdateID: id, Message: &message{
		Text: text,
		From: &sender{ID: from},
		Chat: chat{ID: chatID},
	}}
}

// handlerFunc adapts a function to channel.Handler.
type handlerFunc func(ctx context.Context, text string) (string, error)

func (f handlerFunc) HandleMessage(ctx context.Context, text string) (string, error) {
	return f(ctx, text)
}

func echo() handlerFunc {
	return func(_ context.Context, text string) (string, error) { return "echo " + text, nil }
}

func knownSender() *testutil.FakeStore {
	st := testutil.NewFakeStore()
	st.AddIdentity(platform, strconv.Itoa(senderID), internalID)
	return st
}

func newChannel(api *botAPI, identities Identities) *Channel {
	c := New(testToken, identities)
	c.baseURL = api.URL
	c.minBackoff = time.Millisecond
	c.maxBackoff = 4 * time.Millisecond
	return c
}

// runUntil runs c until done reports true, failing the test if that takes too long.
func runUntil(t *testing.T, c *Channel, h channel.Handler, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- c.Run(ctx, h) }()

	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Error("timed out waiting for the channel")
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-finished; err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestReplySentToTheOriginatingChat(t *testing.T) {
	api := newBotAPI(t, textUpdate(1, senderID, "hello"))

	runUntil(t, newChannel(api, knownSender()), echo(), func() bool { return api.confirmed(1) })

	want := []sentMessage{{ChatID: chatID, Text: "echo hello"}}
	if len(api.sent) != 1 || api.sent[0] != want[0] {
		t.Errorf("sent %+v, want %+v", api.sent, want)
	}
}

func TestUpdatesHandledOneAtATime(t *testing.T) {
	api := newBotAPI(t, textUpdate(1, senderID, "one"), textUpdate(2, senderID, "two"))

	runUntil(t, newChannel(api, knownSender()), echo(), func() bool { return api.confirmed(2) })

	if got, want := strings.Join(api.sentTexts(), ","), "echo one,echo two"; got != want {
		t.Errorf("sent %q, want %q", got, want)
	}
	for _, limit := range api.limits {
		if limit != 1 {
			t.Errorf("polled with limit %d, want 1: a batch would be re-delivered whole after a crash", limit)
		}
	}
}

func TestOffsetAdvancedOnlyAfterHandling(t *testing.T) {
	api := newBotAPI(t, textUpdate(1, senderID, "hello"))
	var confirmedDuringHandling bool
	handler := handlerFunc(func(context.Context, string) (string, error) {
		confirmedDuringHandling = api.confirmed(1)
		return "done", nil
	})

	runUntil(t, newChannel(api, knownSender()), handler, func() bool { return api.confirmed(1) })

	if confirmedDuringHandling {
		t.Error("update was confirmed before it was handled")
	}
}

func TestUpdateReDeliveredAfterCrash(t *testing.T) {
	api := newBotAPI(t, textUpdate(1, senderID, "hello"))
	ctx, crash := context.WithCancel(context.Background())
	defer crash()
	crashing := handlerFunc(func(ctx context.Context, _ string) (string, error) {
		crash()
		return "", ctx.Err()
	})

	if err := newChannel(api, knownSender()).Run(ctx, crashing); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if api.confirmed(1) {
		t.Fatal("update was confirmed although handling never finished")
	}
	if got := api.sentTexts(); len(got) != 0 {
		t.Fatalf("sent %q before the crash", got)
	}

	var handled []string
	restarted := handlerFunc(func(_ context.Context, text string) (string, error) {
		handled = append(handled, text)
		return "done", nil
	})
	runUntil(t, newChannel(api, knownSender()), restarted, func() bool { return api.confirmed(1) })

	if len(handled) != 1 || handled[0] != "hello" {
		t.Errorf("after restart handled %q, want hello once", handled)
	}
}

func TestBackoffOn502(t *testing.T) {
	api := newBotAPI(t, textUpdate(1, senderID, "hello"))
	api.failPolls = 5
	c := newChannel(api, knownSender())
	c.minBackoff = 4 * time.Millisecond
	c.maxBackoff = 16 * time.Millisecond

	runUntil(t, c, echo(), func() bool { return api.confirmed(1) })

	if got := api.sentTexts(); len(got) != 1 {
		t.Errorf("sent %q, want one reply", got)
	}
	times := api.pollTimes
	if len(times) < 6 {
		t.Fatalf("polled %d times, want at least 6", len(times))
	}
	for i, want := range []time.Duration{4, 8, 16, 16, 16} {
		want *= time.Millisecond
		if gap := times[i+1].Sub(times[i]); gap < want {
			t.Errorf("poll %d came after %v, want at least %v", i+1, gap, want)
		}
	}
}

func TestBackoffGrowsToItsCap(t *testing.T) {
	c := New(testToken, nil)
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{6, 32 * time.Second},
		{7, time.Minute},
		{1000, time.Minute},
	}
	for _, tt := range tests {
		if got := c.backoff(tt.failures); got != tt.want {
			t.Errorf("backoff(%d) = %v, want %v", tt.failures, got, tt.want)
		}
	}
}

func TestSendRetriesTransientFailures(t *testing.T) {
	for _, code := range []int{http.StatusBadGateway, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			api := newBotAPI(t, textUpdate(1, senderID, "hello"))
			api.failSends, api.sendCode = 2, code
			handled := 0
			handler := handlerFunc(func(context.Context, string) (string, error) {
				handled++
				return "done", nil
			})

			runUntil(t, newChannel(api, knownSender()), handler, func() bool { return api.confirmed(1) })

			if got := api.sentTexts(); len(got) != 1 {
				t.Errorf("sent %q, want one reply", got)
			}
			if handled != 1 {
				t.Errorf("handled the message %d times, want once: only the send is retried", handled)
			}
		})
	}
}

func TestPermanentSendFailureDoesNotBlockTheQueue(t *testing.T) {
	api := newBotAPI(t, textUpdate(1, senderID, "blocked"), textUpdate(2, senderID, "next"))
	api.failSends, api.sendCode = 1, http.StatusForbidden

	runUntil(t, newChannel(api, knownSender()), echo(), func() bool { return api.confirmed(2) })

	if got, want := strings.Join(api.sentTexts(), ","), "echo next"; got != want {
		t.Errorf("sent %q, want %q", got, want)
	}
}

func TestHandlerFailureSendsNoticeAndAcknowledges(t *testing.T) {
	api := newBotAPI(t, textUpdate(1, senderID, "hello"))
	failing := handlerFunc(func(context.Context, string) (string, error) {
		return "", errors.New("store down")
	})

	runUntil(t, newChannel(api, knownSender()), failing, func() bool { return api.confirmed(1) })

	if got := api.sentTexts(); len(got) != 1 || got[0] != noticeFailure {
		t.Errorf("sent %q, want the failure notice", got)
	}
}

type brokenIdentities struct{ calls *atomic.Int32 }

func (b brokenIdentities) ResolveIdentity(context.Context, string, string) (identity.UserID, error) {
	b.calls.Add(1)
	return 0, errors.New("database down")
}

func TestUnresolvableSenderIsRetriedNotAnswered(t *testing.T) {
	api := newBotAPI(t, textUpdate(1, senderID, "hello"))
	calls := &atomic.Int32{}

	runUntil(t, newChannel(api, brokenIdentities{calls}), echo(), func() bool { return calls.Load() >= 3 })

	if api.confirmed(1) {
		t.Error("update was confirmed although its sender could not be resolved")
	}
	if got := api.sentTexts(); len(got) != 0 {
		t.Errorf("sent %q to a sender who may be a stranger", got)
	}
}

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	api := newBotAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := newChannel(api, knownSender()).Run(ctx, echo()); err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestErrorsNeverContainTheToken(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	api := newBotAPI(t)
	c := newChannel(api, knownSender())
	api.Close() // every request now fails with a transport error, which quotes the URL
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := c.Run(ctx, echo()); err != nil {
		t.Errorf("Run: %v", err)
	}

	_, err := c.call(context.Background(), "getMe", struct{}{})
	if err == nil {
		t.Fatal("call succeeded, want error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error %q contains the token", err)
	}
	if !strings.Contains(logs.String(), "poll failed") {
		t.Errorf("logs %q do not show the failure", logs.String())
	}
	if strings.Contains(logs.String(), testToken) {
		t.Errorf("logs contain the token: %s", logs.String())
	}
}
