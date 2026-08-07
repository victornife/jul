// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"jul/internal/config"
)

// benchCache creates a cache with generous limits so benchmarks measure the
// lookup/store logic, not eviction.
func benchCache() *Cache {
	c, _ := New(config.CacheConfig{
		Enabled:              true,
		MemoryMaxSize:        config.Size(64 << 20),
		DefaultTTL:           config.Duration(5 * time.Minute),
		StaleIfError:         config.Duration(5 * time.Minute),
		StaleWhileRevalidate: config.Duration(30 * time.Second),
	}, nil)
	return c
}

// benchNext is a deterministic upstream that returns a small cacheable response.
var benchNext = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "max-age=300")
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("hello"))
})

func BenchmarkCacheHit(b *testing.B) {
	c := benchCache()
	h := c.Handler(benchNext)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/page", nil)
	// Prime the cache.
	h.ServeHTTP(httptest.NewRecorder(), req)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != "HIT" {
			b.Fatal("expected HIT")
		}
	}
}

func BenchmarkCacheMiss(b *testing.B) {
	c := benchCache()
	h := c.Handler(benchNext)
	// Use a unique URL per iteration to force misses.
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/miss?id="+strconv.Itoa(i), nil)
		h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != "MISS" {
			b.Fatal("expected MISS")
		}
	}
}

func BenchmarkCacheVaryHit(b *testing.B) {
	c := benchCache()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=300")
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write([]byte(r.Header.Get("Accept-Encoding")))
	})
	h := c.Handler(next)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/data", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	// Prime the cache.
	h.ServeHTTP(httptest.NewRecorder(), req)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != "HIT" {
			b.Fatal("expected HIT")
		}
	}
}

func BenchmarkCacheMemOverflow(b *testing.B) {
	// A tiny memory cap forces every store to trigger eviction + disk overflow.
	c, _ := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(512),
		DiskPath:      b.TempDir(),
		DiskMaxSize:   config.Size(64 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	h := c.Handler(benchNext)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/big?id="+strconv.Itoa(i), nil)
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkCacheMandatoryValidation304 measures the synchronous no-cache path:
// every request reaches the origin, receives a 304, merges metadata into a new
// immutable entry, and serves the stored body as REVALIDATED.
func BenchmarkCacheMandatoryValidation304(b *testing.B) {
	c := benchCache()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"bench-v1"`)
		if r.Header.Get("If-None-Match") == `"bench-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("validated body"))
	})
	h := c.Handler(next)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/validated", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != stateRevalidated {
			b.Fatal("expected REVALIDATED")
		}
	}
}

// BenchmarkCacheVariantInvalidation measures removal of a populated Vary
// membership set. Setup is excluded so the result reflects the invalidation
// path rather than response publication.
func BenchmarkCacheVariantInvalidation(b *testing.B) {
	c := benchCache()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=300")
		w.Header().Set("Vary", "Accept-Language")
		_, _ = w.Write([]byte(r.Header.Get("Accept-Language")))
	})
	h := c.Handler(next)
	base := httptest.NewRequest(http.MethodGet, "http://example.com/vary", nil)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for variant := 0; variant < 32; variant++ {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/vary", nil)
			req.Header.Set("Accept-Language", "lang-"+strconv.Itoa(variant))
			h.ServeHTTP(httptest.NewRecorder(), req)
		}
		b.StartTimer()
		c.Delete(key(base))
	}
}
