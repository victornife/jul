// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestReviewCompletedPublicationSnapshot(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotModified} {
		for _, tier := range []string{"memory", "disk", "stream"} {
			for _, varied := range []bool{false, true} {
				t.Run(fmt.Sprintf("status=%d/tier=%s/varied=%v", status, tier, varied), func(t *testing.T) {
					cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}}
					if tier != "memory" {
						cfg.DiskPath = t.TempDir()
						cfg.DiskMaxSize = 8 << 20
					}
					body := "representation"
					if tier == "stream" {
						cfg.MemoryMaxSize = 512
						cfg.MaxEntrySize = 1 << 20
						body = strings.Repeat("x", 4096)
					}
					c, _ := conformanceCache(t, cfg)
					published := make(chan struct{})
					aRelease, aDone := make(chan struct{}), make(chan struct{})
					bReady, bRelease, bDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
					var fresh, paused atomic.Bool
					origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						id := r.Header.Get("X-Test-Request")
						if varied {
							w.Header().Set("Vary", "Accept-Language")
						}
						if r.Header.Get("If-None-Match") != "" {
							if id == "A" {
								w.Header().Set("Cache-Control", "public,max-age=60")
								w.Header().Set("ETag", `"fixture"`)
								w.Header().Set("Cache-Tag", "other")
								if status == http.StatusNotModified {
									w.WriteHeader(status)
								} else {
									_, _ = w.Write([]byte(body))
								}
								return
							}
							if id == "B" {
								close(bReady)
								<-bRelease
								w.WriteHeader(http.StatusForbidden)
								return
							}
						}
						w.Header().Set("Cache-Control", "public,max-age=60")
						w.Header().Set("ETag", `"fixture"`)
						if fresh.Load() {
							w.Header().Set("Cache-Tag", "other")
							_, _ = w.Write([]byte(body))
						} else {
							w.Header().Set("Cache-Tag", "target")
							_, _ = w.Write([]byte(body))
						}
					})
					h := c.Handler(origin)
					req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
					req.Header.Set("Accept-Language", "en")
					h.ServeHTTP(httptest.NewRecorder(), req)
					fresh.Store(true)
					c.onBeforeValidationFinish = func(outcome revalidateOutcome) {
						if (outcome == outcomeStored || outcome == outcomeNotModified) && paused.CompareAndSwap(false, true) {
							close(published)
							<-aRelease
						}
					}
					a := req.Clone(req.Context())
					a.Header.Set("Cache-Control", "no-cache")
					a.Header.Set("X-Test-Request", "A")
					go func() {
						defer close(aDone)
						h.ServeHTTP(httptest.NewRecorder(), a)
					}()
					<-published
					defer func() {
						close(aRelease)
						<-aDone
					}()
					if e, _, ok := c.lookup(key(req), req); !ok || len(e.Tags) != 1 || e.Tags[0] != "other" {
						t.Fatal("A replacement was not published")
					}
					c.fetchAndStore(httptest.NewRecorder(), req, origin, c.clock())
					b := req.Clone(req.Context())
					b.Header.Set("Cache-Control", "no-cache")
					b.Header.Set("X-Test-Request", "B")
					go func() {
						defer close(bDone)
						h.ServeHTTP(httptest.NewRecorder(), b)
					}()
					<-bReady
					sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"target"}}, nil)
					if err != nil {
						close(bRelease)
						<-bDone
						t.Fatal(err)
					}
					res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
					if err != nil {
						close(bRelease)
						<-bDone
						t.Fatal(err)
					}
					if res.InvalidatedResources != 0 {
						t.Errorf("purge selected %d other-tagged resources", res.InvalidatedResources)
					}
					c.fetchAndStore(httptest.NewRecorder(), req, origin, c.clock())
					close(bRelease)
					<-bDone
					if e, _, ok := c.lookup(key(req), req); ok {
						t.Errorf("B denial failed to retire newer entry %q", e.Body)
					}
					if tier != "memory" {
						restarted := newTestCache(t, cfg)
						if e, _, ok := restarted.lookup(key(req), req); ok {
							t.Errorf("denied entry survived restart: %q", e.Body)
						}
					}
				})
			}
		}
	}
}
