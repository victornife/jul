// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// handleV1Routes serves GET /api/v1/routes: every route in declaration order,
// each carrying its durable id when it has one and the revision-scoped selector
// always (ADR 0019 §24).
//
// The projection is the existing projectRoutes, re-encoded. ADR 0019 §24 is
// explicit that where a v1 shape differs from the Console's, the difference is
// a *response encoder* and never a second implementation of the operation — so
// the classification of an action, the route id and the match ordinal are all
// computed once, here as everywhere else.
func (s *Server) handleV1Routes(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	writeAPIJSON(w, http.StatusOK, adminapi.RoutesResponse{
		APIVersion:  adminapi.APIVersion,
		BaseVersion: state.Version,
		Routes:      v1Routes(state.Config),
	})
}

// handleV1Route serves GET /api/v1/routes/{route_id}.
//
// It resolves **from the id alone**, which is what makes the id durable: a
// route without one is collection-only and is not addressable here under any
// encoding (ADR 0019 §4.13). That is why an unknown id is a plain not_found
// rather than an invitation to try a selector — the selector belongs on the
// collection, scoped to a base_version.
func (s *Server) handleV1Route(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	id := r.PathValue("route_id")
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	for _, route := range v1Routes(state.Config) {
		if route.RouteID != "" && route.RouteID == id {
			writeAPIJSON(w, http.StatusOK, adminapi.RouteResponse{
				APIVersion:  adminapi.APIVersion,
				BaseVersion: state.Version,
				Route:       route,
			})
			return
		}
	}
	writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotFound,
		"No route carries that durable id. A route without a route_id is addressable only through the "+
			"collection and its revision-scoped selector.").
		WithDetails(adminapi.Details{Kind: "route", ID: id}))
}

// v1Routes flattens the server/location projection into the external
// collection, preserving declaration order: server blocks in configuration
// order, locations within each in configuration order.
func v1Routes(c *config.Config) []adminapi.Route {
	out := []adminapi.Route{}
	for _, srv := range projectRoutes(c) {
		for _, loc := range srv.Locations {
			out = append(out, adminapi.Route{
				RouteID: loc.RouteID,
				Selector: adminapi.RouteSelector{
					Listen:       srv.Listen,
					ServerNames:  srv.ServerNames,
					MatchType:    loc.Type,
					Path:         loc.Match,
					MatchOrdinal: loc.MatchOrdinal,
				},
				Action:            loc.Action,
				Target:            loc.Target,
				Upstream:          loc.Upstream,
				Methods:           loc.Methods,
				TLS:               loc.Secure,
				Auth:              loc.Auth,
				RequireClientCert: loc.RequireClientCert,
				Cache:             loc.Cache,
				RateLimit:         loc.RateLimit,
				WAF:               loc.WAF != nil,
			})
		}
	}
	return out
}

// handleV1Upstreams serves GET /api/v1/upstreams in configuration order.
func (s *Server) handleV1Upstreams(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	writeAPIJSON(w, http.StatusOK, adminapi.UpstreamsResponse{
		APIVersion:  adminapi.APIVersion,
		BaseVersion: state.Version,
		Upstreams:   s.v1Upstreams(state.Config),
	})
}

// handleV1Upstream serves GET /api/v1/upstreams/{name}, addressing a pool by
// its natural key.
func (s *Server) handleV1Upstream(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	name := r.PathValue("name")
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	for _, up := range s.v1Upstreams(state.Config) {
		if up.Name == name {
			writeAPIJSON(w, http.StatusOK, adminapi.UpstreamResponse{
				APIVersion:  adminapi.APIVersion,
				BaseVersion: state.Version,
				Upstream:    up,
			})
			return
		}
	}
	writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotFound, "No upstream pool with that name.").
		WithDetails(adminapi.Details{Kind: "upstream_pool", ID: name}))
}

// v1Upstreams builds the pool collection from the configuration, in declaration
// order, and enriches each backend with the runtime state the live pools report.
//
// The order comes from the configuration rather than from the runtime snapshot
// deliberately: §24a requires declaration order, and the runtime's own ordering
// is not part of any contract this package controls. Runtime state is joined by
// name, so a pool the runtime has not reported on is still listed — with its
// configured backends and an empty state — rather than disappearing from a
// collection that claims to describe the configuration.
func (s *Server) v1Upstreams(c *config.Config) []adminapi.Upstream {
	live := map[string]UpstreamStatus{}
	if s.deps.Upstreams != nil {
		for _, u := range s.deps.Upstreams() {
			live[u.Name] = u
		}
	}

	out := []adminapi.Upstream{}
	if c == nil {
		return out
	}
	for i := range c.Upstreams {
		up := &c.Upstreams[i]
		entry := adminapi.Upstream{
			Name:     up.Name,
			Strategy: up.Strategy,
			Backends: []adminapi.UpstreamBackend{},
		}
		if l, ok := live[up.Name]; ok {
			entry.Strategy = orDefault(l.Strategy, entry.Strategy)
		}
		byAddress := map[string]BackendStatus{}
		for _, b := range live[up.Name].Backends {
			byAddress[b.Address] = b
		}
		for j := range up.Servers {
			srv := &up.Servers[j]
			b := adminapi.UpstreamBackend{Address: srv.Address, Weight: srv.Weight}
			if l, ok := byAddress[srv.Address]; ok {
				b.State = l.State
				b.InFlight = l.Inflight
				if l.Weight != 0 {
					b.Weight = l.Weight
				}
			}
			entry.Backends = append(entry.Backends, b)
		}
		out = append(out, entry)
	}
	return out
}

// v1ConfigState reads the configuration and the canonical version it was read
// at, together.
//
// They come from one call because a selector is only meaningful against the
// revision it was read with: pairing a configuration from one read with a
// version from another would hand a client a base_version that does not
// describe the routes it is looking at.
func (s *Server) v1ConfigState() (CurrentWriteState, *adminapi.Error) {
	state, err := s.currentWriteState(false)
	if err != nil {
		return CurrentWriteState{}, adminapi.Errorf(adminapi.CodeStorageUnavailable,
			"The current configuration could not be read.")
	}
	return state, nil
}
