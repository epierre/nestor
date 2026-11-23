package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/skills"
)

// loadSkills reads the reviewed skills.yaml and loads the skills that match their pins. No
// SKILLS_CONFIG means no skills. An unreadable or invalid skills.yaml is an error that stops
// startup; a single skill that fails its pin is skipped and logged by skills.Load.
func loadSkills(cfg config.Config, users []identity.UserID, log *slog.Logger) ([]skills.Loaded, error) {
	if cfg.SkillsConfigPath == "" {
		return nil, nil
	}
	f, err := os.Open(cfg.SkillsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("open skills config: %w", err)
	}
	defer f.Close()
	c, err := skills.LoadConfig(f, users)
	if err != nil {
		return nil, err
	}
	return skills.Load(cfg.SkillsDir, c, log), nil
}
