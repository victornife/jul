// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net/textproto"
	"regexp"
	"sort"
	"strings"
)

// This file holds the location match validator: the path coordinates every
// location has always had, plus the bounded method/header/query predicates
// frozen by ADR 0018 §2-§4 and bounded by §16.

// Bounds on the declarative match predicates (ADR 0018 §16). They are
// conservative initial safety ceilings rather than benchmark-derived capacity
// limits: raising one later is additive, lowering an advertised one is
// breaking, and a ceiling set too high first exposes CPU and memory behaviour
// in production rather than in review.
const (
	// MaxMatchMethods bounds match.methods entries per location.
	MaxMatchMethods = 16
	// MaxMatchHeaders bounds match.headers entries per location.
	MaxMatchHeaders = 16
	// MaxMatchQuery bounds match.query entries per location.
	MaxMatchQuery = 16
	// MaxMatchHeaderRegexes bounds how many header predicates may use op = "regex".
	MaxMatchHeaderRegexes = 8
	// MaxMatchHeaderPatternBytes bounds one header regex pattern.
	MaxMatchHeaderPatternBytes = 512
	// MaxMatchHeaderValueBytes bounds one header predicate value. A count limit
	// on unbounded-length entries is not a bound.
	MaxMatchHeaderValueBytes = 1024
	// MaxMatchQueryNameBytes bounds one query predicate name.
	MaxMatchQueryNameBytes = 1024
	// MaxMatchQueryValueBytes bounds one query predicate value.
	MaxMatchQueryValueBytes = 1024
	// MaxQueryPairsParsed bounds how many query pairs a request contributes to
	// matching. Unlike the others this bounds request-time work, not
	// configuration size, so it is enforced by the router rather than here.
	MaxQueryPairsParsed = 1024
)

// ianaMethods is the IANA HTTP Method Registry. It is consulted for exactly one
// rule (ADR 0018 §2): a configured method whose ASCII-uppercase form is
// registered but which is not itself uppercase is rejected, which catches the
// only realistic mistake — methods = ["get"] — without mechanically uppercasing
// a genuinely lowercase extension method and silently breaking it.
var ianaMethods = map[string]struct{}{
	"ACL": {}, "BASELINE-CONTROL": {}, "BIND": {}, "CHECKIN": {}, "CHECKOUT": {},
	"CONNECT": {}, "COPY": {}, "DELETE": {}, "GET": {}, "HEAD": {}, "LABEL": {},
	"LINK": {}, "LOCK": {}, "MERGE": {}, "MKACTIVITY": {}, "MKCALENDAR": {},
	"MKCOL": {}, "MKREDIRECTREF": {}, "MKWORKSPACE": {}, "MOVE": {},
	"OPTIONS": {}, "ORDERPATCH": {}, "PATCH": {}, "POST": {}, "PRI": {},
	"PROPFIND": {}, "PROPPATCH": {}, "PUT": {}, "REBIND": {}, "REPORT": {},
	"SEARCH": {}, "TRACE": {}, "UNBIND": {}, "UNCHECKOUT": {}, "UNLINK": {},
	"UNLOCK": {}, "UPDATE": {}, "UPDATEREDIRECTREF": {}, "VERSION-CONTROL": {},
}

// hopByHopHeaders are connection-scoped field names. A predicate on one is
// accepted but lint-warned (§3): it behaves differently on HTTP/1.1 and HTTP/2
// for reasons the operator did not choose.
var hopByHopHeaders = map[string]struct{}{
	"Connection": {}, "Keep-Alive": {}, "Te": {}, "Trailer": {},
	"Transfer-Encoding": {}, "Upgrade": {}, "Proxy-Connection": {},
	"Proxy-Authenticate": {}, "Proxy-Authorization": {},
}

// rfc9440Headers are the certificate-assertion field names. Like the forwarding
// headers they are asserted by a proxy and forgeable by a client, so they carry
// the same trusted_proxies precondition (§3).
var rfc9440Headers = map[string]struct{}{
	"Client-Cert": {}, "Client-Cert-Chain": {},
}

// IsForwardedHeaderName reports whether a canonical field name is one whose
// value Jul only believes from a declared trusted proxy: RFC 7239 Forwarded,
// any X-Forwarded-* field, or an RFC 9440 certificate assertion. Route
// selection runs before setCanonicalXForwarded, so at matching time these
// fields still hold whatever the client sent.
func IsForwardedHeaderName(canonical string) bool {
	if canonical == "Forwarded" || strings.HasPrefix(canonical, "X-Forwarded-") {
		return true
	}
	_, ok := rfc9440Headers[canonical]
	return ok
}

// IsHopByHopHeaderName reports whether a canonical field name is connection-scoped.
func IsHopByHopHeaderName(canonical string) bool {
	_, ok := hopByHopHeaders[canonical]
	return ok
}

// isFieldToken reports whether s is a valid RFC 9110 §5.6.2 token, the grammar
// shared by method names and header field names.
func isFieldToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validateMatch checks a location's match block: the path coordinates, then the
// predicates. srv supplies the listener's trusted-proxy policy, which is the
// precondition for a forwarded-header predicate (§3).
func validateMatch(m MatchConfig, srv ServerConfig, where string) []error {
	var errs []error
	switch m.Type {
	case "exact", "prefix", "regex":
	case "":
		errs = append(errs, fmt.Errorf("%s: match.type is required (exact|prefix|regex)", where))
	default:
		errs = append(errs, fmt.Errorf("%s: invalid match.type %q (want exact|prefix|regex)", where, m.Type))
	}
	if strings.TrimSpace(m.Path) == "" {
		errs = append(errs, fmt.Errorf("%s: match.path is required", where))
	}
	if m.Type == "regex" {
		if _, err := regexp.Compile(m.Path); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid match regex %q: %v", where, m.Path, err))
		}
	}
	errs = append(errs, validateMatchMethods(m.Methods, where)...)
	errs = append(errs, validateMatchHeaders(m.Headers, srv, where)...)
	errs = append(errs, validateMatchQuery(m.Query, where)...)
	return errs
}

// validateMatchMethods checks match.methods (§2). Omitted means unconstrained;
// an explicitly empty list is an error, because a route that can never match is
// a configuration mistake rather than a way to disable a route.
func validateMatchMethods(methods []string, where string) []error {
	if methods == nil {
		return nil
	}
	var errs []error
	if len(methods) == 0 {
		return []error{fmt.Errorf("%s: match.methods is empty; omit the field to leave the method unconstrained, since an empty list is a route that can never match", where)}
	}
	if len(methods) > MaxMatchMethods {
		errs = append(errs, fmt.Errorf("%s: match.methods has %d entries, over the limit of %d", where, len(methods), MaxMatchMethods))
	}
	seen := make(map[string]int, len(methods))
	for i, method := range methods {
		field := fmt.Sprintf("%s.match.methods[%d]", where, i)
		if !isFieldToken(method) {
			errs = append(errs, fmt.Errorf("%s: %q is not a valid HTTP method token", field, method))
			continue
		}
		if method == "CONNECT" {
			errs = append(errs, fmt.Errorf("%s: CONNECT cannot be matched; Jul implements no tunnelling, and Go gives an authority-form CONNECT an empty URL path, which matches no location", field))
			continue
		}
		upper := strings.ToUpper(method)
		if _, registered := ianaMethods[upper]; registered && method != upper {
			errs = append(errs, fmt.Errorf("%s: method %q must be spelled %q; HTTP methods are case-sensitive (RFC 9110 §9.1)", field, method, upper))
			continue
		}
		if first, dup := seen[method]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate method %q (already listed at match.methods[%d])", field, method, first))
			continue
		}
		seen[method] = i
	}
	return errs
}

// validateMatchHeaders checks match.headers (§3).
func validateMatchHeaders(headers []HeaderMatch, srv ServerConfig, where string) []error {
	if len(headers) == 0 {
		return nil
	}
	var errs []error
	if len(headers) > MaxMatchHeaders {
		errs = append(errs, fmt.Errorf("%s: match.headers has %d entries, over the limit of %d", where, len(headers), MaxMatchHeaders))
	}
	regexes := 0
	trusted := srv.ClientAddress != nil && len(srv.ClientAddress.TrustedProxies) > 0
	for i, h := range headers {
		field := fmt.Sprintf("%s.match.headers[%d]", where, i)
		name := strings.TrimSpace(h.Name)
		switch {
		case name == "":
			errs = append(errs, fmt.Errorf("%s: name is required", field))
		case strings.HasPrefix(name, ":"):
			errs = append(errs, fmt.Errorf("%s: %q is an HTTP/2 or HTTP/3 pseudo-header, not a field; Go never exposes one in the request headers, so the predicate could never match", field, name))
		case !isFieldToken(name):
			errs = append(errs, fmt.Errorf("%s: %q is not a valid header field name", field, name))
		default:
			canonical := textproto.CanonicalMIMEHeaderKey(name)
			if canonical == "Host" {
				errs = append(errs, fmt.Errorf("%s: Host cannot be matched as a header; Go moves it out of the request headers, so the predicate could never match. Use the server block's server_names", field))
			}
			if IsForwardedHeaderName(canonical) && !trusted {
				errs = append(errs, fmt.Errorf("%s: a predicate on %q requires this listener to declare [servers.client_address] trusted_proxies; the field is client-supplied at matching time, so without a declared proxy the rule is trivially forged", field, canonical))
			}
		}

		switch h.Op {
		case "present":
			if h.Value != nil {
				errs = append(errs, fmt.Errorf(`%s: op = "present" takes no value; use op = "exact" with value = "" to match a present-but-empty field`, field))
			}
		case "exact":
			errs = append(errs, requiredPredicateValue(h.Value, field, MaxMatchHeaderValueBytes)...)
		case "regex":
			regexes++
			errs = append(errs, requiredPredicateValue(h.Value, field, MaxMatchHeaderValueBytes)...)
			if h.Value != nil {
				if len(*h.Value) > MaxMatchHeaderPatternBytes {
					errs = append(errs, fmt.Errorf("%s: regex pattern is %d bytes, over the limit of %d", field, len(*h.Value), MaxMatchHeaderPatternBytes))
				} else if _, err := regexp.Compile(*h.Value); err != nil {
					errs = append(errs, fmt.Errorf("%s: invalid regex %q: %v", field, *h.Value, err))
				}
			}
		case "":
			errs = append(errs, fmt.Errorf("%s: op is required (present|exact|regex)", field))
		default:
			errs = append(errs, fmt.Errorf("%s: invalid op %q (want present|exact|regex)", field, h.Op))
		}
	}
	if regexes > MaxMatchHeaderRegexes {
		errs = append(errs, fmt.Errorf("%s: match.headers has %d regex predicates, over the limit of %d", where, regexes, MaxMatchHeaderRegexes))
	}
	return errs
}

// validateMatchQuery checks match.query (§4).
func validateMatchQuery(query []QueryMatch, where string) []error {
	if len(query) == 0 {
		return nil
	}
	var errs []error
	if len(query) > MaxMatchQuery {
		errs = append(errs, fmt.Errorf("%s: match.query has %d entries, over the limit of %d", where, len(query), MaxMatchQuery))
	}
	for i, q := range query {
		field := fmt.Sprintf("%s.match.query[%d]", where, i)
		switch {
		case q.Name == "":
			errs = append(errs, fmt.Errorf("%s: name is required", field))
		case len(q.Name) > MaxMatchQueryNameBytes:
			errs = append(errs, fmt.Errorf("%s: name is %d bytes, over the limit of %d", field, len(q.Name), MaxMatchQueryNameBytes))
		}
		switch q.Op {
		case "present":
			if q.Value != nil {
				errs = append(errs, fmt.Errorf(`%s: op = "present" takes no value; use op = "exact" with value = "" to match ?%s= and ?%s`, field, q.Name, q.Name))
			}
		case "exact":
			errs = append(errs, requiredPredicateValue(q.Value, field, MaxMatchQueryValueBytes)...)
		case "":
			errs = append(errs, fmt.Errorf("%s: op is required (present|exact)", field))
		default:
			errs = append(errs, fmt.Errorf("%s: invalid op %q (want present|exact)", field, q.Op))
		}
	}
	return errs
}

// requiredPredicateValue checks a value that the operation requires. An
// explicitly empty value is legal and meaningful; an omitted one is not, and the
// two never collapse.
func requiredPredicateValue(value *string, field string, limit int) []error {
	if value == nil {
		return []error{fmt.Errorf("%s: value is required for this op", field)}
	}
	if len(*value) > limit {
		return []error{fmt.Errorf("%s: value is %d bytes, over the limit of %d", field, len(*value), limit)}
	}
	return nil
}

// HasPredicates reports whether the match constrains anything beyond the path.
// A location with no predicates is selected by the first path candidate that
// reaches it, which is the pre-ADR-0018 behaviour and the fast path.
func (m MatchConfig) HasPredicates() bool {
	return len(m.Methods) > 0 || len(m.Headers) > 0 || len(m.Query) > 0
}

// CanonicalPredicates renders the match's predicate set in a normalized,
// order-independent form: methods sorted, header and query predicates sorted by
// (name, op, value), header names canonicalized. It is the one implementation
// of "these two routes match the same requests", shared by the policy-scope
// fingerprint (ADR 0018 §14) and the lint shadowing rule (§15), so the two can
// never disagree about what a predicate set is.
//
// It deliberately excludes the path coordinates and preflight_widening: callers
// combine it with whichever of those their identity needs.
func (m MatchConfig) CanonicalPredicates() string {
	var b strings.Builder
	methods := append([]string(nil), m.Methods...)
	sort.Strings(methods)
	if m.Methods == nil {
		b.WriteString("methods=*\n")
	} else {
		b.WriteString("methods=" + strings.Join(methods, ",") + "\n")
	}
	for _, entry := range canonicalPredicateLines(m) {
		b.WriteString(entry)
		b.WriteByte('\n')
	}
	return b.String()
}

// canonicalPredicateLines renders the header and query predicates as sorted,
// unambiguous lines. Lengths prefix the operator-controlled strings so no value
// containing a separator can impersonate a different predicate set.
func canonicalPredicateLines(m MatchConfig) []string {
	lines := make([]string, 0, len(m.Headers)+len(m.Query))
	for _, h := range m.Headers {
		lines = append(lines, "header|"+lengthTagged(textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(h.Name)))+"|"+h.Op+"|"+lengthTaggedPtr(h.Value))
	}
	for _, q := range m.Query {
		lines = append(lines, "query|"+lengthTagged(q.Name)+"|"+q.Op+"|"+lengthTaggedPtr(q.Value))
	}
	sort.Strings(lines)
	return lines
}

func lengthTagged(s string) string { return fmt.Sprintf("%d:%s", len(s), s) }

func lengthTaggedPtr(s *string) string {
	if s == nil {
		return "-"
	}
	return lengthTagged(*s)
}
