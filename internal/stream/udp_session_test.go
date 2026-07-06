// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// udpMetrics captures the UDP session hooks for assertions.
type udpMetrics struct {
	conns       atomic.Int64
	evictedIdle atomic.Int64
	evictedLRU  atomic.Int64
	rejected    atomic.Int64
}

func (m *udpMetrics) hooks() Hooks {
	return Hooks{
		OnConnDelta: func(_ string, d int64) { m.conns.Add(d) },
		OnUDPSessionEvicted: func(reason string) {
			switch reason {
			case "idle":
				m.evictedIdle.Add(1)
			case "lru":
				m.evictedLRU.Add(1)
			}
		},
		OnUDPSessionRejected: func() { m.rejected.Add(1) },
	}
}

// TestAdmitUDPLocked exercises the cap/eviction decision in isolation: below the
// cap admit unconditionally; at the cap reclaim the least-recently-seen session
// only if it is already idle past idle_timeout; otherwise reject. Pending dials
// count toward the cap so concurrent creators cannot overshoot it.
func TestAdmitUDPLocked(t *testing.T) {
	const idle = time.Minute
	now := time.Now().UnixNano()

	newSession := func(lastSeen int64) *udpSession {
		s := &udpSession{}
		s.lastSeen.Store(lastSeen)
		return s
	}

	t.Run("below cap admits without a victim", func(t *testing.T) {
		l := &listener{udpSessions: map[string]*udpSession{}, udpPending: map[string]*udpPending{}}
		victim, _, ok := l.admitUDPLocked(2, idle, now)
		if !ok || victim != nil {
			t.Fatalf("got ok=%v victim=%v, want ok=true victim=nil", ok, victim)
		}
	})

	t.Run("zero cap is unbounded", func(t *testing.T) {
		l := &listener{udpSessions: map[string]*udpSession{"a": newSession(0)}, udpPending: map[string]*udpPending{}}
		if _, _, ok := l.admitUDPLocked(0, idle, now); !ok {
			t.Fatal("zero cap must admit (unbounded)")
		}
	})

	t.Run("at cap reclaims an idle LRU victim", func(t *testing.T) {
		old := newSession(now - 2*idle.Nanoseconds()) // idle past timeout
		fresh := newSession(now)
		l := &listener{
			udpSessions: map[string]*udpSession{"old": old, "fresh": fresh},
			udpPending:  map[string]*udpPending{},
		}
		victim, key, ok := l.admitUDPLocked(2, idle, now)
		if !ok {
			t.Fatal("want admission by reclaiming the idle session")
		}
		if victim != old || key != "old" {
			t.Fatalf("evicted the wrong session: key=%q", key)
		}
		if _, still := l.udpSessions["old"]; still {
			t.Fatal("victim must be detached from the session map")
		}
		if _, kept := l.udpSessions["fresh"]; !kept {
			t.Fatal("the active session must be retained")
		}
	})

	t.Run("at cap with only active sessions rejects", func(t *testing.T) {
		l := &listener{
			udpSessions: map[string]*udpSession{"a": newSession(now), "b": newSession(now)},
			udpPending:  map[string]*udpPending{},
		}
		victim, _, ok := l.admitUDPLocked(2, idle, now)
		if ok || victim != nil {
			t.Fatalf("want rejection, got ok=%v victim=%v", ok, victim)
		}
		if len(l.udpSessions) != 2 {
			t.Fatal("a rejected admission must not mutate the session map")
		}
	})

	t.Run("pending dials count toward the cap", func(t *testing.T) {
		l := &listener{
			udpSessions: map[string]*udpSession{"a": newSession(now)},
			udpPending:  map[string]*udpPending{"b": {done: make(chan struct{})}},
		}
		// One live + one pending == cap of 2; the only live session is fresh, so
		// nothing is reclaimable and admission is rejected.
		if _, _, ok := l.admitUDPLocked(2, idle, now); ok {
			t.Fatal("pending dial should be counted, forcing rejection")
		}
	})
}

// TestUDPRejectsNewClientAtCap proves a UDP listener with max_udp_sessions=1 and
// an active session rejects a second client (counted), rather than growing
// unboundedly or displacing the live session.
func TestUDPRejectsNewClientAtCap(t *testing.T) {
	backend, stop := udpEcho(t)
	defer stop()
	addr := freeUDPAddr(t)

	var m udpMetrics
	s := newTestServer(t, m.hooks())
	if err := s.Reload([]config.StreamServer{{
		Listen:         addr,
		Protocol:       "udp",
		ProxyPass:      backend,
		MaxUDPSessions: 1,
		IdleTimeout:    config.Duration(time.Hour), // never reclaimable during the test
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// Client A establishes the single allowed session (proven by the echo).
	a, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer a.Close()
	if _, err := a.Write([]byte("hello")); err != nil {
		t.Fatalf("A write: %v", err)
	}
	_ = a.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	if _, err := a.Read(buf); err != nil {
		t.Fatalf("A read echo: %v", err)
	}
	if !eventually(func() bool { return m.conns.Load() == 1 }) {
		t.Fatalf("session gauge: got %d want 1", m.conns.Load())
	}

	// Client B (a distinct source port) is over the cap and must be rejected.
	b, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer b.Close()
	if _, err := b.Write([]byte("blocked")); err != nil {
		t.Fatalf("B write: %v", err)
	}
	if !eventually(func() bool { return m.rejected.Load() == 1 }) {
		t.Fatalf("rejected counter: got %d want 1", m.rejected.Load())
	}
	// B gets no echo and the live session count stays at the cap.
	_ = b.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if n, err := b.Read(buf); err == nil {
		t.Fatalf("rejected client unexpectedly got a reply: %q", buf[:n])
	}
	if m.conns.Load() != 1 {
		t.Fatalf("session gauge after reject: got %d want 1", m.conns.Load())
	}
	if m.evictedLRU.Load() != 0 {
		t.Fatalf("an active session must not be evicted: lru=%d", m.evictedLRU.Load())
	}
}

// TestUDPSessionForSingleflight proves concurrent datagrams for the same new
// client dial exactly one backend and share one session, even though the dial
// runs without holding udpMu.
func TestUDPSessionForSingleflight(t *testing.T) {
	backend, stop := udpEcho(t)
	defer stop()
	addr := freeUDPAddr(t)

	var m udpMetrics
	s := newTestServer(t, m.hooks())
	if err := s.Reload([]config.StreamServer{{
		Listen: addr, Protocol: "udp", ProxyPass: backend,
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}

	s.mu.Lock()
	l := s.listeners["udp|"+addr]
	s.mu.Unlock()
	if l == nil {
		t.Fatal("listener not registered")
	}
	clientAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:54321")
	if err != nil {
		t.Fatalf("resolve client addr: %v", err)
	}

	const n = 16
	var wg sync.WaitGroup
	got := make([]*udpSession, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			got[i] = l.udpSessionFor(clientAddr)
		}(i)
	}
	wg.Wait()

	first := got[0]
	if first == nil {
		t.Fatal("session creation failed")
	}
	for i, sess := range got {
		if sess != first {
			t.Fatalf("goroutine %d got a different session: singleflight broken", i)
		}
	}
	if m.conns.Load() != 1 {
		t.Fatalf("dialed %d backends, want 1 (singleflight)", m.conns.Load())
	}
}

// TestUDPIdleEvictionMetric proves an idle session is reaped and counted under
// reason "idle" once it goes quiet past idle_timeout.
func TestUDPIdleEvictionMetric(t *testing.T) {
	backend, stop := udpEcho(t)
	defer stop()
	addr := freeUDPAddr(t)

	var m udpMetrics
	s := newTestServer(t, m.hooks())
	if err := s.Reload([]config.StreamServer{{
		Listen:      addr,
		Protocol:    "udp",
		ProxyPass:   backend,
		IdleTimeout: config.Duration(150 * time.Millisecond),
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}

	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	if _, err := c.Read(buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}

	// Stop sending; the session must be reaped as idle and counted.
	if !eventually(func() bool { return m.evictedIdle.Load() == 1 }) {
		t.Fatalf("idle eviction counter: got %d want 1", m.evictedIdle.Load())
	}
	if !eventually(func() bool { return m.conns.Load() == 0 }) {
		t.Fatalf("session gauge after idle reap: got %d want 0", m.conns.Load())
	}
}
