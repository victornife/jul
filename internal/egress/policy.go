// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package egress implements an optional outbound-destination allow-list for the
// server's config-driven auxiliary fetches — JWKS retrieval, forward-auth
// subrequests, service discovery (Consul/Kubernetes), ACME/OCSP PKI calls, and
// WASM plugin fetches. These destinations come verbatim from configuration, so a
// misconfigured or compromised config could point them at an internal metadata
// endpoint or another unintended host (an SSRF-shaped blast radius). When
// enabled, the policy constrains every guarded dial to a small, operator-approved
// set of hostnames and CIDRs; when disabled (the default) it imposes no
// restriction and adds no overhead, so it is fully backward-compatible.
//
// Enforcement happens at dial time via a guarded net.Dialer.DialContext, so it
// covers redirects and every connection a guarded client makes. Guarded HTTP
// clients additionally validate the request URL before dispatch and pin
// Proxy=nil so an environment proxy cannot hide the real target. A destination
// is permitted when its hostname matches a host rule, when it is an IP literal
// inside an allowed CIDR, or — for a hostname not listed by name — when every
// resolved IP falls inside an allowed CIDR. TLS SNI and the HTTP Host header are
// preserved because the guard only substitutes the dial target, never the name
// the HTTP layer uses.
//
// Subsystems obtain an immutable, subsystem-scoped Guard with Policy.For so
// blocks and metrics are attributed without call sites importing this package's
// enforcement internals. Subsystem names are bounded constants.
package egress

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"jul/internal/config"
)

// Subsystem names for scoped guards and bounded metric labels. Call sites use
// these constants instead of free-form strings.
const (
	SubsystemAuth      = "auth"      // JWKS retrieval and forward-auth subrequests
	SubsystemDiscovery = "discovery" // Consul and Kubernetes service discovery
	SubsystemACME      = "acme"      // ACME directory/order/challenge calls
	SubsystemOCSP      = "ocsp"      // OCSP responder retrieval
	SubsystemPlugin    = "plugin"    // WASM plugin fetch capability
)

// DialFunc matches net.Dialer.DialContext. Subsystems accept a value of this
// shape so they can be guarded without importing this package.
type DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error)

// ipResolver resolves a host to IP addresses; *net.Resolver satisfies it. It is
// a seam so tests can exercise the CIDR path and DNS-rebinding cases with
// injected addresses.
type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Policy is an outbound-destination allow-list. A nil *Policy imposes no
// restriction, so callers may apply it unconditionally: a disabled egress
// configuration is represented by the nil policy.
type Policy struct {
	hosts    []hostRule   // exact hostnames or leading-dot suffix rules
	cidrs    []*net.IPNet // allowed IP ranges; a bare IP is stored as /32 or /128
	resolver ipResolver
	observe  func(Decision)
}

// Option configures a Policy at construction. Options are ignored for a disabled
// (nil) policy because it makes no decisions.
type Option func(*Policy)

// WithObserver registers a callback invoked for every allow/block decision so
// the observability layer can maintain bounded metrics and structured logs.
func WithObserver(fn func(Decision)) Option {
	return func(p *Policy) { p.observe = fn }
}

// WithResolver overrides the DNS resolver used for CIDR-only hostname
// authorization. It exists for tests.
func WithResolver(r ipResolver) Option {
	return func(p *Policy) { p.resolver = r }
}

// New builds a Policy from the [egress] configuration. It returns (nil, nil)
// when egress is disabled, so the nil policy is the disabled policy. Each allow
// entry is a CIDR, a bare IP, an exact hostname, or a leading-dot suffix
// (".example.com" matches any subdomain). Malformed or ambiguous entries are
// rejected rather than silently treated as hostnames.
func New(cfg config.EgressConfig, opts ...Option) (*Policy, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	p := &Policy{resolver: net.DefaultResolver}
	seenHost := make(map[string]bool)
	seenCIDR := make(map[string]bool)
	for _, raw := range cfg.Allow {
		rule, cidr, err := parseEntry(raw)
		switch {
		case err == errSkip:
			continue
		case err != nil:
			return nil, err
		}
		if cidr != nil {
			if key := cidr.String(); !seenCIDR[key] {
				seenCIDR[key] = true
				p.cidrs = append(p.cidrs, cidr)
			}
			continue
		}
		key := rule.value
		if rule.suffix {
			key = "." + key
		}
		if !seenHost[key] {
			seenHost[key] = true
			p.hosts = append(p.hosts, rule)
		}
	}
	if len(p.hosts) == 0 && len(p.cidrs) == 0 {
		return nil, fmt.Errorf("egress: enabled but no valid allow entries")
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.resolver == nil {
		p.resolver = net.DefaultResolver
	}
	return p, nil
}

// Enabled reports whether p imposes a restriction. A nil policy does not.
func (p *Policy) Enabled() bool { return p != nil }

// For returns an immutable, subsystem-scoped handle. A nil policy yields a nil
// *Guard, which enforces nothing; every Guard method is nil-safe.
func (p *Policy) For(subsystem string) *Guard {
	if p == nil {
		return nil
	}
	return &Guard{policy: p, subsystem: subsystem}
}

// Guard is a subsystem-scoped view of a Policy. Obtain one with Policy.For. A
// nil *Guard (from a disabled policy) applies no restriction.
type Guard struct {
	policy    *Policy
	subsystem string
}

// Enabled reports whether the guard enforces a restriction.
func (g *Guard) Enabled() bool { return g != nil && g.policy != nil }

// dial applies the allow-list to a single connection attempt, using baseDial for
// the actual connection. It reports the decision to the policy observer.
func (p *Policy) dial(ctx context.Context, subsystem string, baseDial DialFunc, network, host, port string) (net.Conn, error) {
	nhost := normalizeDialHost(host)
	if nhost == "" {
		return nil, p.block(subsystem, host, "", ReasonInvalidAddress, 0)
	}
	// An IP literal is checked directly against the CIDR rules.
	if ip := net.ParseIP(nhost); ip != nil {
		if !p.allowIP(ip) {
			return nil, p.block(subsystem, nhost, ip.String(), ReasonIPNotAllowed, 0)
		}
		p.observeAllow(subsystem, nhost, ip.String(), 0)
		return baseDial(ctx, network, net.JoinHostPort(host, port))
	}
	// A hostname listed by name is trusted; normal resolution proceeds.
	if p.allowHost(nhost) {
		p.observeAllow(subsystem, nhost, "", 0)
		return baseDial(ctx, network, net.JoinHostPort(host, port))
	}
	// Otherwise the hostname is permitted only when every resolved IP falls
	// inside an allowed CIDR. Resolving here also means a rebinding record that
	// mixes an allowed and a disallowed address is rejected rather than raced.
	resolver := p.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupIPAddr(ctx, nhost)
	if err != nil {
		return nil, err // a DNS failure is not a policy block
	}
	if len(ips) == 0 {
		return nil, p.block(subsystem, nhost, "", ReasonNoDNSAnswers, 0)
	}
	allowed := 0
	var firstBad string
	for _, ip := range ips {
		if p.allowIP(ip.IP) {
			allowed++
		} else if firstBad == "" {
			firstBad = ip.IP.String()
		}
	}
	if allowed == 0 {
		return nil, p.block(subsystem, nhost, firstBad, ReasonHostNotAllowed, len(ips))
	}
	if allowed < len(ips) {
		return nil, p.block(subsystem, nhost, firstBad, ReasonMixedDNS, len(ips))
	}
	// Dial a validated IP directly; the HTTP layer keeps the original hostname
	// for SNI and the Host header.
	p.observeAllow(subsystem, nhost, "", len(ips))
	var lastErr error
	for _, ip := range ips {
		conn, err := baseDial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, p.block(subsystem, nhost, "", ReasonHostNotAllowed, len(ips))
}

// block records a blocked decision and returns the typed error.
func (p *Policy) block(subsystem, host, ip string, r Reason, answers int) error {
	p.emit(Decision{Subsystem: subsystem, Result: ResultBlock, Reason: r, Host: host, IP: ip, DNSAnswers: answers})
	return &BlockError{Subsystem: subsystem, Host: host, IP: ip, Reason: r}
}

// observeAllow records an allowed decision.
func (p *Policy) observeAllow(subsystem, host, ip string, answers int) {
	p.emit(Decision{Subsystem: subsystem, Result: ResultAllow, Host: host, IP: ip, DNSAnswers: answers})
}

func (p *Policy) emit(d Decision) {
	if p != nil && p.observe != nil {
		p.observe(d)
	}
}

// staticBlock reports the destinations a guarded HTTP client can refuse before
// dispatch, without DNS: an unparseable host or an IP literal outside every
// allowed CIDR. Name-listed and CIDR-only-candidate hosts pass; the dial makes
// the final decision after resolution.
func (p *Policy) staticBlock(subsystem, host string) *BlockError {
	nhost := normalizeDialHost(host)
	if nhost == "" {
		return &BlockError{Subsystem: subsystem, Host: host, Reason: ReasonInvalidAddress}
	}
	if ip := net.ParseIP(nhost); ip != nil && !p.allowIP(ip) {
		return &BlockError{Subsystem: subsystem, Host: nhost, IP: ip.String(), Reason: ReasonIPNotAllowed}
	}
	return nil
}

// allowHost reports whether host matches a host rule: an exact hostname or a
// leading-dot suffix (".example.com" matches any subdomain, not the apex). host
// is expected to be already normalized.
func (p *Policy) allowHost(host string) bool {
	if host == "" {
		return false
	}
	for _, r := range p.hosts {
		if r.suffix {
			if strings.HasSuffix(host, "."+r.value) {
				return true
			}
		} else if host == r.value {
			return true
		}
	}
	return false
}

// allowIP reports whether ip falls inside any allowed CIDR.
func (p *Policy) allowIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range p.cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// defaultDialer is the base dialer used when a caller passes nil.
func defaultDialer() *net.Dialer {
	return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
}
