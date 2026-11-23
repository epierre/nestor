package mcp

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/epierre/nestor/internal/mcp/config"
)

// Defaults for Supervisor fields left at zero.
const (
	defaultHealthInterval = 30 * time.Second
	defaultMinBackoff     = time.Second
	defaultMaxBackoff     = 5 * time.Minute
)

// Supervisor keeps one MCP server's tools in the registry only while the server answers. It
// connects, registers the approved tools (the pin check runs on every connect), pings at Interval,
// and on failure removes exactly the tools it registered, then reconnects with exponential backoff.
// Other servers and built-in tools are never touched.
type Supervisor struct {
	Registrar *Registrar
	Server    config.Server
	// Dial opens the session used for the tool list and the health check.
	Dial func(ctx context.Context) (*Session, error)
	// Users, when set, serves calls for a per_user server with header delivery. Its cached
	// sessions are dropped whenever the server is found down.
	Users *UserConnector

	Interval   time.Duration
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// Run supervises the server until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) {
	failures := 0
	for {
		if s.serve(ctx) {
			failures = 0
		}
		if ctx.Err() != nil {
			return
		}
		failures++
		if !sleepCtx(ctx, s.backoff(failures)) {
			return
		}
	}
}

// serve connects once and keeps the tools registered until the server stops answering or ctx is
// cancelled. It reports whether the server was reached.
func (s *Supervisor) serve(ctx context.Context) bool {
	log := slog.With("server", s.Server.Name)
	session, err := s.Dial(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.WarnContext(ctx, "mcp server unavailable", "error", err)
		}
		return false
	}
	defer s.drop(session)

	var conn connector = fixedSession{session}
	if s.Users != nil {
		conn = s.Users
	}
	names, err := s.Registrar.register(ctx, s.Server, session, conn, nil)
	// A closure, so that the tools registered by later refreshes are removed too.
	defer func() { s.remove(names) }()
	if err != nil {
		log.ErrorContext(ctx, "mcp server tools not registered", "error", err)
		return false
	}
	log.InfoContext(ctx, "mcp server connected", "tools", len(names))

	timeout := orDefault(s.Server.Timeout, defaultTimeout)
	ticker := time.NewTicker(orDefault(s.Interval, defaultHealthInterval))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return true
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, timeout)
			err := session.Ping(pingCtx)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					log.WarnContext(ctx, "mcp server stopped answering; its tools are removed", "error", err)
				}
				return true
			}
			// Re-check the pins, so an approval takes effect and a changed tool is disabled
			// without waiting for a reconnect.
			current := names
			names, err = s.Registrar.register(ctx, s.Server, session, conn, current)
			s.remove(without(current, names))
			if added := without(names, current); len(added) > 0 {
				log.InfoContext(ctx, "mcp tools enabled", "tools", added)
			}
			if err != nil {
				log.ErrorContext(ctx, "mcp server tools not refreshed", "error", err)
				return true
			}
		}
	}
}

// without returns the names in all that are not in keep.
func without(all, keep []string) []string {
	var out []string
	for _, name := range all {
		if !slices.Contains(keep, name) {
			out = append(out, name)
		}
	}
	return out
}

func (s *Supervisor) remove(names []string) {
	for _, name := range names {
		s.Registrar.Registry.Unregister(name)
	}
}

func (s *Supervisor) drop(session *Session) {
	_ = session.Close()
	if s.Users != nil {
		s.Users.Close()
	}
}

// backoff doubles from MinBackoff with each consecutive failure, up to MaxBackoff.
func (s *Supervisor) backoff(failures int) time.Duration {
	d := orDefault(s.MinBackoff, defaultMinBackoff)
	limit := orDefault(s.MaxBackoff, defaultMaxBackoff)
	for i := 1; i < failures && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
