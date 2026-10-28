// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestPurgedValidationCannotRetireLaterPublication(t *testing.T) {
	c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 62)
	started, release := make(chan struct{}), make(chan struct{})
	var afterPurge atomic.Bool
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			close(started)
			<-release
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if afterPurge.Load() {
			_, _ = w.Write([]byte("after-purge"))
		} else {
			_, _ = w.Write([]byte("before-purge"))
		}
	}))
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	h.ServeHTTP(httptest.NewRecorder(), req)
	clock.advance(2 * time.Second)
	h.ServeHTTP(httptest.NewRecorder(), req)
	<-started
	c.Purge()
	afterPurge.Store(true)
	h.ServeHTTP(httptest.NewRecorder(), req)
	close(release)
	waitDrained(t, c)
	waitLeaseReleased(t, group)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "after-purge" || rec.Header().Get("X-Cache") != stateHit {
		t.Fatalf("older validation displaced post-purge entry: body=%q cache=%q", rec.Body.String(), rec.Header().Get("X-Cache"))
	}
}
