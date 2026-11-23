package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/testutil"
)

func TestParseSetArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantUser identity.UserID
		wantKey  string
		wantErr  bool
	}{
		{"valid", []string{"1", "todoist_token"}, 1, "todoist_token", false},
		{"non-numeric user", []string{"ana", "todoist_token"}, 0, "", true},
		{"zero user", []string{"0", "todoist_token"}, 0, "", true},
		{"missing key", []string{"1"}, 0, "", true},
		{"extra argument", []string{"1", "todoist_token", "value"}, 0, "", true},
		{"empty key", []string{"1", ""}, 0, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, key, err := parseSetArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSetArgs error = %v, wantErr %v", err, tt.wantErr)
			}
			if user != tt.wantUser || key != tt.wantKey {
				t.Errorf("got (%d, %q), want (%d, %q)", user, key, tt.wantUser, tt.wantKey)
			}
		})
	}
}

func TestSetCredentialStoresCiphertextOnly(t *testing.T) {
	const secret = "token-sentinel-123"
	sealer, err := credentials.NewSealer(bytes.Repeat([]byte{7}, credentials.KeySize))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	st := testutil.NewFakeStore()
	ctx := context.Background()
	var out bytes.Buffer

	err = setCredential(ctx, st, sealer, 1, "todoist_token", strings.NewReader(secret+"\n"), &out)
	if err != nil {
		t.Fatalf("setCredential: %v", err)
	}

	got, err := st.GetCredential(identity.WithUser(ctx, 1), 1, "todoist_token")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if bytes.Contains(got.Ciphertext, []byte(secret)) {
		t.Error("stored ciphertext contains the plaintext")
	}
	plain, err := sealer.Open(1, "todoist_token", got.Ciphertext, got.Nonce)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(plain) != secret {
		t.Errorf("stored value decrypts to %q, want %q", plain, secret)
	}
	if strings.Contains(out.String(), secret) {
		t.Errorf("command output shows the secret: %q", out.String())
	}
}
