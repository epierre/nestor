package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/tools"
)

// Defaults for a skill with scripts enabled and no script_limits. sandboxd applies the same ones.
const (
	DefaultScriptTimeout        = 20 * time.Second
	DefaultScriptMemory         = "256m"
	DefaultScriptMaxOutputBytes = 32 * 1024
)

// sandboxQueueMargin is added to a script's timeout for the whole call to sandboxd, which runs one
// script at a time and may first finish another run.
const sandboxQueueMargin = 60 * time.Second

// maxSandboxResponse bounds the response read from sandboxd. Output is capped there, but JSON
// escaping can make it several times longer.
const maxSandboxResponse = 1 << 20

// SandboxClient calls sandboxd's single operation.
type SandboxClient struct {
	base string
	http *http.Client
}

// NewSandboxClient returns a client for the sandboxd at base, such as http://sandboxd:8080.
func NewSandboxClient(base string) *SandboxClient {
	return &SandboxClient{base: strings.TrimRight(base, "/"), http: &http.Client{}}
}

// scriptRequest and scriptResult mirror sandbox.Request and sandbox.Result; a test in
// internal/sandbox checks that they stay in step.
type scriptRequest struct {
	Skill  string          `json:"skill"`
	Script string          `json:"script"`
	Args   json.RawMessage `json:"args,omitempty"`
}

type scriptResult struct {
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
}

func (c *SandboxClient) run(ctx context.Context, req scriptRequest) (scriptResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return scriptResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/run", bytes.NewReader(body))
	if err != nil {
		return scriptResult{}, fmt.Errorf("sandbox unavailable: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return scriptResult{}, fmt.Errorf("sandbox unavailable: %w", err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxSandboxResponse)

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(limited).Decode(&e)
		if resp.StatusCode == http.StatusBadRequest {
			return scriptResult{}, fmt.Errorf("sandbox refused the run: %s", e.Error)
		}
		return scriptResult{}, fmt.Errorf("sandbox failed: %s", e.Error)
	}
	var res scriptResult
	if err := json.NewDecoder(limited).Decode(&res); err != nil {
		return scriptResult{}, fmt.Errorf("sandbox failed: decode result: %w", err)
	}
	return res, nil
}

// RegisterScriptTool adds run_skill_script to reg. A nil client means SANDBOXD_URL is not set:
// the tool is still offered and answers that scripts are unavailable.
func RegisterScriptTool(reg *tools.Registry, c *Catalog, client *SandboxClient) error {
	t := NewRunSkillScriptTool(c, client)
	if err := reg.Register(t); err != nil {
		return fmt.Errorf("register %s: %w", t.Name(), err)
	}
	return nil
}

// RunSkillScriptTool runs a script bundled with a skill enabled for the requesting user, through
// sandboxd. Its risk is reversible, not read, because it executes code; the code can reach no data
// or network, so its only effect is its output.
type RunSkillScriptTool struct {
	catalog *Catalog
	client  *SandboxClient
}

// NewRunSkillScriptTool returns a run_skill_script tool. client may be nil.
func NewRunSkillScriptTool(c *Catalog, client *SandboxClient) *RunSkillScriptTool {
	return &RunSkillScriptTool{catalog: c, client: client}
}

func (t *RunSkillScriptTool) Name() string { return "run_skill_script" }

func (t *RunSkillScriptTool) Description() string {
	return "Runs a script bundled with a skill, when the skill's instructions say to, in a sandbox with no network. " +
		"The script reads args from /work/args.json. Returns its exit status and output."
}

func (t *RunSkillScriptTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Skill name"},
			"script": {"type": "string", "description": "Script path relative to the skill's folder, such as scripts/score.py"},
			"args": {"type": "object", "description": "Arguments for the script, as a JSON object"}
		},
		"required": ["name", "script"],
		"additionalProperties": false
	}`)
}

func (t *RunSkillScriptTool) Risk() tools.Risk { return tools.RiskReversible }

type scriptArgs struct {
	Name   string          `json:"name"`
	Script string          `json:"script"`
	Args   json.RawMessage `json:"args"`
}

func (t *RunSkillScriptTool) Preview(args json.RawMessage) (string, error) {
	var a scriptArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("%w: %v", tools.ErrValidation, err)
	}
	return fmt.Sprintf("Run %s from skill %s", a.Script, a.Name), nil
}

func (t *RunSkillScriptTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return tools.Result{}, fmt.Errorf("run_skill_script: %w", err)
	}
	var a scriptArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("%w: %v", tools.ErrValidation, err)
	}
	l, err := t.catalog.lookup(user, a.Name)
	if err != nil {
		return tools.Result{}, err
	}
	if !l.Entry.Scripts {
		return tools.Result{}, fmt.Errorf("skill %q has scripts disabled", a.Name)
	}
	if t.client == nil {
		return tools.Result{}, errors.New("scripts are unavailable: no sandbox is configured")
	}
	if !fs.ValidPath(a.Script) || !l.HasFile(a.Script) {
		return tools.Result{}, fmt.Errorf("%q is not a bundled script of skill %q", a.Script, a.Name)
	}

	timeout := l.Entry.ScriptLimits.Timeout
	if timeout == 0 {
		timeout = DefaultScriptTimeout
	}
	maxOutput := l.Entry.ScriptLimits.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = DefaultScriptMaxOutputBytes
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout+sandboxQueueMargin)
	defer cancel()
	res, err := t.client.run(callCtx, scriptRequest{Skill: a.Name, Script: a.Script, Args: a.Args})
	if err != nil {
		return tools.Result{}, err
	}
	if res.TimedOut {
		return tools.Result{}, fmt.Errorf("script %s of skill %q timed out after %s and was stopped", a.Script, a.Name, timeout)
	}

	// sandboxd caps the output already; capping again keeps the limit even if it did not.
	output, truncated := res.Output, res.Truncated
	if len(output) > maxOutput {
		output, truncated = output[:maxOutput], true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Script %s of skill %q finished with exit status %d in %d ms; output %d bytes:\n",
		a.Script, a.Name, res.ExitCode, res.DurationMS, len(output))
	b.WriteString(output)
	if truncated {
		fmt.Fprintf(&b, "\n[output truncated at %d bytes]", maxOutput)
	}
	return tools.Result{Content: b.String(), Truncated: truncated}, nil
}
