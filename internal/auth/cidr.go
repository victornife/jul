// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package auth implements per-location access control for the edge server:
// CIDR allow/deny lists, HTTP Basic authentication, JWT bearer-token validation
// against a JWKS endpoint, and forward-auth subrequests. Each method is built
// from config into an Authenticator whose Wrap method returns middleware that
// composes around a location's action without being an action itself.
package auth

import (
	"net/http"
	"net/netip"

	"jul/internal/clientaddr"
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
func (g cidrGate) allowed(addr netip.Addr) bool {
	if !addr.IsValid() {
		// An unidentifiable client cannot be matched; fail closed only when an
		// allow list is configured (which is an explicit positive gate).
		return len(g.allow) == 0
	}
	addr = addr.Unmap()
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

// clientAddr returns the canonical client address of the request: the address
// derived once by internal/clientaddr from the listener's trusted-proxy policy.
// It equals the transport peer for a direct client and for every request whose
// peer is not a trusted proxy, so a forwarding header from an untrusted sender
// can never move a client into or out of an allow/deny range. This package
// never parses a forwarding header itself.
func clientAddr(r *http.Request) netip.Addr { return clientaddr.Client(r) }

// cidrAllows evaluates the gate for one request.
//
// A degraded identity names a proxy hop rather than a client, so neither list
// can be evaluated against it: an allow list covering the proxy network would
// admit the request, and a deny list would let the real client slip past its
// own rule. Both are decided by attacker-supplied header content, so the gate
// fails closed rather than judging the proxy's address as if it were a client.
func cidrAllows(g cidrGate, r *http.Request) bool {
	if id, ok := clientaddr.FromContext(r.Context()); ok && !id.Attributed() {
		return false
	}
	return g.allowed(clientAddr(r))
}
