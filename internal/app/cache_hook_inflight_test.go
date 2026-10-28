// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
)

func TestRetiredFillCannotCrossResponseHookTransition(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.Header.Get("X-Fixture-Identity") == "old" {
			close(entered)
			<-release
			_, _ = w.Write([]byte("old"))
			return
		}
		_, _ = w.Write([]byte("current"))
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
	var retired []func()
	defer func() {
		for _, fn := range retired {
			if fn != nil {
				fn()
			}
		}
	}()
	build := func(hook bool) http.Handler {
		t.Helper()
		handlers, retire, err := f.Build(context.Background(), cfg(hook), true)
		if err != nil {
			t.Fatal(err)
		}
		if retire != nil {
			retired = append(retired, retire)
		}
		return handlers["127.0.0.1:0"]
	}
	old := build(false)
	oldRec := httptest.NewRecorder()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); old.ServeHTTP(oldRec, requestWithIdentity("old")) }()
	<-entered
	withHook := build(true)
	if rec := get(withHook, "X-Mode", "body"); rec.Header().Get("X-Cache") != "BYPASS" || rec.Body.String() != "current" {
		t.Fatal("new response-hook generation did not bypass the cache")
	}
	current := build(false)
	first := get(current, "X-Fixture-Identity", "current")
	if first.Header().Get("X-Cache") != "MISS" || first.Body.String() != "current" {
		t.Fatal("current generation did not fetch its own response")
	}
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	if oldRec.Body.String() != "old" {
		t.Fatal("the already-admitted old request did not complete")
	}
	next := get(current, "X-Fixture-Identity", "current")
	if next.Body.String() != "current" {
		t.Fatal("an old in-flight fill became eligible after hook removal")
	}
}

func requestWithIdentity(identity string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://h/", nil)
	req.Header.Set("X-Fixture-Identity", identity)
	return req
}
