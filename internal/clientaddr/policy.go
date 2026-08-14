// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package clientaddr

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strings"
)

// Public header identifiers accepted in [servers.client_address].
const (
	HeaderForwarded = "forwarded"
	HeaderXFF       = "x-forwarded-for"
)

// DefaultMaxHops bounds how many asserted hops a chain may contain before
// derivation fails closed. MaxHopsLimit bounds what an operator may configure.
const (
	DefaultMaxHops = 16
	MaxHopsLimit   = 255
)

// maxHeaderBytes bounds the total size of the selected forwarding header across
// all of its field lines. Beyond it the chain fails closed without being
// parsed, so an oversized header cannot drive parsing cost or log volume.
const maxHeaderBytes = 8 << 10

// DefaultForwardedHeaders returns the header preference used when
// forwarded_headers is omitted.
//
// Only X-Forwarded-For is enabled. A forwarding header may be believed only if
// the trusted proxy overwrites it on every request, and nearly every deployed
// proxy writes X-Forwarded-For while passing RFC 7239 Forwarded through
// untouched: defaulting to Forwarded would let a client behind such a proxy
// assert its own address. Enabling it is an explicit operator assertion that
// the proxy authors it.
func DefaultForwardedHeaders() []string {
	return []string{HeaderXFF}
}

// Policy is a compiled, immutable trusted-proxy policy for one listen address.
// It is built during reload preparation, so a malformed CIDR aborts the reload
// before the new handler tree is published.
//
// The zero value (and a nil *Policy) trusts no proxy: forwarding headers are
// never read and the canonical client is always the direct transport peer.
type Policy struct {
	trusted []netip.Prefix
	sources []Source
	maxHops int
}

// NewPolicy compiles a policy.
//
// trustedProxies entries are CIDR prefixes in canonical (host-bits-clear) form,
// or bare addresses meaning a single host. forwardedHeaders is the ordered
// preference list: nil selects DefaultForwardedHeaders, while an explicitly
// empty non-nil slice disables every forwarding header, leaving peer-only
// identity even for a trusted peer. maxHops of 0 selects DefaultMaxHops.
//
// It returns an error describing the first unusable entry; the caller is
// expected to have run configuration validation already, so an error here is a
// reload abort rather than a per-request condition.
func NewPolicy(trustedProxies, forwardedHeaders []string, maxHops int) (*Policy, error) {
	p := &Policy{maxHops: maxHops}
	if p.maxHops <= 0 {
		p.maxHops = DefaultMaxHops
	}
	if p.maxHops > MaxHopsLimit {
		return nil, fmt.Errorf("max_hops %d exceeds the maximum of %d", maxHops, MaxHopsLimit)
	}
	for _, raw := range trustedProxies {
		prefix, err := ParsePrefix(raw)
		if err != nil {
			return nil, err
		}
		p.trusted = append(p.trusted, prefix)
	}
	sortPrefixes(p.trusted)

	headers := forwardedHeaders
	if headers == nil {
		headers = DefaultForwardedHeaders()
	}
	seen := make(map[string]bool, len(headers))
	for _, raw := range headers {
		name := strings.ToLower(strings.TrimSpace(raw))
		if seen[name] {
			return nil, fmt.Errorf("duplicate forwarded header %q", raw)
		}
		seen[name] = true
		switch name {
		case HeaderForwarded:
			p.sources = append(p.sources, SourceForwarded)
		case HeaderXFF:
			p.sources = append(p.sources, SourceXFF)
		default:
			return nil, fmt.Errorf("unsupported forwarded header %q", raw)
		}
	}
	return p, nil
}

// ParsePrefix parses one trusted_proxies entry. A bare address is accepted and
// becomes a single-host prefix; a CIDR must already have its host bits clear so
// that a typo such as 10.1.2.3/8 cannot silently widen trust to a whole /8.
func ParsePrefix(raw string) (netip.Prefix, error) {
	entry := strings.TrimSpace(raw)
	if entry == "" {
		return netip.Prefix{}, fmt.Errorf("empty trusted proxy entry")
	}
	if !strings.Contains(entry, "/") {
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid trusted proxy %q: want an IP address or CIDR prefix", raw)
		}
		addr = normalize(addr)
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	prefix, err := netip.ParsePrefix(entry)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid trusted proxy %q: want an IP address or CIDR prefix", raw)
	}
	if prefix.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("invalid trusted proxy %q: write an IPv4-mapped prefix in IPv4 form", raw)
	}
	if prefix.Masked() != prefix {
		return netip.Prefix{}, fmt.Errorf("trusted proxy %q has host bits set; write %s to trust the whole range", raw, prefix.Masked())
	}
	return prefix, nil
}

// sortPrefixes orders prefixes so a policy's compiled form is deterministic and
// two configurations that differ only in listing order compare equal.
func sortPrefixes(prefixes []netip.Prefix) {
	sort.Slice(prefixes, func(i, j int) bool {
		if c := prefixes[i].Addr().Compare(prefixes[j].Addr()); c != 0 {
			return c < 0
		}
		return prefixes[i].Bits() < prefixes[j].Bits()
	})
}

// TrustedCount returns how many prefixes the policy trusts. It is bounded
// metadata suitable for an authenticated status projection; the prefixes
// themselves are configuration, not telemetry.
func (p *Policy) TrustedCount() int {
	if p == nil {
		return 0
	}
	return len(p.trusted)
}

// Trusts reports whether addr is one of the configured trusted proxies.
func (p *Policy) Trusts(addr netip.Addr) bool {
	if p == nil || !addr.IsValid() {
		return false
	}
	addr = normalize(addr)
	for _, prefix := range p.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Derive computes the canonical identity for one request from the direct
// transport peer and the request headers.
//
// The algorithm is fixed (ADR 0016 §4): an untrusted peer means every asserted
// header is ignored; a trusted peer selects at most one chain in configured
// order; the selected chain is walked right to left removing trusted hops; the
// first untrusted valid address wins; a fully trusted chain yields its leftmost
// address; anything malformed, oversized or over the hop limit fails closed to
// the peer.
func (p *Policy) Derive(peer netip.Addr, h http.Header) Identity {
	peer = normalize(peer)
	id := Identity{Client: peer, Peer: peer, Source: SourcePeer, Result: ResultAccepted}
	if !peer.IsValid() {
		// An unparseable peer is the one case where there is no anchor to fall
		// back to; report it rather than trusting an asserted address instead.
		id.Result = ResultMalformed
		return id
	}
	if p == nil || len(p.trusted) == 0 || len(p.sources) == 0 {
		return id
	}
	trusted := p.Trusts(peer)
	for _, source := range p.sources {
		values := h.Values(headerName(source))
		if !hasValue(values) {
			continue
		}
		if !trusted {
			id.Result = ResultUntrustedPeer
			return id
		}
		hops, err := parseChain(source, values, p.maxHops)
		switch {
		case errors.Is(err, errTooManyHops):
			id.Result = ResultTooManyHops
			return id
		case err != nil:
			id.Result = ResultMalformed
			return id
		}
		return p.walk(id, source, hops)
	}
	return id
}

// walk evaluates an asserted chain right to left. hops is never empty.
func (p *Policy) walk(id Identity, source Source, hops []netip.Addr) Identity {
	for i := len(hops) - 1; i >= 0; i-- {
		addr := hops[i]
		if !addr.IsValid() {
			// An obfuscated, unknown or invalid hop can be neither proven
			// trusted nor accepted as the client, so the chain stops here.
			id.Result = ResultMalformed
			return id
		}
		if p.Trusts(addr) {
			continue
		}
		id.Client, id.Source, id.Result = addr, source, ResultAccepted
		return id
	}
	// Every asserted hop is trusted: the leftmost address is the client.
	id.Client, id.Source, id.Result = hops[0], source, ResultAccepted
	return id
}

// headerName maps a source to its wire header name.
func headerName(source Source) string {
	if source == SourceForwarded {
		return "Forwarded"
	}
	return "X-Forwarded-For"
}

// hasValue reports whether any field line carries non-whitespace content. A
// present but empty header asserts nothing, so the next configured header is
// considered instead of failing the request closed.
func hasValue(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}
