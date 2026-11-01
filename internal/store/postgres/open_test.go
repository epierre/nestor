package postgres

import (
	"context"
	"strings"
	"testing"
)

func TestOpenFailsWithoutLeakingPassword(t *testing.T) {
	const password = "db-password-sentinel"
	tests := []struct {
		name string
		url  string
	}{
		{"server unreachable", "postgres://nestor:" + password + "@127.0.0.1:1/nestor"},
		{"malformed URL", "postgres://nestor:" + password + "@bad host/nestor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(context.Background(), tt.url)
			if err == nil {
				t.Fatal("Open succeeded, want error")
			}
			if strings.Contains(err.Error(), password) {
				t.Errorf("error %q contains the password", err)
			}
		})
	}
}
