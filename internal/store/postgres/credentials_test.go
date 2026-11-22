//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
)

func testSealerFor(t *testing.T, fill byte) *credentials.Sealer {
	t.Helper()
	s, err := credentials.NewSealer(bytes.Repeat([]byte{fill}, credentials.KeySize))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return s
}

// setSealed encrypts plain for user and stores it under key.
func setSealed(t *testing.T, st *Store, sealer *credentials.Sealer, ctx context.Context, user identity.UserID, key, plain string) {
	t.Helper()
	ct, nonce, err := sealer.Seal(user, key, []byte(plain))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := st.SetCredential(ctx, user, key, ct, nonce); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
}

func TestCredentialRoundTrip(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	sealer := testSealerFor(t, 1)
	setSealed(t, st, sealer, ctxA, a, "todoist_token", "test-token")

	got, err := st.GetCredential(ctxA, a, "todoist_token")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	plain, err := sealer.Open(a, "todoist_token", got.Ciphertext, got.Nonce)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(plain) != "test-token" {
		t.Errorf("round trip = %q, want %q", plain, "test-token")
	}
}

func TestCiphertextDiffersPerWrite(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	sealer := testSealerFor(t, 1)

	setSealed(t, st, sealer, ctxA, a, "todoist_token", "test-token")
	first, err := st.GetCredential(ctxA, a, "todoist_token")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	setSealed(t, st, sealer, ctxA, a, "todoist_token", "test-token")
	second, err := st.GetCredential(ctxA, a, "todoist_token")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Error("ciphertext stayed the same after a second write")
	}
}

func TestCredentialWrongKeyFails(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	setSealed(t, st, testSealerFor(t, 1), ctxA, a, "todoist_token", "test-token")

	got, err := st.GetCredential(ctxA, a, "todoist_token")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if _, err := testSealerFor(t, 2).Open(a, "todoist_token", got.Ciphertext, got.Nonce); err == nil {
		t.Error("Open with the wrong key succeeded")
	}
}

func TestCredentialsScopedToUser(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	setSealed(t, st, testSealerFor(t, 1), ctxA, a, "todoist_token", "test-token")

	_, err := st.GetCredential(ctxB, b, "todoist_token")
	if !errors.Is(err, store.ErrCredentialNotFound) {
		t.Errorf("user B read user A's credential: err = %v, want ErrCredentialNotFound", err)
	}
}
