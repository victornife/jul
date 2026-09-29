// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package router maps incoming requests to handlers based on the listen
// address, Host header, and request path, following a simplified subset of
// NGINX server_name and location semantics.
package router

import (
	"strconv"
	"strings"
)

// normalizeHost lowercases the host and strips any port suffix.
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	// Strip port. IPv6 literals are wrapped in brackets: [::1]:8080.
	if strings.HasPrefix(host, "[") {
		if i := strings.IndexByte(host, ']'); i >= 0 {
			rest := host[i+1:]
			if rest == "" || (strings.HasPrefix(rest, ":") && validHostPort(rest[1:])) {
				return host[:i+1]
			}
		}
		return ""
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		if strings.Count(host, ":") != 1 || !validHostPort(host[i+1:]) {
			return ""
		}
		host = host[:i]
	}
	return strings.TrimSuffix(host, ".")
}

func validHostPort(port string) bool {
	if port == "" {
		return false
	}
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(port)
	return err == nil && n <= 65535
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
