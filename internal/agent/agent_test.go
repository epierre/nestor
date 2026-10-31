package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/testutil"
)

var _ channel.Handler = (*Agent)(nil)

func newAgent(client model.ModelClient) *Agent {
	return New(client, testutil.NewFakeStore(), Options{Model: "cheap", SystemPrompt: "be helpful"})
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
