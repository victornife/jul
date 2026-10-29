// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
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
