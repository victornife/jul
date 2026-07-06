// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func newTestStore(t *testing.T) *RateLimiterStore {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewRateLimiterStore(ctx, time.Minute, time.Minute)
}

// storeSize counts live buckets across all shards (test-only helper).
func storeSize(s *RateLimiterStore) int {
	n := 0
	for i := range s.shards {
		s.shards[i].mu.Lock()
		n += len(s.shards[i].entries)
		s.shards[i].mu.Unlock()
	}
	return n
}

func TestRateLimiterAllowsBurstThenThrottles(t *testing.T) {
	store := newTestStore(t)
	lim := store.Scoped("test", 1, 5) // 1 rps, burst 5

	for i := 0; i < 5; i++ {
		if ok, _ := lim.Allow("k"); !ok {
			t.Fatalf("request %d within burst was rejected", i)
		}
	}
	ok, retry := lim.Allow("k")
	if ok {
		t.Fatal("request beyond burst should be rejected")
	}
	if retry <= 0 || retry > time.Second {
		t.Errorf("retryAfter = %v, want (0, 1s]", retry)
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	store := newTestStore(t)
	lim := store.Scoped("test", 1, 1)
	if ok, _ := lim.Allow("a"); !ok {
		t.Fatal("first key should be allowed")
	}
	if ok, _ := lim.Allow("b"); !ok {
		t.Fatal("independent key should be allowed")
	}
	if ok, _ := lim.Allow("a"); ok {
		t.Fatal("exhausted key should be rejected")
	}
}

func TestRateLimiterScopesDoNotCollide(t *testing.T) {
	store := newTestStore(t)
	g := store.Scoped("global", 1, 1)
	l := store.Scoped("loc", 1, 1)
	if ok, _ := g.Allow("k"); !ok {
		t.Fatal("global scope first call should be allowed")
	}
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("same key in a different scope must have its own bucket")
	}
}

// TestRateLimiterReloadUpdatesBucketParams verifies that re-applying a key with
// new rate/burst (as happens after a config reload) updates the existing bucket
// in place rather than keeping the old limits.
func TestRateLimiterReloadUpdatesBucketParams(t *testing.T) {
	store := newTestStore(t)
	store.Scoped("s", 1, 1).Allow("k")
	store.Scoped("s", 5, 9).Allow("k")

	sh := &store.shards[shardIndex("s\x00k")]
	sh.mu.Lock()
	e := sh.entries["s\x00k"]
	sh.mu.Unlock()
	if e == nil {
		t.Fatal("entry missing after reload")
	}
	if e.limit != rate.Limit(5) || e.burst != 9 {
		t.Errorf("cached params not updated: limit=%v burst=%d, want 5/9", e.limit, e.burst)
	}
	if got := e.lim.Limit(); got != rate.Limit(5) {
		t.Errorf("rate.Limiter limit = %v, want 5", got)
	}
	if got := e.lim.Burst(); got != 9 {
		t.Errorf("rate.Limiter burst = %d, want 9", got)
	}
}

func TestRateLimiterEviction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store := NewRateLimiterStore(ctx, time.Minute, time.Hour) // sweep rarely; call evict manually
	store.Scoped("s", 1, 1).Allow("k")

	store.evict(time.Now())
	if got := storeSize(store); got != 1 {
		t.Fatalf("entry evicted too early: size=%d", got)
	}
	store.evict(time.Now().Add(2 * time.Minute))
	if got := storeSize(store); got != 0 {
		t.Fatalf("idle entry not evicted: size=%d", got)
	}
}

func TestRateKeyFuncIP(t *testing.T) {
	key := RateKeyFunc("ip")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:54321"
	if got := key(r); got != "203.0.113.7" {
		t.Errorf("ip key = %q, want 203.0.113.7", got)
	}
}

func TestRateKeyFuncHeader(t *testing.T) {
	key := RateKeyFunc("header:X-Api-Key")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:1"
	r.Header.Set("X-Api-Key", "abc123")
	if got := key(r); got != "abc123" {
		t.Errorf("header key = %q, want abc123", got)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "203.0.113.7:1"
	if got := key(r2); got != "203.0.113.7" {
		t.Errorf("missing header should fall back to IP: got %q", got)
	}
}

func TestRateKeyFuncJWTFallsBackToIP(t *testing.T) {
	key := RateKeyFunc("jwt:sub")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.4:9"
	if got := key(r); got != "198.51.100.4" {
		t.Errorf("jwt key without claims should fall back to client IP: got %q", got)
	}
}

func TestRateKeyFuncJWTReadsClaim(t *testing.T) {
	key := RateKeyFunc("jwt:sub")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.4:9"
	r = r.WithContext(WithClaims(r.Context(), map[string]any{"sub": "user-42"}))
	if got := key(r); got != "user-42" {
		t.Errorf("jwt key = %q, want claim value user-42", got)
	}

	// A present but non-string claim falls back to the client IP.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "198.51.100.4:9"
	r2 = r2.WithContext(WithClaims(r2.Context(), map[string]any{"sub": 42}))
	if got := key(r2); got != "198.51.100.4" {
		t.Errorf("non-string claim should fall back to IP: got %q", got)
	}
}

func TestRateLimitMiddleware429WithRetryAfter(t *testing.T) {
	store := newTestStore(t)
	lim := store.Scoped("mw", 1, 1)
	var limited int
	mw := RateLimit(lim, RateKeyFunc("ip"), func() { limited++ })
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.10:1"

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, r)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rec2.Code)
	}
	if ra := rec2.Header().Get("Retry-After"); ra == "" {
		t.Error("missing Retry-After header on 429")
	} else if n, err := strconv.Atoi(ra); err != nil || n < 1 {
		t.Errorf("Retry-After = %q, want positive integer seconds", ra)
	}
	if limited != 1 {
		t.Errorf("onLimited called %d times, want 1", limited)
	}
}

func TestRateLimitNilLimiterPassesThrough(t *testing.T) {
	mw := RateLimit(nil, RateKeyFunc("ip"), nil)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("nil limiter should pass through to next handler")
	}
}

func TestRateLimiterConcurrentAccess(t *testing.T) {
	store := newTestStore(t)
	lim := store.Scoped("c", 1000, 1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "k" + strconv.Itoa(n%8)
			for j := 0; j < 100; j++ {
				lim.Allow(key)
			}
		}(i)
	}
	wg.Wait()
}
