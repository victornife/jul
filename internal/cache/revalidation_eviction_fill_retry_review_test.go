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

func TestReviewSelectedKeyMissingAfterEviction(t *testing.T) {
	for _, evict := range []bool{false, true} {
		for _, disk := range []bool{false, true} {
			t.Run(fmt.Sprintf("evict=%v/disk=%v", evict, disk), func(t *testing.T) {
				cfg := config.CacheConfig{
					MemoryMaxSize: 4096,
					TagHeaders:    []string{"Cache-Tag"},
				}
				if disk {
					cfg.DiskPath = t.TempDir()
					cfg.DiskMaxSize = 8 << 20
				}
				c, _ := conformanceCache(t, cfg)

				published := make(chan struct{})
				aRelease, aDone := make(chan struct{}), make(chan struct{})
				bReady, bRelease, bDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var paused atomic.Bool

				origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("If-None-Match") != "" && r.Header.Get("X-Test-Request") == "B" {
						close(bReady)
						<-bRelease
						w.WriteHeader(http.StatusForbidden)
						return
					}
					w.Header().Set("Cache-Control", "public,max-age=60")
					w.Header().Set("ETag", `"fixture"`)
					w.Header().Set("Cache-Tag", "other")
					_, _ = w.Write([]byte("new"))
				})

				h := c.Handler(origin)
				req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				h.ServeHTTP(httptest.NewRecorder(), req)

				c.onBeforeReclaim = func(k string) {
					if k == key(req) && paused.CompareAndSwap(false, true) {
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

				b := req.Clone(req.Context())
				b.Header.Set("Cache-Control", "no-cache")
				b.Header.Set("X-Test-Request", "B")
				go func() {
					defer close(bDone)
					h.ServeHTTP(httptest.NewRecorder(), b)
				}()
				<-bReady

				if evict {
					c.set(keyFor(http.MethodGet, "example.test", "/evict"), &Entry{
						Status: http.StatusOK,
						Body:   []byte(strings.Repeat("x", 3500)),
					})
					if disk {
						c.disk.Resize(1)
						c.disk.Resize(8 << 20)
					}
					c.genMu.Lock()
					_, exists := c.keyGen[key(req)]
					c.genMu.Unlock()
					if exists {
						t.Error("selected key state still present after eviction")
					}
				}

				close(aRelease)
				<-aDone

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

func TestReviewRetryKeepsPostCutoffFill(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, disk := range []bool{false, true} {
			t.Run(fmt.Sprintf("retry=%v/disk=%v", retry, disk), func(t *testing.T) {
				cfg := config.CacheConfig{
					MemoryMaxSize: 1 << 20,
					Lock:          true,
					LockTimeout:   config.Duration(30 * time.Second),
					TagHeaders:    []string{"Cache-Tag"},
				}
				if disk {
					cfg.DiskPath = t.TempDir()
					cfg.DiskMaxSize = 8 << 20
				}
				c, _ := conformanceCache(t, cfg)

				aReady, aRelease, aDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				bReady, bDone, waited := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var post atomic.Bool
				var calls atomic.Int32
				var once sync.Once

				c.SetFillLockObserver(func(outcome string) {
					if outcome == string(fillOutcomeWait) {
						once.Do(func() { close(waited) })
					}
				})

				origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Cache-Control", "public,max-age=60")
					if !post.Load() {
						w.Header().Set("Cache-Tag", "target")
						_, _ = w.Write([]byte("initial"))
						return
					}
					w.Header().Set("Cache-Tag", "other")
					if calls.Add(1) == 1 {
						close(aReady)
						<-aRelease
						_, _ = w.Write([]byte("older"))
					} else {
						close(bReady)
						_, _ = w.Write([]byte("newer"))
					}
				})

				h := c.Handler(origin)
				req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				h.ServeHTTP(httptest.NewRecorder(), req)

				sel, err := ParseSelector(Selector{
					Kind: SelectorTag,
					Host: "example.test",
					Tags: []string{"target"},
				}, nil)
				if err != nil {
					t.Fatal(err)
				}

				var ids []string
				first, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{
					CommitFence: func(_ uint64, got []string) error {
						ids = append([]string{}, got...)
						return nil
					},
				})
				if err != nil || len(ids) != 1 {
					t.Fatalf("initial purge: ids=%v err=%v", ids, err)
				}

				post.Store(true)
				go func() {
					defer close(aDone)
					h.ServeHTTP(httptest.NewRecorder(), req)
				}()
				<-aReady

				c.genMu.Lock()
				postCutoff := false
				for _, g := range c.inflightOps {
					if g > first.FenceSequence {
						postCutoff = true
					}
				}

				c.genMu.Unlock()
				if !postCutoff {
					t.Error("fill A did not start after the cutoff")
				}

				if retry {
					res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{
						ReuseFenceGen:    first.FenceSequence,
						ReuseResourceIDs: ids,
					})
					if err != nil {
						close(aRelease)
						<-aDone
						t.Fatal(err)
					}
					if res.FencedInflightOps != 0 {
						t.Errorf("retry fenced %d post-cutoff fill(s)", res.FencedInflightOps)
					}
				}

				go func() {
					defer close(bDone)
					h.ServeHTTP(httptest.NewRecorder(), req)
				}()

				duplicate := false
				select {
				case <-bReady:
					duplicate = true
					<-bDone
					t.Error("post-cutoff fill detached; second origin request started")
					if e, ok := c.get(key(req)); !ok || string(e.Body) != "newer" {
						t.Error("newer fill was not committed before A resumed")
					}
				case <-waited:
				case <-time.After(5 * time.Second):
					t.Error("neither waiter nor second origin reached")
				}

				close(aRelease)
				<-aDone
				if !duplicate {
					<-bDone
				}

				if e, ok := c.get(key(req)); !ok {
					t.Error("final entry missing")
				} else if duplicate && string(e.Body) == "older" {
					t.Error("older fill overwrote newer committed entry")
				}
				if disk {
					restarted := newTestCache(t, cfg)
					if e, ok := restarted.get(key(req)); !ok {
						t.Error("final disk entry missing")
					} else if duplicate && string(e.Body) == "older" {
						t.Error("older fill survived restart")
					}
				}
			})
		}
	}
}
