// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

func TestRouteScopeIsolatesHeadAndRangeVariants(t *testing.T) {
	for _, tier := range []string{"memory", "disk"} {
		t.Run(tier, func(t *testing.T) {
			cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, RangeEnabled: true}
			if tier == "disk" {
				cfg = largeCacheCfg(t.TempDir(), 512, 1<<20, 8<<20)
				cfg.RangeEnabled = true
			}
			c := newTestCache(t, cfg)
			origin := func(label string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=60")
					w.Header().Set("Vary", "Accept-Language")
					w.Header().Set("X-Origin", label)
					http.ServeContent(w, r, "item.txt", time.Time{}, strings.NewReader(strings.Repeat(label, 1024)))
				})
			}
			a, _ := scopedHandler(t, c, origin("alpha"))
			b, _ := scopedHandler(t, c, origin("bravo"))
			request := func(h http.Handler, method, byteRange string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "http://example.test/item", nil)
				req.Header.Set("Accept-Language", "en")
				if byteRange != "" {
					req.Header.Set("Range", byteRange)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				return rec
			}
			request(a, http.MethodGet, "")
			if tier == "disk" {
				req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				req.Header.Set("Accept-Language", "en")
				if _, ok := c.disk.get(variantKey(key(req), []string{"Accept-Language"}, req)); !ok {
					t.Fatal("range fixture was not stored on disk")
				}
			}
			request(a, http.MethodHead, "")
			if rec := request(b, http.MethodHead, ""); rec.Header().Get("X-Origin") != "bravo" {
				t.Fatal("HEAD reused another route's headers")
			}
			for _, step := range []struct {
				handler http.Handler
				body    string
			}{{b, "bravo"}, {a, "alpha"}, {b, "bravo"}} {
				rec := request(step.handler, http.MethodGet, "bytes=0-4")
				if rec.Code != http.StatusPartialContent || rec.Body.String() != step.body {
					t.Fatalf("cross-route range: status=%d body=%q, want %q", rec.Code, rec.Body.String(), step.body)
				}
			}
		})
	}
}
