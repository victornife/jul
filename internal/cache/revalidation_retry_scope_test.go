// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestReviewRetryDoesNotExpandOriginalTagSnapshot(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, disk := range []bool{false, true} {
			t.Run(fmt.Sprintf("retry=%v/disk=%v", retry, disk), func(t *testing.T) {
				cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}}
				if disk {
					cfg.DiskPath = t.TempDir()
					cfg.DiskMaxSize = 8 << 20
				}
				c, _ := conformanceCache(t, cfg)
				ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var phase atomic.Int32
				origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("If-None-Match") != "" {
						close(ready)
						<-release
						w.WriteHeader(http.StatusForbidden)
						return
					}
					w.Header().Set("Cache-Control", "public,max-age=60")
					w.Header().Set("ETag", `"fixture"`)
					if phase.Load() == 1 {
						w.Header().Set("Cache-Tag", "target")
					} else {
						w.Header().Set("Cache-Tag", "other")
					}
					_, _ = w.Write([]byte("body"))
				})
				h := c.Handler(origin)
				req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				h.ServeHTTP(httptest.NewRecorder(), req)
				a := req.Clone(req.Context())
				a.Header.Set("Cache-Control", "no-cache")
				go func() {
					defer close(done)
					h.ServeHTTP(httptest.NewRecorder(), a)
				}()
				<-ready
				sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"target"}}, nil)
				if err != nil {
					close(release)
					<-done
					t.Fatal(err)
				}
				var ids []string
				first, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{
					CommitFence: func(_ uint64, got []string) error {
						ids = append([]string{}, got...)
						return nil
					},
				})
				if err != nil || len(ids) != 0 {
					close(release)
					<-done
					t.Fatalf("first purge: ids=%v err=%v", ids, err)
				}
				phase.Store(1)
				c.fetchAndStore(httptest.NewRecorder(), req, origin, c.clock())
				if retry {
					_, err = c.InvalidateScope(context.Background(), sel, InvalidateOptions{
						ReuseFenceGen: first.FenceSequence, ReuseResourceIDs: ids,
					})
					if err != nil {
						close(release)
						<-done
						t.Fatal(err)
					}
				}
				phase.Store(2)
				c.fetchAndStore(httptest.NewRecorder(), req, origin, c.clock())
				close(release)
				<-done
				if e, ok := c.get(key(req)); ok {
					t.Errorf("403 did not retire post-purge entry %q", e.Body)
				}
				if disk {
					restarted := newTestCache(t, cfg)
					if e, ok := restarted.get(key(req)); ok {
						t.Errorf("denied entry survived restart: %q", e.Body)
					}
				}
			})
		}
	}
}
