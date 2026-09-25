// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/clientaddr"
	"jul/internal/middleware"
)

// gzipCompression builds a real gzip Compression middleware matching-any
// content type, so these tests exercise the actual encoder rather than a
// stand-in.
func gzipCompression(t *testing.T) middleware.Middleware {
	t.Helper()
	compress, err := middleware.NewCompression(middleware.CompressionOptions{
		Encoders: []string{"gzip"},
		MinSize:  0,
		Types:    []string{"*/*"},
	})
	if err != nil {
		t.Fatalf("NewCompression: %v", err)
	}
	return compress
}

// runThroughChain sends req through the real global middleware chain (request
// id, client address, metrics, compression) built the same way HandlerFactory
// builds it for a live listener, and returns the recorded response together
// with the exact bytes the recorder actually received on the wire.
func runThroughChain(t *testing.T, f *HandlerFactory, compress middleware.Middleware, h http.Handler, req *http.Request) (*httptest.ResponseRecorder, int) {
	t.Helper()
	policy, err := clientaddr.NewPolicy(nil, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	chain := middleware.Chain(h, f.globalChain(policy, compress, nil)...)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	return rec, rec.Body.Len()
}

// TestHTTPResponseBytesCountedPostCompression is #431's mandatory
// compression-ordering proof (§21): the counter must equal the compressed
// wire size, not the size of the uncompressed body the handler wrote. If the
// increment were ever moved to read a pre-compression byte count, or
// compression were reordered ahead of the metrics observer, this assertion
// would fail.
func TestHTTPResponseBytesCountedPostCompression(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	compress := gzipCompression(t)

	body := strings.Repeat("compressible-payload ", 500)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec, wireBytes := runThroughChain(t, f, compress, handler, req)

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("response was not compressed (Content-Encoding = %q); test setup is broken", rec.Header().Get("Content-Encoding"))
	}
	if wireBytes >= len(body) {
		t.Fatalf("compressed body (%d bytes) is not smaller than the original (%d bytes); payload is not actually compressible", wireBytes, len(body))
	}

	got := f.Metrics.Snapshot().HTTPResponseBytesTotal
	if got != float64(wireBytes) {
		t.Fatalf("HTTPResponseBytesTotal = %v, want exactly the compressed byte count %d", got, wireBytes)
	}
}

// TestHTTPResponseBytesUncompressedExact proves the ordinary (no
// Accept-Encoding) case counts exactly the bytes the handler wrote.
func TestHTTPResponseBytesUncompressedExact(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	compress := gzipCompression(t)

	const body = "plain uncompressed response body"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No Accept-Encoding: the compression middleware must pass the body through.
	before := f.Metrics.Snapshot().HTTPResponseBytesTotal
	rec, wireBytes := runThroughChain(t, f, compress, handler, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("response was unexpectedly compressed without Accept-Encoding")
	}
	if wireBytes != len(body) {
		t.Fatalf("wire bytes = %d, want exactly %d", wireBytes, len(body))
	}
	after := f.Metrics.Snapshot().HTTPResponseBytesTotal
	if delta := after - before; delta != float64(len(body)) {
		t.Fatalf("HTTPResponseBytesTotal delta = %v, want exactly %d", delta, len(body))
	}
}

// TestHTTPResponseBytesEmptyBody proves a bodyless response (e.g. 204)
// contributes zero, not a fabricated value.
func TestHTTPResponseBytesEmptyBody(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	compress := gzipCompression(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	before := f.Metrics.Snapshot().HTTPResponseBytesTotal
	rec, wireBytes := runThroughChain(t, f, compress, handler, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if wireBytes != 0 {
		t.Fatalf("wire bytes = %d, want 0 for a bodyless response", wireBytes)
	}
	after := f.Metrics.Snapshot().HTTPResponseBytesTotal
	if after != before {
		t.Fatalf("HTTPResponseBytesTotal changed (%v -> %v) for a bodyless response", before, after)
	}
}

// hijackableRecorder is an httptest.ResponseRecorder that also implements
// http.Hijacker over an in-memory pipe, so the chain's Hijack call succeeds.
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	buf := bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn))
	return h.conn, buf, nil
}

// TestHTTPResponseBytesExcludesPostHijackBytes proves the documented WebSocket
// disposition (#431 §22): bytes written after a connection hijack are not
// counted by this HTTP body metric, because they are no longer HTTP responses
// the recorder can see.
func TestHTTPResponseBytesExcludesPostHijackBytes(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	compress := gzipCompression(t)

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	handshakeBody := "upgrade-handshake-bytes"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(handshakeBody))
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support Hijack through the chain")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("Hijack: %v", err)
		}
		go func() {
			_, _ = conn.Write([]byte("post-hijack-framing-not-counted-by-http-metric"))
			_ = conn.Close()
		}()
	})

	policy, err := clientaddr.NewPolicy(nil, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	chain := middleware.Chain(handler, f.globalChain(policy, compress, nil)...)

	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder(), conn: serverConn}
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	before := f.Metrics.Snapshot().HTTPResponseBytesTotal
	chain.ServeHTTP(rec, req)
	after := f.Metrics.Snapshot().HTTPResponseBytesTotal

	if delta := after - before; delta != float64(len(handshakeBody)) {
		t.Fatalf("HTTPResponseBytesTotal delta = %v, want exactly the pre-hijack handshake bytes (%d); post-hijack framing must not be counted", delta, len(handshakeBody))
	}
}
