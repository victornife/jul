// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

// RouteSelector is ADR 0018's revision-scoped selector: the coordinates that
// name a route within one configuration revision.
//
// It is **not** a durable identity. `listen`, `server_names`, `match_type` and
// `path` are mutable route semantics, so a selector is only meaningful against
// the `base_version` it was read with — which is why every collection that
// carries selectors also carries that version, and why a mutation targeted by
// selector requires it (ADR 0019 §32).
type RouteSelector struct {
	Listen      string   `json:"listen"`
	ServerNames []string `json:"server_names,omitempty"`
	MatchType   string   `json:"match_type"`
	Path        string   `json:"path"`
	// MatchOrdinal disambiguates two locations with the same match type and
	// path within one server block.
	MatchOrdinal int `json:"match_ordinal"`
}

// Route is one entry of the routes collection.
//
// Every entry carries the selector; `route_id` is present only for a route that
// has a durable identity. A route without one is **collection-only**: it is not
// addressable at /api/v1/routes/{route_id} under any encoding, and a mutation
// targets it through the selector plus `base_version` (ADR 0019 §4.13).
//
// The booleans report whether a policy is attached, not its content. A client
// that needs the policy reads the configuration; this collection exists to
// answer "what routes exist, in what precedence order, and which of them have a
// durable id".
type Route struct {
	RouteID  string        `json:"route_id,omitempty"`
	Selector RouteSelector `json:"selector"`

	// Action is one of static, proxy, grpc, grpc_transcode, fastcgi, redirect,
	// deny, return.
	Action string `json:"action"`
	// Target is the action's destination where it has one — a proxy target, a
	// redirect location, a document root. It is a configuration value, not a
	// secret, and this operation requires status:read.
	Target string `json:"target,omitempty"`
	// Upstream names the pool this route proxies to, when it names one rather
	// than an address.
	Upstream string   `json:"upstream,omitempty"`
	Methods  []string `json:"methods,omitempty"`

	TLS               bool `json:"tls"`
	Auth              bool `json:"auth"`
	RequireClientCert bool `json:"require_client_cert"`
	Cache             bool `json:"cache"`
	RateLimit         bool `json:"rate_limit"`
	WAF               bool `json:"waf"`
}

// RoutesResponse is GET /api/v1/routes.
//
// The collection is in **declaration order** — server blocks in configuration
// order, locations within each in configuration order. Never map-iteration
// order, and never sorted by an identifier: ADR 0018 makes declaration order
// part of the routing contract, so a collection that reordered it would
// misrepresent precedence (ADR 0019 §24a).
type RoutesResponse struct {
	APIVersion string `json:"api_version"`
	// BaseVersion is the canonical revision these entries were read from.
	// Selectors are scoped to it, and a mutation targeted by selector must
	// carry it.
	BaseVersion string  `json:"base_version"`
	Routes      []Route `json:"routes"`
}

// RouteResponse is GET /api/v1/routes/{route_id}.
type RouteResponse struct {
	APIVersion  string `json:"api_version"`
	BaseVersion string `json:"base_version"`
	Route       Route  `json:"route"`
}

// UpstreamBackend is one backend of a pool, with its live state.
type UpstreamBackend struct {
	Address string `json:"address"`
	Weight  int    `json:"weight"`
	// State is the runtime eligibility: available, circuit_open,
	// circuit_half_open, health_unhealthy or at_capacity. It is empty when the
	// runtime has not reported on this backend.
	State string `json:"state,omitempty"`
	// InFlight is the current request count for this backend.
	InFlight int64 `json:"in_flight"`
}

// Upstream is one configured pool with its live backend state.
type Upstream struct {
	Name     string            `json:"name"`
	Strategy string            `json:"strategy,omitempty"`
	Backends []UpstreamBackend `json:"backends"`
}

// UpstreamsResponse is GET /api/v1/upstreams, in configuration order.
type UpstreamsResponse struct {
	APIVersion  string     `json:"api_version"`
	BaseVersion string     `json:"base_version"`
	Upstreams   []Upstream `json:"upstreams"`
}

// UpstreamResponse is GET /api/v1/upstreams/{name}. An upstream is addressed by
// its natural key, which is required and unique by validation; renaming one is
// delete-plus-create (ADR 0019 §5).
type UpstreamResponse struct {
	APIVersion  string   `json:"api_version"`
	BaseVersion string   `json:"base_version"`
	Upstream    Upstream `json:"upstream"`
}
