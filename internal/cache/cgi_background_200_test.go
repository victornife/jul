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

func TestCGIBackgroundConditional200DoesNotRetainOldAliasVariant(t *testing.T) {
	c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 51)
	var full atomic.Int32
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("ETag", `"fixture"`)
		w.Header().Set("Vary", "If_None_Match")
		if r.Header.Get("If-None-Match") != "" {
			_, _ = w.Write([]byte("conditional"))
			return
		}
		if full.Add(1) == 1 {
			_, _ = w.Write([]byte("old"))
		} else {
			_, _ = w.Write([]byte("new"))
		}
	})
	_, scope := scopedHandler(t, c, origin)
	h := cgiScopedHandler(t, c, scope, origin)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	h.ServeHTTP(httptest.NewRecorder(), req)
	clock.advance(2 * time.Second)
	stale := httptest.NewRecorder()
	h.ServeHTTP(stale, req)
	if stale.Body.String() != "old" || stale.Header().Get("X-Cache") != stateStale {
		t.Fatal("already admitted stale response did not complete")
	}
	waitDrained(t, c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "new" || rec.Header().Get("X-Cache") != stateMiss {
		t.Fatalf("incompatible CGI refresh left stale body reusable: body=%q state=%q", rec.Body.String(), rec.Header().Get("X-Cache"))
	}
}
