// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer && stream

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"jul/internal/app"
	"jul/internal/config"
	"jul/internal/migrate/nginx/corpus"
	"jul/internal/proxyproto"
)

// streamCorpusReadiness is the plain-HTTP readiness probe every stream corpus
// fixture's paired `http {}` block answers, since a stream listener itself
// speaks no HTTP.
var streamCorpusReadiness = corpus.Scenario{Request: corpus.RequestSpec{Method: "GET", Path: "/"}}

// reserveLoopbackUDPAddress returns a free loopback UDP address, mirroring
// reserveLoopbackAddress's reserve-then-release pattern for TCP.
func reserveLoopbackUDPAddress(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("reserve loopback UDP address: %v", err)
	}
	address := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatalf("release loopback UDP address: %v", err)
	}
	return address
}

// clientHelloBytesForCorpus captures a real TLS ClientHello for serverName,
// the same net.Pipe-capture technique internal/stream's own tests use
// (unexported there, so reproduced here rather than imported).
func clientHelloBytesForCorpus(t *testing.T, serverName string) []byte {
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

// startJulForStreamCorpus starts a real Jul instance from an already-wired
// stream corpus candidate: every [[stream]] target and [[upstreams]] member
// must already point at a real local backend before calling this, since
// (unlike startRealJulForCorpus) it does not run startCorpusTCPBackends -
// that helper unconditionally rewrites any splittable host:port upstream
// member to its own generic HTTP handler, which would clobber the raw TCP/UDP
// backends a stream scenario needs. Readiness is still polled over the
// fixture's own plain HTTP block (every stream corpus fixture pairs its
// `stream {}` section with a trivial `http { listen ...; return 204; }`
// server for exactly this purpose), since a stream listener speaks no HTTP.
func startJulForStreamCorpus(t *testing.T, name string, cfg *config.Config) (cleanup func(), logs *corpusLogBuffer) {
	t.Helper()
	if len(cfg.Servers) != 1 {
		t.Fatalf("%s: fixture requires exactly one HTTP readiness server, got %d", name, len(cfg.Servers))
	}
	cfg.Servers[0].Listen = reserveLoopbackAddress(t)
	if err := app.ValidateRuntimeConfig(context.Background(), cfg); err != nil {
		t.Fatalf("%s: runtime preflight: %v", name, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan struct{})
	logs = &corpusLogBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- app.Serve(ctx, reload, memorySource{name: "<nginx-corpus:" + name + ">", cfg: cfg}, cfg, productName, version, app.WithLogOutput(logs))
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	waitForCorpusServer(t, ctx, client, "http://"+cfg.Servers[0].Listen, streamCorpusReadiness, done, logs)
	client.CloseIdleConnections()

	return func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("%s: Jul exit code = %d\nlogs:\n%s", name, code, logs.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: Jul did not shut down\nlogs:\n%s", name, logs.String())
		}
	}, logs
}

// streamEchoBackend is a raw TCP backend for stream E2E scenarios: it echoes
// whatever bytes it receives verbatim - proving byte-for-byte payload relay. None
// of streamEchoBackend's callers enable outbound proxy_protocol (that is
// streamIdentifyingBackend's scenario), so it relays raw bytes unconditionally
// rather than peeking for a PROXY header first: a fixed 12-byte peek would
// block forever on a short payload (e.g. "ping\n") sent alone with no PROXY
// prefix, since bufio.Reader.Peek waits for that many bytes or an error.
func streamEchoBackend(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve stream echo backend: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// streamIdentifyingBackend is a raw TCP backend that reports its own id after
// consuming an optional PROXY v2 header and a one-line ping, so a weighted
// stream upstream's dispatch can be counted the same way the HTTP weighted
// test counts X-Corpus-Backend-Id. srcAddrs, when non-nil, receives the
// PROXY-asserted source address string of every connection it accepts.
func streamIdentifyingBackend(t *testing.T, id string, srcAddrs chan<- string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve stream identifying backend %s: %v", id, err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				src, err := proxyproto.ReadHeader(br)
				if err != nil {
					return
				}
				if srcAddrs != nil {
					addr := ""
					if src != nil {
						addr = src.String()
					}
					srcAddrs <- addr
				}
				if _, err := br.ReadString('\n'); err != nil {
					return
				}
				_, _ = c.Write([]byte(id + "\n"))
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// TestNGINXCorpusStreamTCPBasicRealE2E proves the bounded stream translation
// (#426) actually relays bytes through a real Jul TCP listener to a real
// local backend, and that proxy_timeout -> idle_timeout takes effect: an idle
// connection is closed once the (here shortened, for a fast test) idle
// timeout elapses.
func TestNGINXCorpusStreamTCPBasicRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "stream-tcp-basic")
	if len(cfg.Streams) != 1 || cfg.Streams[0].ProxyPass == "" {
		t.Fatalf("stream-tcp-basic: want 1 stream server with a direct proxy_pass, got %+v", cfg.Streams)
	}
	backend := streamEchoBackend(t)
	cfg.Streams[0].Listen = reserveLoopbackAddress(t)
	cfg.Streams[0].ProxyPass = backend.Addr().String()
	cfg.Streams[0].IdleTimeout = config.Duration(300 * time.Millisecond)

	cleanup, _ := startJulForStreamCorpus(t, "stream-tcp-basic", cfg)
	defer cleanup()

	conn, err := net.DialTimeout("tcp", cfg.Streams[0].Listen, 2*time.Second)
	if err != nil {
		t.Fatalf("dial stream listener: %v", err)
	}
	defer conn.Close()
	const payload = "hello-stream-tcp\n"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read echoed payload: %v", err)
	}
	if got != payload {
		t.Fatalf("echoed payload = %q, want %q (byte-for-byte relay)", got, payload)
	}

	// idle_timeout: a second connection that never sends anything must be
	// closed by Jul once the idle window elapses, without any data flowing.
	idle, err := net.DialTimeout("tcp", cfg.Streams[0].Listen, 2*time.Second)
	if err != nil {
		t.Fatalf("dial for idle-timeout case: %v", err)
	}
	defer idle.Close()
	_ = idle.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if n, err := idle.Read(buf); err == nil {
		t.Fatalf("idle connection: got %d bytes, want the connection closed after idle_timeout", n)
	}
}

// TestNGINXCorpusStreamNamedUpstreamRealE2E proves a stream server's named
// upstream (reusing the HTTP upstream translation, #426) dispatches with the
// exact same deterministic smooth-weighted-round-robin algorithm the HTTP
// path already proves, and that outbound PROXY protocol asserts the real
// client's own address to the backend on every connection - a distinct
// dispatch happens once per TCP connection (a stream listener has no
// per-request concept), so counting requires one connection per sample
// rather than one keep-alive HTTP request per sample.
func TestNGINXCorpusStreamNamedUpstreamRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "stream-named-upstream")
	if len(cfg.Streams) != 1 || cfg.Streams[0].ProxyPass == "" || cfg.Streams[0].ProxyProtocol != "out" {
		t.Fatalf("stream-named-upstream: want 1 stream server with a named upstream and proxy_protocol=out, got %+v", cfg.Streams)
	}
	if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Servers) != 2 {
		t.Fatalf("stream-named-upstream: want 1 upstream with 2 members, got %+v", cfg.Upstreams)
	}

	srcAddrs := make(chan string, 128)
	weightedLn := streamIdentifyingBackend(t, "weighted", srcAddrs)
	defaultLn := streamIdentifyingBackend(t, "default", srcAddrs)
	cfg.Upstreams[0].Servers[0].Address = weightedLn.Addr().String() // weight=5, declared first
	cfg.Upstreams[0].Servers[1].Address = defaultLn.Addr().String()  // implicit weight=1
	cfg.Streams[0].Listen = reserveLoopbackAddress(t)

	cleanup, _ := startJulForStreamCorpus(t, "stream-named-upstream", cfg)
	defer cleanup()

	counts := map[string]int{}
	const connections = 60 // 10 full weight(5):weight(1) cycles of 6
	for i := 0; i < connections; i++ {
		conn, err := net.DialTimeout("tcp", cfg.Streams[0].Listen, 2*time.Second)
		if err != nil {
			t.Fatalf("connection %d: dial: %v", i, err)
		}
		if _, err := conn.Write([]byte("ping\n")); err != nil {
			t.Fatalf("connection %d: write: %v", i, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		id, err := bufio.NewReader(conn).ReadString('\n')
		_ = conn.Close()
		if err != nil {
			t.Fatalf("connection %d: read backend id: %v", i, err)
		}
		counts[strings.TrimSuffix(id, "\n")]++
	}

	if weighted, unweighted := counts["weighted"], counts["default"]; weighted != 50 || unweighted != 10 {
		t.Fatalf("weighted distribution = weight5:%d weight1:%d, want exactly 50:10 (smooth weighted round-robin over %d connections)", weighted, unweighted, connections)
	}

	close(srcAddrs)
	sawLoopback := false
	for addr := range srcAddrs {
		if addr == "" {
			t.Fatal("backend saw a connection with no PROXY-asserted source address; want outbound proxy_protocol to have propagated one")
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("PROXY-asserted address %q: %v", addr, err)
		}
		if host == "127.0.0.1" {
			sawLoopback = true
		}
	}
	if !sawLoopback {
		t.Fatal("no backend connection carried the real client's own loopback address via outbound PROXY protocol")
	}
}

// TestNGINXCorpusStreamUDPRealE2E proves a UDP stream listener relays
// datagrams to a real local backend and returns the response to the original
// client, matching stream-udp's bounded UDP session model.
func TestNGINXCorpusStreamUDPRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "stream-udp")
	if len(cfg.Streams) != 1 || cfg.Streams[0].Protocol != "udp" {
		t.Fatalf("stream-udp: want 1 udp stream server, got %+v", cfg.Streams)
	}

	backendLn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("reserve UDP backend: %v", err)
	}
	t.Cleanup(func() { _ = backendLn.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := backendLn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = backendLn.WriteToUDP(buf[:n], addr)
		}
	}()
	cfg.Streams[0].ProxyPass = backendLn.LocalAddr().String()
	cfg.Streams[0].Listen = reserveLoopbackUDPAddress(t)

	cleanup, _ := startJulForStreamCorpus(t, "stream-udp", cfg)
	defer cleanup()

	clientConn, err := net.Dial("udp", cfg.Streams[0].Listen)
	if err != nil {
		t.Fatalf("dial stream UDP listener: %v", err)
	}
	defer clientConn.Close()
	const payload = "hello-stream-udp"
	if _, err := clientConn.Write([]byte(payload)); err != nil {
		t.Fatalf("write datagram: %v", err)
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("read echoed datagram: %v", err)
	}
	if got := string(buf[:n]); got != payload {
		t.Fatalf("echoed datagram = %q, want %q", got, payload)
	}
}

// TestNGINXCorpusStreamSNIBoundedRealE2E proves the merged SNI-routing group
// (#426, three server_name-distinguished stream servers sharing one listen
// address) dispatches a real TLS ClientHello to the backend its own SNI host
// names, and falls back to the no-server_name member for an unmatched host -
// all without Jul terminating TLS (ClientHello bytes are peeked, then the
// full connection, including that same ClientHello, is relayed verbatim).
func TestNGINXCorpusStreamSNIBoundedRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "stream-sni-bounded")
	if len(cfg.Streams) != 1 || len(cfg.Streams[0].SNIRoutes) != 2 || cfg.Streams[0].ProxyPass == "" {
		t.Fatalf("stream-sni-bounded: want 1 stream server with 2 SNI routes plus a fallback, got %+v", cfg.Streams)
	}

	aLn := streamEchoBackend(t)
	bLn := streamEchoBackend(t)
	fallbackLn := streamEchoBackend(t)
	cfg.Streams[0].SNIRoutes = map[string]string{
		"a.stream-sni.test": aLn.Addr().String(),
		"b.stream-sni.test": bLn.Addr().String(),
	}
	cfg.Streams[0].ProxyPass = fallbackLn.Addr().String()
	cfg.Streams[0].Listen = reserveLoopbackAddress(t)

	cleanup, _ := startJulForStreamCorpus(t, "stream-sni-bounded", cfg)
	defer cleanup()

	dialAndEcho := func(t *testing.T, sni string) string {
		t.Helper()
		conn, err := net.DialTimeout("tcp", cfg.Streams[0].Listen, 2*time.Second)
		if err != nil {
			t.Fatalf("sni %q: dial: %v", sni, err)
		}
		defer conn.Close()
		hello := clientHelloBytesForCorpus(t, sni)
		if _, err := conn.Write(hello); err != nil {
			t.Fatalf("sni %q: write ClientHello: %v", sni, err)
		}
		payload := "sni-check:" + sni + "\n"
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatalf("sni %q: write payload: %v", sni, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		br := bufio.NewReader(conn)
		got := make([]byte, len(hello))
		if _, err := io.ReadFull(br, got); err != nil {
			t.Fatalf("sni %q: read echoed ClientHello: %v", sni, err)
		}
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("sni %q: read echoed payload: %v", sni, err)
		}
		return line
	}

	if got, want := dialAndEcho(t, "a.stream-sni.test"), "sni-check:a.stream-sni.test\n"; got != want {
		t.Fatalf("route a: echoed payload = %q, want %q (backend a selected)", got, want)
	}
	if got, want := dialAndEcho(t, "b.stream-sni.test"), "sni-check:b.stream-sni.test\n"; got != want {
		t.Fatalf("route b: echoed payload = %q, want %q (backend b selected)", got, want)
	}
	if got, want := dialAndEcho(t, "unmatched.stream-sni.test"), "sni-check:unmatched.stream-sni.test\n"; got != want {
		t.Fatalf("unmatched host: echoed payload = %q, want %q (fallback backend selected)", got, want)
	}
}

// TestNGINXCorpusStreamUpstreamFailoverRealE2E proves a stream server's named
// upstream reuses Jul's shared upstream-wide resilience circuit breaker
// (#365's max_fails/fail_timeout translation) exactly as HTTP does: a
// deliberately dead backend fails its first dial, Jul's default
// retry-every-distinct-backend-on-dial-failure behavior masks that failure by
// falling through to the healthy one within the same client connection, the
// max_fails=1 breaker then excludes the dead backend for fail_timeout, and
// every following connection goes straight to the healthy backend.
func TestNGINXCorpusStreamUpstreamFailoverRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "stream-upstream-failover-runtime")
	if len(cfg.Streams) != 1 || cfg.Streams[0].ProxyPass == "" {
		t.Fatalf("stream-upstream-failover-runtime: want 1 stream server with a named upstream, got %+v", cfg.Streams)
	}
	if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Servers) != 2 {
		t.Fatalf("stream-upstream-failover-runtime: want 1 upstream with 2 servers, got %+v", cfg.Upstreams)
	}
	if cfg.Upstreams[0].Resilience == nil || cfg.Upstreams[0].Resilience.MaxFails != 1 {
		t.Fatalf("stream-upstream-failover-runtime: candidate resilience = %+v, want MaxFails=1 from the translated max_fails", cfg.Upstreams[0].Resilience)
	}

	healthy := streamEchoBackend(t)

	cfg.Upstreams[0].Servers[0].Address = healthy.Addr().String()
	// 127.0.0.1:1 is a fixed, privileged, never-bound loopback port: unlike a
	// reserve-then-release ephemeral port, it cannot be handed straight back
	// out as the client's own OS-assigned source port for the very next
	// dial (a real "connect to self" hazard observed with the ephemeral
	// reserve/release pattern here), so every dial against it gets a
	// deterministic, immediate connection refused.
	cfg.Upstreams[0].Servers[1].Address = "127.0.0.1:1"
	cfg.Streams[0].Listen = reserveLoopbackAddress(t)

	cleanup, logs := startJulForStreamCorpus(t, "stream-upstream-failover-runtime", cfg)
	defer cleanup()

	const connections = 12
	for i := 0; i < connections; i++ {
		conn, err := net.DialTimeout("tcp", cfg.Streams[0].Listen, 2*time.Second)
		if err != nil {
			t.Fatalf("connection %d: dial: %v (failover should be transparent to the client)", i, err)
		}
		payload := "ping\n"
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatalf("connection %d: write: %v", i, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		got, err := bufio.NewReader(conn).ReadString('\n')
		_ = conn.Close()
		if err != nil {
			t.Fatalf("connection %d: read: %v (failover should be transparent to the client)\nlogs:\n%s", i, err, logs.String())
		}
		if got != payload {
			t.Fatalf("connection %d: echoed payload = %q, want %q", i, got, payload)
		}
	}
}
