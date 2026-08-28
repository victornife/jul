// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"fmt"
	"net/http"
)

// This file exposes the router's own selection to the diagnostic surfaces.
//
// ADR 0014 allows exactly one server-side implementation behind every operator
// surface, and ADR 0018 §14 applies it here by name: the admin route-test
// endpoint used to carry its own bestServer/bestLocation pair, which was merely
// stale before predicates existed and would have become a second matching
// *semantics* the moment they did. Explain is the seam that removes it — the
// same tier slices, the same enumeration, the same predicates.

// RouteCandidate is one location the enumeration visited, in visit order.
type RouteCandidate struct {
	// ServerIndex and LocationIndex are coordinates into the operator's own
	// configuration: config.Config.Servers[ServerIndex].Locations[LocationIndex].
	ServerIndex   int
	LocationIndex int
	// Tier is 1 for exact, 2 for prefix, 3 for regex and 4 for the `prefix "/"`
	// catch-all.
	Tier      int
	MatchType string
	Path      string
	// Selected is true for the one candidate that took the request.
	Selected bool
	// Rejection names the predicate that rejected this candidate — for example
	// "match.methods" or "match.headers[1]" — and is empty on the selected one.
	// A predicate mismatch is never logged per request, so this is the only
	// place an operator can see one.
	Rejection string
}

// Explanation is the full result of resolving a request against the compiled
// table: which server block took the host, every path candidate it produced,
// and which of them survived its predicates.
type Explanation struct {
	// ServerMatched reports whether any server block was selected for the host.
	ServerMatched bool
	// ServerIndex is the index into config.Config.Servers of the selected block.
	ServerIndex int
	// Candidates is every visited candidate, in enumeration order. Enumeration
	// stops at the selected candidate, so a candidate declared after it does not
	// appear: it was never consulted, which is itself the answer to "why not
	// this route".
	Candidates []RouteCandidate
	// Selected indexes Candidates, or is -1 when nothing matched and the request
	// would be answered with the router's ordinary 404.
	Selected int
}

// SelectedCandidate returns the winning candidate, if any.
func (e Explanation) SelectedCandidate() (RouteCandidate, bool) {
	if e.Selected < 0 || e.Selected >= len(e.Candidates) {
		return RouteCandidate{}, false
	}
	return e.Candidates[e.Selected], true
}

// Explain resolves req the way For does and reports every step, without running
// a handler, dialling an upstream or mutating anything. host is the value the
// request's Host header would carry.
//
// Server selection differs from For in one respect and one only: For is scoped
// to the listen address the connection arrived on, whereas a dry run has no
// connection, so every configured block is considered and a block with no
// server_names acts as the default. Location selection is identical code.
func (r *Router) Explain(host string, req *http.Request) Explanation {
	out := Explanation{Selected: -1}
	srv := r.bestServerForHost(host)
	if srv == nil {
		return out
	}
	out.ServerMatched = true
	out.ServerIndex = srv.index

	q := requestQuery{raw: req.URL.RawQuery}
	srv.eachCandidate(req.URL.Path, func(tier int, loc *locationRoute) bool {
		candidate := RouteCandidate{
			ServerIndex:   srv.index,
			LocationIndex: loc.index,
			Tier:          tier,
			MatchType:     normalizedMatchType(loc.matchType),
			Path:          loc.path,
		}
		ok, failure := loc.predicates.match(req, &q)
		if ok {
			candidate.Selected = true
			out.Candidates = append(out.Candidates, candidate)
			out.Selected = len(out.Candidates) - 1
			return true
		}
		candidate.Rejection = failure.field()
		out.Candidates = append(out.Candidates, candidate)
		return false
	})
	return out
}

// bestServerForHost picks the block a dry-run request resolves to: the highest
// host score wins, a block without server_names acts as the default, and ties go
// to the first declared block.
//
// byAddr is a map, so the traversal order is undefined — which is why the
// tie-break is the block's declaration index rather than "the first one seen".
// The result is identical for every iteration order.
func (r *Router) bestServerForHost(host string) *serverRoute {
	h := normalizeHost(host)
	var best *serverRoute
	bestScore := 0
	for _, ar := range r.byAddr {
		for _, srv := range ar.servers {
			score := srv.score(h)
			if score == 0 && len(srv.names) == 0 {
				score = 1 // the default server for its address
			}
			if score == 0 {
				continue
			}
			if best == nil || score > bestScore || (score == bestScore && srv.index < best.index) {
				best, bestScore = srv, score
			}
		}
	}
	return best
}

// field renders the failure as the configuration path of the predicate that
// rejected the candidate.
func (f predicateFailure) field() string {
	switch f.kind {
	case "method":
		return "match.methods"
	case "header":
		return fmt.Sprintf("match.headers[%d]", f.index)
	case "query":
		return fmt.Sprintf("match.query[%d]", f.index)
	default:
		return ""
	}
}

// normalizedMatchType renders an omitted match type as the documented default.
func normalizedMatchType(t string) string {
	if t == "" {
		return "prefix"
	}
	return t
}
