// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestTagPurgeFencesScopedReplacementAfterCatalogChange(t *testing.T) {
	for _, mode := range []string{"synchronous", "background"} {
		for _, vary := range []bool{false, true} {
			for _, disk := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/vary=%t/disk=%t", mode, vary, disk), func(t *testing.T) {
					cfg := config.CacheConfig{
						MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"},
						StaleWhileRevalidate: config.Duration(time.Minute),
					}
					if disk {
						cfg = largeCacheCfg(t.TempDir(), 512, 1<<20, 8<<20)
						cfg.TagHeaders = []string{"Cache-Tag"}
						cfg.StaleWhileRevalidate = config.Duration(time.Minute)
					}
					c, clock := conformanceCache(t, cfg)
					group := testLease(t, 19)
					entered, release := make(chan struct{}), make(chan struct{})
					var releaseOnce sync.Once
					t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
					a, aScope := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if vary {
							w.Header().Set("Vary", "Accept-Language")
						}
						w.Header().Set("ETag", `"fixture"`)
						if r.Header.Get("If-None-Match") != "" {
							close(entered)
							<-release
							w.Header().Set("Cache-Control", "public, max-age=60")
							w.Header().Set("Cache-Tag", "tag-c")
							_, _ = w.Write([]byte(strings.Repeat("late", 1024)))
							return
						}
						if mode == "background" {
							w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
						} else {
							w.Header().Set("Cache-Control", "public, max-age=60")
						}
						w.Header().Set("Cache-Tag", "tag-a")
						_, _ = w.Write([]byte(strings.Repeat("old", 1024)))
					}))
					b, _ := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Cache-Control", "public, max-age=60")
						w.Header().Set("Cache-Tag", "tag-b")
						if vary {
							w.Header().Set("Vary", "Accept-Language")
						}
						_, _ = w.Write([]byte(strings.Repeat("other", 1024)))
					}))
					req := func(validate bool) *http.Request {
						r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
						r.Header.Set("Accept-Language", "en")
						if validate {
							r.Header.Set("Cache-Control", "no-cache")
						}
						return leased(r, group)
					}
					a.ServeHTTP(httptest.NewRecorder(), req(false))
					if disk {
						key := key(req(false))
						if vary {
							key = variantKey(key, []string{"Accept-Language"}, req(false))
						}
						if _, ok := c.disk.get(key); !ok {
							t.Fatal("fixture was not stored on disk")
						}
					}
					if mode == "background" {
						clock.advance(2 * time.Second)
						a.ServeHTTP(httptest.NewRecorder(), req(false))
					} else {
						go a.ServeHTTP(httptest.NewRecorder(), req(true))
					}
					awaitSignal(t, entered)
					b.ServeHTTP(httptest.NewRecorder(), req(false))
					sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"tag-a"}}, nil)
					if err != nil {
						t.Fatal(err)
					}
					preview := c.PreviewScope(sel, PreviewOptions{})
					if preview.InflightFillsMatched < 1 {
						t.Error("preview missed the validating entry's original tag")
					}
					result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
					if err != nil || result.State != "succeeded" {
						t.Fatalf("purge state %q: %v", result.State, err)
					}
					releaseOnce.Do(func() { close(release) })
					waitDrained(t, c)
					waitLeaseReleased(t, group)
					if _, _, found := c.lookupWithOwner(key(req(false)), req(false), &aScope); found {
						t.Fatal("pre-purge response was republished for route A")
					}
				})
			}
		}
	}
}

func TestExactKeyPurgeDoesNotDetachUnrelatedScopedValidation(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 23)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var validations atomic.Int32
	h, scope := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			if validations.Add(1) == 1 {
				close(entered)
				<-release
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("original"))
	}))
	req := func(validate bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/a", nil)
		if validate {
			r.Header.Set("Cache-Control", "no-cache")
		}
		return leased(r, group)
	}
	h.ServeHTTP(httptest.NewRecorder(), req(false))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.ServeHTTP(httptest.NewRecorder(), req(true)) }()
	awaitSignal(t, entered)
	sel, err := ParseSelector(Selector{Kind: SelectorKey, Key: keyFor(http.MethodGet, "example.test", "/b")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preview := c.PreviewScope(sel, PreviewOptions{}); preview.InflightFillsMatched != 0 {
		t.Error("preview matched an unrelated validation")
	}
	if result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil || result.State != "succeeded" {
		t.Fatalf("purge state %q: %v", result.State, err)
	}
	wg.Add(1)
	go func() { defer wg.Done(); h.ServeHTTP(httptest.NewRecorder(), req(true)) }()
	awaitJoined(t, c, revalidateKey{key: key(req(false)), gen: 23, scope: scope}, 1)
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	if got := validations.Load(); got != 1 {
		t.Fatalf("unrelated purge caused %d origin validations", got)
	}
}
