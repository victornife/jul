// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"context"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// This file is the characterization matrix that #89 requires before
// stream.*.protocol may be reclassified from restart_required to hot_reload.
//
// The claim under test is that Reload represents a protocol switch on one
// numeric address as a transactional remove/add: listeners are keyed by
// "proto|addr", the candidate protocol's socket is bound before any live state
// is mutated, and only then is the previous listener retired. TCP and UDP
// occupy independent port spaces, so both sockets can exist during the swap.
//
// Every test here drives real sockets. None of them assert a desired outcome by
// construction: the failure paths assert that the running configuration is left
// untouched.

// streamBlock builds one [[stream]] block.
func streamBlock(addr, proto, target string) config.StreamServer {
	return config.StreamServer{
		Listen:         addr,
		Protocol:       proto,
		ProxyPass:      target,
		ConnectTimeout: config.Duration(2 * time.Second),
		IdleTimeout:    config.Duration(2 * time.Second),
	}
}

// dialTCPAndEcho proves a TCP listener on addr relays to its backend.
func dialTCPAndEcho(t *testing.T, addr, payload string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial tcp %s: %v", addr, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte(payload)); err != nil {
		t.Fatalf("write tcp: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := readFull(c, buf); err != nil {
		t.Fatalf("read tcp echo: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("tcp echo = %q, want %q", buf, payload)
	}
}

// dialUDPAndEcho proves a UDP listener on addr relays to its backend. UDP is
// lossless on loopback but not ordered by contract, so the datagram is retried
// briefly before failing.
func dialUDPAndEcho(t *testing.T, addr, payload string) {
	t.Helper()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial udp %s: %v", addr, err)
	}
	defer c.Close()
	buf := make([]byte, 512)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.Write([]byte(payload)); err != nil {
			t.Fatalf("write udp: %v", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := c.Read(buf)
		if err == nil && string(buf[:n]) == payload {
			return
		}
	}
	t.Fatalf("no udp echo from %s within the deadline", addr)
}

func readFull(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func mustReload(t *testing.T, s *Server, blocks ...config.StreamServer) {
	t.Helper()
	if err := s.Reload(blocks, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

// TestStreamProtocolSwitchTCPToUDP is case 1 of the matrix: the same numeric
// address moves from TCP to UDP within one reload and serves UDP afterwards.
func TestStreamProtocolSwitchTCPToUDP(t *testing.T) {
	tcpBackend, stopTCP := tcpEcho(t)
	defer stopTCP()
	udpBackend, stopUDP := udpEcho(t)
	defer stopUDP()

	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})

	mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))
	dialTCPAndEcho(t, addr, "before")

	mustReload(t, s, streamBlock(addr, "udp", udpBackend))

	if keys := s.BoundKeys(); len(keys) != 1 || keys[0] != "udp|"+addr {
		t.Fatalf("bound keys = %v, want exactly [udp|%s]", keys, addr)
	}
	dialUDPAndEcho(t, addr, "after")

	// The TCP socket is released, so the port is free for a TCP bind again.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("tcp port was not released after the switch: %v", err)
	}
	_ = ln.Close()
}

// TestStreamProtocolSwitchUDPToTCP is case 2: the reverse transition.
func TestStreamProtocolSwitchUDPToTCP(t *testing.T) {
	tcpBackend, stopTCP := tcpEcho(t)
	defer stopTCP()
	udpBackend, stopUDP := udpEcho(t)
	defer stopUDP()

	addr := freeUDPAddr(t)
	s := newTestServer(t, Hooks{})

	mustReload(t, s, streamBlock(addr, "udp", udpBackend))
	dialUDPAndEcho(t, addr, "before")

	mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))

	if keys := s.BoundKeys(); len(keys) != 1 || keys[0] != "tcp|"+addr {
		t.Fatalf("bound keys = %v, want exactly [tcp|%s]", keys, addr)
	}
	dialTCPAndEcho(t, addr, "after")

	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("udp port was not released after the switch: %v", err)
	}
	_ = pc.Close()
}

// TestStreamProtocolSwitchRepeated is case 8: repeated transitions neither leak
// a socket nor wedge the listener set.
func TestStreamProtocolSwitchRepeated(t *testing.T) {
	tcpBackend, stopTCP := tcpEcho(t)
	defer stopTCP()
	udpBackend, stopUDP := udpEcho(t)
	defer stopUDP()

	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})

	for i := 0; i < 5; i++ {
		mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))
		dialTCPAndEcho(t, addr, "tcp")
		mustReload(t, s, streamBlock(addr, "udp", udpBackend))
		dialUDPAndEcho(t, addr, "udp")
	}
	if keys := s.BoundKeys(); len(keys) != 1 {
		t.Fatalf("bound keys after repeated switches = %v, want exactly one", keys)
	}
}

// TestStreamProtocolSwitchRouteBuildFailureIsAtomic is cases 3 and 7: a
// candidate whose routes cannot be built leaves the running listener serving
// the previous protocol. Both Phase-1 rejections are exercised: a duplicate
// listener key and an upstream the pool builder refuses.
func TestStreamProtocolSwitchRouteBuildFailureIsAtomic(t *testing.T) {
	tcpBackend, stopTCP := tcpEcho(t)
	defer stopTCP()

	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})
	mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))

	t.Run("duplicate candidate listener", func(t *testing.T) {
		err := s.Reload([]config.StreamServer{
			streamBlock(addr, "udp", tcpBackend),
			streamBlock(addr, "udp", tcpBackend),
		}, nil)
		if err == nil {
			t.Fatal("a candidate declaring the same listener twice must be rejected")
		}
	})

	t.Run("unbuildable upstream", func(t *testing.T) {
		err := s.Reload(
			[]config.StreamServer{streamBlock(addr, "udp", "empty-pool")},
			map[string]config.UpstreamConfig{"empty-pool": {Name: "empty-pool", Strategy: "round_robin"}},
		)
		if err == nil {
			t.Fatal("a candidate whose upstream pool cannot be built must be rejected")
		}
	})

	if keys := s.BoundKeys(); len(keys) != 1 || keys[0] != "tcp|"+addr {
		t.Fatalf("bound keys after a rejected candidate = %v, want the previous [tcp|%s]", keys, addr)
	}
	dialTCPAndEcho(t, addr, "still-tcp")
}

// TestStreamProtocolSwitchBindFailureIsAtomic is cases 4 and 7: when the
// candidate protocol's socket cannot be bound, nothing is mutated.
func TestStreamProtocolSwitchBindFailureIsAtomic(t *testing.T) {
	tcpBackend, stopTCP := tcpEcho(t)
	defer stopTCP()
	udpBackend, stopUDP := udpEcho(t)
	defer stopUDP()

	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})
	mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))

	// Hold the UDP side of the same numeric address so the candidate bind fails.
	held, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Skipf("cannot hold udp %s to force a bind failure: %v", addr, err)
	}
	defer held.Close()

	if err := s.Reload([]config.StreamServer{streamBlock(addr, "udp", udpBackend)}, nil); err == nil {
		t.Fatal("a candidate whose socket cannot bind must be rejected")
	}

	if keys := s.BoundKeys(); len(keys) != 1 || keys[0] != "tcp|"+addr {
		t.Fatalf("bound keys after a failed bind = %v, want the previous [tcp|%s]", keys, addr)
	}
	dialTCPAndEcho(t, addr, "still-tcp-after-bind-failure")
}

// TestStreamProtocolSwitchPreflightProbesCandidateProtocol proves the apply-time
// gate probes the candidate protocol's socket, so an unbindable switch is
// rejected before the configuration is persisted.
func TestStreamProtocolSwitchPreflightProbesCandidateProtocol(t *testing.T) {
	udpBackend, stopUDP := udpEcho(t)
	defer stopUDP()

	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})

	bound := map[string]struct{}{"tcp|" + addr: {}}
	next := []config.StreamServer{streamBlock(addr, "udp", udpBackend)}

	if err := s.PreflightListeners(context.Background(), bound, next); err != nil {
		t.Fatalf("a free udp port must pass the probe: %v", err)
	}

	held, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Skipf("cannot hold udp %s: %v", addr, err)
	}
	defer held.Close()

	err = s.PreflightListeners(context.Background(), bound, next)
	if err == nil {
		t.Fatal("a busy candidate port must fail the probe")
	}
	if !strings.Contains(err.Error(), "udp") {
		t.Fatalf("probe error does not name the candidate protocol: %v", err)
	}
}

// TestStreamProtocolSwitchDrainsEstablishedTCP is case 5: an established TCP
// relay keeps working while the switch is prepared, and the switch completes
// once the client closes. This is the retirement boundary documented for the
// hot_reload classification: existing connections follow the retired listener,
// new traffic uses the candidate protocol.
func TestStreamProtocolSwitchDrainsEstablishedTCP(t *testing.T) {
	tcpBackend, stopTCP := tcpEcho(t)
	defer stopTCP()
	udpBackend, stopUDP := udpEcho(t)
	defer stopUDP()

	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})
	mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))

	client, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write([]byte("live")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := readFull(client, buf); err != nil {
		t.Fatalf("read: %v", err)
	}

	// The reload retires the old listener, which waits for in-flight relays.
	// Run it concurrently and prove it is still pending while the connection is
	// open, then completes as soon as the client goes away.
	done := make(chan error, 1)
	go func() { done <- s.Reload([]config.StreamServer{streamBlock(addr, "udp", udpBackend)}, nil) }()

	select {
	case err := <-done:
		t.Fatalf("the switch completed while a relay was still established: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	_ = client.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("switch after drain: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the switch did not complete after the established connection closed")
	}

	dialUDPAndEcho(t, addr, "after-drain")
}

// TestStreamProtocolSwitchRetiresUDPSessions is case 6: switching away from UDP
// tears the tracked sessions down rather than leaving their backend sockets
// open.
func TestStreamProtocolSwitchRetiresUDPSessions(t *testing.T) {
	udpBackend, stopUDP := udpEcho(t)
	defer stopUDP()
	tcpBackend, stopTCP := tcpEcho(t)
	defer stopTCP()

	addr := freeUDPAddr(t)
	s := newTestServer(t, Hooks{})
	mustReload(t, s, streamBlock(addr, "udp", udpBackend))
	dialUDPAndEcho(t, addr, "session")

	backends := udpSessionBackends(s, addr)
	if len(backends) == 0 {
		t.Fatal("expected at least one tracked UDP session before the switch")
	}

	mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))

	if keys := s.BoundKeys(); len(keys) != 1 || keys[0] != "tcp|"+addr {
		t.Fatalf("bound keys = %v", keys)
	}
	for i, b := range backends {
		if _, err := b.Write([]byte("x")); err == nil {
			t.Errorf("UDP session backend %d survived the switch", i)
		}
	}
	dialTCPAndEcho(t, addr, "tcp-after-udp")
}

// udpSessionBackends snapshots the backend sockets of the UDP sessions tracked
// by the listener on addr.
func udpSessionBackends(s *Server, addr string) []net.Conn {
	s.mu.Lock()
	l, ok := s.listeners["udp|"+addr]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	l.udpMu.Lock()
	defer l.udpMu.Unlock()
	out := make([]net.Conn, 0, len(l.udpSessions))
	for _, sess := range l.udpSessions {
		out = append(out, sess.backend)
	}
	return out
}

// TestStreamProtocolSwitchLeavesNoGoroutines is case 10: a completed switch
// retires the old listener's goroutines rather than leaking them. It is a
// direct leak assertion; the race detector lane covers the concurrency side.
func TestStreamProtocolSwitchLeavesNoGoroutines(t *testing.T) {
	tcpBackend, stopTCP := tcpEcho(t)
	defer stopTCP()
	udpBackend, stopUDP := udpEcho(t)
	defer stopUDP()

	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})
	mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))
	dialTCPAndEcho(t, addr, "warm")

	settle()
	base := runtime.NumGoroutine()

	for i := 0; i < 10; i++ {
		mustReload(t, s, streamBlock(addr, "udp", udpBackend))
		mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	settle()
	if got := runtime.NumGoroutine(); got > base+4 {
		t.Fatalf("goroutines grew from %d to %d across repeated protocol switches", base, got)
	}
}

// TestStreamProtocolSwitchConcurrentTraffic is case 10's race companion: traffic
// runs against the address while protocol switches are applied, so the race
// detector observes the route swap and listener retirement under load.
func TestStreamProtocolSwitchConcurrentTraffic(t *testing.T) {
	tcpBackend, stopTCP := tcpEcho(t)
	defer stopTCP()
	udpBackend, stopUDP := udpEcho(t)
	defer stopUDP()

	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})
	mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Failures are expected while the address is mid-switch; the point
			// is that neither side races or panics.
			if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
				_, _ = c.Write([]byte("x"))
				_ = c.Close()
			}
		}
	}()

	for i := 0; i < 6; i++ {
		mustReload(t, s, streamBlock(addr, "udp", udpBackend))
		mustReload(t, s, streamBlock(addr, "tcp", tcpBackend))
	}
	close(stop)
	wg.Wait()

	dialTCPAndEcho(t, addr, "final")
}

// settle gives retired goroutines a chance to exit before a count is taken.
func settle() {
	for i := 0; i < 20; i++ {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
}
