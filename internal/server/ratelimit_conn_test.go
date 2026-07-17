// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// TestServerConnectionCap verifies that RateLimit.MaxConns caps concurrent
// connections per listener: with a cap of 1, a second connection is accepted by
// the kernel but not served until the first connection frees its slot.
func TestServerConnectionCap(t *testing.T) {
	addr := freePort(t)

	var entered int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }

	factory := func(c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), redact.State, error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&entered, 1)
			<-release
			_, _ = io.WriteString(w, "ok")
		})
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = h
		}
		return m, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, redact.EmptyState(), nil
	}

	cfg := &config.Config{
		Global:    config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		RateLimit: config.RateLimitConfig{Enabled: true, Key: "ip", Rate: 1000, Burst: 1000, MaxConns: 1},
		Servers: []config.ServerConfig{{
			Listen:    addr,
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}}},
		}},
	}

	srv := New(cfg, nil, lifecycle.Fingerprint{}, quietLogger(), factory, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, nil) }()
	t.Cleanup(func() {
		doRelease()
		cancel()
		<-done
	})

	// Give the server a moment to start accepting. Without this the test can
	// race ahead and the first connection reaches the handler too late.
	time.Sleep(50 * time.Millisecond)

	select {
	case err := <-done:
		t.Fatalf("Run exited early: %v", err)
	default:
	}

	// Wait for the listener to accept by retrying the first real connection.
	// We avoid a separate probe dial because MaxConns=1: a probe would consume
	// the single slot and race with this connection.
	var conn1 net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for {
		var err error
		conn1, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener never came up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer conn1.Close()

	// Connection 1 takes the single slot and blocks inside the handler.
	if _, err := io.WriteString(conn1, "GET / HTTP/1.1\r\nHost: t\r\n\r\n"); err != nil {
		t.Fatalf("write conn1: %v", err)
	}
	if !waitForCount(&entered, 1) {
		t.Fatal("handler for conn1 never started")
	}

	// Connection 2 connects (kernel backlog) but must not be served while the
	// only slot is held, so a read blocks until our deadline.
	conn2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial conn2: %v", err)
	}
	defer conn2.Close()
	if _, err := io.WriteString(conn2, "GET / HTTP/1.1\r\nHost: t\r\n\r\n"); err != nil {
		t.Fatalf("write conn2: %v", err)
	}
	_ = conn2.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := conn2.Read(make([]byte, 16)); err == nil {
		t.Fatal("conn2 was served while the connection cap (1) should have blocked it")
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("conn2 read error = %v, want timeout", err)
	}

	// Free the slot: release conn1's handler and close conn1 so the limiter
	// returns its token.
	doRelease()
	_ = conn1.Close()

	// conn2 should now be accepted and served.
	_ = conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp := make([]byte, 256)
	n, err := conn2.Read(resp)
	if err != nil {
		t.Fatalf("conn2 read after release: %v", err)
	}
	if !strings.Contains(string(resp[:n]), "200") {
		t.Fatalf("conn2 response did not indicate success: %q", string(resp[:n]))
	}
}

// waitForCount polls an atomic counter until it reaches want or times out.
func waitForCount(p *int32, want int32) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(p) >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
