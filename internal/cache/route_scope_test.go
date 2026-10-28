// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"jul/internal/config"
)

func scopedHandler(t *testing.T, c *Cache, origin http.Handler) (http.Handler, RouteScope) {
	t.Helper()
	owner, err := c.NewRouteOwner()
	if err != nil {
		t.Fatal(err)
	}
	compatibility, err := c.NewRouteCompatibility()
	if err != nil {
		t.Fatal(err)
	}
	scope := RouteScope{Owner: owner, Compatibility: compatibility}
	h, err := c.HandlerForRoute(origin, scope, c.Policy())
	if err != nil {
		t.Fatal(err)
	}
	return h, scope
}

func TestRouteOwnerIsolatesSameResource(t *testing.T) {
	for _, vary := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "vary"}[vary], func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
			origin := func(body string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=60")
					if vary {
						w.Header().Set("Vary", "Accept-Language")
					}
					_, _ = w.Write([]byte(body))
				})
			}
			a, _ := scopedHandler(t, c, origin("route-a"))
			b, _ := scopedHandler(t, c, origin("route-b"))
			for _, step := range []struct {
				handler http.Handler
				state   string
				body    string
			}{
				{a, stateMiss, "route-a"},
				{a, stateHit, "route-a"},
				{b, stateMiss, "route-b"},
				{b, stateHit, "route-b"},
				{a, stateMiss, "route-a"},
			} {
				req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				req.Header.Set("Accept-Language", "en")
				rec := httptest.NewRecorder()
				step.handler.ServeHTTP(rec, req)
				if rec.Header().Get("X-Cache") != step.state || rec.Body.String() != step.body {
					t.Fatalf("got state %q and body %q, want %q and %q", rec.Header().Get("X-Cache"), rec.Body.String(), step.state, step.body)
				}
			}
		})
	}
}

func TestRouteOwnerRejectsLegacyAndRestartEntries(t *testing.T) {
	dir := t.TempDir()
	cfg := largeCacheCfg(dir, 1<<20, 1<<20, 8<<20)
	c := newTestCache(t, cfg)
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write([]byte("origin"))
	})
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	c.Handler(origin).ServeHTTP(httptest.NewRecorder(), req)
	scoped, owner := scopedHandler(t, c, origin)
	rec := httptest.NewRecorder()
	scoped.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != stateMiss {
		t.Fatal("legacy entry was accepted without route ownership")
	}
	restarted := newTestCache(t, cfg)
	if _, err := restarted.HandlerForRoute(origin, owner, restarted.Policy()); err == nil {
		t.Fatal("owner from a prior process was accepted")
	}
	next, _ := scopedHandler(t, restarted, origin)
	rec = httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != stateMiss {
		t.Fatal("entry from a prior process was accepted without current ownership")
	}
}

func TestResponseHookBypassSkipsCacheAndPreservesInvalidation(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.Header.Get("X-Fixture-Identity") != "" {
			_, _ = w.Write([]byte("personal"))
		} else {
			_, _ = w.Write([]byte("anonymous"))
		}
	})
	h, _ := scopedHandler(t, c, origin)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	personal := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	personal.Header.Set("X-Fixture-Identity", "fixture")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, WithResponseHookBypass(personal))
	if rec.Header().Get("X-Cache") != stateBypass || rec.Body.String() != "personal" {
		t.Fatal("response-hook request did not bypass lookup and storage")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != stateHit || rec.Body.String() != "anonymous" {
		t.Fatal("response-hook bypass modified the anonymous entry")
	}
	post := WithResponseHookBypass(httptest.NewRequest(http.MethodPost, "http://example.test/item", nil))
	h.ServeHTTP(httptest.NewRecorder(), post)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != stateMiss {
		t.Fatal("unsafe request did not invalidate the cached representation")
	}
}

func TestOldRouteFillCannotBeReusedByNewOwner(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	entered, release := make(chan struct{}), make(chan struct{})
	old, _ := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write([]byte("old"))
	}))
	current, _ := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write([]byte("current"))
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); old.ServeHTTP(httptest.NewRecorder(), req) }()
	<-entered
	first := httptest.NewRecorder()
	current.ServeHTTP(first, req)
	close(release)
	wg.Wait()
	next := httptest.NewRecorder()
	current.ServeHTTP(next, req)
	if first.Body.String() != "current" || next.Body.String() != "current" {
		t.Fatal("a prior owner supplied a response to the current owner")
	}
}
