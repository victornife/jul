// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"

	"jul/internal/config"
)

// This file holds the compiled request predicates a location may carry beyond
// its path (ADR 0018 §2-§5), and the bounded, lazy query parser they use.
//
// Everything here is built once, inside router.New during Prepare, and is
// immutable afterwards: no regex is compiled and no configuration is parsed on
// the request path, ever.

// compiledPredicates is one location's predicate set. A nil *compiledPredicates
// means the location constrains nothing beyond its path, which is the fast path
// every pre-ADR-0018 configuration takes.
type compiledPredicates struct {
	// methods is the OR-set of accepted methods in declaration order, compared
	// byte-exactly. nil means the method is unconstrained.
	methods []string
	// acceptsHEAD records that GET is listed, so HEAD is accepted too
	// (RFC 9110 §9.3.2 defines HEAD as GET without a body).
	acceptsHEAD bool
	// preflightWidening records that this location answers CORS preflights, so
	// a methods predicate additionally accepts one (§2). Without it a
	// CORS-enabled route with methods = ["GET", "POST"] could never be selected
	// for its own preflight.
	preflightWidening bool

	headers []compiledHeaderPredicate
	query   []compiledQueryPredicate
}

type compiledHeaderPredicate struct {
	name  string // canonicalized at compile time
	op    string
	value string
	re    *regexp.Regexp // op == "regex" only
}

type compiledQueryPredicate struct {
	name  string
	op    string
	value string
}

// predicateFailure names the predicate that rejected a candidate. It is a
// value, not a message: a mismatch is an ordinary routing outcome that happens
// on every request to a predicate-bearing path, so nothing is formatted or
// allocated here. The route-test surface renders it; the request path discards it.
type predicateFailure struct {
	kind  string // "", "method", "header" or "query"
	index int    // index within match.headers / match.query; -1 for a method
}

// compilePredicates builds the immutable predicate set for a location, or nil
// when the location carries none. Validation has already rejected everything
// malformed; the errors here are the compile-time backstop that makes an
// invalid pattern fail the reload transaction rather than a request.
func compilePredicates(loc config.LocationConfig) (*compiledPredicates, error) {
	m := loc.Match
	if !m.HasPredicates() {
		return nil, nil
	}
	p := &compiledPredicates{
		preflightWidening: config.LocationPreflightWidening(loc),
	}
	if m.Methods != nil {
		p.methods = append([]string(nil), m.Methods...)
		for _, method := range p.methods {
			if method == http.MethodGet {
				p.acceptsHEAD = true
			}
		}
	}
	for i, h := range m.Headers {
		cp := compiledHeaderPredicate{
			name: textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(h.Name)),
			op:   h.Op,
		}
		if h.Value != nil {
			cp.value = *h.Value
		}
		if h.Op == "regex" {
			re, err := regexp.Compile(cp.value)
			if err != nil {
				return nil, fmt.Errorf("location %q match.headers[%d]: invalid regex %q: %w", m.Path, i, cp.value, err)
			}
			cp.re = re
		}
		p.headers = append(p.headers, cp)
	}
	for _, q := range m.Query {
		cp := compiledQueryPredicate{name: q.Name, op: q.Op}
		if q.Value != nil {
			cp.value = *q.Value
		}
		p.query = append(p.query, cp)
	}
	return p, nil
}

// match evaluates every predicate against the request. The predicates of one
// location are ANDed; a list inside one field is an OR-set (§5). The second
// result names the first predicate that failed, for the route-test surface.
func (p *compiledPredicates) match(r *http.Request, q *requestQuery) (bool, predicateFailure) {
	if p == nil {
		return true, predicateFailure{}
	}
	if !p.matchMethod(r) {
		return false, predicateFailure{kind: "method", index: -1}
	}
	for i := range p.headers {
		if !p.headers[i].match(r.Header) {
			return false, predicateFailure{kind: "header", index: i}
		}
	}
	for i := range p.query {
		if !p.query[i].match(q) {
			return false, predicateFailure{kind: "query", index: i}
		}
	}
	return true, predicateFailure{}
}

// matchMethod compares r.Method byte-exactly against the configured OR-set.
// HTTP methods are case-sensitive (RFC 9110 §9.1), so nothing is case-folded.
func (p *compiledPredicates) matchMethod(r *http.Request) bool {
	if p.methods == nil {
		return true
	}
	for _, method := range p.methods {
		if r.Method == method {
			return true
		}
	}
	if p.acceptsHEAD && r.Method == http.MethodHead {
		return true
	}
	return p.preflightWidening && isCORSPreflight(r)
}

// isCORSPreflight reports whether the request is a CORS preflight: OPTIONS
// carrying exactly one Origin and an Access-Control-Request-Method. A plain
// OPTIONS that is not a preflight still obeys the methods predicate.
func isCORSPreflight(r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	if len(r.Header.Values("Origin")) != 1 {
		return false
	}
	return len(r.Header.Values("Access-Control-Request-Method")) > 0
}

// match evaluates one header predicate. Lookup is by canonical name, so field
// names match case-insensitively on every protocol version. Values are compared
// byte-exactly and are never split on commas: "Accept: a, b" is one value, and
// splitting it would be wrong for Date, Set-Cookie and every other field whose
// grammar admits a comma.
func (h *compiledHeaderPredicate) match(header http.Header) bool {
	values := header.Values(h.name)
	switch h.op {
	case "present":
		// Present-empty counts as present; absent and present-empty stay
		// distinguishable because op = "exact" with an empty value matches only
		// the latter.
		return len(values) > 0
	case "exact":
		for _, v := range values {
			if v == h.value {
				return true
			}
		}
	case "regex":
		// RE2, unanchored, per value, any-match — consistent with the existing
		// match.type = "regex" path matcher.
		for _, v := range values {
			if h.re.MatchString(v) {
				return true
			}
		}
	}
	return false
}

// match evaluates one query predicate against the lazily parsed query.
func (q *compiledQueryPredicate) match(rq *requestQuery) bool {
	switch q.op {
	case "present":
		// "?x" and "?x=" both count as present.
		return rq.present(q.name)
	case "exact":
		return rq.hasValue(q.name, q.value)
	}
	return false
}

// queryPair is one decoded name/value pair of a request's query string.
type queryPair struct {
	name  string
	value string
}

// requestQuery parses a request's query string at most once, lazily, and only
// when a candidate route actually carries a query predicate. A configuration
// with no query predicates therefore never parses one, which is what keeps the
// path-only fast path untouched.
//
// The parsed form is a flat slice rather than a url.Values map. A map costs a
// hash and a one-element slice header per distinct key, which at §16's cap of
// 1024 pairs measured at 116 µs and 207 KiB per request — a 20x allocation
// amplification of a 10 KiB request line, which is the shape the bound exists
// to prevent. A pre-sized slice scanned linearly is one allocation, and
// predicates are capped at 16 per location, so the scan is bounded by a number
// an operator can read off their own configuration. The representation is
// private; §4's semantics are what is frozen.
type requestQuery struct {
	raw    string
	parsed bool
	pairs  []queryPair
}

// ensureParsed parses the query string on first use.
func (q *requestQuery) ensureParsed() {
	if q.parsed {
		return
	}
	q.pairs = parseBoundedQuery(q.raw)
	q.parsed = true
}

// present reports whether the parameter appears at all.
func (q *requestQuery) present(name string) bool {
	q.ensureParsed()
	for i := range q.pairs {
		if q.pairs[i].name == name {
			return true
		}
	}
	return false
}

// hasValue reports whether any occurrence of the parameter decodes to value.
func (q *requestQuery) hasValue(name, value string) bool {
	q.ensureParsed()
	for i := range q.pairs {
		if q.pairs[i].name == name && q.pairs[i].value == value {
			return true
		}
	}
	return false
}

// parseBoundedQuery implements url.ParseQuery's semantics with two deliberate
// differences (§4).
//
// A malformed percent-escape makes only that pair unusable rather than failing
// the parse: a gateway must not turn a request it would otherwise forward into
// a 400 because a route it did not select carried a query predicate. And only
// the first config.MaxQueryPairsParsed pairs are considered, because
// max_header_bytes bounds the request line at a size that still admits hundreds
// of thousands of empty pairs, which is an allocation amplifier a routing
// decision must not expose.
func parseBoundedQuery(raw string) []queryPair {
	if raw == "" {
		return nil
	}
	// One counting pass, so the slice is allocated exactly once at the right
	// size and a long query cannot walk it through a doubling sequence.
	size := strings.Count(raw, "&") + 1
	if size > config.MaxQueryPairsParsed {
		size = config.MaxQueryPairsParsed
	}
	out := make([]queryPair, 0, size)

	seen := 0
	for raw != "" && seen < config.MaxQueryPairsParsed {
		var pair string
		pair, raw, _ = strings.Cut(raw, "&")
		seen++
		if pair == "" {
			continue
		}
		if strings.Contains(pair, ";") {
			// url.ParseQuery rejects a semicolon rather than treating it as a
			// separator; the pair is dropped and the rest is still parsed.
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		name, err := url.QueryUnescape(name)
		if err != nil {
			continue
		}
		value, err = url.QueryUnescape(value)
		if err != nil {
			continue
		}
		out = append(out, queryPair{name: name, value: value})
	}
	return out
}
