// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strconv"
	"testing"
)

// #147 §10 requires the typed patch API's cost at each field's documented
// maximum to be measured, not assumed — the same "measured, not assumed"
// standard ADR 0018 §16 already holds the router and response-policy
// middleware to (internal/middleware/responsepolicy_bench_test.go).
//
//	go test -bench=BenchmarkApplyPatch -benchmem ./internal/admin

// BenchmarkApplyPatchSetPredicatesAtBounds measures location_set_predicates
// with 16 methods, 16 header predicates, and 16 query predicates — the
// documented per-field maximums (docs/configuration.md's request-predicates
// bounds table).
func BenchmarkApplyPatchSetPredicatesAtBounds(b *testing.B) {
	methods := make([]string, 16)
	for i := range methods {
		methods[i] = "M" + strconv.Itoa(i)
	}
	headers := make([]headerPredicate, 16)
	for i := range headers {
		v := "v"
		headers[i] = headerPredicate{Name: "X-Bench-" + strconv.Itoa(i), Op: "exact", Value: &v}
	}
	query := make([]queryPredicate, 16)
	for i := range query {
		v := "v"
		query[i] = queryPredicate{Name: "q" + strconv.Itoa(i), Op: "exact", Value: &v}
	}
	req := patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"},
		MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Methods: &methods, Headers: &headers, Query: &query},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := crudConfig()
		if _, err := applyPatch(c, req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkApplyPatchResponseHeadersAtBound measures
// location_response_headers_set with the 32-operation bound
// (config.MaxResponseHeaderOps).
func BenchmarkApplyPatchResponseHeadersAtBound(b *testing.B) {
	ops := make([]responseHeaderOpPatch, 32)
	for i := range ops {
		ops[i] = responseHeaderOpPatch{Op: "set", Name: "X-Bench-" + strconv.Itoa(i), Value: strp("v")}
	}
	req := patchRequest{
		Op: "location_response_headers_set", Listen: ":8080", ServerNames: []string{"app.example"},
		MatchType: "prefix", Path: "/",
		ResponseHeaders: &ops,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := crudConfig()
		if _, err := applyPatch(c, req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkApplyPatchCORSSetAtBounds measures location_cors_set with every
// list field at its documented maximum (config.MaxCORSOrigins,
// MaxCORSAllowedMethods, MaxCORSAllowedHeaders, MaxCORSExposedHeaders).
func BenchmarkApplyPatchCORSSetAtBounds(b *testing.B) {
	origins := make([]string, 64)
	for i := range origins {
		origins[i] = "https://origin-" + strconv.Itoa(i) + ".example.test"
	}
	methods := make([]string, 32)
	for i := range methods {
		methods[i] = "M" + strconv.Itoa(i)
	}
	headers := make([]string, 64)
	for i := range headers {
		headers[i] = "X-Bench-" + strconv.Itoa(i)
	}
	exposed := make([]string, 32)
	for i := range exposed {
		exposed[i] = "X-Expose-" + strconv.Itoa(i)
	}
	req := patchRequest{
		Op: "location_cors_set", Listen: ":8080", ServerNames: []string{"app.example"},
		MatchType: "prefix", Path: "/",
		CORS: &corsPatch{
			Enabled:        true,
			AllowedOrigins: origins,
			AllowedMethods: methods,
			AllowedHeaders: headers,
			ExposedHeaders: exposed,
			MaxAge:         strp("10m"),
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := crudConfig()
		if _, err := applyPatch(c, req); err != nil {
			b.Fatal(err)
		}
	}
}
