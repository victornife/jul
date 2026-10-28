// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestScopedInvalidationFencesConcurrentRoutePublications(t *testing.T) {
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
				entered := make(chan struct{}, 2)
				release := make(chan struct{})
				var once sync.Once
				t.Cleanup(func() { once.Do(func() { close(release) }) })
				var block atomic.Bool
				origin := func(body string) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Cache-Control", "public, max-age=60")
						w.Header().Set("Cache-Tag", "article:42")
						w.WriteHeader(http.StatusOK)
						if r.Method == http.MethodGet && block.Load() {
							entered <- struct{}{}
							<-release
						}
						_, _ = w.Write([]byte(body))
					})
				}
				a, _ := scopedHandler(t, c, origin("route-a"))
				b, _ := scopedHandler(t, c, origin("route-b"))
				req := func(method string) *http.Request {
					return httptest.NewRequest(method, "http://example.test/articles/42", nil)
				}
				a.ServeHTTP(httptest.NewRecorder(), req(http.MethodHead))
				selector, err := ParseSelector(sel, nil)
				if err != nil {
					t.Fatal(err)
				}
				if preview := c.PreviewScope(selector, PreviewOptions{}); preview.MatchedRepresentations == 0 {
					t.Fatal("preview omitted the existing HEAD representation")
				}
				block.Store(true)
				var wg sync.WaitGroup
				for _, h := range []http.Handler{a, b} {
					wg.Add(1)
					go func(h http.Handler) {
						defer wg.Done()
						h.ServeHTTP(httptest.NewRecorder(), req(http.MethodGet))
					}(h)
				}
				awaitSignal(t, entered)
				awaitSignal(t, entered)
				result, err := c.InvalidateScope(context.Background(), selector, InvalidateOptions{})
				if err != nil || result.State != "succeeded" {
					t.Fatalf("invalidation state %q: %v", result.State, err)
				}
				block.Store(false)
				once.Do(func() { close(release) })
				wg.Wait()
				if _, found := c.get(key(req(http.MethodGet))); found {
					t.Fatal("a pre-invalidation fill published after the fence")
				}
				for _, step := range []struct {
					handler http.Handler
					body    string
				}{{a, "route-a"}, {b, "route-b"}} {
					rec := httptest.NewRecorder()
					step.handler.ServeHTTP(rec, req(http.MethodGet))
					if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != step.body {
						t.Fatalf("post-purge request: %s %q", rec.Header().Get("X-Cache"), rec.Body.String())
					}
				}
			})
		}
	}
}
