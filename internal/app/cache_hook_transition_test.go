// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
)

func TestResponseHookTransitionsDoNotRestoreOldEntries(t *testing.T) {
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
	id := "fixture-route"
	cfg := func(hook bool) *config.Config {
		c := &config.Config{
			Cache: cacheCfg,
			Servers: []config.ServerConfig{{
				Listen: "127.0.0.1:0",
				Locations: []config.LocationConfig{{
					RouteID: &id, Match: config.MatchConfig{Type: "prefix", Path: "/"},
					ProxyPass: origin.URL, Cache: true,
				}},
			}},
		}
		if hook {
			c.Plugins = map[string]config.PluginConfig{"p": v2Plugin("vary-cookie")}
			c.Servers[0].Locations[0].Plugins = []string{"p"}
		}
		return c
	}
	var retired []func()
	defer func() {
		for _, fn := range retired {
			if fn != nil {
				fn()
			}
		}
	}()
	build := func(c *config.Config, commit bool) http.Handler {
		t.Helper()
		if err := config.Validate(c); err != nil {
			t.Fatal(err)
		}
		handlers, retire, err := f.Build(context.Background(), c, commit)
		if err != nil {
			t.Fatal(err)
		}
		if retire != nil {
			retired = append(retired, retire)
		}
		return handlers["127.0.0.1:0"]
	}
	check := func(h http.Handler, wantState, wantBody string, headers ...string) {
		t.Helper()
		rec := get(h, headers...)
		if rec.Header().Get("X-Cache") != wantState || rec.Body.String() != wantBody {
			t.Fatalf("got state %q and body %q, want %q and %q", rec.Header().Get("X-Cache"), rec.Body.String(), wantState, wantBody)
		}
	}
	h := build(cfg(false), true)
	check(h, "MISS", "version-1")
	check(h, "HIT", "version-1")
	_ = build(cfg(true), false)
	h = build(cfg(false), true)
	check(h, "HIT", "version-1")

	version.Store(2)
	h = build(cfg(true), true)
	check(h, "BYPASS", "version-2", "X-Mode", "body")
	check(h, "MISS", "version-2", "X-Op", "req-only")
	check(h, "HIT", "version-2", "X-Op", "req-only")

	version.Store(21)
	replacement := cfg(true)
	replacement.Plugins["p"] = config.PluginConfig{
		Path: filepath.Join("..", "..", "testdata", "plugins", "v2-status-header.wasm"),
		ABI:  config.PluginABIV2,
	}
	h = build(replacement, true)
	check(h, "BYPASS", "version-21")
	h = build(cfg(true), true)
	check(h, "MISS", "version-21", "X-Op", "req-only")

	version.Store(3)
	h = build(cfg(false), true)
	check(h, "MISS", "version-3")
	check(h, "HIT", "version-3")

	version.Store(4)
	h = build(cfg(true), true)
	check(h, "BYPASS", "version-4", "X-Mode", "headers")
	version.Store(5)
	h = build(cfg(false), true)
	check(h, "MISS", "version-5")
}

func TestDeletedRouteIDDoesNotRecoverOldOwner(t *testing.T) {
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

	id := "fixture-route"
	location := config.LocationConfig{RouteID: &id, Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: origin.URL, Cache: true}
	cfg := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{Listen: "127.0.0.1:0", Locations: []config.LocationConfig{location}}}}
	var retired []func()
	defer func() {
		for _, fn := range retired {
			if fn != nil {
				fn()
			}
		}
	}()
	build := func(c *config.Config) http.Handler {
		t.Helper()
		handlers, retire, err := f.Build(context.Background(), c, true)
		if err != nil {
			t.Fatal(err)
		}
		if retire != nil {
			retired = append(retired, retire)
		}
		return handlers["127.0.0.1:0"]
	}
	h := build(cfg)
	get(h)
	empty := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{Listen: "127.0.0.1:0"}}}
	_ = build(empty)
	version.Store(2)
	h = build(cfg)
	rec := get(h)
	if rec.Header().Get("X-Cache") != "MISS" || rec.Body.String() != "version-2" {
		t.Fatal("reused route ID recovered an old ownership token")
	}
}

func TestRequestOnlyPluginTransitionsRotateCompatibility(t *testing.T) {
	for _, scope := range []string{"location", "server"} {
		t.Run(scope, func(t *testing.T) {
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
			id := "fixture-route"
			cfg := func(withPlugin bool) *config.Config {
				c := &config.Config{
					Cache: cacheCfg,
					Servers: []config.ServerConfig{{
						Listen: "127.0.0.1:0",
						Locations: []config.LocationConfig{{
							RouteID: &id, Match: config.MatchConfig{Type: "prefix", Path: "/"},
							ProxyPass: origin.URL, Cache: true,
						}},
					}},
				}
				if withPlugin {
					c.Plugins = map[string]config.PluginConfig{"p": {
						Path: filepath.Join("..", "..", "testdata", "plugins", "v1-current-header-inject.wasm"),
					}}
					if scope == "server" {
						c.Servers[0].Plugins = []string{"p"}
					} else {
						c.Servers[0].Locations[0].Plugins = []string{"p"}
					}
				}
				return c
			}
			var retired []func()
			defer func() {
				for _, fn := range retired {
					if fn != nil {
						fn()
					}
				}
			}()
			checkGeneration := func(withPlugin bool, wantVersion int32) {
				t.Helper()
				version.Store(wantVersion)
				c := cfg(withPlugin)
				if err := config.Validate(c); err != nil {
					t.Fatal(err)
				}
				handlers, retire, err := f.Build(context.Background(), c, true)
				if err != nil {
					t.Fatal(err)
				}
				retired = append(retired, retire)
				h := handlers["127.0.0.1:0"]
				for _, wantState := range []string{"MISS", "HIT"} {
					rec := get(h)
					if rec.Header().Get("X-Cache") != wantState || rec.Body.String() != fmt.Sprintf("version-%d", wantVersion) {
						t.Fatalf("plugin=%t: got %q %q, want %q version-%d", withPlugin, rec.Header().Get("X-Cache"), rec.Body.String(), wantState, wantVersion)
					}
				}
			}
			checkGeneration(false, 1)
			checkGeneration(true, 2)
			checkGeneration(true, 3)
			checkGeneration(false, 4)
			checkGeneration(true, 5)
		})
	}
}
