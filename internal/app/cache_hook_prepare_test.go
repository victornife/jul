// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/upstream"
)

func TestResponseHookOwnershipPrepareAbortAndPublish(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write([]byte("origin"))
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
	prepare := func(hook bool) (http.Handler, func() (upstream.SnapshotMap, func()), func()) {
		t.Helper()
		handlers, _, commit, abort, err := f.Prepare(context.Background(), cfg(hook))
		if err != nil {
			t.Fatal(err)
		}
		return handlers["127.0.0.1:0"], commit, abort
	}
	var retired []func()
	defer func() {
		for _, fn := range retired {
			if fn != nil {
				fn()
			}
		}
	}()
	publish := func(commit func() (upstream.SnapshotMap, func())) {
		_, retire := commit()
		if retire != nil {
			retired = append(retired, retire)
		}
	}
	old, commit, _ := prepare(false)
	publish(commit)
	get(old)
	if rec := get(old); rec.Header().Get("X-Cache") != "HIT" {
		t.Fatal("initial route did not warm")
	}
	_, _, abort := prepare(true)
	if rec := get(old); rec.Header().Get("X-Cache") != "HIT" {
		t.Fatal("prepare changed the serving route owner")
	}
	abort()
	hook, commit, _ := prepare(true)
	publish(commit)
	if rec := get(hook, "X-Mode", "body"); rec.Header().Get("X-Cache") != "BYPASS" {
		t.Fatal("published response-hook request did not bypass cache")
	}
	current, commit, _ := prepare(false)
	publish(commit)
	if rec := get(current); rec.Header().Get("X-Cache") != "MISS" {
		t.Fatal("hook removal restored an earlier owner")
	}
}
