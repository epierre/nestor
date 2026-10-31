// Package config loads and validates the process configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	defaultLLMModel          = "cheap"
	defaultHistoryLimit      = 30
	defaultMaxLoopIterations = 8
)

// TelegramUser is one entry of the ALLOWED_TELEGRAM_USERS allowlist.
type TelegramUser struct {
	ID          int64
	DisplayName string
}

// Config holds host-wide settings. TelegramBotToken, LLMAPIKey and DatabaseURL are secrets and
// must not be logged.
type Config struct {
	TelegramBotToken     string
	AllowedTelegramUsers []TelegramUser
	DatabaseURL          string
	LLMBaseURL           string
	LLMAPIKey            string
	LLMModel             string
	HistoryLimit         int
	MaxLoopIterations    int
}

// Load reads the configuration through getenv and reports every invalid or missing variable at
// once. Errors name variables but never quote their values, so they are safe to log.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	required := func(name string) string {
		value := get(name)
		if value == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
		return value
	}
	positiveInt := func(name string, fallback int) int {
		value := get(name)
		if value == "" {
			return fallback
		}
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("%s must be a positive integer", name))
		}
		return n
	}

	cfg := Config{
		TelegramBotToken:  required("TELEGRAM_BOT_TOKEN"),
		DatabaseURL:       required("DATABASE_URL"),
		LLMBaseURL:        required("LLM_BASE_URL"),
		LLMAPIKey:         required("LLM_API_KEY"),
		LLMModel:          get("LLM_MODEL"),
		HistoryLimit:      positiveInt("HISTORY_LIMIT", defaultHistoryLimit),
		MaxLoopIterations: positiveInt("MAX_LOOP_ITERATIONS", defaultMaxLoopIterations),
	}
	if cfg.LLMModel == "" {
		cfg.LLMModel = defaultLLMModel
	}
	if cfg.LLMBaseURL != "" && !isHTTPURL(cfg.LLMBaseURL) {
		errs = append(errs, errors.New("LLM_BASE_URL must be an http or https URL"))
	}

	if allowlist := required("ALLOWED_TELEGRAM_USERS"); allowlist != "" {
		users, err := parseAllowlist(allowlist)
		if err != nil {
			errs = append(errs, fmt.Errorf("ALLOWED_TELEGRAM_USERS: %w", err))
		}
		cfg.AllowedTelegramUsers = users
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// parseAllowlist parses comma-separated telegram_id:display_name pairs. Errors identify entries
// by position because the entries themselves are personal data.
func parseAllowlist(raw string) ([]TelegramUser, error) {
	entries := strings.Split(raw, ",")
	users := make([]TelegramUser, 0, len(entries))
	seen := make(map[int64]bool, len(entries))
	for i, entry := range entries {
		idText, name, found := strings.Cut(entry, ":")
		if !found {
			return nil, fmt.Errorf("entry %d: want telegram_id:display_name", i+1)
		}
		id, err := strconv.ParseInt(strings.TrimSpace(idText), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("entry %d: telegram_id must be a positive integer", i+1)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("entry %d: display_name is empty", i+1)
		}
		if seen[id] {
			return nil, fmt.Errorf("entry %d: duplicate telegram_id", i+1)
		}
		seen[id] = true
		users = append(users, TelegramUser{ID: id, DisplayName: name})
	}
	return users, nil
}
