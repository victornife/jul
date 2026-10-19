// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/respwriter"
)

func TestWithdrawnGateDominatesRewritesPluginsPreflightAndHeaderDecoration(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	exp := config.Duration(time.Hour)

	pluginCalled := false
	actionCalled := false

	baseLoc := config.LocationConfig{
		RouteID:   strPtr("dash"),
		Match:     config.MatchConfig{Type: "prefix", Path: "/dash"},
		ProxyPass: "http://127.0.0.1:9999",
		Withdrawn: true,
		Expires:   &exp,
		Rewrites: []config.RewriteConfig{{
			Pattern:     "^/dash(.*)",
			Replacement: "https://redirect.example.com$1",
			Flag:        "redirect",
		}},
		CORS: &config.CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.com"},
			AllowedMethods: []string{"GET", "POST", "OPTIONS"},
			AllowedHeaders: []string{"Authorization", "Content-Type"},
		},
		ResponseHeaders: []config.ResponseHeaderOp{
			{Op: "set", Name: "X-Custom-Policy", Value: strPtr("preserved")},
			{Op: "set", Name: "Cache-Control", Value: strPtr("public, max-age=3600")},
			{Op: "remove", Name: "Cache-Control"},
		},
	}

	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      "127.0.0.1:8080",
			ServerNames: []string{"dash.example.com"},
			Locations:   []config.LocationConfig{baseLoc},
		}},
	}

	builders := map[string]Builder{
		ActionProxy: func(config.ServerConfig, config.LocationConfig) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				actionCalled = true
				w.WriteHeader(http.StatusOK)
			}), nil
		},
	}
	locMod := func(srv config.ServerConfig, loc config.LocationConfig) middleware.Middleware {
		cors := middleware.CompileCORS(loc.CORS)
		pf := middleware.Preflight(cors, nil, nil)
		return func(next http.Handler) http.Handler {
			h := next
			if pf != nil {
				h = pf(h)
			}
			_ = h
			// Simulate a request plugin that answers 200 Stop before everything else in loc.handler.
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pluginCalled = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("plugin-stop"))
			})
		}
	}

	rtr, err := New(cfg, builders, nil, locMod, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	h := rtr.For("127.0.0.1:8080")

	// 1. Ordinary GET that would match the redirect rewrite and plugin Stop, with unidentifiable RemoteAddr.
	reqGet := httptest.NewRequest(http.MethodGet, "http://dash.example.com/dash/secret", nil)
	reqGet.RemoteAddr = "invalid-remote-addr"
	recGet := httptest.NewRecorder()
	h.ServeHTTP(recGet, reqGet)

	if recGet.Code != http.StatusForbidden {
		t.Fatalf("withdrawn GET status = %d, want 403", recGet.Code)
	}
	if pluginCalled || actionCalled {
		t.Fatalf("withdrawn gate must run before plugins and action (pluginCalled=%v actionCalled=%v)", pluginCalled, actionCalled)
	}
	if gotCC := recGet.Header().Get("Cache-Control"); gotCC != "no-store" {
		t.Fatalf("Point 4: withdrawn Cache-Control = %q, want \"no-store\" overriding response_headers/expires", gotCC)
	}
	if gotExp := recGet.Header().Get("Expires"); gotExp != "" {
		t.Fatalf("Point 4: withdrawn Expires = %q, want empty", gotExp)
	}
	if gotCustom := recGet.Header().Get("X-Custom-Policy"); gotCustom != "preserved" {
		t.Fatalf("expected non-conflicting response_header X-Custom-Policy=preserved, got %q", gotCustom)
	}

	// 2. Approved CORS preflight OPTIONS request must also be denied 403 by the stage-0 gate, never 204.
	reqOpt := httptest.NewRequest(http.MethodOptions, "http://dash.example.com/dash/secret", nil)
	reqOpt.Header.Set("Origin", "https://app.example.com")
	reqOpt.Header.Set("Access-Control-Request-Method", "POST")
	recOpt := httptest.NewRecorder()
	h.ServeHTTP(recOpt, reqOpt)
	if recOpt.Code != http.StatusForbidden {
		t.Fatalf("withdrawn OPTIONS preflight status = %d, want 403", recOpt.Code)
	}
	if recOpt.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("withdrawn OPTIONS must not grant CORS headers, got %v", recOpt.Header())
	}

	// 3. Non-withdrawn route with ExchangeRegistry in request context admits and releases the exchange.
	cfgActive, _ := cfg.Clone()
	cfgActive.Servers[0].Locations[0].Withdrawn = false
	cfgActive.Servers[0].Locations[0].Rewrites = nil
	rtrActive, err := New(cfgActive, builders, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("router.New active: %v", err)
	}
	reg := respwriter.NewExchangeRegistry()
	reg.SetInitialGeneration(7)
	reqActive := httptest.NewRequest(http.MethodGet, "http://dash.example.com/dash/ok", nil)
	reqActive = reqActive.WithContext(respwriter.WithExchangeRegistry(reqActive.Context(), reg, 7))
	recActive := httptest.NewRecorder()
	rtrActive.For("127.0.0.1:8080").ServeHTTP(recActive, reqActive)
	if recActive.Code != http.StatusOK || !actionCalled {
		t.Fatalf("expected active route to execute action with 200, got %d", recActive.Code)
	}

	// 3. Recovery: setting Withdrawn = false restores exact prior behavior (redirect rewrite 302).
	cfg.Servers[0].Locations[0].Withdrawn = false
	rtrRecovered, err := New(cfg, builders, nil, locMod, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("router.New recovery: %v", err)
	}
	recRecov := httptest.NewRecorder()
	rtrRecovered.For("127.0.0.1:8080").ServeHTTP(recRecov, httptest.NewRequest(http.MethodGet, "http://dash.example.com/dash/secret", nil))
	if recRecov.Code != http.StatusFound || recRecov.Header().Get("Location") != "https://redirect.example.com/secret" {
		t.Fatalf("expected recovery to restore 302 redirect rewrite, got status=%d Location=%q", recRecov.Code, recRecov.Header().Get("Location"))
	}
}
