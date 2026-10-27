// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func inspectSelector(t testing.TB, kind SelectorKind, uri string) NormalizedSelector {
	t.Helper()
	in := Selector{Kind: kind, Host: "example.com"}
	if kind == SelectorExactURL {
		in.URI = uri
	}
	sel, err := ParseSelector(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

func TestInspectionDecisionOwnersAndSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, cc, reason                       string
		auth, rangeReq, upgrade, sse, oversize bool
	}{
		{name: "fresh", cc: "public, max-age=60", reason: "fresh_hit"},
		{name: "private", cc: "private, max-age=60", reason: "response_private"},
		{name: "no-store", cc: "no-store", reason: "response_no_store"},
		{name: "cookie", cc: "max-age=60", reason: "response_set_cookie"},
		{name: "auth", cc: "max-age=60", auth: true, reason: "auth_storage_restricted"},
		{name: "range", rangeReq: true, reason: "range_bypass"},
		{name: "upgrade", upgrade: true, reason: "upgrade_bypass"},
		{name: "sse", sse: true, reason: "sse_bypass"},
		{name: "oversize", oversize: true, reason: "capture_oversize"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), DecisionSamples: true, MaxEntrySize: 16})
			t.Cleanup(func() { c.setDecisionSampling(false) })
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", tc.cc)
				if tc.name == "cookie" {
					w.Header().Set("Set-Cookie", "SECRET_COOKIE")
				}
				if tc.sse {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				if tc.oversize {
					w.Header().Set("Content-Length", "64")
					_, _ = w.Write([]byte(strings.Repeat("a", 64)))
				} else {
					_, _ = w.Write([]byte("ok"))
				}
			}))
			r := httptest.NewRequest("GET", "http://example.com/item?token=SECRET_QUERY", nil)
			r.Header.Set("Cookie", "SECRET_REQUEST_COOKIE")
			if tc.auth {
				r.Header.Set("Authorization", "Bearer SECRET_AUTH")
			}
			if tc.rangeReq {
				r.Header.Set("Range", "bytes=0-1")
			}
			if tc.upgrade {
				r.Header.Set("Connection", "upgrade")
				r.Header.Set("Upgrade", "websocket")
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			h.ServeHTTP(httptest.NewRecorder(), r)
			d, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/item?token=SECRET_QUERY"))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, v := range d.Decisions {
				if v.Reason == tc.reason {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing owner %s in %+v", tc.reason, d.Decisions)
			}
			raw, _ := json.Marshal(d)
			for _, secret := range []string{"SECRET_QUERY", "SECRET_COOKIE", "SECRET_REQUEST_COOKIE", "SECRET_AUTH"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("leaked %s", secret)
				}
			}
		})
	}
}

func TestInspectionAgePolicyReloadValidationAndAbsentEvidence(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), StaleWhileRevalidate: config.Duration(time.Minute), StaleIfError: config.Duration(time.Minute)}
	c := newTestCache(t, cfg)
	c.now = func() time.Time { return now }
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Age", "10")
		w.Header().Set("Cache-Control", "max-age=30")
		_, _ = w.Write([]byte("ok"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/a", nil))
	sel := inspectSelector(t, SelectorExactURL, "/a")
	d, err := c.InspectScope(context.Background(), sel)
	if err != nil {
		t.Fatal(err)
	}
	rep := d.Resources[0].Details[0]
	if rep.OriginAgeSeconds != 10 || rep.Evidence.InsertedAt != now || rep.Evidence.FreshnessSource != "origin_max_age" || rep.Evidence.FreshnessSeconds != 30 || rep.EvidenceState != "recorded" || d.EvidenceState != "disabled" {
		t.Fatalf("bad age/policy: %+v", rep)
	}
	now = now.Add(21 * time.Second)
	d, _ = c.InspectScope(context.Background(), sel)
	if d.Resources[0].Details[0].FreshnessState != "stale" {
		t.Fatal("stale boundary")
	}
	cfg.DefaultTTL = config.Duration(2 * time.Minute)
	c.PrepareCacheUpdate(cfg).Commit()
	d, _ = c.InspectScope(context.Background(), sel)
	if !d.Resources[0].Details[0].RetainedPolicy {
		t.Fatal("reload provenance lost")
	}
	old := c.mem.ll.Front().Value.(*memItem).entry
	refreshed, action := c.merge304(old, http.Header{"Cache-Control": []string{"max-age=45"}}, now)
	if action != mergeReplace || refreshed.Evidence.ValidatedAt != now.UnixNano() || refreshed.Evidence.InsertedAt != old.Evidence.InsertedAt {
		t.Fatal("304 evidence")
	}
	now = now.Add(3 * time.Minute)
	d, _ = c.InspectScope(context.Background(), sel)
	if d.Resources[0].Details[0].FreshnessState != "expired" {
		t.Fatal("expiry boundary")
	}
	empty, _ := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/missing"))
	if empty.State != "empty" || empty.EvidenceState != "disabled" {
		t.Fatalf("invented history: %+v", empty)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.InspectScope(ctx, sel); err == nil {
		t.Fatal("cancellation ignored")
	}
	var disabled *Cache
	dd, _ := disabled.InspectScope(context.Background(), sel)
	if dd.State != "disabled" {
		t.Fatal(dd)
	}
	for _, cc := range []string{"s-maxage=5, stale-while-revalidate=2, stale-if-error=3", "no-cache, max-age=5", "must-revalidate, max-age=5", "max-age=5", ""} {
		headers := http.Header{"Cache-Control": []string{cc}}
		p := parseResponsePolicy(headers)
		e := c.entryEvidence(200, headers, p, c.Policy(), now, 5*time.Second, 2*time.Second)
		if e.FreshnessSource == 0 {
			t.Fatal(e)
		}
	}
	for _, hh := range []http.Header{{"Expires": []string{now.Add(time.Minute).Format(http.TimeFormat)}}, {"Cdn-Cache-Control": []string{"max-age=5"}}} {
		p := parseResponsePolicy(hh)
		e := c.entryEvidence(200, hh, p, c.Policy(), now, time.Minute, 0)
		if e.FreshnessSource == 0 {
			t.Fatal(e)
		}
	}
}

func TestInspectionVaryDiskRestartLegacyAndNoResidencyMutation(t *testing.T) {
	cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20, DefaultTTL: config.Duration(time.Minute)}
	c := newTestCache(t, cfg)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Language")
		_, _ = w.Write([]byte("hello"))
	}))
	for i := 0; i < 40; i++ {
		r := httptest.NewRequest("GET", "http://example.com/vary", nil)
		r.Header.Set("Accept-Language", fmt.Sprint(i))
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	sel := inspectSelector(t, SelectorHost, "")
	order := func() []string {
		c.mem.mu.Lock()
		defer c.mem.mu.Unlock()
		var out []string
		for e := c.mem.ll.Front(); e != nil; e = e.Next() {
			out = append(out, e.Value.(*memItem).key)
		}
		return out
	}
	before := order()
	stats, _ := c.Stats()
	d, err := c.InspectScope(context.Background(), sel)
	if err != nil {
		t.Fatal(err)
	}
	after := order()
	stats2, _ := c.Stats()
	if !reflect.DeepEqual(before, after) || stats != stats2 {
		t.Fatal("inspection changed residency/occupancy")
	}
	if len(d.Resources) != 1 || d.MatchedRepresentations != 40 || len(d.Resources[0].Details) != MaxInspectionDetails || !d.Resources[0].DetailsTruncated {
		t.Fatalf("vary counts: %+v", d)
	}
	c2 := newTestCache(t, cfg)
	d2, err := c2.InspectScope(context.Background(), sel)
	if err != nil {
		t.Fatal(err)
	}
	if d2.MatchedRepresentations != 40 || len(d2.Resources[0].Details) == 0 || d2.Resources[0].Details[0].EvidenceState != "recorded" {
		t.Fatalf("restart metadata: %+v", d2)
	}
	legacy := &Entry{Status: 200, Body: []byte("old"), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	c2.set(keyFor("GET", "example.com", "/legacy"), legacy)
	ld, _ := c2.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/legacy"))
	if ld.Resources[0].Details[0].EvidenceState != "legacy_unavailable" {
		t.Fatal("fabricated legacy evidence")
	}
	c2.disk.unrecoverableInvalidation.Store(true)
	pd, _ := c2.InspectScope(context.Background(), sel)
	if pd.State != "partial" {
		t.Fatal(pd.State)
	}
}

func TestInspectionBoundsSamplingReclamationAndConcurrentPurge(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 64 << 20, DefaultTTL: config.Duration(time.Minute), DecisionSamples: true})
	defer c.setDecisionSampling(false)
	now := time.Now()
	e := &Entry{Status: 200, Body: []byte("a"), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	for i := 0; i < MaxPreviewScanEntries+1; i++ {
		c.set(keyFor("GET", "example.com", fmt.Sprintf("/%d", i)), e)
	}
	sel := inspectSelector(t, SelectorHost, "")
	d, err := c.InspectScope(context.Background(), sel)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Resources) != MaxInspectionResources || !d.Truncated || d.CountAccuracy != "lower_bound_truncated" {
		t.Fatal("enumeration not bounded")
	}
	for i := 0; i < DecisionCapacity+2; i++ {
		c.recordKeyDecision(keyFor("GET", "example.com", fmt.Sprintf("/%d?secret=TOKEN", i)), "fresh_hit")
	}
	d, _ = c.InspectScope(context.Background(), sel)
	if len(d.Decisions) != DecisionCapacity {
		t.Fatal(len(d.Decisions))
	}
	b := c.samples.Load()
	b.mu.Lock()
	b.expireLocked(now.Add(2 * time.Minute))
	if len(b.records) != 0 {
		t.Fatal("expired sample retained")
	}
	b.mu.Unlock()
	c.recordKeyDecision("malformed", "fresh_hit")
	c.recordKeyDecision(keyFor("GET", "example.com", "/"+strings.Repeat("x", 600)), "fresh_hit")
	c.setDecisionSampling(false)
	d, _ = c.InspectScope(context.Background(), sel)
	if d.EvidenceState != "disabled" || len(d.Decisions) != 0 {
		t.Fatal("disable retained evidence")
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				_, _ = c.InspectScope(context.Background(), sel)
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = c.InvalidateScope(context.Background(), sel, InvalidateOptions{}) }()
	wg.Wait()
}

func BenchmarkInspectionBounded(b *testing.B) {
	for _, count := range []int{0, 256, 2000, 10001} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			c, err := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 64 << 20, DefaultTTL: config.Duration(time.Minute)}, testLogger())
			if err != nil {
				b.Fatal(err)
			}
			now := time.Now()
			for i := 0; i < count; i++ {
				c.set(keyFor("GET", "example.com", fmt.Sprintf("/%d", i)), &Entry{Status: 200, Body: []byte("x"), CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
			}
			sel := inspectSelector(b, SelectorHost, "")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := c.InspectScope(context.Background(), sel); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestInspectionReviewRegressions(t *testing.T) {
	t.Run("invalid-vary-is-not-a-safe-name", func(t *testing.T) {
		c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Vary", "Authorization: Bearer SECRET_VARY_CREDENTIAL")
			_, _ = w.Write([]byte("x"))
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/a", nil))
		d, _ := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/a"))
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), "SECRET_VARY_CREDENTIAL") || !d.Resources[0].DetailsTruncated {
			t.Fatal("malformed Vary exposed or suppression hidden", string(b))
		}
	})
	t.Run("fallback-and-effective-sie", func(t *testing.T) {
		cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), StaleIfError: config.Duration(time.Minute)}
		c := newTestCache(t, cfg)
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache, max-age=0")
			_, _ = w.Write([]byte("x"))
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/a", nil))
		d, _ := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/a"))
		if d.Resources[0].Details[0].Evidence.FreshnessSource != "gateway_fallback_no_cache" {
			t.Fatal(d.Resources[0].Details[0].Evidence)
		}
		h = c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("y")) }))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/b", nil))
		cfg.StaleIfError = 0
		c.PrepareCacheUpdate(cfg).Commit()
		d, _ = c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/b"))
		v := d.Resources[0].Details[0]
		if v.EffectiveSIESeconds == nil || *v.EffectiveSIESeconds != 0 || v.Evidence.SIESeconds != 60 {
			t.Fatalf("historical/effective conflated %+v", v)
		}
	})
	t.Run("stub-is-not-a-response", func(t *testing.T) {
		cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)}
		c := newTestCache(t, cfg)
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Vary", "Accept-Language")
			if r.Header.Get("Accept-Language") == "fr" {
				w.Header().Set("Cache-Control", "no-store")
			}
			_, _ = w.Write([]byte("x"))
		}))
		for _, lang := range []string{"en", "fr"} {
			r := httptest.NewRequest("GET", "http://example.com/a", nil)
			r.Header.Set("Accept-Language", lang)
			h.ServeHTTP(httptest.NewRecorder(), r)
		}
		base := keyFor("GET", "example.com", "/a")
		stub, ok := c.mem.get(base)
		if !ok {
			t.Fatal("stub absent")
		}
		cfg.MemoryMaxSize = config.Size(stub.Size() + int64(len(base)))
		c.PrepareCacheUpdate(cfg).Commit()
		d, _ := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/a"))
		if d.MatchedRepresentations != 0 || d.Resources[0].FreshnessState != "metadata_only" {
			t.Fatalf("phantom response %+v", d)
		}
		b, _ := json.Marshal(d)
		if !strings.Contains(string(b), `"details":[]`) {
			t.Fatal("missing array", string(b))
		}
	})
	t.Run("tag-current-membership", func(t *testing.T) {
		c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), DecisionSamples: true, TagHeaders: []string{"Cache-Tag"}})
		defer c.setDecisionSampling(false)
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Tag", "article:42")
			_, _ = w.Write([]byte("x"))
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/a", nil))
		sel, _ := ParseSelector(Selector{Kind: SelectorTag, Host: "example.com", Tags: []string{"article:42"}}, nil)
		d, _ := c.InspectScope(context.Background(), sel)
		if d.EvidenceState != "observed_current_membership" || len(d.Decisions) == 0 {
			t.Fatalf("current membership not distinguished %+v", d)
		}
	})
	t.Run("in-progress-sse", func(t *testing.T) {
		c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), DecisionSamples: true})
		defer c.setDecisionSampling(false)
		started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			close(started)
			<-release
		}))
		go func() {
			defer close(done)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/a", nil))
		}()
		<-started
		d, _ := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/a"))
		found := false
		for _, v := range d.Decisions {
			if v.Reason == "sse_bypass" {
				found = true
			}
		}
		close(release)
		<-done
		if !found {
			t.Fatal("bypass withheld until stream completion")
		}
	})
	t.Run("enabled-range-bypass-and-private-validation", func(t *testing.T) {
		c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), RangeEnabled: true, MaxRanges: 1, DecisionSamples: true})
		defer c.setDecisionSampling(false)
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"v1"`)
			if r.Header.Get("If-None-Match") != "" {
				w.Header().Set("Cache-Control", "private, max-age=60")
			}
			_, _ = w.Write([]byte("x"))
		}))
		r := httptest.NewRequest("GET", "http://example.com/a", nil)
		h.ServeHTTP(httptest.NewRecorder(), r)
		r.Header.Set("Cache-Control", "no-cache")
		h.ServeHTTP(httptest.NewRecorder(), r)
		rr := httptest.NewRequest("GET", "http://example.com/range", nil)
		rr.Header.Set("Range", "malformed")
		h.ServeHTTP(httptest.NewRecorder(), rr)
		d, _ := c.InspectScope(context.Background(), inspectSelector(t, SelectorHost, ""))
		found := map[string]bool{}
		for _, v := range d.Decisions {
			found[v.Reason] = true
		}
		if !found["response_private"] || !found["range_bypass"] {
			t.Fatal(found)
		}
	})
}

func TestInspectionCompactMetadataIsolationAndTimerReclamation(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), DecisionSamples: true})
	defer c.setDecisionSampling(false)
	e := c.buildEntry(httptest.NewRequest("GET", "http://example.com/a", nil), 200, http.Header{}, []byte("x"), time.Now())
	if e == nil {
		t.Fatal("missing entry")
	}
	clone := e.Clone()
	clone.Evidence.InsertedAt++
	if clone.Evidence.InsertedAt == e.Evidence.InsertedAt || e.Evidence == clone.Evidence {
		t.Fatal("clone mutates published provenance")
	}
	c.set(keyFor("GET", "example.com", "/a"), e)
	if c.keyGen[keyFor("GET", "example.com", "/a")].evidence != e.Evidence {
		t.Fatal("provenance duplicated instead of shared")
	}
	if e.Size()-(&Entry{Header: e.Header, Body: e.Body}).Size() != 96 {
		t.Fatal("provenance not charged to capacity")
	}
	c.recordKeyDecision(keyFor("GET", "example.com", "/a"), "fresh_hit")
	b := c.samples.Load()
	b.mu.Lock()
	b.timer.Reset(time.Millisecond)
	b.mu.Unlock()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		b.mu.Lock()
		n := len(b.records)
		b.mu.Unlock()
		if n == 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("timer did not reclaim samples")
		case <-tick.C:
		}
	}
	// Invalid names/tags and corrupt provenance are unavailable, never reflected.
	if names, truncated := boundedInspectionVary([]string{strings.Repeat("x", 129)}); len(names) != 0 || !truncated {
		t.Fatal(names, truncated)
	}
	if tags, truncated := boundedInspectionTags([]string{"Bearer SECRET"}); len(tags) != 0 || !truncated {
		t.Fatal(tags, truncated)
	}
	if _, truncated := boundedInspectionTags(make([]string, 33)); !truncated {
		t.Fatal("unbounded tag scan")
	}
	if _, truncated := boundedInspectionVary(make([]string, 17)); !truncated {
		t.Fatal("unbounded vary scan")
	}
	if sourceCode("unknown", freshnessSources[:]) != 0 {
		t.Fatal("unknown provenance accepted")
	}
	if got := evidenceValue(&publicationEvidence{FreshnessSource: 255}, &keyGenState{}); got.FreshnessSource != "" {
		t.Fatal("corrupt provenance exposed")
	}
	var nilCache *Cache
	if nilCache.InspectionSequence() != 0 {
		t.Fatal("disabled sequence")
	}
	if c.InspectionSequence() == 0 {
		t.Fatal("publication sequence missing")
	}
}

func TestInspectionObservedReuseFillAndDiskFailures(t *testing.T) {
	reasons := func(t *testing.T, c *Cache, want ...string) {
		t.Helper()
		d, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorHost, ""))
		if err != nil {
			t.Fatal(err)
		}
		have := map[string]bool{}
		for _, v := range d.Decisions {
			have[v.Reason] = true
			if v.Source != "observed" || v.Summary == "" {
				t.Fatal("reason lacks server guidance", v)
			}
		}
		for _, r := range want {
			if !have[r] {
				t.Fatalf("missing %s in %v", r, have)
			}
		}
	}
	for _, mode := range []string{"swr", "validated", "sie", "private304"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Second), DecisionSamples: true, StaleIfError: config.Duration(time.Minute)}
			if mode == "swr" {
				cfg.StaleWhileRevalidate = config.Duration(time.Minute)
			}
			c := newTestCache(t, cfg)
			defer c.setDecisionSampling(false)
			now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
			c.now = func() time.Time { return now }
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") != "" {
					if mode == "sie" {
						w.WriteHeader(500)
						return
					}
					if mode == "private304" {
						w.Header().Set("Cache-Control", "private")
					}
					w.Header().Set("Date", now.Format(http.TimeFormat))
					w.WriteHeader(304)
					return
				}
				w.Header().Set("Cache-Control", "public, max-age=1")
				w.Header().Set("ETag", `"v1"`)
				_, _ = w.Write([]byte("x"))
			}))
			r := httptest.NewRequest("GET", "http://example.com/a", nil)
			h.ServeHTTP(httptest.NewRecorder(), r)
			now = now.Add(2 * time.Second)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			switch mode {
			case "swr":
				reasons(t, c, "stale_while_revalidate", "stale_reuse")
			case "validated":
				reasons(t, c, "revalidation_required", "validated_reuse")
			case "sie":
				reasons(t, c, "revalidation_required", "stale_if_error", "stale_reuse")
			case "private304":
				reasons(t, c, "response_private")
			}
		})
	}
	t.Run("fill-timeout", func(t *testing.T) {
		c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), Lock: true, LockTimeout: config.Duration(time.Millisecond), DecisionSamples: true})
		defer c.setDecisionSampling(false)
		started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			_, _ = w.Write([]byte("x"))
		}))
		go func() {
			defer close(done)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/a", nil))
		}()
		<-started
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/a", nil))
		close(release)
		<-done
		reasons(t, c, "fill_lock_timeout")
	})
	t.Run("disk-capture-error", func(t *testing.T) {
		c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1024, MaxEntrySize: 4096, DiskPath: t.TempDir(), DiskMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), DecisionSamples: true})
		defer c.setDecisionSampling(false)
		// A missing temporary-file parent injects an actual filesystem open failure.
		c.disk.dir = filepath.Join(t.TempDir(), "missing")
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "2048")
			_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/a", nil))
		reasons(t, c, "disk_capture_failed", "capture_not_stored")
	})
}

func TestInspectionSelectorRouteScopeAndExactKey(t *testing.T) {
	prefix, exact, regex := "prefix-route", "exact-route", "regex-route"
	cfg := &config.Config{Servers: []config.ServerConfig{{ServerNames: []string{"example.com", "second.example.com"}, Locations: []config.LocationConfig{
		{RouteID: &prefix, Match: config.MatchConfig{Type: "prefix", Path: "/app"}},
		{RouteID: &exact, Match: config.MatchConfig{Type: "exact", Path: "/only"}},
		{RouteID: &regex, Match: config.MatchConfig{Type: "regex", Path: "/regex.*"}},
	}}}}
	for _, tc := range []struct {
		id, path string
		match    bool
	}{{prefix, "/app/a", true}, {prefix, "/apple", true}, {prefix, "/other", false}, {exact, "/only?x=1", true}, {exact, "/only/more", false}} {
		s, err := ParseInspectionSelector(Selector{Kind: SelectorHost, Host: "example.com", RouteID: tc.id}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.MatchesResource("example.com", tc.path, nil); got != tc.match {
			t.Fatalf("route %s %s matched=%t", tc.id, tc.path, got)
		}
	}
	if _, err := ParseInspectionSelector(Selector{Kind: SelectorHost, Host: "example.com", RouteID: regex}, cfg); err == nil {
		t.Fatal("regex widened")
	}
	if _, err := ParseInspectionSelector(Selector{Kind: SelectorHost, Host: "example.com", RouteID: "unknown"}, cfg); err == nil {
		t.Fatal("unknown route accepted")
	}
	if _, err := ParseInspectionSelector(Selector{Kind: SelectorHost, Host: "example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), DecisionSamples: true})
	defer c.setDecisionSampling(false)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) }))
	for _, p := range []string{"/app/a", "/other"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com"+p, nil))
	}
	keySel, err := ParseInspectionSelector(Selector{Kind: SelectorKey, Key: keyFor("GET", "example.com", "/other"), RouteID: prefix}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.InspectScope(context.Background(), keySel)
	if err != nil || d.MatchedRepresentations != 0 || len(d.Decisions) != 0 {
		t.Fatal("key escaped route intersection", err, d)
	}
	exactSel := inspectSelector(t, SelectorExactURL, "/app/a")
	d, _ = c.InspectScope(context.Background(), exactSel)
	if len(d.Decisions) == 0 {
		t.Fatal("exact evidence unavailable")
	}
	otherKey, _ := ParseSelector(Selector{Kind: SelectorKey, Key: keyFor("GET", "example.com", "/app/a")}, nil)
	d, _ = c.InspectScope(context.Background(), otherKey)
	if len(d.Decisions) == 0 {
		t.Fatal("exact-key evidence unavailable")
	}
}

func BenchmarkCacheHitDecisionSampling(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprint(enabled), func(b *testing.B) {
			c, err := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 64 << 20, DefaultTTL: config.Duration(time.Minute), DecisionSamples: enabled}, testLogger())
			if err != nil {
				b.Fatal(err)
			}
			defer c.setDecisionSampling(false)
			h := c.Handler(benchNext)
			r := httptest.NewRequest("GET", "http://example.com/a", nil)
			h.ServeHTTP(httptest.NewRecorder(), r)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.ServeHTTP(httptest.NewRecorder(), r)
			}
		})
	}
}

func TestInspectionSSEValidationInProgress(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute), DecisionSamples: true})
	defer c.setDecisionSampling(false)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	n := 0
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("ETag", "v1")
		if n == 1 {
			w.Header().Set("Cache-Control", "max-age=60")
		} else {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(200)
		if n == 2 {
			close(started)
			<-release
		}
		w.Write([]byte("x"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/a", nil))
	req := httptest.NewRequest("GET", "http://example.com/a", nil)
	req.Header.Set("Cache-Control", "no-cache")
	go func() { h.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	<-started
	sel, _ := ParseSelector(Selector{Kind: SelectorExactURL, Host: "example.com", URI: "/a"}, nil)
	d, _ := c.InspectScope(context.Background(), sel)
	found := false
	for _, r := range d.Decisions {
		if r.Reason == "sse_bypass" {
			found = true
		}
	}
	close(release)
	<-done
	t.Logf("active validation decisions=%v", d.Decisions)
	if !found {
		t.Error("recorder SSE decision remains unobserved while validation handler runs")
	}
}
