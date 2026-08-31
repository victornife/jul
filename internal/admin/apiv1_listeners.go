// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"sort"
	"strings"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// handleV1Listeners serves GET /api/v1/listeners in declaration order.
//
// The internal listener route sorts by address for the Console's table. That
// sort is not carried over: §24a makes declaration order the published
// ordering, and the two surfaces are allowed to differ in encoding precisely so
// the Console can keep its table without the contract inheriting a sort.
func (s *Server) handleV1Listeners(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	writeAPIJSON(w, http.StatusOK, adminapi.ListenersResponse{
		APIVersion:  adminapi.APIVersion,
		BaseVersion: state.Version,
		Listeners:   v1Listeners(state.Config),
	})
}

func v1Listeners(c *config.Config) []adminapi.Listener {
	out := []adminapi.Listener{}
	if c == nil {
		return out
	}
	index := map[string]int{}
	for i := range c.Servers {
		srv := &c.Servers[i]
		addr := strings.TrimSpace(srv.Listen)
		if addr == "" {
			continue
		}
		at, ok := index[addr]
		if !ok {
			index[addr] = len(out)
			at = len(out)
			out = append(out, adminapi.Listener{Listen: addr})
		}
		l := &out[at]
		l.ServerBlocks++
		for _, name := range srv.ServerNames {
			if !containsString(l.ServerNames, name) {
				l.ServerNames = append(l.ServerNames, name)
			}
		}
		if srv.TLS != nil && srv.TLS.Enabled {
			l.TLS = true
			if srv.TLS.ClientAuth != nil && srv.TLS.ClientAuth.Active() {
				l.ClientAuth = srv.TLS.ClientAuth.Mode
			}
		}
		if srv.HTTP3 != nil && srv.HTTP3.Enabled {
			l.HTTP3 = true
		}
		if srv.ClientAddress != nil {
			l.ClientAddressConfigured = true
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// handleV1ClientAddress serves GET /api/v1/listeners/{addr}/client_address: the
// effective trusted-proxy policy for one bound address.
//
// The policy is a sub-resource rather than a field of the listener because it
// is the one part of a listener with its own permission — reading it is
// config:read and changing it is config:trust, since a trusted-proxy range
// decides which address a request is attributed to, and therefore what every
// allow-list and rate limit downstream sees.
func (s *Server) handleV1ClientAddress(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	addr := r.PathValue("addr")
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	view, ok := projectListenerClientAddress(state.Config, addr)
	if !ok {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotFound, "No server block listens on that address.").
			WithDetails(adminapi.Details{Kind: "listener", ID: addr}))
		return
	}
	writeAPIJSON(w, http.StatusOK, adminapi.ClientAddressResponse{
		APIVersion:  adminapi.APIVersion,
		BaseVersion: state.Version,
		ClientAddress: adminapi.ClientAddress{
			Listen:            view.Listen,
			ServerBlocks:      view.ServerBlocks,
			Configured:        view.Configured,
			TrustedProxies:    view.TrustedProxies,
			ForwardedHeaders:  view.ForwardedHeaders,
			MaxHops:           view.MaxHops,
			HeadersDisabled:   view.HeadersDisabled,
			TrustsEveryClient: view.TrustsEveryClient,
		},
	})
}

// handleV1Streams serves GET /api/v1/streams in declaration order.
func (s *Server) handleV1Streams(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	writeAPIJSON(w, http.StatusOK, adminapi.StreamsResponse{
		APIVersion:  adminapi.APIVersion,
		BaseVersion: state.Version,
		Compiled:    s.deps.StreamCompiled,
		Streams:     v1Streams(state.Config),
	})
}

func v1Streams(c *config.Config) []adminapi.Stream {
	out := []adminapi.Stream{}
	if c == nil {
		return out
	}
	for i := range c.Streams {
		st := &c.Streams[i]
		out = append(out, adminapi.Stream{
			Listen:           strings.TrimSpace(st.Listen),
			Protocol:         streamProtoOrDefault(st.Protocol),
			Target:           st.ProxyPass,
			SNIRoutes:        v1SNIRoutes(st.SNIRoutes),
			TLSPassthrough:   st.TLSPassthrough,
			ProxyProtocol:    strings.ToLower(strings.TrimSpace(st.ProxyProtocol)),
			ConnectTimeoutMS: st.ConnectTimeout.Std().Milliseconds(),
			IdleTimeoutMS:    st.IdleTimeout.Std().Milliseconds(),
		})
	}
	return out
}

// v1SNIRoutes flattens the SNI map into a sorted list.
//
// Sorting here is the opposite call from the collections, and for the opposite
// reason: a map has no declaration order to preserve, so leaving it as a map
// would publish Go's randomized iteration as if it were an ordering. Sorting by
// server name is the only stable choice, and a stable one matters because a
// client diffing two reads would otherwise see phantom changes.
func v1SNIRoutes(m map[string]string) []adminapi.SNIRoute {
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]adminapi.SNIRoute, 0, len(names))
	for _, name := range names {
		out = append(out, adminapi.SNIRoute{ServerName: name, Target: m[name]})
	}
	return out
}
