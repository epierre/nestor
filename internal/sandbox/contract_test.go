package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/skills"
)

// TestToolMatchesHandler runs the agent's run_skill_script tool against this package's handler, so
// the request and result shapes on both sides cannot drift apart.
func TestToolMatchesHandler(t *testing.T) {
	r, eng, root := newFixture(t, skills.ScriptLimits{})
	eng.exitCode = 5
	eng.output = []byte("hello\n")
	srv := httptest.NewServer(Handler(r, slog.Default()))
	defer srv.Close()

	var logs bytes.Buffer
	catalog := skills.NewCatalog(skills.Load(root, r.Config, slog.New(slog.NewTextHandler(&logs, nil))))
	tool := skills.NewRunSkillScriptTool(catalog, skills.NewSandboxClient(srv.URL))
	ctx := identity.WithUser(context.Background(), 1)

	res, err := tool.Execute(ctx, json.RawMessage(`{"name":"probe","script":"scripts/hello.py","args":{"n":1}}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "exit status 5") || !strings.Contains(res.Content, "hello\n") {
		t.Errorf("content %q, want the exit status and output", res.Content)
	}
	if len(eng.specs) != 1 || eng.specs[0].Env[0] != `NESTOR_ARGS={"n":1}` {
		t.Errorf("specs %+v, want one run with the arguments", eng.specs)
	}

	// A refusal by sandboxd reaches the tool as an error.
	if _, err := tool.Execute(ctx, json.RawMessage(`{"name":"probe","script":"scripts/hello.py","args":[1]}`)); err == nil ||
		!strings.Contains(err.Error(), "sandbox refused the run") {
		t.Errorf("error = %v, want the sandbox refusal", err)
	}
}
