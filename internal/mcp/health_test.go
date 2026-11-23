package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// switchableServer stands in for a server that goes down and comes back, possibly changed.
type switchableServer struct {
	mu          sync.Mutex
	up          bool
	description string
	current     *sdk.ServerSession
	dials       int
}

func (s *switchableServer) set(up bool, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.up, s.description = up, description
	if !up && s.current != nil {
		_ = s.current.Close() // drops the live connection, as a crash would
		s.current = nil
	}
}

func (s *switchableServer) dialCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dials
}

func (s *switchableServer) dial(ctx context.Context) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dials++
	if !s.up {
		return nil, errors.New("connection refused")
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "switchable", Version: "0.0.1"}, nil)
	object := map[string]any{"type": "object"}
	ok := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
	}
	srv.AddTool(&sdk.Tool{Name: "get_forecast", Description: s.description, InputSchema: object}, ok)
	srv.AddTool(&sdk.Tool{Name: "get_tasks", Description: "List tasks.", InputSchema: object}, ok)
	serverT, clientT := sdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		return nil, err
	}
	s.current = ss
	return Connect(ctx, clientT)
}

func supervise(t *testing.T, reg *tools.Registry, pins PinStore, srv *switchableServer) {
	t.Helper()
	sup := &Supervisor{
		Registrar:  &Registrar{Registry: reg, Pins: pins},
		Server:     forecastServer(),
		Dial:       srv.dial,
		Interval:   10 * time.Millisecond,
		MinBackoff: 5 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sup.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func registered(reg *tools.Registry, name string) bool {
	_, err := reg.Lookup(name)
	return err == nil
}

func TestDownServerToolsAbsent(t *testing.T) {
	t.Run("server goes down while connected", func(t *testing.T) {
		reg := tools.NewRegistry()
		if err := reg.Register(tools.NewCurrentTimeTool()); err != nil {
			t.Fatalf("Register built-in: %v", err)
		}
		srv := &switchableServer{up: true, description: v1}
		supervise(t, reg, approvingPins(), srv)
		waitFor(t, "tools registered", func() bool { return registered(reg, "weather__get_forecast") })

		srv.set(false, v1)
		waitFor(t, "tools removed", func() bool {
			return !registered(reg, "weather__get_forecast") && !registered(reg, "weather__get_tasks")
		})
		if !registered(reg, "current_time") {
			t.Error("built-in tool removed along with the server's tools")
		}
	})

	t.Run("server down from the start", func(t *testing.T) {
		reg := tools.NewRegistry()
		if err := reg.Register(tools.NewCurrentTimeTool()); err != nil {
			t.Fatalf("Register built-in: %v", err)
		}
		srv := &switchableServer{up: false}
		supervise(t, reg, approvingPins(), srv)
		waitFor(t, "retries with backoff", func() bool { return srv.dialCount() >= 3 })

		if registered(reg, "weather__get_forecast") {
			t.Error("tool of an unreachable server registered")
		}
		if !registered(reg, "current_time") {
			t.Error("built-in tool missing")
		}
	})
}

func TestRecoveryReAddsAfterPinCheck(t *testing.T) {
	pins := newPins()
	connectAs(t, pins, &fakeAlerts{}, v1)
	pins.approve(t, "weather", "get_forecast")
	pins.approve(t, "weather", "get_tasks")

	reg := tools.NewRegistry()
	srv := &switchableServer{up: false}
	supervise(t, reg, pins, srv)
	waitFor(t, "a failed dial", func() bool { return srv.dialCount() >= 2 })

	// It comes back changed: the unchanged tool returns, the changed one stays out.
	srv.set(true, v2)
	waitFor(t, "unchanged tool re-added", func() bool { return registered(reg, "weather__get_tasks") })
	if registered(reg, "weather__get_forecast") {
		t.Error("changed tool re-added without re-approval")
	}

	// Down again, then back with the approved description: both tools return.
	srv.set(false, v2)
	waitFor(t, "tools removed", func() bool { return !registered(reg, "weather__get_tasks") })
	srv.set(true, v1)
	waitFor(t, "approved tool re-added", func() bool { return registered(reg, "weather__get_forecast") })
}

func TestApprovalTakesEffectWithoutReconnect(t *testing.T) {
	pins := newPins()
	reg := tools.NewRegistry()
	srv := &switchableServer{up: true, description: v1}
	supervise(t, reg, pins, srv)
	waitFor(t, "first connect recorded", func() bool { _, ok := pins.seen("weather", "get_forecast"); return ok })
	if registered(reg, "weather__get_forecast") {
		t.Fatal("tool registered before approval")
	}

	pins.approve(t, "weather", "get_forecast")
	waitFor(t, "approved tool registered", func() bool { return registered(reg, "weather__get_forecast") })
	if n := srv.dialCount(); n != 1 {
		t.Errorf("server dialled %d times, want 1: approval must not need a reconnect", n)
	}
	if registered(reg, "weather__get_tasks") {
		t.Error("unapproved tool registered")
	}
}
