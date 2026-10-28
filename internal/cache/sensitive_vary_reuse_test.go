// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestPresetSensitiveVaryBypassesExistingEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write([]byte(r.Header.Get("X-Fixture-Identity")))
	})
	cached := c.Handler(origin)
	first := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	first.Header.Set("X-Fixture-Identity", "fixture-alice")
	cached.ServeHTTP(httptest.NewRecorder(), first)

	next := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	next.Header.Set("X-Fixture-Identity", "fixture-bob")
	rec := httptest.NewRecorder()
	rec.Header().Set("Vary", "Cookie")
	cached.ServeHTTP(rec, next)
	if rec.Header().Get("X-Cache") != stateBypass || rec.Body.String() != "fixture-bob" {
		t.Fatal("preset sensitive Vary did not bypass the existing entry")
	}
}
