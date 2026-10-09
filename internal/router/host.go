// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package router maps incoming requests to handlers based on the listen
// address, Host header, and request path, following a simplified subset of
// NGINX server_name and location semantics.
package router

import (
	"strings"

	"jul/internal/middleware"
)

// normalizeHost lowercases the host and strips any port suffix.
func normalizeHost(host string) string {
	return middleware.CanonicalHTTPHost(host)
}

// hostScore reports how well host matches one of the server's names. Higher is
// better: 3 = exact, 2 = leading-wildcard (*.example.com), 0 = no match.
func hostScore(names []string, host string) int {
	best := 0
	for _, name := range names {
		name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
		switch {
		case name == host:
			return 3
		case strings.HasPrefix(name, "*."):
			// "*.example.com" matches "a.example.com" but not "example.com".
			suffix := name[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				if best < 2 {
					best = 2
				}
			}
		}
	}
	return best
}
