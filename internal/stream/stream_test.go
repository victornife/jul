// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// eventually polls cond for up to ~2s, returning true as soon as it holds. It
// smooths over the brief scheduling lag between a relay write and its byte
// accounting without making tests timing-fragile.
func eventually(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

// freeTCPAddr reserves and releases an ephemeral TCP port, returning its
// address for a listener to bind. There is a small reuse window, acceptable in
// tests.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp addr: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func freeUDPAddr(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp addr: %v", err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}

// tcpEcho starts a TCP echo backend.
func tcpEcho(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _, _ = io.Copy(c, c); _ = c.Close() }(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

// tcpAnnounce starts a backend that writes id on connect, then echoes.
func tcpAnnounce(t *testing.T, id string) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("announce listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = c.Write([]byte(id))
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

// tcpProxyHeaderReader starts a backend that parses an inbound PROXY header and
// writes back the parsed source address string.
func tcpProxyHeaderReader(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pp listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReaderSize(c, 1024)
				src, err := readProxyHeader(br)
				if err != nil {
					_, _ = c.Write([]byte("ERR:" + err.Error()))
					return
				}
				if src == nil {
					_, _ = c.Write([]byte("LOCAL"))
					return
				}
				_, _ = c.Write([]byte(src.String()))
			}(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

// udpEcho starts a UDP echo backend.
func udpEcho(t *testing.T) (addr string, stop func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("udp echo listen: %v", err)
	}
	uc := pc.(*net.UDPConn)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, a, err := uc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = uc.WriteToUDP(buf[:n], a)
		}
	}()
	return uc.LocalAddr().String(), func() { _ = uc.Close() }
}

// clientHelloBytes captures a real TLS ClientHello for the given server name.
func clientHelloBytes(t *testing.T, serverName string) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	done := make(chan struct{})
	go func() {
		_ = tls.Client(c1, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}).Handshake()
		close(done)
	}()
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := c2.Read(buf)
	_ = c1.Close()
	_ = c2.Close()
	<-done
	if err != nil || n == 0 {
		t.Fatalf("capture ClientHello: n=%d err=%v", n, err)
	}
	return buf[:n]
}

func newTestServer(t *testing.T, hooks Hooks) *Server {
	t.Helper()
	s := NewServer(Options{Logger: discardLogger(), Hooks: hooks})
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestPreflightBuildDoesNotBind proves PreflightBuild validates a stream config
// without binding any socket or registering a listener: the live set stays
// empty and the address remains free afterwards. This is what lets the admin
// apply-time gate reject a bad [[stream]] block up front without disturbing the
// running listeners.
func TestPreflightBuildDoesNotBind(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	addr := freeTCPAddr(t)

	s := newTestServer(t, Hooks{})
	if err := s.PreflightBuild([]config.StreamServer{{
		Listen: addr, Protocol: "tcp", ProxyPass: backend,
	}}, nil); err != nil {
		t.Fatalf("preflight valid config: %v", err)
	}
	s.mu.Lock()
	n := len(s.listeners)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("preflight registered %d listeners, want 0", n)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("address held after preflight (socket not released): %v", err)
	}
	_ = ln.Close()
}

// TestPreflightBuildRejectsDuplicate proves PreflightBuild applies the same
// duplicate-listener rule as Reload, so an ambiguous config is rejected at apply
// time rather than during the asynchronous reload.
func TestPreflightBuildRejectsDuplicate(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	addr := freeTCPAddr(t)

	s := newTestServer(t, Hooks{})
	err := s.PreflightBuild([]config.StreamServer{
		{Listen: addr, Protocol: "tcp", ProxyPass: backend},
		{Listen: addr, Protocol: "tcp", ProxyPass: backend},
	}, nil)
	if err == nil {
		t.Fatal("preflight accepted a duplicate listener, want error")
	}
}

// TestPreflightListenersDetectsBusyPort proves the apply-time bind-probe rejects
// a NEWLY added stream listener whose address is already in use, so the apply is
// refused before the config is written rather than failing in the asynchronous
// reload after "applied" was already reported.
func TestPreflightListenersDetectsBusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy: %v", err)
	}
	defer busy.Close()
	busyAddr := busy.Addr().String()

	s := newTestServer(t, Hooks{})
	err = s.PreflightListeners(nil, []config.StreamServer{
		{Listen: busyAddr, Protocol: "tcp", ProxyPass: "127.0.0.1:1"},
	})
	if err == nil {
		t.Fatal("preflight accepted an in-use stream listen address, want error")
	}
}

// TestPreflightListenersSkipsExisting proves an address already present in the
// running set is not re-probed: a listener Jul.IA itself holds must not make its
// own apply fail.
func TestPreflightListenersSkipsExisting(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	defer held.Close()
	addr := held.Addr().String()

	s := newTestServer(t, Hooks{})
	old := []config.StreamServer{{Listen: addr, Protocol: "tcp", ProxyPass: "127.0.0.1:1"}}
	next := []config.StreamServer{{Listen: addr, Protocol: "tcp", ProxyPass: "127.0.0.1:2"}}
	if err := s.PreflightListeners(old, next); err != nil {
		t.Fatalf("preflight re-probed an existing listener: %v", err)
	}
}

// TestPreflightListenersReleasesSocket proves a successful probe of a free
// address does not hold the socket, so the subsequent real bind can succeed.
func TestPreflightListenersReleasesSocket(t *testing.T) {
	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})
	if err := s.PreflightListeners(nil, []config.StreamServer{
		{Listen: addr, Protocol: "tcp", ProxyPass: "127.0.0.1:1"},
	}); err != nil {
		t.Fatalf("preflight free addr: %v", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("address held after preflight (socket not released): %v", err)
	}
	_ = ln.Close()
}

// TestPreflightListenersUDP proves the probe covers UDP listeners, not just TCP.
func TestPreflightListenersUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy udp: %v", err)
	}
	defer pc.Close()
	busyAddr := pc.LocalAddr().String()

	s := newTestServer(t, Hooks{})
	err = s.PreflightListeners(nil, []config.StreamServer{
		{Listen: busyAddr, Protocol: "udp", ProxyPass: "127.0.0.1:1"},
	})
	if err == nil {
		t.Fatal("preflight accepted an in-use UDP stream listen address, want error")
	}
}

func TestTCPProxyEcho(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	addr := freeTCPAddr(t)

	var ups, downs atomic.Int64
	var conns atomic.Int64
	s := newTestServer(t, Hooks{
		OnConnDelta: func(_ string, d int64) { conns.Add(d) },
		OnBytes: func(_, dir string, n int64) {
			if dir == "up" {
				ups.Add(n)
			} else {
				downs.Add(n)
			}
		},
	})
	if err := s.Reload([]config.StreamServer{{
		Listen: addr, Protocol: "tcp", ProxyPass: backend,
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo: got %q want ping", buf)
	}
	// The downstream byte count is recorded just after the relay writes to the
	// client, so it can lag the client's read by a scheduling tick; poll for it.
	if !eventually(func() bool { return ups.Load() >= 4 && downs.Load() >= 4 }) {
		t.Errorf("byte counters: up=%d down=%d, want >=4 each", ups.Load(), downs.Load())
	}
}

func TestTCPProxyToUpstream(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	addr := freeTCPAddr(t)

	s := newTestServer(t, Hooks{})
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: backend, Weight: 1}}, MaxFails: 1},
	}
	if err := s.Reload([]config.StreamServer{{Listen: addr, Protocol: "tcp", ProxyPass: "pool"}}, ups); err != nil {
		t.Fatalf("reload: %v", err)
	}

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("hi"))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "hi" {
		t.Fatalf("echo via upstream: got %q", buf)
	}
}

func TestTCPSNIRouting(t *testing.T) {
	b1, stop1 := tcpAnnounce(t, "ONE")
	defer stop1()
	b2, stop2 := tcpAnnounce(t, "TWO")
	defer stop2()
	addr := freeTCPAddr(t)

	s := newTestServer(t, Hooks{})
	if err := s.Reload([]config.StreamServer{{
		Listen:   addr,
		Protocol: "tcp",
		SNIRoutes: map[string]string{
			"one.example.com": b1,
			"two.example.com": b2,
		},
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}

	check := func(serverName, want string) {
		t.Helper()
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.Close()
		if _, err := c.Write(clientHelloBytes(t, serverName)); err != nil {
			t.Fatalf("write hello: %v", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 3)
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Fatalf("read announce: %v", err)
		}
		if string(buf) != want {
			t.Errorf("sni %q routed to %q, want %q", serverName, buf, want)
		}
	}
	check("one.example.com", "ONE")
	check("two.example.com", "TWO")
}

func TestTCPSNICatchAll(t *testing.T) {
	b1, stop1 := tcpAnnounce(t, "DEF")
	defer stop1()
	addr := freeTCPAddr(t)

	s := newTestServer(t, Hooks{})
	if err := s.Reload([]config.StreamServer{{
		Listen:    addr,
		Protocol:  "tcp",
		SNIRoutes: map[string]string{"*": b1},
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, _ = c.Write(clientHelloBytes(t, "unknown.example.com"))
	buf := make([]byte, 3)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "DEF" {
		t.Errorf("catch-all routed to %q, want DEF", buf)
	}
}

func TestProxyProtocolInAndOut(t *testing.T) {
	backend, stop := tcpProxyHeaderReader(t)
	defer stop()
	addr := freeTCPAddr(t)

	s := newTestServer(t, Hooks{})
	if err := s.Reload([]config.StreamServer{{
		Listen: addr, Protocol: "tcp", ProxyPass: backend, ProxyProtocol: "both",
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// Client advertises a real source via an inbound v1 header.
	_, _ = c.Write([]byte("PROXY TCP4 1.2.3.4 5.6.7.8 1111 2222\r\n"))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(buf[:n])
	if !bytes.Contains(buf[:n], []byte("1.2.3.4:1111")) {
		t.Errorf("backend saw source %q, want 1.2.3.4:1111", got)
	}
}

func TestUDPProxyRelay(t *testing.T) {
	backend, stop := udpEcho(t)
	defer stop()
	addr := freeUDPAddr(t)

	var conns atomic.Int64
	s := newTestServer(t, Hooks{OnConnDelta: func(_ string, d int64) { conns.Add(d) }})
	if err := s.Reload([]config.StreamServer{{
		Listen: addr, Protocol: "udp", ProxyPass: backend,
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}

	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("datagram")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "datagram" {
		t.Fatalf("udp echo: got %q", buf[:n])
	}
	if conns.Load() != 1 {
		t.Errorf("udp session gauge: got %d want 1", conns.Load())
	}
}

func TestReloadSwapsTargetAndStops(t *testing.T) {
	b1, stop1 := tcpAnnounce(t, "AAA")
	defer stop1()
	b2, stop2 := tcpAnnounce(t, "BBB")
	defer stop2()
	addr := freeTCPAddr(t)

	s := newTestServer(t, Hooks{})
	if err := s.Reload([]config.StreamServer{{Listen: addr, Protocol: "tcp", ProxyPass: b1}}, nil); err != nil {
		t.Fatalf("reload 1: %v", err)
	}
	read3 := func() string {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 3)
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(buf)
	}
	if got := read3(); got != "AAA" {
		t.Fatalf("before swap: got %q want AAA", got)
	}

	// Swap the target on the same listen address (no rebind).
	if err := s.Reload([]config.StreamServer{{Listen: addr, Protocol: "tcp", ProxyPass: b2}}, nil); err != nil {
		t.Fatalf("reload 2: %v", err)
	}
	if got := read3(); got != "BBB" {
		t.Fatalf("after swap: got %q want BBB", got)
	}

	// Reload to empty: the listener must stop and the port must free up.
	if err := s.Reload(nil, nil); err != nil {
		t.Fatalf("reload empty: %v", err)
	}
	if _, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
		t.Errorf("expected connection refused after stopping listener")
	}
}

func TestReloadBindFailureRollsBack(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	// Occupy a port so the stream listener cannot bind it.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy: %v", err)
	}
	defer busy.Close()
	busyAddr := busy.Addr().String()

	s := newTestServer(t, Hooks{})
	err = s.Reload([]config.StreamServer{{Listen: busyAddr, Protocol: "tcp", ProxyPass: backend}}, nil)
	if err == nil {
		t.Fatal("expected bind error for an address already in use")
	}
	// A failed reload must leave no listeners running.
	s.mu.Lock()
	n := len(s.listeners)
	s.mu.Unlock()
	if n != 0 {
		t.Errorf("after failed reload: %d listeners, want 0", n)
	}
}

func TestProxyProtocolV2RoundTrip(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 4567}
	dst := &net.TCPAddr{IP: net.ParseIP("10.9.8.7"), Port: 89}
	var buf bytes.Buffer
	if err := writeProxyV2(&buf, src, dst); err != nil {
		t.Fatalf("write: %v", err)
	}
	br := bufio.NewReader(&buf)
	got, err := readProxyHeader(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil || got.String() != "10.1.2.3:4567" {
		t.Errorf("round-trip src: got %v want 10.1.2.3:4567", got)
	}
}

func TestProxyProtocolV2IPv6RoundTrip(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 443}
	dst := &net.TCPAddr{IP: net.ParseIP("2001:db8::2"), Port: 80}
	var buf bytes.Buffer
	if err := writeProxyV2(&buf, src, dst); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readProxyHeader(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil || got.(*net.TCPAddr).Port != 443 {
		t.Errorf("ipv6 round-trip: got %v", got)
	}
}

func TestProxyProtocolV1Parse(t *testing.T) {
	br := bufio.NewReader(bytes.NewReader([]byte("PROXY TCP4 192.168.0.1 192.168.0.2 56324 443\r\nDATA")))
	got, err := readProxyHeader(br)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.String() != "192.168.0.1:56324" {
		t.Errorf("v1 src: got %v", got)
	}
	// The header is consumed; the payload remains.
	rest, _ := io.ReadAll(br)
	if string(rest) != "DATA" {
		t.Errorf("remaining payload: got %q want DATA", rest)
	}
}

func TestSNIPeekDoesNotConsume(t *testing.T) {
	hello := clientHelloBytes(t, "host.example.com")
	br := bufio.NewReaderSize(bytes.NewReader(hello), tlsRecordMax+512)
	host := peekSNI(br)
	if host != "host.example.com" {
		t.Fatalf("peekSNI: got %q want host.example.com", host)
	}
	// The peeked bytes must still be readable in full.
	rest, _ := io.ReadAll(br)
	if !bytes.Equal(rest, hello) {
		t.Errorf("peek consumed bytes: got %d want %d", len(rest), len(hello))
	}
}

func TestSNIPeekNonTLS(t *testing.T) {
	br := bufio.NewReaderSize(bytes.NewReader([]byte("GET / HTTP/1.1\r\n")), 1024)
	if host := peekSNI(br); host != "" {
		t.Errorf("non-TLS peekSNI: got %q want empty", host)
	}
}

// TestCloseStopsAllListeners verifies Close releases sockets and goroutines.
func TestCloseStopsAllListeners(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	addr := freeTCPAddr(t)
	s := NewServer(Options{Logger: discardLogger()})
	if err := s.Reload([]config.StreamServer{{Listen: addr, Protocol: "tcp", ProxyPass: backend}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.Close() }()
	wg.Wait()
	if _, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
		t.Errorf("expected refused connection after Close")
	}
}

func TestCheckEmpty(t *testing.T) {
	if err := Check(nil); err != nil {
		t.Fatalf("Check(nil): %v", err)
	}
	if err := Check([]config.StreamServer{}); err != nil {
		t.Fatalf("Check(empty): %v", err)
	}
}
