// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net/textproto"
	"strings"
)

// This file validates [[servers.locations.response_headers]] (ADR 0018 §8,
// §8a, §8b): the add/set/remove operation list, the protected-header and
// framing rejections, the Vary/cache interaction, and the CORS/response-header
// field-ownership split. §9's CORS block itself is validated in
// validate_cors.go; this file only rejects an Access-Control-* operation here
// when cors.enabled, since CORS owns that field set (§8b).

// Bounds on a location's response-header policy (ADR 0018 §16).
const (
	// MaxResponseHeaderOps bounds response_headers entries per location.
	MaxResponseHeaderOps = 32
	// MaxResponseHeaderValueBytes bounds one operation's value.
	MaxResponseHeaderValueBytes = 4096
	// MaxResponseHeaderTotalBytes bounds the static bytes response_headers can
	// add to one response: the sum of name+value across every add/set
	// operation. remove contributes nothing. This is a conservative
	// configuration-time approximation (it does not count CRLF/framing
	// overhead) rather than a measured wire size.
	MaxResponseHeaderTotalBytes = 8192
)

// protectedResponseHeaders are hop-by-hop or framing fields a response-header
// operation may never name: mutating them here would desynchronize the
// connection or contradict what the server itself is about to write.
var protectedResponseHeaders = map[string]struct{}{
	"Connection": {}, "Content-Length": {}, "Transfer-Encoding": {},
	"Upgrade": {}, "Keep-Alive": {}, "Proxy-Connection": {}, "Te": {},
	"Trailer": {}, "Proxy-Authenticate": {}, "Proxy-Authorization": {},
}

// validateResponsePolicy validates a location's response_headers list and its
// CORS block together, since §8a and §8b are interactions between them and
// with Cache.
func validateResponsePolicy(loc LocationConfig, where string) []error {
	var errs []error
	errs = append(errs, validateResponseHeaders(loc.ResponseHeaders, loc.Cache, loc.CORS != nil && loc.CORS.Enabled, where)...)
	errs = append(errs, validateCORS(loc.CORS, where)...)
	return errs
}

// validateResponseHeaders checks response_headers (§8, §8a, §8b). cached is
// the location's own Cache setting; corsEnabled is whether it has an enabled
// CORS policy.
func validateResponseHeaders(ops []ResponseHeaderOp, cached, corsEnabled bool, where string) []error {
	if len(ops) == 0 {
		return nil
	}
	var errs []error
	if len(ops) > MaxResponseHeaderOps {
		errs = append(errs, fmt.Errorf("%s: response_headers has %d entries, over the limit of %d", where, len(ops), MaxResponseHeaderOps))
	}
	totalBytes := 0
	for i, op := range ops {
		field := fmt.Sprintf("%s.response_headers[%d]", where, i)
		name := strings.TrimSpace(op.Name)
		var canonical string
		switch {
		case name == "":
			errs = append(errs, fmt.Errorf("%s: name is required", field))
		case strings.HasPrefix(name, ":"):
			errs = append(errs, fmt.Errorf("%s: %q is an HTTP/2 or HTTP/3 pseudo-header, not a field", field, name))
		case !isFieldToken(name):
			errs = append(errs, fmt.Errorf("%s: %q is not a valid header field name", field, name))
		default:
			canonical = textproto.CanonicalMIMEHeaderKey(name)
			if _, protected := protectedResponseHeaders[canonical]; protected {
				errs = append(errs, fmt.Errorf("%s: %q is a hop-by-hop or framing field and cannot be operated on", field, canonical))
			}
			if canonical == "Content-Encoding" {
				errs = append(errs, fmt.Errorf("%s: Content-Encoding is owned by the compression layer", field))
			}
			if canonical == "Vary" {
				switch {
				case cached:
					errs = append(errs, fmt.Errorf("%s: a Vary operation on a location with cache = true is rejected; response policy runs outside the cache, so an operator-added Vary is invisible to it and can leak a cached representation across the variance it claims to declare", field))
				case op.Op != "add":
					errs = append(errs, fmt.Errorf("%s: only op = \"add\" is permitted on Vary, and only as a directive to downstream caches; this location has no cache of its own", field))
				}
			}
			if corsEnabled && strings.HasPrefix(canonical, "Access-Control-") {
				errs = append(errs, fmt.Errorf("%s: %q is owned by cors; this location has cors.enabled = true, so response_headers may not operate on Access-Control-* fields", field, canonical))
			}
		}

		switch op.Op {
		case "add", "set":
			if op.Value == nil {
				errs = append(errs, fmt.Errorf("%s: value is required for op %q; an omitted value is an error, an explicitly empty one is legal", field, op.Op))
				break
			}
			if len(*op.Value) > MaxResponseHeaderValueBytes {
				errs = append(errs, fmt.Errorf("%s: value is %d bytes, over the limit of %d", field, len(*op.Value), MaxResponseHeaderValueBytes))
			} else if bad, pos := firstInvalidHeaderValueByte(*op.Value); bad {
				errs = append(errs, fmt.Errorf("%s: value contains a byte at offset %d outside RFC 9110 field-value grammar (VCHAR / SP / HTAB / obs-text)", field, pos))
			}
			totalBytes += len(name) + len(*op.Value)
		case "remove":
			if op.Value != nil {
				errs = append(errs, fmt.Errorf("%s: op = \"remove\" takes no value", field))
			}
		case "":
			errs = append(errs, fmt.Errorf("%s: op is required (add|set|remove)", field))
		default:
			errs = append(errs, fmt.Errorf("%s: invalid op %q (want add|set|remove)", field, op.Op))
		}
	}
	if totalBytes > MaxResponseHeaderTotalBytes {
		errs = append(errs, fmt.Errorf("%s: response_headers adds an estimated %d bytes to the response, over the limit of %d", where, totalBytes, MaxResponseHeaderTotalBytes))
	}
	return errs
}

// firstInvalidHeaderValueByte reports the first byte in s that is outside RFC
// 9110 §5.5's field-value grammar: VCHAR (0x21-0x7E) / SP (0x20) / HTAB (0x09)
// / obs-text (0x80-0xFF). Rejecting only CR/LF/NUL would still admit the other
// C0 controls and DEL; Go silently drops an invalid header at write time, so
// configuration time is the only place the operator finds out.
func firstInvalidHeaderValueByte(s string) (bool, int) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == 0x09, c == 0x20:
		case c >= 0x21 && c <= 0x7E:
		case c >= 0x80:
		default:
			return true, i
		}
	}
	return false, -1
}
