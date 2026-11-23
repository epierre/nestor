package credentials

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/epierre/nestor/internal/identity"
)

func testSealer(t *testing.T, key []byte) *Sealer {
	t.Helper()
	s, err := NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return s
}

var (
	keyA = bytes.Repeat([]byte{1}, KeySize)
	keyB = bytes.Repeat([]byte{2}, KeySize)
)

func TestRoundTrip(t *testing.T) {
	s := testSealer(t, keyA)
	plain := []byte("test-token")

	ct, nonce, err := s.Seal(1, "todoist_token", plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := s.Open(1, "todoist_token", ct, nonce)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("Open = %q, want %q", got, plain)
	}
	if bytes.Contains(ct, plain) {
		t.Error("ciphertext contains the plaintext")
	}
}

func TestCiphertextDiffersPerWrite(t *testing.T) {
	s := testSealer(t, keyA)
	ct1, n1, err := s.Seal(1, "todoist_token", []byte("test-token"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	ct2, n2, err := s.Seal(1, "todoist_token", []byte("test-token"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(n1, n2) {
		t.Error("nonce repeated across writes")
	}
	if bytes.Equal(ct1, ct2) {
		t.Error("same plaintext produced the same ciphertext")
	}
}

func TestWrongKeyFails(t *testing.T) {
	ct, nonce, err := testSealer(t, keyA).Seal(1, "todoist_token", []byte("test-token"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := testSealer(t, keyB).Open(1, "todoist_token", ct, nonce); err == nil {
		t.Error("Open with the wrong key succeeded")
	}
}

func TestCiphertextBoundToUserAndKey(t *testing.T) {
	s := testSealer(t, keyA)
	ct, nonce, err := s.Seal(1, "todoist_token", []byte("test-token"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	tests := []struct {
		name string
		user identity.UserID
		key  string
	}{
		{"another user", 2, "todoist_token"},
		{"another credential key", 1, "other_token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Open(tt.user, tt.key, ct, nonce); err == nil {
				t.Error("row moved to another user or key still decrypted")
			}
		})
	}
}

func TestParseKey(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(keyA)
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid 32-byte key", good, false},
		{"short key", base64.StdEncoding.EncodeToString(keyA[:16]), true},
		{"not base64", "not base64!", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseKey(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseKey error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
