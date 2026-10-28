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

func TestProxyVaryConnectionNominatedHeader(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "X-Removed")
		_, _ = fmt.Fprint(w, "origin:"+r.Header.Get("X-Removed"))
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
	id := "connection-fixture"
	cfg := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{Listen: "127.0.0.1:0", Locations: []config.LocationConfig{{
		RouteID: &id, Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: origin.URL, Cache: true,
	}}}}}
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
	request := func(connection string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		r.Header.Set("X-Removed", "fixture-value")
		if connection != "" {
			r.Header.Add("Connection", "keep-alive")
			r.Header.Add("Connection", connection)
		}
		rec := httptest.NewRecorder()
		handlers["127.0.0.1:0"].ServeHTTP(rec, r)
		return rec
	}
	first, stripped, again := request(""), request("x-removed"), request("")
	if first.Body.String() != "origin:fixture-value" || stripped.Body.String() != "origin:" ||
		stripped.Header().Get("X-Cache") == "HIT" || again.Body.String() != "origin:fixture-value" ||
		again.Header().Get("X-Cache") != "HIT" || calls.Load() != 2 {
		t.Fatalf("connection-nominated mismatch: first=%q stripped=%q again=%q state=%q calls=%d",
			first.Body.String(), stripped.Body.String(), again.Body.String(), again.Header().Get("X-Cache"), calls.Load())
	}
}
