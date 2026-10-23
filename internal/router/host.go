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

// normalizeServerName canonicalizes a configured server_name entry (trimmed,
// lowercased, and with any trailing DNS root dot removed).
func normalizeServerName(name string) string {
	if !needsServerNameNormalization(name) {
		return name
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// needsServerNameNormalization reports whether name has leading/trailing ASCII
// whitespace, a trailing DNS dot, uppercase ASCII, or non-ASCII bytes.
func needsServerNameNormalization(name string) bool {
	n := len(name)
	if n == 0 {
		return false
	}
	if name[0] <= ' ' || name[n-1] <= ' ' || name[n-1] == '.' {
		return true
	}
	for i := 0; i < n; i++ {
		c := name[i]
		if (c >= 'A' && c <= 'Z') || c >= 0x80 {
			return true
		}
	}
	return false
}

// normalizeServerNames returns a pre-normalized copy of configured server_names.
func normalizeServerNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = normalizeServerName(name)
	}
	return out
}

// hostScore reports how well host matches one of the server's names. Higher is
// better: 3 = exact, 2 = leading-wildcard (*.example.com), 0 = no match.
func hostScore(names []string, host string) int {
	best := 0
	for _, name := range names {
		name = normalizeServerName(name)
		switch {
		case name == host:
			return 3
		case strings.HasPrefix(name, "*."):
			// "*.example.com" matches "a.example.com" but not "example.com".
			suffix := name[1:] // ".example.com"
			if len(host) > len(suffix) && strings.HasSuffix(host, suffix) {
				if best < 2 {
					best = 2
				}
			}
		}
	}
	return best
}

// normalizedHostScore evaluates host against server_names that have already
// been normalized at router build time.
func normalizedHostScore(names []string, host string) int {
	best := 0
	for _, name := range names {
		switch {
		case name == host:
			return 3
		case strings.HasPrefix(name, "*."):
			// "*.example.com" matches "a.example.com" but not "example.com".
			suffix := name[1:] // ".example.com"
			if len(host) > len(suffix) && strings.HasSuffix(host, suffix) {
				if best < 2 {
					best = 2
				}
			}
		}
	}
	return best
}
