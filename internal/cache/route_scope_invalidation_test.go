// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestRouteOwnersPreserveScopedInvalidation(t *testing.T) {
	selectors := []Selector{
		{Kind: SelectorExactURL, Host: "example.test", URI: "/articles/42"},
		{Kind: SelectorPrefix, Host: "example.test", Prefix: "/articles"},
		{Kind: SelectorGlob, Host: "example.test", Pattern: "/articles/*"},
		{Kind: SelectorTag, Host: "example.test", Tags: []string{"article:42"}},
	}
	for _, sel := range selectors {
		for _, disk := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/disk=%t", sel.Kind, disk), func(t *testing.T) {
				cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}}
				if disk {
					cfg.DiskPath = t.TempDir()
					cfg.DiskMaxSize = 8 << 20
				}
				c := newTestCache(t, cfg)
				origin := func(body string) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Cache-Control", "public, max-age=60")
						w.Header().Set("Vary", "Accept-Language")
						if r.URL.Path == "/articles/42" {
							w.Header().Set("Cache-Tag", "article:42")
						} else {
							w.Header().Set("Cache-Tag", "unrelated")
						}
						_, _ = w.Write([]byte(body))
					})
				}
				a, _ := scopedHandler(t, c, origin("route-a"))
				b, _ := scopedHandler(t, c, origin("route-b"))
				request := func(h http.Handler, method, path, language string) *httptest.ResponseRecorder {
					req := httptest.NewRequest(method, "http://example.test"+path, nil)
					req.Header.Set("Accept-Language", language)
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					return rec
				}
				request(a, http.MethodGet, "/articles/42", "en")
				request(a, http.MethodGet, "/articles/42", "fr")
				request(a, http.MethodHead, "/articles/42", "en")
				request(b, http.MethodGet, "/articles/42", "en")
				request(b, http.MethodGet, "/unrelated", "en")
				normalized, err := ParseSelector(sel, nil)
				if err != nil {
					t.Fatal(err)
				}
				preview := c.PreviewScope(normalized, PreviewOptions{})
				if preview.MatchedRepresentations < 2 || disk && preview.DiskEntries == 0 {
					t.Fatalf("preview did not cover route representations: %+v", preview)
				}
				result, err := c.InvalidateScope(context.Background(), normalized, InvalidateOptions{})
				if err != nil || result.State != "succeeded" {
					t.Fatalf("invalidation state %q: %v", result.State, err)
				}
				if rec := request(a, http.MethodHead, "/articles/42", "en"); rec.Header().Get("X-Cache") != stateMiss {
					t.Fatal("HEAD representation survived invalidation")
				}
				if rec := request(b, http.MethodGet, "/articles/42", "en"); rec.Header().Get("X-Cache") != stateMiss {
					t.Fatal("GET representation survived invalidation")
				}
				if rec := request(b, http.MethodGet, "/unrelated", "en"); rec.Header().Get("X-Cache") != stateHit {
					t.Fatal("invalidation removed an unrelated resource")
				}
			})
		}
	}
}
