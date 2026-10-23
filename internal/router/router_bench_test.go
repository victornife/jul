// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"fmt"
	"regexp"
	"testing"
)

func BenchmarkHostScore(b *testing.B) {
	names := []string{"www.example.com", "*.example.com", "api.example.com"}
	cases := []struct {
		name string
		host string
	}{
		{"exact", "api.example.com"},
		{"wildcard", "a.example.com"},
		{"miss", "other.test"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = hostScore(names, c.host)
			}
		})
	}
}

// benchServerRoute builds a representative server block with exact, prefix,
// regex, and "/" catch-all locations for matching benchmarks and fuzzing.
func benchServerRoute() *serverRoute {
	mk := func(typ, p string) *locationRoute {
		lr := &locationRoute{matchType: typ, path: p}
		if typ == "regex" {
			lr.re = regexp.MustCompile(p)
		}
		return lr
	}
	return testServerRoute(
		mk("exact", "/health"),
		mk("prefix", "/api/v1"),
		mk("prefix", "/api"),
		mk("prefix", "/static"),
		mk("regex", `\.php$`),
		mk("prefix", "/"),
	)
}

func BenchmarkSelectLocation(b *testing.B) {
	sr := benchServerRoute()
	cases := []struct {
		name string
		path string
	}{
		{"exact", "/health"},
		{"prefix", "/api/v1/users"},
		{"regex", "/index.php"},
		{"fallback", "/nothing/here"},
	}
	for _, c := range cases {
		req := selectRequest(c.path, "")
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = sr.selectLocation(req)
			}
		})
	}
}

func BenchmarkSelectLocationManyExact(b *testing.B) {
	locs := make([]*locationRoute, 0, 260)
	for i := 0; i < 256; i++ {
		locs = append(locs, &locationRoute{matchType: "exact", path: fmt.Sprintf("/exact/route-%03d", i)})
	}
	locs = append(locs,
		&locationRoute{matchType: "prefix", path: "/api/v1"},
		&locationRoute{matchType: "prefix", path: "/"},
	)
	sr := testServerRoute(locs...)

	cases := []struct {
		name string
		path string
	}{
		{"exact_last", "/exact/route-255"},
		{"prefix_after_256_exact", "/api/v1/users"},
	}
	for _, c := range cases {
		req := selectRequest(c.path, "")
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if sr.selectLocation(req) == nil {
					b.Fatal("expected match")
				}
			}
		})
	}
}
