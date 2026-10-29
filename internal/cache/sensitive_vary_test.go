// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestSensitiveVaryAdmissionAndInspection(t *testing.T) {
	tests := []struct {
		name        string
		vary        []string
		auth        bool
		wantBlocked bool
	}{
		{"cookie", []string{"Cookie"}, false, true},
		{"authorization", []string{"authorization"}, true, true},
		{"quoted cookie", []string{`"Cookie"`}, false, true},
		{"quoted parameterized authorization", []string{`"Authorization";param=1`}, true, true},
		{"mixed list", []string{"Accept, cOoKiE, Accept-Encoding"}, false, true},
		{"split fields", []string{"Accept", "Accept-Language, AUTHORIZATION"}, true, true},
		{"ordinary vary", []string{"Accept"}, true, false},
		{"no vary", nil, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{
				MemoryMaxSize:   1 << 20,
				DefaultTTL:      config.Duration(time.Minute),
				DecisionSamples: true,
			})
			t.Cleanup(func() { c.setDecisionSampling(false) })
			var calls int
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Cache-Control", "public, max-age=60")
				for _, v := range tt.vary {
					w.Header().Add("Vary", v)
				}
				_, _ = w.Write([]byte("origin"))
			}))
			for i := 0; i < 2; i++ {
				req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
				req.Header.Set("Accept", "application/json")
				req.Header.Set("Cookie", "session=fixture-cookie-marker")
				if tt.auth {
					req.Header.Set("Authorization", "Bearer fixture-auth-marker")
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				want := stateMiss
				if i == 1 && !tt.wantBlocked {
					want = stateHit
				}
				if rec.Header().Get("X-Cache") != want || rec.Body.String() != "origin" {
					t.Fatalf("request %d: unexpected cache result", i)
				}
			}
			if tt.wantBlocked && calls != 2 || !tt.wantBlocked && calls != 1 {
				t.Fatalf("origin calls = %d", calls)
			}
			d, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/item"))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, decision := range d.Decisions {
				found = found || decision.Reason == "vary_sensitive_rejected"
			}
			if found != tt.wantBlocked {
				t.Fatalf("sensitive Vary decision present = %v", found)
			}
			raw, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("fixture-cookie-marker")) || bytes.Contains(raw, []byte("fixture-auth-marker")) {
				t.Fatal("inspection exposed a fixture credential")
			}
		})
	}
}

func seedSensitiveVaryDisk(t *testing.T, dir, method, header string) (*http.Request, string) {
	t.Helper()
	req := httptest.NewRequest(method, "http://example.com/legacy", nil)
	req.Header.Set(header, "fixture-legacy-marker")
	base := key(req)
	variant := variantKey(base, []string{header}, req)
	now := time.Now()
	entry := &Entry{
		Status:     http.StatusOK,
		Header:     http.Header{"Vary": {header}},
		Body:       []byte("legacy-body"),
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Hour),
		StaleUntil: now.Add(2 * time.Hour),
		Vary:       []string{header},
		VaryValues: map[string]string{header: varyRequestValue(req.Header, header)},
	}
	stub := &Entry{IsVaryStub: true, Vary: []string{header}, Variants: []string{variant}}
	d, err := newDiskStore(dir, 8<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	d.set(variant, entry)
	d.set(base, stub)
	if !d.hasKey(base) || !d.hasKey(variant) {
		t.Fatal("failed to seed legacy disk fixture")
	}
	return req, variant
}

func TestSensitiveVaryLegacyDiskRejectedAfterRestart(t *testing.T) {
	for _, header := range []string{"Cookie", "Authorization"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(header+"/"+method, func(t *testing.T) {
				dir := t.TempDir()
				req, variant := seedSensitiveVaryDisk(t, dir, method, header)
				c := newTestCache(t, largeCacheCfg(dir, 1<<20, 1<<20, 8<<20))
				origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=60")
					w.Header().Set("Vary", header)
					_, _ = w.Write([]byte("fresh-origin"))
				})
				h := c.Handler(origin)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "fresh-origin" {
					t.Fatalf("legacy representation was reused: state=%q body=%q", rec.Header().Get("X-Cache"), rec.Body.String())
				}
				if _, err := os.Stat(filepath.Join(dir, hashKey(variant))); !os.IsNotExist(err) {
					t.Fatalf("legacy variant cleanup: %v", err)
				}
				again := newTestCache(t, largeCacheCfg(dir, 1<<20, 1<<20, 8<<20))
				rec = httptest.NewRecorder()
				again.Handler(origin).ServeHTTP(rec, req)
				if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "fresh-origin" {
					t.Fatal("legacy representation was reused after another restart")
				}
			})
		}
	}
}

func TestSensitiveVaryCleanupFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	req, variant := seedSensitiveVaryDisk(t, dir, http.MethodGet, "Cookie")
	var logs bytes.Buffer
	cfg := largeCacheCfg(dir, 1<<20, 1<<20, 8<<20)
	c, err := New(cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c.disk.testRemoveFile = func(path string) error {
		return &os.PathError{Op: "remove", Path: path, Err: os.ErrPermission}
	}
	rec := httptest.NewRecorder()
	c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("origin"))
	})).ServeHTTP(rec, req)
	if rec.Body.String() != "origin" || rec.Header().Get("X-Cache") != stateMiss {
		t.Fatal("legacy entry was reused after cleanup failure")
	}
	failures, _, ok := c.DiskWriteHealth()
	if !ok || failures == 0 || !strings.Contains(logs.String(), "disk write failed") {
		t.Fatal("cleanup failure was not surfaced")
	}
	if strings.Contains(logs.String(), "fixture-legacy-marker") || strings.Contains(logs.String(), hashKey(variant)) {
		t.Fatal("cleanup log exposed a fixture credential or its cache-file hash")
	}
}

func TestSensitiveVaryOn304ForcesFullOriginResponse(t *testing.T) {
	for _, header := range []string{"Cookie", "Authorization"} {
		t.Run(header, func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
			var calls int
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Cache-Control", "public, no-cache")
				w.Header().Set("ETag", `"fixture"`)
				if r.Header.Get("If-None-Match") != "" {
					w.Header().Add("Vary", "Accept")
					w.Header().Add("Vary", strings.ToLower(header))
					w.WriteHeader(http.StatusNotModified)
					return
				}
				if calls == 1 {
					_, _ = w.Write([]byte("old-body"))
					return
				}
				w.Header().Set("Vary", header)
				_, _ = w.Write([]byte("new-body"))
			}))
			req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
			req.Header.Set(header, "fixture-marker")
			first := httptest.NewRecorder()
			h.ServeHTTP(first, req)
			second := httptest.NewRecorder()
			h.ServeHTTP(second, req)
			if first.Body.String() != "old-body" || second.Body.String() != "new-body" || second.Header().Get("X-Cache") == stateRevalidated || calls != 3 {
				t.Fatalf("304 did not force a full response: calls=%d", calls)
			}
		})
	}
}

func TestSensitiveVaryFillFollowersFetchTheirOwnResponses(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, Lock: true, LockTimeout: config.Duration(time.Second)})
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "Cookie")
		_, _ = w.Write([]byte(r.Header.Get("Cookie")))
	}))
	var wg sync.WaitGroup
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	for i, value := range []string{"session=fixture-alice", "session=fixture-bob"} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
		req.Header.Set("Cookie", value)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.ServeHTTP(recs[i], req)
		}(i)
		if i == 0 {
			<-entered
		}
	}
	deadline := time.Now().Add(time.Second)
	fk := key(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
	for {
		c.fillMu.Lock()
		call := c.fills[fk]
		c.fillMu.Unlock()
		if call != nil && call.joined.Load() >= 1 {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			wg.Wait()
			t.Fatal("fill follower did not join")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if recs[0].Body.String() != "session=fixture-alice" || recs[1].Body.String() != "session=fixture-bob" || calls.Load() != 2 {
		t.Fatal("fill follower received another request's response")
	}
}

func TestSensitiveVaryRangeFetchesOriginalRequest(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{
		MemoryMaxSize: 1 << 20,
		RangeEnabled:  true,
		MaxRanges:     1,
		DefaultTTL:    config.Duration(time.Minute),
	})
	var calls int
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "Cookie")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-1/4")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("fo"))
			return
		}
		_, _ = w.Write([]byte("full"))
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	req.Header.Set("Range", "bytes=0-1")
	req.Header.Set("Cookie", "session=fixture")
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusPartialContent || rec.Body.String() != "fo" || rec.Header().Get("X-Cache") != stateBypass {
			t.Fatal("range response was not forwarded from the original request")
		}
	}
	if calls != 4 {
		t.Fatalf("origin calls = %d, want two complete fills and two range fallbacks", calls)
	}
}

func TestSensitiveVary304ValidationFollowers(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var calls atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "public, no-cache")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			close(entered)
			<-release
			w.Header().Set("Vary", "Cookie")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if n == 1 {
			_, _ = w.Write([]byte("old-body"))
			return
		}
		w.Header().Set("Vary", "Cookie")
		_, _ = w.Write([]byte(r.Header.Get("Cookie")))
	}))
	warm := httptest.NewRecorder()
	h.ServeHTTP(warm, httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
	if warm.Body.String() != "old-body" {
		t.Fatal("failed to warm the fixture")
	}
	var wg sync.WaitGroup
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	for i, value := range []string{"session=fixture-alice", "session=fixture-bob"} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
		req.Header.Set("Cookie", value)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.ServeHTTP(recs[i], req)
		}(i)
		if i == 0 {
			<-entered
		}
	}
	awaitJoined(t, c, revalidateKey{key: key(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))}, 1)
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	if recs[0].Body.String() != "session=fixture-alice" || recs[1].Body.String() != "session=fixture-bob" || calls.Load() != 4 {
		t.Fatalf("304 validation followers did not fetch independent responses: calls=%d", calls.Load())
	}
}

func TestSensitiveVaryBackground304DiscardsOldEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	g := testLease(t, 1)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil), g)
	stale := staleEntry("old-body")
	stale.ETag = `"fixture"`
	c.set(key(req), stale)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Authorization")
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("new-body"))
	}))
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	if first.Body.String() != "old-body" || first.Header().Get("X-Cache") != stateStale {
		t.Fatal("the already-admitted stale request did not complete as expected")
	}
	waitDrained(t, c)
	next := httptest.NewRecorder()
	h.ServeHTTP(next, req)
	if next.Body.String() != "new-body" || next.Header().Get("X-Cache") != stateMiss {
		t.Fatal("the 304 allowed subsequent reuse of the old representation")
	}
}

func TestSensitiveVaryCleanupPreservesNewGeneration(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	base := key(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
	before := c.genSeq.Load()
	newEntry := &Entry{Status: http.StatusOK, Body: []byte("new")}
	c.set(base, newEntry)
	legacy := &Entry{IsVaryStub: true, Vary: []string{"Cookie"}}
	c.cleanupSensitiveVary(base, legacy, before)
	if got, ok := c.get(base); !ok || got != newEntry {
		t.Fatal("legacy cleanup removed a newer generation")
	}
}

func TestSensitiveVaryRealOrigin(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Add("Vary", "Accept")
		w.Header().Add("Vary", "cOoKiE")
		_, _ = w.Write([]byte(r.Header.Get("Cookie")))
	}))
	defer origin.Close()
	target, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	gateway := httptest.NewServer(c.Handler(httputil.NewSingleHostReverseProxy(target)))
	defer gateway.Close()
	for _, value := range []string{"session=fixture-alice", "session=fixture-bob"} {
		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/item", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", value)
		res, err := gateway.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body := new(bytes.Buffer)
		_, readErr := body.ReadFrom(res.Body)
		closeErr := res.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read origin response: %v; close: %v", readErr, closeErr)
		}
		if res.Header.Get("X-Cache") != stateMiss || body.String() != value {
			t.Fatal("request did not receive its own origin response")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("origin calls = %d, want 2", calls.Load())
	}
}

func TestSensitiveVary304ConnectionNominatedHeader(t *testing.T) {
	c, stored, now := mergeFixture(t)
	h := http.Header{
		"Connection": {"Vary"},
		"Vary":       {"Cookie"},
	}
	_, action, reason := c.merge304Decision(stored, h, now)
	if action != mergeDiscard || reason != "vary_sensitive_rejected" {
		t.Fatal("304 sensitive Vary was not rejected before hop-by-hop removal")
	}
}

func TestSensitiveVaryOuterHeaderSubtractionDoesNotAllowReuse(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{
		MemoryMaxSize:   1 << 20,
		DefaultTTL:      config.Duration(time.Minute),
		DecisionSamples: true,
	})
	t.Cleanup(func() { c.setDecisionSampling(false) })
	var calls int
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "Cookie")
		_, _ = w.Write([]byte(r.Header.Get("Cookie")))
	})
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pre-set the same Vary header on the shared ResponseWriter before the
		// cache handler runs; headerDifference must not mask the origin's Vary.
		w.Header().Set("Vary", "Cookie")
		c.Handler(origin).ServeHTTP(w, r)
	})
	for _, cookie := range []string{"session=alice", "session=bob"} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/sub", nil)
		req.Header.Set("Cookie", cookie)
		rec := httptest.NewRecorder()
		outer.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != cookie {
			t.Fatalf("cookie=%q: got X-Cache=%q body=%q", cookie, rec.Header().Get("X-Cache"), rec.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("origin calls = %d, want 2", calls)
	}
}
