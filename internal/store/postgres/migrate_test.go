package postgres

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func file(sql string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(sql)}
}

func TestLoadMigrationsOrdersByVersion(t *testing.T) {
	got, err := loadMigrations(fstest.MapFS{
		"migrations/0010_later.up.sql":   file("up 10"),
		"migrations/0010_later.down.sql": file("down 10"),
		"migrations/0002_next.up.sql":    file("up 2"),
		"migrations/0002_next.down.sql":  file("down 2"),
	})
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	want := []migration{
		{version: 2, up: "up 2", down: "down 2"},
		{version: 10, up: "up 10", down: "down 10"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLoadMigrationsRejectsInvalidSets(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{"missing down", fstest.MapFS{
			"migrations/0001_a.up.sql": file("up"),
		}, "version 1"},
		{"missing up", fstest.MapFS{
			"migrations/0001_a.down.sql": file("down"),
		}, "version 1"},
		{"duplicate version", fstest.MapFS{
			"migrations/0001_a.up.sql":   file("up"),
			"migrations/0001_a.down.sql": file("down"),
			"migrations/0001_b.up.sql":   file("up"),
			"migrations/0001_b.down.sql": file("down"),
		}, "version 1"},
		{"unexpected name", fstest.MapFS{
			"migrations/init.sql": file("up"),
		}, "init.sql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadMigrations(tt.files)
			if err == nil {
				t.Fatal("loadMigrations succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not name %q", err, tt.want)
			}
		})
	}
}

func TestEmbeddedMigrationsAreValid(t *testing.T) {
	got, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no embedded migrations")
	}
}
