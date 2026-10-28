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

func cgiScopedHandler(t *testing.T, c *Cache, scope RouteScope, origin http.Handler) http.Handler {
	t.Helper()
	h, err := c.HandlerForCGIRoute(origin, scope, c.Policy(), []string{"Client-Cert", "X-Configured"}, []string{"X-Forwarded-", "X-Ssl-Client-", "Ssl-Client-"}, true)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCGIVaryPolicyMatchesTransformsAndAliases(t *testing.T) {
	p := newCGIVaryPolicy([]string{"Client-Cert", "X-Configured"}, []string{"X-Forwarded-", "X-Ssl-Client-"})
	for _, tt := range []struct {
		name, vary, incoming, want string
	}{
		{"generated", "cLiEnT-cErT", "", "vary_cgi_rewrite_rejected"},
		{"generated underscore alias", "client_cert", "", "vary_cgi_rewrite_rejected"},
		{"configured", "Accept, x-configured", "", "vary_cgi_rewrite_rejected"},
		{"removed family", "X-Forwarded-Anything", "", "vary_cgi_rewrite_rejected"},
		{"removed alias family", "x_ssl_client_subject", "", "vary_cgi_rewrite_rejected"},
		{"incoming alias", "X-Preference", "X_Preference", "vary_cgi_rewrite_rejected"},
		{"unaffected", "Accept-Language", "", ""},
		{"unaffected header", "X-Preference", "X-Preference", ""},
		{"sensitive precedence", "X-Configured, cookie", "", "vary_sensitive_rejected"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			if tt.incoming != "" {
				r.Header.Set(tt.incoming, "fixture")
			}
			r = withProxyVaryPolicy(r, p)
			if got := varyRejectionHeader(r, nil, http.Header{"Vary": {tt.vary}}); got != tt.want {
				t.Fatalf("rejection = %q, want %q", got, tt.want)
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	for i := 0; i <= MaxProxyVaryNames; i++ {
		r.Header.Set(fmt.Sprintf("X-Fixture-%d", i), "v")
	}
	r = withProxyVaryPolicy(r, p)
	if got := varyRejectionHeader(r, nil, http.Header{"Vary": {"Accept"}}); got != "vary_cgi_rewrite_rejected" {
		t.Fatalf("oversized request-header set accepted: %q", got)
	}
	invalid := withProxyVaryPolicy(r, newCGIVaryPolicy(nil, []string{"invalid prefix"}))
	if got := varyRejectionHeader(invalid, nil, http.Header{"Vary": {"Accept"}}); got != "vary_cgi_rewrite_rejected" {
		t.Fatalf("incomplete CGI policy accepted: %q", got)
	}
}

func TestCGIVaryRejectsLegacyBeforeVariantKey(t *testing.T) {
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
					w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=60")
					w.Header().Set("Vary", "Client-Cert")
					_, _ = w.Write([]byte(strings.Repeat("old", 1024)))
				})
				legacy, scope := scopedHandler(t, c, old)
				req := httptest.NewRequest(method, "http://example.test/item", nil)
				req.Header.Set("Client-Cert", "fixture-spoof")
				legacy.ServeHTTP(httptest.NewRecorder(), req)
				base := key(req)
				if disk {
					if !c.disk.hasKey(base) {
						t.Fatal("legacy stub was not stored on disk")
					}
					c.mem.del(base)
				}
				guarded := withProxyVaryPolicy(req, newCGIVaryPolicy([]string{"Client-Cert"}, nil))
				if _, effective, ok := c.lookupWithOwner(base, guarded, &scope); ok || effective != base {
					t.Fatal("legacy transformed field was used for variant lookup")
				}
				origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=60")
					w.Header().Set("Vary", "Client-Cert")
					_, _ = w.Write([]byte("fresh"))
				})
				h := cgiScopedHandler(t, c, scope, origin)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Header().Get("X-Cache") != stateMiss {
					t.Fatalf("legacy entry reused: X-Cache=%q", rec.Header().Get("X-Cache"))
				}
				if disk {
					restarted := newTestCache(t, cfg)
					_, freshScope := scopedHandler(t, restarted, origin)
					rec = httptest.NewRecorder()
					cgiScopedHandler(t, restarted, freshScope, origin).ServeHTTP(rec, req)
					if rec.Header().Get("X-Cache") != stateMiss {
						t.Fatal("reconstructed legacy entry was reused")
					}
				}
			})
		}
	}
}

func TestCGIVary304CannotAuthorizeOldBody(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", failure), func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
			var calls int
			origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Cache-Control", "public, no-cache, stale-if-error=120")
				w.Header().Set("ETag", `"fixture"`)
				if r.Header.Get("If-None-Match") != "" {
					w.Header().Add("Vary", "Accept")
					w.Header().Add("Vary", "cLiEnT-CeRt")
					w.WriteHeader(http.StatusNotModified)
					return
				}
				if calls == 1 {
					_, _ = w.Write([]byte("old"))
				} else if failure {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte("unavailable"))
				} else {
					w.Header().Set("Vary", "Client-Cert")
					_, _ = w.Write([]byte("new"))
				}
			})
			_, scope := scopedHandler(t, c, origin)
			h := cgiScopedHandler(t, c, scope, origin)
			req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			h.ServeHTTP(httptest.NewRecorder(), req)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			want := "new"
			if failure {
				want = "unavailable"
			}
			if calls != 3 || rec.Body.String() != want || rec.Header().Get("X-Cache") == stateStale {
				t.Fatalf("unsafe 304: calls=%d body=%q state=%q", calls, rec.Body.String(), rec.Header().Get("X-Cache"))
			}
		})
	}
}

func TestCGIVaryConcurrentFillAndInvalidation(t *testing.T) {
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
		w.Header().Set("Vary", "Client-Cert")
		_, _ = w.Write([]byte(r.Header.Get("X-Fixture")))
	})
	_, scope := scopedHandler(t, c, origin)
	h := cgiScopedHandler(t, c, scope, origin)
	req := func(id string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		r.Header.Set("X-Fixture", id)
		r.Header.Set("Client-Cert", "same-spoof")
		return r
	}
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.ServeHTTP(recs[0], req("a")) }()
	awaitSignal(t, entered)
	go func() { defer wg.Done(); h.ServeHTTP(recs[1], req("b")) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.fillMu.Lock()
		call := c.fills[fillCoordKey{key: key(req("a")), scope: scope}]
		c.fillMu.Unlock()
		if call != nil && call.joined.Load() > 0 {
			break
		}
		if time.Now().After(deadline) {
			once.Do(func() { close(release) })
			wg.Wait()
			t.Fatal("follower did not join the fill")
		}
		time.Sleep(time.Millisecond)
	}
	once.Do(func() { close(release) })
	wg.Wait()
	if recs[0].Body.String() != "a" || recs[1].Body.String() != "b" || calls.Load() != 2 {
		t.Fatal("CGI fill shared a transformed representation")
	}
	sel, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "example.test", URI: "/item"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil || result.State != "succeeded" {
		t.Fatalf("purge state %q: %v", result.State, err)
	}
}

func TestCGIVaryUnaffectedVariantHitsAndInvalidates(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}})
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "Accept-Language")
		w.Header().Set("Cache-Tag", "article:42")
		_, _ = w.Write([]byte("representation"))
	})
	_, scope := scopedHandler(t, c, origin)
	h := cgiScopedHandler(t, c, scope, origin)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	req.Header.Set("Accept-Language", "en")
	for _, want := range []string{stateMiss, stateHit} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != want {
			t.Fatalf("state=%q, want %q", rec.Header().Get("X-Cache"), want)
		}
	}
	sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"article:42"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil || result.State != "succeeded" {
		t.Fatalf("purge state %q: %v", result.State, err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != stateMiss {
		t.Fatal("unaffected CGI variant survived invalidation")
	}
}

func TestCGIVaryBackground304DiscardsOldEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 1)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Client-Cert")
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
	h := cgiScopedHandler(t, c, scope, origin)
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	if first.Body.String() != "old" || first.Header().Get("X-Cache") != stateStale {
		t.Fatal("already admitted stale response did not complete")
	}
	waitDrained(t, c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "new" || rec.Header().Get("X-Cache") != stateMiss {
		t.Fatal("background CGI 304 allowed subsequent reuse")
	}
}

func TestCGIVaryRangeRejectsLegacyEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, RangeEnabled: true, MaxRanges: 1})
	origin := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60")
			w.Header().Set("Vary", "Client-Cert")
			http.ServeContent(w, r, "item", time.Time{}, strings.NewReader(body))
		})
	}
	legacy, scope := scopedHandler(t, c, origin("old-body"))
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	legacy.ServeHTTP(httptest.NewRecorder(), req)
	req.Header.Set("Range", "bytes=0-2")
	rec := httptest.NewRecorder()
	cgiScopedHandler(t, c, scope, origin("new-body")).ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "new" || rec.Header().Get("X-Cache") == stateHit {
		t.Fatalf("legacy CGI range reused: code=%d body=%q state=%q", rec.Code, rec.Body.String(), rec.Header().Get("X-Cache"))
	}
}

func TestCGIVary304FollowersFetchIndependently(t *testing.T) {
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
			w.Header().Set("Vary", "Client-Cert")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if n == 1 {
			_, _ = w.Write([]byte("old"))
		} else {
			w.Header().Set("Vary", "Client-Cert")
			_, _ = w.Write([]byte(r.Header.Get("X-Fixture")))
		}
	})
	_, scope := scopedHandler(t, c, origin)
	h := cgiScopedHandler(t, c, scope, origin)
	req := func(id string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		r.Header.Set("X-Fixture", id)
		return r
	}
	h.ServeHTTP(httptest.NewRecorder(), req("warm"))
	var wg sync.WaitGroup
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	wg.Add(2)
	go func() { defer wg.Done(); h.ServeHTTP(recs[0], req("a")) }()
	awaitSignal(t, entered)
	go func() { defer wg.Done(); h.ServeHTTP(recs[1], req("b")) }()
	awaitJoined(t, c, revalidateKey{key: key(req("")), scope: scope}, 1)
	once.Do(func() { close(release) })
	wg.Wait()
	if recs[0].Body.String() != "a" || recs[1].Body.String() != "b" || calls.Load() != 4 {
		t.Fatalf("unsafe CGI validation results: A=%q B=%q calls=%d", recs[0].Body.String(), recs[1].Body.String(), calls.Load())
	}
}
