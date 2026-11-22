package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/store/postgres"
)

const credentialsUsage = "usage: nestor credentials set <user-id> <key>   (the value is read from stdin)"

// runCredentials handles "nestor credentials ...". Only "set" exists. It opens the database named
// by the environment, so it runs in the same place as the agent.
func runCredentials(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "set" {
		return errors.New(credentialsUsage)
	}
	user, key, err := parseSetArgs(args[1:])
	if err != nil {
		return err
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.CredentialsKey == nil {
		return errors.New("CREDENTIALS_KEY is not set")
	}
	sealer, err := credentials.NewSealer(cfg.CredentialsKey)
	if err != nil {
		return err
	}

	st, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	return setCredential(ctx, st, sealer, user, key, stdin, stdout)
}

// parseSetArgs reads "<user-id> <key>". The user ID is an internal ID, not a platform identity.
func parseSetArgs(args []string) (identity.UserID, string, error) {
	if len(args) != 2 {
		return 0, "", errors.New(credentialsUsage)
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", fmt.Errorf("user ID must be a positive integer, got %q", args[0])
	}
	if args[1] == "" {
		return 0, "", errors.New("credential key must not be empty")
	}
	return identity.UserID(id), args[1], nil
}

// setCredential reads the secret from r, encrypts it for user and stores it under key. Only the
// key name is written to w; the value never is.
func setCredential(ctx context.Context, st store.Store, sealer *credentials.Sealer, user identity.UserID, key string, r io.Reader, w io.Writer) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read credential value: %w", err)
	}
	value := bytes.TrimRight(raw, "\r\n")
	if len(value) == 0 {
		return errors.New("credential value is empty")
	}
	ciphertext, nonce, err := sealer.Seal(user, key, value)
	if err != nil {
		return fmt.Errorf("encrypt credential: %w", err)
	}
	if err := st.SetCredential(identity.WithUser(ctx, user), user, key, ciphertext, nonce); err != nil {
		return fmt.Errorf("store credential: %w", err)
	}
	_, err = fmt.Fprintf(w, "stored credential %s for user %d\n", strings.TrimSpace(key), user)
	return err
}
