// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestBackgroundRetirementDecisions(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		length      int
		tagLimit    bool
		wantRetired bool
	}{
		{name: "truncated", status: 200, body: "short", length: 10},
		{name: "oversized-truncated", status: 200, body: strings.Repeat("x", 80), length: 120},
		{name: "oversized-complete", status: 200, body: strings.Repeat("x", 80), length: 80, wantRetired: true},
		{name: "oversized-401", status: 401, body: strings.Repeat("x", 80), length: 80, wantRetired: true},
		{name: "oversized-403", status: 403, body: strings.Repeat("x", 80), length: 80, wantRetired: true},
		{name: "publication-rejected", status: 200, body: "replacement", length: 11, tagLimit: true, wantRetired: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, MaxEntrySize: 64, TagHeaders: []string{"Cache-Tag"}})
			group := testLease(t, 56)
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
				w.Header().Set("ETag", `"fixture"`)
				if r.Header.Get("If-None-Match") != "" {
					if tt.tagLimit {
						w.Header().Set("Cache-Tag", "replacement-tag")
					}
					w.Header().Set("Content-Length", strconv.Itoa(tt.length))
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
					return
				}
				_, _ = w.Write([]byte("old"))
			}))
			req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
			h.ServeHTTP(httptest.NewRecorder(), req)
			if tt.tagLimit {
				c.genMu.Lock()
				c.totalTagLinks = MaxTotalTagLinks
				c.genMu.Unlock()
			}
			clock.advance(2 * time.Second)
			h.ServeHTTP(httptest.NewRecorder(), req)
			waitDrained(t, c)
			waitLeaseReleased(t, group)
			entry, found := c.get(key(req))
			if found == tt.wantRetired {
				t.Fatalf("entry found=%v, want retired=%v", found, tt.wantRetired)
			}
			if found && string(entry.Body) != "old" {
				t.Fatalf("body = %q, want old", entry.Body)
			}
		})
	}
}

func TestRetirementFencesOlderFillButPreservesNewerPublication(t *testing.T) {
	c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, MaxEntrySize: 64})
	group := testLease(t, 57)
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan struct{})
	var phase atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("X-Old-Fixture") != "" {
			close(oldStarted)
			<-releaseOld
			_, _ = w.Write([]byte("older-fill"))
			return
		}
		if r.Header.Get("If-None-Match") != "" {
			phase.Store(1)
			w.Header().Set("Content-Length", "80")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(strings.Repeat("x", 80)))
			return
		}
		if phase.Load() == 0 {
			_, _ = w.Write([]byte("old"))
		} else {
			_, _ = w.Write([]byte("new"))
		}
	}))
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	oldReq := req.Clone(req.Context())
	oldReq.Header.Set("X-Old-Fixture", "1")
	go func() {
		defer close(oldDone)
		h.ServeHTTP(httptest.NewRecorder(), oldReq)
	}()
	<-oldStarted
	h.ServeHTTP(httptest.NewRecorder(), req)
	clock.advance(2 * time.Second)
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitDrained(t, c)
	waitLeaseReleased(t, group)
	fresh := httptest.NewRecorder()
	h.ServeHTTP(fresh, req)
	if fresh.Body.String() != "new" || fresh.Header().Get("X-Cache") != stateMiss {
		t.Fatalf("new publication body=%q state=%q", fresh.Body.String(), fresh.Header().Get("X-Cache"))
	}
	close(releaseOld)
	<-oldDone
	hit := httptest.NewRecorder()
	h.ServeHTTP(hit, req)
	if hit.Body.String() != "new" || hit.Header().Get("X-Cache") != stateHit {
		t.Fatalf("older fill displaced newer entry: body=%q state=%q", hit.Body.String(), hit.Header().Get("X-Cache"))
	}
	if stats := c.IndexStats(); stats.ActiveFences != 0 || stats.InflightOriginOps != 0 {
		t.Fatalf("retirement state not reclaimed: %+v", stats)
	}
}

func TestRetirementPreservesOtherResidentVariants(t *testing.T) {
	c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 58)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("ETag", `"fixture"`)
		w.Header().Set("Vary", "Accept-Language")
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(r.Header.Get("Accept-Language")))
	}))
	req := func(language string) *http.Request {
		r := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
		r.Header.Set("Accept-Language", language)
		return r
	}
	en, fr := req("en"), req("fr")
	h.ServeHTTP(httptest.NewRecorder(), en)
	h.ServeHTTP(httptest.NewRecorder(), fr)
	clock.advance(2 * time.Second)
	h.ServeHTTP(httptest.NewRecorder(), en)
	waitDrained(t, c)
	waitLeaseReleased(t, group)
	base, ok := c.get(key(fr))
	if !ok || !base.IsVaryStub {
		t.Fatal("variant stub missing")
	}
	entry, ok := c.get(variantKey(key(fr), base.Vary, fr))
	if !ok || string(entry.Body) != "fr" {
		t.Fatalf("unrelated resident variant lost: found=%v", ok)
	}
}

func TestBackgroundRetirementSurvivesDiskRestart(t *testing.T) {
	cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, MaxEntrySize: 64, DiskPath: t.TempDir(), DiskMaxSize: 8 << 20}
	c, clock := conformanceCache(t, cfg)
	group := testLease(t, 59)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Content-Length", "80")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(strings.Repeat("x", 80)))
			return
		}
		_, _ = w.Write([]byte("old"))
	}))
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !c.disk.hasKey(key(req)) {
		t.Fatal("initial representation was not persisted")
	}
	clock.advance(2 * time.Second)
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitDrained(t, c)
	waitLeaseReleased(t, group)
	restarted := newTestCache(t, cfg)
	if _, ok := restarted.get(key(req)); ok {
		t.Fatal("retired disk representation survived restart")
	}
}
