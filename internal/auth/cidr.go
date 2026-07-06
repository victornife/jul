// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package auth implements per-location access control for the edge server:
// CIDR allow/deny lists, HTTP Basic authentication, JWT bearer-token validation
// against a JWKS endpoint, and forward-auth subrequests. Each method is built
// from config into an Authenticator whose Wrap method returns middleware that
// composes around a location's action without being an action itself.
package auth

import (
	"net"
	"net/http"
	"net/netip"
)

// cidrGate evaluates CIDR allow/deny lists against the client address. Deny
// takes precedence over Allow. When the allow list is non-empty a client must
// fall inside one of its ranges; an empty allow list permits any client not
// denied.
type cidrGate struct {
	allow []netip.Prefix
	deny  []netip.Prefix
}

// newCIDRGate parses allow/deny CIDR strings into a gate. The strings are
// validated at config load, so parse errors here are skipped defensively rather
// than failing closed at request time.
func newCIDRGate(allow, deny []string) cidrGate {
	return cidrGate{allow: parsePrefixes(allow), deny: parsePrefixes(deny)}
}

func parsePrefixes(cidrs []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

// empty reports whether the gate has no rules (and therefore allows everyone).
func (g cidrGate) empty() bool { return len(g.allow) == 0 && len(g.deny) == 0 }

// allowed reports whether the client address passes the gate.
func (g cidrGate) allowed(remoteAddr string) bool {
	addr, ok := addrOf(remoteAddr)
	if !ok {
		// An unparseable peer address cannot be matched; fail closed only when
		// an allow list is configured (which is an explicit positive gate).
		return len(g.allow) == 0
	}
	for _, p := range g.deny {
		if p.Contains(addr) {
			return false
		}
	}
	if len(g.allow) == 0 {
		return true
	}
	for _, p := range g.allow {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// addrOf extracts the netip.Addr from a "host:port" RemoteAddr, tolerating a
// bare host. The address is unmapped so an IPv4-in-IPv6 peer matches IPv4
// prefixes.
func addrOf(remoteAddr string) (netip.Addr, bool) {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// clientAddr returns the request's transport peer address (never a spoofable
// X-Forwarded-For value).
func clientAddr(r *http.Request) string { return r.RemoteAddr }
