// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func reviewDecisionReasons(t *testing.T, c *Cache) map[string]bool {
	t.Helper()
	d, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorHost, ""))
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]bool)
	for _, v := range d.Decisions {
		out[v.Reason] = true
	}
	return out
}
func resetReviewDecisions(c *Cache) { c.setDecisionSampling(false); c.setDecisionSampling(true) }

func TestInspectionReviewSIEReuseBasis(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Second), StaleIfError: config.Duration(time.Minute), DecisionSamples: true})
	defer c.setDecisionSampling(false)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC).UnixNano())
	c.now = func() time.Time { return time.Unix(0, clock.Load()) }
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "max-age=1")
		_, _ = w.Write([]byte("cached"))
	}))
	req := httptest.NewRequest("GET", "http://example.com/sie", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	clock.Add(int64(2 * time.Second))
	failed := httptest.NewRecorder()
	h.ServeHTTP(failed, req)
	if failed.Header().Get("X-Cache") != "STALE" {
		t.Fatal(failed.Code, failed.Header())
	}
	resetReviewDecisions(c)
	g := testLease(t, 1)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, leased(req, g))
	waitDrained(t, c)
	if rr.Body.String() != "cached" || rr.Header().Get("X-Cache") != "STALE" {
		t.Fatal(rr.Code, rr.Body.String())
	}
	have := reviewDecisionReasons(t, c)
	if have["stale_while_revalidate"] || !have["stale_if_error_reuse"] || !have["stale_if_error"] || !have["stale_if_error_extended"] {
		t.Fatal("wrong stale basis/background extension evidence", have)
	}
}

func TestInspectionReviewRangeReuseAndRestrictions(t *testing.T) {
	t.Run("hit", func(t *testing.T) {
		c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), RangeEnabled: true, DecisionSamples: true})
		defer c.setDecisionSampling(false)
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60")
			_, _ = w.Write([]byte("abcdef"))
		}))
		req := httptest.NewRequest("GET", "http://example.com/r", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
		resetReviewDecisions(c)
		req.Header.Set("Range", "bytes=1-3")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != 206 || rr.Body.String() != "bcd" || rr.Header().Get("X-Cache") != "HIT" {
			t.Fatal(rr.Code, rr.Body.String(), rr.Header())
		}
		if have := reviewDecisionReasons(t, c); !have["fresh_hit"] {
			t.Fatal("206 hit lacks reuse evidence", have)
		}
	})
	for _, tc := range []struct {
		name, cc, reason string
		cookie           bool
	}{{"private", "private, max-age=60", "response_private", false}, {"no-store", "no-store", "response_no_store", false}, {"cookie", "max-age=60", "response_set_cookie", true}} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), RangeEnabled: true, DecisionSamples: true})
			defer c.setDecisionSampling(false)
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", tc.cc)
				if tc.cookie {
					w.Header().Set("Set-Cookie", "SESSION=SECRET_COOKIE")
				}
				_, _ = w.Write([]byte("abcdef"))
			}))
			req := httptest.NewRequest("GET", "http://example.com/private-range", nil)
			req.Header.Set("Range", "bytes=1-3")
			h.ServeHTTP(httptest.NewRecorder(), req)
			if have := reviewDecisionReasons(t, c); !have[tc.reason] || !have["range_bypass"] {
				t.Fatal("range-fill rejection missing actual reason", have)
			}
		})
	}
}

func TestInspectionReviewBackgroundOversize(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, MaxEntrySize: 128, DefaultTTL: config.Duration(time.Second), StaleWhileRevalidate: config.Duration(time.Minute), DecisionSamples: true})
	defer c.setDecisionSampling(false)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	c.now = func() time.Time { return time.Unix(0, clock.Load()) }
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=1")
		w.Header().Set("ETag", `"v1"`)
		body := "old"
		if r.Header.Get("If-None-Match") != "" {
			body = strings.Repeat("x", 2048)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	req := httptest.NewRequest("GET", "http://example.com/oversize", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	resetReviewDecisions(c)
	clock.Add(int64(2 * time.Second))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, leased(req, testLease(t, 2)))
	waitDrained(t, c)
	if rr.Body.String() != "old" {
		t.Fatal(rr.Body.String())
	}
	if have := reviewDecisionReasons(t, c); !have["capture_oversize"] || !have["capture_not_stored"] {
		t.Fatal("background rejection lacks capture evidence", have)
	}
}

func TestInspectionReviewUnknownAndKnownValidationTime(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(304)
			return
		}
		_, _ = w.Write([]byte("x"))
	}))
	req := httptest.NewRequest("GET", "http://example.com/time", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	evidence := func() map[string]any {
		d, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorHost, ""))
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(d.Resources[0].Details[0].Evidence)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err = json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := evidence()
	if _, exists := before["validated_at"]; exists {
		t.Fatal("unvalidated response has a date", before)
	}
	if before["inserted_at"] == nil {
		t.Fatal("known insertion date omitted")
	}
	req.Header.Set("Cache-Control", "no-cache")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if after := evidence(); after["validated_at"] == nil || strings.HasPrefix(after["validated_at"].(string), "0001-") {
		t.Fatal("successful validation time missing", after)
	}
}

func TestInspectionReviewStaleBasisRestartAndReplacement(t *testing.T) {
	cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20, DefaultTTL: config.Duration(time.Second), StaleWhileRevalidate: config.Duration(time.Minute), StaleIfError: config.Duration(2 * time.Minute), DecisionSamples: true}
	c := newTestCache(t, cfg)
	defer c.setDecisionSampling(false)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	c.now = now
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "max-age=1")
		_, _ = w.Write([]byte("cached"))
	}))
	req := httptest.NewRequest("GET", "http://example.com/restart", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	clock.Add(int64(2 * time.Second))
	req.Header.Set("Cache-Control", "no-cache")
	h.ServeHTTP(httptest.NewRecorder(), req)
	req.Header.Del("Cache-Control")
	c2 := newTestCache(t, cfg)
	defer c2.setDecisionSampling(false)
	c2.now = now
	h2 := c2.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=1")
		w.Header().Set("Date", now().UTC().Format(http.TimeFormat))
		w.WriteHeader(304)
	}))
	rr := httptest.NewRecorder()
	h2.ServeHTTP(rr, req)
	if rr.Body.String() != "cached" {
		t.Fatal("restart lost response", rr.Code, rr.Body.String())
	}
	if have := reviewDecisionReasons(t, c2); !have["stale_if_error_reuse"] || have["stale_while_revalidate"] {
		t.Fatal("restart lost error basis", have)
	}
	// Simulate the previous metadata version: publication evidence existed but
	// carried no stale-basis discriminator. Do not invent SWR from that deadline.
	e, ok := c2.get(key(req))
	if !ok {
		t.Fatal("entry missing")
	}
	legacy := e.Clone()
	legacy.Evidence.Flags &= 7
	c2.set(key(req), legacy)
	resetReviewDecisions(c2)
	h2.ServeHTTP(httptest.NewRecorder(), req)
	if have := reviewDecisionReasons(t, c2); !have["stale_basis_unavailable"] || have["stale_while_revalidate"] {
		t.Fatal("older provenance invented a stale basis", have)
	}
	req.Header.Set("Cache-Control", "no-cache")
	h2.ServeHTTP(httptest.NewRecorder(), req)
	req.Header.Del("Cache-Control")
	resetReviewDecisions(c2)
	clock.Add(int64(2 * time.Second))
	h2.ServeHTTP(httptest.NewRecorder(), req)
	if have := reviewDecisionReasons(t, c2); !have["stale_while_revalidate"] || have["stale_if_error_reuse"] {
		t.Fatal("304 retained obsolete error basis", have)
	}
}

func TestInspectionReviewBackgroundSIEExtension(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Second), StaleWhileRevalidate: config.Duration(time.Minute), StaleIfError: config.Duration(2 * time.Minute), DecisionSamples: true})
	defer c.setDecisionSampling(false)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	c.now = func() time.Time { return time.Unix(0, clock.Load()) }
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Cache-Control", "max-age=1")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("cached"))
	}))
	req := httptest.NewRequest("GET", "http://example.com/background-sie", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	resetReviewDecisions(c)
	clock.Add(int64(2 * time.Second))
	h.ServeHTTP(httptest.NewRecorder(), leased(req, testLease(t, 3)))
	waitDrained(t, c)
	if have := reviewDecisionReasons(t, c); !have["stale_while_revalidate"] || !have["stale_if_error"] || !have["stale_if_error_extended"] {
		t.Fatal("background failure/extension not observed", have)
	}
	resetReviewDecisions(c)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if have := reviewDecisionReasons(t, c); !have["stale_if_error_reuse"] || have["stale_while_revalidate"] {
		t.Fatal("post-background reuse mislabeled", have)
	}
}

func TestInspectionReviewRangeFillPublication(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), RangeEnabled: true, DecisionSamples: true})
	defer c.setDecisionSampling(false)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("abcdef")) }))
	req := httptest.NewRequest("GET", "http://example.com/fill", nil)
	req.Header.Set("Range", "bytes=1-3")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 206 || rr.Body.String() != "bcd" {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if have := reviewDecisionReasons(t, c); !have["response_stored"] || have["fresh_hit"] {
		t.Fatal("fill publication must be observed, not inferred from a miss", have)
	}
}
