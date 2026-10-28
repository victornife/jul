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

func TestWithdrawnRouteCannotRecoverPriorCacheOwner(t *testing.T) {
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
	cfg := func(withdrawn bool) *config.Config {
		return &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{
			Listen: "127.0.0.1:0",
			Locations: []config.LocationConfig{{
				RouteID: &id, Match: config.MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: origin.URL, Cache: true, Withdrawn: withdrawn,
			}},
		}}}
	}
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
		handlers, retire, err := f.Build(context.Background(), cfg(withdrawn), true)
		if err != nil {
			t.Fatal(err)
		}
		retired = append(retired, retire)
		return handlers["127.0.0.1:0"]
	}
	request := func(h http.Handler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.test/item", nil))
		return rec
	}
	active := build(false)
	request(active)
	if rec := request(active); rec.Header().Get("X-Cache") != "HIT" {
		t.Fatal("route did not warm the cache")
	}
	if rec := request(build(true)); rec.Code != http.StatusForbidden {
		t.Fatalf("withdrawn route returned %d, want 403", rec.Code)
	}
	version.Store(2)
	if rec := request(build(false)); rec.Header().Get("X-Cache") != "MISS" || rec.Body.String() != "version-2" {
		t.Fatalf("reactivated route reused an old response: state=%q body=%q", rec.Header().Get("X-Cache"), rec.Body.String())
	}
}
