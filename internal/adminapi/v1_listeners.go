// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

// Listener is one bound address. A listener's identity is a composite natural
// key on `listen`: several server blocks may share one address, so the listener
// is the address, not the block.
type Listener struct {
	Listen string `json:"listen"`
	// ServerBlocks is how many server blocks bind this address.
	ServerBlocks int `json:"server_blocks"`
	// ServerNames is the union of the names served here, in declaration order.
	ServerNames []string `json:"server_names,omitempty"`
	TLS         bool     `json:"tls"`
	HTTP3       bool     `json:"http3"`
	// ClientAuth is the mutual-TLS mode when one is active: request or require.
	ClientAuth string `json:"client_auth,omitempty"`
	// ClientAddressConfigured reports whether a trusted-proxy policy was
	// written for this listener. The policy itself is not inlined here: it has
	// its own addressable sub-resource, and publishing it in two shapes would
	// be two representations of one thing, free to drift apart.
	ClientAddressConfigured bool `json:"client_address_configured"`
}

// ListenersResponse is GET /api/v1/listeners, in declaration order.
type ListenersResponse struct {
	APIVersion  string     `json:"api_version"`
	BaseVersion string     `json:"base_version"`
	Listeners   []Listener `json:"listeners"`
}

// ClientAddress is one listener's effective trusted-proxy policy: the rules
// that decide which client address a request is attributed to.
type ClientAddress struct {
	Listen       string `json:"listen"`
	ServerBlocks int    `json:"server_blocks"`
	// Configured distinguishes a written policy from the defaults below, which
	// otherwise look identical on the wire.
	Configured       bool     `json:"configured"`
	TrustedProxies   []string `json:"trusted_proxies"`
	ForwardedHeaders []string `json:"forwarded_headers"`
	MaxHops          int      `json:"max_hops"`
	// HeadersDisabled reports an explicitly empty forwarded_headers, which is
	// not the same as the default preference.
	HeadersDisabled bool `json:"headers_disabled"`
	// TrustsEveryClient flags a range covering the whole address space, which
	// lets any client assert any address.
	TrustsEveryClient bool `json:"trusts_every_client"`
}

// ClientAddressResponse is GET /api/v1/listeners/{addr}/client_address.
type ClientAddressResponse struct {
	APIVersion    string        `json:"api_version"`
	BaseVersion   string        `json:"base_version"`
	ClientAddress ClientAddress `json:"client_address"`
}

// SNIRoute is one server-name-to-target mapping of a TLS-passthrough stream.
//
// It is a list entry rather than an object keyed by server name because the
// keys are operator-chosen: a JSON object with unbounded keys cannot be
// described by a schema, so publishing one would put part of the contract
// beyond the generated document.
type SNIRoute struct {
	ServerName string `json:"server_name"`
	Target     string `json:"target"`
}

// Stream is one declared L4 listener.
//
// A stream's identity is the composite natural key (protocol, listen), and it
// has deliberately no addressable path: the collection is its whole external
// surface.
type Stream struct {
	Listen   string `json:"listen"`
	Protocol string `json:"protocol"`
	// Target is the proxy destination for a non-passthrough stream.
	Target string `json:"target,omitempty"`
	// SNIRoutes is sorted by server name. A stream carries at most one of
	// Target and SNIRoutes.
	SNIRoutes      []SNIRoute `json:"sni_routes,omitempty"`
	TLSPassthrough bool       `json:"tls_passthrough"`
	ProxyProtocol  string     `json:"proxy_protocol,omitempty"`
	// ConnectTimeoutMS and IdleTimeoutMS are integer milliseconds, never a Go
	// duration string (ADR 0019 §26.4), and they are the **effective** values
	// after defaulting — a client reads what the stream will actually do without
	// having to know this server's defaults.
	ConnectTimeoutMS int64 `json:"connect_timeout_ms"`
	IdleTimeoutMS    int64 `json:"idle_timeout_ms"`
}

// StreamsResponse is GET /api/v1/streams, in declaration order.
type StreamsResponse struct {
	APIVersion  string `json:"api_version"`
	BaseVersion string `json:"base_version"`
	// Compiled reports whether this build includes the L4 stream proxy. A
	// declared stream still validates in a build without it, but the process
	// refuses to start — so a client that saw only the declarations would
	// believe a service was configured that this binary cannot serve.
	Compiled bool     `json:"compiled"`
	Streams  []Stream `json:"streams"`
}
