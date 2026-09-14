// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"errors"
	"net"
	"testing"
	"time"

	"jul/internal/config"
)

func dialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return c
}

func acceptAsync(l net.Listener) <-chan struct {
	conn net.Conn
	err  error
} {
	ch := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		c, err := l.Accept()
		ch <- struct {
			conn net.Conn
			err  error
		}{c, err}
	}()
	return ch
}

func mustAccept(t *testing.T, ch <-chan struct {
	conn net.Conn
	err  error
}) net.Conn {
	t.Helper()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("accept: %v", got.err)
		}
		return got.conn
	case <-time.After(2 * time.Second):
		t.Fatal("accept timed out")
		return nil
	}
}

func assertBlocked(t *testing.T, ch <-chan struct {
	conn net.Conn
	err  error
}) {
	t.Helper()
	select {
	case got := <-ch:
		if got.conn != nil {
			_ = got.conn.Close()
		}
		t.Fatalf("accept completed while admission should be blocked: err=%v", got.err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDynamicConnLimiterLiveTransitions(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newDynamicConnLimiter(raw, 2)
	defer l.Close()

	client1 := dialTCP(t, raw.Addr().String())
	server1 := mustAccept(t, acceptAsync(l))
	defer client1.Close()
	defer server1.Close()
	client2 := dialTCP(t, raw.Addr().String())
	server2 := mustAccept(t, acceptAsync(l))
	defer client2.Close()
	defer server2.Close()

	client3 := dialTCP(t, raw.Addr().String())
	third := acceptAsync(l)
	assertBlocked(t, third)

	l.SetLimit(3)
	server3 := mustAccept(t, third)
	defer client3.Close()
	defer server3.Close()
	if got := l.Active(); got != 3 {
		t.Fatalf("active=%d want 3", got)
	}

	// Lowering below the current active count never terminates admitted conns.
	l.SetLimit(1)
	client4 := dialTCP(t, raw.Addr().String())
	fourth := acceptAsync(l)
	assertBlocked(t, fourth)
	_ = server1.Close()
	_ = server2.Close()
	assertBlocked(t, fourth) // active == 1 is still at the cap
	_ = server3.Close()
	server4 := mustAccept(t, fourth)
	defer client4.Close()
	defer server4.Close()

	// Unlimited becomes effective immediately.
	l.SetLimit(0)
	client5 := dialTCP(t, raw.Addr().String())
	server5 := mustAccept(t, acceptAsync(l))
	defer client5.Close()
	defer server5.Close()
}

func TestDynamicConnLimiterCloseReleasesExactlyOnceAndUnblocksWaiter(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newDynamicConnLimiter(raw, 1)

	client1 := dialTCP(t, raw.Addr().String())
	server1 := mustAccept(t, acceptAsync(l))
	defer client1.Close()
	if err := server1.Close(); err != nil {
		t.Fatal(err)
	}
	_ = server1.Close()
	if got := l.Active(); got != 0 {
		t.Fatalf("active after double close=%d want 0", got)
	}

	client2 := dialTCP(t, raw.Addr().String())
	server2 := mustAccept(t, acceptAsync(l))
	defer client2.Close()
	client3 := dialTCP(t, raw.Addr().String())
	defer client3.Close()
	blocked := acceptAsync(l)
	assertBlocked(t, blocked)
	if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	select {
	case got := <-blocked:
		if !errors.Is(got.err, net.ErrClosed) {
			t.Fatalf("blocked accept after close err=%v want net.ErrClosed", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener close did not wake blocked admission")
	}
	_ = server2.Close()
}

func TestEffectiveConnectionCapMasterSwitch(t *testing.T) {
	cfg := &config.Config{}
	cfg.RateLimit.MaxConns = 7
	if got := effectiveConnectionCap(cfg); got != 0 {
		t.Fatalf("disabled cap=%d want unlimited", got)
	}
	cfg.RateLimit.Enabled = true
	if got := effectiveConnectionCap(cfg); got != 7 {
		t.Fatalf("enabled cap=%d want 7", got)
	}
	cfg.RateLimit.Enabled = false
	if got := effectiveConnectionCap(cfg); got != 0 {
		t.Fatalf("disabled-again cap=%d want unlimited", got)
	}
}

func TestMaxConnsNoLongerChangesListenerFingerprint(t *testing.T) {
	base := &config.Config{RateLimit: config.RateLimitConfig{Enabled: true, MaxConns: 2}}
	base.Servers = []config.ServerConfig{{Listen: "127.0.0.1:8080"}}
	next := *base
	next.RateLimit = base.RateLimit
	next.RateLimit.MaxConns = 20
	if before, after := listenerBindFingerprint(base, base.Servers[0].Listen), listenerBindFingerprint(&next, next.Servers[0].Listen); before != after {
		t.Fatalf("max_conns still changes bind fingerprint: before=%q after=%q", before, after)
	}
}
