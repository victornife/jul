// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestSensitiveVaryCommitted304CannotBeHidden(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	var calls int
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "public, no-cache")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Vary", "Cookie")
			w.WriteHeader(http.StatusNotModified)
			w.Header().Del("Vary")
			return
		}
		if calls == 1 {
			_, _ = w.Write([]byte("old-body"))
		} else {
			_, _ = w.Write([]byte("new-body"))
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "new-body" || rec.Header().Get("X-Cache") == stateRevalidated || calls != 3 {
		t.Fatal("a committed sensitive Vary 304 authorized the old body")
	}
}

func TestSensitiveVaryCommittedBackground304CannotBeHidden(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 1)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil), group)
	old := staleEntry("old-body")
	old.ETag = `"fixture"`
	c.set(key(req), old)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Vary", "Cookie")
			w.WriteHeader(http.StatusNotModified)
			w.Header().Del("Vary")
			return
		}
		_, _ = w.Write([]byte("new-body"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitDrained(t, c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "new-body" {
		t.Fatal("background 304 restored an entry with committed sensitive Vary")
	}
}

func TestSensitiveVaryValidationCancelsAndReleasesFollowers(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	entered, releaseHeaders := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "public, no-cache")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			close(entered)
			<-releaseHeaders
			w.Header().Set("Vary", "Cookie")
			w.WriteHeader(http.StatusOK)
			<-r.Context().Done()
			return
		}
		if n == 1 {
			_, _ = w.Write([]byte("old-body"))
			return
		}
		_, _ = w.Write([]byte(r.Header.Get("X-Fixture-Identity")))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	var wg sync.WaitGroup
	for i, identity := range []string{"fixture-alice", "fixture-bob"} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil).WithContext(ctx)
		req.Header.Set("X-Fixture-Identity", identity)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.ServeHTTP(recs[i], req)
		}(i)
		if i == 0 {
			<-entered
		}
	}
	awaitJoined(t, c, revalidateKey{key: key(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))}, 1)
	close(releaseHeaders)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("rejected validation did not cancel and release followers promptly")
	}
	if recs[0].Body.String() != "fixture-alice" || recs[1].Body.String() != "fixture-bob" {
		t.Fatal("validation fallback did not fetch independent original requests")
	}
}
