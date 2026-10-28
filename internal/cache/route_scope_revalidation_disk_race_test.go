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
)

func TestTagPurgeFencesScopedRevalidationDuringDiskPublication(t *testing.T) {
	for _, vary := range []bool{false, true} {
		t.Run(fmt.Sprintf("vary=%t", vary), func(t *testing.T) {
			cfg := largeCacheCfg(t.TempDir(), 512, 1<<20, 8<<20)
			cfg.TagHeaders = []string{"Cache-Tag"}
			c := newTestCache(t, cfg)
			group := testLease(t, 29)
			entered, releaseOrigin := make(chan struct{}), make(chan struct{})
			published, releaseDisk := make(chan struct{}), make(chan struct{})
			var originOnce, diskOnce sync.Once
			t.Cleanup(func() {
				originOnce.Do(func() { close(releaseOrigin) })
				diskOnce.Do(func() { close(releaseDisk) })
			})
			var fullFetches atomic.Int32
			a, aScope := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=60")
				w.Header().Set("ETag", `"fixture"`)
				if vary {
					w.Header().Set("Vary", "Accept-Language")
				}
				if r.Header.Get("If-None-Match") != "" {
					close(entered)
					<-releaseOrigin
					w.Header().Set("Cache-Tag", "tag-c")
					_, _ = w.Write([]byte(strings.Repeat("late", 1024)))
					return
				}
				if fullFetches.Add(1) > 1 {
					w.Header().Set("Cache-Control", "no-store")
					_, _ = w.Write([]byte("post-purge"))
					return
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
			done := make(chan struct{})
			go func() { defer close(done); a.ServeHTTP(httptest.NewRecorder(), req(true)) }()
			awaitSignal(t, entered)
			b.ServeHTTP(httptest.NewRecorder(), req(false))
			var blocked atomic.Bool
			c.onAfterDiskPublication = func(string, uint64) {
				if blocked.CompareAndSwap(false, true) {
					close(published)
					<-releaseDisk
				}
			}
			originOnce.Do(func() { close(releaseOrigin) })
			awaitSignal(t, published)
			sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"tag-a"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil || result.State != "succeeded" {
				t.Fatalf("purge state %q: %v", result.State, err)
			}
			diskOnce.Do(func() { close(releaseDisk) })
			awaitSignal(t, done)
			if e, _, found := c.lookupWithOwner(key(req(false)), req(false), &aScope); found {
				t.Fatalf("late disk publication remained reusable after tag purge; tags=%v", e.Tags)
			}
		})
	}
}
