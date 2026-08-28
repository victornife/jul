// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
	"jul/internal/middleware"
)

// These tests exercise ADR 0018 §8-§11 end to end through a real *Router: the
// response-header/CORS wrapper and the location-scoped recover are always
// installed by buildServerRoute itself (never caller-supplied), and the
// preflight terminator is supplied here through locModifier exactly the way
// internal/app.HandlerFactory wires it in production, so this is the same
// composition order production uses, not a reimplementation of it.

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func corsStrPtr(s string) *string { return &s }

// policyLocModifier reproduces the one part of internal/app.HandlerFactory's
// locModifier this package cannot import: the CORS preflight terminator, built
// from the location's own CORS policy and a fresh rate limiter store per test.
func policyLocModifier(t *testing.T) LocationModifier {
	t.Helper()
	store := middleware.NewRateLimiterStore(context.Background(), 0, 0)
	return func(_ config.ServerConfig, loc config.LocationConfig) middleware.Middleware {
		cors := middleware.CompileCORS(loc.CORS)
		if cors == nil {
			return nil
		}
		var rl middleware.Middleware
		if loc.RateLimit != nil && loc.RateLimit.Enabled {
			lim := store.Scoped("preflight:test", loc.RateLimit.Rate, loc.RateLimit.Burst)
			rl = middleware.RateLimit(lim, middleware.RateKeyFunc("ip"), func() {})
		}
		pf := middleware.Preflight(cors, rl, nil)
		if pf == nil {
			return nil
		}
		return func(next http.Handler) http.Handler { return pf(next) }
	}
}

func corsWiringRouter(t *testing.T, loc config.LocationConfig, action http.Handler) *Router {
	t.Helper()
	loc.Match = config.MatchConfig{Type: "prefix", Path: "/"}
	if loc.Root == "" && !loc.Deny && loc.Redirect == "" {
		loc.Root = "/dummy" // any action.go-recognized field selects ActionStatic
	}
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen:    "127.0.0.1:80",
		Locations: []config.LocationConfig{loc},
	}}}
	builders := map[string]Builder{
		ActionStatic: func(config.ServerConfig, config.LocationConfig) (http.Handler, error) { return action, nil },
	}
	fallback := func(config.ServerConfig, config.LocationConfig) (http.Handler, error) { return action, nil }
	r, err := New(cfg, builders, fallback, policyLocModifier(t), testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func corsRequest(method, path, origin string) *http.Request {
	r := httptest.NewRequest(method, "http://h"+path, nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestWiringResponseHeadersAndCORSOnOrdinaryResponse(t *testing.T) {
	action := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r := corsWiringRouter(t, config.LocationConfig{
		ResponseHeaders: []config.ResponseHeaderOp{{Op: "set", Name: "X-Frame-Options", Value: corsStrPtr("DENY")}},
		CORS:            &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}},
	}, action)

	rec := httptest.NewRecorder()
	r.For("127.0.0.1:80").ServeHTTP(rec, corsRequest(http.MethodGet, "/", "https://a.example.test"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
		t.Errorf("Allow-Origin = %q", got)
	}
}

func TestWiringResponseHeadersAndCORSOnErrorResponse(t *testing.T) {
	// A denied action (403) must still carry the location's policy — CORS
	// headers on error responses is a named requirement (ADR 0018 §9).
	r := corsWiringRouter(t, config.LocationConfig{
		Deny: true,
		CORS: &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}},
	}, nil)

	rec := httptest.NewRecorder()
	r.For("127.0.0.1:80").ServeHTTP(rec, corsRequest(http.MethodGet, "/", "https://a.example.test"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
		t.Errorf("Allow-Origin = %q, want present on a 403", got)
	}
}

func TestWiringApprovedPreflightNeverReachesTheAction(t *testing.T) {
	called := false
	action := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	r := corsWiringRouter(t, config.LocationConfig{
		ResponseHeaders: []config.ResponseHeaderOp{{Op: "set", Name: "X-Test", Value: corsStrPtr("v")}},
		CORS:            &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}},
	}, action)

	rec := httptest.NewRecorder()
	req := corsRequest(http.MethodOptions, "/", "https://a.example.test")
	req.Header.Set("Access-Control-Request-Method", "GET")
	r.For("127.0.0.1:80").ServeHTTP(rec, req)

	if called {
		t.Error("an approved preflight must never reach the location's action")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("X-Test"); got != "" {
		t.Errorf("X-Test = %q, want generic response_headers not applied to the generated preflight response", got)
	}
}

func TestWiringDeniedPreflightFallsThroughToTheAction(t *testing.T) {
	called := false
	action := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	r := corsWiringRouter(t, config.LocationConfig{
		CORS: &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}},
	}, action)

	rec := httptest.NewRecorder()
	req := corsRequest(http.MethodOptions, "/", "https://evil.example.test")
	req.Header.Set("Access-Control-Request-Method", "GET")
	r.For("127.0.0.1:80").ServeHTTP(rec, req)

	if !called {
		t.Error("a denied preflight must fall through to the ordinary chain")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want nothing granted on behalf of a denied preflight", got)
	}
}

func TestWiringLocationRecoverCarriesThePolicyHeaders(t *testing.T) {
	action := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	r := corsWiringRouter(t, config.LocationConfig{
		ResponseHeaders: []config.ResponseHeaderOp{{Op: "set", Name: "X-Frame-Options", Value: corsStrPtr("DENY")}},
		CORS:            &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}},
	}, action)

	rec := httptest.NewRecorder()
	req := corsRequest(http.MethodGet, "/", "https://a.example.test")

	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("the location-scoped recover should have absorbed the panic, got: %v", rec)
			}
		}()
		r.For("127.0.0.1:80").ServeHTTP(rec, req)
	}()

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want the location's response_headers policy on the recovered 500", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
		t.Errorf("Allow-Origin = %q, want CORS applied to the recovered 500", got)
	}
}

func TestWiringNoPolicyLocationInstallsNoWrapper(t *testing.T) {
	// A location with neither response_headers nor cors must not carry any of
	// this machinery — the fast path every pre-ADR-0018 configuration takes.
	action := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r := corsWiringRouter(t, config.LocationConfig{}, action)

	rec := httptest.NewRecorder()
	r.For("127.0.0.1:80").ServeHTTP(rec, corsRequest(http.MethodGet, "/", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want nothing without a cors policy", got)
	}
}
