// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
)

type h3LimitFixture struct {
	h3   *h3Conn
	addr string
	pool *x509.CertPool
}

func startLimitedHTTP3(t *testing.T, handler http.Handler, limits h3Limits) h3LimitFixture {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "h3", "localhost")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load cert: %v", err)
	}
	tlsConf := &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }}
	ln, err := newStagedHTTP3WithLimits("127.0.0.1:0", tlsConf, handler, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), limits)
	if err != nil {
		t.Fatalf("newStagedHTTP3WithLimits: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close(context.Background()) })
	if err := ln.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pemBytes)
	h3 := ln.(*h3Conn)
	return h3LimitFixture{h3: h3, addr: h3.ln.Addr().String(), pool: pool}
}

func (f h3LimitFixture) client(t *testing.T, timeout time.Duration) (*http.Client, *http3.Transport) {
	t.Helper()
	tr := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: f.pool, ServerName: "localhost"}}
	t.Cleanup(func() { _ = tr.Close() })
	return &http.Client{Transport: tr, Timeout: timeout}, tr
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
}

func TestHTTP3ConnGateBlocksAtLimitAndReleases(t *testing.T) {
	g := newH3ConnGate(1)
	if !g.acquire() {
		t.Fatal("first acquire must succeed")
	}
	admitted := make(chan bool, 1)
	go func() { admitted <- g.acquire() }()
	select {
	case <-admitted:
		t.Fatal("second acquire must wait while the only slot is in use")
	case <-time.After(50 * time.Millisecond):
	}
	g.release()
	if !<-admitted {
		t.Fatal("waiter must be admitted after a release")
	}

	g.setLimit(1)
	go func() { admitted <- g.acquire() }()
	time.Sleep(20 * time.Millisecond)
	g.setLimit(0) // unlimited wakes the waiter
	if !<-admitted {
		t.Fatal("raising the cap to unlimited must admit the waiter")
	}
	if limit, active := g.snapshot(); limit != 0 || active != 2 {
		t.Fatalf("snapshot = %d/%d, want 0/2", limit, active)
	}

	g.setLimit(1)
	go func() { admitted <- g.acquire() }()
	time.Sleep(20 * time.Millisecond)
	g.close()
	if <-admitted {
		t.Fatal("close must refuse a waiting admission")
	}
}

func TestHTTP3HonoursMaxHeaderBytes(t *testing.T) {
	f := startLimitedHTTP3(t, okHandler(), h3Limits{maxHeaderBytes: 4 << 10})
	client, _ := f.client(t, 5*time.Second)

	req, _ := http.NewRequest(http.MethodGet, "https://"+f.addr+"/", nil)
	req.Header.Set("X-Small", "ok")
	resp, err := retryH3(client, req)
	if err != nil {
		t.Fatalf("small headers: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("small headers status %d", resp.StatusCode)
	}

	big, _ := http.NewRequest(http.MethodGet, "https://"+f.addr+"/", nil)
	big.Header.Set("X-Big", strings.Repeat("a", 16<<10))
	resp, err = client.Do(big)
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("a HEADERS frame larger than max_header_bytes must not be served")
		}
	}
}

func TestHTTP3HonoursIdleTimeout(t *testing.T) {
	f := startLimitedHTTP3(t, okHandler(), h3Limits{idleTimeout: 300 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, f.addr, &tls.Config{RootCAs: f.pool, ServerName: "localhost", NextProtos: []string{http3.NextProtoH3}}, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	select {
	case <-conn.Context().Done():
	case <-time.After(10 * time.Second):
		t.Fatal("an idle QUIC connection must close well before the 30s library default")
	}
}

func TestHTTP3HonoursMaxConns(t *testing.T) {
	f := startLimitedHTTP3(t, okHandler(), h3Limits{connLimit: 1})
	first, firstTr := f.client(t, 5*time.Second)
	req, _ := http.NewRequest(http.MethodGet, "https://"+f.addr+"/", nil)
	resp, err := retryH3(first, req)
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	_ = resp.Body.Close()

	second, secondTr := f.client(t, 700*time.Millisecond)
	if resp, err := second.Get("https://" + f.addr + "/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a second QUIC connection must wait while max_conns=1 is in use")
	}

	// Freeing the only slot admits the connection that was waiting for it.
	_ = firstTr.Close()
	waited := &http.Client{Transport: secondTr, Timeout: 5 * time.Second}
	resp, err = retryH3(waited, req)
	if err != nil {
		t.Fatalf("after the slot frees, the waiting connection must be served: %v", err)
	}
	_ = resp.Body.Close()

	f.h3.SetConnLimit(0)
	if limit, _ := f.h3.gate.snapshot(); limit != 0 {
		t.Fatalf("SetConnLimit(0) must make the gate unlimited, got %d", limit)
	}
}

func TestHTTP3ListenerInheritsListenerLimits(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "h3", "localhost")
	addr := freePort(t)
	cfg := tlsCfgFor(addr, certPath, keyPath, "localhost")
	cfg.RateLimit = config.RateLimitConfig{Enabled: true, MaxConns: 7}
	cfg.Servers[0].MaxHeaderBytes = config.Size(8 << 10)
	cfg.Servers[0].IdleTimeout = config.Duration(45 * time.Second)
	cfg.Servers[0].HTTP3 = &config.HTTP3Config{Enabled: true}

	s := &Server{cfg: cfg, log: quietLogger(), listeners: map[string]*listenerEntry{}}
	entry, err := s.buildListenerEntry(addr, cfg)
	if err != nil {
		t.Fatalf("buildListenerEntry: %v", err)
	}
	t.Cleanup(func() {
		_ = entry.ln.Close()
		if entry.h3 != nil {
			_ = entry.h3.Close(context.Background())
		}
	})
	h3, ok := entry.h3.(*h3Conn)
	if !ok {
		t.Fatalf("expected a staged HTTP/3 listener, got %T", entry.h3)
	}
	if h3.server.MaxHeaderBytes != 8<<10 || h3.server.IdleTimeout != 45*time.Second {
		t.Fatalf("http3.Server limits = %d/%s, want 8KiB/45s", h3.server.MaxHeaderBytes, h3.server.IdleTimeout)
	}
	if limit, _ := h3.gate.snapshot(); limit != 7 {
		t.Fatalf("QUIC connection cap = %d, want max_conns 7", limit)
	}

	s.mu.Lock()
	s.listeners[addr] = entry
	s.mu.Unlock()
	s.updateConnectionLimits(&config.Config{RateLimit: config.RateLimitConfig{Enabled: true, MaxConns: 3}})
	if limit, _ := h3.gate.snapshot(); limit != 3 {
		t.Fatalf("reloaded QUIC connection cap = %d, want 3", limit)
	}
}

func retryH3(client *http.Client, req *http.Request) (*http.Response, error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Do(req)
		if err == nil || time.Now().After(deadline) {
			return resp, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}
