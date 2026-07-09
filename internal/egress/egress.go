// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package egress implements an optional outbound-destination allow-list for the
// server's config-driven auxiliary fetches — JWKS retrieval, forward-auth
// subrequests, and service discovery (Consul/Kubernetes). These destinations are
// taken verbatim from the configuration, so a misconfigured or compromised
// config could point them at an internal metadata endpoint or another
// unintended host (an SSRF-shaped blast radius). When enabled, the policy
// constrains every guarded dial to a small, operator-approved set of hostnames
// and CIDRs; when disabled (the default) it imposes no restriction and adds no
// overhead, so it is fully backward-compatible.
//
// Enforcement happens at dial time via a guarded net.Dialer.DialContext, so it
// covers redirects and every connection a guarded client makes. A destination is
// permitted when its hostname matches a host rule, when it is an IP literal
// inside an allowed CIDR, or — for a hostname not listed by name — when every
// resolved IP falls inside an allowed CIDR. TLS SNI and the HTTP Host header are
// preserved because the guard only substitutes the dial target, never the name
// the HTTP layer uses.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"jul/internal/config"
)

// ErrBlocked is returned by a guarded dial when the destination is not permitted
// by the egress allow-list.
var ErrBlocked = errors.New("egress blocked: destination not in the [egress] allow-list")

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
	hosts    []string     // exact hostnames or leading-dot suffixes (".example.com")
	cidrs    []*net.IPNet // allowed IP ranges; a bare IP is stored as /32 or /128
	resolver ipResolver
}

// New builds a Policy from the [egress] configuration. It returns (nil, nil)
// when egress is disabled, so the nil policy is the disabled policy. Each allow
// entry is a CIDR, a bare IP, an exact hostname, or a leading-dot suffix
// (".example.com" matches any subdomain).
func New(cfg config.EgressConfig) (*Policy, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	p := &Policy{resolver: net.DefaultResolver}
	for _, raw := range cfg.Allow {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(entry); err == nil {
			p.cidrs = append(p.cidrs, ipnet)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			p.cidrs = append(p.cidrs, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		p.hosts = append(p.hosts, strings.ToLower(entry))
	}
	if len(p.hosts) == 0 && len(p.cidrs) == 0 {
		return nil, fmt.Errorf("egress: enabled but no valid allow entries")
	}
	return p, nil
}

// Enabled reports whether p imposes a restriction. A nil policy does not.
func (p *Policy) Enabled() bool { return p != nil }

// DialContext returns a dial function that enforces the policy on top of base.
// When p is nil the base dialer's DialContext is returned unchanged, so a
// disabled policy adds no behaviour and no overhead.
func (p *Policy) DialContext(base *net.Dialer) DialFunc {
	if base == nil {
		base = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	}
	if p == nil {
		return base.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return p.dial(ctx, base, network, host, port)
	}
}

// dial applies the allow-list to a single connection attempt.
func (p *Policy) dial(ctx context.Context, base *net.Dialer, network, host, port string) (net.Conn, error) {
	// An IP literal is checked directly against the CIDR rules.
	if ip := net.ParseIP(host); ip != nil {
		if !p.allowIP(ip) {
			return nil, ErrBlocked
		}
		return base.DialContext(ctx, network, net.JoinHostPort(host, port))
	}
	// A hostname listed by name is trusted; normal resolution proceeds.
	if p.allowHost(host) {
		return base.DialContext(ctx, network, net.JoinHostPort(host, port))
	}
	// Otherwise the hostname is permitted only when every resolved IP falls
	// inside an allowed CIDR. Resolving here also means a rebinding record that
	// mixes an allowed and a disallowed address is rejected rather than raced.
	resolver := p.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, ErrBlocked
	}
	for _, ip := range ips {
		if !p.allowIP(ip.IP) {
			return nil, ErrBlocked
		}
	}
	// Dial a validated IP directly; the HTTP layer keeps the original hostname
	// for SNI and the Host header.
	var lastErr error
	for _, ip := range ips {
		conn, err := base.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrBlocked
}

// allowHost reports whether host matches a host rule: an exact hostname or a
// leading-dot suffix (".example.com" matches any subdomain, not the apex).
func (p *Policy) allowHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, a := range p.hosts {
		if a == host {
			return true
		}
		if strings.HasPrefix(a, ".") && strings.HasSuffix(host, a) {
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

// Transport returns an *http.Transport whose DialContext enforces the policy.
// When base is nil a clone of http.DefaultTransport is used so proxy, idle
// pooling, and TLS defaults are preserved; when p is nil base is returned
// unchanged.
func (p *Policy) Transport(base *http.Transport) *http.Transport {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	if p == nil {
		return base
	}
	base.DialContext = p.DialContext(&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second})
	return base
}

// Client returns an *http.Client with the given timeout whose transport enforces
// the policy. A nil policy yields an ordinary client.
func (p *Policy) Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: p.Transport(nil)}
}
