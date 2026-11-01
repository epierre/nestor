//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const composeFile = "../../../deploy/docker-compose.yml"

// serverURL points at a maintenance database. Each test gets its own database on that server.
var serverURL string

// TestMain uses the server named by TEST_DATABASE_URL, or else starts the Postgres image that
// deploy/docker-compose.yml runs. With neither available the suite fails rather than skips.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	serverURL = os.Getenv("TEST_DATABASE_URL")
	if serverURL == "" {
		url, stop, err := startContainer()
		if err != nil {
			fmt.Fprintf(os.Stderr, "start Postgres container: %v\n", err)
			return 1
		}
		defer stop()
		serverURL = url
	}
	return m.Run()
}

func startContainer() (string, func(), error) {
	image, err := composeImage()
	if err != nil {
		return "", nil, err
	}
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"-e", "POSTGRES_PASSWORD=test-password",
		"-p", "127.0.0.1::5432",
		image).Output()
	if err != nil {
		return "", nil, fmt.Errorf("docker run: %w", err)
	}
	id := strings.TrimSpace(string(out))
	stop := func() { _ = exec.Command("docker", "rm", "-f", id).Run() }

	out, err = exec.Command("docker", "port", id, "5432/tcp").Output()
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("docker port: %w", err)
	}
	address, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	url := "postgres://postgres:test-password@" + address + "/postgres?sslmode=disable"
	if err := waitForServer(url); err != nil {
		stop()
		return "", nil, err
	}
	return url, stop, nil
}

// composeImage returns the Postgres image of the Compose stack, so tests run what deploys.
func composeImage() (string, error) {
	data, err := os.ReadFile(composeFile)
	if err != nil {
		return "", err
	}
	match := regexp.MustCompile(`(?m)^\s*image:\s*(pgvector/\S+)`).FindSubmatch(data)
	if match == nil {
		return "", errors.New("no pgvector image in " + composeFile)
	}
	return string(match[1]), nil
}

func waitForServer(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for {
		conn, err := pgx.Connect(ctx, url)
		if err == nil {
			return conn.Close(ctx)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for Postgres: %w", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// newDatabase creates an empty database that is dropped when the test ends.
func newDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("nestor_test_%d", rand.Uint64())
	admin := func(statement string) {
		t.Helper()
		conn, err := pgx.Connect(ctx, serverURL)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	admin("CREATE DATABASE " + name)
	t.Cleanup(func() { admin("DROP DATABASE " + name + " WITH (FORCE)") })

	config, err := pgxpool.ParseConfig(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
