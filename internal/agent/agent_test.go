package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

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

func TestHandleMessageReturnsModelError(t *testing.T) {
	boom := errors.New("boom")
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{}, boom
	}}

	ctx := identity.WithUser(context.Background(), 1)
	if _, err := newAgent(client).HandleMessage(ctx, "hello"); !errors.Is(err, boom) {
		t.Errorf("got error %v, want %v", err, boom)
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
	ctx := identity.WithUser(context.Background(), user)
	st := testutil.NewFakeStore()

	send(t, New(numberedReplies(), st, testOptions()), user, "hello")

	conversation, err := st.ActiveConversation(ctx, user)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	got, err := st.RecentMessages(ctx, user, conversation.ID, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	assertStored(t, got[0], user, model.RoleUser, "hello")
	assertStored(t, got[1], user, model.RoleAssistant, "reply 1")
}

func assertStored(t *testing.T, m store.Message, user identity.UserID, role model.Role, content string) {
	t.Helper()
	if m.UserID != user || m.Role != role || m.Content != content || m.CreatedAt.IsZero() {
		t.Errorf("got message %+v, want %s %q of user %d", m, role, content, user)
	}
}
