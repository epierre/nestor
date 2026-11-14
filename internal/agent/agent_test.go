package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
	"github.com/epierre/nestor/internal/tools"
)

var _ channel.Handler = (*Agent)(nil)

const (
	testHistoryLimit  = 30
	testMaxIterations = 8
)

func testOptions() Options {
	return Options{
		Model:         "cheap",
		SystemPrompt:  "be helpful",
		HistoryLimit:  testHistoryLimit,
		MaxIterations: testMaxIterations,
	}
}

func newAgent(client model.ModelClient) *Agent {
	return New(client, testutil.NewFakeStore(), testOptions())
}

// numberedReplies answers "reply 1", "reply 2" and so on.
func numberedReplies() *testutil.FakeModel {
	n := 0
	return &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		n++
		return model.CompletionResponse{Content: fmt.Sprintf("reply %d", n)}, nil
	}}
}

func send(t *testing.T, a *Agent, user identity.UserID, text string) string {
	t.Helper()
	reply, err := a.HandleMessage(identity.WithUser(context.Background(), user), text)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	return reply
}

func lastRequest(t *testing.T, client *testutil.FakeModel) model.CompletionRequest {
	t.Helper()
	requests := client.Requests()
	if len(requests) == 0 {
		t.Fatal("no model request was made")
	}
	return requests[len(requests)-1]
}

func text(role model.Role, content string) model.Message {
	return model.Message{Role: role, Content: content}
}

func TestRepliesWithFakeModelOutput(t *testing.T) {
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{Content: "hello back"}, nil
	}}
	ch := &testutil.FakeChannel{User: 1, ChatID: 100, Inbound: []string{"hello"}}

	if err := ch.Run(context.Background(), newAgent(client)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got, want := ch.Sent(), []string{"hello back"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sent %q, want %q", got, want)
	}
	want := []model.CompletionRequest{{
		Model: "cheap",
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: "be helpful"},
			{Role: model.RoleUser, Content: "hello"},
		},
	}}
	if got := client.Requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests %+v, want %+v", got, want)
	}
}

func TestHandleMessageWithoutUserMakesNoModelCall(t *testing.T) {
	client := &testutil.FakeModel{}

	_, err := newAgent(client).HandleMessage(context.Background(), "hello")

	if !errors.Is(err, identity.ErrNoUser) {
		t.Errorf("got error %v, want ErrNoUser", err)
	}
	if got := client.Requests(); len(got) != 0 {
		t.Errorf("made %d model calls, want 0", len(got))
	}
}

func TestModelErrorYieldsNotice(t *testing.T) {
	const user identity.UserID = 1
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{}, errors.New("boom")
	}}
	st := testutil.NewFakeStore()

	reply := send(t, New(client, st, testOptions()), user, "hello")

	if reply != noticeModelFailure {
		t.Errorf("got reply %q, want %q", reply, noticeModelFailure)
	}
	got := stored(t, st, user)
	if len(got) != 1 {
		t.Fatalf("stored %d messages, want only the user's", len(got))
	}
	assertStored(t, got[0], user, model.RoleUser, "hello")
}

func TestRunawayLoopStopsAtMax(t *testing.T) {
	const user identity.UserID = 1
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{ToolCalls: []model.ToolCall{{ID: "call_1", Name: "anything"}}}, nil
	}}
	st := testutil.NewFakeStore()

	reply := send(t, New(client, st, testOptions()), user, "hello")

	if got := len(client.Requests()); got != testMaxIterations {
		t.Errorf("made %d model calls, want %d", got, testMaxIterations)
	}
	if reply != noticeLoopLimit {
		t.Errorf("got reply %q, want %q", reply, noticeLoopLimit)
	}
	if got := stored(t, st, user); len(got) != 1 {
		t.Errorf("stored %d messages, want only the user's", len(got))
	}
}

func TestToolCallIsAnsweredWithToolRoleError(t *testing.T) {
	call := model.ToolCall{ID: "call_1", Name: "anything"}
	turns := []model.CompletionResponse{{ToolCalls: []model.ToolCall{call}}, {Content: "sorry"}}
	client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
		return turns[(len(req.Messages)-2)/2], nil
	}}

	reply := send(t, newAgent(client), 1, "hello")

	if reply != "sorry" {
		t.Errorf("got reply %q, want %q", reply, "sorry")
	}
	want := []model.Message{
		text(model.RoleSystem, "be helpful"),
		text(model.RoleUser, "hello"),
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{call}},
		{Role: model.RoleTool, ToolCallID: "call_1", Content: toolsUnavailable},
	}
	if got := lastRequest(t, client).Messages; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// brokenStore fails the way a store does when the database goes away.
type brokenStore struct {
	store.Store
}

var errStoreDown = errors.New("store down")

func (brokenStore) ActiveConversation(context.Context, identity.UserID) (store.Conversation, error) {
	return store.Conversation{}, errStoreDown
}

func TestStoreFailureReturnsError(t *testing.T) {
	client := numberedReplies()
	a := New(client, brokenStore{}, testOptions())

	_, err := a.HandleMessage(identity.WithUser(context.Background(), 1), "hello")

	if !errors.Is(err, errStoreDown) {
		t.Errorf("got error %v, want %v", err, errStoreDown)
	}
	if got := len(client.Requests()); got != 0 {
		t.Errorf("made %d model calls, want 0", got)
	}
}

func TestHistoryWithinLimit(t *testing.T) {
	client := numberedReplies()
	a := newAgent(client)
	send(t, a, 1, "one")
	send(t, a, 1, "two")

	send(t, a, 1, "three")

	want := []model.Message{
		text(model.RoleSystem, "be helpful"),
		text(model.RoleUser, "one"),
		text(model.RoleAssistant, "reply 1"),
		text(model.RoleUser, "two"),
		text(model.RoleAssistant, "reply 2"),
		text(model.RoleUser, "three"),
	}
	if got := lastRequest(t, client).Messages; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestHistoryBeyondLimit(t *testing.T) {
	const user identity.UserID = 1
	ctx := identity.WithUser(context.Background(), user)
	st := testutil.NewFakeStore()
	conversation, err := st.ActiveConversation(ctx, user)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	for i := range 50 {
		if _, err := st.AppendMessage(ctx, user, conversation.ID, model.RoleUser, fmt.Sprintf("old %d", i)); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	client := numberedReplies()

	send(t, New(client, st, testOptions()), user, "new")

	want := []model.Message{text(model.RoleSystem, "be helpful")}
	for i := 21; i < 50; i++ {
		want = append(want, text(model.RoleUser, fmt.Sprintf("old %d", i)))
	}
	want = append(want, text(model.RoleUser, "new"))
	if got := lastRequest(t, client).Messages; !reflect.DeepEqual(got, want) {
		t.Errorf("got %d messages %+v, want %d %+v", len(got), got, len(want), want)
	}
}

func TestRestartKeepsContext(t *testing.T) {
	st := testutil.NewFakeStore()
	send(t, New(numberedReplies(), st, testOptions()), 1, "remember this")

	client := numberedReplies()
	send(t, New(client, st, testOptions()), 1, "what did I say?")

	want := []model.Message{
		text(model.RoleSystem, "be helpful"),
		text(model.RoleUser, "remember this"),
		text(model.RoleAssistant, "reply 1"),
		text(model.RoleUser, "what did I say?"),
	}
	if got := lastRequest(t, client).Messages; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestTwoUsersNeverSeeEachOthersMessages(t *testing.T) {
	client := numberedReplies()
	a := newAgent(client)

	send(t, a, 1, "my code word is falcon")
	send(t, a, 2, "what is my code word?")

	want := []model.Message{
		text(model.RoleSystem, "be helpful"),
		text(model.RoleUser, "what is my code word?"),
	}
	if got := lastRequest(t, client).Messages; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestPersistsUserMessageAndReply(t *testing.T) {
	const user identity.UserID = 1
	st := testutil.NewFakeStore()

	send(t, New(numberedReplies(), st, testOptions()), user, "hello")

	got := stored(t, st, user)
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	assertStored(t, got[0], user, model.RoleUser, "hello")
	assertStored(t, got[1], user, model.RoleAssistant, "reply 1")
}

func stored(t *testing.T, st *testutil.FakeStore, user identity.UserID) []store.Message {
	t.Helper()
	ctx := identity.WithUser(context.Background(), user)
	conversation, err := st.ActiveConversation(ctx, user)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	messages, err := st.RecentMessages(ctx, user, conversation.ID, 100)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	return messages
}

func assertStored(t *testing.T, m store.Message, user identity.UserID, role model.Role, content string) {
	t.Helper()
	if m.UserID != user || m.Role != role || m.Content != content || m.CreatedAt.IsZero() {
		t.Errorf("got message %+v, want %s %q of user %d", m, role, content, user)
	}
}

const testAPIKey = "test-token"

// proxyAgent returns an agent using the real model client against a server that answers every
// request with status and body, and a counter of the requests it received.
func proxyAgent(t *testing.T, st *testutil.FakeStore, status int, body string) (*Agent, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return New(model.NewClient(server.URL, testAPIKey), st, testOptions()), requests
}

func TestCallRecordedOnSuccess(t *testing.T) {
	const user identity.UserID = 1
	cost := 0.25
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{
			Content: "hello back",
			Usage:   model.Usage{PromptTokens: 12, CompletionTokens: 3, CostUSD: &cost},
		}, nil
	}}
	st := testutil.NewFakeStore()

	send(t, New(client, st, testOptions()), user, "hello")

	calls := st.ModelCalls(user)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	got := calls[0]
	if got.UserID != user || got.ConversationID == 0 || got.Model != "cheap" || got.StartedAt.IsZero() ||
		got.Latency <= 0 || got.Status != store.CallStatusOK || got.Error != "" {
		t.Errorf("got call %+v", got)
	}
	if got.PromptTokens != 12 || got.CompletionTokens != 3 || got.CostUSD == nil || *got.CostUSD != cost {
		t.Errorf("got usage %d, %d, %v", got.PromptTokens, got.CompletionTokens, got.CostUSD)
	}
	if !json.Valid(got.RequestJSON) || !strings.Contains(string(got.RequestJSON), "hello") {
		t.Errorf("got request %s", got.RequestJSON)
	}
	if !json.Valid(got.ResponseJSON) || !strings.Contains(string(got.ResponseJSON), "hello back") {
		t.Errorf("got response %s", got.ResponseJSON)
	}
}

func TestCallRecordedOnFailure(t *testing.T) {
	const user identity.UserID = 1
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{Attempts: []model.Attempt{{
			StartedAt: time.Now(),
			Latency:   30 * time.Second,
			Request:   []byte(`{"model":"cheap"}`),
			Err:       context.DeadlineExceeded,
		}}}, context.DeadlineExceeded
	}}
	st := testutil.NewFakeStore()

	reply := send(t, New(client, st, testOptions()), user, "hello")

	if reply != noticeModelFailure {
		t.Errorf("got reply %q, want the failure notice", reply)
	}
	calls := st.ModelCalls(user)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	got := calls[0]
	if got.Status != store.CallStatusError || got.Error != context.DeadlineExceeded.Error() {
		t.Errorf("got status %q, error %q", got.Status, got.Error)
	}
	if string(got.RequestJSON) != `{"model":"cheap"}` {
		t.Errorf("got request %s", got.RequestJSON)
	}
	if got.ResponseJSON != nil {
		t.Errorf("got response %s, want none", got.ResponseJSON)
	}
	if got.CostUSD != nil {
		t.Errorf("got cost %v, want null", *got.CostUSD)
	}
}

func TestCallWithoutReportedCostStaysNull(t *testing.T) {
	st := testutil.NewFakeStore()

	send(t, New(numberedReplies(), st, testOptions()), 1, "hello")

	if got := st.ModelCalls(1); len(got) != 1 || got[0].CostUSD != nil {
		t.Errorf("got calls %+v, want one with null cost", got)
	}
}

func TestProviderReturns503Twice(t *testing.T) {
	const user identity.UserID = 1
	st := testutil.NewFakeStore()
	a, requests := proxyAgent(t, st, http.StatusServiceUnavailable, "busy")

	reply := send(t, a, user, "hello")

	if reply != noticeModelFailure {
		t.Errorf("got reply %q, want the failure notice", reply)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("proxy received %d requests, want 2", got)
	}
	calls := st.ModelCalls(user)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	for _, call := range calls {
		if call.Status != store.CallStatusError || !strings.Contains(call.Error, "503") {
			t.Errorf("got status %q, error %q", call.Status, call.Error)
		}
		if want := `"busy"`; string(call.ResponseJSON) != want {
			t.Errorf("got response %s, want the body as a JSON string %s", call.ResponseJSON, want)
		}
	}
}

func TestRetriedCallChargesOnlyTheSuccessfulAttempt(t *testing.T) {
	const user identity.UserID = 1
	cost := 0.25
	failure := &model.StatusError{Code: http.StatusServiceUnavailable}
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{
			Content: "hello back",
			Usage:   model.Usage{PromptTokens: 12, CompletionTokens: 3, CostUSD: &cost},
			Attempts: []model.Attempt{
				{StartedAt: time.Now(), Latency: time.Millisecond, Request: []byte(`{}`), Err: failure},
				{StartedAt: time.Now(), Latency: time.Millisecond, Request: []byte(`{}`), Response: []byte(`{}`)},
			},
		}, nil
	}}
	st := testutil.NewFakeStore()

	if reply := send(t, New(client, st, testOptions()), user, "hello"); reply != "hello back" {
		t.Errorf("got reply %q", reply)
	}

	calls := st.ModelCalls(user)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	failed, succeeded := calls[0], calls[1]
	if failed.Status != store.CallStatusError || failed.PromptTokens != 0 || failed.CostUSD != nil {
		t.Errorf("got failed call %+v", failed)
	}
	if succeeded.Status != store.CallStatusOK || succeeded.PromptTokens != 12 || succeeded.CostUSD == nil {
		t.Errorf("got successful call %+v", succeeded)
	}
}

func TestProviderReturns400(t *testing.T) {
	const user identity.UserID = 1
	st := testutil.NewFakeStore()
	a, requests := proxyAgent(t, st, http.StatusBadRequest, `{"error": {"message": "bad"}}`)

	reply := send(t, a, user, "hello")

	if reply != noticeModelFailure {
		t.Errorf("got reply %q, want the failure notice", reply)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("proxy received %d requests, want 1", got)
	}
	calls := st.ModelCalls(user)
	if len(calls) != 1 || calls[0].Status != store.CallStatusError {
		t.Fatalf("got calls %+v, want one with status error", calls)
	}
	if want := `{"error": {"message": "bad"}}`; string(calls[0].ResponseJSON) != want {
		t.Errorf("got response %s, want %s", calls[0].ResponseJSON, want)
	}
}

func TestEmptyErrorBodyStoredAsNoResponse(t *testing.T) {
	st := testutil.NewFakeStore()
	a, _ := proxyAgent(t, st, http.StatusBadRequest, "")

	send(t, a, 1, "hello")

	if got := st.ModelCalls(1); len(got) != 1 || got[0].ResponseJSON != nil {
		t.Errorf("got calls %+v, want one without a response", got)
	}
}

func TestNoAuthorizationInStoredRequest(t *testing.T) {
	const user identity.UserID = 1
	st := testutil.NewFakeStore()
	a, _ := proxyAgent(t, st, http.StatusUnauthorized, `{"error": {"message": "bad key `+testAPIKey+`"}}`)

	send(t, a, user, "hello")

	calls := st.ModelCalls(user)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	stored, err := json.Marshal(calls[0])
	if err != nil {
		t.Fatalf("encode call: %v", err)
	}
	for _, secret := range []string{testAPIKey, "Authorization", "Bearer"} {
		if strings.Contains(string(stored), secret) {
			t.Errorf("stored call contains %q: %s", secret, stored)
		}
	}
}

func TestRunawayLoopRecordsEveryCall(t *testing.T) {
	const user identity.UserID = 1
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{ToolCalls: []model.ToolCall{{ID: "call_1", Name: "anything"}}}, nil
	}}
	st := testutil.NewFakeStore()

	send(t, New(client, st, testOptions()), user, "hello")

	if got := len(st.ModelCalls(user)); got != testMaxIterations {
		t.Errorf("recorded %d calls, want %d", got, testMaxIterations)
	}
}

func TestCallsRecordedForTheirOwnUser(t *testing.T) {
	st := testutil.NewFakeStore()
	a := New(numberedReplies(), st, testOptions())

	send(t, a, 1, "hello")
	send(t, a, 1, "again")
	send(t, a, 2, "hello")

	if first, second := len(st.ModelCalls(1)), len(st.ModelCalls(2)); first != 2 || second != 1 {
		t.Errorf("got %d and %d calls, want 2 and 1", first, second)
	}
}

// brokenLog accepts everything but model calls.
type brokenLog struct {
	*testutil.FakeStore
}

func (brokenLog) RecordModelCall(context.Context, identity.UserID, store.ModelCall) error {
	return errStoreDown
}

func TestUnrecordableCallReturnsError(t *testing.T) {
	a := New(numberedReplies(), brokenLog{testutil.NewFakeStore()}, testOptions())

	_, err := a.HandleMessage(identity.WithUser(context.Background(), 1), "hello")

	if !errors.Is(err, errStoreDown) {
		t.Errorf("got error %v, want %v", err, errStoreDown)
	}
}

func TestNewClearsOnlyTheSendersHistory(t *testing.T) {
	client := numberedReplies()
	a := newAgent(client)
	send(t, a, 1, "first of A")
	send(t, a, 2, "first of B")

	reply := send(t, a, 1, "/new")

	if reply != noticeNewConversation {
		t.Errorf("got reply %q, want %q", reply, noticeNewConversation)
	}
	if got := len(client.Requests()); got != 2 {
		t.Errorf("made %d model calls, want 2: /new calls no model", got)
	}

	send(t, a, 1, "what did I just say?")
	want := []model.Message{
		text(model.RoleSystem, "be helpful"),
		text(model.RoleUser, "what did I just say?"),
	}
	if got := lastRequest(t, client).Messages; !reflect.DeepEqual(got, want) {
		t.Errorf("user A: got %+v, want %+v", got, want)
	}

	send(t, a, 2, "and now?")
	want = []model.Message{
		text(model.RoleSystem, "be helpful"),
		text(model.RoleUser, "first of B"),
		text(model.RoleAssistant, "reply 2"),
		text(model.RoleUser, "and now?"),
	}
	if got := lastRequest(t, client).Messages; !reflect.DeepEqual(got, want) {
		t.Errorf("user B: got %+v, want %+v", got, want)
	}
}

func TestNewOnlyMatchesTheWholeCommand(t *testing.T) {
	client := numberedReplies()

	if got := send(t, newAgent(client), 1, "/newsletter"); got != "reply 1" {
		t.Errorf("got reply %q, want the model's", got)
	}
}

func TestToolDispatch(t *testing.T) {
	t.Run("read tool executes and result returned", func(t *testing.T) {
		call := model.ToolCall{ID: "call_time", Name: "current_time", Arguments: json.RawMessage(`{}`)}
		turns := []model.CompletionResponse{
			{ToolCalls: []model.ToolCall{call}},
			{Content: "The time is 12:00."},
		}
		client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
			return turns[(len(req.Messages)-2)/2], nil
		}}

		reg := tools.NewRegistry()
		_ = reg.Register(tools.NewCurrentTimeToolWithClock(func() time.Time {
			return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
		}))

		opts := testOptions()
		opts.Tools = reg
		a := New(client, testutil.NewFakeStore(), opts)

		reply := send(t, a, 1, "what time is it?")
		if reply != "The time is 12:00." {
			t.Errorf("got reply %q, want 'The time is 12:00.'", reply)
		}

		req := lastRequest(t, client)
		lastMsg := req.Messages[len(req.Messages)-1]
		if lastMsg.Role != model.RoleTool {
			t.Fatalf("last message role: got %q, want %q", lastMsg.Role, model.RoleTool)
		}
		if lastMsg.ToolCallID != "call_time" {
			t.Errorf("tool call id: got %q, want 'call_time'", lastMsg.ToolCallID)
		}
		if !strings.Contains(lastMsg.Content, "2026-11-14") {
			t.Errorf("tool message content does not contain date: %q", lastMsg.Content)
		}
		if !strings.HasPrefix(lastMsg.Content, "[data]") {
			t.Errorf("tool message content not marked as data: %q", lastMsg.Content)
		}
	})

	t.Run("unknown tool yields error result", func(t *testing.T) {
		call := model.ToolCall{ID: "call_unknown", Name: "delete_all_email", Arguments: json.RawMessage(`{}`)}
		turns := []model.CompletionResponse{
			{ToolCalls: []model.ToolCall{call}},
			{Content: "I cannot do that."},
		}
		client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
			return turns[(len(req.Messages)-2)/2], nil
		}}

		reg := tools.NewRegistry()
		opts := testOptions()
		opts.Tools = reg
		a := New(client, testutil.NewFakeStore(), opts)

		reply := send(t, a, 1, "delete email")
		if reply != "I cannot do that." {
			t.Errorf("got reply %q, want 'I cannot do that.'", reply)
		}

		req := lastRequest(t, client)
		lastMsg := req.Messages[len(req.Messages)-1]
		if lastMsg.Role != model.RoleTool {
			t.Fatalf("last message role: got %q, want %q", lastMsg.Role, model.RoleTool)
		}
		if lastMsg.ToolCallID != "call_unknown" {
			t.Errorf("tool call id: got %q, want 'call_unknown'", lastMsg.ToolCallID)
		}
		if !strings.Contains(lastMsg.Content, "unknown tool") {
			t.Errorf("tool message content does not mention unknown tool: %q", lastMsg.Content)
		}
		if !strings.HasPrefix(lastMsg.Content, "[data]") {
			t.Errorf("tool message content not marked as data: %q", lastMsg.Content)
		}
	})
	t.Run("tool receives requesting user in context", func(t *testing.T) {
		var userInContext identity.UserID
		tool := &testUserTool{
			onExec: func(ctx context.Context) {
				u, _ := identity.UserFrom(ctx)
				userInContext = u
			},
		}

		reg := tools.NewRegistry()
		_ = reg.Register(tool)

		call := model.ToolCall{ID: "call_inspect", Name: "inspect_user", Arguments: json.RawMessage(`{}`)}
		turns := []model.CompletionResponse{
			{ToolCalls: []model.ToolCall{call}},
			{Content: "Done."},
		}
		client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
			return turns[(len(req.Messages)-2)/2], nil
		}}

		opts := testOptions()
		opts.Tools = reg
		st := testutil.NewFakeStore()
		a := New(client, st, opts)

		reply := send(t, a, 42, "inspect my user")
		if reply != "Done." {
			t.Errorf("got reply %q, want 'Done.'", reply)
		}
		if userInContext != 42 {
			t.Errorf("expected tool to receive user 42 in context, got %v", userInContext)
		}

		recorded := st.ToolCalls(42)
		if len(recorded) != 1 {
			t.Fatalf("expected 1 recorded tool call for user 42, got %d", len(recorded))
		}
		if recorded[0].Status != store.ToolCallStatusExecuted {
			t.Errorf("got recorded status %q, want %q", recorded[0].Status, store.ToolCallStatusExecuted)
		}
	})

	t.Run("user_id argument rejected by schema", func(t *testing.T) {
		call := model.ToolCall{
			ID:        "call_exploit",
			Name:      "current_time",
			Arguments: json.RawMessage(`{"user_id": 999}`),
		}
		turns := []model.CompletionResponse{
			{ToolCalls: []model.ToolCall{call}},
			{Content: "Refused."},
		}
		client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
			return turns[(len(req.Messages)-2)/2], nil
		}}

		reg := tools.NewRegistry()
		_ = reg.Register(tools.NewCurrentTimeTool())

		opts := testOptions()
		opts.Tools = reg
		st := testutil.NewFakeStore()
		a := New(client, st, opts)

		reply := send(t, a, 1, "check time for 999")
		if reply != "Refused." {
			t.Errorf("got reply %q, want 'Refused.'", reply)
		}

		req := lastRequest(t, client)
		lastMsg := req.Messages[len(req.Messages)-1]
		if !strings.Contains(lastMsg.Content, "invalid arguments") {
			t.Errorf("expected validation error in tool message, got %q", lastMsg.Content)
		}

		// Tool call was recorded with status rejected for user 1, none for user 999
		calls1 := st.ToolCalls(1)
		if len(calls1) != 1 || calls1[0].Status != store.ToolCallStatusRejected {
			t.Errorf("expected 1 rejected call for user 1, got %+v", calls1)
		}
		calls999 := st.ToolCalls(999)
		if len(calls999) != 0 {
			t.Errorf("expected 0 calls for user 999, got %d", len(calls999))
		}
	})

	t.Run("critical tool never executed by agent", func(t *testing.T) {
		var executed bool
		sender := func(ctx context.Context, uid identity.UserID, text string) error {
			executed = true
			return nil
		}

		reg := tools.NewRegistry()
		_ = reg.Register(tools.NewNotifySelfTool(sender))

		call := model.ToolCall{
			ID:        "call_crit",
			Name:      "notify_self",
			Arguments: json.RawMessage(`{"text": "Dentist at 3pm"}`),
		}
		turns := []model.CompletionResponse{
			{ToolCalls: []model.ToolCall{call}},
			{Content: "Waiting."},
		}
		client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
			return turns[(len(req.Messages)-2)/2], nil
		}}

		opts := testOptions()
		opts.Tools = reg
		st := testutil.NewFakeStore()
		a := New(client, st, opts)

		reply := send(t, a, 1, "notify me")
		if reply != "Waiting." {
			t.Errorf("got reply %q, want 'Waiting.'", reply)
		}

		// Tool was never executed
		if executed {
			t.Error("critical tool must never be executed by agent")
		}

		// Tool obtained from agent's registry view refuses direct execution
		tool, err := a.opts.Tools.Lookup("notify_self")
		if err != nil {
			t.Fatalf("Lookup notify_self: %v", err)
		}
		_, execErr := tool.Execute(identity.WithUser(context.Background(), 1), json.RawMessage(`{"text": "Dentist at 3pm"}`))
		if !errors.Is(execErr, tools.ErrCriticalRefusal) {
			t.Errorf("expected ErrCriticalRefusal from agent view, got: %v", execErr)
		}
	})

	t.Run("critical call becomes pending", func(t *testing.T) {
		var executed bool
		sender := func(ctx context.Context, uid identity.UserID, text string) error {
			executed = true
			return nil
		}
		reg := tools.NewRegistry()
		_ = reg.Register(tools.NewNotifySelfTool(sender))

		call := model.ToolCall{
			ID:        "call_crit",
			Name:      "notify_self",
			Arguments: json.RawMessage(`{"text": "Dentist at 3pm"}`),
		}
		turns := []model.CompletionResponse{
			{ToolCalls: []model.ToolCall{call}},
			{Content: "Waiting for your approval."},
		}
		client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
			return turns[(len(req.Messages)-2)/2], nil
		}}

		st := testutil.NewFakeStore()
		opts := testOptions()
		opts.Tools = reg
		opts.Approvals = approval.New(st, reg, time.Hour)
		a := New(client, st, opts)

		reply := send(t, a, 1, "notify me")
		if reply != "Waiting for your approval." {
			t.Errorf("got reply %q, want the model's words", reply)
		}
		if executed {
			t.Error("critical tool executed without approval")
		}

		pending := st.PendingActions(1)
		if len(pending) != 1 {
			t.Fatalf("got %d pending actions for user 1, want 1", len(pending))
		}
		if got, want := toolResult(t, client, "call_crit"), "pending approval "+pending[0].ID; got != want {
			t.Errorf("model saw %q, want %q", got, want)
		}

		calls := st.ToolCalls(1)
		if len(calls) != 1 {
			t.Fatalf("got %d tool calls logged, want 1", len(calls))
		}
		if calls[0].Status != store.ToolCallStatusProposed || calls[0].Risk != tools.RiskCritical {
			t.Errorf("logged status %q risk %q, want proposed and critical", calls[0].Status, calls[0].Risk)
		}
	})

	t.Run("risk claim in args ignored", func(t *testing.T) {
		var executed bool
		reg := tools.NewRegistry()
		_ = reg.Register(&openCriticalTool{executed: &executed})

		// The schema accepts extra properties, so the claim reaches the dispatcher.
		call := model.ToolCall{
			ID:        "call_claim",
			Name:      "open_critical",
			Arguments: json.RawMessage(`{"text": "Dentist at 3pm", "risk": "read"}`),
		}
		turns := []model.CompletionResponse{
			{ToolCalls: []model.ToolCall{call}},
			{Content: "Waiting."},
		}
		client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
			return turns[(len(req.Messages)-2)/2], nil
		}}

		st := testutil.NewFakeStore()
		opts := testOptions()
		opts.Tools = reg
		opts.Approvals = approval.New(st, reg, time.Hour)
		a := New(client, st, opts)

		send(t, a, 1, "do it")
		if executed {
			t.Fatal("critical tool executed on the strength of a risk claim")
		}
		if n := len(st.PendingActions(1)); n != 1 {
			t.Fatalf("got %d pending actions, want 1", n)
		}
		calls := st.ToolCalls(1)
		if len(calls) != 1 || calls[0].Risk != tools.RiskCritical || calls[0].Status != store.ToolCallStatusProposed {
			t.Errorf("logged calls %+v, want one proposed critical call", calls)
		}
	})
}

// recordingCards records the cards it is asked to show, and fails them when err is set.
type recordingCards struct {
	shown []store.PendingAction
	err   error
}

func (r *recordingCards) SendApprovalCard(ctx context.Context, action store.PendingAction) error {
	r.shown = append(r.shown, action)
	return r.err
}

// criticalTurns scripts one critical call to notify_self followed by a plain reply.
func criticalTurns() *testutil.FakeModel {
	turns := []model.CompletionResponse{
		{ToolCalls: []model.ToolCall{{
			ID:        "call_card",
			Name:      "notify_self",
			Arguments: json.RawMessage(`{"text": "Dentist at 3pm"}`),
		}}},
		{Content: "Waiting."},
	}
	return &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
		return turns[(len(req.Messages)-2)/2], nil
	}}
}

func TestCardShownAfterProposal(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(tools.NewNotifySelfTool(func(context.Context, identity.UserID, string) error { return nil }))
	st := testutil.NewFakeStore()
	cards := &recordingCards{}
	opts := testOptions()
	opts.Tools = reg
	opts.Approvals = approval.New(st, reg, time.Hour)
	opts.Cards = cards
	a := New(criticalTurns(), st, opts)

	send(t, a, 1, "notify me")

	pending := st.PendingActions(1)
	if len(cards.shown) != 1 || len(pending) != 1 {
		t.Fatalf("cards %d, pending %d, want one of each", len(cards.shown), len(pending))
	}
	if cards.shown[0].ID != pending[0].ID {
		t.Errorf("card shows action %s, want the proposed %s", cards.shown[0].ID, pending[0].ID)
	}
}

func TestFailedCardLeavesActionPending(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(tools.NewNotifySelfTool(func(context.Context, identity.UserID, string) error { return nil }))
	st := testutil.NewFakeStore()
	client := criticalTurns()
	opts := testOptions()
	opts.Tools = reg
	opts.Approvals = approval.New(st, reg, time.Hour)
	opts.Cards = &recordingCards{err: errors.New("telegram down")}
	a := New(client, st, opts)

	if reply := send(t, a, 1, "notify me"); reply != "Waiting." {
		t.Errorf("reply %q, want the model's words despite the failed card", reply)
	}
	pending := st.PendingActions(1)
	if len(pending) != 1 || pending[0].State != store.PendingProposed {
		t.Fatalf("pending %+v, want one proposed action", pending)
	}
	if got, want := toolResult(t, client, "call_card"), "pending approval "+pending[0].ID; got != want {
		t.Errorf("model saw %q, want %q", got, want)
	}
}

// toolResult returns the content of the tool message that answers callID in the last request.
func toolResult(t *testing.T, client *testutil.FakeModel, callID string) string {
	t.Helper()
	for _, m := range lastRequest(t, client).Messages {
		if m.Role == model.RoleTool && m.ToolCallID == callID {
			return m.Content
		}
	}
	t.Fatalf("no tool result for call %s in the last request", callID)
	return ""
}

// openCriticalTool is critical and accepts any arguments.
type openCriticalTool struct {
	executed *bool
}

func (t *openCriticalTool) Name() string        { return "open_critical" }
func (t *openCriticalTool) Description() string { return "Does something critical." }
func (t *openCriticalTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object"}`)
}
func (t *openCriticalTool) Risk() tools.Risk { return tools.RiskCritical }
func (t *openCriticalTool) Preview(args json.RawMessage) (string, error) {
	return "Do something critical", nil
}
func (t *openCriticalTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	*t.executed = true
	return tools.Result{Content: "done"}, nil
}

type testUserTool struct {
	onExec func(ctx context.Context)
}

func (t *testUserTool) Name() string                                 { return "inspect_user" }
func (t *testUserTool) Description() string                          { return "inspects user" }
func (t *testUserTool) Schema() json.RawMessage                      { return json.RawMessage(`{"type":"object"}`) }
func (t *testUserTool) Risk() tools.Risk                             { return tools.RiskRead }
func (t *testUserTool) Preview(args json.RawMessage) (string, error) { return "", nil }
func (t *testUserTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	if t.onExec != nil {
		t.onExec(ctx)
	}
	return tools.Result{Content: "ok"}, nil
}

func TestStructuralCriticalToolExecution(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root")
		}
		dir = parent
	}

	fset := token.NewFileSet()
	var callSites []string

	err = filepath.Walk(filepath.Join(dir, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if strings.HasPrefix(rel, "internal/approval") || strings.HasPrefix(rel, "internal/tools") || strings.HasPrefix(rel, "internal/testutil") {
			return nil
		}

		node, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Execute" {
				return true
			}
			pos := fset.Position(call.Pos())
			callSites = append(callSites, rel+":"+strconv.Itoa(pos.Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("ast walk: %v", err)
	}

	// Verify that the only call site outside approval is in agent.go
	for _, site := range callSites {
		if !strings.HasPrefix(site, "internal/agent/agent.go:") {
			t.Errorf("forbidden Execute call site outside internal/approval: %s", site)
		}
	}
}
