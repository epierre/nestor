// Package sandbox runs scripts bundled with reviewed skills in throwaway containers with no
// network. It is used by sandboxd, the only process that holds the Docker socket. What runs, and
// with which settings, is decided here from reviewed configuration; a request only names a skill,
// one of its pinned scripts and JSON arguments.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/epierre/nestor/internal/skills"
)

// RunnerImage is the only image sandboxd starts. It is built from deploy/skill-runner.
const RunnerImage = "nestor-skill-runner"

// runLabel marks containers started by sandboxd; its value is the skill name.
const runLabel = "org.nestor.skill-run"

// Defaults for a skill with scripts enabled and no script_limits.
const (
	DefaultTimeout        = 20 * time.Second
	DefaultMemory         = "256m"
	DefaultMaxOutputBytes = 32 * 1024
)

// runUser runs every script. The tmpfs work folders belong to it, since a tmpfs is otherwise
// owned by root.
const runUser = "65532:65532"

const tmpfsOptions = "rw,noexec,nosuid,size=16m,uid=65532,gid=65532,mode=0700"

// removeTimeout bounds the cleanup of a container, which runs even after the request ended.
const removeTimeout = 15 * time.Second

// interpreters maps a script extension to the program that runs it inside the image.
var interpreters = map[string]string{
	".py": "python3",
	".sh": "sh",
}

// ErrRefused marks a request that was not run because it is not allowed. Nothing was started.
var ErrRefused = errors.New("refused")

// Request asks for one run of a bundled script.
type Request struct {
	Skill  string          `json:"skill"`
	Script string          `json:"script"` // path relative to the skill folder
	Args   json.RawMessage `json:"args"`   // a JSON object, written to /work/args.json
}

// Result is the outcome of a run that was started.
type Result struct {
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"` // stdout and stderr, interleaved
	Truncated  bool   `json:"truncated"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
}

// Runner starts one container per run. Runs are serialised.
type Runner struct {
	Engine Engine
	Config skills.Config
	// SkillsDir is the skills folder as this process sees it; HostSkillsDir is the same folder as
	// the Docker daemon sees it, used for the bind mount.
	SkillsDir     string
	HostSkillsDir string
	Log           *slog.Logger

	once sync.Once
	slot chan struct{} // one run at a time
}

func (r *Runner) acquire(ctx context.Context) error {
	r.once.Do(func() { r.slot = make(chan struct{}, 1) })
	select {
	case r.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// Run checks req against the reviewed configuration and, if it is allowed, runs the script in a
// fresh container that is removed afterwards whatever happens. An error wrapping ErrRefused means
// nothing was started; any other error means the run could not be carried out.
func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	entry, cmd, args, err := r.check(req)
	if err != nil {
		return Result{}, err
	}
	limits, err := limitsFor(entry)
	if err != nil {
		return Result{}, err
	}
	if err := r.acquire(ctx); err != nil {
		return Result{}, err
	}
	defer func() { <-r.slot }()

	spec := ContainerSpec{
		Image:           RunnerImage,
		Cmd:             cmd,
		Env:             []string{"NESTOR_ARGS=" + string(args)},
		User:            runUser,
		WorkingDir:      "/work",
		NetworkDisabled: true,
		Labels:          map[string]string{runLabel: entry.Name},
		HostConfig: HostConfig{
			NetworkMode:    "none",
			ReadonlyRootfs: true,
			Binds:          []string{filepath.Join(r.HostSkillsDir, entry.Name) + ":/skill:ro"},
			Tmpfs: map[string]string{
				"/work": tmpfsOptions,
				"/tmp":  tmpfsOptions,
			},
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
			Memory:      limits.memory,
			MemorySwap:  limits.memory,
			NanoCPUs:    1e9,
			PidsLimit:   64,
		},
	}

	start := time.Now()
	id, err := r.Engine.Create(ctx, spec)
	if err != nil {
		return Result{}, fmt.Errorf("create container: %w", err)
	}
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
		defer cancel()
		if err := r.Engine.Remove(rmCtx, id); err != nil {
			r.Log.Error("sandbox container not removed", "container", id, "skill", entry.Name, "error", err)
		}
	}()
	if err := r.Engine.Start(ctx, id); err != nil {
		return Result{}, fmt.Errorf("start container: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, limits.timeout)
	exitCode, waitErr := r.Engine.Wait(waitCtx, id)
	cancel()
	res := Result{ExitCode: exitCode}
	switch {
	case waitErr == nil:
	case errors.Is(waitErr, context.DeadlineExceeded) && ctx.Err() == nil:
		res.TimedOut = true
		res.ExitCode = -1
	default:
		return Result{}, fmt.Errorf("wait for container: %w", waitErr)
	}
	res.DurationMS = time.Since(start).Milliseconds()

	logCtx, cancelLogs := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancelLogs()
	out, truncated, err := r.Engine.Logs(logCtx, id, limits.maxOutput)
	if err != nil {
		return Result{}, fmt.Errorf("read output: %w", err)
	}
	res.Output, res.Truncated = string(out), truncated

	r.Log.Info("sandbox run finished", "skill", entry.Name, "script", req.Script, "exit_code", res.ExitCode,
		"timed_out", res.TimedOut, "output_bytes", len(out), "truncated", truncated, "duration_ms", res.DurationMS)
	return res, nil
}

// check returns the skill entry, the command and the arguments for req, or a refusal.
func (r *Runner) check(req Request) (skills.Entry, []string, []byte, error) {
	var entry skills.Entry
	found := false
	for _, e := range r.Config.Skills {
		if e.Name == req.Skill {
			entry, found = e, true
			break
		}
	}
	if !found {
		return entry, nil, nil, refuse("unknown skill %q", req.Skill)
	}
	if !entry.Scripts {
		return entry, nil, nil, refuse("skill %q has scripts disabled", req.Skill)
	}
	loaded, err := skills.Verify(r.SkillsDir, entry)
	if err != nil {
		return entry, nil, nil, refuse("skill %q: %v", req.Skill, err)
	}
	if !fs.ValidPath(req.Script) || strings.Contains(req.Script, `\`) || !loaded.HasFile(req.Script) {
		return entry, nil, nil, refuse("%q is not a bundled script of skill %q", req.Script, req.Skill)
	}
	interpreter, ok := interpreters[path.Ext(req.Script)]
	if !ok {
		return entry, nil, nil, refuse("no interpreter for %q", req.Script)
	}

	args := bytes.TrimSpace(req.Args)
	if len(args) == 0 || bytes.Equal(args, []byte("null")) {
		args = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil || obj == nil {
		return entry, nil, nil, refuse("arguments are not a JSON object")
	}
	compact := new(bytes.Buffer)
	if err := json.Compact(compact, args); err != nil {
		return entry, nil, nil, refuse("arguments are not a JSON object")
	}
	return entry, []string{interpreter, "/skill/" + req.Script}, compact.Bytes(), nil
}

type runLimits struct {
	timeout   time.Duration
	memory    int64
	maxOutput int
}

func limitsFor(e skills.Entry) (runLimits, error) {
	l := runLimits{timeout: e.ScriptLimits.Timeout, maxOutput: e.ScriptLimits.MaxOutputBytes}
	if l.timeout == 0 {
		l.timeout = DefaultTimeout
	}
	if l.maxOutput == 0 {
		l.maxOutput = DefaultMaxOutputBytes
	}
	mem := e.ScriptLimits.Memory
	if mem == "" {
		mem = DefaultMemory
	}
	m, err := parseMemory(mem)
	if err != nil {
		return runLimits{}, refuse("skill %q: %v", e.Name, err)
	}
	l.memory = m
	return l, nil
}

// minMemory is the smallest memory limit Docker accepts.
const minMemory = 6 << 20

// parseMemory reads a size such as "256m": a positive integer with an optional k, m or g suffix.
func parseMemory(s string) (int64, error) {
	unit := int64(1)
	num := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.HasSuffix(num, "k"):
		unit, num = 1<<10, strings.TrimSuffix(num, "k")
	case strings.HasSuffix(num, "m"):
		unit, num = 1<<20, strings.TrimSuffix(num, "m")
	case strings.HasSuffix(num, "g"):
		unit, num = 1<<30, strings.TrimSuffix(num, "g")
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 || n > (1<<40)/unit {
		return 0, fmt.Errorf("invalid memory limit %q", s)
	}
	if n*unit < minMemory {
		return 0, fmt.Errorf("memory limit %q is below 6m", s)
	}
	return n * unit, nil
}
