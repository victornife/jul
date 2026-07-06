// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
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
// regex, and "/" fallback locations for matching benchmarks and fuzzing.
func benchServerRoute() *serverRoute {
	mk := func(typ, p string) *locationRoute {
		lr := &locationRoute{matchType: typ, path: p}
		if typ == "regex" {
			lr.re = regexp.MustCompile(p)
		}
		return lr
	}
	fb := mk("prefix", "/")
	return &serverRoute{
		locations: []*locationRoute{
			mk("exact", "/health"),
			mk("prefix", "/api/v1"),
			mk("prefix", "/api"),
			mk("prefix", "/static"),
			mk("regex", `\.php$`),
			fb,
		},
		fallback: fb,
	}
}

func BenchmarkMatchLocation(b *testing.B) {
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
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = sr.matchLocation(c.path)
			}
		})
	}
}
