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

func BenchmarkCacheColdMissFillLockCoalesced(b *testing.B) {
	c, _ := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(64 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		Lock:          true,
		LockTimeout:   config.Duration(5 * time.Second),
	}, nil)
	h := c.Handler(benchNext)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://example.com/coalesced-hot", nil)
			h.ServeHTTP(rec, req)
		}
	})
}

// BenchmarkCacheLargeDiskCapture measures streaming capture of a 4 MiB object
// larger than the 64 KiB memory tier into the disk tier, proving heap
// allocations stay O(1) rather than proportional to the 4 MiB body (#525 Part 2).
func BenchmarkCacheLargeDiskCapture(b *testing.B) {
	const bodySize = 4 << 20 // 4 MiB
	chunk := make([]byte, 32<<10)
	c, _ := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(64 << 10),
		DiskPath:      b.TempDir(),
		DiskMaxSize:   config.Size(64 << 20),
		MaxEntrySize:  config.Size(8 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Length", strconv.Itoa(bodySize))
		w.WriteHeader(http.StatusOK)
		for written := 0; written < bodySize; written += len(chunk) {
			_, _ = w.Write(chunk)
		}
	}))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := newDiscardResponseWriter()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/large-capture?id="+strconv.Itoa(i%4), nil)
		c.Delete(key(req))
		h.ServeHTTP(w, req)
		if w.written != bodySize || w.Header().Get("X-Cache") != stateMiss {
			b.Fatalf("written=%d state=%q", w.written, w.Header().Get("X-Cache"))
		}
	}
}

// BenchmarkCacheLargeDiskHit measures serving a warm 4 MiB disk-backed cache
// hit, proving heap allocations stay O(1) without loading the 4 MiB body into
// memory (#525 Part 2).
func BenchmarkCacheLargeDiskHit(b *testing.B) {
	const bodySize = 4 << 20 // 4 MiB
	chunk := make([]byte, 32<<10)
	c, _ := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(64 << 10),
		DiskPath:      b.TempDir(),
		DiskMaxSize:   config.Size(64 << 20),
		MaxEntrySize:  config.Size(8 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Length", strconv.Itoa(bodySize))
		w.WriteHeader(http.StatusOK)
		for written := 0; written < bodySize; written += len(chunk) {
			_, _ = w.Write(chunk)
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.com/large-warm", nil)
	h.ServeHTTP(newDiscardResponseWriter(), req)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := newDiscardResponseWriter()
		h.ServeHTTP(w, req)
		if w.written != bodySize || w.Header().Get("X-Cache") != stateHit {
			b.Fatalf("written=%d state=%q", w.written, w.Header().Get("X-Cache"))
		}
	}
}
