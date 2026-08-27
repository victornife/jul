// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"jul/internal/config"
	"jul/internal/router"
)

// routeTestRequest is the input to POST /api/routes/test: a synthetic request
// the operator wants to see resolved against the running configuration.
//
// Method and Headers predate route predicates and were accepted and discarded;
// they are real inputs now. RawQuery and HeaderValues are the additive fields
// ADR 0018 §14 froze, because a map[string]string cannot carry repeated field
// lines and there was no query input at all — which are exactly the cases §3
// and §4 spend most of their text specifying. A diagnostic that cannot
// reproduce the semantics it diagnoses is not a diagnostic.
type routeTestRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Host    string            `json:"host"`
	Headers map[string]string `json:"headers,omitempty"`
	// RawQuery is the query string exactly as it would appear after "?", parsed
	// with §4's rules, so repeated keys, percent-encoding, "+" and malformed
	// escapes all survive. It is not derived by splitting Path: a "?" inside
	// Path stays a literal, and every existing caller keeps working.
	RawQuery string `json:"raw_query,omitempty"`
	// HeaderValues is an ordered list appended to whatever Headers supplied, so a
	// caller can express two field lines of the same name. Headers is retained
	// verbatim and remains the convenient single-value form.
	HeaderValues []routeTestHeader `json:"header_values,omitempty"`
}

// routeTestHeader is one request header field line.
type routeTestHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// routeTestCandidate is one location the router's enumeration visited, in visit
// order. Predicate mismatch is never logged per request, so this is the only
// surface on which an operator can see why a route was passed over.
type routeTestCandidate struct {
	MatchType string `json:"match_type"`
	Match     string `json:"match"`
	// Tier is 1 exact, 2 prefix, 3 regex, 4 the "/" catch-all.
	Tier int `json:"tier"`
	// MatchOrdinal is the candidate's index among the locations sharing its
	// listen, host set, match type and path. It is revision-relative: it names a
	// route within this configuration and must not be stored or correlated.
	MatchOrdinal int  `json:"match_ordinal"`
	Selected     bool `json:"selected"`
	// Rejection is the configuration path of the predicate that rejected this
	// candidate, empty on the selected one.
	Rejection string `json:"rejection,omitempty"`
}

// routeTestResult is the dry-run match result returned to the Console. It mirrors
// how the router would resolve the request without sending real upstream traffic
// (Milestone 2.3).
type routeTestResult struct {
	Matched     bool     `json:"matched"`
	Listen      string   `json:"listen,omitempty"`
	ServerNames []string `json:"server_names,omitempty"`
	Match       string   `json:"match,omitempty"`
	MatchType   string   `json:"match_type,omitempty"`
	Action      string   `json:"action,omitempty"`
	Target      string   `json:"target,omitempty"`
	Upstream    string   `json:"upstream,omitempty"`
	Auth        bool     `json:"auth"`
	Cache       bool     `json:"cache"`
	Compression bool     `json:"compression"`
	RateLimit   bool     `json:"rate_limit"`
	Secure      bool     `json:"secure"`
	Warnings    []string `json:"warnings,omitempty"`
	Explanation string   `json:"explanation"`
	// MatchOrdinal is the selected location's ordinal among its same-coordinate
	// siblings, which is what a typed patch needs to address it unambiguously.
	MatchOrdinal int `json:"match_ordinal,omitempty"`
	// Candidates is every candidate the enumeration visited, including the
	// rejected ones and the reason each was rejected.
	Candidates []routeTestCandidate `json:"candidates,omitempty"`
}

// handleRouteTest resolves a synthetic request against the running config and
// returns the matched server/location and its effective edge flags. It is a
// pure dry-run: it never dials an upstream and never mutates state.
// POST /api/routes/test
func (s *Server) handleRouteTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil {
		writeJSON(w, http.StatusOK, map[string]any{"loaded": false})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var in routeTestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		path = "/"
	}
	in.Path = path
	writeJSON(w, http.StatusOK, testRoute(cfg, in))
}

// syntheticRequest builds the http.Request the router will be asked to resolve.
// It is never served: nothing dials an upstream, runs a handler or mutates state.
func syntheticRequest(in routeTestRequest) *http.Request {
	method := strings.TrimSpace(in.Method)
	if method == "" {
		method = http.MethodGet
	}
	req := &http.Request{
		Method: method,
		URL:    &url.URL{Path: in.Path, RawQuery: in.RawQuery},
		Header: http.Header{},
		Host:   in.Host,
	}
	// Headers is a map, so its traversal order is undefined — which is why the
	// ordered form exists. Two entries of a map necessarily have different names,
	// and no predicate compares across names, so the order they are added in
	// cannot change a match.
	for name, value := range in.Headers {
		req.Header.Add(name, value)
	}
	for _, h := range in.HeaderValues {
		req.Header.Add(h.Name, h.Value)
	}
	return req
}

// testRoute performs the dry-run resolution by asking the router itself, which
// is the whole point: a second matcher becomes a second semantics the moment
// predicates exist (ADR 0014, ADR 0018 §14).
func testRoute(c *config.Config, in routeTestRequest) routeTestResult {
	rt, err := router.New(c, nil, nil, nil, nil)
	if err != nil {
		return routeTestResult{
			Matched:     false,
			Explanation: "The running configuration could not be compiled into a routing table: " + err.Error(),
		}
	}
	explanation := rt.Explain(in.Host, syntheticRequest(in))
	if !explanation.ServerMatched {
		return routeTestResult{
			Matched:     false,
			Explanation: "No server block matches this host. Jul would reject the request.",
		}
	}
	srv := &c.Servers[explanation.ServerIndex]

	candidates := make([]routeTestCandidate, 0, len(explanation.Candidates))
	for _, candidate := range explanation.Candidates {
		candidates = append(candidates, routeTestCandidate{
			MatchType:    candidate.MatchType,
			Match:        candidate.Path,
			Tier:         candidate.Tier,
			MatchOrdinal: matchOrdinalOf(srv, candidate.LocationIndex),
			Selected:     candidate.Selected,
			Rejection:    candidate.Rejection,
		})
	}

	selected, ok := explanation.SelectedCandidate()
	if !ok {
		return routeTestResult{
			Matched:     false,
			Listen:      srv.Listen,
			ServerNames: srv.ServerNames,
			Candidates:  candidates,
			Explanation: noMatchExplanation(candidates),
		}
	}
	loc := &srv.Locations[selected.LocationIndex]
	lp := projectLocation(c, srv, loc)
	res := routeTestResult{
		Matched:      true,
		Listen:       srv.Listen,
		ServerNames:  srv.ServerNames,
		Match:        lp.Match,
		MatchType:    lp.Type,
		Action:       lp.Action,
		Target:       lp.Target,
		Upstream:     lp.Upstream,
		Auth:         lp.Auth,
		Cache:        lp.Cache,
		Compression:  lp.Compression,
		RateLimit:    lp.RateLimit,
		Secure:       lp.Secure,
		Warnings:     lp.Warnings,
		MatchOrdinal: matchOrdinalOf(srv, selected.LocationIndex),
		Candidates:   candidates,
	}
	res.Explanation = explainMatch(srv, &lp) + rejectedCandidateNote(candidates)
	return res
}

// matchOrdinalOf reports a location's index among the locations of its server
// block that share its match type and path.
func matchOrdinalOf(srv *config.ServerConfig, locationIndex int) int {
	target := srv.Locations[locationIndex].Match
	ordinal := 0
	for i := 0; i < locationIndex; i++ {
		if srv.Locations[i].Match.Type == target.Type && srv.Locations[i].Match.Path == target.Path {
			ordinal++
		}
	}
	return ordinal
}

// noMatchExplanation says why the request ends in the router's 404. There is no
// 405 and no Allow header anywhere, by decision (ADR 0018 §7), so a method that
// matched no route reads as "no route", and the candidate list is what tells the
// operator that the path did in fact match something.
func noMatchExplanation(candidates []routeTestCandidate) string {
	if len(candidates) == 0 {
		return "A server block matched the host, but no location matched the path. Jul would return 404."
	}
	return "A server block matched the host and " + countOf(len(candidates), "location") +
		" matched the path, but every one was rejected by a predicate" + rejectionList(candidates) +
		". Jul would return 404; there is no 405 and no Allow header."
}

// rejectedCandidateNote appends the rejected candidates to a successful match,
// so an operator can see which more specific route was passed over and why.
func rejectedCandidateNote(candidates []routeTestCandidate) string {
	rejected := 0
	for _, candidate := range candidates {
		if !candidate.Selected {
			rejected++
		}
	}
	if rejected == 0 {
		return ""
	}
	return " " + countOf(rejected, "earlier candidate") + " was passed over" + rejectionList(candidates) + "."
}

func rejectionList(candidates []routeTestCandidate) string {
	var parts []string
	for _, candidate := range candidates {
		if candidate.Selected || candidate.Rejection == "" {
			continue
		}
		parts = append(parts, quote(candidate.MatchType+" "+candidate.Match)+" on "+candidate.Rejection)
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, "; ")
}

func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// projectLocation builds a single LocationProjection for one location, reusing
// the same enrichment as projectRoutes so the test view and the route list stay
// consistent.
func projectLocation(c *config.Config, srv *config.ServerConfig, loc *config.LocationConfig) LocationProjection {
	lp := LocationProjection{
		Match:       loc.Match.Path,
		Type:        loc.Match.Type,
		Auth:        loc.Auth != nil,
		Cache:       loc.Cache,
		RateLimit:   loc.RateLimit != nil && loc.RateLimit.Enabled,
		Compression: c.Compression.IsEnabled(),
		Secure:      srv.TLS != nil && srv.TLS.Enabled,
	}
	switch {
	case loc.GRPCTranscode != nil:
		lp.Action = "grpc_transcode"
		lp.Target = loc.ProxyPass
	case loc.GRPC:
		lp.Action = "grpc"
		lp.Target = loc.ProxyPass
	case loc.ProxyPass != "":
		lp.Action = "proxy"
		lp.Target = loc.ProxyPass
	case loc.FastCGIPass != "":
		lp.Action = "fastcgi"
		lp.Target = loc.FastCGIPass
	case loc.UWSGIPass != "":
		lp.Action = "uwsgi"
		lp.Target = loc.UWSGIPass
	case loc.Redirect != "":
		lp.Action = "redirect"
		lp.Target = loc.Redirect
	case loc.Deny:
		lp.Action = "deny"
	case loc.Root != "":
		lp.Action = "static"
		lp.Target = loc.Root
	case loc.Return != 0:
		lp.Action = "return"
		lp.Target = strconv.Itoa(loc.Return)
	case loc.Plugin != "":
		lp.Action = "plugin"
		lp.Target = loc.Plugin
	default:
		lp.Action = "unknown"
	}
	lp.Upstream = upstreamRef(lp.Target, lp.Action)
	lp.Warnings = locationWarnings(c, srv, loc, &lp)
	return lp
}

// explainMatch produces a short human sentence describing what Jul will do.
func explainMatch(srv *config.ServerConfig, lp *LocationProjection) string {
	var b strings.Builder
	b.WriteString("Jul matches this request to the ")
	if lp.Type != "" {
		b.WriteString(lp.Type + " ")
	}
	b.WriteString("location " + quote(lp.Match) + " on " + quote(srv.Listen) + ". ")
	switch lp.Action {
	case "proxy", "grpc":
		if lp.Upstream != "" {
			b.WriteString("It proxies to the upstream pool " + quote(lp.Upstream) + ".")
		} else {
			b.WriteString("It proxies to " + quote(lp.Target) + ".")
		}
	case "static":
		b.WriteString("It serves static files from " + quote(lp.Target) + ".")
	case "redirect":
		b.WriteString("It redirects to " + quote(lp.Target) + ".")
	case "deny":
		b.WriteString("It denies the request with 403.")
	case "return":
		b.WriteString("It returns HTTP status " + lp.Target + ".")
	case "fastcgi":
		b.WriteString("It forwards to the FastCGI app at " + quote(lp.Target) + ".")
	case "uwsgi":
		b.WriteString("It forwards to the uWSGI app at " + quote(lp.Target) + ".")
	case "grpc_transcode":
		b.WriteString("It transcodes REST/JSON to gRPC for the backend.")
	default:
		b.WriteString("Its action is not recognized; check the configuration.")
	}
	var mods []string
	if lp.Auth {
		mods = append(mods, "auth")
	}
	if lp.Cache {
		mods = append(mods, "cache")
	}
	if lp.Compression {
		mods = append(mods, "compression")
	}
	if lp.RateLimit {
		mods = append(mods, "rate limiting")
	}
	if len(mods) > 0 {
		b.WriteString(" Edge rules applied: " + strings.Join(mods, ", ") + ".")
	}
	return b.String()
}

// quote wraps a value in double quotes for human-readable explanations,
// rendering an empty value as a dash so the sentence still reads cleanly.
func quote(s string) string {
	if s == "" {
		return "—"
	}
	return "\"" + s + "\""
}
