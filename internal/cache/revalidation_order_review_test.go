// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestRetirementFencesEveryFillOutstandingAtDecision(t *testing.T) {
	for _, tt := range []struct {
		name, schedule string
		disk           bool
	}{
		{"memory-old-publishes-first", "old-publishes-first", false},
		{"memory-started-during-validation", "started-during-validation", false},
		{"memory-newer-publishes-first", "newer-publishes-first", false},
		{"disk-old-publishes-first", "old-publishes-first", true},
		{"disk-started-during-validation", "started-during-validation", true},
		{"disk-newer-publishes-first", "newer-publishes-first", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.CacheConfig{MemoryMaxSize: 1 << 20}
			if tt.disk {
				cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), 8<<20
			}
			c, clock := conformanceCache(t, cfg)
			group := testLease(t, 61)
			oldStarted, oldRelease, oldDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			midStarted, midRelease, midDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			validationStarted, validationRelease := make(chan struct{}), make(chan struct{})
			var oldOnce, midOnce, validationOnce sync.Once
			defer oldOnce.Do(func() { close(oldRelease) })
			defer midOnce.Do(func() { close(midRelease) })
			defer validationOnce.Do(func() { close(validationRelease) })
			var denied atomic.Bool
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "max-age=1, stale-while-revalidate=60")
				w.Header().Set("ETag", `"fixture"`)
				switch r.Header.Get("X-Fixture") {
				case "old":
					close(oldStarted)
					<-oldRelease
					w.Header().Set("Cache-Control", "public, max-age=60")
					_, _ = w.Write([]byte("old-fill"))
					return
				case "middle":
					close(midStarted)
					<-midRelease
					w.Header().Set("Cache-Control", "public, max-age=60")
					_, _ = w.Write([]byte("middle-fill"))
					return
				}
				if r.Header.Get("If-None-Match") != "" {
					close(validationStarted)
					<-validationRelease
					denied.Store(true)
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if denied.Load() {
					w.Header().Set("Cache-Control", "public, max-age=60")
					_, _ = w.Write([]byte("after-denial"))
					return
				}
				_, _ = w.Write([]byte("seed"))
			}))
			req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
			startFill := func(name string, done chan struct{}) {
				r := req.Clone(req.Context())
				r.Header.Set("X-Fixture", name)
				if name == "middle" {
					r.Header.Set("Authorization", "Bearer synthetic")
				}
				go func() {
					defer close(done)
					h.ServeHTTP(httptest.NewRecorder(), r)
				}()
			}
			startFill("old", oldDone)
			<-oldStarted
			h.ServeHTTP(httptest.NewRecorder(), req)
			clock.advance(2 * time.Second)
			h.ServeHTTP(httptest.NewRecorder(), req)
			<-validationStarted
			if tt.schedule == "old-publishes-first" {
				oldOnce.Do(func() { close(oldRelease) })
				<-oldDone
			} else {
				startFill("middle", midDone)
				<-midStarted
				if tt.schedule == "newer-publishes-first" {
					midOnce.Do(func() { close(midRelease) })
					<-midDone
				}
			}
			validationOnce.Do(func() { close(validationRelease) })
			waitDrained(t, c)
			waitLeaseReleased(t, group)
			oldOnce.Do(func() { close(oldRelease) })
			<-oldDone
			if tt.schedule == "started-during-validation" {
				midOnce.Do(func() { close(midRelease) })
				<-midDone
			}
			if e, ok := c.get(key(req)); ok {
				t.Fatalf("pre-decision fill survived denial: %q", e.Body)
			}
			if tt.disk {
				restarted := newTestCache(t, cfg)
				if e, ok := restarted.get(key(req)); ok {
					t.Fatalf("pre-decision fill survived restart: %q", e.Body)
				}
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Body.String() != "after-denial" || rec.Header().Get("X-Cache") != stateMiss {
				t.Fatalf("post-denial fill: body=%q cache=%q", rec.Body.String(), rec.Header().Get("X-Cache"))
			}
			hit := httptest.NewRecorder()
			h.ServeHTTP(hit, req)
			if hit.Body.String() != "after-denial" || hit.Header().Get("X-Cache") != stateHit {
				t.Fatalf("new publication not retained: body=%q cache=%q", hit.Body.String(), hit.Header().Get("X-Cache"))
			}
		})
	}
}

func TestImplicitTruncatedSynchronousValidationRefetches(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	var full atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"fixture"`)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Content-Length", "10")
			return
		}
		if full.Add(1) == 1 {
			_, _ = w.Write([]byte("old"))
		} else {
			_, _ = w.Write([]byte("new"))
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "new" || full.Load() != 2 {
		t.Fatalf("truncated validation forwarded: body=%q full requests=%d", rec.Body.String(), full.Load())
	}
}
