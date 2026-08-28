// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"net/http"
	"regexp"
	"strings"
)

// compiledRewrite is a regex rewrite rule prepared at build time.
type compiledRewrite struct {
	re          *regexp.Regexp
	replacement string
	flag        string // "", "last", "break", "redirect", "permanent"
}

// applyRewrites runs the location's rewrite rules against the request path.
// It returns true if it already wrote a response (a redirect), in which case
// the caller must not continue to the handler.
//
// Simplification vs NGINX: "last" and "break" both stop further rewriting but
// do not trigger a fresh location search; the already-matched location handles
// the (rewritten) request.
func applyRewrites(rules []compiledRewrite, w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	for _, rw := range rules {
		if !rw.re.MatchString(path) {
			continue
		}
		target := rw.re.ReplaceAllString(path, rw.replacement)
		switch rw.flag {
		case "redirect":
			http.Redirect(w, r, target, http.StatusFound)
			return true
		case "permanent":
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return true
		default: // "", "last", "break"
			path = target
			r.URL.Path = target
			if rw.flag == "last" || rw.flag == "break" {
				return false
			}
		}
	}
	return false
}

// selectLocation resolves a request to a location within a server, implementing
// ADR 0018 §6: the tiered enumeration with fallthrough. It returns nil when no
// candidate matched, which Router.For answers with the existing 404 — there is
// no automatic 405 and no Allow header anywhere.
func (s *serverRoute) selectLocation(r *http.Request) *locationRoute {
	q := requestQuery{raw: r.URL.RawQuery}
	var selected *locationRoute
	s.eachCandidate(r.URL.Path, func(_ int, loc *locationRoute) bool {
		if ok, _ := loc.predicates.match(r, &q); ok {
			selected = loc
			return true
		}
		return false
	})
	return selected
}

// eachCandidate visits every location whose path matches, in the frozen order:
//
//	tier 1  exact locations whose path equals the request path, declaration order
//	tier 2  non-root prefix locations whose path is a prefix of it, descending
//	        path length, ties in declaration order
//	tier 3  regex locations whose pattern matches it, declaration order
//	tier 4  `prefix "/"` locations, declaration order
//
// Visiting stops as soon as visit reports that it selected a candidate.
//
// Two properties are load-bearing. Path specificity always outranks predicates:
// predicates filter candidates within a tier and never promote one across tiers
// or across prefix lengths, so no scoring exists anywhere. And declaration order
// is the only tie-breaker, at every tier — the tier slices are built once, in
// declaration order, and no map is iterated in selection.
//
// ADR 0018 §6 folds `prefix "/"` into tier 2 on the stated grounds that a
// candidate of length 1 sorts last among prefixes and therefore "behaves
// exactly as the current sr.fallback does". That is not true, and the
// differential gate the same issue mandates is what shows it: sr.fallback is
// consulted *after* the regex tier, so folding it into tier 2 would let a
// `prefix "/"` location shadow every regex location — the ordinary
// `location /` plus `location ~ \.php$` shape. That is a silent traffic move on
// a very common configuration, which is the one outcome §6 exists to prevent,
// so the catch-all keeps its position and gets its own tier. Everything §6
// actually asks for survives: it is an enumerated candidate with fallthrough,
// it can carry predicates, and duplicates resolve to the first declared rather
// than the last, which is what lint.go has always told the operator.
//
// A request path that is not rooted — the empty path Go gives an authority-form
// CONNECT, or the "*" of a server-wide OPTIONS — matches no tier, including
// this one, and is answered with the router's ordinary 404. §2 depends on that
// property for its CONNECT rule.
func (s *serverRoute) eachCandidate(path string, visit func(tier int, loc *locationRoute) bool) {
	for _, loc := range s.exactLocations {
		if loc.path == path && visit(1, loc) {
			return
		}
	}
	for _, loc := range s.prefixLocations {
		if strings.HasPrefix(path, loc.path) && visit(2, loc) {
			return
		}
	}
	for _, loc := range s.regexLocations {
		if loc.re.MatchString(path) && visit(3, loc) {
			return
		}
	}
	for _, loc := range s.rootLocations {
		if strings.HasPrefix(path, "/") && visit(4, loc) {
			return
		}
	}
}
