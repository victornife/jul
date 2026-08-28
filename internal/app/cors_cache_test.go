// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/observability"
)

// ADR 0018 §11's invariant is one-directional: no header contributed by a
// layer outside the cache may appear in a stored entry. CORS is the sharpest
// instance of this, because getting it wrong is a cross-origin leak, not
// merely a stale value: a stored Access-Control-Allow-Origin would let a
// cache HIT serve one origin's grant to a different one. This test proves the
// property end to end, through a real HandlerFactory-built handler tree with a
// real cache backend and a real backend server — not a unit-level assertion
// about one function.

func TestCORSResponseHeadersNeverEnterTheCacheAcrossOrigins(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("origin-independent body"))
	}))
	defer backend.Close()

	f, cleanup := minimalFactory(t)
	defer cleanup()
	log := observability.NewLogger(io.Discard, "info", "text")
	c, err := cache.New(config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}, log)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	if c == nil {
		t.Skip("cache.New returned nil — caching not available in this build")
	}
	f.Cache = c

	cfg := &config.Config{
		Cache: config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20},
		Servers: []config.ServerConfig{{
			Listen: "127.0.0.1:0",
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: backend.URL,
				Cache:     true,
				CORS: &config.CORSConfig{
					Enabled:        true,
					AllowedOrigins: []string{"https://a.example.test", "https://b.example.test"},
				},
			}},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	handlers, _, err := f.Build(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	h := handlers["127.0.0.1:0"]

	get := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://h/", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	first := get("https://a.example.test")
	if first.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first request X-Cache = %q, want MISS", first.Header().Get("X-Cache"))
	}
	if got := first.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
		t.Fatalf("first response Allow-Origin = %q", got)
	}

	second := get("https://b.example.test")
	if second.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second request X-Cache = %q, want HIT (same body, no Vary from the origin-independent upstream)", second.Header().Get("X-Cache"))
	}
	if got := second.Header().Get("Access-Control-Allow-Origin"); got != "https://b.example.test" {
		t.Fatalf("second response Allow-Origin = %q, want b's own grant, not a leaked/stale one from the MISS that populated the cache", got)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("bodies differ between MISS and HIT: %q vs %q", first.Body.String(), second.Body.String())
	}
}

// TestUpstreamVaryOriginStillCreatesCacheVariants proves the other half of §11:
// an upstream-authored Vary: Origin is not stripped and does create one
// variant per origin, unlike Jul's own CORS response headers, which never do.
func TestUpstreamVaryOriginStillCreatesCacheVariants(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Vary", "Origin")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body for " + r.Header.Get("Origin")))
	}))
	defer backend.Close()

	f, cleanup := minimalFactory(t)
	defer cleanup()
	log := observability.NewLogger(io.Discard, "info", "text")
	c, err := cache.New(config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}, log)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	if c == nil {
		t.Skip("cache.New returned nil — caching not available in this build")
	}
	f.Cache = c

	cfg := &config.Config{
		Cache: config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20},
		Servers: []config.ServerConfig{{
			Listen: "127.0.0.1:0",
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: backend.URL,
				Cache:     true,
				CORS: &config.CORSConfig{
					Enabled:        true,
					AllowedOrigins: []string{"https://a.example.test", "https://b.example.test"},
				},
			}},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	handlers, _, err := f.Build(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	h := handlers["127.0.0.1:0"]

	get := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://h/", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	first := get("https://a.example.test")
	second := get("https://b.example.test")

	if first.Header().Get("X-Cache") != "MISS" || second.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected two MISSes (one variant per origin): a=%q b=%q", first.Header().Get("X-Cache"), second.Header().Get("X-Cache"))
	}
	if first.Body.String() == second.Body.String() {
		t.Fatal("expected distinct variant bodies; the upstream's own Vary: Origin must not be stripped")
	}
}
