// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

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
	"jul/internal/middleware"
)

func TestProxyVaryTransformedIdentityIsolation(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.URL.Path == "/safe" {
			w.Header().Set("Vary", "Accept-Language")
			_, _ = fmt.Fprint(w, r.Header.Get("Accept-Language"))
			return
		}
		w.Header().Add("Vary", "Accept-Language")
		w.Header().Add("Vary", "x-ClIeNt-Cn, Accept")
		_, _ = fmt.Fprint(w, r.Header.Get("X-Client-CN"))
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
	id := "proxy-vary-fixture"
	cfg := &config.Config{Cache: cacheCfg, Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:0",
		Locations: []config.LocationConfig{{
			RouteID:   &id,
			Match:     config.MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: origin.URL,
			Cache:     true,
			Headers:   map[string]string{"X-Client-CN": "$ssl_client_cn"},
		}},
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
	h := handlers["127.0.0.1:0"]
	request := func(path, identity, spoof string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
		r.Header.Set("Accept-Language", "en")
		if spoof != "" {
			r.Header.Set("X-Client-CN", spoof)
		}
		r = r.WithContext(middleware.WithPeerCertIdentity(r.Context(), &middleware.PeerCertIdentity{Verified: true, CN: identity}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	for _, step := range []struct {
		path, identity, spoof, state, body string
	}{
		{"/item", "fixture-a", "", "MISS", "fixture-a"},
		{"/item", "fixture-b", "", "MISS", "fixture-b"},
		{"/item", "fixture-a", "same-spoof", "MISS", "fixture-a"},
		{"/item", "fixture-b", "same-spoof", "MISS", "fixture-b"},
		{"/safe", "fixture-a", "", "MISS", "en"},
		{"/safe", "fixture-b", "", "HIT", "en"},
	} {
		rec := request(step.path, step.identity, step.spoof)
		if rec.Header().Get("X-Cache") != step.state || rec.Body.String() != step.body {
			t.Fatalf("path %s: state=%q body=%q; want %q %q", step.path, rec.Header().Get("X-Cache"), rec.Body.String(), step.state, step.body)
		}
	}
	if calls.Load() != 5 {
		t.Fatalf("origin calls = %d, want 5", calls.Load())
	}
}

func TestProxyVaryTransformedIdentityConcurrentFill(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Client-CN")
		if id == "fixture-a" {
			close(entered)
			<-release
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "X-Client-CN")
		_, _ = fmt.Fprint(w, id)
	}))
	defer origin.Close()
	f, cleanup := minimalFactory(t)
	defer cleanup()
	cfgCache := config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20, Lock: true}
	var err error
	f.Cache, err = cache.New(cfgCache, f.Log)
	if err != nil {
		t.Fatal(err)
	}
	id := "concurrent-fixture"
	cfg := &config.Config{Cache: cfgCache, Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:0",
		Locations: []config.LocationConfig{{
			RouteID:   &id,
			Match:     config.MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: origin.URL,
			Cache:     true,
			Headers:   map[string]string{"X-Client-CN": "$ssl_client_cn"},
		}},
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
	h := handlers["127.0.0.1:0"]
	request := func(identity string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		r.Header.Set("X-Client-CN", "same-spoof")
		r = r.WithContext(middleware.WithPeerCertIdentity(r.Context(), &middleware.PeerCertIdentity{Verified: true, CN: identity}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	doneA, doneB := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { doneA <- request("fixture-a") }()
	<-entered
	go func() { doneB <- request("fixture-b") }()
	once.Do(func() { close(release) })
	a, b := <-doneA, <-doneB
	if a.Body.String() != "fixture-a" || b.Body.String() != "fixture-b" || b.Header().Get("X-Cache") == "HIT" {
		t.Fatalf("concurrent responses A=%q B=%q B-cache=%q", a.Body.String(), b.Body.String(), b.Header().Get("X-Cache"))
	}
}
