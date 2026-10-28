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

func TestSensitiveVaryErrorCancelsAndPreservesStaleIfError(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	entered, releaseHeaders := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") != "" {
			close(entered)
			<-releaseHeaders
			w.Header().Set("Vary", "Cookie")
			w.WriteHeader(http.StatusServiceUnavailable)
			<-r.Context().Done()
			return
		}
		w.Header().Set("Cache-Control", "public, no-cache, stale-if-error=60")
		w.Header().Set("ETag", `"fixture"`)
		_, _ = w.Write([]byte("old-body"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	var wg sync.WaitGroup
	for i := range recs {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil).WithContext(ctx)
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
		t.Fatal("sensitive 5xx validation did not cancel promptly")
	}
	for _, rec := range recs {
		if rec.Header().Get("X-Cache") != stateStale || rec.Body.String() != "old-body" {
			t.Fatal("explicit stale-if-error was not preserved")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("origin calls = %d, want warm-up and one validation", calls.Load())
	}
}
