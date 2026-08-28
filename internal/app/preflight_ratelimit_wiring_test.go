// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

// ADR 0018 §10's preflight rate guard is its own scope, evaluated a second
// time relative to the identity-aware limiter that guards actual requests.
// These tests exercise the real factory wiring (locPreflightRateLimit +
// middleware.Preflight), not a fake rate limiter, so a bug in how the two are
// spliced together would be caught here.

func preflightRateLimitConfig(t *testing.T, rl *config.RateLimitConfig) *config.Config {
	t.Helper()
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen: "127.0.0.1:0",
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/"},
				Return:    204,
				RateLimit: rl,
				CORS: &config.CORSConfig{
					Enabled:        true,
					AllowedOrigins: []string{"https://a.example.test"},
				},
			}},
		}},
	}
}

func doPreflight(h http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodOptions, "http://h/", nil)
	req.Header.Set("Origin", "https://a.example.test")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestPreflightRateLimitAppliesLocationOverride proves the guard is wired
// through the real factory: a per-location rate_limit of rate=1/burst=1 lets
// exactly one approved preflight through per window, then 429s the next.
func TestPreflightRateLimitAppliesLocationOverride(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := preflightRateLimitConfig(t, &config.RateLimitConfig{Enabled: true, Rate: 1, Burst: 1, Key: "ip"})
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	handlers, _, err := f.Build(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	h := handlers["127.0.0.1:0"]

	if rec := doPreflight(h); rec.Code != http.StatusNoContent {
		t.Fatalf("first preflight = %d, want 204", rec.Code)
	}
	if rec := doPreflight(h); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second preflight = %d, want 429 from the preflight's own rate scope", rec.Code)
	}
}

// TestPreflightRateLimitDisabledInstallsNoGuard proves a location with no
// effective rate policy gets no preflight guard, consistent with an ordinary
// request to the same route.
func TestPreflightRateLimitDisabledInstallsNoGuard(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := preflightRateLimitConfig(t, nil)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	handlers, _, err := f.Build(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	h := handlers["127.0.0.1:0"]

	for i := 0; i < 5; i++ {
		if rec := doPreflight(h); rec.Code != http.StatusNoContent {
			t.Fatalf("preflight %d = %d, want 204 (no rate policy, no guard)", i, rec.Code)
		}
	}
}

// TestPreflightRateLimitScopeIsSeparateFromTheRequestLimiter proves the
// preflight guard's own scope does not share a bucket with the ordinary
// per-request limiter guarding actual requests on the same route: exhausting
// the preflight bucket must not affect a normal GET to the same location.
func TestPreflightRateLimitScopeIsSeparateFromTheRequestLimiter(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := preflightRateLimitConfig(t, &config.RateLimitConfig{Enabled: true, Rate: 1, Burst: 1, Key: "ip"})
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	handlers, _, err := f.Build(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	h := handlers["127.0.0.1:0"]

	doPreflight(h) // consumes the preflight scope's single token
	if rec := doPreflight(h); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second preflight = %d, want 429", rec.Code)
	}

	get := httptest.NewRequest(http.MethodGet, "http://h/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("ordinary GET after the preflight scope was exhausted = %d, want 204 (separate scope)", rec.Code)
	}
}
