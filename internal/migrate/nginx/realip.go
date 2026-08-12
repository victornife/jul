// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

// Translation of nginx's realip module into Jul's [servers.client_address].
//
// The two models line up closely: nginx's `set_real_ip_from` is Jul's
// `trusted_proxies`, and both evaluate the chain right to left by default.
// Two differences are load-bearing and are reported rather than papered over:
// Jul does not support `X-Real-IP` (a single address cannot be evaluated
// against a trust boundary), and Jul's policy is scoped to a listen address
// rather than to a server block.

import (
	"fmt"
	"strings"

	"jul/internal/clientaddr"
	"jul/internal/config"
)

// realIPPolicy is the realip configuration collected from one nginx scope
// (the http block, or one server block).
type realIPPolicy struct {
	trusted []string
	// header is the resolved forwarded_headers value, nil when real_ip_header
	// was never seen in this scope.
	header []string
	// declared is true when the scope contained any realip directive, so an
	// empty server-level scope inherits the http-level policy instead of
	// clearing it.
	declared bool
	// blocked is set when the scope used a form Jul cannot express. A blocked
	// scope emits no policy at all: emitting a different one would silently
	// change which requests are trusted.
	blocked bool
	line    int
}

// merge layers a server-level scope over the inherited http-level one, exactly
// as nginx's directive inheritance does.
func (p realIPPolicy) merge(child realIPPolicy) realIPPolicy {
	if !child.declared {
		return p
	}
	out := child
	if len(child.trusted) == 0 {
		out.trusted = p.trusted
	}
	if child.header == nil {
		out.header = p.header
	}
	out.blocked = p.blocked || child.blocked
	return out
}

// isRealIPDirective reports whether name is one of the realip directives this
// file consumes.
func isRealIPDirective(name string) bool {
	switch name {
	case "set_real_ip_from", "real_ip_header", "real_ip_recursive":
		return true
	}
	return false
}

// realIPDirective folds one realip directive into the scope's policy. It
// returns false when the directive is not a realip directive at all, so the
// caller can fall through to its own default handling.
func (t *translator) realIPDirective(name string, params []string, line int, p *realIPPolicy) bool {
	switch name {
	case "set_real_ip_from":
		p.declared, p.line = true, line
		if len(params) == 0 {
			t.report.skipNamed(name, line, "no address given")
			p.blocked = true
			return true
		}
		entry := strings.TrimSpace(params[0])
		if strings.HasPrefix(entry, "unix:") {
			t.report.skipNamed(name, line, "unix-socket peers cannot be expressed as a trusted CIDR")
			p.blocked = true
			return true
		}
		prefix, err := clientaddr.ParsePrefix(entry)
		if err != nil {
			// A host name, or a prefix with host bits set. Jul rejects both,
			// so translating it would produce a config that fails validation.
			t.report.skipNamed(name, line, fmt.Sprintf("%q is not an address or canonical CIDR prefix", entry))
			p.blocked = true
			return true
		}
		p.trusted = append(p.trusted, prefix.String())
		return true

	case "real_ip_header":
		p.declared, p.line = true, line
		value := ""
		if len(params) > 0 {
			value = strings.TrimSpace(params[0])
		}
		switch strings.ToLower(value) {
		case "x-forwarded-for":
			p.header = []string{clientaddr.HeaderXFF}
		case "forwarded":
			p.header = []string{clientaddr.HeaderForwarded}
		case "proxy_protocol":
			t.report.skipNamed(name, line, "PROXY-protocol source addresses are not supported on HTTP listeners")
			p.blocked = true
		default:
			// X-Real-IP is nginx's default and the most common explicit value.
			// It carries a single address with no chain, so it cannot be
			// evaluated against a trust boundary; Jul does not support it.
			t.report.skipNamed(name, line, fmt.Sprintf("%q is not supported; Jul reads Forwarded or X-Forwarded-For", value))
			p.blocked = true
		}
		return true

	case "real_ip_recursive":
		p.declared, p.line = true, line
		if len(params) > 0 && strings.EqualFold(strings.TrimSpace(params[0]), "off") {
			t.report.skipNamed(name, line, "Jul always evaluates the chain right to left; 'off' cannot be expressed")
			p.blocked = true
			return true
		}
		t.report.note("real_ip_recursive at line %d is already Jul's behaviour: the chain is always evaluated right to left", line)
		return true
	}
	return false
}

// clientAddressFrom builds the policy block for a server, or nil when the
// scope declared nothing usable.
func (t *translator) clientAddressFrom(p realIPPolicy) *config.ClientAddressConfig {
	if !p.declared || p.blocked || len(p.trusted) == 0 {
		if p.declared && !p.blocked && len(p.trusted) == 0 {
			t.report.note("realip at line %d declared no set_real_ip_from, so no trusted-proxy policy was emitted; the client address stays the transport peer", p.line)
		}
		return nil
	}
	if p.header == nil {
		// nginx defaults real_ip_header to X-Real-IP, which Jul cannot express.
		// Emitting the Forwarded/X-Forwarded-For default here would invent a
		// trust rule the source never stated, so nothing is emitted.
		t.report.skipNamed("real_ip_header", p.line,
			"defaulted to X-Real-IP, which is not supported; set real_ip_header to X-Forwarded-For (or add [servers.client_address] by hand)")
		return nil
	}
	return &config.ClientAddressConfig{
		TrustedProxies:   append([]string(nil), p.trusted...),
		ForwardedHeaders: append([]string(nil), p.header...),
	}
}

// hoistClientAddress makes the emitted policy listener scoped.
//
// Jul derives identity per listen address, before the Host header selects a
// server block, and validation rejects blocks on one address whose policies
// differ. nginx has no such rule, so a source config may legitimately set
// different realip policies per virtual host on one port. That cannot be
// translated: rather than pick one (which would silently apply it to the
// others) or widen to the union (which would grow the trusted set), the
// address is left with no policy and the conflict is reported.
func (t *translator) hoistClientAddress(out *config.Config) {
	type entry struct {
		policy *config.ClientAddressConfig
		key    string
	}
	byAddr := map[string]entry{}
	conflicting := map[string]bool{}

	for i := range out.Servers {
		addr := strings.TrimSpace(out.Servers[i].Listen)
		if addr == "" {
			continue
		}
		key := clientAddressKey(out.Servers[i].ClientAddress)
		prev, seen := byAddr[addr]
		switch {
		case !seen:
			byAddr[addr] = entry{policy: out.Servers[i].ClientAddress, key: key}
		case prev.key == key:
			// identical; nothing to do
		case prev.policy == nil || out.Servers[i].ClientAddress == nil:
			// One block declared a policy and a sibling did not. Hoisting the
			// declared one is the faithful reading: nginx applies realip per
			// connection, and both blocks share the connection.
			if prev.policy == nil {
				byAddr[addr] = entry{policy: out.Servers[i].ClientAddress, key: key}
			}
			t.report.note("listen %s: a trusted-proxy policy from one server block now applies to every block on that address, because Jul derives the client address before the Host header selects a block", addr)
		default:
			conflicting[addr] = true
		}
	}

	for addr := range conflicting {
		t.report.skipNamed("set_real_ip_from", 0, fmt.Sprintf(
			"server blocks on %s declare different realip policies; Jul applies one policy per listen address, so none was emitted for it", addr))
		byAddr[addr] = entry{}
	}

	for i := range out.Servers {
		addr := strings.TrimSpace(out.Servers[i].Listen)
		if addr == "" {
			continue
		}
		out.Servers[i].ClientAddress = byAddr[addr].policy
	}
}

// clientAddressKey renders a policy for equality comparison.
func clientAddressKey(p *config.ClientAddressConfig) string {
	if p == nil {
		return ""
	}
	return strings.Join(p.TrustedProxies, ",") + "|" + strings.Join(p.ForwardedHeaders, ",") + "|" + fmt.Sprint(p.MaxHops)
}
