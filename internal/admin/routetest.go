// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"jul/internal/config"
)

// routeTestRequest is the input to POST /api/routes/test: a synthetic request
// the operator wants to see resolved against the running configuration.
type routeTestRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Host    string            `json:"host"`
	Headers map[string]string `json:"headers,omitempty"`
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
	writeJSON(w, http.StatusOK, testRoute(cfg, normalizeTestHost(in.Host), path))
}

// testRoute performs the dry-run resolution: pick the best-scoring server block
// for the host, then the best-matching location within it.
func testRoute(c *config.Config, host, path string) routeTestResult {
	srv := bestServer(c, host)
	if srv == nil {
		return routeTestResult{
			Matched:     false,
			Explanation: "No server block matches this host. Jul would reject the request.",
		}
	}
	loc := bestLocation(srv, path)
	if loc == nil {
		return routeTestResult{
			Matched:     false,
			Listen:      srv.Listen,
			ServerNames: srv.ServerNames,
			Explanation: "A server block matched the host, but no location matched the path. Jul would return 404.",
		}
	}
	lp := projectLocation(c, srv, loc)
	res := routeTestResult{
		Matched:     true,
		Listen:      srv.Listen,
		ServerNames: srv.ServerNames,
		Match:       lp.Match,
		MatchType:   lp.Type,
		Action:      lp.Action,
		Target:      lp.Target,
		Upstream:    lp.Upstream,
		Auth:        lp.Auth,
		Cache:       lp.Cache,
		Compression: lp.Compression,
		RateLimit:   lp.RateLimit,
		Secure:      lp.Secure,
		Warnings:    lp.Warnings,
	}
	res.Explanation = explainMatch(srv, &lp)
	return res
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
		Compression: c.Compression.Enabled,
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

// bestServer selects the server block with the highest host score. Blocks with
// no server_names act as a default (score 1) so a request still resolves when no
// named vhost matches, mirroring the router's default-server behavior.
func bestServer(c *config.Config, host string) *config.ServerConfig {
	var best *config.ServerConfig
	bestScore := -1
	for i := range c.Servers {
		srv := &c.Servers[i]
		score := 0
		if len(srv.ServerNames) == 0 {
			score = 1 // default server
		} else {
			score = testHostScore(srv.ServerNames, host)
			if score > 0 {
				score += 10 // a named match always beats a default
			}
		}
		if score > bestScore {
			best = srv
			bestScore = score
		}
	}
	if bestScore <= 0 {
		return nil
	}
	return best
}

// bestLocation mirrors router.matchLocation: exact, then longest non-root
// prefix, then regex in config order, then the "/" prefix fallback.
func bestLocation(srv *config.ServerConfig, path string) *config.LocationConfig {
	for i := range srv.Locations {
		loc := &srv.Locations[i]
		if loc.Match.Type == "exact" && loc.Match.Path == path {
			return loc
		}
	}
	var best *config.LocationConfig
	bestLen := -1
	for i := range srv.Locations {
		loc := &srv.Locations[i]
		if loc.Match.Type != "prefix" || loc.Match.Path == "/" {
			continue
		}
		if strings.HasPrefix(path, loc.Match.Path) && len(loc.Match.Path) > bestLen {
			best = loc
			bestLen = len(loc.Match.Path)
		}
	}
	if best != nil {
		return best
	}
	for i := range srv.Locations {
		loc := &srv.Locations[i]
		if loc.Match.Type == "regex" {
			if re, err := regexp.Compile(loc.Match.Path); err == nil && re.MatchString(path) {
				return loc
			}
		}
	}
	for i := range srv.Locations {
		loc := &srv.Locations[i]
		if loc.Match.Type == "prefix" && loc.Match.Path == "/" {
			return loc
		}
	}
	return nil
}

// testHostScore mirrors router.hostScore: 3 exact, 2 leading-wildcard, 0 none.
func testHostScore(names []string, host string) int {
	best := 0
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		switch {
		case name == host:
			return 3
		case strings.HasPrefix(name, "*."):
			suffix := name[1:]
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				if best < 2 {
					best = 2
				}
			}
		}
	}
	return best
}

// normalizeTestHost lowercases the host and strips any port suffix, mirroring
// router.normalizeHost.
func normalizeTestHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if strings.HasPrefix(host, "[") {
		if i := strings.LastIndex(host, "]"); i >= 0 {
			return host[:i+1]
		}
		return host
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
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
