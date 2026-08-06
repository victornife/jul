// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// canonical rewrites a map literal into the canonical MIME key form net/http
// guarantees in production, where every header reaches the cache through
// Header.Set or Header.Add.
func canonical(h http.Header) http.Header {
	out := http.Header{}
	for name, values := range h {
		for _, v := range values {
			out.Add(name, v)
		}
	}
	return out
}

func mergeFixture(t *testing.T) (*Cache, *Entry, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	c := &Cache{defaultTTL: time.Minute, swr: 30 * time.Second}
	stored := &Entry{
		Status: http.StatusOK,
		Header: canonical(http.Header{
			"Content-Type":   {"text/plain"},
			"Content-Length": {"4"},
			"Cache-Control":  {"max-age=60"},
			"Date":           {now.Add(-time.Hour).Format(http.TimeFormat)},
			"ETag":           {`"v1"`},
			"Warning":        {`110 jul "Response is stale"`},
			"X-Custom":       {"old"},
		}),
		Body:         []byte("body"),
		CreatedAt:    now.Add(-time.Hour),
		ExpiresAt:    now.Add(-time.Hour).Add(time.Minute),
		StaleUntil:   now.Add(-time.Hour).Add(90 * time.Second),
		ETag:         `"v1"`,
		LastModified: now.Add(-2 * time.Hour).Format(http.TimeFormat),
	}
	return c, stored, now
}

func TestMerge304PreservesTheRepresentation(t *testing.T) {
	c, stored, now := mergeFixture(t)
	refreshed, action := c.merge304(stored, canonical(http.Header{"Date": {now.Format(http.TimeFormat)}}), now)
	if action != mergeReplace {
		t.Fatal("an ordinary 304 must refresh the entry")
	}
	if string(refreshed.Body) != "body" {
		t.Errorf("body = %q, a 304 must not change it", refreshed.Body)
	}
	if refreshed.Status != http.StatusOK {
		t.Errorf("status = %d, the stored representation's status must survive", refreshed.Status)
	}
	if !refreshed.Fresh(now) {
		t.Error("a successful 304 must restore freshness")
	}
}

func TestMerge304NeverMutatesThePublishedEntry(t *testing.T) {
	c, stored, now := mergeFixture(t)
	before := stored.Clone()

	refreshed, action := c.merge304(stored, canonical(http.Header{
		"Date":          {now.Format(http.TimeFormat)},
		"Cache-Control": {"max-age=600"},
		"ETag":          {`"v2"`},
		"X-Custom":      {"new"},
	}), now)
	if action != mergeReplace {
		t.Fatal("expected a replace")
	}
	if refreshed == stored {
		t.Fatal("the merge returned the same pointer; it must publish a new entry")
	}
	if stored.ETag != before.ETag || stored.Header.Get("X-Custom") != before.Header.Get("X-Custom") ||
		!stored.ExpiresAt.Equal(before.ExpiresAt) || !stored.CreatedAt.Equal(before.CreatedAt) {
		t.Fatal("the published entry was written through")
	}
	// Header maps must not be shared either.
	refreshed.Header.Set("X-Custom", "mutated-later")
	if stored.Header.Get("X-Custom") != "old" {
		t.Fatal("the refreshed entry aliases the published header map")
	}
}

func TestMerge304UpdatesMetadata(t *testing.T) {
	cases := []struct {
		name  string
		h304  http.Header
		check func(t *testing.T, e *Entry, now time.Time)
	}{
		{
			name: "changed ETag",
			h304: http.Header{"ETag": {`"v2"`}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if e.ETag != `"v2"` || e.Header.Get("ETag") != `"v2"` {
					t.Errorf("ETag = %q / %q", e.ETag, e.Header.Get("ETag"))
				}
			},
		},
		{
			name: "changed Last-Modified",
			h304: http.Header{"Last-Modified": {"Wed, 05 Aug 2026 00:00:00 GMT"}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if e.LastModified != "Wed, 05 Aug 2026 00:00:00 GMT" {
					t.Errorf("Last-Modified = %q", e.LastModified)
				}
			},
		},
		{
			name: "changed Cache-Control extends the lifetime",
			h304: http.Header{"Cache-Control": {"max-age=600"}},
			check: func(t *testing.T, e *Entry, now time.Time) {
				if got := e.ExpiresAt.Sub(e.CreatedAt); got != 600*time.Second {
					t.Errorf("lifetime = %v, want 600s from the 304's Cache-Control", got)
				}
			},
		},
		{
			name: "a 304 that adds no-cache makes every later reuse validate",
			h304: http.Header{"Cache-Control": {"no-cache"}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if !e.RequiresValidation {
					t.Error("RequiresValidation must be recomputed from the merged directives")
				}
			},
		},
		{
			name: "a 304 that adds must-revalidate removes the stale window",
			h304: http.Header{"Cache-Control": {"must-revalidate, max-age=60"}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if !e.MustRevalidate {
					t.Error("MustRevalidate must be recomputed")
				}
				if !e.StaleUntil.Equal(e.ExpiresAt) {
					t.Error("must-revalidate must leave no stale window")
				}
			},
		},
		{
			name: "a 304 that adds public permits authenticated reuse",
			h304: http.Header{"Cache-Control": {"public, max-age=60"}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if !e.SharedAuthReuse {
					t.Error("SharedAuthReuse must be recomputed from the merged directives")
				}
			},
		},
		{
			name: "changed Expires",
			h304: http.Header{"Cache-Control": {""}, "Expires": {"Thu, 06 Aug 2026 12:10:00 GMT"}, "Date": {"Thu, 06 Aug 2026 12:00:00 GMT"}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if got := e.ExpiresAt.Sub(e.CreatedAt); got != 10*time.Minute {
					t.Errorf("lifetime = %v, want 10m from the merged Expires", got)
				}
			},
		},
		{
			name: "Warning is removed",
			h304: http.Header{"Date": {"Thu, 06 Aug 2026 12:00:00 GMT"}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if e.Header.Get("Warning") != "" {
					t.Errorf("Warning survived the refresh: %q", e.Header.Get("Warning"))
				}
			},
		},
		{
			name: "an ordinary end-to-end header is replaced",
			h304: http.Header{"X-Custom": {"new"}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if got := e.Header.Values("X-Custom"); len(got) != 1 || got[0] != "new" {
					t.Errorf("X-Custom = %v, want exactly [new] — replaced, not appended", got)
				}
			},
		},
		{
			name: "a header the 304 does not mention is kept",
			h304: http.Header{"X-Custom": {"new"}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if e.Header.Get("Content-Type") != "text/plain" {
					t.Error("Content-Type was dropped")
				}
			},
		},
		{
			name: "hop-by-hop fields are never merged",
			h304: http.Header{
				"Connection":        {"X-Secret"},
				"X-Secret":          {"leak"},
				"Keep-Alive":        {"timeout=5"},
				"Transfer-Encoding": {"chunked"},
				"Upgrade":           {"websocket"},
			},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				for _, name := range []string{"Connection", "X-Secret", "Keep-Alive", "Transfer-Encoding", "Upgrade"} {
					if e.Header.Get(name) != "" {
						t.Errorf("hop-by-hop field %s was merged into the stored entry", name)
					}
				}
			},
		},
		{
			name: "Content-Length is never taken from the 304",
			h304: http.Header{"Content-Length": {"0"}},
			check: func(t *testing.T, e *Entry, _ time.Time) {
				if got := e.Header.Get("Content-Length"); got != "4" {
					t.Errorf("Content-Length = %q, want the stored body's 4", got)
				}
			},
		},
		{
			name: "the 304's Date and Age restart the age clock",
			h304: http.Header{"Date": {"Thu, 06 Aug 2026 11:59:00 GMT"}, "Age": {"120"}},
			check: func(t *testing.T, e *Entry, now time.Time) {
				if got := now.Sub(e.CreatedAt); got != 2*time.Minute {
					t.Errorf("corrected age = %v, want 2m", got)
				}
			},
		},
		{
			name: "a 304 without timing is treated as generated now",
			h304: http.Header{"ETag": {`"v2"`}},
			check: func(t *testing.T, e *Entry, now time.Time) {
				if !e.CreatedAt.Equal(now) {
					t.Errorf("CreatedAt = %v, want %v", e.CreatedAt, now)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, stored, now := mergeFixture(t)
			refreshed, action := c.merge304(stored, canonical(tc.h304), now)
			if action != mergeReplace {
				t.Fatalf("action = %v, want mergeReplace", action)
			}
			tc.check(t, refreshed, now)
		})
	}
}

func TestMerge304Discards(t *testing.T) {
	cases := []struct {
		name string
		h304 http.Header
	}{
		{"a 304 that turns the response private", http.Header{"Cache-Control": {"private"}}},
		{"a 304 that turns the response no-store", http.Header{"Cache-Control": {"no-store"}}},
		{"a 304 that adds a Set-Cookie", http.Header{"Set-Cookie": {"sid=1"}}},
		{"a 304 that changes Vary", http.Header{"Vary": {"Accept-Language"}}},
		{"a 304 that introduces Vary", http.Header{"Vary": {"Accept"}}},
		{"a 304 that sets Vary: *", http.Header{"Vary": {"*"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, stored, now := mergeFixture(t)
			if _, action := c.merge304(stored, canonical(tc.h304), now); action != mergeDiscard {
				t.Fatalf("action = %v, want mergeDiscard", action)
			}
		})
	}
}

// TestMerge304ChangedVaryLeavesNothingReachableAtTheOldKey is the end-to-end
// form of the discard rule: a refreshed representation must never remain
// reachable through a keying rule that no longer describes it.
func TestMerge304ChangedVaryLeavesNothingReachableAtTheOldKey(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	varyOn := "Accept"
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Vary", varyOn)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("body"))
	}))

	wantResult(t, get(t, h, "http://x/doc", "Accept", "application/json"), stateMiss, "body")

	// The next request validates (no-cache) and the origin answers 304 while
	// having changed what it varies on.
	varyOn = "Accept-Language"
	rec := get(t, h, "http://x/doc", "Accept", "application/json")
	if rec.Header().Get("X-Cache") == stateRevalidated {
		t.Fatal("a 304 that changed Vary must not refresh the entry at the old key")
	}

	r := httptest.NewRequest(http.MethodGet, "http://x/doc", nil)
	r.Header.Set("Accept", "application/json")
	base := key(r)
	if e, ok := c.get(variantKey(base, []string{"Accept"}, r)); ok && !e.IsVaryStub {
		t.Fatal("an entry is still reachable under the superseded Vary key")
	}
}

// TestMerge304UnderConcurrentReaders is the immutability race scenario: readers
// hold the published pointer while the merge replaces it.
func TestMerge304UnderConcurrentReaders(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("stable-body"))
	}))
	get(t, h, "http://x/doc")

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				rec := get(t, h, "http://x/doc")
				if b := rec.Body.String(); b != "stable-body" {
					t.Errorf("observed a torn body during replacement: %q", b)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestMerge304AcrossBothTiers proves replacement works the same whether the
// entry currently lives in memory or came back from disk.
func TestMerge304AcrossBothTiers(t *testing.T) {
	for _, name := range []string{"memory", "disk"} {
		t.Run(name, func(t *testing.T) {
			cfg := memCfg()
			if name == "disk" {
				cfg.MemoryMaxSize = config.Size(512)
				cfg.DiskPath = t.TempDir()
				cfg.DiskMaxSize = config.Size(1 << 20)
			}
			c, _ := conformanceCache(t, cfg)
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("X-Version", "b")
				if r.Header.Get("If-None-Match") == `"v1"` {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("X-Version", "a")
				_, _ = w.Write([]byte("body"))
			}))

			wantResult(t, get(t, h, "http://x/doc"), stateMiss, "body")
			rec := get(t, h, "http://x/doc")
			wantResult(t, rec, stateRevalidated, "body")
			if got := rec.Header().Get("X-Version"); got != "b" {
				t.Errorf("X-Version = %q, want the 304's value merged in", got)
			}
		})
	}
}
