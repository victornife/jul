// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"sort"
	"strings"

	"jul/internal/config"
)

// SearchResult is one ranked discovery hit for the Console v2 search surface
// (Milestone 4.7). It is intentionally compact: titles/details are abstract
// (paths, action kinds, pool names, counts) and never carry secrets such as
// tokens, certificate material, or full upstream credentials.
type SearchResult struct {
	Kind     string   `json:"kind"`  // "route" | "app"
	Title    string   `json:"title"` // primary label
	Detail   string   `json:"detail"`
	Score    int      `json:"score"`
	Target   string   `json:"target,omitempty"`   // route → app/target
	Upstream string   `json:"upstream,omitempty"` // route → upstream pool
	Routes   []string `json:"routes,omitempty"`   // app → routes using it
	Badges   []string `json:"badges,omitempty"`
}

// handleSearch serves ranked discovery results across routes and apps at
// GET /api/search?q=<query>&type=<routes|apps|all>. It complements the
// client-side filter by giving scripts and the UI a single ranked surface that
// reflects route↔app relationships. Results are derived from the parsed config
// via LoadConfig; when that hook is unwired it returns an empty list so the
// console renders a clean empty state.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	out := []SearchResult{}
	if s.deps.LoadConfig == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	typ := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	if typ == "" {
		typ = "all"
	}

	if typ == "all" || typ == "routes" {
		out = append(out, searchRoutes(cfg, q)...)
	}
	if typ == "all" || typ == "apps" {
		out = append(out, searchApps(cfg, q)...)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 50 {
		out = out[:50]
	}
	writeJSON(w, http.StatusOK, out)
}

// scoreMatch ranks a candidate string against a lower-cased query. An empty
// query matches everything with a low baseline score so discovery lists still
// populate. Exact > prefix > substring.
func scoreMatch(haystack, q string) int {
	if q == "" {
		return 1
	}
	h := strings.ToLower(haystack)
	switch {
	case h == q:
		return 100
	case strings.HasPrefix(h, q):
		return 70
	case strings.Contains(h, q):
		return 40
	default:
		return 0
	}
}

func bestScore(q string, fields ...string) int {
	best := 0
	for _, f := range fields {
		if sc := scoreMatch(f, q); sc > best {
			best = sc
		}
	}
	return best
}

func searchRoutes(c *config.Config, q string) []SearchResult {
	routes := projectRoutes(c)
	var out []SearchResult
	for _, rp := range routes {
		hosts := strings.Join(rp.ServerNames, ", ")
		for _, loc := range rp.Locations {
			score := bestScore(q, loc.Match, rp.Listen, hosts, loc.Action, loc.Target, loc.Upstream)
			if q != "" && score == 0 {
				continue
			}
			badges := []string{loc.Action}
			if loc.Upstream != "" {
				badges = append(badges, "→ "+loc.Upstream)
			}
			if loc.Secure {
				badges = append(badges, "TLS")
			}
			if len(loc.Warnings) > 0 {
				badges = append(badges, "warnings")
			}
			detail := rp.Listen
			if hosts != "" {
				detail += " · " + hosts
			}
			if loc.Target != "" {
				detail += " · " + sanitizeTarget(loc.Target)
			}
			out = append(out, SearchResult{
				Kind:     "route",
				Title:    loc.Match + " (" + loc.Type + ")",
				Detail:   detail,
				Score:    score + 5, // routes rank slightly above apps on ties
				Target:   sanitizeTarget(loc.Target),
				Upstream: loc.Upstream,
				Badges:   badges,
			})
		}
	}
	return out
}

// sanitizeTarget removes userinfo (user:password@) from a URL before exposing
// it in search results so credentials are not leaked through the console.
func sanitizeTarget(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	i := strings.Index(s, "://")
	scheme := s[:i+3]
	rest := s[i+3:]
	at := strings.Index(rest, "@")
	if at < 0 {
		return s
	}
	return scheme + "***@" + rest[at+1:]
}

func searchApps(c *config.Config, q string) []SearchResult {
	apps := projectApps(c, nil)
	var out []SearchResult
	for _, a := range apps {
		fields := []string{a.Name, a.Strategy}
		for _, b := range a.Backends {
			fields = append(fields, b.Address)
		}
		score := bestScore(q, fields...)
		if q != "" && score == 0 {
			continue
		}
		badges := []string{pluralCount(len(a.Backends), "backend")}
		if a.HealthCheck {
			badges = append(badges, "health check")
		}
		if len(a.RoutesUsing) == 0 {
			badges = append(badges, "unused")
		} else {
			badges = append(badges, pluralCount(len(a.RoutesUsing), "route"))
		}
		detail := a.Strategy
		if len(a.RoutesUsing) > 0 {
			detail += " · used by " + strings.Join(a.RoutesUsing, ", ")
		} else {
			detail += " · not referenced by any route"
		}
		out = append(out, SearchResult{
			Kind:   "app",
			Title:  a.Name,
			Detail: detail,
			Score:  score,
			Routes: a.RoutesUsing,
			Badges: badges,
		})
	}
	return out
}

func pluralCount(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return itoa(n) + " " + unit + "s"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
