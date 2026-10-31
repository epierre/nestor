package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

			got, err := NewClient(p.URL, testAPIKey).Complete(context.Background(), CompletionRequest{Model: "cheap"})
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

	_, err := NewClient(p.URL, testAPIKey).Complete(context.Background(), CompletionRequest{Model: "cheap"})

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
