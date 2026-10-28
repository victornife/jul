// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestBackgroundOversized200RetiresOldEntry(t *testing.T) {
	c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, MaxEntrySize: 64})
	group := testLease(t, 52)
	var full atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			_, _ = w.Write([]byte(strings.Repeat("x", 128)))
			return
		}
		if full.Add(1) == 1 {
			_, _ = w.Write([]byte("old"))
		} else {
			_, _ = w.Write([]byte("new"))
		}
	}))
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
		t.Fatalf("oversized 200 retained old entry: body=%q state=%q", rec.Body.String(), rec.Header().Get("X-Cache"))
	}
}
