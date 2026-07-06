// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import "testing"

// FuzzHostScore checks that host matching never panics and only ever returns
// one of the defined scores (3 exact, 2 leading-wildcard, 0 no match) for
// arbitrary Host header input.
func FuzzHostScore(f *testing.F) {
	names := []string{"www.example.com", "*.example.com", "api.example.com"}
	seeds := []string{
		"www.example.com", "a.example.com", "EXAMPLE.com:8080",
		"[::1]:8080", "..", "*.example.com", "", "\x00",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, host string) {
		got := hostScore(names, normalizeHost(host))
		switch got {
		case 0, 2, 3:
		default:
			t.Fatalf("hostScore(%q) = %d, want one of {0,2,3}", host, got)
		}
	})
}

// FuzzMatchLocation checks that location resolution never panics for arbitrary
// request paths and always resolves a location when a "/" fallback is present.
func FuzzMatchLocation(f *testing.F) {
	sr := benchServerRoute()
	seeds := []string{
		"/health", "/api/v1/x", "/x.php", "/../../etc/passwd",
		"/", "", "//api//v1", "\x00", "/static/../secret",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if loc := sr.matchLocation(path); loc == nil {
			t.Fatalf("matchLocation(%q) = nil, want non-nil (fallback present)", path)
		}
	})
}
