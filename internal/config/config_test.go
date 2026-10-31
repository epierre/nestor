package config

import (
	"reflect"
	"strings"
	"testing"
)

const (
	testToken    = "bot-token-sentinel"
	testAPIKey   = "api-key-sentinel"
	testDBSecret = "db-password-sentinel"
)

func validEnv() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN":     testToken,
		"ALLOWED_TELEGRAM_USERS": "123:Owner",
		"DATABASE_URL":           "postgres://nestor:" + testDBSecret + "@postgres:5432/nestor",
		"LLM_BASE_URL":           "http://litellm:4000/v1",
		"LLM_API_KEY":            testAPIKey,
	}
}

func load(env map[string]string) (Config, error) {
	return Load(func(name string) string { return env[name] })
}

func TestLoadValid(t *testing.T) {
	env := validEnv()
	env["ALLOWED_TELEGRAM_USERS"] = " 123:Owner , 456:Ana: the second "
	env["LLM_MODEL"] = "strong"
	env["HISTORY_LIMIT"] = "12"
	env["MAX_LOOP_ITERATIONS"] = "3"

	cfg, err := load(env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		TelegramBotToken: testToken,
		AllowedTelegramUsers: []TelegramUser{
			{ID: 123, DisplayName: "Owner"},
			{ID: 456, DisplayName: "Ana: the second"},
		},
		DatabaseURL:       env["DATABASE_URL"],
		LLMBaseURL:        "http://litellm:4000/v1",
		LLMAPIKey:         testAPIKey,
		LLMModel:          "strong",
		HistoryLimit:      12,
		MaxLoopIterations: 3,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(validEnv())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLMModel != "cheap" || cfg.HistoryLimit != 30 || cfg.MaxLoopIterations != 8 {
		t.Errorf("unexpected defaults: model=%q history=%d iterations=%d",
			cfg.LLMModel, cfg.HistoryLimit, cfg.MaxLoopIterations)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	names := []string{
		"TELEGRAM_BOT_TOKEN",
		"ALLOWED_TELEGRAM_USERS",
		"DATABASE_URL",
		"LLM_BASE_URL",
		"LLM_API_KEY",
	}
	for _, name := range names {
		for label, value := range map[string]string{"empty": "", "blank": "   "} {
			t.Run(name+"/"+label, func(t *testing.T) {
				env := validEnv()
				env[name] = value
				_, err := load(env)
				if err == nil {
					t.Fatal("Load succeeded, want error")
				}
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q does not name %s", err, name)
				}
			})
		}
	}
}

func TestLoadReportsEveryMissingVariable(t *testing.T) {
	_, err := load(map[string]string{})
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	for name := range validEnv() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func TestLoadMalformedAllowlist(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"no separator", "123", "entry 1"},
		{"non-numeric id", "abc:Owner", "entry 1"},
		{"negative id", "-123:Owner", "entry 1"},
		{"zero id", "0:Owner", "entry 1"},
		{"id overflows int64", "9223372036854775808:Owner", "entry 1"},
		{"empty id", ":Owner", "entry 1"},
		{"empty name", "123:", "entry 1"},
		{"blank name", "123:   ", "entry 1"},
		{"trailing comma", "123:Owner,", "entry 2"},
		{"second entry bad", "123:Owner,nope", "entry 2"},
		{"duplicate id", "123:Owner,123:Other", "entry 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env["ALLOWED_TELEGRAM_USERS"] = tt.value
			_, err := load(env)
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			if !strings.Contains(err.Error(), "ALLOWED_TELEGRAM_USERS") ||
				!strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not locate %s", err, tt.want)
			}
		})
	}
}

func TestLoadInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"HISTORY_LIMIT", "many"},
		{"HISTORY_LIMIT", "0"},
		{"HISTORY_LIMIT", "-1"},
		{"MAX_LOOP_ITERATIONS", "1.5"},
		{"MAX_LOOP_ITERATIONS", "0"},
		{"LLM_BASE_URL", "litellm:4000"},
		{"LLM_BASE_URL", "ftp://litellm/v1"},
		{"LLM_BASE_URL", "http://"},
		{"LLM_BASE_URL", "http://bad host/"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			env := validEnv()
			env[tt.name] = tt.value
			_, err := load(env)
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.name) {
				t.Errorf("error %q does not name %s", err, tt.name)
			}
		})
	}
}

func TestLoadErrorsContainNoValues(t *testing.T) {
	const (
		urlSecret  = "url-password-sentinel"
		nameSecret = "display-name-sentinel"
	)
	tests := []struct {
		name string
		edit func(env map[string]string)
	}{
		{"missing variable", func(env map[string]string) {
			delete(env, "TELEGRAM_BOT_TOKEN")
		}},
		{"malformed allowlist", func(env map[string]string) {
			env["ALLOWED_TELEGRAM_USERS"] = "abc:" + nameSecret
		}},
		{"invalid URL with credentials", func(env map[string]string) {
			env["LLM_BASE_URL"] = "http://user:" + urlSecret + "@bad host/"
		}},
		{"invalid integer", func(env map[string]string) {
			env["HISTORY_LIMIT"] = urlSecret
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			tt.edit(env)
			_, err := load(env)
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			for _, secret := range []string{testToken, testAPIKey, testDBSecret, urlSecret, nameSecret} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q contains %q", err, secret)
				}
			}
		})
	}
}
