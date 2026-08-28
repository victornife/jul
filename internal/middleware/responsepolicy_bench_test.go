// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

// ADR 0018 §16 requires the worst case at each maximum to be measured and
// recorded, not assumed. These benchmarks are that measurement.
//
//	go test -bench='Benchmark.*(ResponseHeader|CORS)' -benchmem ./internal/middleware

// BenchmarkNoPolicyFastPath is the regression gate: a location with neither
// response_headers nor cors must install no wrapper and allocate nothing.
func BenchmarkNoPolicyFastPath(b *testing.B) {
	mw := ResponsePolicy(nil, nil)
	if mw != nil {
		b.Fatal("expected no wrapper for a location with no policy")
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkResponseHeaderOps measures applying the full 32-operation bound
// (ADR 0018 §16) on every response.
func BenchmarkResponseHeaderOps(b *testing.B) {
	ops := make([]config.ResponseHeaderOp, MaxResponseHeaderOpsForBench)
	for i := range ops {
		v := "v"
		ops[i] = config.ResponseHeaderOp{Op: "set", Name: "X-Bench-" + itoaBench(i), Value: &v}
	}
	mw := ResponsePolicy(ops, nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// MaxResponseHeaderOpsForBench mirrors config.MaxResponseHeaderOps (32) without
// importing config's bound constant name directly into the benchmark's own
// naming, so the two are free to be read side by side in -bench output.
const MaxResponseHeaderOpsForBench = 32

func itoaBench(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// benchCORSPolicy compiles a policy at the §16 bound: 64 origins, 32 methods,
// 64 allowed headers, 32 exposed headers.
func benchCORSPolicy(b *testing.B) *CORSPolicy {
	b.Helper()
	origins := make([]string, 64)
	for i := range origins {
		origins[i] = "https://tenant-" + itoaBench(i) + ".example.test"
	}
	methods := make([]string, 32)
	for i := range methods {
		methods[i] = "M" + itoaBench(i)
	}
	headers := make([]string, 64)
	for i := range headers {
		headers[i] = "X-Header-" + itoaBench(i)
	}
	exposed := make([]string, 32)
	for i := range exposed {
		exposed[i] = "X-Expose-" + itoaBench(i)
	}
	return CompileCORS(&config.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   origins,
		AllowedMethods:   methods,
		AllowedHeaders:   headers,
		ExposedHeaders:   exposed,
		AllowCredentials: true,
	})
}

// BenchmarkCORSActualResponseAllowedOrigin measures ApplyToResponse's cost on
// an ordinary (non-preflight) response with an allowed origin, at the §16
// bound.
func BenchmarkCORSActualResponseAllowedOrigin(b *testing.B) {
	p := benchCORSPolicy(b)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://tenant-63.example.test")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h := http.Header{}
		p.ApplyToResponse(h, req)
	}
}

// BenchmarkCORSPreflightApproved measures the full decide-then-guard decision
// and response for an approved preflight at the §16 bound (64
// Access-Control-Request-Headers tokens).
func BenchmarkCORSPreflightApproved(b *testing.B) {
	p := benchCORSPolicy(b)
	tokens := make([]string, 64)
	for i := range tokens {
		tokens[i] = "X-Header-" + itoaBench(i)
	}
	acrh := strings.Join(tokens, ", ")

	mw := Preflight(p, nil, nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.Fatal("must not reach the backend action")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://tenant-63.example.test")
	req.Header.Set("Access-Control-Request-Method", "M0")
	req.Header.Set("Access-Control-Request-Headers", acrh)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkCORSPreflightDenied measures the pure evaluation cost when a
// preflight is denied (a disallowed origin), which must not run the rate/WAF
// guards or allocate a response.
func BenchmarkCORSPreflightDenied(b *testing.B) {
	p := benchCORSPolicy(b)
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://not-allowed.example.test")
	req.Header.Set("Access-Control-Request-Method", "M0")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := p.EvaluatePreflight(req); ok {
			b.Fatal("expected denial")
		}
	}
}
