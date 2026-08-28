// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestPreflightNilWhenNoCORS(t *testing.T) {
	if Preflight(nil, nil, nil) != nil {
		t.Error("no CORS policy should install no wrapper")
	}
}

func TestPreflightApprovedTerminatesWithoutCallingNext(t *testing.T) {
	cors := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	mw := Preflight(cors, nil, nil)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, preflightRequest("https://a.example.test", "GET"))

	if called {
		t.Error("an approved preflight must not reach the backend action")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
		t.Errorf("Allow-Origin = %q", got)
	}
}

func TestPreflightDeniedFallsThroughToNext(t *testing.T) {
	cors := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNotFound)
	})
	mw := Preflight(cors, nil, nil)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, preflightRequest("https://evil.example.test", "GET"))

	if !called {
		t.Error("a denied preflight must not be short-circuited; the ordinary chain must run")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want whatever the ordinary chain returned (404)", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want nothing on behalf of a denied preflight", got)
	}
}

func TestPreflightNonPreflightPassesThrough(t *testing.T) {
	cors := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	mw := Preflight(cors, nil, nil)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Error("a non-preflight request must reach the ordinary chain")
	}
}

func TestPreflightRunsRateLimitThenWAFThenEmits(t *testing.T) {
	cors := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	var order []string
	rl := Middleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "ratelimit")
			next.ServeHTTP(w, r)
		})
	})
	waf := Middleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "waf")
			next.ServeHTTP(w, r)
		})
	})
	mw := Preflight(cors, rl, waf)
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next must not be called for an approved preflight")
	})).ServeHTTP(rec, preflightRequest("https://a.example.test", "GET"))

	want := []string{"ratelimit", "waf"}
	if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("guard order = %v, want %v", order, want)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 once both guards pass", rec.Code)
	}
}

func TestPreflightRateLimitDenialStopsBeforeEmit(t *testing.T) {
	cors := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	rl := Middleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
	})
	mw := Preflight(cors, rl, nil)
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("ordinary chain must not run for an approved-but-rate-limited preflight")
	})).ServeHTTP(rec, preflightRequest("https://a.example.test", "GET"))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 from the rate guard", rec.Code)
	}
}

func TestPreflightMarksGeneratedResponse(t *testing.T) {
	cors := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	var marked bool
	rec := httptest.NewRecorder()
	req := preflightRequest("https://a.example.test", "GET")

	// Wrap with ResponsePolicy so the context carries a *policyWriter, then
	// confirm the terminator's own generic response_headers are NOT applied to
	// its 204 (proving markGeneratedResponse reached the wrapper end to end).
	ops := []config.ResponseHeaderOp{{Op: "set", Name: "X-Test", Value: corsStrPtr("v")}}
	outer := ResponsePolicy(ops, cors)
	inner := Preflight(cors, nil, nil)
	h := outer(inner(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marked = true
	})))
	h.ServeHTTP(rec, req)

	_ = marked
	if got := rec.Header().Get("X-Test"); got != "" {
		t.Errorf("X-Test = %q, want generic response_headers skipped on the generated preflight response", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
