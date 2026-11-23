//go:build integration

package mcp

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildTestServer compiles testdata/testserver into a temporary directory and returns its path.
func buildTestServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "testserver")
	out, err := exec.Command("go", "build", "-o", bin, "./testdata/testserver").CombinedOutput()
	if err != nil {
		t.Fatalf("build testserver: %v\n%s", err, out)
	}
	return bin
}

func listToolNames(t *testing.T, s *Session) []string {
	t.Helper()
	tools, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func TestConnectStdio(t *testing.T) {
	bin := buildTestServer(t)

	s, err := Connect(context.Background(), &sdk.CommandTransport{Command: exec.Command(bin)})
	if err != nil {
		t.Fatalf("Connect over stdio: %v", err)
	}
	defer s.Close()

	want := []string{"get_forecast", "get_tasks"}
	if got := listToolNames(t, s); !slices.Equal(got, want) {
		t.Errorf("ListTools = %v, want %v", got, want)
	}
}

func TestConnectStreamableHTTP(t *testing.T) {
	bin := buildTestServer(t)
	addr := freeAddr(t)

	cmd := exec.Command(bin, "-http", addr)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start testserver: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	endpoint := "http://" + addr + "/mcp"
	s := connectWhenReady(t, &sdk.StreamableClientTransport{Endpoint: endpoint, DisableStandaloneSSE: true})
	defer s.Close()

	want := []string{"get_forecast", "get_tasks"}
	if got := listToolNames(t, s); !slices.Equal(got, want) {
		t.Errorf("ListTools = %v, want %v", got, want)
	}
}

// freeAddr returns a loopback address with a port that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.Addr().String()
}

// connectWhenReady retries Connect until the HTTP server has started listening.
func connectWhenReady(t *testing.T, transport sdk.Transport) *Session {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := Connect(context.Background(), transport)
		if err == nil {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("Connect over streamable HTTP: %v", fmt.Errorf("gave up: %w", err))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
