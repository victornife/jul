// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package clientaddr derives one canonical client address per HTTP request and
// carries it, together with the direct transport peer, in the request context.
//
// It is the single place in Jul where forwarding headers are parsed. Every
// consumer (CIDR authentication, rate limiting, the WAF, access logs, upstream
// forwarding headers, the FastCGI environment) reads the derived identity
// instead of re-deriving it, so all of them agree about who the client is.
//
// The package is deliberately dependency-light: it imports only the standard
// library, so auth, middleware, waf, handler and admin can all use it without
// import cycles. It never mutates http.Request.RemoteAddr — the direct peer
// stays available to every consumer as an independent fact (ADR 0016).
package clientaddr

import (
	"context"
	"net/http"
	"net/netip"
)

// Source records which input produced the canonical client address. It is a
// bounded enum so it is safe as a metric label or a log field.
type Source uint8

const (
	// SourcePeer means the direct transport peer is the canonical client,
	// either because no forwarding header was trusted or because derivation
	// failed closed.
	SourcePeer Source = iota
	// SourceForwarded means the RFC 7239 Forwarded header supplied the chain.
	SourceForwarded
	// SourceXFF means the X-Forwarded-For header supplied the chain.
	SourceXFF
)

// String returns the stable lowercase identifier of the source.
func (s Source) String() string {
	switch s {
	case SourceForwarded:
		return "forwarded"
	case SourceXFF:
		return "xff"
	default:
		return "peer"
	}
}

// Result records why the canonical client address is what it is. Like Source it
// is a bounded enum, never a free-form message.
type Result uint8

const (
	// ResultAccepted means derivation completed normally. With no trusted
	// proxy in play this is the ordinary direct-connection outcome.
	ResultAccepted Result = iota
	// ResultUntrustedPeer means a forwarding header was present but ignored
	// because the direct peer is not a trusted proxy.
	ResultUntrustedPeer
	// ResultMalformed means the selected header could not be parsed into a
	// usable chain, so derivation failed closed to the direct peer.
	ResultMalformed
	// ResultTooManyHops means the selected chain exceeded max_hops, so
	// derivation failed closed to the direct peer.
	ResultTooManyHops
)

// String returns the stable lowercase identifier of the result.
func (r Result) String() string {
	switch r {
	case ResultUntrustedPeer:
		return "untrusted_peer"
	case ResultMalformed:
		return "malformed"
	case ResultTooManyHops:
		return "too_many_hops"
	default:
		return "accepted"
	}
}

// Identity is the immutable, request-scoped answer to "who is the client?".
//
// Client is the canonical client address; Peer is the direct transport peer.
// In a direct deployment they are equal. Consumers receive a copy, so no
// consumer can mutate the value another consumer reads.
//
// There is deliberately no chain field: netip.Addr is comparable and heap-free,
// while a chain would add a slice allocation to every request for a value
// almost nothing reads. Source and Result answer the diagnostic question.
type Identity struct {
	Client netip.Addr
	Peer   netip.Addr
	Source Source
	Result Result
}

// Spoofed reports whether a forwarding header was present and ignored because
// the direct peer is not trusted. It exists so diagnostics can distinguish an
// ordinary direct request from an attempted header injection.
func (i Identity) Spoofed() bool { return i.Result == ResultUntrustedPeer }

// ctxKey is the unexported context key for a *Identity.
type ctxKey struct{}

// NewContext returns a copy of ctx carrying id. The pointer is never exposed:
// FromContext returns a value, so a consumer cannot mutate shared state. A
// pooled Identity is deliberately not used because the background-lease seam
// lets a request context outlive the request.
func NewContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, &id)
}

// FromContext returns the identity stored in ctx. ok is false when no identity
// middleware ran, which is the case for the admin listener and for failures
// handled before the per-address chain.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(*Identity)
	if !ok || id == nil {
		return Identity{}, false
	}
	return *id, true
}

// Client returns the canonical client address for r. When no identity is
// present it falls back to the direct transport peer, which is the fail-closed
// answer: an absent policy never widens trust.
func Client(r *http.Request) netip.Addr {
	if id, ok := FromContext(r.Context()); ok && id.Client.IsValid() {
		return id.Client
	}
	return PeerFromRemoteAddr(r.RemoteAddr)
}

// Peer returns the direct transport peer of r. It reads the identity when one
// is present and parses RemoteAddr otherwise; both yield the same address.
func Peer(r *http.Request) netip.Addr {
	if id, ok := FromContext(r.Context()); ok && id.Peer.IsValid() {
		return id.Peer
	}
	return PeerFromRemoteAddr(r.RemoteAddr)
}

// PeerFromRemoteAddr parses an http.Request.RemoteAddr ("ip:port", or a bare
// address for synthetic requests) into a normalized address. It returns the
// zero netip.Addr when the value cannot be parsed; callers treat an invalid
// address as "unknown" and never as "trusted".
func PeerFromRemoteAddr(remote string) netip.Addr {
	if remote == "" {
		return netip.Addr{}
	}
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return normalize(ap.Addr())
	}
	if a, err := netip.ParseAddr(remote); err == nil {
		return normalize(a)
	}
	return netip.Addr{}
}

// normalize puts an address into the single comparable form used everywhere in
// this package: IPv4-mapped IPv6 becomes IPv4, and the zone is dropped so a
// link-local address cannot compare unequal to the prefix an operator wrote.
func normalize(a netip.Addr) netip.Addr {
	if !a.IsValid() {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}
