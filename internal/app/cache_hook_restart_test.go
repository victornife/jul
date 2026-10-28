// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
)

func TestResponseHookRemovalAfterRestartDoesNotReuseOldEntry(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = fmt.Fprintf(w, "version-%d", version.Load())
	}))
	defer origin.Close()
	cacheCfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20, DiskPath: t.TempDir(), DiskMaxSize: 8 << 20}
	id := "fixture-route"
	cfg := func(hook bool) *config.Config {
		c := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{
			Listen: "127.0.0.1:0",
			Locations: []config.LocationConfig{{
				RouteID: &id, Match: config.MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: origin.URL, Cache: true,
			}},
		}}}
		if hook {
			c.Plugins = map[string]config.PluginConfig{"p": v2Plugin("vary-cookie")}
			c.Servers[0].Locations[0].Plugins = []string{"p"}
		}
		return c
	}
	build := func(f *HandlerFactory, hook bool) http.Handler {
		t.Helper()
		handlers, _, err := f.Build(context.Background(), cfg(hook), true)
		if err != nil {
			t.Fatal(err)
		}
		return handlers["127.0.0.1:0"]
	}
	firstFactory, firstCleanup := minimalFactory(t)
	closeFirst := sync.OnceFunc(firstCleanup)
	defer closeFirst()
	firstCache, err := cache.New(cacheCfg, firstFactory.Log)
	if err != nil {
		t.Fatal(err)
	}
	firstFactory.Cache = firstCache
	old := build(firstFactory, true)
	if rec := get(old, "X-Op", "req-only"); rec.Header().Get("X-Cache") != "MISS" {
		t.Fatal("request-only execution did not populate the fixture")
	}
	if rec := get(old, "X-Op", "req-only"); rec.Header().Get("X-Cache") != "HIT" {
		t.Fatal("fixture did not reach a warm state")
	}
	closeFirst()
	version.Store(2)
	secondFactory, secondCleanup := minimalFactory(t)
	defer secondCleanup()
	secondCache, err := cache.New(cacheCfg, secondFactory.Log)
	if err != nil {
		t.Fatal(err)
	}
	secondFactory.Cache = secondCache
	current := build(secondFactory, false)
	rec := get(current)
	if rec.Header().Get("X-Cache") != "MISS" || rec.Body.String() != "version-2" {
		t.Fatal("hook removal after restart reused an unproven old entry")
	}
}
