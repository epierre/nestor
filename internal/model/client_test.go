package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testAPIKey = "test-token"

// proxy is a test server replaying body with headers, keeping the last request it received.
type proxy struct {
	*httptest.Server
	request *http.Request
	body    []byte
}

func newProxy(t *testing.T, status int, headers map[string]string, body []byte) *proxy {
	t.Helper()
	p := &proxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.request = r
		p.body, _ = io.ReadAll(r.Body)
		for name, value := range headers {
			w.Header().Set(name, value)
		}
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(p.Close)
	return p
}

// fastClient retries without waiting.
func fastClient(baseURL string) *Client {
	c := NewClient(baseURL, testAPIKey)
	c.backoff = 0
	return c
}

// script is a test server running one handler per request, repeating the last one.
type script struct {
	*httptest.Server
	mu       sync.Mutex
	times    []time.Time
	requests [][]byte
}

func newScript(t *testing.T, handlers ...http.HandlerFunc) *script {
	t.Helper()
	s := &script{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.times = append(s.times, time.Now())
		s.requests = append(s.requests, body)
		n := min(len(s.requests), len(handlers)) - 1
		s.mu.Unlock()
		handlers[n](w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *script) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func reply(status int, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write(body)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func TestCompleteTextResponse(t *testing.T) {
	p := newProxy(t, http.StatusOK, nil, fixture(t, "text.json"))

	got, err := NewClient(p.URL+"/v1", testAPIKey).Complete(context.Background(), CompletionRequest{Model: "cheap"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got.Attempts = nil // TestCompleteRecordsAttempt covers them
	want := CompletionResponse{
		Content: "Hello back",
		Usage:   Usage{PromptTokens: 12, CompletionTokens: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestCompleteToolCallResponse(t *testing.T) {
	p := newProxy(t, http.StatusOK, nil, fixture(t, "tool_calls.json"))

	got, err := NewClient(p.URL, testAPIKey).Complete(context.Background(), CompletionRequest{Model: "cheap"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got.Attempts = nil // TestCompleteRecordsAttempt covers them
	want := CompletionResponse{
		ToolCalls: []ToolCall{
			{ID: "call_1", Name: "current_time", Arguments: json.RawMessage(`{"zone":"Europe/Paris"}`)},
			{ID: "call_2", Name: "list_events", Arguments: json.RawMessage(`{}`)},
		},
		Usage: Usage{PromptTokens: 40, CompletionTokens: 18},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestCompleteSendsRequest(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{"base URL", "/v1"},
		{"base URL with trailing slash", "/v1/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProxy(t, http.StatusOK, nil, fixture(t, "text.json"))

			_, err := NewClient(p.URL+tt.baseURL, testAPIKey).Complete(context.Background(), CompletionRequest{
				Model: "strong",
				Messages: []Message{
					{Role: RoleSystem, Content: "be helpful"},
					{Role: RoleUser, Content: "what time is it?"},
					{Role: RoleAssistant, ToolCalls: []ToolCall{
						{ID: "call_1", Name: "current_time", Arguments: json.RawMessage(`{"zone":"UTC"}`)},
					}},
					{Role: RoleTool, ToolCallID: "call_1", Content: "12:00"},
				},
				Tools: []Tool{{
					Name:        "current_time",
					Description: "Returns the time",
					Parameters:  json.RawMessage(`{"type":"object"}`),
				}},
				MaxTokens: 100,
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}

			if p.request.Method != http.MethodPost || p.request.URL.Path != "/v1/chat/completions" {
				t.Errorf("got %s %s, want POST /v1/chat/completions", p.request.Method, p.request.URL.Path)
			}
			if got, want := p.request.Header.Get("Authorization"), "Bearer "+testAPIKey; got != want {
				t.Errorf("Authorization %q, want %q", got, want)
			}
			if got, want := p.request.Header.Get("Content-Type"), "application/json"; got != want {
				t.Errorf("Content-Type %q, want %q", got, want)
			}
			want := `{
				"model": "strong",
				"messages": [
					{"role": "system", "content": "be helpful"},
					{"role": "user", "content": "what time is it?"},
					{"role": "assistant", "content": null, "tool_calls": [
						{"id": "call_1", "type": "function",
						 "function": {"name": "current_time", "arguments": "{\"zone\":\"UTC\"}"}}
					]},
					{"role": "tool", "content": "12:00", "tool_call_id": "call_1"}
				],
				"tools": [
					{"type": "function", "function": {
						"name": "current_time",
						"description": "Returns the time",
						"parameters": {"type": "object"}
					}}
				],
				"max_tokens": 100
			}`
			assertJSONEqual(t, p.body, []byte(want))
		})
	}
}

func TestCompleteOmitsUnsetOptions(t *testing.T) {
	p := newProxy(t, http.StatusOK, nil, fixture(t, "text.json"))

	_, err := NewClient(p.URL, testAPIKey).Complete(context.Background(), CompletionRequest{
		Model:    "cheap",
		Messages: []Message{{Role: RoleTool, ToolCallID: "call_1"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	want := `{"model": "cheap", "messages": [{"role": "tool", "content": "", "tool_call_id": "call_1"}]}`
	assertJSONEqual(t, p.body, []byte(want))
}

func TestCompleteCost(t *testing.T) {
	const costHeader = "x-litellm-response-cost"
	cost := func(v float64) *float64 { return &v }
	tests := []struct {
		name    string
		headers map[string]string
		usage   string
		want    *float64
	}{
		{"no cost reported", nil, `{}`, nil},
		{"cost in header", map[string]string{costHeader: "0.00042"}, `{}`, cost(0.00042)},
		{"cost in usage field", nil, `{"cost": 0.0007}`, cost(0.0007)},
		{"header wins over field", map[string]string{costHeader: "0.1"}, `{"cost": 0.2}`, cost(0.1)},
		{"zero is kept, not null", map[string]string{costHeader: "0.0"}, `{}`, cost(0)},
		{"proxy reports None", map[string]string{costHeader: "None"}, `{}`, nil},
		{"empty header", map[string]string{costHeader: ""}, `{}`, nil},
		{"negative header", map[string]string{costHeader: "-1"}, `{}`, nil},
		{"not a number", map[string]string{costHeader: "NaN"}, `{}`, nil},
		{"infinite", map[string]string{costHeader: "Inf"}, `{}`, nil},
		{"bad header falls back to field", map[string]string{costHeader: "None"}, `{"cost": 0.3}`, cost(0.3)},
		{"negative field", nil, `{"cost": -1}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"choices": [{"message": {"content": "hi"}}], "usage": ` + tt.usage + `}`
			p := newProxy(t, http.StatusOK, tt.headers, []byte(body))

			got, err := NewClient(p.URL, testAPIKey).Complete(context.Background(), CompletionRequest{Model: "cheap"})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if !reflect.DeepEqual(got.Usage.CostUSD, tt.want) {
				t.Errorf("got cost %v, want %v", got.Usage.CostUSD, tt.want)
			}
		})
	}
}

func TestCompleteFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"client error", http.StatusBadRequest, `{"error": {"message": "bad"}}`, "status 400"},
		{"server error", http.StatusServiceUnavailable, `busy`, "status 503"},
		{"malformed body", http.StatusOK, `not json`, "decode response"},
		{"no choices", http.StatusOK, `{"choices": []}`, "no choices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProxy(t, tt.status, nil, []byte(tt.body))

			got, err := fastClient(p.URL).Complete(context.Background(), CompletionRequest{Model: "cheap"})
			if err == nil {
				t.Fatalf("Complete returned %+v, want error", got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestCompleteHTTPErrorExposesStatus(t *testing.T) {
	p := newProxy(t, http.StatusBadGateway, nil, nil)

	_, err := fastClient(p.URL).Complete(context.Background(), CompletionRequest{Model: "cheap"})

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("got error %v, want a StatusError", err)
	}
	if statusErr.Code != http.StatusBadGateway {
		t.Errorf("got status %d, want %d", statusErr.Code, http.StatusBadGateway)
	}
}

func TestCompleteErrorContainsNoSecret(t *testing.T) {
	// A proxy can echo the key it rejected; the error ends up in logs and the call log.
	p := newProxy(t, http.StatusUnauthorized, nil, []byte(`{"error": {"message": "bad key `+testAPIKey+`"}}`))

	_, err := NewClient(p.URL, testAPIKey).Complete(context.Background(), CompletionRequest{Model: "cheap"})
	if err == nil {
		t.Fatal("Complete succeeded, want error")
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Errorf("error %q contains the API key", err)
	}
}

func TestCompleteReturnsContextError(t *testing.T) {
	p := newProxy(t, http.StatusOK, nil, fixture(t, "text.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewClient(p.URL, testAPIKey).Complete(ctx, CompletionRequest{Model: "cheap"})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("got error %v, want context.Canceled", err)
	}
}

func TestCompleteRetriesServerErrorOnce(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"500", http.StatusInternalServerError},
		{"502", http.StatusBadGateway},
		{"503", http.StatusServiceUnavailable},
		{"504", http.StatusGatewayTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name+" then success", func(t *testing.T) {
			text := fixture(t, "text.json")
			s := newScript(t, reply(tt.status, []byte("busy")), reply(http.StatusOK, text))

			got, err := fastClient(s.URL).Complete(context.Background(), CompletionRequest{Model: "cheap"})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}

			if got.Content != "Hello back" {
				t.Errorf("got content %q", got.Content)
			}
			if s.calls() != 2 {
				t.Errorf("made %d requests, want 2", s.calls())
			}
			if !bytes.Equal(s.requests[0], s.requests[1]) {
				t.Errorf("retry sent %s, first attempt sent %s", s.requests[1], s.requests[0])
			}
			if len(got.Attempts) != 2 {
				t.Fatalf("got %d attempts, want 2", len(got.Attempts))
			}
			var statusErr *StatusError
			if !errors.As(got.Attempts[0].Err, &statusErr) || statusErr.Code != tt.status {
				t.Errorf("first attempt error %v, want status %d", got.Attempts[0].Err, tt.status)
			}
			if got.Attempts[1].Err != nil {
				t.Errorf("second attempt error %v, want none", got.Attempts[1].Err)
			}
		})
		t.Run(tt.name+" twice", func(t *testing.T) {
			s := newScript(t, reply(tt.status, []byte("busy")))

			got, err := fastClient(s.URL).Complete(context.Background(), CompletionRequest{Model: "cheap"})

			var statusErr *StatusError
			if !errors.As(err, &statusErr) || statusErr.Code != tt.status {
				t.Fatalf("got error %v, want status %d", err, tt.status)
			}
			if s.calls() != 2 {
				t.Errorf("made %d requests, want 2", s.calls())
			}
			if len(got.Attempts) != 2 || got.Attempts[0].Err == nil || got.Attempts[1].Err == nil {
				t.Errorf("got attempts %+v, want two failures", got.Attempts)
			}
		})
	}
}

func TestCompleteDoesNotRetryOtherFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"400", http.StatusBadRequest, `{}`},
		{"401", http.StatusUnauthorized, `{}`},
		{"404", http.StatusNotFound, `{}`},
		{"429", http.StatusTooManyRequests, `{}`},
		{"malformed body", http.StatusOK, `not json`},
		{"no choices", http.StatusOK, `{"choices": []}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScript(t, reply(tt.status, []byte(tt.body)))

			got, err := fastClient(s.URL).Complete(context.Background(), CompletionRequest{Model: "cheap"})

			if err == nil {
				t.Fatal("Complete succeeded, want error")
			}
			if s.calls() != 1 {
				t.Errorf("made %d requests, want 1", s.calls())
			}
			if len(got.Attempts) != 1 || got.Attempts[0].Err == nil {
				t.Errorf("got attempts %+v, want one failure", got.Attempts)
			}
		})
	}
}

func TestCompleteWaitsBeforeRetry(t *testing.T) {
	const backoff = 30 * time.Millisecond
	s := newScript(t, reply(http.StatusBadGateway, nil), reply(http.StatusOK, fixture(t, "text.json")))
	client := NewClient(s.URL, testAPIKey)
	client.backoff = backoff

	if _, err := client.Complete(context.Background(), CompletionRequest{Model: "cheap"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if waited := s.times[1].Sub(s.times[0]); waited < backoff {
		t.Errorf("retried after %v, want at least %v", waited, backoff)
	}
}

func TestCompleteCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newScript(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		cancel()
	})
	client := NewClient(s.URL, testAPIKey)
	client.backoff = time.Hour

	got, err := client.Complete(ctx, CompletionRequest{Model: "cheap"})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("got error %v, want context.Canceled", err)
	}
	if s.calls() != 1 {
		t.Errorf("made %d requests, want 1", s.calls())
	}
	if len(got.Attempts) != 1 {
		t.Errorf("got %d attempts, want 1", len(got.Attempts))
	}
}

func TestCompleteTimeoutReturnsError(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"no response", func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}},
		{"stalled body", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScript(t, tt.handler)
			client := fastClient(s.URL)
			client.timeout = 20 * time.Millisecond

			got, err := client.Complete(context.Background(), CompletionRequest{Model: "cheap"})

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("got error %v, want a timeout", err)
			}
			if s.calls() != 1 {
				t.Errorf("made %d requests, want 1: a timeout is not retried", s.calls())
			}
			if len(got.Attempts) != 1 {
				t.Fatalf("got %d attempts, want 1", len(got.Attempts))
			}
			if !errors.Is(got.Attempts[0].Err, context.DeadlineExceeded) {
				t.Errorf("attempt error %v, want a timeout", got.Attempts[0].Err)
			}
			if len(got.Attempts[0].Request) == 0 {
				t.Error("attempt has no request body")
			}
		})
	}
}

func TestCompleteDefaultTimeoutIs30Seconds(t *testing.T) {
	if got := NewClient("http://proxy", testAPIKey).timeout; got != 30*time.Second {
		t.Errorf("got timeout %v, want 30s", got)
	}
}

func TestCompleteRecordsAttempt(t *testing.T) {
	text := fixture(t, "text.json")
	s := newScript(t, reply(http.StatusOK, text))
	before := time.Now()

	got, err := fastClient(s.URL).Complete(context.Background(), CompletionRequest{Model: "cheap"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(got.Attempts) != 1 {
		t.Fatalf("got %d attempts, want 1", len(got.Attempts))
	}
	attempt := got.Attempts[0]
	if attempt.StartedAt.Before(before) {
		t.Errorf("started at %v, before the call at %v", attempt.StartedAt, before)
	}
	if attempt.Latency <= 0 {
		t.Errorf("got latency %v, want positive", attempt.Latency)
	}
	if !bytes.Equal(attempt.Request, s.requests[0]) {
		t.Errorf("recorded request %s, sent %s", attempt.Request, s.requests[0])
	}
	if !bytes.Equal(attempt.Response, text) {
		t.Errorf("recorded response %s, want %s", attempt.Response, text)
	}
	if attempt.Err != nil {
		t.Errorf("got attempt error %v", attempt.Err)
	}
}

func TestCompleteRecordsNoSecret(t *testing.T) {
	echo := fmt.Sprintf(`{"error": {"message": "bad key %s"}}`, testAPIKey)
	s := newScript(t, reply(http.StatusUnauthorized, []byte(echo)))

	got, err := fastClient(s.URL).Complete(context.Background(), CompletionRequest{Model: "cheap"})
	if err == nil {
		t.Fatal("Complete succeeded, want error")
	}

	if len(got.Attempts) != 1 {
		t.Fatalf("got %d attempts, want 1", len(got.Attempts))
	}
	attempt := got.Attempts[0]
	if bytes.Contains(attempt.Request, []byte(testAPIKey)) {
		t.Errorf("recorded request %s contains the API key", attempt.Request)
	}
	if bytes.Contains(attempt.Response, []byte(testAPIKey)) {
		t.Errorf("recorded response %s contains the API key", attempt.Response)
	}
	if !bytes.Contains(attempt.Response, []byte("bad key")) {
		t.Errorf("recorded response %s lost the provider's message", attempt.Response)
	}
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode %s: %v", got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("got %s, want %s", got, want)
	}
}
