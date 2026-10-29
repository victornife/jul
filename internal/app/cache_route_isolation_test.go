// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/versionid"
)

func TestCacheRouteOwnershipIsolatesPredicatesAndFallbackWithoutThrashing(t *testing.T) {
	newOrigin := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60")
			_, _ = w.Write([]byte(body))
		}))
	}
	a, b, fallback := newOrigin("route-a"), newOrigin("route-b"), newOrigin("fallback")
	defer a.Close()
	defer b.Close()
	defer fallback.Close()
	f, cleanup := minimalFactory(t)
	defer cleanup()
	cacheCfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}
	var err error
	f.Cache, err = cache.New(cacheCfg, f.Log)
	if err != nil {
		t.Fatal(err)
	}
	va, vb := "a", "b"
	cfg := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:0",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/", Headers: []config.HeaderMatch{{Name: "X-Fixture-Route", Op: "exact", Value: &va}}}, ProxyPass: a.URL, Cache: true},
			{Match: config.MatchConfig{Type: "prefix", Path: "/", Headers: []config.HeaderMatch{{Name: "X-Fixture-Route", Op: "exact", Value: &vb}}}, ProxyPass: b.URL, Cache: true},
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: fallback.URL, Cache: true},
		},
	}}}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	handlers, retire, err := f.Build(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if retire != nil {
		defer retire()
	}
	for _, step := range []struct {
		route, state, body string
	}{
		{"a", "MISS", "route-a"},
		{"a", "HIT", "route-a"},
		{"b", "MISS", "route-b"},
		{"b", "HIT", "route-b"},
		{"", "MISS", "fallback"},
		{"", "HIT", "fallback"},
		// Returning to route "a" and "b" hits their own cached representations
		// rather than thrashing a single shared slot.
		{"a", "HIT", "route-a"},
		{"b", "HIT", "route-b"},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		if step.route != "" {
			req.Header.Set("X-Fixture-Route", step.route)
		}
		rec := httptest.NewRecorder()
		handlers["127.0.0.1:0"].ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != step.state || rec.Body.String() != step.body {
			t.Fatalf("route %q got state %q and body %q, want %q and %q", step.route, rec.Header().Get("X-Cache"), rec.Body.String(), step.state, step.body)
		}
	}
}

func TestCacheMatcherFactoryWarmReloadPreflightAndRestart(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = fmt.Fprintf(w, "origin-%d", calls.Add(1))
	}))
	defer origin.Close()
	f, cleanup := minimalFactory(t)
	defer cleanup()
	key, _ := versionid.New(bytes.Repeat([]byte{0x73}, 32))
	f.Versions = key
	cacheCfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20}
	var err error
	f.Cache, err = cache.New(cacheCfg, f.Log)
	if err != nil {
		t.Fatal(err)
	}
	id, predicate := "stable-route", "before"
	cfg := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{Listen: "127.0.0.1:0", Locations: []config.LocationConfig{{
		RouteID: &id, Cache: true, ProxyPass: origin.URL,
		Match: config.MatchConfig{Type: "prefix", Path: "/", Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: &predicate}}},
	}}}}}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	build := func(factory *HandlerFactory, commit bool) http.Handler {
		t.Helper()
		handlers, retire, err := factory.Build(context.Background(), cfg, commit)
		if err != nil {
			t.Fatal(err)
		}
		if retire != nil {
			t.Cleanup(retire)
		}
		return handlers["127.0.0.1:0"]
	}
	request := func(h http.Handler, predicate, wantState, wantBody string) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://example.test/item", nil)
		r.Header.Set("X-Tenant", predicate)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("X-Cache") != wantState || w.Body.String() != wantBody {
			t.Fatalf("state=%s body=%q, want %s %q", w.Header().Get("X-Cache"), w.Body.String(), wantState, wantBody)
		}
	}
	h := build(f, true)
	request(h, "before", "MISS", "origin-1")
	h = build(f, true)
	request(h, "before", "HIT", "origin-1")
	// The candidate's changed predicate must not retire the live route until
	// the generation commits. Prepare/dry-run performs no manifest write.
	changed := "after"
	cfg.Servers[0].Locations[0].Match.Headers[0].Value = &changed
	_ = build(f, false)
	request(h, "before", "HIT", "origin-1")
	h = build(f, true)
	request(h, "after", "MISS", "origin-2")
	request(h, "after", "HIT", "origin-2")
	// Reconstruct factory/cache with the same durable version context.
	f2, cleanup2 := minimalFactory(t)
	defer cleanup2()
	f2.Versions = key
	f2.Cache, err = cache.New(cacheCfg, f2.Log)
	if err != nil {
		t.Fatal(err)
	}
	request(build(f2, true), "after", "HIT", "origin-2")
}
