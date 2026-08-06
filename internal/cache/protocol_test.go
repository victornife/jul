// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

// hijackableWriter is an HTTP/1.1-shaped writer: it can flush and it can hand
// over the connection.
type hijackableWriter struct {
	*httptest.ResponseRecorder
	conn     net.Conn
	hijacked bool
	err      error
}

func (h *hijackableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h.err != nil {
		return nil, nil, h.err
	}
	h.hijacked = true
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

// plainWriter is an HTTP/2-shaped writer: it can flush but cannot be hijacked.
type plainWriter struct{ *httptest.ResponseRecorder }

// noCapWriter implements nothing optional at all.
type noCapWriter struct{ http.ResponseWriter }

func upgradeRequest(url string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return r
}

// TestUpgradeRequestBypassesCache proves the primary contract: a protocol
// upgrade reaches the handler with the untouched writer, is marked BYPASS, and
// neither reads nor writes the cache.
func TestUpgradeRequestBypassesCache(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})

	var gotWriter http.ResponseWriter
	var calls int
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		gotWriter = w
		w.Header().Set("Cache-Control", "max-age=60")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))

	r := upgradeRequest("http://x/ws")
	// A fresh entry exists for this key; the upgrade must not be served from it.
	c.set(key(r), &Entry{
		Status:     200,
		Header:     http.Header{"Cache-Control": {"max-age=60"}},
		Body:       []byte("cached"),
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
		StaleUntil: time.Now().Add(time.Hour),
	})

	under := &plainWriter{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(under, r)

	if calls != 1 {
		t.Fatalf("handler called %d times, want 1 (an upgrade must never be served from cache)", calls)
	}
	if gotWriter != http.ResponseWriter(under) {
		t.Fatalf("handler received %T, want the untouched underlying writer", gotWriter)
	}
	if got := under.Header().Get("X-Cache"); got != "BYPASS" {
		t.Fatalf("X-Cache = %q, want BYPASS", got)
	}
	if under.Body.Len() != 0 {
		t.Fatalf("cached body was served for an upgrade: %q", under.Body.String())
	}

	// The 101 must not have replaced the stored entry either.
	e, ok := c.get(key(r))
	if !ok || string(e.Body) != "cached" {
		t.Fatalf("stored entry after upgrade = %+v; the upgrade must not touch the cache", e)
	}
}

// TestUpgradeDetectionRequiresBothHeaders proves the bypass is not a hole: an
// Upgrade header alone, which a client can always send, must not disable
// caching for an ordinary request.
func TestUpgradeDetectionRequiresBothHeaders(t *testing.T) {
	cases := []struct {
		name       string
		connection string
		upgrade    string
		want       bool
	}{
		{"websocket", "Upgrade", "websocket", true},
		{"case insensitive", "upgrade", "WebSocket", true},
		{"token in list", "keep-alive, Upgrade", "h2c", true},
		{"upgrade only", "", "websocket", false},
		{"connection only", "Upgrade", "", false},
		{"unrelated connection token", "keep-alive", "websocket", false},
		{"neither", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
			if tc.connection != "" {
				r.Header.Set("Connection", tc.connection)
			}
			if tc.upgrade != "" {
				r.Header.Set("Upgrade", tc.upgrade)
			}
			if got := isUpgradeRequest(r); got != tc.want {
				t.Fatalf("isUpgradeRequest = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUpgradeHeaderWithoutConnectionIsCachedNormally is the behavioural half of
// the previous test: a stray Upgrade header must not turn a cacheable response
// into a permanent bypass.
func TestUpgradeHeaderWithoutConnectionIsCachedNormally(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	var calls int
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("v1"))
	}))

	do := func() string {
		r := httptest.NewRequest(http.MethodGet, "http://x/stray", nil)
		r.Header.Set("Upgrade", "websocket") // no Connection: Upgrade
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Header().Get("X-Cache")
	}

	if got := do(); got != "MISS" {
		t.Fatalf("first X-Cache = %q, want MISS", got)
	}
	if got := do(); got != "HIT" {
		t.Fatalf("second X-Cache = %q, want HIT", got)
	}
	if calls != 1 {
		t.Fatalf("origin called %d times, want 1", calls)
	}
}

// TestCacheWriterMirrorsUnderlyingCapabilities proves the cache neither removes
// nor invents optional interfaces. The HTTP/2 row is the important one: telling
// a handler it can hijack an HTTP/2 stream is exactly the false-positive the
// wrapper must not produce.
func TestCacheWriterMirrorsUnderlyingCapabilities(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = server.Close(); _ = client.Close() }()

	cases := []struct {
		name       string
		under      func() http.ResponseWriter
		wantFlush  bool
		wantHijack bool
	}{
		{
			name: "http/1.1 (flush + hijack)",
			under: func() http.ResponseWriter {
				return &hijackableWriter{ResponseRecorder: httptest.NewRecorder(), conn: server}
			},
			wantFlush:  true,
			wantHijack: true,
		},
		{
			name:       "http/2 (flush only)",
			under:      func() http.ResponseWriter { return &plainWriter{ResponseRecorder: httptest.NewRecorder()} },
			wantFlush:  true,
			wantHijack: false,
		},
		{
			name:       "no optional interfaces",
			under:      func() http.ResponseWriter { return &noCapWriter{httptest.NewRecorder()} },
			wantFlush:  false,
			wantHijack: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
			var gotFlush, gotHijack bool
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, gotFlush = w.(http.Flusher)
				_, gotHijack = w.(http.Hijacker)
				w.Header().Set("Cache-Control", "max-age=60")
				_, _ = w.Write([]byte("body"))
			}))

			h.ServeHTTP(tc.under(), httptest.NewRequest(http.MethodGet, "http://x/caps", nil))

			if gotFlush != tc.wantFlush {
				t.Errorf("http.Flusher = %v, want %v", gotFlush, tc.wantFlush)
			}
			if gotHijack != tc.wantHijack {
				t.Errorf("http.Hijacker = %v, want %v", gotHijack, tc.wantHijack)
			}
		})
	}
}

// TestResponseControllerReachesUnderlyingWriter proves the Unwrap chain works,
// which is how httputil.ReverseProxy finds Hijack and how the standard library
// reaches capabilities with no classic interface.
func TestResponseControllerReachesUnderlyingWriter(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = server.Close(); _ = client.Close() }()

	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	under := &hijackableWriter{ResponseRecorder: httptest.NewRecorder(), conn: server}

	var hijackErr error
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _, hijackErr = http.NewResponseController(w).Hijack()
	}))
	h.ServeHTTP(under, httptest.NewRequest(http.MethodGet, "http://x/rc", nil))

	if hijackErr != nil {
		t.Fatalf("ResponseController.Hijack through the cache writer: %v", hijackErr)
	}
	if !under.hijacked {
		t.Fatal("hijack did not reach the underlying writer")
	}
}

// TestResponseControllerReportsUnsupportedOnHTTP2Shape is the negative case: the
// cache must not turn "cannot hijack" into a silent success.
func TestResponseControllerReportsUnsupportedOnHTTP2Shape(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	under := &plainWriter{ResponseRecorder: httptest.NewRecorder()}

	var hijackErr error
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _, hijackErr = http.NewResponseController(w).Hijack()
	}))
	h.ServeHTTP(under, httptest.NewRequest(http.MethodGet, "http://x/rc2", nil))

	if !errors.Is(hijackErr, http.ErrNotSupported) {
		t.Fatalf("Hijack error = %v, want http.ErrNotSupported", hijackErr)
	}
}

// TestHijackDropsCaptureAndRefusesWrites proves the post-handoff contract: once
// the handler owns the connection the wrapper stores nothing and writes nothing.
func TestHijackDropsCaptureAndRefusesWrites(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = server.Close(); _ = client.Close() }()

	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	under := &hijackableWriter{ResponseRecorder: httptest.NewRecorder(), conn: server}

	var writeErr error
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("before-hijack"))
		if _, _, err := w.(http.Hijacker).Hijack(); err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		// A handler that keeps writing after the handoff must not reach the
		// connection the client now owns.
		_, writeErr = w.Write([]byte("after-hijack"))
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))

	r := httptest.NewRequest(http.MethodGet, "http://x/hijack", nil)
	h.ServeHTTP(under, r)

	if !errors.Is(writeErr, http.ErrHijacked) {
		t.Fatalf("post-hijack Write error = %v, want http.ErrHijacked", writeErr)
	}
	if got := under.Body.String(); got != "before-hijack" {
		t.Fatalf("underlying body = %q, want only the pre-hijack bytes", got)
	}
	if _, ok := c.get(key(r)); ok {
		t.Fatal("a hijacked response was stored")
	}
}

// TestFailedHijackKeepsWriterUsable proves the wrapper does not mark itself
// hijacked when the handoff failed.
func TestFailedHijackKeepsWriterUsable(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	under := &hijackableWriter{ResponseRecorder: httptest.NewRecorder(), err: errors.New("no")}

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, _, err := w.(http.Hijacker).Hijack(); err == nil {
			t.Error("expected the hijack to fail")
		}
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("still-served"))
	}))

	r := httptest.NewRequest(http.MethodGet, "http://x/failedhijack", nil)
	h.ServeHTTP(under, r)

	if got := under.Body.String(); got != "still-served" {
		t.Fatalf("body = %q, want still-served", got)
	}
	if _, ok := c.get(key(r)); !ok {
		t.Fatal("a normally completed response was not stored after a failed hijack")
	}
}

// TestProtocolSwitchResponseNeverStored proves rule 4 of the contract even when
// the request carried no upgrade headers, so the bypass never ran.
func TestProtocolSwitchResponseNeverStored(t *testing.T) {
	for _, code := range []int{http.StatusContinue, http.StatusSwitchingProtocols, http.StatusProcessing} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "max-age=60")
				w.WriteHeader(code)
				_, _ = w.Write([]byte("interim"))
			}))

			r := httptest.NewRequest(http.MethodGet, "http://x/switch", nil)
			h.ServeHTTP(httptest.NewRecorder(), r)

			if _, ok := c.get(key(r)); ok {
				t.Fatalf("a %d response was stored", code)
			}
		})
	}
}

// TestEventStreamIsNeverStoredOrBuffered proves the streaming policy: an SSE
// response is served with its flushes intact but is never stored.
func TestEventStreamIsNeverStoredOrBuffered(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "max-age=60") // deliberately cacheable-looking
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for i := 0; i < 50; i++ {
			_, _ = io.WriteString(w, "data: event\n\n")
			f.Flush()
		}
	}))

	under := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://x/sse", nil)
	h.ServeHTTP(under, r)

	if _, ok := c.get(key(r)); ok {
		t.Fatal("an event stream was stored")
	}
	if n := strings.Count(under.Body.String(), "data: event"); n != 50 {
		t.Fatalf("client received %d events, want 50", n)
	}
	if !under.Flushed {
		t.Fatal("flushes did not reach the client")
	}
}

// TestCaptureBufferStaysEmptyForStreams asserts the memory half of the policy
// directly on the capture writer: neither an event stream nor an oversized body
// may accumulate bytes that will only be discarded.
func TestCaptureBufferStaysEmptyForStreams(t *testing.T) {
	t.Run("event stream", func(t *testing.T) {
		cw := &cacheWriter{ResponseWriter: httptest.NewRecorder(), limit: 1 << 20}
		cw.Header().Set("Content-Type", "text/event-stream")
		cw.WriteHeader(http.StatusOK)
		for i := 0; i < 1000; i++ {
			_, _ = io.WriteString(cw, "data: event\n\n")
		}
		if cw.buf.Len() != 0 {
			t.Fatalf("capture buffer held %d bytes for an event stream, want 0", cw.buf.Len())
		}
		if cw.storable() {
			t.Fatal("an event stream was reported storable")
		}
	})

	t.Run("over the size limit", func(t *testing.T) {
		cw := &cacheWriter{ResponseWriter: httptest.NewRecorder(), limit: 512}
		cw.WriteHeader(http.StatusOK)
		for i := 0; i < 100; i++ {
			_, _ = cw.Write(make([]byte, 64))
		}
		if cw.buf.Len() != 0 {
			t.Fatalf("capture buffer held %d bytes past the limit, want 0", cw.buf.Len())
		}
		if cw.storable() {
			t.Fatal("an oversized response was reported storable")
		}
	})
}

// TestFlushedChunkedResponseIsStillCached is the regression guard for the
// streaming decision. The standard reverse proxy flushes on every write of any
// response with an unknown Content-Length, so "a flush makes it uncacheable"
// would silently stop caching ordinary chunked responses.
func TestFlushedChunkedResponseIsStillCached(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	var calls int
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Content-Type", "application/json")
		f := w.(http.Flusher)
		for _, chunk := range []string{`{"a":`, `1}`} {
			_, _ = io.WriteString(w, chunk)
			f.Flush()
		}
	}))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "http://x/chunked", nil))
	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "http://x/chunked", nil))

	if got := second.Header().Get("X-Cache"); got != "HIT" {
		t.Fatalf("second X-Cache = %q, want HIT (a flushed chunked response must stay cacheable)", got)
	}
	if second.Body.String() != `{"a":1}` {
		t.Fatalf("cached body = %q", second.Body.String())
	}
	if calls != 1 {
		t.Fatalf("origin called %d times, want 1", calls)
	}
}

// TestOversizedStreamIsNotStored proves the existing size bound still applies to
// a flushed non-SSE stream.
func TestOversizedStreamIsNotStored(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(512)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		f := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			_, _ = w.Write(make([]byte, 64))
			f.Flush()
		}
	}))

	r := httptest.NewRequest(http.MethodGet, "http://x/big", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)

	if _, ok := c.get(key(r)); ok {
		t.Fatal("an oversized response was stored")
	}
}

// TestRevalidationNeverStoresProtocolSwitch closes the loop with #131: the
// background revalidation path uses its own in-memory writer, and a 101 arriving
// there must not become a cache entry either.
func TestRevalidationNeverStoresProtocolSwitch(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	g := testLease(t, 1)

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))

	r := leased(httptest.NewRequest(http.MethodGet, "http://x/reval101", nil), g)
	seeded := staleEntry("stale")
	c.set(key(r), seeded)

	h.ServeHTTP(httptest.NewRecorder(), r)
	waitDrained(t, c)

	e, ok := c.get(key(r))
	if !ok {
		t.Fatal("the stale entry disappeared")
	}
	if string(e.Body) != "stale" || e.Status != 200 {
		t.Fatalf("a 101 revalidation replaced the entry: %+v", e)
	}
}
