// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/observability"
)

// newCache builds a response cache with the same defaults a `cache = true`
// location gets.
func newCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.New(config.CacheConfig{
		Enabled:              true,
		MemoryMaxSize:        config.Size(1 << 20),
		DefaultTTL:           config.Duration(time.Minute),
		StaleWhileRevalidate: config.Duration(time.Minute),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	if c == nil {
		t.Skip("cache not available in this build")
	}
	return c
}

// productionChain composes the middleware in the same order the handler factory
// uses, so a composition test exercises what actually runs: metrics and access
// log observe from outside, Recover converts panics, compression is innermost of
// the global chain, and the cache wraps the location action.
func productionChain(t *testing.T, c *cache.Cache, sinks []middleware.AccessSink, action http.Handler) http.Handler {
	t.Helper()
	compress, err := middleware.NewCompression(middleware.CompressionOptions{
		Encoders: []string{"gzip"},
		MinSize:  1,
		Types:    []string{"text/*", "application/json"},
	})
	if err != nil {
		t.Fatalf("NewCompression: %v", err)
	}
	metrics := observability.NewMetrics()

	mws := []middleware.Middleware{
		middleware.RequestID(),
		metrics.Middleware,
	}
	if len(sinks) > 0 {
		mws = append(mws, middleware.AccessLog(sinks...))
	}
	mws = append(mws, middleware.Recover(slog.New(slog.NewTextHandler(io.Discard, nil))), compress)
	return middleware.Chain(c.Handler(action), mws...)
}

// TestWebSocketThroughCachedProxy is the headline conformance test for #133: a
// real RFC 6455 handshake and real frames must survive a `cache = true` proxy
// route over a real (hijackable) server. Before the transparent wrapper the
// cache writer hid Hijack, so httputil.ReverseProxy could not switch protocols
// and the upgrade failed.
func TestWebSocketThroughCachedProxy(t *testing.T) {
	backend := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		for {
			var msg []byte
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				return
			}
			if err := websocket.Message.Send(ws, msg); err != nil {
				return
			}
		}
	}))
	defer backend.Close()

	c := newCache(t)
	front := httptest.NewServer(c.Handler(newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)))
	defer front.Close()

	wsURL := "ws" + strings.TrimPrefix(front.URL, "http")
	ws, err := websocket.Dial(wsURL, "", front.URL)
	if err != nil {
		t.Fatalf("WebSocket dial through a cached proxy route: %v", err)
	}
	defer ws.Close()

	for _, want := range []string{"hello", `{"type":"connection_init"}`, "subscription-data"} {
		if err := websocket.Message.Send(ws, []byte(want)); err != nil {
			t.Fatalf("send %q: %v", want, err)
		}
		var got []byte
		if err := websocket.Message.Receive(ws, &got); err != nil {
			t.Fatalf("receive after %q: %v", want, err)
		}
		if string(got) != want {
			t.Fatalf("echo = %q, want %q", got, want)
		}
	}

	binPayload := []byte{0x00, 0x01, 0x02, 0xfe, 0xff}
	if err := websocket.Message.Send(ws, binPayload); err != nil {
		t.Fatalf("send binary: %v", err)
	}
	var gotBin []byte
	if err := websocket.Message.Receive(ws, &gotBin); err != nil {
		t.Fatalf("receive binary: %v", err)
	}
	if !bytes.Equal(gotBin, binPayload) {
		t.Fatalf("binary echo = %v, want %v", gotBin, binPayload)
	}
}

// TestWebSocketThroughFullMiddlewareChain repeats the upgrade through the whole
// production chain. Each observer and transformer wraps the response writer, so
// this is what proves the capability survives composition and not just the cache
// in isolation.
func TestWebSocketThroughFullMiddlewareChain(t *testing.T) {
	backend := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		var msg []byte
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			return
		}
		_ = websocket.Message.Send(ws, append([]byte("echo:"), msg...))
	}))
	defer backend.Close()

	c := newCache(t)
	sink := &recordingSink{}
	chain := productionChain(t, c, []middleware.AccessSink{sink}, newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil))
	front := httptest.NewServer(chain)
	defer front.Close()

	ws, err := websocket.Dial("ws"+strings.TrimPrefix(front.URL, "http"), "", front.URL)
	if err != nil {
		t.Fatalf("WebSocket dial through the full chain: %v", err)
	}
	defer ws.Close()

	if err := websocket.Message.Send(ws, []byte("ping")); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got []byte
	if err := websocket.Message.Receive(ws, &got); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if string(got) != "echo:ping" {
		t.Fatalf("echo = %q, want echo:ping", got)
	}

	// The access log must record the upgrade exactly once, as a 101, and must
	// not double-finalize after the connection was handed over.
	_ = ws.Close()
	records := sink.wait(t, 1)
	if len(records) != 1 {
		t.Fatalf("access log records = %d, want 1", len(records))
	}
	if records[0].Status != http.StatusSwitchingProtocols {
		t.Fatalf("logged status = %d, want 101", records[0].Status)
	}
}

// TestUpgradeIsNotStoredByCachedProxy proves nothing about the upgrade enters
// either cache tier, including the disk tier's directory.
func TestUpgradeIsNotStoredByCachedProxy(t *testing.T) {
	backend := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		var msg []byte
		_ = websocket.Message.Receive(ws, &msg)
	}))
	defer backend.Close()

	dir := t.TempDir()
	c, err := cache.New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(1 << 20),
		DiskPath:      dir,
		DiskMaxSize:   config.Size(1 << 20),
		DefaultTTL:    config.Duration(time.Minute),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}

	front := httptest.NewServer(c.Handler(newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)))
	defer front.Close()

	ws, err := websocket.Dial("ws"+strings.TrimPrefix(front.URL, "http"), "", front.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = websocket.Message.Send(ws, []byte("bye"))
	_ = ws.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Fatalf("upgrade produced a disk cache file %q", filepath.Join(dir, e.Name()))
		}
	}
}

// TestRepeatedUpgradesThroughCachedProxy is the leak check: many upgrades in a
// row must not leave goroutines or connections behind, which is what a wrapper
// that finalizes a hijacked response would produce.
func TestRepeatedUpgradesThroughCachedProxy(t *testing.T) {
	backend := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		var msg []byte
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			return
		}
		_ = websocket.Message.Send(ws, msg)
	}))
	defer backend.Close()

	c := newCache(t)
	front := httptest.NewServer(c.Handler(newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)))
	defer front.Close()

	url := "ws" + strings.TrimPrefix(front.URL, "http")
	for i := 0; i < 8; i++ { // warm the pools before the baseline
		roundTrip(t, url, front.URL)
	}
	base := stableGoroutineCount()

	for i := 0; i < 100; i++ {
		roundTrip(t, url, front.URL)
	}
	end := stableGoroutineCount()

	if growth := end - base; growth > 16 {
		t.Fatalf("goroutine leak across upgrades: grew by %d (%d -> %d)", growth, base, end)
	}
}

func roundTrip(t *testing.T, wsURL, origin string) {
	t.Helper()
	ws, err := websocket.Dial(wsURL, "", origin)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = ws.Close() }()
	if err := websocket.Message.Send(ws, []byte("x")); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got []byte
	if err := websocket.Message.Receive(ws, &got); err != nil {
		t.Fatalf("receive: %v", err)
	}
}

// TestSSEThroughCachedProxyStreamsAndIsNotStored proves SSE keeps its
// event-by-event latency through the cache and is never turned into an entry.
// The backend withholds the second event until the client has the first, so a
// buffering chain dead-locks instead of quietly passing.
func TestSSEThroughCachedProxyStreamsAndIsNotStored(t *testing.T) {
	releaseSecond := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "max-age=60") // deliberately cacheable-looking
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: first\n\n")
		fl.Flush()
		<-releaseSecond
		_, _ = io.WriteString(w, "data: second\n\n")
		fl.Flush()
	}))
	defer backend.Close()

	c := newCache(t)
	front := httptest.NewServer(c.Handler(newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)))
	defer front.Close()

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	first := readEvent(t, br)
	if first != "data: first" {
		t.Fatalf("first event = %q", first)
	}
	close(releaseSecond)
	second := readEvent(t, br)
	if second != "data: second" {
		t.Fatalf("second event = %q", second)
	}
	_ = resp.Body.Close()

	// A second request must reach the origin again: the stream was not stored.
	resp2, err := http.DefaultClient.Get(front.URL + "/events")
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	defer resp2.Body.Close()
	if got := resp2.Header.Get("X-Cache"); got == "HIT" {
		t.Fatal("an event stream was served from cache")
	}
}

func readEvent(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := br.ReadString('\n')
		ch <- result{strings.TrimRight(line, "\r\n"), err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read event: %v", r.err)
		}
		// Consume the blank separator line.
		_, _ = br.ReadString('\n')
		return r.line
	case <-time.After(5 * time.Second):
		t.Fatal("event did not arrive; the chain is buffering instead of streaming")
		return ""
	}
}

// TestCachedProxyStillCachesOrdinaryResponses guards the composition against an
// over-eager bypass: the protocol work must not disable caching for normal
// traffic, including through compression and the observers.
func TestCachedProxyStillCachesOrdinaryResponses(t *testing.T) {
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer backend.Close()

	c := newCache(t)
	chain := productionChain(t, c, nil, newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil))
	front := httptest.NewServer(chain)
	defer front.Close()

	get := func() *http.Response {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/api", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}

	if got := get().Header.Get("X-Cache"); got != "MISS" {
		t.Fatalf("first X-Cache = %q, want MISS", got)
	}
	if got := get().Header.Get("X-Cache"); got != "HIT" {
		t.Fatalf("second X-Cache = %q, want HIT", got)
	}
	if calls != 1 {
		t.Fatalf("origin called %d times, want 1", calls)
	}
}

// TestPanicAfterHeadersThroughCachedChain proves Recover still converts a panic
// and the observers still see the request, with the cache in the chain.
func TestPanicAfterHeadersThroughCachedChain(t *testing.T) {
	c := newCache(t)
	sink := &recordingSink{}
	action := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial")
		panic("boom after headers")
	})
	front := httptest.NewServer(productionChain(t, c, []middleware.AccessSink{sink}, action))
	defer front.Close()

	resp, err := http.Get(front.URL + "/panic")
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	records := sink.wait(t, 1)
	if len(records) != 1 {
		t.Fatalf("access log records = %d, want 1", len(records))
	}
	if records[0].Status != http.StatusOK {
		t.Fatalf("logged status = %d, want 200 (headers were already sent)", records[0].Status)
	}
}

// recordingSink collects access-log records for assertions.
type recordingSink struct {
	mu      sync.Mutex
	records []middleware.AccessRecord
}

func (s *recordingSink) Log(r middleware.AccessRecord) {
	s.mu.Lock()
	s.records = append(s.records, r)
	s.mu.Unlock()
}

func (s *recordingSink) wait(t *testing.T, n int) []middleware.AccessRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		got := append([]middleware.AccessRecord(nil), s.records...)
		s.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// stableGoroutineCount returns the goroutine count once it stops moving, so a
// just-closed connection's teardown is not mistaken for a leak.
func stableGoroutineCount() int {
	prev := -1
	for i := 0; i < 200; i++ {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n == prev {
			return n
		}
		prev = n
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}
