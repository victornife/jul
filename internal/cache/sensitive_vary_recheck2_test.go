// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSensitiveVaryCanceledBackgroundErrorDoesNotExtendStale(t *testing.T) {
	c := newTestCache(t, memCfg())
	group := testLease(t, 1)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil), group)
	old := staleEntry("old-body")
	old.ETag = `"fixture"`
	old.HasStaleIfError = true
	old.StaleIfError = 60 * 1e9
	originalDeadline := old.StaleUntil
	c.set(key(req), old)
	entered := make(chan struct{})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		w.Header().Set("Vary", "Cookie")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	h.ServeHTTP(httptest.NewRecorder(), req)
	<-entered
	group.Cancel()
	waitDrained(t, c)
	entry, ok := c.get(key(req))
	if !ok || !entry.StaleUntil.Equal(originalDeadline) {
		t.Fatal("canceled background refresh extended the stale deadline")
	}
}

func TestValidationTrailersHonorTagStripping(t *testing.T) {
	cfg := memCfg()
	cfg.TagHeaders = []string{"Cache-Tag"}
	c := newTestCache(t, cfg)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Trailer", "Cache-Tag, Content-Digest")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("replacement"))
			w.Header().Set("Cache-Tag", "fixture-internal-tag")
			w.Header().Set("Content-Digest", "fixture-digest")
			w.Header().Set(http.TrailerPrefix+"Cache-Tag", "fixture-prefixed-tag")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"fixture"`)
		_, _ = w.Write([]byte("original"))
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.Trailer.Get("Cache-Tag") != "" || strings.Contains(res.Header.Get("Trailer"), "Cache-Tag") {
		t.Fatal("configured internal tag leaked through response trailers")
	}
	if res.Trailer.Get("Content-Digest") != "fixture-digest" {
		t.Fatal("unrelated response trailer was lost")
	}
}
