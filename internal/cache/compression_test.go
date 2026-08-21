// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/middleware"
)

// gzipMW builds a compression middleware compressing text/plain regardless of
// size, standing in for the real pipeline where compression sits outside the
// router and therefore outside the cache (HandlerFactory.globalChain appends
// it last, while withCache is applied deep inside by the action builders).
func gzipMW(t *testing.T) middleware.Middleware {
	t.Helper()
	mw, err := middleware.NewCompression(middleware.CompressionOptions{
		Encoders: []string{"gzip"},
		Types:    []string{"text/plain"},
		MinSize:  1,
	})
	if err != nil {
		t.Fatalf("NewCompression: %v", err)
	}
	return mw
}

func gunzip(t *testing.T, b []byte) []byte {
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
	return out
}

// TestCacheCompressionRoundTrip is #326: a cache = true location whose
// response is compressible must never pair a Content-Encoding an outer
// compression layer set with the uncompressed body the cache buffered. It
// fails against the pre-fix code (a hit's body is plain bytes under
// Content-Encoding: gzip) and passes once cacheWriter stores a header
// snapshot taken before compression, so every response — hit or miss — is
// compressed fresh from the plain representation the cache actually holds.
func TestCacheCompressionRoundTrip(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	body := strings.Repeat("hello world ", 200)
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write([]byte(body))
	}}
	h := gzipMW(t)(c.Handler(origin))

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		return r
	}

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, newReq())
	if got := rec1.Header().Get("X-Cache"); got != stateMiss {
		t.Fatalf("first request X-Cache = %q, want %q", got, stateMiss)
	}
	if got := rec1.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("first request Content-Encoding = %q, want gzip", got)
	}
	if got := gunzip(t, rec1.Body.Bytes()); string(got) != body {
		t.Fatalf("first request body decoded = %q, want %q", got, body)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, newReq())
	if got := rec2.Header().Get("X-Cache"); got != stateHit {
		t.Fatalf("second request X-Cache = %q, want %q", got, stateHit)
	}
	if got := rec2.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("second request (cache hit) Content-Encoding = %q, want gzip", got)
	}
	if got := gunzip(t, rec2.Body.Bytes()); string(got) != body {
		t.Fatalf("second request (cache hit) body decoded = %q, want %q", got, body)
	}
}

// TestCacheEntryExcludesCompressionHeaders proves the stored entry carries
// only what the origin produced: never the Content-Encoding, Content-Length,
// Accept-Ranges or Vary an outer compression layer adds after the cache's own
// WriteHeader has already snapshotted the response.
func TestCacheEntryExcludesCompressionHeaders(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	body := strings.Repeat("hello world ", 200)
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write([]byte(body))
	}}
	h := gzipMW(t)(c.Handler(origin))

	r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(httptest.NewRecorder(), r)

	e, ok := c.get(key(r))
	if !ok {
		t.Fatal("entry not stored")
	}
	if got := e.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("stored Content-Encoding = %q, want empty", got)
	}
	if got := e.Header.Get("Vary"); got != "" {
		t.Errorf("stored Vary = %q, want empty (compressor-added, not origin-set)", got)
	}
	if got := e.Header.Get("Accept-Ranges"); got != "" {
		t.Errorf("stored Accept-Ranges = %q, want empty", got)
	}
	if !bytes.Equal(e.Body, []byte(body)) {
		t.Errorf("stored body = %q, want plain origin body", e.Body)
	}
}

// TestCacheCompressionPassThrough proves a non-compressible response (below
// the compressor's MIME allow-list) still stores and serves correctly when
// composed with compression, so the fix does not regress the ordinary case.
func TestCacheCompressionPassThrough(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write([]byte("not-really-a-png"))
	}}
	h := gzipMW(t)(c.Handler(origin))

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		return r
	}

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, newReq())
	if got := rec1.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("first request Content-Encoding = %q, want empty", got)
	}
	if got := rec1.Body.String(); got != "not-really-a-png" {
		t.Fatalf("first request body = %q", got)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, newReq())
	if got := rec2.Header().Get("X-Cache"); got != stateHit {
		t.Fatalf("second request X-Cache = %q, want %q", got, stateHit)
	}
	if got := rec2.Body.String(); got != "not-really-a-png" {
		t.Fatalf("second request (cache hit) body = %q", got)
	}
}
