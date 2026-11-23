package sandbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/skills"
)

// fakeEngine records what the runner asks of Docker.
type fakeEngine struct {
	mu        sync.Mutex
	specs     []ContainerSpec
	removed   []string
	started   []string
	exitCode  int
	output    []byte
	block     bool // Wait blocks until its context ends, like a script that never exits
	createErr error
}

func (f *fakeEngine) Create(_ context.Context, spec ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.specs = append(f.specs, spec)
	return "c1", nil
}

func (f *fakeEngine) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
	return nil
}

func (f *fakeEngine) Wait(ctx context.Context, _ string) (int, error) {
	if f.block {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return f.exitCode, nil
}

func (f *fakeEngine) Logs(_ context.Context, _ string, max int) ([]byte, bool, error) {
	if len(f.output) > max {
		return f.output[:max], true, nil
	}
	return f.output, false, nil
}

func (f *fakeEngine) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeEngine) MountSource(context.Context, string, string) (string, error) {
	return "", errors.New("not in a container")
}

func (f *fakeEngine) ImageExists(context.Context, string) (bool, error) { return true, nil }

var errInjected = errors.New("injected")

const probeSkill = "---\nname: probe\ndescription: Probe the sandbox.\n---\nRun scripts/hello.py.\n"

// newFixture writes the skills "probe" (scripts enabled) and "quiet" (scripts disabled), pins
// them, and returns a runner over a fake engine.
func newFixture(t *testing.T, limits skills.ScriptLimits) (*Runner, *fakeEngine, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"probe/SKILL.md":         probeSkill,
		"probe/scripts/hello.py": "print('hello')\n",
		"probe/scripts/run.sh":   "echo hi\n",
		"probe/notes.md":         "not a script\n",
		"quiet/SKILL.md":         strings.Replace(probeSkill, "name: probe", "name: quiet", 1),
		"quiet/scripts/hello.py": "print('hello')\n",
	}
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pin := func(name string) string {
		h, err := skills.HashDir(os.DirFS(filepath.Join(root, name)))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	cfg := skills.Config{Skills: []skills.Entry{
		{Name: "probe", Hash: pin("probe"), Users: []string{"*"}, Scripts: true, ScriptLimits: limits},
		{Name: "quiet", Hash: pin("quiet"), Users: []string{"*"}},
	}}
	eng := &fakeEngine{output: []byte("hello\n")}
	r := &Runner{
		Engine:        eng,
		Config:        cfg,
		SkillsDir:     root,
		HostSkillsDir: "/srv/nestor/skills",
		Log:           slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}
	return r, eng, root
}

func TestRunContainerSettings(t *testing.T) {
	r, eng, _ := newFixture(t, skills.ScriptLimits{Timeout: 5 * time.Second, Memory: "128m", MaxOutputBytes: 1024})
	res, err := r.Run(context.Background(), Request{Skill: "probe", Script: "scripts/hello.py", Args: []byte(`{"n":1}`)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 || res.Output != "hello\n" || res.Truncated || res.TimedOut {
		t.Errorf("result %+v", res)
	}

	want := ContainerSpec{
		Image:           RunnerImage,
		Cmd:             []string{"python3", "/skill/scripts/hello.py"},
		Env:             []string{`NESTOR_ARGS={"n":1}`},
		User:            "65532:65532",
		WorkingDir:      "/work",
		NetworkDisabled: true,
		Labels:          map[string]string{runLabel: "probe"},
		HostConfig: HostConfig{
			NetworkMode:    "none",
			ReadonlyRootfs: true,
			Binds:          []string{"/srv/nestor/skills/probe:/skill:ro"},
			Tmpfs: map[string]string{
				"/work": tmpfsOptions,
				"/tmp":  tmpfsOptions,
			},
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
			Memory:      128 << 20,
			MemorySwap:  128 << 20,
			NanoCPUs:    1e9,
			PidsLimit:   64,
		},
	}
	if len(eng.specs) != 1 || !reflect.DeepEqual(eng.specs[0], want) {
		t.Fatalf("container spec\n got %+v\nwant %+v", eng.specs, want)
	}
	if !reflect.DeepEqual(eng.removed, []string{"c1"}) {
		t.Errorf("removed %v, want the container removed", eng.removed)
	}
}

func TestRunDefaults(t *testing.T) {
	r, eng, _ := newFixture(t, skills.ScriptLimits{})
	if _, err := r.Run(context.Background(), Request{Skill: "probe", Script: "scripts/run.sh"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	spec := eng.specs[0]
	if !reflect.DeepEqual(spec.Cmd, []string{"sh", "/skill/scripts/run.sh"}) {
		t.Errorf("cmd %v", spec.Cmd)
	}
	if !reflect.DeepEqual(spec.Env, []string{"NESTOR_ARGS={}"}) {
		t.Errorf("env %v, want empty object arguments", spec.Env)
	}
	if spec.HostConfig.Memory != 256<<20 {
		t.Errorf("memory %d, want the 256m default", spec.HostConfig.Memory)
	}
}

func TestRunRefused(t *testing.T) {
	tests := []struct {
		name    string
		req     Request
		mutate  func(t *testing.T, root string)
		wantErr string
	}{
		{"unknown skill", Request{Skill: "nope", Script: "scripts/hello.py"}, nil, `unknown skill "nope"`},
		{"scripts disabled", Request{Skill: "quiet", Script: "scripts/hello.py"}, nil, `skill "quiet" has scripts disabled`},
		{"script not bundled", Request{Skill: "probe", Script: "scripts/other.py"}, nil, `not a bundled script`},
		{"traversal", Request{Skill: "probe", Script: "../quiet/scripts/hello.py"}, nil, `not a bundled script`},
		{"absolute path", Request{Skill: "probe", Script: "/bin/sh"}, nil, `not a bundled script`},
		{"no interpreter", Request{Skill: "probe", Script: "notes.md"}, nil, `no interpreter`},
		{"invalid args", Request{Skill: "probe", Script: "scripts/hello.py", Args: []byte(`{"a":`)}, nil, `arguments are not a JSON object`},
		{"non-object args", Request{Skill: "probe", Script: "scripts/hello.py", Args: []byte(`[1]`)}, nil, `arguments are not a JSON object`},
		{"skill edited after pinning", Request{Skill: "probe", Script: "scripts/hello.py"},
			func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "probe", "scripts", "hello.py"), []byte("import os\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}, `does not match pinned hash`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, eng, root := newFixture(t, skills.ScriptLimits{})
			if tt.mutate != nil {
				tt.mutate(t, root)
			}
			_, err := r.Run(context.Background(), tt.req)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Run() error = %v, want refusal containing %q", err, tt.wantErr)
			}
			if len(eng.specs) != 0 {
				t.Fatalf("a container was created: %+v", eng.specs)
			}
		})
	}
}

func TestRunTimeoutRemovesContainer(t *testing.T) {
	r, eng, _ := newFixture(t, skills.ScriptLimits{Timeout: 50 * time.Millisecond})
	eng.block = true
	eng.output = []byte("partial")
	res, err := r.Run(context.Background(), Request{Skill: "probe", Script: "scripts/hello.py"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.TimedOut || res.Output != "partial" {
		t.Errorf("result %+v, want timed out with the output so far", res)
	}
	if !reflect.DeepEqual(eng.removed, []string{"c1"}) {
		t.Errorf("removed %v, want the container removed", eng.removed)
	}
}

func TestRunOutputCapped(t *testing.T) {
	r, eng, _ := newFixture(t, skills.ScriptLimits{MaxOutputBytes: 4})
	eng.output = []byte("0123456789")
	res, err := r.Run(context.Background(), Request{Skill: "probe", Script: "scripts/hello.py"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Output != "0123" || !res.Truncated {
		t.Errorf("result %+v, want 4 bytes and truncated", res)
	}
}

func TestRunEngineFailure(t *testing.T) {
	r, eng, _ := newFixture(t, skills.ScriptLimits{})
	eng.createErr = errors.New("no such image")
	_, err := r.Run(context.Background(), Request{Skill: "probe", Script: "scripts/hello.py"})
	if err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("Run() error = %v, want a run failure", err)
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"256m", 256 << 20, true},
		{"1g", 1 << 30, true},
		{"8192k", 8 << 20, true},
		{"8388608", 8 << 20, true},
		{"", 0, false},
		{"-1m", 0, false},
		{"lots", 0, false},
		{"2m", 0, false}, // below the 6 MiB Docker minimum
	}
	for _, tt := range tests {
		got, err := parseMemory(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("parseMemory(%q) = %d, %v", tt.in, got, err)
		}
	}
}
