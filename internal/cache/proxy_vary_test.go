// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"encoding/json"
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

func proxyScopedHandler(t *testing.T, c *Cache, scope RouteScope, origin http.Handler, names ...string) http.Handler {
	t.Helper()
	h, err := c.HandlerForProxyRoute(origin, scope, c.Policy(), names, true)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func withProxyVaryPolicy(r *http.Request, policy *proxyVaryPolicy) *http.Request {
	return withRouteScope(r, &RouteScope{proxyVary: policy})
}

func TestProxyVaryPolicyBoundsAndMatching(t *testing.T) {
	tests := []struct {
		name, connection string
		vary             []string
		want             string
	}{
		{"configured mixed case", "", []string{"Accept", "x-ID, Accept-Language"}, "vary_proxy_rewrite_rejected"},
		{"connection named", "Keep-Alive, x-extra", []string{"X-Extra"}, "vary_proxy_rewrite_rejected"},
		{"malformed connection", "X-Good, bad name", []string{"Accept"}, "vary_proxy_rewrite_rejected"},
		{"oversized connection", strings.Repeat("x", maxConnectionVaryBytes+1), []string{"Accept"}, "vary_proxy_rewrite_rejected"},
		{"oversized vary field", "", []string{strings.Repeat("x", maxProxyVaryFieldBytes+1)}, "vary_proxy_rewrite_rejected"},
		{"invalid vary field", "", []string{"invalid field"}, "vary_proxy_rewrite_rejected"},
		{"too many vary fields", "", []string{strings.TrimSuffix(strings.Repeat("x,", maxProxyVaryFields+1), ",")}, "vary_proxy_rewrite_rejected"},
		{"oversized vary list", "", []string{strings.Repeat("x", maxProxyVaryBytes+1)}, "vary_proxy_rewrite_rejected"},
		{"too many connection names", strings.TrimSuffix(strings.Repeat("a,", maxConnectionVaryNames+1), ",") + ",b", []string{"Accept"}, "vary_proxy_rewrite_rejected"},
		{"sensitive precedence", "", []string{"X-Id", "cOoKiE"}, "vary_sensitive_rejected"},
		{"unaffected", "X-Other", []string{"Accept-Language"}, ""},
		{"no vary", "bad name", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			if tt.connection != "" {
				r.Header.Set("Connection", tt.connection)
			}
			r = withProxyVaryPolicy(r, newProxyVaryPolicy([]string{"X-ID"}))
			h := make(http.Header)
			for _, line := range tt.vary {
				h.Add("Vary", line)
			}
			if got := varyRejectionHeader(r, nil, h); got != tt.want {
				t.Fatalf("rejection = %q, want %q", got, tt.want)
			}
		})
	}
	names := make([]string, MaxProxyVaryNames+1)
	for i := range names {
		names[i] = fmt.Sprintf("X-Fixture-%d", i)
	}
	req := withProxyVaryPolicy(httptest.NewRequest(http.MethodGet, "http://example.test/", nil), newProxyVaryPolicy(names))
	if got := varyRejectionHeader(req, nil, http.Header{"Vary": {"Accept"}}); got != "vary_proxy_rewrite_rejected" {
		t.Fatalf("over-limit policy rejection = %q", got)
	}
	req = withProxyVaryPolicy(httptest.NewRequest(http.MethodGet, "http://example.test/", nil), newProxyVaryPolicy([]string{"invalid name"}))
	if got := varyRejectionHeader(req, nil, http.Header{"Vary": {"Accept"}}); got != "vary_proxy_rewrite_rejected" {
		t.Fatalf("invalid policy rejection = %q", got)
	}
	var connection []string
	for i := 0; i <= maxConnectionVaryNames; i++ {
		connection = append(connection, fmt.Sprintf("x-%d", i))
	}
	req = httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Header.Set("Connection", strings.Join(connection, ","))
	req = withProxyVaryPolicy(req, newProxyVaryPolicy(nil))
	if got := varyRejectionHeader(req, nil, http.Header{"Vary": {"Accept"}}); got != "vary_proxy_rewrite_rejected" {
		t.Fatalf("over-limit connection rejection = %q", got)
	}
}

func TestProxyVaryRejectsExistingEntriesBeforeVariantLookup(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, method), func(t *testing.T) {
				cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, RangeEnabled: true}
				if disk {
					cfg = largeCacheCfg(t.TempDir(), 512, 1<<20, 8<<20)
					cfg.RangeEnabled = true
				}
				c := newTestCache(t, cfg)
				old := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=60")
					w.Header().Set("Vary", "X-Identity")
					_, _ = w.Write([]byte(strings.Repeat("old", 1024)))
				})
				legacy, scope := scopedHandler(t, c, old)
				req := httptest.NewRequest(method, "http://example.test/item", nil)
				req.Header.Set("X-Identity", "fixture-spoof")
				legacy.ServeHTTP(httptest.NewRecorder(), req)
				base := key(req)
				if disk {
					if !c.disk.hasKey(base) {
						t.Fatal("legacy Vary stub was not stored on disk")
					}
					c.mem.del(base)
				}
				guarded := withProxyVaryPolicy(req, newProxyVaryPolicy([]string{"X-Identity"}))
				if _, effective, ok := c.lookupWithOwner(base, guarded, &scope); ok || effective != base {
					t.Fatal("transformed legacy Vary was used to construct a variant lookup")
				}
				origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=60")
					w.Header().Set("Vary", "X-Identity")
					_, _ = w.Write([]byte("fresh"))
				})
				h := proxyScopedHandler(t, c, scope, origin, "X-Identity")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "fresh" {
					t.Fatalf("legacy entry reused: state=%s body=%q", rec.Header().Get("X-Cache"), rec.Body.String())
				}
				if disk {
					restarted := newTestCache(t, cfg)
					_, newScope := scopedHandler(t, restarted, origin)
					next := proxyScopedHandler(t, restarted, newScope, origin, "X-Identity")
					rec = httptest.NewRecorder()
					next.ServeHTTP(rec, req)
					if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "fresh" {
						t.Fatal("reconstructed legacy entry was reused")
					}
				}
			})
		}
	}
}

func TestProxyVary304RequiresFullResponseWithoutStaleFallback(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("originFailure=%t", failure), func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
			var calls int
			origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Cache-Control", "public, no-cache, stale-if-error=120")
				w.Header().Set("ETag", `"fixture"`)
				if r.Header.Get("If-None-Match") != "" {
					w.Header().Add("Vary", "Accept")
					w.Header().Add("Vary", "x-IdEnTiTy")
					w.WriteHeader(http.StatusNotModified)
					return
				}
				if calls == 1 {
					_, _ = w.Write([]byte("old-body"))
					return
				}
				if failure {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte("origin-unavailable"))
					return
				}
				w.Header().Set("Vary", "X-Identity")
				_, _ = w.Write([]byte("new-body"))
			})
			_, scope := scopedHandler(t, c, origin)
			h := proxyScopedHandler(t, c, scope, origin, "X-Identity")
			req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			first, second := httptest.NewRecorder(), httptest.NewRecorder()
			h.ServeHTTP(first, req)
			h.ServeHTTP(second, req)
			want, status := "new-body", http.StatusOK
			if failure {
				want, status = "origin-unavailable", http.StatusServiceUnavailable
			}
			if first.Body.String() != "old-body" || second.Body.String() != want || second.Code != status || calls != 3 {
				t.Fatalf("unsafe 304 transition: calls=%d status=%d body=%q", calls, second.Code, second.Body.String())
			}
		})
	}
}

func TestProxyVaryFillFollowerFetchesOwnResponse(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, Lock: true, LockTimeout: config.Duration(5 * time.Second)})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls atomic.Int32
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "X-Identity")
		_, _ = w.Write([]byte(r.Header.Get("X-Fixture")))
	})
	_, scope := scopedHandler(t, c, origin)
	h := proxyScopedHandler(t, c, scope, origin, "X-Identity")
	request := func(identity string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		r.Header.Set("X-Identity", "same-spoof")
		r.Header.Set("X-Fixture", identity)
		return r
	}
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.ServeHTTP(recs[0], request("fixture-a")) }()
	awaitSignal(t, entered)
	go func() { defer wg.Done(); h.ServeHTTP(recs[1], request("fixture-b")) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.fillMu.Lock()
		call := c.fills[fillCoordKey{key: key(request("fixture-a")), scope: scope}]
		c.fillMu.Unlock()
		if call != nil && call.joined.Load() >= 1 {
			break
		}
		if time.Now().After(deadline) {
			once.Do(func() { close(release) })
			wg.Wait()
			t.Fatal("follower did not join the in-flight fill")
		}
		time.Sleep(time.Millisecond)
	}
	once.Do(func() { close(release) })
	wg.Wait()
	if recs[0].Body.String() != "fixture-a" || recs[1].Body.String() != "fixture-b" || calls.Load() != 2 {
		t.Fatalf("wrong fill responses: A=%q B=%q calls=%d", recs[0].Body.String(), recs[1].Body.String(), calls.Load())
	}
}

func TestProxyVaryRangeDoesNotReuseOldEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, RangeEnabled: true, MaxRanges: 1})
	old := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "X-Identity")
		http.ServeContent(w, r, "item", time.Time{}, strings.NewReader("old-body"))
	})
	legacy, scope := scopedHandler(t, c, old)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	legacy.ServeHTTP(httptest.NewRecorder(), req)
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "X-Identity")
		http.ServeContent(w, r, "item", time.Time{}, strings.NewReader("new-body"))
	})
	h := proxyScopedHandler(t, c, scope, origin, "X-Identity")
	req.Header.Set("Range", "bytes=0-2")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "new" || rec.Header().Get("X-Cache") == stateHit {
		t.Fatalf("unsafe range response: status=%d body=%q cache=%q", rec.Code, rec.Body.String(), rec.Header().Get("X-Cache"))
	}
}

func TestProxyVary304FollowersFetchIndependently(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls atomic.Int32
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "public, no-cache")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			close(entered)
			<-release
			w.Header().Set("Vary", "X-Identity")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if n == 1 {
			_, _ = w.Write([]byte("old"))
			return
		}
		w.Header().Set("Vary", "X-Identity")
		_, _ = w.Write([]byte(r.Header.Get("X-Fixture")))
	})
	_, scope := scopedHandler(t, c, origin)
	h := proxyScopedHandler(t, c, scope, origin, "X-Identity")
	req := func(identity string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		r.Header.Set("X-Fixture", identity)
		return r
	}
	h.ServeHTTP(httptest.NewRecorder(), req("warm"))
	var wg sync.WaitGroup
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	wg.Add(2)
	go func() { defer wg.Done(); h.ServeHTTP(recs[0], req("fixture-a")) }()
	awaitSignal(t, entered)
	go func() { defer wg.Done(); h.ServeHTTP(recs[1], req("fixture-b")) }()
	awaitJoined(t, c, revalidateKey{key: key(req("")), scope: scope}, 1)
	once.Do(func() { close(release) })
	wg.Wait()
	if recs[0].Body.String() != "fixture-a" || recs[1].Body.String() != "fixture-b" || calls.Load() != 4 {
		t.Fatalf("unsafe 304 follower results: A=%q B=%q calls=%d", recs[0].Body.String(), recs[1].Body.String(), calls.Load())
	}
}

func TestProxyVaryBackground304DiscardsOldEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 1)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "X-Identity")
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("new"))
	})
	_, scope := scopedHandler(t, c, origin)
	stale := staleEntry("old")
	stale.ETag = `"fixture"`
	stale.routeOwner, stale.routeCompatibility = scope.Owner, scope.Compatibility
	c.set(key(req), stale)
	h := proxyScopedHandler(t, c, scope, origin, "X-Identity")
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	if first.Body.String() != "old" || first.Header().Get("X-Cache") != stateStale {
		t.Fatal("request admitted before background validation did not complete as stale")
	}
	waitDrained(t, c)
	next := httptest.NewRecorder()
	h.ServeHTTP(next, req)
	if next.Body.String() != "new" || next.Header().Get("X-Cache") != stateMiss {
		t.Fatal("background 304 allowed subsequent reuse")
	}
}

func TestProxyVaryDecisionIsBoundedAndRedacted(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DecisionSamples: true})
	t.Cleanup(func() { c.setDecisionSampling(false) })
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "X-Identity")
		_, _ = w.Write([]byte("origin"))
	})
	_, scope := scopedHandler(t, c, origin)
	h := proxyScopedHandler(t, c, scope, origin, "X-Identity")
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	req.Header.Set("X-Identity", "fixture-secret-marker")
	h.ServeHTTP(httptest.NewRecorder(), req)
	result, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/item"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range result.Decisions {
		found = found || decision.Reason == "vary_proxy_rewrite_rejected"
	}
	if !found {
		t.Fatal("bounded proxy Vary rejection was not recorded")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-secret-marker") {
		t.Fatal("decision sample exposed the request header value")
	}
}

func TestProxyVaryPreservesScopedInvalidation(t *testing.T) {
	for _, sel := range []Selector{
		{Kind: SelectorExactURL, Host: "example.test", URI: "/articles/42"},
		{Kind: SelectorPrefix, Host: "example.test", Prefix: "/articles"},
		{Kind: SelectorGlob, Host: "example.test", Pattern: "/articles/*"},
		{Kind: SelectorTag, Host: "example.test", Tags: []string{"article:42"}},
	} {
		t.Run(string(sel.Kind), func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}})
			origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=60")
				w.Header().Set("Vary", "Accept-Language")
				w.Header().Set("Cache-Tag", "article:42")
				_, _ = w.Write([]byte("origin"))
			})
			_, scope := scopedHandler(t, c, origin)
			h := proxyScopedHandler(t, c, scope, origin, "X-Identity")
			req := httptest.NewRequest(http.MethodGet, "http://example.test/articles/42", nil)
			req.Header.Set("Accept-Language", "en")
			h.ServeHTTP(httptest.NewRecorder(), req)
			second := httptest.NewRecorder()
			h.ServeHTTP(second, req)
			if second.Header().Get("X-Cache") != stateHit {
				t.Fatal("unaffected response did not hit")
			}
			normalized, err := ParseSelector(sel, nil)
			if err != nil {
				t.Fatal(err)
			}
			if preview := c.PreviewScope(normalized, PreviewOptions{}); preview.MatchedRepresentations == 0 {
				t.Fatal("preview omitted an unaffected representation")
			}
			result, err := c.InvalidateScope(context.Background(), normalized, InvalidateOptions{})
			if err != nil || result.State != "succeeded" {
				t.Fatalf("invalidation state=%q error=%v", result.State, err)
			}
			after := httptest.NewRecorder()
			h.ServeHTTP(after, req)
			if after.Header().Get("X-Cache") != stateMiss {
				t.Fatal("unaffected representation survived invalidation")
			}
		})
	}
}
