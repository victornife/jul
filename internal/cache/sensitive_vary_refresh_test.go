// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestSensitiveVaryReplacementDuringBackgroundRefreshDiscardsOldEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 1)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil), group)
	old := staleEntry("old-body")
	old.ETag = `"fixture"`
	c.set(key(req), old)
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Vary", "Cookie")
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write([]byte("new-body"))
	})
	h := c.Handler(origin)
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	if first.Header().Get("X-Cache") != stateStale || first.Body.String() != "old-body" {
		t.Fatal("the already-admitted stale request did not complete as expected")
	}
	waitDrained(t, c)
	next := httptest.NewRecorder()
	h.ServeHTTP(next, req)
	if next.Header().Get("X-Cache") != stateMiss || next.Body.String() != "new-body" {
		t.Fatal("a later request reused the old entry after a sensitive Vary replacement")
	}
}
