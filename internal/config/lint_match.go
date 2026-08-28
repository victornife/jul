// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net/textproto"
	"strings"
)

// This file holds the lint rules for location matching (ADR 0018 §15): the
// forwarded-header finding, the hop-by-hop warning, and the unreachable-route
// rule, which reports a shadowed location only where the shadowing is provable.

// LocationPreflightWidening reports the effective matcher bit ADR 0018 §14 calls
// preflight_widening:
//
//	preflight_widening := cors.enabled && match.methods is present
//
// §2 widens a methods predicate on a CORS-enabled location to accept that
// location's own preflight, which makes cors.enabled a *matcher* input and not
// only a policy input. Two routes with the same type, path and predicates but
// different cors.enabled are therefore different routes, and collapsing them
// would give them one auth, WAF and rate-limit scope.
func LocationPreflightWidening(loc LocationConfig) bool {
	return loc.CORS != nil && loc.CORS.Enabled && loc.Match.Methods != nil
}

// matchPredicateDiagnostics reports the per-predicate findings of §3: a
// forwarded-header predicate that passed validation's trusted_proxies
// precondition is still a SeverityError, because the declared trust extends to
// the proxy and not to the client behind it; a hop-by-hop predicate is a warning.
func matchPredicateDiagnostics(srv *ServerConfig, serverIndex int) []Diagnostic {
	var diags []Diagnostic
	for j := range srv.Locations {
		for k, h := range srv.Locations[j].Match.Headers {
			name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(h.Name))
			field := fmt.Sprintf("servers[%d].locations[%d].match.headers[%d]", serverIndex, j, k)
			switch {
			case IsForwardedHeaderName(name):
				diags = append(diags, Diagnostic{
					Severity: SeverityError,
					Field:    field,
					Message:  fmt.Sprintf("routing on %q trusts a field the client can set; matching runs before the forwarded chain is rebuilt, so the value is whatever arrived on the wire", name),
					Hint:     "route on a field your own proxy sets under a private name, or accept that this rule holds only as far as the declared trusted_proxies",
				})
			case IsHopByHopHeaderName(name):
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    field,
					Message:  fmt.Sprintf("%q is connection-scoped, so this predicate behaves differently on HTTP/1.1 and on HTTP/2 or HTTP/3", name),
					Hint:     "match an end-to-end field instead",
				})
			}
		}
	}
	return diags
}

// unreachableLocationDiagnostics reports a later location as unreachable only
// when an earlier location with the same (type, path) provably subsumes it.
//
// The rule is deliberately incomplete (§15). A false "this route is
// unreachable" is worse than silence: an operator who acts on it deletes a
// route that was in fact reachable and loses traffic. Two different regexes, a
// regex against an exact value, and disjoint predicate names are all reachable
// as far as this rule is concerned, and are not reported.
func unreachableLocationDiagnostics(srv *ServerConfig, serverIndex int) []Diagnostic {
	var diags []Diagnostic
	for j := range srv.Locations {
		later := &srv.Locations[j]
		for i := 0; i < j; i++ {
			earlier := &srv.Locations[i]
			if normalizedMatchType(earlier.Match.Type) != normalizedMatchType(later.Match.Type) || earlier.Match.Path != later.Match.Path {
				continue
			}
			if !subsumes(earlier, later) {
				continue
			}
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("servers[%d].locations[%d]", serverIndex, j),
				Message: fmt.Sprintf("locations[%d] (%s %q) matches every request this block could, so this block is unreachable",
					i, normalizedMatchType(earlier.Match.Type), earlier.Match.Path),
				Hint: "remove this block, narrow the earlier one, or reorder them",
			})
			break
		}
	}
	return diags
}

// subsumes reports whether every request the later location could match is
// already taken by the earlier one, by the provable rules of §15 and no others.
func subsumes(earlier, later *LocationConfig) bool {
	e, l := earlier.Match, later.Match
	if !e.HasPredicates() {
		// The earlier location constrains nothing beyond the path they share.
		return true
	}
	if !methodsSubsume(e.Methods, l.Methods) {
		return false
	}
	// Once the earlier route constrains the method, a preflight the later route
	// widens for is a request the earlier one rejects — so it is not shadowed.
	if e.Methods != nil && LocationPreflightWidening(*later) && !LocationPreflightWidening(*earlier) {
		return false
	}
	for _, ep := range e.Headers {
		if !headerPredicateSubsumed(ep, l.Headers) {
			return false
		}
	}
	for _, ep := range e.Query {
		if !queryPredicateSubsumed(ep, l.Query) {
			return false
		}
	}
	return true
}

// methodsSubsume reports whether an earlier method set accepts every method the
// later one does. methods is an OR-set, so a *subset* shadows nothing: ["GET"]
// does not shadow ["GET", "POST"], because the later route is the only one
// answering POST.
func methodsSubsume(earlier, later []string) bool {
	if earlier == nil {
		return true
	}
	if later == nil {
		// The later route accepts every method; the earlier one does not.
		return false
	}
	allowed := make(map[string]struct{}, len(earlier))
	for _, m := range earlier {
		allowed[m] = struct{}{}
	}
	for _, m := range later {
		if _, ok := allowed[m]; !ok {
			return false
		}
	}
	return true
}

// headerPredicateSubsumed reports whether some later predicate provably implies
// the earlier one: presence is implied by any predicate on the same name, and
// exact and regex only by a byte-equal counterpart.
func headerPredicateSubsumed(earlier HeaderMatch, later []HeaderMatch) bool {
	name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(earlier.Name))
	for _, l := range later {
		if textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(l.Name)) != name {
			continue
		}
		if earlier.Op == "present" {
			return true
		}
		if l.Op == earlier.Op && predicateValuesEqual(earlier.Value, l.Value) {
			return true
		}
	}
	return false
}

// queryPredicateSubsumed is headerPredicateSubsumed for query parameters, whose
// names are compared verbatim rather than canonicalized.
func queryPredicateSubsumed(earlier QueryMatch, later []QueryMatch) bool {
	for _, l := range later {
		if l.Name != earlier.Name {
			continue
		}
		if earlier.Op == "present" {
			return true
		}
		if l.Op == earlier.Op && predicateValuesEqual(earlier.Value, l.Value) {
			return true
		}
	}
	return false
}

func predicateValuesEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// normalizedMatchType treats an omitted type as "prefix", the documented
// default, so two blocks that differ only in spelling still compare equal.
func normalizedMatchType(t string) string {
	if t == "" {
		return "prefix"
	}
	return t
}
