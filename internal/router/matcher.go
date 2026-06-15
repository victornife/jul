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

// matchLocation resolves a request path to a location within a server, using
// the order: exact -> longest non-root prefix -> regex (config order) ->
// "/" fallback.
func (s *serverRoute) matchLocation(path string) *locationRoute {
	// 1. Exact match.
	for _, loc := range s.locations {
		if loc.matchType == "exact" && loc.path == path {
			return loc
		}
	}

	// 2. Longest matching prefix, excluding the "/" catch-all.
	var best *locationRoute
	bestLen := -1
	for _, loc := range s.locations {
		if loc.matchType != "prefix" || loc.path == "/" {
			continue
		}
		if strings.HasPrefix(path, loc.path) && len(loc.path) > bestLen {
			best = loc
			bestLen = len(loc.path)
		}
	}
	if best != nil {
		return best
	}

	// 3. Regex matches in configuration order.
	for _, loc := range s.locations {
		if loc.matchType == "regex" && loc.re.MatchString(path) {
			return loc
		}
	}

	// 4. "/" prefix fallback.
	return s.fallback
}
