package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
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
