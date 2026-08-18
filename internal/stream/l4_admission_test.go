// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

// boundedStream builds a TCP stream route whose upstream bounds concurrent
// connections, returning the server and the listen address.
func boundedStream(t *testing.T, backend string, limit int, hooks Hooks) (*Server, string) {
	t.Helper()
	s := newTestServer(t, hooks)
	addr := freeTCPAddr(t)
	ups := map[string]config.UpstreamConfig{
		"l4": {
			Name:       "l4",
			Strategy:   "round_robin",
			Servers:    []config.UpstreamServer{{Address: backend, Weight: 1}},
			MaxFails:   1,
			Resilience: &config.ResilienceConfig{MaxActiveRequests: limit},
		},
	}
	if err := s.Reload([]config.StreamServer{{
		Listen: addr, Protocol: "tcp", ProxyPass: "l4",
	}}, ups); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return s, addr
}

// TestTCPAdmissionBoundsConcurrentConnections pins the L4 meaning of
// max_active_requests: at layer 4 the connection is the unit of work, so the
// limit counts concurrent connections. A connection over the limit is closed
// rather than refused with a status, because a raw socket has no status to
// send.
func TestTCPAdmissionBoundsConcurrentConnections(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()

	var rejected []string
	var mu sync.Mutex
	hooks := Hooks{OnDialFailure: func(_, reason string) {
		mu.Lock()
		rejected = append(rejected, reason)
		mu.Unlock()
	}}
	s, addr := boundedStream(t, backend, 2, hooks)

	// Two connections occupy the limit and are kept open.
	held := make([]net.Conn, 0, 2)
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		defer c.Close()
		// Force the relay to start so the connection is genuinely admitted.
		if _, err := c.Write([]byte("x")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		buf := make([]byte, 1)
		if _, err := c.Read(buf); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		held = append(held, c)
	}

	if !eventually(func() bool { return activeOf(s) == 2 }) {
		t.Fatalf("active = %d, want 2", activeOf(s))
	}

	// The third connection is admitted-rejected: the accept succeeds (the
	// listener is still listening) but the connection is closed without data.
	over, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial over the limit: %v", err)
	}
	defer over.Close()
	_ = over.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := over.Write([]byte("y")); err == nil {
		if _, rerr := over.Read(buf); rerr == nil {
			t.Fatal("a connection over max_active_requests was served")
		}
	}

	mu.Lock()
	sawOverload := false
	for _, r := range rejected {
		if r == "overloaded" {
			sawOverload = true
		}
	}
	mu.Unlock()
	if !sawOverload {
		t.Fatalf("the rejection was not counted as overload: %v", rejected)
	}

	// Closing a held connection frees its slot.
	held[0].Close()
	if !eventually(func() bool { return activeOf(s) == 1 }) {
		t.Fatalf("active after close = %d, want 1", activeOf(s))
	}
}

// livePool returns the pool the data path is actually using, read from the
// listener's published route rather than from the registry, so a test asserts
// against the same object a connection would.
func livePool(s *Server) *upstream.Pool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.listeners {
		if r := l.route.Load(); r != nil && r.defaultPool != nil {
			return r.defaultPool
		}
	}
	return nil
}

// activeOf reports the admitted connection count of the single stream pool.
func activeOf(s *Server) int64 {
	p := livePool(s)
	if p == nil {
		return -1
	}
	return p.Admission().Active()
}

// TestTCPAdmissionReleasesOnBackendFailure pins that a connection that never
// reaches a backend still returns its slot. A leak here would shrink capacity
// precisely while the backend is down.
func TestTCPAdmissionReleasesOnBackendFailure(t *testing.T) {
	// Port 1 on loopback refuses immediately.
	s, addr := boundedStream(t, "127.0.0.1:1", 2, Hooks{})

	for i := 0; i < 6; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 1)
		_, _ = c.Read(buf)
		c.Close()
	}
	if !eventually(func() bool { return activeOf(s) == 0 }) {
		t.Fatalf("active after 6 failed connections = %d, want 0", activeOf(s))
	}
}

// TestTCPAdmissionReleasesOnClientReset pins the abrupt-teardown path: a client
// that vanishes mid-relay must not hold its slot.
func TestTCPAdmissionReleasesOnClientReset(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	s, addr := boundedStream(t, backend, 4, Hooks{})

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !eventually(func() bool { return activeOf(s) == 1 }) {
		t.Fatalf("active = %d, want 1", activeOf(s))
	}

	// Abort rather than close cleanly.
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	c.Close()

	if !eventually(func() bool { return activeOf(s) == 0 }) {
		t.Fatalf("active after client reset = %d, want 0", activeOf(s))
	}
}

// TestStreamPoolReusedAcrossReload is the gap this slice closes. Stream pools
// were rebuilt on every reload, so an L4 backend lost its in-flight accounting
// — and, after the breaker lands, its failure history — each time the
// configuration was touched for any reason at all.
func TestStreamPoolReusedAcrossReload(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	s, addr := boundedStream(t, backend, 8, Hooks{})

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !eventually(func() bool { return activeOf(s) == 1 }) {
		t.Fatalf("active = %d, want 1", activeOf(s))
	}
	before := livePool(s)

	// Reload with an unrelated change: the idle timeout, not the upstream.
	ups := map[string]config.UpstreamConfig{
		"l4": {
			Name:       "l4",
			Strategy:   "round_robin",
			Servers:    []config.UpstreamServer{{Address: backend, Weight: 1}},
			MaxFails:   1,
			Resilience: &config.ResilienceConfig{MaxActiveRequests: 8},
		},
	}
	if err := s.Reload([]config.StreamServer{{
		Listen: addr, Protocol: "tcp", ProxyPass: "l4",
		IdleTimeout: config.Duration(90 * time.Second),
	}}, ups); err != nil {
		t.Fatalf("reload: %v", err)
	}

	after := livePool(s)
	if before == nil || after == nil {
		t.Fatal("pool missing from the route")
	}
	if after != before {
		t.Fatal("the stream pool was rebuilt across reload instead of reused")
	}
	if activeOf(s) != 1 {
		t.Fatalf("active after reload = %d, want 1: the pool was rebuilt and its accounting discarded", activeOf(s))
	}
}

// TestStreamHealthCheckIsForcedToTCP is the regression the forced probe type
// exists to prevent.
//
// The active checker defaults to http and probeHTTP issues a GET to a path. A
// stream route fronting Postgres, Redis, MQTT or SMTP that shares an
// [[upstreams]] block with health_check.enabled would fail every probe, flip
// every backend to unhealthy — which available() honours — and take the whole
// route down. The backend here answers nothing resembling HTTP; the route must
// stay usable regardless.
func TestStreamHealthCheckIsForcedToTCP(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()

	s := newTestServer(t, Hooks{})
	addr := freeTCPAddr(t)
	ups := map[string]config.UpstreamConfig{
		"l4": {
			Name:     "l4",
			Strategy: "round_robin",
			Servers:  []config.UpstreamServer{{Address: backend, Weight: 1}},
			MaxFails: 1,
			HealthCheck: &config.HealthCheckConfig{
				Enabled:            true,
				Type:               "http", // what the HTTP side of this upstream wants
				Path:               "/healthz",
				Interval:           config.Duration(20 * time.Millisecond),
				Timeout:            config.Duration(20 * time.Millisecond),
				HealthyThreshold:   1,
				UnhealthyThreshold: 1,
			},
		},
	}
	if err := s.Reload([]config.StreamServer{{
		Listen: addr, Protocol: "tcp", ProxyPass: "l4",
	}}, ups); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Give the checker several intervals to do its worst.
	time.Sleep(200 * time.Millisecond)

	p := livePool(s)
	if p == nil {
		t.Fatal("no pool")
	}
	for _, b := range p.Backends() {
		if !b.Available() {
			t.Fatal("active health took a non-HTTP stream backend out of rotation; the probe type was not forced to tcp")
		}
	}

	// And the route still serves.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("z")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 1)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(buf); err != nil {
		t.Fatalf("read through a health-checked stream route: %v", err)
	}
}

// TestUDPRouteGetsNoHealthChecker pins that a UDP-only route is not probed at
// all: probeTCP dials TCP, which a UDP-only backend does not answer, so a
// checker would report it permanently down.
func TestUDPRouteGetsNoHealthChecker(t *testing.T) {
	hc := &config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/x",
		Interval: config.Duration(time.Second), Timeout: config.Duration(time.Second),
	}
	if got := streamHealthCheck(hc, "udp"); got != nil {
		t.Fatal("a UDP route was given an active health checker")
	}
	got := streamHealthCheck(hc, "tcp")
	if got == nil {
		t.Fatal("a TCP route lost its health checker")
	}
	if got.Type != "tcp" {
		t.Fatalf("probe type = %q, want tcp", got.Type)
	}
	if got.Path != "" {
		t.Fatalf("probe path = %q, want it dropped with the http type", got.Path)
	}
	// The caller's configuration must not be mutated: the same block still
	// governs the HTTP registry's own pool.
	if hc.Type != "http" {
		t.Fatalf("the shared upstream's health_check.type was mutated to %q", hc.Type)
	}
}

// TestPreflightBuildDoesNotDisturbLivePools pins that validating a candidate
// configuration cannot retire or rebuild a running pool: preflight stages into
// a transaction it always aborts.
func TestPreflightBuildDoesNotDisturbLivePools(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	s, addr := boundedStream(t, backend, 8, Hooks{})

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !eventually(func() bool { return activeOf(s) == 1 }) {
		t.Fatalf("active = %d, want 1", activeOf(s))
	}

	err = s.PreflightBuild(context.Background(), []config.StreamServer{{
		Listen: freeTCPAddr(t), Protocol: "tcp", ProxyPass: backend,
	}}, nil)
	if err != nil {
		t.Fatalf("PreflightBuild: %v", err)
	}

	if activeOf(s) != 1 {
		t.Fatalf("active after preflight = %d, want 1: preflight disturbed a live pool", activeOf(s))
	}
}
