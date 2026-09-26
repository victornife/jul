// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"net/http"

	"jul/internal/middleware"
)

// Set is the compiled, instantiated plugins for one configuration generation. It
// is an io.Closer: the server registers it with the generational handler-closer
// machinery so the previous generation's runtimes are torn down after the new
// generation is live.
type Set struct {
	plugins map[string]*plugin
}

// Close tears down every plugin runtime in the set.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	for _, p := range s.plugins {
		p.close()
	}
	return nil
}

// Identities returns the content identity of every plugin's compiled module.
func (s *Set) Identities() map[string]ModuleIdentity {
	if s == nil {
		return nil
	}
	out := make(map[string]ModuleIdentity, len(s.plugins))
	for name, p := range s.plugins {
		out[name] = p.identity
	}
	return out
}

// Has reports whether the set contains a plugin by name.
func (s *Set) Has(name string) bool {
	_, ok := s.plugins[name]
	return ok
}

// Middleware returns the named plugin as middleware that wraps the next handler.
// The guest may mutate the request and response and either pass through
// (Continue) or short-circuit with its own response (Stop). It returns nil if no
// such plugin exists (validation guarantees it does).
func (s *Set) Middleware(name string) middleware.Middleware {
	p := s.plugins[name]
	if p == nil {
		return nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			action, inv, err := p.invoke(r.Context(), w, r)
			if err != nil {
				http.Error(w, "plugin error", http.StatusInternalServerError)
				return
			}
			if action == 1 { // Continue
				if inv.sub >= 0 {
					r = subscribe(r, p, uint32(inv.sub), inv.state)
				}
				next.ServeHTTP(w, r)
				return
			}
			inv.flush() // Stop: the guest produced the response.
		})
	}
}

// ResponsePoint returns the jul-abi/v2 response-point middleware when any of
// the named middleware plugins can subscribe to responses, else nil. The
// composition root installs it just inside the location's WAF (ADR 0020 §6).
func (s *Set) ResponsePoint(names ...string) middleware.Middleware {
	for _, name := range names {
		if p := s.plugins[name]; p != nil && p.hasResponse && !p.isHandler {
			return responsePoint
		}
	}
	return nil
}

// ABIs returns the configured ABI of every plugin in the set.
func (s *Set) ABIs() map[string]string {
	if s == nil {
		return nil
	}
	out := make(map[string]string, len(s.plugins))
	for name, p := range s.plugins {
		out[name] = p.abi
	}
	return out
}

// Handler returns the named plugin as a terminal handler (a location action).
// The guest always produces the full response. It returns nil if no such plugin
// exists (validation guarantees it does).
func (s *Set) Handler(name string) http.Handler {
	p := s.plugins[name]
	if p == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, inv, err := p.invoke(r.Context(), w, r)
		if err != nil {
			http.Error(w, "plugin error", http.StatusInternalServerError)
			return
		}
		inv.flush()
	})
}
