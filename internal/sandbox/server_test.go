package sandbox

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/epierre/nestor/internal/skills"
)

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		body     string
		wantCode int
		wantBody string
		wantRun  bool
	}{
		{"runs a bundled script", "/run", `{"skill":"probe","script":"scripts/hello.py","args":{"x":1}}`, 200, `"output":"hello\n"`, true},
		{"arbitrary image refused", "/run", `{"skill":"probe","script":"scripts/hello.py","image":"alpine"}`, 400, "unknown field", false},
		{"arbitrary command refused", "/run", `{"skill":"probe","script":"scripts/hello.py","cmd":["sh","-c","id"]}`, 400, "unknown field", false},
		{"limits from request refused", "/run", `{"skill":"probe","script":"scripts/hello.py","timeout":"1h"}`, 400, "unknown field", false},
		{"unbundled command refused", "/run", `{"skill":"probe","script":"/bin/sh"}`, 400, "not a bundled script", false},
		{"missing script refused", "/run", `{"skill":"probe"}`, 400, "skill and script are required", false},
		{"trailing data refused", "/run", `{"skill":"probe","script":"scripts/hello.py"}{}`, 400, "single JSON object", false},
		{"oversized body refused", "/run", `{"skill":"probe","script":"scripts/hello.py","args":{"x":"` + strings.Repeat("a", 70*1024) + `"}}`, 400, "too large", false},
		{"other path not found", "/containers/create", `{}`, 404, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, eng, _ := newFixture(t, skills.ScriptLimits{})
			var logs bytes.Buffer
			h := Handler(r, slog.New(slog.NewTextHandler(&logs, nil)))
			rec := post(t, h, tt.path, tt.body)
			if rec.Code != tt.wantCode || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("POST %s = %d %s, want %d containing %q", tt.path, rec.Code, rec.Body.String(), tt.wantCode, tt.wantBody)
			}
			if ran := len(eng.specs) > 0; ran != tt.wantRun {
				t.Fatalf("container created = %v, want %v", ran, tt.wantRun)
			}
			if tt.wantCode == 400 && !strings.Contains(logs.String(), "sandbox request refused") {
				t.Errorf("refusal not logged:\n%s", logs.String())
			}
		})
	}
}

func TestHandlerRunFailure(t *testing.T) {
	r, eng, _ := newFixture(t, skills.ScriptLimits{})
	eng.createErr = errInjected
	rec := post(t, Handler(r, slog.Default()), "/run", `{"skill":"probe","script":"scripts/hello.py"}`)
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadGateway || !strings.Contains(body["error"], "injected") {
		t.Fatalf("POST /run = %d %v, want 502 with the error", rec.Code, body)
	}
}
