// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"jul/internal/cache"
	"jul/internal/config"
)

func TestWithdrawnRouteRejectsRetiredFill(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.Header.Get("X-Fixture-Generation") == "old" {
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
	var retired []func()
	defer func() {
		for _, fn := range retired {
			if fn != nil {
				fn()
			}
		}
	}()
	build := func(withdrawn bool) http.Handler {
		t.Helper()
		cfg := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{
			Listen: "127.0.0.1:0",
			Locations: []config.LocationConfig{{
				RouteID: &id, Match: config.MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: origin.URL, Cache: true, Withdrawn: withdrawn,
			}},
		}}}
		handlers, retire, err := f.Build(context.Background(), cfg, true)
		if err != nil {
			t.Fatal(err)
		}
		retired = append(retired, retire)
		return handlers["127.0.0.1:0"]
	}
	req := func(generation string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		r.Header.Set("X-Fixture-Generation", generation)
		return r
	}
	old := build(false)
	oldRec := httptest.NewRecorder()
	done := make(chan struct{})
	defer releaseOnce.Do(func() { close(release) })
	go func() { defer close(done); old.ServeHTTP(oldRec, req("old")) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old fill did not reach the origin")
	}
	_ = build(true)
	current := build(false)
	first := httptest.NewRecorder()
	current.ServeHTTP(first, req("current"))
	if first.Header().Get("X-Cache") != "MISS" || first.Body.String() != "current" {
		t.Fatal("reactivated route did not fetch its current response")
	}
	releaseOnce.Do(func() { close(release) })
	<-done
	next := httptest.NewRecorder()
	current.ServeHTTP(next, req("current"))
	if oldRec.Body.String() != "old" || next.Body.String() != "current" {
		t.Fatal("retired fill became eligible after withdrawal and reactivation")
	}
}
