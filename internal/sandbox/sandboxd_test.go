//go:build integration

package sandbox

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/skills"
)

// These tests need a Docker daemon at /var/run/docker.sock that can build images. Like the
// Postgres suite, they fail rather than skip without one.

var buildOnce sync.Once

func buildRunnerImage(t *testing.T) {
	t.Helper()
	var err error
	var out []byte
	buildOnce.Do(func() {
		cmd := exec.Command("docker", "build", "-q", "-t", RunnerImage, "-f", "deploy/skill-runner/Dockerfile", ".")
		cmd.Dir = "../.."
		out, err = cmd.CombinedOutput()
	})
	if err != nil {
		t.Fatalf("build %s: %v\n%s", RunnerImage, err, out)
	}
}

const probeScripts = `---
name: probe
description: Probe the sandbox.
---
Run the scripts.
`

var scripts = map[string]string{
	"SKILL.md": probeScripts,
	"scripts/net.py": `import json, socket, sys
args = json.load(open("/work/args.json"))
print("args", args.get("n"))
try:
    socket.create_connection(("1.1.1.1", 80), timeout=3)
    print("connected")
    sys.exit(0)
except OSError as e:
    print("network error:", e)
    sys.exit(3)
`,
	"scripts/write.py": `import sys
try:
    open("/skill/new.txt", "w").write("x")
    print("wrote skill folder")
    sys.exit(0)
except OSError as e:
    print("write failed:", e)
    sys.exit(4)
`,
	"scripts/sleep.sh": "sleep 60\n",
	"scripts/big.py":   "print('x' * 100000)\n",
}

// realRunner writes the probe skill to a folder the runner's user can read, pins it and returns a
// runner over the local Docker daemon.
func realRunner(t *testing.T, limits skills.ScriptLimits) *Runner {
	t.Helper()
	buildRunnerImage(t)
	root := t.TempDir()
	for p, c := range scripts {
		full := filepath.Join(root, "probe", filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// t.TempDir creates folders only its owner can enter; the scripts run as 65532.
	for dir := root; dir != filepath.Dir(dir) && strings.HasPrefix(dir, os.TempDir()) && dir != os.TempDir(); dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := skills.HashDir(os.DirFS(filepath.Join(root, "probe")))
	if err != nil {
		t.Fatal(err)
	}
	cfg := skills.Config{Skills: []skills.Entry{{Name: "probe", Hash: hash, Users: []string{"*"}, Scripts: true, ScriptLimits: limits}}}
	t.Cleanup(func() { assertNoRunContainers(t) })
	return &Runner{Engine: NewDocker("/var/run/docker.sock"), Config: cfg, SkillsDir: root, HostSkillsDir: root, Log: slog.Default()}
}

func runContainers(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-a", "-q", "--filter", "label="+runLabel).Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	return strings.Fields(string(out))
}

func assertNoRunContainers(t *testing.T) {
	t.Helper()
	if ids := runContainers(t); len(ids) != 0 {
		t.Errorf("sandbox containers left behind: %v", ids)
		_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
	}
}

func TestSandboxNetworkUnavailable(t *testing.T) {
	r := realRunner(t, skills.ScriptLimits{Timeout: 30 * time.Second})
	res, err := r.Run(context.Background(), Request{Skill: "probe", Script: "scripts/net.py", Args: []byte(`{"n": 7}`)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 3 || !strings.Contains(res.Output, "network error") || strings.Contains(res.Output, "connected") {
		t.Fatalf("result %+v, want a network error and exit status 3", res)
	}
	if !strings.Contains(res.Output, "args 7") {
		t.Errorf("output %q, want the arguments read from /work/args.json", res.Output)
	}
}

func TestSandboxReadOnlyMount(t *testing.T) {
	r := realRunner(t, skills.ScriptLimits{Timeout: 30 * time.Second})
	res, err := r.Run(context.Background(), Request{Skill: "probe", Script: "scripts/write.py"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 4 || !strings.Contains(res.Output, "Read-only file system") {
		t.Fatalf("result %+v, want the write to fail on a read-only file system", res)
	}
	if _, err := os.Stat(filepath.Join(r.SkillsDir, "probe", "new.txt")); err == nil {
		t.Fatal("the script wrote into the skill folder")
	}
}

func TestSandboxTimeoutKillsAndRemovesContainer(t *testing.T) {
	r := realRunner(t, skills.ScriptLimits{Timeout: 2 * time.Second})
	start := time.Now()
	res, err := r.Run(context.Background(), Request{Skill: "probe", Script: "scripts/sleep.sh"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("result %+v, want timed out", res)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("run took %v after a 2s timeout", elapsed)
	}
	if ids := runContainers(t); len(ids) != 0 {
		t.Fatalf("container not removed after the timeout: %v", ids)
	}
}

func TestSandboxOutputCapped(t *testing.T) {
	r := realRunner(t, skills.ScriptLimits{Timeout: 30 * time.Second, MaxOutputBytes: 1024})
	res, err := r.Run(context.Background(), Request{Skill: "probe", Script: "scripts/big.py"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Output) != 1024 || !res.Truncated || res.ExitCode != 0 {
		t.Fatalf("got %d bytes, truncated %v, exit %d; want 1024 bytes truncated", len(res.Output), res.Truncated, res.ExitCode)
	}
}

func TestSandboxArbitraryImageRefused(t *testing.T) {
	r := realRunner(t, skills.ScriptLimits{})
	var logs bytes.Buffer
	srv := httptest.NewServer(Handler(r, slog.New(slog.NewTextHandler(&logs, nil))))
	defer srv.Close()

	for _, body := range []string{
		`{"skill":"probe","script":"scripts/net.py","image":"alpine:latest"}`,
		`{"skill":"probe","script":"scripts/net.py","cmd":["sh","-c","id"]}`,
		`{"skill":"probe","script":"/bin/sh"}`,
	} {
		resp, err := http.Post(srv.URL+"/run", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, resp.StatusCode)
		}
		if ids := runContainers(t); len(ids) != 0 {
			t.Fatalf("a container was created for %s: %v", body, ids)
		}
	}
	if strings.Count(logs.String(), "sandbox request refused") != 3 {
		t.Errorf("refusals not all logged:\n%s", logs.String())
	}
}
