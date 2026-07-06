// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// BenchmarkRateLimiterAllow measures the cost of a single Allow call on an
// existing bucket (hot path after the first request for a key).
func BenchmarkRateLimiterAllow(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := NewRateLimiterStore(ctx, 10*time.Minute, time.Minute)
	lim := store.Scoped("bench", 1000, 1000)
	// Warm up the bucket so the benchmark measures the hot path.
	lim.Allow("k")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lim.Allow("k")
	}
}

// BenchmarkRateLimiterAllowParallel measures contention under concurrent load
// with distinct keys (many clients, no shard collision).
func BenchmarkRateLimiterAllowParallel(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := NewRateLimiterStore(ctx, 10*time.Minute, time.Minute)
	lim := store.Scoped("bench", 10000, 10000)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			lim.Allow(string(rune('a' + i%26)))
		}
	})
}

// BenchmarkRateLimiterAllowParallelContention measures the worst-case: many
// goroutines hitting the same key (same shard lock, same bucket).
func BenchmarkRateLimiterAllowParallelContention(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := NewRateLimiterStore(ctx, 10*time.Minute, time.Minute)
	lim := store.Scoped("bench", 100000, 100000)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lim.Allow("same-key")
		}
	})
}

// BenchmarkRateLimitMiddleware measures the full middleware path: key
// extraction + Allow + 200/response.  Uses the IP key func.
func BenchmarkRateLimitMiddleware(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := NewRateLimiterStore(ctx, 10*time.Minute, time.Minute)
	lim := store.Scoped("mw", 100000, 100000)
	mw := RateLimit(lim, RateKeyFunc("ip"), nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "203.0.113.1:12345"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}
}
