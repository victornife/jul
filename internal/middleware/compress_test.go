// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gzipMiddleware(t *testing.T, opts CompressionOptions) Middleware {
	t.Helper()
	if len(opts.Encoders) == 0 {
		opts.Encoders = []string{"gzip"}
	}
	if opts.Types == nil {
		opts.Types = []string{"text/*", "application/json"}
	}
	mw, err := NewCompression(opts)
	if err != nil {
		t.Fatalf("NewCompression: %v", err)
	}
	return mw
}

func serveCompress(mw Middleware, h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mw(h).ServeHTTP(rec, r)
	return rec
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	return string(out)
}

func TestCompressNegotiation(t *testing.T) {
	body := strings.Repeat("hello world ", 100)
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 8})

	tests := []struct {
		name, accept, wantEnc string
	}{
		{"gzip", "gzip", "gzip"},
		{"identity only", "identity", ""},
		{"unknown only", "deflate", ""},
		{"q zero", "gzip;q=0", ""},
		{"wildcard", "*", "gzip"},
		{"no header", "", ""},
		{"with params", "gzip;q=0.5, deflate;q=1.0", "gzip"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			rec := serveCompress(mw, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				io.WriteString(w, body)
			}, req)

			if got := rec.Header().Get("Content-Encoding"); got != tc.wantEnc {
				t.Fatalf("Content-Encoding = %q, want %q", got, tc.wantEnc)
			}
			if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
				t.Fatalf("Vary must always include Accept-Encoding, got %q", rec.Header().Get("Vary"))
			}
			if tc.wantEnc == "gzip" {
				if got := gunzip(t, rec.Body.Bytes()); got != body {
					t.Fatalf("decoded body mismatch")
				}
				if rec.Header().Get("Content-Length") != "" {
					t.Fatalf("Content-Length must be stripped when compressing")
				}
			} else if rec.Body.String() != body {
				t.Fatalf("passthrough body mismatch")
			}
		})
	}
}

func TestCompressMinSize(t *testing.T) {
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 1024})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serveCompress(mw, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "small body below threshold")
	}, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("body below min_size must not be compressed")
	}
	if rec.Body.String() != "small body below threshold" {
		t.Fatalf("body mismatch: %q", rec.Body.String())
	}
}

func TestCompressMIMEGate(t *testing.T) {
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 8, Types: []string{"text/*"}})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serveCompress(mw, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, strings.Repeat("x", 1000))
	}, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("disallowed MIME type must not be compressed")
	}
}

func TestCompressNoDoubleEncode(t *testing.T) {
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 1})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serveCompress(mw, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "gzip")
		io.WriteString(w, "already-encoded")
	}, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Body.String() != "already-encoded" {
		t.Fatalf("pre-encoded body must pass through unchanged: %q", rec.Body.String())
	}
}

func TestCompressSkipsRange(t *testing.T) {
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 8})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-10")
	rec := serveCompress(mw, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, strings.Repeat("y", 1000))
	}, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Range requests must not be compressed")
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++; f.ResponseRecorder.Flush() }

func TestCompressSSEFlush(t *testing.T) {
	// Large min_size so only the flush forces a compression decision.
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 4096})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	fr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("compress writer must implement http.Flusher")
		}
		io.WriteString(w, "data: one\n\n")
		f.Flush()
		io.WriteString(w, "data: two\n\n")
		f.Flush()
	})).ServeHTTP(fr, req)

	if fr.flushes == 0 {
		t.Fatal("Flush did not propagate to the underlying writer")
	}
	if fr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("flushed SSE should be compressed, got %q", fr.Header().Get("Content-Encoding"))
	}
	got := gunzip(t, fr.Body.Bytes())
	if !strings.Contains(got, "data: one") || !strings.Contains(got, "data: two") {
		t.Fatalf("SSE content lost after compression: %q", got)
	}
}

type hijackRecorder struct {
	*httptest.ResponseRecorder
	conn     net.Conn
	hijacked bool
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

func TestCompressHijack(t *testing.T) {
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 8})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	hr := &hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: server}

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("compress writer must implement http.Hijacker")
		}
		c, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		if c != server {
			t.Fatal("hijack returned the wrong connection")
		}
	})).ServeHTTP(hr, req)

	if !hr.hijacked {
		t.Fatal("Hijack was not forwarded to the underlying writer")
	}
	if hr.Body.Len() != 0 {
		t.Fatalf("nothing should be written after hijack, got %d bytes", hr.Body.Len())
	}
}

func TestCompressUnknownEncoderErrors(t *testing.T) {
	_, err := NewCompression(CompressionOptions{
		Encoders: []string{"gzip", "nope"},
		Types:    []string{"text/*"},
	})
	if err == nil {
		t.Fatal("expected an error for an unregistered encoder")
	}
	if !strings.Contains(err.Error(), "not compiled in this build") {
		t.Fatalf("error = %q, want 'not compiled in this build'", err.Error())
	}
}

func TestGzipAlwaysAvailable(t *testing.T) {
	if !EncoderAvailable("gzip") {
		t.Fatal("gzip must always be available")
	}
}

func TestCompressEncoderReuse(t *testing.T) {
	// Sequential requests share one encoder pool, so every request after the
	// first reuses an encoder via Reset() following a prior Close(). A broken
	// reuse path would corrupt the second and later response bodies.
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 8})
	for i := 0; i < 3; i++ {
		body := strings.Repeat("reuse payload ", 50+i)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := serveCompress(mw, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, body)
		}, req)
		if rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("iter %d: response was not compressed", i)
		}
		if got := gunzip(t, rec.Body.Bytes()); got != body {
			t.Fatalf("iter %d: decoded body mismatch after pool reuse", i)
		}
	}
}

func TestCompressEmptyResponseHasVary(t *testing.T) {
	// A handler that writes neither a header nor a body must still emit Vary so
	// caches key the (compressible) resource correctly.
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 8})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serveCompress(mw, func(w http.ResponseWriter, r *http.Request) {}, req)
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("empty response must still carry Vary: Accept-Encoding, got %q", rec.Header().Get("Vary"))
	}
}

// TestCompressInterimResponseDoesNotLatchTheFinalStatus is the regression test
// for #331: a 103 Early Hints ahead of a body-less final status must reach the
// client as that final status, not as an implicit 200.
func TestCompressInterimResponseDoesNotLatchTheFinalStatus(t *testing.T) {
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 1})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	sink := newClientSink()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</style.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusEarlyHints) // multiple interim responses are permitted
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(sink, req)

	want := []int{http.StatusEarlyHints, http.StatusEarlyHints, http.StatusNoContent}
	if !slicesEqual(sink.statuses, want) {
		t.Fatalf("statuses forwarded to the client = %v, want %v", sink.statuses, want)
	}
	if got := sink.finalStatus(); got != http.StatusNoContent {
		t.Fatalf("status delivered to the client = %d, want 204", got)
	}
}

// TestCompressInterimThenImplicitOKStillCompresses proves the compression
// decision is made against the final status and headers, not the 1xx that
// preceded them.
func TestCompressInterimThenImplicitOKStillCompresses(t *testing.T) {
	mw := gzipMiddleware(t, CompressionOptions{MinSize: 1})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	sink := newClientSink()
	body := strings.Repeat("z", 100)

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</style.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, body) // no explicit WriteHeader: implicit 200
	})).ServeHTTP(sink, req)

	want := []int{http.StatusEarlyHints, http.StatusOK}
	if !slicesEqual(sink.statuses, want) {
		t.Fatalf("statuses forwarded to the client = %v, want %v", sink.statuses, want)
	}
	if sink.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", sink.Header().Get("Content-Encoding"))
	}
	if got := gunzip(t, sink.body.Bytes()); got != body {
		t.Fatalf("decoded body = %q, want %q", got, body)
	}
}

// TestCompressWriter101StillPassesThroughImmediately proves 101 keeps its
// existing treatment: bodyAllowed(101) is false, so it starts pass-through on
// the spot exactly as it did before interim responses were handled specially.
func TestCompressWriter101StillPassesThroughImmediately(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	sink := newClientSink()
	c := &compression{mime: newMimeMatcher([]string{"text/*"})}
	cw := &compressWriter{ResponseWriter: sink, c: c, r: req}

	cw.WriteHeader(http.StatusSwitchingProtocols)

	if !cw.decided || cw.enc != nil {
		t.Fatal("101 must start pass-through immediately, as before")
	}
	if len(sink.statuses) != 1 || sink.statuses[0] != http.StatusSwitchingProtocols {
		t.Fatalf("statuses forwarded to the client = %v, want [101]", sink.statuses)
	}
}

func TestParseAcceptEncoding(t *testing.T) {
	q := parseAcceptEncoding("gzip, br;q=0.8, *;q=0.1")
	if q["gzip"] != 1.0 {
		t.Errorf("gzip q = %v, want 1.0", q["gzip"])
	}
	if q["br"] != 0.8 {
		t.Errorf("br q = %v, want 0.8", q["br"])
	}
	if q["*"] != 0.1 {
		t.Errorf("* q = %v, want 0.1", q["*"])
	}
}

func TestNegotiatePreference(t *testing.T) {
	c := &compression{pools: []*encoderPool{{name: "br"}, {name: "gzip"}}}
	req := func(ae string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", ae)
		return r
	}
	if ep := c.negotiate(req("gzip, br")); ep == nil || ep.name != "br" {
		t.Fatalf("equal q should pick server-preferred br, got %v", epName(ep))
	}
	if ep := c.negotiate(req("gzip;q=1.0, br;q=0.5")); ep == nil || ep.name != "gzip" {
		t.Fatalf("higher client q should win (gzip), got %v", epName(ep))
	}
	if ep := c.negotiate(req("identity")); ep != nil {
		t.Fatalf("no acceptable coding should return nil, got %v", epName(ep))
	}
}

func epName(ep *encoderPool) string {
	if ep == nil {
		return "<nil>"
	}
	return ep.name
}

func TestMIMEMatcher(t *testing.T) {
	m := newMimeMatcher([]string{"text/*", "application/json"})
	cases := map[string]bool{
		"text/html; charset=utf-8": true,
		"text/plain":               true,
		"application/json":         true,
		"application/octet-stream": false,
		"image/png":                false,
	}
	for ct, want := range cases {
		if got := m.match(ct); got != want {
			t.Errorf("match(%q) = %v, want %v", ct, got, want)
		}
	}
	if !newMimeMatcher([]string{"*"}).match("anything/here") {
		t.Error("wildcard should match all types")
	}
}
