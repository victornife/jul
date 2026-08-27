// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"jul/internal/config"
)

// ADR 0018 §16 labels its bounds conservative initial safety ceilings rather
// than benchmark-derived capacity limits, and requires #145 to measure the worst
// case at each maximum and record the numbers. These benchmarks are that
// measurement. Raising a ceiling later is additive and needs evidence; lowering
// an advertised one is breaking.
//
//	go test -bench='Benchmark.*Route.*(Path|Method|Header|Query)' -benchmem ./internal/router

// benchLocation compiles one location's predicates into a server block whose
// only candidate is that location.
func benchLocation(b *testing.B, m config.MatchConfig) *serverRoute {
	b.Helper()
	predicates, err := compilePredicates(config.LocationConfig{Match: m})
	if err != nil {
		b.Fatalf("compile predicates: %v", err)
	}
	return testServerRoute(&locationRoute{matchType: m.Type, path: m.Path, predicates: predicates})
}

// BenchmarkRouteSelectionPathOnly is the fast-path regression gate: a location
// with no predicates must not pay for the feature. Nothing is parsed, nothing
// is evaluated, and the enumeration selects the first path candidate it sees.
func BenchmarkRouteSelectionPathOnly(b *testing.B) {
	sr := benchLocation(b, config.MatchConfig{Type: "prefix", Path: "/api/"})
	req := selectRequest("/api/users", "version=v2&tenant=public")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if sr.selectLocation(req) == nil {
			b.Fatal("no selection")
		}
	}
}

func BenchmarkRouteSelectionMethodPredicate(b *testing.B) {
	sr := benchLocation(b, config.MatchConfig{Type: "prefix", Path: "/api/", Methods: []string{"GET", "POST"}})
	req := selectRequest("/api/users", "")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if sr.selectLocation(req) == nil {
			b.Fatal("no selection")
		}
	}
}

func BenchmarkRouteSelectionExactHeaderPredicate(b *testing.B) {
	value := "public"
	sr := benchLocation(b, config.MatchConfig{
		Type:    "prefix",
		Path:    "/api/",
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: &value}},
	})
	req := selectRequest("/api/users", "")
	req.Header.Set("X-Tenant", "public")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if sr.selectLocation(req) == nil {
			b.Fatal("no selection")
		}
	}
}

func BenchmarkRouteSelectionRegexHeaderPredicate(b *testing.B) {
	pattern := "^[0-9a-f]{32}$"
	sr := benchLocation(b, config.MatchConfig{
		Type:    "prefix",
		Path:    "/api/",
		Headers: []config.HeaderMatch{{Name: "X-Trace", Op: "regex", Value: &pattern}},
	})
	req := selectRequest("/api/users", "")
	req.Header.Set("X-Trace", strings.Repeat("ab", 16))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if sr.selectLocation(req) == nil {
			b.Fatal("no selection")
		}
	}
}

func BenchmarkRouteSelectionQueryPredicate(b *testing.B) {
	value := "v2"
	sr := benchLocation(b, config.MatchConfig{
		Type:  "prefix",
		Path:  "/api/",
		Query: []config.QueryMatch{{Name: "version", Op: "exact", Value: &value}},
	})
	req := selectRequest("/api/users", "version=v2&tenant=public")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if sr.selectLocation(req) == nil {
			b.Fatal("no selection")
		}
	}
}

// BenchmarkRouteSelectionHeaderPredicatesAtTheBound is the §16 worst case for
// configuration size: 16 header predicates on one location, of which 8 are
// 512-byte regexes, all satisfied so every one is evaluated.
func BenchmarkRouteSelectionHeaderPredicatesAtTheBound(b *testing.B) {
	// A 512-byte pattern that is expensive to write and cheap to be wrong
	// about: an alternation, so RE2 cannot reduce it to a literal scan.
	var pattern strings.Builder
	for pattern.Len() < config.MaxMatchHeaderPatternBytes-16 {
		fmt.Fprintf(&pattern, "tenant-%d|", pattern.Len())
	}
	pattern.WriteString("public")
	patternValue := pattern.String()
	if len(patternValue) > config.MaxMatchHeaderPatternBytes {
		b.Fatalf("pattern is %d bytes, over the %d-byte bound", len(patternValue), config.MaxMatchHeaderPatternBytes)
	}
	exactValue := "public"

	var headers []config.HeaderMatch
	for i := 0; i < config.MaxMatchHeaderRegexes; i++ {
		headers = append(headers, config.HeaderMatch{Name: fmt.Sprintf("X-Re-%d", i), Op: "regex", Value: &patternValue})
	}
	for i := len(headers); i < config.MaxMatchHeaders; i++ {
		headers = append(headers, config.HeaderMatch{Name: fmt.Sprintf("X-Exact-%d", i), Op: "exact", Value: &exactValue})
	}

	sr := benchLocation(b, config.MatchConfig{Type: "prefix", Path: "/api/", Headers: headers})
	req := selectRequest("/api/users", "")
	for _, h := range headers {
		req.Header.Set(h.Name, "public")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if sr.selectLocation(req) == nil {
			b.Fatal("no selection")
		}
	}
}

// BenchmarkRouteSelectionQueryPairsAtTheBound is the §16 worst case for
// request-time work: a query string carrying the full 1024-pair cap, with the
// predicate matching the last pair so the whole set is parsed and searched.
func BenchmarkRouteSelectionQueryPairsAtTheBound(b *testing.B) {
	var raw strings.Builder
	for i := 0; i < config.MaxQueryPairsParsed-1; i++ {
		fmt.Fprintf(&raw, "k%d=v%d&", i, i)
	}
	raw.WriteString("version=v2")

	value := "v2"
	sr := benchLocation(b, config.MatchConfig{
		Type:  "prefix",
		Path:  "/api/",
		Query: []config.QueryMatch{{Name: "version", Op: "exact", Value: &value}},
	})
	req := selectRequest("/api/users", raw.String())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if sr.selectLocation(req) == nil {
			b.Fatal("no selection")
		}
	}
}

// BenchmarkRouteSelectionCandidateFallthrough measures the miss path: many
// same-path candidates whose predicates all fail, so the enumeration walks the
// whole tier before selecting the unconstrained route at the bottom. This is
// also the shape §7's withdrawn performance argument was wrong about — the
// enumeration already visits every path candidate on a miss.
func BenchmarkRouteSelectionCandidateFallthrough(b *testing.B) {
	locs := make([]*locationRoute, 0, config.MaxMatchHeaders+1)
	for i := 0; i < 16; i++ {
		value := fmt.Sprintf("tenant-%d", i)
		predicates, err := compilePredicates(config.LocationConfig{Match: config.MatchConfig{
			Type:    "prefix",
			Path:    "/api/",
			Methods: []string{http.MethodPost},
			Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: &value}},
		}})
		if err != nil {
			b.Fatalf("compile predicates: %v", err)
		}
		locs = append(locs, &locationRoute{matchType: "prefix", path: "/api/", predicates: predicates})
	}
	locs = append(locs, &locationRoute{matchType: "prefix", path: "/api/"})
	sr := testServerRoute(locs...)

	req := selectRequest("/api/users", "")
	req.Header.Set("X-Tenant", "unmatched")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		loc := sr.selectLocation(req)
		if loc == nil || loc.predicates != nil {
			b.Fatal("expected the unconstrained route at the bottom of the tier")
		}
	}
}
