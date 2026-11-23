// Command sandboxd runs scripts bundled with reviewed skills in throwaway containers. It is the
// only process given the Docker socket, and it accepts one operation, POST /run, from Nestor on
// the internal sandbox network. See openspec/changes/add-skills/design.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/epierre/nestor/internal/sandbox"
	"github.com/epierre/nestor/internal/skills"
)

const (
	dockerSocket = "/var/run/docker.sock"
	defaultAddr  = ":8080"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("sandboxd stopped", "error", err)
		os.Exit(1)
	}
}

type settings struct {
	addr      string
	skillsDir string
	config    skills.Config
}

func loadSettings(getenv func(string) string) (settings, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	s := settings{addr: get("SANDBOXD_ADDR"), skillsDir: get("SKILLS_DIR")}
	if s.addr == "" {
		s.addr = defaultAddr
	}
	path := get("SKILLS_CONFIG")
	switch {
	case path == "":
		return settings{}, errors.New("SKILLS_CONFIG is required")
	case s.skillsDir == "":
		return settings{}, errors.New("SKILLS_DIR is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return settings{}, fmt.Errorf("open skills config: %w", err)
	}
	defer f.Close()
	if s.config, err = skills.DecodeConfig(f); err != nil {
		return settings{}, err
	}
	return s, nil
}

// hostSkillsDir returns the skills folder as the Docker daemon sees it. Inside a container it is
// the host source of the mount at skillsDir, found by inspecting this container (whose hostname is
// its ID); a failure there stops startup rather than bind-mounting a wrong path.
func hostSkillsDir(ctx context.Context, engine sandbox.Engine, skillsDir, hostname string, inContainer bool) (string, error) {
	if !inContainer {
		return skillsDir, nil
	}
	src, err := engine.MountSource(ctx, hostname, skillsDir)
	if err != nil {
		return "", fmt.Errorf("find host path of %s: %w", skillsDir, err)
	}
	return src, nil
}

func run(log *slog.Logger) error {
	s, err := loadSettings(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	engine := sandbox.NewDocker(dockerSocket)
	hostname, _ := os.Hostname()
	_, statErr := os.Stat("/.dockerenv")
	hostDir, err := hostSkillsDir(ctx, engine, s.skillsDir, hostname, statErr == nil)
	if err != nil {
		return err
	}
	if ok, err := engine.ImageExists(ctx, sandbox.RunnerImage); err != nil || !ok {
		log.Error("skill runner image missing; build it with: docker compose --profile build build skill-runner",
			"image", sandbox.RunnerImage, "error", err)
	}

	runner := &sandbox.Runner{Engine: engine, Config: s.config, SkillsDir: s.skillsDir, HostSkillsDir: hostDir, Log: log}
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           sandbox.Handler(runner, log),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("sandboxd started", "addr", s.addr, "skills", len(s.config.Skills))

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
