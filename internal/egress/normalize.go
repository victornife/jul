// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"golang.org/x/net/idna"
)

// hostRule is a normalized hostname rule. When suffix is true the rule matches
// any subdomain of value (".example.com" → value "example.com"), not the apex.
type hostRule struct {
	value  string
	suffix bool
}

// errSkip marks a blank allow entry that is dropped without being an error, so
// stray whitespace lines in config never fail policy construction.
var errSkip = errors.New("egress: empty allow entry")

// parseEntry classifies and normalizes one [egress].allow entry into either a
// host rule or a CIDR. Exactly one of the returned values is meaningful; a blank
// entry returns errSkip, and an ambiguous or malformed entry returns a
// descriptive error instead of being silently treated as a hostname.
func parseEntry(raw string) (hostRule, *net.IPNet, error) {
	entry := strings.TrimSpace(raw)
	if entry == "" {
		return hostRule{}, nil, errSkip
	}
	if strings.Contains(entry, "://") {
		return hostRule{}, nil, fmt.Errorf("egress: %q looks like a URL; use a host, IP, or CIDR", raw)
	}
	// A '/' means the operator intends a CIDR; a parse failure is an error, not
	// a fallthrough to hostname handling.
	if strings.ContainsRune(entry, '/') {
		_, ipnet, err := net.ParseCIDR(entry)
		if err != nil {
			return hostRule{}, nil, fmt.Errorf("egress: invalid CIDR %q: %w", raw, err)
		}
		return hostRule{}, canonicalCIDR(ipnet), nil
	}
	// A bracketed or bare IP literal becomes a single-address CIDR.
	if ip := parseIPLiteral(entry); ip != nil {
		return hostRule{}, ipToCIDR(ip), nil
	}
	// Otherwise it is a hostname or a leading-dot suffix rule.
	suffix := strings.HasPrefix(entry, ".")
	name := entry
	if suffix {
		name = entry[1:]
	}
	nh, err := normalizeHost(name)
	if err != nil {
		return hostRule{}, nil, fmt.Errorf("egress: invalid host %q: %w", raw, err)
	}
	if err := validateHostname(nh); err != nil {
		return hostRule{}, nil, fmt.Errorf("egress: invalid host %q: %w", raw, err)
	}
	return hostRule{value: nh, suffix: suffix}, nil, nil
}

// parseIPLiteral accepts a bare or bracketed IP, rejecting a zone because a
// scoped address is ambiguous in an allow-list.
func parseIPLiteral(s string) net.IP {
	s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	if strings.ContainsRune(s, '%') {
		return nil
	}
	return net.ParseIP(s)
}

// canonicalCIDR returns the masked network so equal ranges written differently
// (10.0.0.5/8 vs 10.0.0.0/8) canonicalize to one rule for deduplication.
func canonicalCIDR(ipnet *net.IPNet) *net.IPNet {
	return &net.IPNet{IP: ipnet.IP.Mask(ipnet.Mask), Mask: ipnet.Mask}
}

// ipToCIDR stores a bare IP as an all-ones network (/32 or /128).
func ipToCIDR(ip net.IP) *net.IPNet {
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
}

// normalizeHost lowercases, strips one trailing root dot, and converts a Unicode
// IDN to its ASCII (punycode) form so rules and dial targets compare identically.
// Pure-ASCII names skip IDNA so operational hostnames with underscores or other
// DNS-legal-but-not-IDNA characters are preserved.
func normalizeHost(h string) (string, error) {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", errors.New("empty hostname")
	}
	if isASCII(h) {
		return h, nil
	}
	a, err := idna.Lookup.ToASCII(h)
	if err != nil {
		return "", err
	}
	return a, nil
}

// validateHostname rejects entries that are not plausibly a hostname: those with
// spaces, a path, userinfo, an explicit port, or an empty DNS label.
func validateHostname(h string) error {
	if h == "" {
		return errors.New("empty hostname")
	}
	if strings.ContainsAny(h, " \t\r\n/@:") {
		return errors.New("hostname contains a space, path, userinfo, or port")
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" {
			return errors.New("hostname has an empty label")
		}
	}
	return nil
}

// normalizeDialHost normalizes a host taken from a dial target or request URL to
// the same form as the stored rules. It strips IPv6 brackets and zones, returns
// the canonical form of an IP literal, and returns "" for an unusable host so
// the caller can fail closed.
func normalizeDialHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	if h == "" {
		return ""
	}
	if i := strings.IndexByte(h, '%'); i >= 0 {
		if addr, err := netip.ParseAddr(h); err == nil {
			return addr.WithZone("").String()
		}
		h = h[:i]
	}
	if ip := net.ParseIP(h); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		return ip.String()
	}
	nh, err := normalizeHost(h)
	if err != nil {
		return ""
	}
	return nh
}

// isASCII reports whether s contains only ASCII bytes.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
