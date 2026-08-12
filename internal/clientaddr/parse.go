// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package clientaddr

import (
	"errors"
	"net/netip"
	"strings"
)

// Parsing outcomes that Derive maps onto a Result. They are sentinels rather
// than descriptive errors because nothing derived from an untrusted header ever
// reaches a log line or a metric label.
var (
	errMalformed   = errors.New("clientaddr: malformed forwarding header")
	errTooManyHops = errors.New("clientaddr: forwarding chain exceeds max_hops")
)

// parseChain turns the field lines of one forwarding header into an ordered
// chain, leftmost first. An entry that carries no usable address — an
// obfuscated identifier, "unknown", a hostname or an invalid address — is
// retained as an invalid netip.Addr so the right-to-left walk can fail closed
// when it actually reaches one, rather than discarding the position and
// silently shifting the chain.
//
// Repeated field lines of the same header are one logical list (RFC 9110), so
// they are concatenated. Two *different* headers are never merged.
func parseChain(source Source, values []string, maxHops int) ([]netip.Addr, error) {
	total := 0
	for _, v := range values {
		total += len(v)
	}
	if total > maxHeaderBytes {
		return nil, errMalformed
	}
	var hops []netip.Addr
	for _, v := range values {
		var err error
		if source == SourceForwarded {
			hops, err = appendForwarded(hops, v, maxHops)
		} else {
			hops, err = appendXFF(hops, v, maxHops)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(hops) == 0 {
		return nil, errMalformed
	}
	return hops, nil
}

// appendXFF parses one X-Forwarded-For field line. Entries are bare addresses:
// a port, brackets or a hostname is a non-standard form and is retained as an
// unusable hop rather than guessed at.
func appendXFF(hops []netip.Addr, value string, maxHops int) ([]netip.Addr, error) {
	for entry := range strings.SplitSeq(value, ",") {
		if len(hops) >= maxHops {
			return nil, errTooManyHops
		}
		hops = append(hops, parseBareAddr(strings.TrimSpace(entry)))
	}
	return hops, nil
}

// appendForwarded parses one RFC 7239 Forwarded field line. Structural errors
// (an unterminated quoted string, a parameter without a value, two "for"
// parameters in one element) fail the whole header closed; an element whose
// "for" is syntactically fine but carries no usable address becomes an
// unusable hop.
func appendForwarded(hops []netip.Addr, value string, maxHops int) ([]netip.Addr, error) {
	elements, err := splitOutsideQuotes(value, ',')
	if err != nil {
		return nil, err
	}
	for _, element := range elements {
		if len(hops) >= maxHops {
			return nil, errTooManyHops
		}
		addr, err := forwardedElementAddr(element)
		if err != nil {
			return nil, err
		}
		hops = append(hops, addr)
	}
	return hops, nil
}

// forwardedElementAddr extracts the "for" address of one Forwarded element.
func forwardedElementAddr(element string) (netip.Addr, error) {
	if strings.TrimSpace(element) == "" {
		return netip.Addr{}, nil
	}
	params, err := splitOutsideQuotes(element, ';')
	if err != nil {
		return netip.Addr{}, err
	}
	var raw string
	var seen bool
	for _, param := range params {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		name, value, ok := strings.Cut(param, "=")
		if !ok {
			return netip.Addr{}, errMalformed
		}
		if !strings.EqualFold(strings.TrimSpace(name), "for") {
			continue
		}
		if seen {
			// Two "for" parameters in one element are ambiguous; there is no
			// deterministic way to choose, so the header fails closed.
			return netip.Addr{}, errMalformed
		}
		unquoted, err := unquote(strings.TrimSpace(value))
		if err != nil {
			return netip.Addr{}, err
		}
		raw, seen = unquoted, true
	}
	if !seen {
		return netip.Addr{}, nil
	}
	return parseForwardedFor(raw), nil
}

// parseForwardedFor parses an RFC 7239 node identifier. Obfuscated identifiers
// ("_hidden"), "unknown", hostnames and invalid addresses yield the zero
// address: they are never canonical clients. No name resolution happens here or
// anywhere else in this package.
func parseForwardedFor(raw string) netip.Addr {
	if raw == "" || raw[0] == '_' || strings.EqualFold(raw, "unknown") {
		return netip.Addr{}
	}
	if raw[0] == '[' {
		end := strings.IndexByte(raw, ']')
		if end < 0 {
			return netip.Addr{}
		}
		if rest := raw[end+1:]; rest != "" && !validPortSuffix(rest) {
			return netip.Addr{}
		}
		addr := parseBareAddr(raw[1:end])
		if addr.Is4() {
			// Brackets denote an IPv6 literal; an IPv4 address inside them is
			// not a form any standard produces.
			return netip.Addr{}
		}
		return addr
	}
	if host, port, ok := strings.Cut(raw, ":"); ok {
		if !validPortSuffix(":" + port) {
			return netip.Addr{}
		}
		addr := parseBareAddr(host)
		if !addr.Is4() {
			return netip.Addr{}
		}
		return addr
	}
	return parseBareAddr(raw)
}

// parseBareAddr parses a bare IP address, returning the zero address for
// anything else. A zone is rejected outright: a scoped address asserted by a
// proxy has no meaning on this host.
func parseBareAddr(s string) netip.Addr {
	if s == "" || strings.ContainsAny(s, "%/ \t") {
		return netip.Addr{}
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return normalize(addr)
}

// validPortSuffix reports whether s is ":" followed by a decimal port.
func validPortSuffix(s string) bool {
	if len(s) < 2 || s[0] != ':' || len(s) > 6 {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// splitOutsideQuotes splits s on sep, ignoring separators inside a quoted
// string. An unterminated quoted string or a trailing escape is a structural
// error.
func splitOutsideQuotes(s string, sep byte) ([]string, error) {
	var out []string
	start, inQuote, escaped := 0, false, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case escaped:
			escaped = false
		case inQuote && c == '\\':
			escaped = true
		case c == '"':
			inQuote = !inQuote
		case c == sep && !inQuote:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if inQuote || escaped {
		return nil, errMalformed
	}
	return append(out, s[start:]), nil
}

// unquote removes RFC 7230 quoted-string framing and escaping. A bare token is
// returned unchanged; anything that is quoted must be quoted correctly.
func unquote(s string) (string, error) {
	if len(s) == 0 || s[0] != '"' {
		if strings.ContainsAny(s, "\"\\") {
			return "", errMalformed
		}
		return s, nil
	}
	if len(s) < 2 || s[len(s)-1] != '"' {
		return "", errMalformed
	}
	inner := s[1 : len(s)-1]
	if !strings.Contains(inner, "\\") {
		if strings.Contains(inner, "\"") {
			return "", errMalformed
		}
		return inner, nil
	}
	var b strings.Builder
	b.Grow(len(inner))
	for i := 0; i < len(inner); i++ {
		switch c := inner[i]; c {
		case '\\':
			i++
			if i >= len(inner) {
				return "", errMalformed
			}
			b.WriteByte(inner[i])
		case '"':
			return "", errMalformed
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}
