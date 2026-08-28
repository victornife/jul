// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestResponsePolicyNilWhenNothingToDo(t *testing.T) {
	if ResponsePolicy(nil, nil) != nil {
		t.Error("no ops and no CORS should install no wrapper")
	}
}

func TestResponsePolicyAppliesOperationsInOrder(t *testing.T) {
	ops := []config.ResponseHeaderOp{
		{Op: "set", Name: "X-Frame-Options", Value: corsStrPtr("DENY")},
		{Op: "add", Name: "Set-Cookie", Value: corsStrPtr("a=1")},
		{Op: "add", Name: "Set-Cookie", Value: corsStrPtr("b=2")},
		{Op: "remove", Name: "X-Powered-By"},
	}
	mw := ResponsePolicy(ops, nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Powered-By", "jul")
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q", got)
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 2 || got[0] != "a=1" || got[1] != "b=2" {
		t.Errorf("Set-Cookie = %v, want both add values in order", got)
	}
	if got := rec.Header().Get("X-Powered-By"); got != "" {
		t.Errorf("X-Powered-By = %q, want removed", got)
	}
}

func TestResponsePolicyPassesInformationalResponsesThrough(t *testing.T) {
	ops := []config.ResponseHeaderOp{{Op: "set", Name: "X-Test", Value: corsStrPtr("v")}}
	mw := ResponsePolicy(ops, nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints) // 103
		w.WriteHeader(http.StatusNoContent)  // 204, the real final status
	}))
	sink := newClientSink()
	h.ServeHTTP(sink, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := sink.finalStatus(); got != http.StatusNoContent {
		t.Errorf("final status = %d, want 204 (the 103 must not latch)", got)
	}
	want := []int{http.StatusEarlyHints, http.StatusNoContent}
	if !slicesEqual(sink.statuses, want) {
		t.Errorf("statuses forwarded = %v, want %v", sink.statuses, want)
	}
	if got := sink.header.Get("X-Test"); got != "v" {
		t.Errorf("X-Test = %q, want the operation applied to the final status", got)
	}
}

func TestResponsePolicyAppliesOnceOnImplicitWrite(t *testing.T) {
	ops := []config.ResponseHeaderOp{{Op: "set", Name: "X-Test", Value: corsStrPtr("v")}}
	mw := ResponsePolicy(ops, nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body")) // no explicit WriteHeader
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Test"); got != "v" {
		t.Errorf("X-Test = %q, want the operation applied via the implicit 200", got)
	}
}

func TestResponsePolicyAppliesCORSToOrdinaryResponses(t *testing.T) {
	cors := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	mw := ResponsePolicy(nil, cors)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // CORS headers must reach error responses too
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://a.example.test")
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 preserved", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
		t.Errorf("Allow-Origin = %q, want it present even on a 401", got)
	}
}

func TestResponsePolicySkipsCORSOn101(t *testing.T) {
	cors := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	ops := []config.ResponseHeaderOp{{Op: "set", Name: "X-Test", Value: corsStrPtr("v")}}
	mw := ResponsePolicy(ops, cors)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://a.example.test")
	h.ServeHTTP(rec, r)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want no CORS surface on a 101 (§12)", got)
	}
	if got := rec.Header().Get("X-Test"); got != "v" {
		t.Errorf("X-Test = %q, want response_headers still applied on a 101", got)
	}
}

func TestResponsePolicySkipsDecorationForAMarkedGeneratedResponse(t *testing.T) {
	// Simulates what the preflight terminator does: mark the response and let
	// the outer policy wrapper see it come back up unmodified.
	ops := []config.ResponseHeaderOp{{Op: "set", Name: "X-Test", Value: corsStrPtr("v")}}
	cors := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	mw := ResponsePolicy(ops, cors)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		markGeneratedResponse(r)
		w.Header().Set("Access-Control-Allow-Origin", "https://a.example.test")
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/", nil)
	r.Header.Set("Origin", "https://a.example.test")
	h.ServeHTTP(rec, r)

	if got := rec.Header().Get("X-Test"); got != "" {
		t.Errorf("X-Test = %q, want generic response_headers skipped for a generated response", got)
	}
	if got := rec.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 {
		t.Errorf("Allow-Origin = %v, want exactly the terminator's own single value untouched", got)
	}
}
