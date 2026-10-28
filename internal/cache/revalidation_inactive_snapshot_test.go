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

func TestReviewTagSnapshotIgnoresInactiveValidators(t *testing.T) {
	for _, mode := range []string{"invalidated", "rejected"} {
		for _, disk := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/disk=%v", mode, disk), func(t *testing.T) {
				cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}}
				if disk {
					cfg.DiskPath = t.TempDir()
					cfg.DiskMaxSize = 8 << 20
				}
				c, _ := conformanceCache(t, cfg)
				aReady, aRelease, aDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				bReady, bRelease, bDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var fresh atomic.Bool
				origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					id := r.Header.Get("X-Test-Request")
					if r.Header.Get("If-None-Match") != "" {
						if id == "A" {
							if mode == "rejected" {
								w.Header().Set("Vary", "Authorization")
								w.WriteHeader(http.StatusForbidden)
								return
							}
							close(aReady)
							<-aRelease
							w.WriteHeader(http.StatusForbidden)
							return
						}
						if id == "B" {
							close(bReady)
							<-bRelease
							w.WriteHeader(http.StatusForbidden)
							return
						}
					}
					if id == "A" {
						close(aReady)
						<-aRelease
					}
					w.Header().Set("Cache-Control", "public, max-age=60")
					w.Header().Set("ETag", `"fixture"`)
					if fresh.Load() {
						w.Header().Set("Cache-Tag", "other")
						_, _ = w.Write([]byte("new"))
					} else {
						w.Header().Set("Cache-Tag", "target")
						_, _ = w.Write([]byte("old"))
					}
				})
				h := c.Handler(origin)
				req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				h.ServeHTTP(httptest.NewRecorder(), req)
				a := req.Clone(req.Context())
				a.Header.Set("Cache-Control", "no-cache")
				a.Header.Set("X-Test-Request", "A")
				go func() {
					defer close(aDone)
					h.ServeHTTP(httptest.NewRecorder(), a)
				}()
				<-aReady
				defer func() {
					close(aRelease)
					<-aDone
				}()
				sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"target"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "invalidated" {
					res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
					if err != nil {
						t.Fatal(err)
					}
					if res.FencedInflightOps < 1 {
						t.Fatal("first purge did not fence validator A")
					}
				} else {
					if _, ok := c.get(key(req)); ok {
						t.Fatal("A failed to retire target")
					}
					if c.inflightRevalidations() != 0 {
						t.Fatal("rejected A remained in coordination map")
					}
				}
				fresh.Store(true)
				h.ServeHTTP(httptest.NewRecorder(), req)
				if e, ok := c.get(key(req)); !ok || string(e.Body) != "new" {
					t.Fatal("other-tagged entry missing")
				}
				b := req.Clone(req.Context())
				b.Header.Set("Cache-Control", "no-cache")
				b.Header.Set("X-Test-Request", "B")
				go func() {
					defer close(bDone)
					h.ServeHTTP(httptest.NewRecorder(), b)
				}()
				<-bReady
				res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
				if err != nil {
					close(bRelease)
					<-bDone
					t.Fatal(err)
				}
				if res.InvalidatedResources != 0 {
					t.Errorf("target purge selected %d unrelated resources", res.InvalidatedResources)
				}
				c.fetchAndStore(httptest.NewRecorder(), req, origin, c.clock())
				close(bRelease)
				<-bDone
				if e, ok := c.get(key(req)); ok {
					t.Errorf("B denial did not retire newer entry %q", e.Body)
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
