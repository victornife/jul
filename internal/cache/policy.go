// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxDeltaSeconds bounds every delta-seconds directive. RFC 9111 §1.2.2 requires
// a cache that receives a value larger than it can represent to use the greatest
// value it can represent; clamping here keeps the arithmetic inside
// time.Duration (which overflows at ~292 years) instead of wrapping negative.
const maxDeltaSeconds = 100 * 365 * 24 * 60 * 60

// cacheControl is a parsed Cache-Control field value.
//
// Parsing is deliberately total: every input produces a value, and an
// unparseable argument is resolved to the most restrictive interpretation rather
// than to "directive absent". A response whose freshness cannot be determined
// must not be treated as fresh.
type cacheControl struct {
	// flags holds every directive token seen, lowercased, whether or not it
	// carried an argument. A field-qualified form such as
	// no-cache="Set-Cookie" registers as the plain token: Jul treats it as
	// whole-representation validation (see responsePolicy.NoCache).
	flags map[string]bool
	// secs holds recognized delta-seconds directives. When a directive appears
	// more than once the SMALLEST value wins, because duplicate directives are
	// undefined by RFC 9111 and the shortest lifetime is the safe reading.
	secs map[string]time.Duration
}

func (cc cacheControl) has(name string) bool { return cc.flags[name] }

// delta returns the directive's duration and whether it was present. A present
// directive with a malformed or negative argument reports zero, which every
// caller treats as "no reusable lifetime".
func (cc cacheControl) delta(name string) (time.Duration, bool) {
	d, ok := cc.secs[name]
	return d, ok
}

// deltaDirectives are the directives whose argument is a delta-seconds value.
var deltaDirectives = map[string]bool{
	"max-age":                true,
	"s-maxage":               true,
	"stale-while-revalidate": true,
	"stale-if-error":         true,
	"min-fresh":              true,
	"max-stale":              true,
}

// parseCacheControl parses every Cache-Control field line of h into one
// directive set. Multiple field lines are equivalent to one comma-separated
// line (RFC 9110 §5.3), so they are merged rather than the first one winning.
func parseCacheControl(h http.Header, field string) cacheControl {
	cc := cacheControl{flags: map[string]bool{}, secs: map[string]time.Duration{}}
	for _, line := range h.Values(field) {
		for _, part := range splitDirectives(line) {
			name, value, hasValue := splitDirective(part)
			if name == "" {
				continue
			}
			cc.flags[name] = true
			if !deltaDirectives[name] {
				continue
			}
			d := parseDeltaSeconds(value, hasValue)
			if prev, seen := cc.secs[name]; !seen || d < prev {
				cc.secs[name] = d
			}
		}
	}
	return cc
}

// splitDirectives splits a Cache-Control field value on commas that are not
// inside a quoted-string, so no-cache="X-A, X-B" stays one directive.
func splitDirectives(v string) []string {
	var out []string
	var start int
	var inQuotes, escaped bool
	for i := 0; i < len(v); i++ {
		switch {
		case escaped:
			escaped = false
		case v[i] == '\\' && inQuotes:
			escaped = true
		case v[i] == '"':
			inQuotes = !inQuotes
		case v[i] == ',' && !inQuotes:
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	out = append(out, v[start:])
	return out
}

// splitDirective splits one directive into its lowercased name and its
// argument, unquoting the argument when it is a quoted-string.
func splitDirective(part string) (name, value string, hasValue bool) {
	part = strings.TrimSpace(part)
	if part == "" {
		return "", "", false
	}
	i := strings.IndexByte(part, '=')
	if i < 0 {
		return strings.ToLower(part), "", false
	}
	name = strings.ToLower(strings.TrimSpace(part[:i]))
	value = strings.TrimSpace(part[i+1:])
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = strings.ReplaceAll(value[1:len(value)-1], `\"`, `"`)
	}
	return name, value, true
}

// parseDeltaSeconds converts a delta-seconds argument to a duration.
//
// A missing, empty, non-numeric or negative argument yields zero: the directive
// was written, so it must not be ignored, but its lifetime cannot be trusted and
// zero is the shortest one. An out-of-range argument is clamped upward, as
// RFC 9111 §1.2.2 requires.
func parseDeltaSeconds(v string, hasValue bool) time.Duration {
	if !hasValue || v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		// strconv reports ErrRange for a syntactically valid number that does
		// not fit; that is the clamp case, not a malformed one.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange && !strings.HasPrefix(v, "-") {
			return maxDeltaSeconds * time.Second
		}
		return 0
	}
	if n < 0 {
		return 0
	}
	if n > maxDeltaSeconds {
		n = maxDeltaSeconds
	}
	return time.Duration(n) * time.Second
}

// requestPolicy is the caching decision carried by one request's own
// Cache-Control (plus the HTTP/1.0 Pragma fallback).
//
// Only the directives Jul implements are represented. min-fresh, max-stale and
// only-if-cached are parsed but deliberately NOT honored — honoring part of a
// directive is worse than not honoring it, because the client cannot tell the
// difference. They are listed as unsupported in docs/cache.md.
type requestPolicy struct {
	// NoStore bypasses both lookup and storage for this request.
	NoStore bool
	// MustValidate requires successful validation before any stored response is
	// reused, however fresh it is. It is set by no-cache and by max-age=0.
	MustValidate bool
}

func parseRequestPolicy(r *http.Request) requestPolicy {
	cc := parseCacheControl(r.Header, "Cache-Control")
	p := requestPolicy{
		NoStore:      cc.has("no-store"),
		MustValidate: cc.has("no-cache"),
	}
	if d, ok := cc.delta("max-age"); ok && d == 0 {
		// max-age=0 asks for a response no older than zero seconds, which no
		// stored response can satisfy without contacting the origin. It is
		// therefore handled exactly like no-cache.
		p.MustValidate = true
	}
	if len(cc.flags) == 0 && pragmaNoCache(r.Header) {
		// RFC 9111 §5.4: Pragma: no-cache is the HTTP/1.0 spelling and applies
		// only when the request carries no Cache-Control at all.
		p.MustValidate = true
	}
	return p
}

// pragmaNoCache reports whether any Pragma field line carries the no-cache
// extension.
func pragmaNoCache(h http.Header) bool {
	for _, line := range h.Values("Pragma") {
		for _, part := range splitDirectives(line) {
			if name, _, _ := splitDirective(part); name == "no-cache" {
				return true
			}
		}
	}
	return false
}

// responsePolicy is the shared-cache policy of one origin response, derived once
// at publication and then stored on the immutable Entry. Deriving it once is the
// point: reconstructing "may this be reused?" from a lossy header subset on every
// hit is how the pre-#132 cache lost track of no-cache and must-revalidate.
type responsePolicy struct {
	NoStore         bool
	Private         bool
	Public          bool
	NoCache         bool
	MustRevalidate  bool
	ProxyRevalidate bool

	SMaxAge    time.Duration
	HasSMaxAge bool
	MaxAge     time.Duration
	HasMaxAge  bool
	SWR        time.Duration
	HasSWR     bool
	SIE        time.Duration
	HasSIE     bool
}

func parseResponsePolicy(h http.Header) responsePolicy {
	cc := parseCacheControl(h, "Cache-Control")
	p := responsePolicy{
		NoStore: cc.has("no-store"),
		Private: cc.has("private"),
		Public:  cc.has("public"),
		// A field-qualified no-cache="Header-Name" is treated as an unqualified
		// no-cache: selective header replacement on reuse is a separate design
		// (#132 scope §2), and validating the whole representation is the
		// conservative superset of it.
		NoCache:         cc.has("no-cache"),
		MustRevalidate:  cc.has("must-revalidate"),
		ProxyRevalidate: cc.has("proxy-revalidate"),
	}
	p.SMaxAge, p.HasSMaxAge = cc.delta("s-maxage")
	p.MaxAge, p.HasMaxAge = cc.delta("max-age")
	p.SWR, p.HasSWR = cc.delta("stale-while-revalidate")
	p.SIE, p.HasSIE = cc.delta("stale-if-error")
	return p
}

// revalidationRequired reports whether the origin forbids serving this response
// once it is stale without contacting the origin first (RFC 9111 §5.2.2.2 and
// §5.2.2.8). proxy-revalidate binds shared caches specifically, which Jul is.
func (p responsePolicy) revalidationRequired() bool {
	return p.MustRevalidate || p.ProxyRevalidate
}

// sharedAuthReuse reports whether a stored response may satisfy a request that
// carries Authorization.
//
// RFC 9111 §3.5: a shared cache MUST NOT use a stored response to satisfy such a
// request unless the response contains public, s-maxage, or must-revalidate.
// Anything else — including a response with no directives at all — is not
// reusable for an authenticated request.
func (p responsePolicy) sharedAuthReuse() bool {
	return p.Public || p.HasSMaxAge || p.revalidationRequired()
}

// sharedAuthStorable reports whether a response produced FOR a request carrying
// Authorization may be published into the shared cache at all.
//
// This is deliberately stricter than sharedAuthReuse. §3.5 governs reuse by an
// authenticated request; it does not make an authenticated response safe to hand
// to an anonymous one. must-revalidate says "do not serve this stale", not "this
// is not user-specific", so it does not authorize publication here. Only an
// explicit shared-cache permission — public, or an explicit shared lifetime via
// s-maxage — does.
func (p responsePolicy) sharedAuthStorable() bool {
	return p.Public || p.HasSMaxAge
}
