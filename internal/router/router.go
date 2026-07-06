// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"

	"jul/internal/config"
	"jul/internal/middleware"
)

// Builder constructs the http.Handler for a location's action. The router seeds
// built-in Builders for the config-only actions (deny, redirect, return); the
// caller (the composition root in main) supplies the content actions (static,
// proxy, fastcgi) and may register or override others. This single registry is
// the seam future action types — plugin, graphql, AI gateway, webtransport —
// plug into: add a LocationConfig field, an actionOf case, and a Builder here,
// with no change to the router's dispatch core. Cross-cutting concerns (auth,
// rate limiting, guardrails) stay LocationModifiers that compose around the
// action; they are never actions themselves.
type Builder func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error)

type locationRoute struct {
	matchType string
	path      string
	re        *regexp.Regexp
	rewrites  []compiledRewrite
	handler   http.Handler
}

type serverRoute struct {
	names         []string
	locations     []*locationRoute
	fallback      *locationRoute // the "/" prefix location, if present
	redirectHTTPS int            // status code (301/308) to redirect HTTP->HTTPS, or 0
}

func (s *serverRoute) score(host string) int { return hostScore(s.names, host) }

type addrRouter struct {
	servers []*serverRoute
	def     *serverRoute // default server for this address (first declared)
}

// match selects the server block for a host, falling back to the default
// server when no server_name matches.
func (ar *addrRouter) match(host string) *serverRoute {
	h := normalizeHost(host)
	best := ar.def
	bestScore := 0
	for _, s := range ar.servers {
		if sc := s.score(h); sc > bestScore {
			bestScore = sc
			best = s
		}
	}
	return best
}

// Router holds the compiled routing tables, keyed by listen address.
type Router struct {
	byAddr map[string]*addrRouter
	log    *slog.Logger
}

// LocationModifier returns optional middleware to wrap a location's handler,
// derived from the server and location config. A nil result adds no middleware.
// It is the per-location extension point for cross-cutting concerns such as
// rate limiting (and later authentication), applied as the outermost wrapper.
type LocationModifier func(config.ServerConfig, config.LocationConfig) middleware.Middleware

// New compiles a Router from cfg. The router provides built-in builders for the
// config-only actions (deny, redirect, return); builders supplies the content
// actions (static, proxy, fastcgi) and may register or override others. fallback
// handles an action with no registered builder (it lets earlier build phases run
// before every handler exists). locModifier, when non-nil, may wrap each
// location's handler with additional middleware.
func New(cfg *config.Config, builders map[string]Builder, fallback Builder, locModifier LocationModifier, log *slog.Logger) (*Router, error) {
	if fallback == nil {
		fallback = notImplementedBuilder
	}
	// Seed the registry with the router's built-in action builders, then overlay
	// the caller's builders so every action dispatches through one uniform lookup
	// in buildServerRoute. Caller-supplied builders override a built-in of the
	// same action name.
	reg := builtinBuilders()
	for action, b := range builders {
		reg[action] = b
	}
	r := &Router{byAddr: map[string]*addrRouter{}, log: log}

	for _, srv := range cfg.Servers {
		if srv.Listen == "" {
			continue
		}
		sr, err := buildServerRoute(srv, reg, fallback, locModifier)
		if err != nil {
			return nil, err
		}
		ar := r.byAddr[srv.Listen]
		if ar == nil {
			ar = &addrRouter{}
			r.byAddr[srv.Listen] = ar
		}
		ar.servers = append(ar.servers, sr)
		if ar.def == nil {
			ar.def = sr
		}
	}
	return r, nil
}

func buildServerRoute(srv config.ServerConfig, reg map[string]Builder, fallback Builder, locModifier LocationModifier) (*serverRoute, error) {
	sr := &serverRoute{names: srv.ServerNames, redirectHTTPS: srv.RedirectHTTPS}

	bodyLimit := srv.ClientMaxBodySize.Bytes()
	for _, loc := range srv.Locations {
		lr := &locationRoute{matchType: loc.Match.Type, path: loc.Match.Path}

		if loc.Match.Type == "regex" {
			re, err := regexp.Compile(loc.Match.Path)
			if err != nil {
				return nil, fmt.Errorf("location regex %q: %w", loc.Match.Path, err)
			}
			lr.re = re
		}

		rewrites, err := compileRewrites(loc.Rewrites)
		if err != nil {
			return nil, err
		}
		lr.rewrites = rewrites

		action, err := actionOf(loc)
		if err != nil {
			return nil, err
		}

		// Every action — built-in, content, or future plugin — is constructed
		// through the one registry lookup, falling back when no builder is set.
		b := reg[action]
		if b == nil {
			b = fallback
		}
		h, err := b(srv, loc)
		if err != nil {
			return nil, err
		}

		// Per-location body limit (location override wins over server default).
		limit := bodyLimit
		if loc.ClientMaxBodySize > 0 {
			limit = loc.ClientMaxBodySize.Bytes()
		}
		h = middleware.BodyLimit(limit)(h)

		// Per-location modifier (e.g. rate limiting) wraps outside BodyLimit so it
		// runs before the request body is read.
		if locModifier != nil {
			if mw := locModifier(srv, loc); mw != nil {
				h = mw(h)
			}
		}

		lr.handler = h
		sr.locations = append(sr.locations, lr)
		if loc.Match.Type == "prefix" && loc.Match.Path == "/" {
			sr.fallback = lr
		}
	}
	return sr, nil
}

func compileRewrites(rules []config.RewriteConfig) ([]compiledRewrite, error) {
	out := make([]compiledRewrite, 0, len(rules))
	for _, rule := range rules {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, fmt.Errorf("rewrite pattern %q: %w", rule.Pattern, err)
		}
		out = append(out, compiledRewrite{re: re, replacement: rule.Replacement, flag: rule.Flag})
	}
	return out, nil
}

// For returns the http.Handler serving a given listen address.
func (r *Router) For(addr string) http.Handler {
	ar := r.byAddr[addr]
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if ar == nil {
			http.NotFound(w, req)
			return
		}
		srv := ar.match(req.Host)
		if srv == nil {
			http.NotFound(w, req)
			return
		}
		if srv.redirectHTTPS != 0 && req.TLS == nil {
			redirectToHTTPS(w, req, srv.redirectHTTPS)
			return
		}
		loc := srv.matchLocation(req.URL.Path)
		if loc == nil {
			http.NotFound(w, req)
			return
		}
		if applyRewrites(loc.rewrites, w, req) {
			return
		}
		loc.handler.ServeHTTP(w, req)
	})
}

// notImplementedBuilder is the default fallback: it returns a handler that
// reports the action is not yet wired up. Replaced as later phases register
// real builders.
func notImplementedBuilder(_ config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
	action, _ := actionOf(loc)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintf(w, "501 action %q not implemented yet (path %s)\n", action, loc.Match.Path)
	}), nil
}
