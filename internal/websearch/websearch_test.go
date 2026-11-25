package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/obs"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
	"github.com/epierre/nestor/internal/tools"
)

func activeCtx(t *testing.T, st *testutil.FakeStore, user identity.UserID) (context.Context, int64) {
	t.Helper()
	ctx := identity.WithUser(context.Background(), user)
	conv, err := st.ActiveConversation(ctx, user)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	return store.WithConversation(ctx, conv.ID), conv.ID
}

func TestWebSearchExecutesIsolatedModelCall(t *testing.T) {
	zurich, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	fixed := time.Date(2026, 11, 25, 8, 15, 0, 0, time.UTC)

	st := testutil.NewFakeStore()
	ctx1, conv1 := activeCtx(t, st, 1)
	ctx2, conv2 := activeCtx(t, st, 2)
	// Seed prior conversation messages to verify search does not receive conversation history.
	if _, err := st.AppendMessage(ctx1, 1, conv1, model.RoleUser, "secret user 1 history"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := st.AppendMessage(ctx2, 2, conv2, model.RoleUser, "secret user 2 history"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	client := &testutil.FakeModel{Reply: func(req model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{
			Content: "Go 1.25.0 is the latest release.",
			Usage:   model.Usage{PromptTokens: 20, CompletionTokens: 10},
		}, nil
	}}

	tool := NewTool(client, st, Options{
		Model:    "web-search",
		Location: zurich,
		Now:      func() time.Time { return fixed },
	})
	if tool.Name() != "web_search" {
		t.Fatalf("Name() = %q, want web_search", tool.Name())
	}
	if tool.Risk() != tools.RiskRead {
		t.Fatalf("Risk() = %q, want read", tool.Risk())
	}
	preview, err := tool.Preview(json.RawMessage(`{"query":"latest Go release"}`))
	if err != nil || preview != "Search the web for: latest Go release" {
		t.Fatalf("Preview() = %q, %v", preview, err)
	}

	res1, err := tool.Execute(ctx1, json.RawMessage(`{"query":"latest Go release"}`))
	if err != nil {
		t.Fatalf("Execute user 1: %v", err)
	}
	if res1.Truncated || res1.Content != "Go 1.25.0 is the latest release." {
		t.Fatalf("res1 = %+v", res1)
	}

	res2, err := tool.Execute(ctx2, json.RawMessage(`{"query":"weather in Zurich"}`))
	if err != nil {
		t.Fatalf("Execute user 2: %v", err)
	}
	if res2.Content != "Go 1.25.0 is the latest release." {
		t.Fatalf("res2 = %+v", res2)
	}

	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d model requests, want 2", len(reqs))
	}
	for i, req := range reqs {
		if req.Model != "web-search" {
			t.Errorf("req[%d].Model = %q, want web-search", i, req.Model)
		}
		if len(req.Tools) != 0 {
			t.Errorf("req[%d].Tools = %+v, want none", i, req.Tools)
		}
		if req.MaxTokens != 0 {
			t.Errorf("req[%d].MaxTokens = %d, want 0", i, req.MaxTokens)
		}
		if len(req.Messages) != 2 {
			t.Fatalf("req[%d].Messages = %d, want 2 (system + user)", i, len(req.Messages))
		}
		if req.Messages[0].Role != model.RoleSystem {
			t.Errorf("req[%d].Messages[0].Role = %q, want system", i, req.Messages[0].Role)
		}
		if !strings.Contains(req.Messages[0].Content, "Wednesday, 2026-11-25 09:15 CET (Europe/Zurich)") {
			t.Errorf("req[%d] system prompt missing local time: %q", i, req.Messages[0].Content)
		}
		if strings.Contains(req.Messages[0].Content, "secret user") || strings.Contains(req.Messages[1].Content, "secret user") {
			t.Errorf("req[%d] leaked conversation history: %+v", i, req.Messages)
		}
	}
	if reqs[0].Messages[1].Role != model.RoleUser || reqs[0].Messages[1].Content != "latest Go release" {
		t.Errorf("req[0] user message = %+v", reqs[0].Messages[1])
	}
	if reqs[1].Messages[1].Role != model.RoleUser || reqs[1].Messages[1].Content != "weather in Zurich" {
		t.Errorf("req[1] user message = %+v", reqs[1].Messages[1])
	}

	// Verify two-user isolation in recorded model calls.
	calls1 := st.ModelCalls(1)
	calls2 := st.ModelCalls(2)
	if len(calls1) != 1 || calls1[0].ConversationID != conv1 || calls1[0].UserID != 1 {
		t.Errorf("user 1 model calls = %+v, want 1 under conv %d", calls1, conv1)
	}
	if len(calls2) != 1 || calls2[0].ConversationID != conv2 || calls2[0].UserID != 2 {
		t.Errorf("user 2 model calls = %+v, want 1 under conv %d", calls2, conv2)
	}
}

func TestWebSearchRejectsInvalidQueries(t *testing.T) {
	st := testutil.NewFakeStore()
	ctx, _ := activeCtx(t, st, 1)
	client := &testutil.FakeModel{}
	tool := NewTool(client, st, Options{Model: "web-search"})

	reg := tools.NewRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}

	oversized := strings.Repeat("a", 501)
	tests := []struct {
		name string
		args string
	}{
		{"empty object", `{}`},
		{"empty query", `{"query":""}`},
		{"whitespace query", `{"query":"   \t\n "}`},
		{"oversized query", `{"query":"` + oversized + `"}`},
		{"extra user_id field", `{"query":"go","user_id":2}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := reg.Validate("web_search", json.RawMessage(tt.args)); err == nil {
				t.Errorf("Validate(%s) succeeded, want error", tt.args)
			}
			if _, err := tool.Execute(ctx, json.RawMessage(tt.args)); err == nil {
				t.Errorf("Execute(%s) succeeded, want error", tt.args)
			}
		})
	}
	if len(client.Requests()) != 0 {
		t.Errorf("made %d model requests for invalid queries, want 0", len(client.Requests()))
	}
}

func TestWebSearchAppendsDeduplicatedCitations(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		citations []model.Citation
		want      string
	}{
		{
			name:    "deduplicates, sanitizes titles, and filters non-http schemes",
			content: "  Summary result.  ",
			citations: []model.Citation{
				{Title: "First\nTitle\x00With\tControls", URL: "https://example.com/a"},
				{Title: "Duplicate URL", URL: "https://example.com/a"},
				{Title: "XSS", URL: "javascript:alert(1)"},
				{Title: "Local file", URL: "file:///etc/passwd"},
				{Title: "FTP", URL: "ftp://example.com/file"},
				{Title: "Relative", URL: "/relative/path"},
				{Title: "", URL: "http://example.com/b"},
			},
			want: "Summary result.\n\nSources:\n- First Title With Controls (https://example.com/a)\n- http://example.com/b",
		},
		{
			name:      "no citations omits sources section",
			content:   "Direct answer only.",
			citations: nil,
			want:      "Direct answer only.",
		},
		{
			name:    "only filtered citations omits sources section",
			content: "Direct answer only.",
			citations: []model.Citation{
				{Title: "Bad", URL: "javascript:void(0)"},
			},
			want: "Direct answer only.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.NewFakeStore()
			ctx, _ := activeCtx(t, st, 1)
			client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
				return model.CompletionResponse{
					Content:   tt.content,
					Citations: tt.citations,
				}, nil
			}}
			tool := NewTool(client, st, Options{Model: "web-search"})

			res, err := tool.Execute(ctx, json.RawMessage(`{"query":"test"}`))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if res.Content != tt.want {
				t.Errorf("Content = %q, want %q", res.Content, tt.want)
			}
		})
	}
}

func TestWebSearchTruncatesLargeOutput(t *testing.T) {
	st := testutil.NewFakeStore()
	ctx, _ := activeCtx(t, st, 1)

	// Build multi-byte UTF-8 content ("é" is 2 bytes, "€" is 3 bytes) exceeding maxBytes.
	longText := strings.Repeat("é€", 200)
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{Content: longText}, nil
	}}

	const maxBytes = 128
	tool := NewTool(client, st, Options{Model: "web-search", MaxResultBytes: maxBytes})

	res, err := tool.Execute(ctx, json.RawMessage(`{"query":"utf8 truncation"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(res.Content) > maxBytes {
		t.Errorf("len(Content) = %d, want <= %d", len(res.Content), maxBytes)
	}
	if !utf8.ValidString(res.Content) {
		t.Errorf("truncated Content is not valid UTF-8: %q", res.Content)
	}
	if !strings.Contains(res.Content, "\n[truncated: ") || !strings.HasSuffix(res.Content, " bytes omitted]") {
		t.Errorf("missing truncation marker in %q", res.Content)
	}
}

func TestWebSearchRecordsAllModelAttempts(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(obs.NewHandler(slog.NewJSONHandler(&logs, nil))))
	t.Cleanup(func() { slog.SetDefault(prev) })

	st := testutil.NewFakeStore()
	ctx, convID := activeCtx(t, st, 5)
	ctx = obs.WithRequestID(ctx, "req-search-123")

	cost := 0.00042
	t1 := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	t2 := t1.Add(50 * time.Millisecond)
	client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
		return model.CompletionResponse{
			Content: "Search result secret-response-text",
			Usage:   model.Usage{PromptTokens: 15, CompletionTokens: 9, CostUSD: &cost},
			Attempts: []model.Attempt{
				{
					StartedAt: t1,
					Latency:   20 * time.Millisecond,
					Request:   []byte(`{"model":"web-search"}`),
					Response:  []byte(`service unavailable`),
					Err:       errors.New("model endpoint returned status 503"),
				},
				{
					StartedAt: t2,
					Latency:   30 * time.Millisecond,
					Request:   []byte(`{"model":"web-search"}`),
					Response:  []byte(`{"choices":[{"message":{"content":"Search result secret-response-text"}}]}`),
				},
			},
		}, nil
	}}

	tool := NewTool(client, st, Options{Model: "web-search"})
	if _, err := tool.Execute(ctx, json.RawMessage(`{"query":"secret-query-text"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	calls := st.ModelCalls(5)
	if len(calls) != 2 {
		t.Fatalf("got %d model_calls rows, want 2", len(calls))
	}
	if calls[0].Status != store.CallStatusError || calls[0].ConversationID != convID || calls[0].Model != "web-search" {
		t.Errorf("calls[0] = %+v", calls[0])
	}
	if string(calls[0].ResponseJSON) != `"service unavailable"` {
		t.Errorf("calls[0].ResponseJSON = %s, want JSON string", calls[0].ResponseJSON)
	}
	if calls[1].Status != store.CallStatusOK || calls[1].PromptTokens != 15 || calls[1].CompletionTokens != 9 || calls[1].CostUSD == nil || *calls[1].CostUSD != cost {
		t.Errorf("calls[1] = %+v", calls[1])
	}

	logText := logs.String()
	if !strings.Contains(logText, `"msg":"model call finished"`) ||
		!strings.Contains(logText, `"request_id":"req-search-123"`) ||
		!strings.Contains(logText, `"user_id":5`) ||
		!strings.Contains(logText, `"model":"web-search"`) {
		t.Errorf("logs missing expected fields:\n%s", logText)
	}
	if strings.Contains(logText, "secret-query-text") || strings.Contains(logText, "secret-response-text") {
		t.Errorf("logs leaked query or response text:\n%s", logText)
	}
}

func TestWebSearchRejectsMissingUserOrConversation(t *testing.T) {
	st := testutil.NewFakeStore()
	client := &testutil.FakeModel{}
	tool := NewTool(client, st, Options{Model: "web-search"})

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"hello"}`)); !errors.Is(err, identity.ErrNoUser) {
		t.Errorf("missing user err = %v, want %v", err, identity.ErrNoUser)
	}
	if _, err := tool.Execute(identity.WithUser(context.Background(), 1), json.RawMessage(`{"query":"hello"}`)); !errors.Is(err, store.ErrNoConversation) {
		t.Errorf("missing conversation err = %v, want %v", err, store.ErrNoConversation)
	}
	if len(client.Requests()) != 0 {
		t.Errorf("made %d model calls, want 0", len(client.Requests()))
	}
}

type failingRecordStore struct {
	*testutil.FakeStore
	err error
}

func (f failingRecordStore) RecordModelCall(context.Context, identity.UserID, store.ModelCall) (int64, error) {
	return 0, f.err
}

func TestWebSearchHandlesModelAndStoreFailures(t *testing.T) {
	t.Run("timeout still records failed attempt using parent context", func(t *testing.T) {
		st := testutil.NewFakeStore()
		ctx, _ := activeCtx(t, st, 1)

		client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
			return model.CompletionResponse{
				Attempts: []model.Attempt{{
					StartedAt: time.Now(),
					Latency:   20 * time.Millisecond,
					Request:   []byte(`{"model":"web-search"}`),
					Err:       context.DeadlineExceeded,
				}},
			}, context.DeadlineExceeded
		}}
		tool := NewTool(client, st, Options{Model: "web-search", Timeout: 20 * time.Millisecond})

		_, err := tool.Execute(ctx, json.RawMessage(`{"query":"slow query"}`))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want DeadlineExceeded", err)
		}
		if calls := st.ModelCalls(1); len(calls) != 1 || calls[0].Status != store.CallStatusError {
			t.Errorf("calls = %+v, want 1 error row recorded", calls)
		}
	})

	t.Run("empty content or unexpected tool calls rejected without execution", func(t *testing.T) {
		st := testutil.NewFakeStore()
		ctx, _ := activeCtx(t, st, 1)
		client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
			return model.CompletionResponse{
				Content:   "   ",
				ToolCalls: []model.ToolCall{{ID: "call_1", Name: "task_delete", Arguments: json.RawMessage(`{"id":1}`)}},
			}, nil
		}}
		tool := NewTool(client, st, Options{Model: "web-search"})

		if _, err := tool.Execute(ctx, json.RawMessage(`{"query":"empty"}`)); err == nil {
			t.Fatal("Execute succeeded on empty content + tool calls, want error")
		}
		if calls := st.ToolCalls(1); len(calls) != 0 {
			t.Errorf("executed %d tool calls, want 0", len(calls))
		}
	})

	t.Run("RecordModelCall failure fails closed", func(t *testing.T) {
		base := testutil.NewFakeStore()
		ctx, _ := activeCtx(t, base, 1)
		dbErr := errors.New("postgres down")
		st := failingRecordStore{FakeStore: base, err: dbErr}

		client := &testutil.FakeModel{Reply: func(model.CompletionRequest) (model.CompletionResponse, error) {
			return model.CompletionResponse{Content: "Must not reach caller"}, nil
		}}
		tool := NewTool(client, st, Options{Model: "web-search"})

		res, err := tool.Execute(ctx, json.RawMessage(`{"query":"db fail"}`))
		if !errors.Is(err, dbErr) {
			t.Fatalf("err = %v, want %v", err, dbErr)
		}
		if res.Content != "" {
			t.Errorf("Content = %q, want empty on fail-closed", res.Content)
		}
	})
}
