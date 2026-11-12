// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"errors"
	"net/http"

	"jul/internal/inspection"
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
			// The outermost inspecting layer owns the request lease and
			// holds it through the whole downstream handler and its
			// synchronous response callbacks (ADR 0032 §4).
			lease, r, owner, ok := p.requestLease(w, r)
			if !ok {
				return
			}
			if owner {
				defer lease.Close()
			}
			action, inv, err := p.invoke(r.Context(), w, r)
			if err != nil {
				undoPluginHeaders(w, r, inv)
				writePluginError(w, r, err)
				return
			}
			if action == 1 { // Continue
				r = continueWithJournal(r, inv)
				inv.releaseResponse()
				if inv.sub >= 0 {
					// The subscription reads the state after the action;
					// it stays charged until the downstream chain returns.
					defer inv.releaseState()
					r = subscribe(r, p, uint32(inv.sub), inv.state)
				} else {
					inv.releaseState()
				}
				next.ServeHTTP(w, r)
				return
			}
			inv.flush() // Stop: the guest produced the response.
			inv.releaseAll()
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

// InspectionFacts returns the named plugin's static inspection-budget facts
// (ADR 0032 §7). SubscribesResponse is a plugin property; the caller decides
// whether the plugin sits in a route's middleware chain.
func (s *Set) InspectionFacts(name string) (inspection.PluginFacts, bool) {
	if s == nil {
		return inspection.PluginFacts{}, false
	}
	p := s.plugins[name]
	if p == nil {
		return inspection.PluginFacts{}, false
	}
	return p.facts, true
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
		lease, r, owner, ok := p.requestLease(w, r)
		if !ok {
			return
		}
		if owner {
			defer lease.Close()
		}
		_, inv, err := p.invoke(r.Context(), w, r)
		if err != nil {
			undoPluginHeaders(w, r, inv)
			writePluginError(w, r, err)
			return
		}
		inv.flush()
		inv.releaseAll()
	})
}

// writePluginError answers a failed plugin call. Inspection outcomes keep
// their ADR 0032 responses: overload is 503 with Retry-After (gRPC
// UNAVAILABLE), an expired body-arrival deadline is 408 (gRPC
// DEADLINE_EXCEEDED). Exhausted instances are 503 with Retry-After (#506);
// everything else, including an unsupported required read deadline, is 500.
func writePluginError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case inspection.IsOverload(err):
		inspection.WriteOverloaded(w, r)
		return
	case errors.Is(err, inspection.ErrBodyTimeout):
		inspection.WriteBodyTimeout(w, r)
		return
	}
	if errors.Is(err, errInstanceLimit) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "plugin busy", http.StatusServiceUnavailable)
		return
	}
	http.Error(w, "plugin error", http.StatusInternalServerError)
}
