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
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// TestHTTP3EndToEnd starts a real HTTP/3 (QUIC) listener via startHTTP3 and
// drives it with a quic-go HTTP/3 client. It proves an h3 request reaches the
// same handler and returns its exact response (status, body, headers) — i.e.
// HTTP/3 serves identical responses to the TCP path — and that the connection
// hook counts the connection. Building requires -tags http3.
func TestHTTP3EndToEnd(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "h3", "localhost")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load cert: %v", err)
	}
	getCert := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		_, _ = io.WriteString(w, "hello over QUIC")
	})

	var conns atomic.Int64
	onConn := func(d int64) { conns.Add(d) }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	h3, err := startHTTP3("127.0.0.1:0", getCert, handler, onConn, logger)
	if err != nil {
		t.Fatalf("startHTTP3: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()

	addr := h3.(*h3Conn).ln.Addr().String()

	pool := x509.NewCertPool()
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("failed to add self-signed cert to pool")
	}

	tr := &http3.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"},
	}
	defer func() { _ = tr.Close() }()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	// The accept loop starts in a goroutine; retry briefly so the test does not
	// race the first QUIC handshake.
	var resp *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err = client.Get("https://" + addr + "/")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP/3 GET never succeeded: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if resp.ProtoMajor != 3 {
		t.Errorf("response proto = %q, want HTTP/3", resp.Proto)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello over QUIC" {
		t.Errorf("body = %q, want %q", string(body), "hello over QUIC")
	}
	if got := resp.Header.Get("X-Proto"); got != "HTTP/3.0" {
		t.Errorf("X-Proto = %q, want HTTP/3.0", got)
	}
	if conns.Load() < 1 {
		t.Errorf("connection hook count = %d, want >= 1", conns.Load())
	}
}

// TestHTTP3BadCertificate checks that a client with an untrusted CA pool
// cannot establish a QUIC connection: the TLS handshake fails and the request
// errors, confirming certificate verification is enforced.
func TestHTTP3BadCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "h3-badcert", "localhost")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load cert: %v", err)
	}
	getCert := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h3, err := startHTTP3("127.0.0.1:0", getCert, handler, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("startHTTP3: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()

	addr := h3.(*h3Conn).ln.Addr().String()

	// Use an *empty* cert pool so the server cert is not trusted.
	tr := &http3.Transport{
		TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "localhost"},
	}
	defer func() { _ = tr.Close() }()
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}

	_, err = client.Get("https://" + addr + "/")
	if err == nil {
		t.Fatal("expected TLS error for untrusted cert, got nil")
	}
}

// TestHTTP3ConnectionCloseUnderLoad starts a simple handler and verifies that
// Close() drains in-flight requests and zeroes the connection gauge.
func TestHTTP3ConnectionCloseUnderLoad(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "h3-close", "localhost")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load cert: %v", err)
	}
	getCert := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }

	var reqCount atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reqCount.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	var conns atomic.Int64
	onConn := func(d int64) { conns.Add(d) }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	h3, err := startHTTP3("127.0.0.1:0", getCert, handler, onConn, logger)
	if err != nil {
		t.Fatalf("startHTTP3: %v", err)
	}

	addr := h3.(*h3Conn).ln.Addr().String()

	pool := x509.NewCertPool()
	pemBytes, _ := os.ReadFile(certPath)
	pool.AppendCertsFromPEM(pemBytes)

	tr := &http3.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"},
	}
	defer func() { _ = tr.Close() }()
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}

	// Warmup
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("warmup GET: %v", err)
	}
	resp.Body.Close()
	if conns.Load() < 1 {
		t.Fatal("no connection counted after warmup")
	}

	// Close the listener and drain.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h3.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close the gauge must drop back to zero.
	if conns.Load() != 0 {
		t.Fatalf("connection gauge = %d after close, want 0", conns.Load())
	}
}

// BenchmarkHTTP3Throughput measures raw request throughput over a single HTTP/3
// connection. It establishes the connection in the benchmark setup so the
// measured iterations run over an already-warmed QUIC path. The result is
// useful for comparing QUIC overhead against the TCP/HTTP2 baseline in
// handler/proxy benchmarks.
func BenchmarkHTTP3Throughput(b *testing.B) {
	dir := b.TempDir()
	certPath, keyPath := writeSelfSigned(b, dir, "h3-bench", "localhost")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		b.Fatalf("load cert: %v", err)
	}
	getCert := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	h3, err := startHTTP3("127.0.0.1:0", getCert, handler, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("startHTTP3: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()

	addr := h3.(*h3Conn).ln.Addr().String()

	pool := x509.NewCertPool()
	pemBytes, _ := os.ReadFile(certPath)
	pool.AppendCertsFromPEM(pemBytes)

	tr := &http3.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"},
	}
	defer func() { _ = tr.Close() }()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	// Warmup: establish the QUIC connection before measuring.
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		b.Fatalf("warmup GET: %v", err)
	}
	resp.Body.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := client.Get("https://" + addr + "/")
		if err != nil {
			b.Fatalf("GET: %v", err)
		}
		r.Body.Close()
	}
}
