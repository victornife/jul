// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
)

func TestNamedCacheOwnersSurviveRouteReordering(t *testing.T) {
	newOrigin := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60")
			_, _ = w.Write([]byte(body))
		}))
	}
	a, b := newOrigin("route-a"), newOrigin("route-b")
	defer a.Close()
	defer b.Close()
	f, cleanup := minimalFactory(t)
	defer cleanup()
	cacheCfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}
	var err error
	f.Cache, err = cache.New(cacheCfg, f.Log)
	if err != nil {
		t.Fatal(err)
	}
	idA, idB, va, vb := "route-a", "route-b", "a", "b"
	routeA := config.LocationConfig{RouteID: &idA, Match: config.MatchConfig{Type: "prefix", Path: "/", Headers: []config.HeaderMatch{{Name: "X-Fixture-Route", Op: "exact", Value: &va}}}, ProxyPass: a.URL, Cache: true}
	routeB := config.LocationConfig{RouteID: &idB, Match: config.MatchConfig{Type: "prefix", Path: "/", Headers: []config.HeaderMatch{{Name: "X-Fixture-Route", Op: "exact", Value: &vb}}}, ProxyPass: b.URL, Cache: true}
	build := func(routes []config.LocationConfig) (http.Handler, func()) {
		t.Helper()
		cfg := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{Listen: "127.0.0.1:0", Locations: routes}}}
		handlers, retire, err := f.Build(context.Background(), cfg, true)
		if err != nil {
			t.Fatal(err)
		}
		return handlers["127.0.0.1:0"], retire
	}
	old, firstRetire := build([]config.LocationConfig{routeA, routeB})
	if firstRetire != nil {
		defer firstRetire()
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	req.Header.Set("X-Fixture-Route", "a")
	old.ServeHTTP(httptest.NewRecorder(), req)
	current, retire := build([]config.LocationConfig{routeB, routeA})
	if retire != nil {
		defer retire()
	}
	rec := httptest.NewRecorder()
	current.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != "HIT" || rec.Body.String() != "route-a" {
		t.Fatal("route reordering changed a continuously owned named route")
	}
}

func TestUnidentifiedCacheOwnerIsRenewedOnReload(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = fmt.Fprintf(w, "version-%d", version.Load())
	}))
	defer origin.Close()
	f, cleanup := minimalFactory(t)
	defer cleanup()
	cacheCfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}
	var err error
	f.Cache, err = cache.New(cacheCfg, f.Log)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:0",
		Locations: []config.LocationConfig{{
			Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: origin.URL, Cache: true,
		}},
	}}}
	build := func() (http.Handler, func()) {
		t.Helper()
		handlers, retire, err := f.Build(context.Background(), cfg, true)
		if err != nil {
			t.Fatal(err)
		}
		return handlers["127.0.0.1:0"], retire
	}
	old, firstRetire := build()
	if firstRetire != nil {
		defer firstRetire()
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	old.ServeHTTP(httptest.NewRecorder(), req)
	version.Store(2)
	current, retire := build()
	if retire != nil {
		defer retire()
	}
	rec := httptest.NewRecorder()
	current.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != "MISS" || rec.Body.String() != "version-2" {
		t.Fatal("route without durable identity reused a prior generation's owner")
	}
}
