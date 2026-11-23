package skills

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/identity"
)

func TestParseSkill(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Skill
		wantErr string // substring; empty means valid
	}{
		{
			name: "valid skill parsed",
			input: "---\nname: weekly-review\ndescription: Run my Sunday weekly review of tasks and calendar.\n" +
				"license: MIT\n---\n\n# Weekly review\n\n1. List open tasks.\n",
			want: Skill{
				Name:         "weekly-review",
				Description:  "Run my Sunday weekly review of tasks and calendar.",
				Instructions: "# Weekly review\n\n1. List open tasks.\n",
			},
		},
		{
			name:  "CRLF line endings accepted",
			input: "---\r\nname: triage\r\ndescription: Sort mail.\r\n---\r\nDo it.\r\n",
			want:  Skill{Name: "triage", Description: "Sort mail.", Instructions: "Do it.\r\n"},
		},
		{
			name:    "missing name rejected",
			input:   "---\ndescription: Something useful.\n---\nBody.\n",
			wantErr: "no name",
		},
		{
			name:    "missing description rejected",
			input:   "---\nname: weekly-review\n---\nBody.\n",
			wantErr: "no description",
		},
		{
			name:    "description over cap rejected",
			input:   "---\nname: weekly-review\ndescription: " + strings.Repeat("é", MaxDescriptionRunes+1) + "\n---\nBody.\n",
			wantErr: "description is longer than 200 characters",
		},
		{
			name:  "description at cap accepted",
			input: "---\nname: weekly-review\ndescription: " + strings.Repeat("é", MaxDescriptionRunes) + "\n---\nBody.\n",
			want: Skill{
				Name:         "weekly-review",
				Description:  strings.Repeat("é", MaxDescriptionRunes),
				Instructions: "Body.\n",
			},
		},
		{
			name:    "name with path separator rejected",
			input:   "---\nname: ../config\ndescription: Escape.\n---\nBody.\n",
			wantErr: "invalid name",
		},
		{
			name:    "missing front matter rejected",
			input:   "# Weekly review\n",
			wantErr: "front matter",
		},
		{
			name:    "unterminated front matter rejected",
			input:   "---\nname: weekly-review\ndescription: x\n",
			wantErr: "front matter",
		},
		{
			name:    "malformed front matter rejected",
			input:   "---\nname: [weekly\n---\nBody.\n",
			wantErr: "front matter",
		},
		{
			name:    "empty body rejected",
			input:   "---\nname: weekly-review\ndescription: x\n---\n  \n",
			wantErr: "no instructions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSkill([]byte(tt.input))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseSkill() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSkill() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseSkill() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

const validConfig = `
skills:
  - name: weekly-review
    hash: sha256:3f1c0000000000000000000000000000000000000000000000000000000000aa
    users: ["*"]
    scripts: false
  - name: newsletter-triage
    hash: sha256:9a070000000000000000000000000000000000000000000000000000000000bb
    users: [1]
    scripts: true
    script_limits: { timeout: 20s, memory: 256m, max_output_bytes: 32768 }
`

func TestLoadConfig(t *testing.T) {
	known := []identity.UserID{1, 2}
	tests := []struct {
		name    string
		input   string
		want    Config
		wantErr string // substring; empty means valid
	}{
		{
			name:  "valid config parsed",
			input: validConfig,
			want: Config{Skills: []Entry{
				{
					Name:  "weekly-review",
					Hash:  "sha256:3f1c0000000000000000000000000000000000000000000000000000000000aa",
					Users: []string{"*"},
				},
				{
					Name:    "newsletter-triage",
					Hash:    "sha256:9a070000000000000000000000000000000000000000000000000000000000bb",
					Users:   []string{"1"},
					Scripts: true,
					ScriptLimits: ScriptLimits{
						Timeout:        20 * time.Second,
						Memory:         "256m",
						MaxOutputBytes: 32768,
					},
				},
			}},
		},
		{
			name:  "empty file is no skills",
			input: "",
			want:  Config{},
		},
		{
			name:    "unknown field rejected",
			input:   "skills:\n  - name: a\n    hash: sha256:" + strings.Repeat("0", 64) + "\n    risk: read\n",
			wantErr: "field risk not found",
		},
		{
			name:    "duplicate name rejected",
			input:   "skills:\n  - {name: a, hash: \"sha256:" + strings.Repeat("0", 64) + "\"}\n  - {name: a, hash: \"sha256:" + strings.Repeat("1", 64) + "\"}\n",
			wantErr: `skill "a" is listed twice`,
		},
		{
			name:    "missing hash rejected",
			input:   "skills:\n  - name: a\n",
			wantErr: `skill "a" is not pinned`,
		},
		{
			name:    "malformed hash rejected",
			input:   "skills:\n  - {name: a, hash: \"md5:abc\"}\n",
			wantErr: `skill "a" has malformed hash`,
		},
		{
			name:    "invalid name rejected",
			input:   "skills:\n  - {name: ../etc, hash: \"sha256:" + strings.Repeat("0", 64) + "\"}\n",
			wantErr: "invalid name",
		},
		{
			name:    "unknown user rejected",
			input:   "skills:\n  - {name: a, hash: \"sha256:" + strings.Repeat("0", 64) + "\", users: [7]}\n",
			wantErr: `skill "a" refers to unknown user "7"`,
		},
		{
			name:    "limits without scripts rejected",
			input:   "skills:\n  - {name: a, hash: \"sha256:" + strings.Repeat("0", 64) + "\", script_limits: {timeout: 5s}}\n",
			wantErr: `skill "a" sets script_limits but scripts are disabled`,
		},
		{
			name:    "negative limit rejected",
			input:   "skills:\n  - {name: a, hash: \"sha256:" + strings.Repeat("0", 64) + "\", scripts: true, script_limits: {max_output_bytes: -1}}\n",
			wantErr: `skill "a" has negative script limits`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadConfig(strings.NewReader(tt.input), known)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("LoadConfig() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LoadConfig() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDecodeConfig(t *testing.T) {
	hash := "sha256:" + strings.Repeat("0", 64)
	c, err := DecodeConfig(strings.NewReader("skills:\n  - {name: a, hash: \"" + hash + "\", users: [7, \"*\"]}\n"))
	if err != nil || len(c.Skills) != 1 || c.Skills[0].Users[0] != "7" {
		t.Fatalf("DecodeConfig() = %+v, %v; want user 7 accepted without a user list", c, err)
	}
	for _, bad := range []string{
		"skills:\n  - {name: a, hash: \"" + hash + "\", users: [bob]}\n",
		"skills:\n  - {name: a, hash: \"md5:x\"}\n",
		"skills:\n  - {name: a, hash: \"" + hash + "\", image: alpine}\n",
	} {
		if _, err := DecodeConfig(strings.NewReader(bad)); err == nil {
			t.Errorf("DecodeConfig(%q) succeeded", bad)
		}
	}
}

func TestEnabledFor(t *testing.T) {
	tests := []struct {
		name  string
		users []string
		user  identity.UserID
		want  bool
	}{
		{"wildcard includes everyone", []string{"*"}, 2, true},
		{"listed user included", []string{"1"}, 1, true},
		{"other user excluded", []string{"1"}, 2, false},
		{"empty list excludes everyone", nil, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Entry{Name: "a", Users: tt.users}
			if got := e.EnabledFor(tt.user); got != tt.want {
				t.Fatalf("EnabledFor(%d) = %v, want %v", tt.user, got, tt.want)
			}
		})
	}
}
