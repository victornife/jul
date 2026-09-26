// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/observability"
)

// These tests pin ADR 0020 §6 — where the jul-abi/v2 response point sits in
// the real HandlerFactory chain — against cache, compression, response_headers,
// CORS, auth-style denials, error pages and access-log byte accounting.

func v2GuestPath() string {
	return filepath.Join("..", "..", "testdata", "plugins", "testguest-v2.wasm")
}

func v2Plugin(op string) config.PluginConfig {
	return config.PluginConfig{Path: v2GuestPath(), ABI: config.PluginABIV2, Config: map[string]string{"op": op}}
}

// buildV2Handler builds the factory handler for one location proxying to
// backend with plugin p attached, after mutate adjusts the config.
func buildV2Handler(t *testing.T, backend string, p config.PluginConfig, mutate func(*config.Config)) (http.Handler, *HandlerFactory) {
	t.Helper()
	f, cleanup := minimalFactory(t)
	t.Cleanup(cleanup)
	cfg := &config.Config{
		Plugins: map[string]config.PluginConfig{"p": p},
		Servers: []config.ServerConfig{{
			Listen: "127.0.0.1:0",
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: backend,
				Plugins:   []string{"p"},
			}},
		}},
	}
	if mutate != nil {
		mutate(cfg)
	}
	if cfg.Cache.Enabled {
		c, err := cache.New(cfg.Cache, observability.NewLogger(io.Discard, "info", "text"))
		if err != nil || c == nil {
			t.Fatalf("cache.New: %v", err)
		}
		f.Cache = c
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	handlers, _, err := f.Build(context.Background(), cfg, true)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return handlers["127.0.0.1:0"], f
}

func get(h http.Handler, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://h/", nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestV2ResponsePhaseRunsOnCacheHitsAndCacheStoresOrigin: the hook runs for
// the miss and the hit, and the cache stores the pre-plugin representation
// (a non-idempotent transform proves it: "v1!" twice, never "v1!!").
func TestV2ResponsePhaseRunsOnCacheHitsAndCacheStoresOrigin(t *testing.T) {
	var hits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "v1")
	}))
	defer backend.Close()
	h, _ := buildV2Handler(t, backend.URL, v2Plugin("append"), func(c *config.Config) {
		c.Cache = config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}
		c.Servers[0].Locations[0].Cache = true
	})
	first := get(h, "X-Mode", "body")
	second := get(h, "X-Mode", "body")
	if first.Header().Get("X-Cache") != "MISS" || second.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("X-Cache = %q then %q", first.Header().Get("X-Cache"), second.Header().Get("X-Cache"))
	}
	if first.Body.String() != "v1!" || second.Body.String() != "v1!" {
		t.Fatalf("bodies = %q, %q; want the transform applied once per response to the stored origin", first.Body.String(), second.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("backend hits = %d", hits.Load())
	}
}

// TestV2ResponsePhaseSeesLogicalBodyUnderCompression: the guest transforms
// the identity body, Jul compresses the result for the client, and the access
// log counts the bytes actually sent.
func TestV2ResponsePhaseSeesLogicalBodyUnderCompression(t *testing.T) {
	var sawAE atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAE.Store(r.Header.Get("Accept-Encoding"))
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, strings.Repeat("hello ", 200))
	}))
	defer backend.Close()
	h, f := buildV2Handler(t, backend.URL, v2Plugin("upper"), func(c *config.Config) {
		c.Compression = config.CompressionConfig{Enabled: boolPtr(true), Encoders: []string{"gzip"}, Types: []string{"text/plain"}}
		c.Observability.AccessLog = config.AccessLogConfig{Sinks: []string{"stdout"}}
	})
	before := len(f.AccessLogTail.Snapshot(0))
	rec := get(h, "X-Mode", "body", "Accept-Encoding", "gzip")
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q", rec.Header().Get("Content-Encoding"))
	}
	wire := rec.Body.Len()
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if string(plain) != strings.Repeat("HELLO ", 200) {
		t.Fatalf("decoded body = %.40q", plain)
	}
	if ae, _ := sawAE.Load().(string); strings.Contains(ae, "br") {
		t.Fatalf("client Accept-Encoding reached the origin under a body subscription: %q", ae)
	}
	entries := f.AccessLogTail.Snapshot(0)
	if len(entries) != before+1 {
		t.Fatalf("access log entries = %+v", entries[before:])
	}
	if got := entries[len(entries)-1].Bytes; got != int64(wire) || got >= int64(len(plain)) {
		t.Fatalf("access log counted %d bytes; want the %d compressed bytes on the wire", got, wire)
	}
}

// TestV2ResponsePolicyAppliesAfterThePlugin: response_headers and CORS are
// applied after the hook, so a plugin cannot remove a security header or
// widen CORS on a location that has a policy.
func TestV2ResponsePolicyAppliesAfterThePlugin(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = io.WriteString(w, "x")
	}))
	defer backend.Close()
	deny := "DENY"
	h, _ := buildV2Handler(t, backend.URL, v2Plugin("echo"), func(c *config.Config) {
		loc := &c.Servers[0].Locations[0]
		loc.ResponseHeaders = []config.ResponseHeaderOp{{Op: "set", Name: "X-Frame-Options", Value: &deny}}
		loc.CORS = &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}}
	})
	rec := get(h, "Origin", "https://evil.example.test")
	if rec.Header().Get("X-Seen-Status") != "200" {
		t.Fatalf("hook did not run: %v", rec.Header())
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("X-Frame-Options = %q; response_headers must win", rec.Header().Get("X-Frame-Options"))
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("CORS widened to %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

// TestV2PolicyDenialsNeverReachTheHook: a request rejected by the location's
// policy layers (here: an IP-keyed rate limit) is not presented, so a plugin
// cannot turn it into a success.
func TestV2PolicyDenialsNeverReachTheHook(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "x") }))
	defer backend.Close()
	h, _ := buildV2Handler(t, backend.URL, v2Plugin("set-status"), func(c *config.Config) {
		c.Servers[0].Locations[0].RateLimit = &config.RateLimitConfig{Enabled: true, Rate: 1, Burst: 1, Key: "ip"}
	})
	if rec := get(h, "X-Status-To", "201"); rec.Code != 201 {
		t.Fatalf("first request: %d", rec.Code)
	}
	rec := get(h, "X-Status-To", "201")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("X-RC") != "" {
		t.Fatalf("rate-limit denial presented to the hook: %d %v", rec.Code, rec.Header())
	}
}

// TestV2ErrorResponsesArePresented: an upstream failure is an action response
// (502), so a policy plugin sees it; REJECT replaces it.
func TestV2ErrorResponsesArePresented(t *testing.T) {
	h, f := buildV2Handler(t, "http://127.0.0.1:1", v2Plugin("echo"), nil)
	if phases := f.PluginResponsePhases(); !phases["p"] || len(phases) != 1 {
		t.Fatalf("published response phases = %v", phases)
	}
	rec := get(h)
	if rec.Code != http.StatusBadGateway || rec.Header().Get("X-Seen-Status") != "502" {
		t.Fatalf("upstream error: %d %v", rec.Code, rec.Header())
	}
}

func boolPtr(b bool) *bool { return &b }
