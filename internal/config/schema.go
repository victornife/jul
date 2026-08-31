// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package config defines the configuration schema for the edge server and the
// machinery to load, validate, and hot-reload it.
//
// The schema mirrors common NGINX concepts (listen, server_name, location,
// proxy_pass, upstream, ...) but deliberately uses TOML rather than NGINX
// syntax. The [[stream]] table configures L4 (TCP/UDP) proxying and is active
// in builds with the "stream" tag. The [plugins] table configures WASM plugins
// and is active in builds with the "wasmplugins" tag.
package config

import (
	"strings"
	"time"

	"jul/internal/backendtls"
	"jul/internal/resilience"
)

// Config is the root configuration document.
type Config struct {
	Global        GlobalConfig        `toml:"global"`
	Servers       []ServerConfig      `toml:"servers"`
	Upstreams     []UpstreamConfig    `toml:"upstreams,omitempty"`
	Cache         CacheConfig         `toml:"cache"`
	Admin         AdminConfig         `toml:"admin"`
	Compression   CompressionConfig   `toml:"compression"`
	RateLimit     RateLimitConfig     `toml:"rate_limit"`
	Observability ObservabilityConfig `toml:"observability"`

	// Egress is the optional outbound-destination allow-list ([egress]). When
	// enabled it constrains the server's config-driven auxiliary fetches (JWKS,
	// forward-auth, service discovery) to an approved set of hosts/CIDRs,
	// reducing the blast radius of a misconfigured or compromised config. It is
	// disabled by default and core (no build tag). See internal/egress.
	Egress EgressConfig `toml:"egress"`

	// WAF is the global web-application-firewall policy ([waf]). It applies to
	// every location unless a location sets its own [servers.locations.waf]
	// override. It is enforced only in builds with the "waf" tag; a lean build
	// refuses any configuration that enables it (see internal/waf.Check).
	WAF WAFConfig `toml:"waf"`

	// Plugins declares WASM plugins by name ([plugins.NAME]). Locations and
	// servers reference them by name. Plugins are loaded only in builds with the
	// "wasmplugins" tag; a lean build refuses any config that declares them.
	Plugins map[string]PluginConfig `toml:"plugins,omitempty"`

	// Streams declares L4 (TCP/UDP) reverse-proxy listeners ([[stream]]). They
	// are served only in builds with the "stream" tag; a lean build refuses any
	// config that declares them (see internal/stream.Check).
	Streams []StreamServer `toml:"stream,omitempty"`
}

// StreamServer is one L4 (TCP or UDP) reverse-proxy listener ([[stream]]). It
// forwards raw connections/datagrams to a backend without parsing the
// application protocol. For TLS it can route by SNI host without terminating
// (TLS passthrough) and preserve the client address via the PROXY protocol.
type StreamServer struct {
	// Listen is the bind address (host:port), e.g. "0.0.0.0:5432".
	Listen string `toml:"listen"`
	// Protocol is "tcp" (default) or "udp".
	Protocol string `toml:"protocol"`
	// ProxyPass is the default backend: a named upstream or a literal host:port.
	// It is used when no SNI route matches (or for non-TLS / UDP streams).
	ProxyPass string `toml:"proxy_pass"`
	// SNIRoutes maps a TLS server name (SNI host) to a backend (named upstream
	// or host:port). Setting it enables ClientHello SNI inspection on a TCP
	// listener and routes by host without terminating TLS. A "*" key is a
	// catch-all that takes precedence over ProxyPass.
	SNIRoutes map[string]string `toml:"sni_routes"`
	// TLSPassthrough documents that the listener forwards TLS unmodified. It is
	// implied whenever SNIRoutes is set and is informational otherwise. JUL
	// never terminates TLS on a stream listener in v1.
	TLSPassthrough bool `toml:"tls_passthrough"`
	// ProxyProtocol controls HAProxy PROXY-protocol handling (TCP only):
	// "" (off), "in" (parse a header from the client), "out" (emit a v2 header
	// to the backend), or "both". It preserves the real client address across
	// the proxy hop.
	ProxyProtocol string `toml:"proxy_protocol"`
	// TrustedProxies lists the CIDR prefixes, or bare addresses meaning a single
	// host, permitted to assert a client address with an inbound PROXY header.
	// It is required whenever ProxyProtocol ingests one ("in" or "both"): a
	// PROXY header is an assertion, not a kernel fact, so believing it from any
	// peer would let a direct connection choose the address the backend sees.
	// A connection from an address outside this set is refused.
	TrustedProxies []string `toml:"trusted_proxies"`
	// ConnectTimeout bounds dialing the backend. Zero applies a 10s default.
	ConnectTimeout Duration `toml:"connect_timeout"`
	// IdleTimeout closes a relayed connection/UDP session after this period with
	// no traffic in either direction. Zero applies a 5m default.
	IdleTimeout Duration `toml:"idle_timeout"`
	// MaxUDPSessions caps the number of concurrent UDP sessions (one per client
	// source address) a UDP listener tracks, bounding memory and backend sockets
	// on the public internet where source addresses are cheap to spoof. When the
	// cap is reached, an idle session is reclaimed to admit a new client; if none
	// is reclaimable the new client's datagram is dropped. Zero applies a 10000
	// default. Ignored for tcp listeners.
	MaxUDPSessions int `toml:"max_udp_sessions"`
}

// PluginConfig declares a single WASM plugin. Exactly one of Path or Inline
// supplies the module bytes. Capabilities (KV, Fetch) default off and must be
// granted explicitly; Fetch additionally requires AllowedHosts.
type PluginConfig struct {
	// Path is the filesystem path to the .wasm module.
	Path string `toml:"path"`
	// Inline is the module bytes encoded as standard base64, an alternative to
	// Path for self-contained configs.
	Inline string `toml:"inline"`
	// Type is "middleware" (wraps a handler, may pass through) or "handler"
	// (a terminal location action). Defaults to "middleware".
	Type string `toml:"type"`
	// Config is an arbitrary string map handed to the guest as a JSON object via
	// the get_config host function.
	Config map[string]string `toml:"config"`
	// MemoryLimit caps the guest's linear memory. Zero applies a 16 MiB default.
	MemoryLimit Size `toml:"memory_limit"`
	// Timeout bounds a single guest invocation. Zero applies a 100ms default.
	Timeout Duration `toml:"timeout"`
	// KV grants access to the plugin key/value store host functions.
	KV bool `toml:"kv"`
	// Fetch grants the guarded outbound HTTP fetch host function. Requires
	// AllowedHosts.
	Fetch bool `toml:"fetch"`
	// AllowedHosts is the allowlist of hosts the guest may fetch from when Fetch
	// is granted.
	AllowedHosts []string `toml:"allowed_hosts"`
	// MaxRequestBody caps the request body the host buffers for a guest. Zero
	// applies a 1 MiB default; a larger body fails the call rather than truncating.
	MaxRequestBody Size `toml:"max_request_body"`
	// MaxResponseBody caps the response body a guest may accumulate. Zero applies
	// an 8 MiB default; an overflow fails the call rather than dropping bytes.
	MaxResponseBody Size `toml:"max_response_body"`
	// FetchTimeout bounds a single outbound fetch. Zero applies a 5s default.
	FetchTimeout Duration `toml:"fetch_timeout"`
	// MaxFetchResponse caps a fetch response body. Zero applies a 1 MiB default.
	MaxFetchResponse Size `toml:"max_fetch_response"`
	// KVMaxEntries caps distinct keys per plugin. Zero applies a 1024 default.
	KVMaxEntries int `toml:"kv_max_entries"`
	// KVMaxBytes caps total stored bytes per plugin. Zero applies a 1 MiB default.
	KVMaxBytes Size `toml:"kv_max_bytes"`
}

// WAFConfig configures the Coraza-based web application firewall, either
// globally ([waf]) or as a per-location override ([servers.locations.waf]). It
// is enforced only in builds with the "waf" tag; a lean build refuses any
// configuration that enables it (see internal/waf.Check).
type WAFConfig struct {
	// Enabled turns the firewall on for the scope it appears in.
	Enabled bool `toml:"enabled"`
	// Mode is "block" (default) — a rule interruption returns BlockStatus — or
	// "detect", which records and logs the event but lets the request proceed.
	Mode string `toml:"mode"`
	// BlockStatus is the HTTP status returned when a request is blocked in
	// "block" mode. Zero applies 403. A rule may override it via its own status.
	BlockStatus int `toml:"block_status"`
	// DirectivesFiles lists SecLang rule files to load, in order, after the
	// CRS (when crs_enabled) and before InlineRules. They are post-CRS tuning
	// files, so a rule exclusion can reference a CRS rule that already loaded.
	DirectivesFiles []string `toml:"directives_files"`
	// InlineRules is a SecLang snippet appended last (after files and the CRS).
	// It is handy for small allow-list or tuning rules without a separate file.
	InlineRules string `toml:"inline_rules"`
	// CRSEnabled loads the embedded OWASP Core Rule Set with zero external
	// setup (the rules ship inside the binary in builds with the "waf" tag).
	CRSEnabled bool `toml:"crs_enabled"`
	// Paranoia sets the CRS blocking paranoia level (1–4) when CRSEnabled is
	// set. Zero leaves the CRS default (1). Higher levels catch more attacks at
	// the cost of more false positives.
	Paranoia int `toml:"paranoia"`
	// RequestBodyLimit caps how many request-body bytes are buffered for
	// inspection. Zero applies a 128 KiB default.
	RequestBodyLimit Size `toml:"request_body_limit"`
	// ResponseBodyCheck enables inspection of response bodies (CRS phase 4).
	// It buffers the response up to Coraza's response-body limit and so adds
	// latency and memory; leave it off unless outbound rules are needed.
	ResponseBodyCheck bool `toml:"response_body_check"`
}

// GlobalConfig holds process-wide settings.
type GlobalConfig struct {
	// WorkerThreads accepts "auto" or a positive integer as a string.
	// "auto" (or empty) maps to GOMAXPROCS defaults.
	WorkerThreads string `toml:"worker_threads"`
	AccessLog     string `toml:"access_log"`
	ErrorLog      string `toml:"error_log"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `toml:"log_level"`
	// LogFormat is "text" (human readable, default in dev) or "json".
	LogFormat string `toml:"log_format"`
	// ShutdownTimeout is how long to wait for in-flight requests to drain.
	ShutdownTimeout Duration `toml:"shutdown_timeout"`
	// RedactMinSecretLength is the shortest resolved secret value that is masked
	// from logs. Zero uses the default (4). Lower it (down to 1) when secrets are
	// shorter than the default, accepting that short values may also mask
	// incidental log text.
	RedactMinSecretLength int `toml:"redact_min_secret_length"`
	// ReloadTimeout is how long a hot reload may run before it is reported as
	// timed out. The previous config keeps serving. Zero or omitted defaults to
	// 10s. Set a larger value for very large configs or slow DNS; operators who
	// want to be warned about slow reloads without disabling the threshold should
	// use a very large duration (e.g. "1h").
	ReloadTimeout Duration `toml:"reload_timeout"`
	// ConfigAuthority declares who owns configuration persistence, managed
	// history, and drift detection: "managed" (Jul owns the file; Console/API
	// writes are subject to RBAC and CAS, and external edits become drift that
	// must be explicitly adopted) or "file_owned" (an external file or GitOps
	// pipeline owns the file; every mutating admin endpoint is refused and the
	// file watcher/SIGHUP behave exactly as they do without this field).
	// "controller_owned" is a reserved value rejected by validation until a
	// real external controller exists. Omitted resolves to "file_owned" — a
	// fixed default, never derived from any other field (ADR 0019 §9.1). It is
	// restart-required: changing it moves ownership of the configuration file
	// and can only be staged through stage_restart (ADR 0019 §9.2).
	ConfigAuthority string `toml:"config_authority"`
}

// ServerConfig is a virtual host bound to one listen address.
type ServerConfig struct {
	Name        string           `toml:"name"`
	Listen      string           `toml:"listen"`
	ServerNames []string         `toml:"server_names"`
	Locations   []LocationConfig `toml:"locations"`
	TLS         *TLSConfig       `toml:"tls"`
	// HTTP3, when enabled, starts a parallel HTTP/3 (QUIC) listener on the same
	// address over UDP and advertises it via Alt-Svc. It requires TLS on this
	// server block and is compiled only into builds with the "http3" tag.
	HTTP3 *HTTP3Config `toml:"http3"`
	// ProxyProtocol enables ingesting a HAProxy PROXY-protocol header from a TCP
	// load balancer: "" (off) or "in". The advertised address becomes this
	// listener's transport peer, so the ordinary client_address derivation runs
	// on top of it unchanged. It requires client_address.trusted_proxies, which
	// names the balancers permitted to assert an address, and cannot be combined
	// with HTTP/3 on the same listener because QUIC carries no such framing.
	// Emitting a header outbound is a backend concern and is not offered here.
	ProxyProtocol string `toml:"proxy_protocol"`

	// H2C enables cleartext HTTP/2 (h2c) on this listener so native gRPC and
	// other HTTP/2 clients can connect without TLS, in addition to HTTP/1.1. It
	// only takes effect on a plaintext (non-TLS) listener — TLS listeners already
	// negotiate HTTP/2 via ALPN. Typically paired with a grpc = true proxy
	// location (which itself requires the "grpc" build tag).
	H2C bool `toml:"h2c"`

	// Limits and timeouts (per-server defaults; locations may override).
	ClientMaxBodySize Size     `toml:"client_max_body_size"`
	ReadHeaderTimeout Duration `toml:"read_header_timeout"`
	ReadTimeout       Duration `toml:"read_timeout"`
	WriteTimeout      Duration `toml:"write_timeout"`
	IdleTimeout       Duration `toml:"idle_timeout"`
	// MaxHeaderBytes caps the size of request headers (default 1 MiB).
	MaxHeaderBytes Size `toml:"max_header_bytes"`

	AccessLog string `toml:"access_log"`
	ErrorLog  string `toml:"error_log"`

	// ErrorPages maps a status code to a file path or redirect URL.
	ErrorPages map[string]string `toml:"error_pages"`

	// RedirectHTTPS, when set on an HTTP server block, issues a redirect to
	// the equivalent HTTPS URL. Value is the status code (301 or 308).
	RedirectHTTPS int `toml:"redirect_https"`

	// Plugins lists middleware plugin names applied to every location in this
	// server, outermost first. Each name must appear in [plugins]. Requires the
	// "wasmplugins" build tag.
	Plugins []string `toml:"plugins"`

	// ClientAddress is the trusted-proxy policy that decides how the canonical
	// client address is derived for requests on this listener. It is a listener
	// scoped policy: every server block sharing a listen address must declare
	// the same one, because identity is derived before the Host header selects
	// a block.
	ClientAddress *ClientAddressConfig `toml:"client_address"`
}

// ClientAddressConfig is the per-listener policy for deriving the canonical
// client address from forwarding headers. Omitting the block trusts no proxy,
// so the canonical client is always the direct transport peer.
//
// TrustedProxies is a security boundary and should be as narrow as the
// deployment allows: every address it covers may assert any client address.
type ClientAddressConfig struct {
	// TrustedProxies lists the CIDR prefixes (or bare addresses, meaning a
	// single host) whose forwarding headers are believed. Prefixes must be in
	// canonical form with host bits clear. Empty means no proxy is trusted and
	// forwarding headers are never read.
	TrustedProxies []string `toml:"trusted_proxies"`
	// ForwardedHeaders is the ordered preference of forwarding headers:
	// "forwarded" (RFC 7239) and "x-forwarded-for". Omitted defaults to
	// ["forwarded", "x-forwarded-for"]; an explicitly empty list disables both,
	// keeping peer-only identity even for a trusted peer. The first header
	// present on the request is the only one used; chains are never merged.
	ForwardedHeaders []string `toml:"forwarded_headers"`
	// MaxHops bounds how many asserted hops a chain may carry. A longer chain
	// fails closed to the direct peer. Zero or omitted defaults to 16.
	MaxHops int `toml:"max_hops"`
}

// MatchConfig selects requests for a location. Type and Path decide which
// candidates a request produces; the predicate fields filter within that
// candidate set without ever promoting a route across it (ADR 0018 §1-§6).
//
// A list inside one field is an OR-set; separate fields and separate table
// entries are ANDed. That is the whole Boolean model: no negation, no grouping,
// no OR across fields.
type MatchConfig struct {
	// Type is one of "exact", "prefix", or "regex".
	Type string `toml:"type"`
	Path string `toml:"path"`

	// Methods is the OR-set of request methods the location accepts, compared
	// byte-exactly against r.Method. Omitted means the route does not constrain
	// the method; an explicitly empty list is a validation error, because a
	// route that can never match is a mistake rather than a way to disable one.
	// A route listing GET also matches HEAD (RFC 9110 §9.3.2).
	Methods []string `toml:"methods,omitempty"`

	// Headers are request-header predicates. An array of tables rather than a
	// map, because declaration order is part of the contract, one field name may
	// carry more than one predicate, and a Go map has no iteration order.
	Headers []HeaderMatch `toml:"headers,omitempty"`

	// Query are query-parameter predicates, in the same shape and for the same
	// reasons as Headers.
	Query []QueryMatch `toml:"query,omitempty"`
}

// HeaderMatch is one request-header predicate. Value is a pointer so an omitted
// value stays distinguishable from an explicitly empty one: `op = "exact"` with
// an empty value matches only a present-but-empty field, which is a different
// configuration from omitting the key altogether.
type HeaderMatch struct {
	// Name is the field name, canonicalized with textproto.CanonicalMIMEHeaderKey
	// when the router compiles it, so lookup is case-insensitive as HTTP requires.
	Name string `toml:"name"`
	// Op is "present", "exact" or "regex".
	Op string `toml:"op"`
	// Value is required for "exact" and "regex" and forbidden for "present".
	Value *string `toml:"value,omitempty"`
}

// QueryMatch is one query-parameter predicate. Value is a pointer for the same
// reason as HeaderMatch.Value.
type QueryMatch struct {
	// Name is the parameter name, compared after percent-decoding.
	Name string `toml:"name"`
	// Op is "present" or "exact". There is no regex operator for query
	// parameters in this tranche.
	Op string `toml:"op"`
	// Value is required for "exact" and forbidden for "present".
	Value *string `toml:"value,omitempty"`
}

// ResponseHeaderOp is one response-header operation
// ([[servers.locations.response_headers]], ADR 0018 §8). Operations apply top
// to bottom in declaration order, and a later one observes the earlier ones'
// effect on the response header map — this is what makes an ordered list, not
// a map, the schema: a "set" followed by two "add"s is the canonical
// deterministic way to express a multi-value header.
type ResponseHeaderOp struct {
	// Op is "add" (Header.Add), "set" (Header.Set) or "remove" (Header.Del).
	Op   string `toml:"op"`
	Name string `toml:"name"`
	// Value is required for "add"/"set" and forbidden for "remove". A pointer so
	// an omitted value (an error) stays distinguishable from an explicitly empty
	// one (legal: it emits an empty field value).
	Value *string `toml:"value,omitempty"`
}

// CORSConfig is a location's CORS policy ([servers.locations.cors], ADR 0018
// §9). A nil pointer means the location has no CORS policy at all, which is
// distinct from Enabled = false: both are inert at runtime, but a populated,
// disabled block still has every field validated (§9's "flipping enabled later
// must not surface a value that was never valid").
type CORSConfig struct {
	Enabled bool `toml:"enabled"`

	// AllowedOrigins is required when Enabled. Exact, normalized origins
	// ("scheme://host[:port]", lowercase, no path, no default port) — no
	// wildcard subdomains, no regex — or exactly ["*"], which is unconditional:
	// it forbids AllowCredentials and forbids any other entry, and matches
	// every request including one with no Origin or Origin: null.
	AllowedOrigins []string `toml:"allowed_origins,omitempty"`

	// AllowedMethods governs preflight approval only, never ordinary requests
	// (that is match.methods). Omitted defaults to the CORS-safelisted set
	// ["GET", "HEAD", "POST"]; an explicit empty list is a validation error.
	// No "*".
	AllowedMethods []string `toml:"allowed_methods,omitempty"`

	// AllowedHeaders governs preflight approval only. Omitted or empty means no
	// header is approved beyond the safelist Access-Control-Request-Headers
	// never lists; the response omits Access-Control-Allow-Headers rather than
	// emitting it empty. No "*" — under Fetch a wildcard does not cover
	// Authorization, the header an operator writing "*" usually wants.
	AllowedHeaders []string `toml:"allowed_headers,omitempty"`

	// ExposedHeaders is emitted as Access-Control-Expose-Headers on a granted
	// response. Omitted or empty omits the header. No "*".
	ExposedHeaders []string `toml:"exposed_headers,omitempty"`

	// AllowCredentials emits Access-Control-Allow-Credentials: true on a granted
	// response. Forbidden together with AllowedOrigins = ["*"].
	AllowCredentials bool `toml:"allow_credentials"`

	// MaxAge is Access-Control-Max-Age, in whole seconds, 0 to 24h. Omitted
	// means the header is not emitted at all; "0s" is legal and emits 0. A
	// pointer so omitted and zero stay distinguishable.
	MaxAge *Duration `toml:"max_age,omitempty"`
}

// LocationConfig describes how to handle requests matching Match. Exactly one
// action (root/proxy_pass/fastcgi_pass/redirect/return) should be set.
type LocationConfig struct {
	Match MatchConfig `toml:"match"`

	// RouteID is an optional, durable identifier for this location (ADR 0019
	// §4). It survives edits to Match, the action, and reordering, and is
	// globally unique across the whole configuration document. Jul mints one
	// (r-<26 lowercase base32 characters over 128 CSPRNG bits>) only when a
	// managed structured-API create operation omits it; every other path —
	// edit, raw apply, adoption, parse, validate, lint, fmt, schema
	// generation, a projection or diff read, reload, or startup — preserves
	// exactly what is present and never adds one. A route without a RouteID
	// remains fully addressable through ADR 0018's revision-scoped selector
	// (listen, server_names, match_type, path, match_ordinal) plus
	// base_version; neither form is degraded relative to the other.
	RouteID *string `toml:"route_id,omitempty"`

	// ResponseHeaders is the ordered list of add/set/remove operations applied
	// to the response this location produces, outside the cache and outside
	// compression's own headers (ADR 0018 §8). CORS's own Access-Control-*
	// fields are governed separately by CORS below and run after these.
	ResponseHeaders []ResponseHeaderOp `toml:"response_headers,omitempty"`

	// CORS is this location's Cross-Origin Resource Sharing policy (ADR 0018
	// §9). A nil pointer means no policy; CORS is authoritative over any
	// Access-Control-* field ResponseHeaders might otherwise have set, which is
	// why configuring one when the other targets the same fields is a
	// validation error (§8b) rather than an ordering rule.
	CORS *CORSConfig `toml:"cors,omitempty"`

	// Static file serving.
	Root             string   `toml:"root"`
	Index            []string `toml:"index"`
	TryFiles         []string `toml:"try_files"`
	DirectoryListing bool     `toml:"directory_listing"`
	AllowHidden      bool     `toml:"allow_hidden"`
	CacheControl     string   `toml:"cache_control"`

	// Reverse proxy. ProxyPass is either an upstream reference
	// ("http://name") or a concrete URL ("http://127.0.0.1:3000").
	ProxyPass           string   `toml:"proxy_pass"`
	ProxyConnectTimeout Duration `toml:"proxy_connect_timeout"`
	ProxyReadTimeout    Duration `toml:"proxy_read_timeout"`
	ProxySendTimeout    Duration `toml:"proxy_send_timeout"`
	// ProxyRetries caps the number of retry attempts for idempotent requests
	// against other backends on connection failure. 0 (default) means try every
	// distinct backend at most once. A positive value limits attempts to the
	// configured count.
	//
	// Deprecated: use [servers.locations.resilience] retry_attempts, which is
	// the same control under the name the pool-level block already uses, so the
	// override rule reads as one word overriding the same word. This spelling
	// stays valid and is scheduled for removal in the next major; setting both
	// is a validation error rather than a silent precedence rule.
	ProxyRetries int `toml:"proxy_retries"`

	// BackendTLS is the outbound TLS policy for this location's backend,
	// whether the target is an https:// literal, a named upstream reached over
	// TLS, native gRPC over TLS, or a TLS transcoding target. It is the same
	// block, with the same meaning, as under [[upstreams]]; a location block
	// overrides the pool's for this route.
	BackendTLS *BackendTLSConfig `toml:"backend_tls"`

	// Resilience carries the stateless resilience controls this route may
	// override. The stateful admission controls are not part of this type: they
	// belong under [[upstreams]], where the pool owns their state.
	Resilience *LocationResilienceConfig `toml:"resilience"`

	// GRPC turns the proxy_pass into a native gRPC / HTTP-2 passthrough: the
	// request is forwarded end-to-end over HTTP/2 (preserving trailers such as
	// grpc-status) with response buffering disabled so streaming frames flush
	// immediately. A proxy_pass scheme of http:// dials the backend over
	// cleartext HTTP/2 (h2c); https:// dials over HTTP/2 with TLS. It requires a
	// build with the "grpc" tag. Distinct from grpc_transcode, which converts
	// REST/JSON to gRPC; this proxies native gRPC unchanged.
	GRPC bool `toml:"grpc"`

	// FastCGI / uWSGI.
	FastCGIPass   string            `toml:"fastcgi_pass"`
	FastCGIParams map[string]string `toml:"fastcgi_params"`
	UWSGIPass     string            `toml:"uwsgi_pass"`

	// Redirect / return.
	Redirect string `toml:"redirect"` // target URL; uses Return code or 302
	Return   int    `toml:"return"`   // status code for redirect or bare return

	// Deny rejects matching requests with 403.
	Deny bool `toml:"deny"`

	// Headers added/overridden on the upstream request. Values may use
	// variables such as $host, $remote_addr, $proxy_add_x_forwarded_for.
	Headers map[string]string `toml:"headers"`

	// Rewrite rules applied before dispatch.
	Rewrites []RewriteConfig `toml:"rewrites"`

	// Caching toggle for this location (requires [cache].enabled).
	Cache bool `toml:"cache"`

	// ClientMaxBodySize overrides the server default for this location.
	ClientMaxBodySize Size `toml:"client_max_body_size"`

	// RateLimit, when set, overrides the global [rate_limit] policy for this
	// location (rate/burst/key). MaxConns is ignored here, since connection
	// caps are listener-global. A nil pointer inherits the global policy.
	RateLimit *RateLimitConfig `toml:"rate_limit"`

	// Auth, when set, applies per-location access control (CIDR allow/deny,
	// HTTP Basic, JWT, forward-auth) as a modifier composed around the
	// location's action. A nil pointer leaves the location unauthenticated.
	Auth *AuthConfig `toml:"auth"`

	// WAF, when set, overrides the global [waf] policy for this location: the
	// pointer fully replaces the global policy (it is not merged). A nil pointer
	// inherits the global [waf] policy. It is enforced only in builds with the
	// "waf" tag.
	WAF *WAFConfig `toml:"waf"`

	// RequireClientCert rejects a request that arrives without a verified mTLS
	// client certificate with 403, even when the server's tls.client_auth mode
	// is "request" (which admits the connection at the handshake). It requires
	// the enclosing server block to enable tls.client_auth. The verified
	// identity is exposed to proxy headers as $ssl_client_* variables.
	RequireClientCert bool `toml:"require_client_cert"`

	// GRPCTranscode, when set, turns this location into a gRPC-JSON transcoder:
	// it accepts REST/JSON requests, maps them to a gRPC method (unary or, when
	// streaming is enabled, server/client/bidi streaming) via google.api.http
	// annotations, calls the gRPC backend, and returns the reply as JSON. It is
	// an action (mutually exclusive with root/proxy_pass/etc.) and requires a
	// build with the "grpc" tag.
	GRPCTranscode *GRPCTranscodeConfig `toml:"grpc_transcode"`

	// Plugins lists middleware plugin names applied to this location, composed
	// around the action (after any server-level plugins, outermost first). Each
	// name must appear in [plugins]. Requires the "wasmplugins" build tag.
	Plugins []string `toml:"plugins"`

	// Plugin names a handler plugin that serves this location as its action
	// (mutually exclusive with root/proxy_pass/etc.). The name must appear in
	// [plugins] with type = "handler". Requires the "wasmplugins" build tag.
	Plugin string `toml:"plugin"`
}

// GRPCTranscodeConfig configures gRPC<->REST/JSON transcoding for a location.
// The method routing table is built from google.api.http annotations carried in
// the service's protobuf descriptors, supplied either as a compiled
// FileDescriptorSet (DescriptorSet) or fetched from the backend via server
// reflection (UseReflection). Exactly one descriptor source must be set.
type GRPCTranscodeConfig struct {
	// Target is the gRPC backend: an upstream name or a concrete "host:port".
	Target string `toml:"target"`
	// DescriptorSet is the path to a protoc-generated FileDescriptorSet
	// (protoc --descriptor_set_out with --include_imports).
	DescriptorSet string `toml:"descriptor_set"`
	// UseReflection fetches descriptors from the backend via gRPC server
	// reflection instead of reading a DescriptorSet file.
	UseReflection bool `toml:"use_reflection"`
	// TLS dials the backend over TLS; otherwise plaintext HTTP/2 (h2c).
	TLS bool `toml:"tls"`
	// PreserveNames keeps original proto field names in JSON output instead of
	// the default lowerCamelCase.
	PreserveNames bool `toml:"preserve_proto_field_names"`
	// Streaming enables transcoding of streaming methods (server-streaming,
	// client-streaming, and bidirectional). When false, a request to a
	// streaming method returns 501 Not Implemented (unary methods are always
	// transcoded).
	Streaming bool `toml:"streaming"`
	// StreamMode selects the wire framing for streamed responses: "ndjson"
	// (newline-delimited JSON objects, the default) or "sse" (Server-Sent
	// Events). It has no effect on unary methods.
	StreamMode string `toml:"stream_mode"`
	// MaxMessageSize caps a single encoded message (a JSON request frame or a
	// gRPC reply). Zero applies the 4 MiB default.
	MaxMessageSize Size `toml:"max_message_size"`
}

// RewriteConfig is a regex rewrite rule.
type RewriteConfig struct {
	Pattern     string `toml:"pattern"`
	Replacement string `toml:"replacement"`
	// Flag is one of "", "last", "break", "redirect", "permanent".
	Flag string `toml:"flag"`
}

// UpstreamConfig is a named pool of backend servers.
type UpstreamConfig struct {
	Name string `toml:"name"`
	// Strategy is one of "round_robin", "weighted_round_robin", "least_conn".
	Strategy string           `toml:"strategy"`
	Servers  []UpstreamServer `toml:"servers"`

	// MaxFails and FailTimeout are the circuit breaker's failure threshold and
	// open duration.
	//
	// Deprecated: use [upstreams.resilience] max_fails and fail_timeout, so the
	// block is the whole resilience surface rather than most of it. Same names,
	// same defaults, same meanings. This spelling stays valid and is scheduled
	// for removal in the next major; setting both is a validation error rather
	// than a silent precedence rule.
	MaxFails    int      `toml:"max_fails"`
	FailTimeout Duration `toml:"fail_timeout"`

	// HealthCheck, when set and enabled, runs active probes against each backend
	// so failures are detected (and recoveries observed) without waiting for live
	// traffic. nil leaves only passive health checking in effect.
	HealthCheck *HealthCheckConfig `toml:"health_check"`

	// Discovery, when set to a non-static type, resolves the pool's backend set
	// from an external source (DNS, Consul, Kubernetes) and refreshes it live
	// without a reload. nil (or type "static"/"") uses the static Servers list.
	// When discovery is enabled the Servers list is optional and acts only as a
	// seed/fallback until the first successful resolution.
	Discovery *DiscoveryConfig `toml:"discovery"`

	// BackendTLS is the outbound TLS policy used when this pool is reached over
	// https (or TLS gRPC): trust roots, client certificate, verified name,
	// minimum version and explicit peer identities. It is the same block, with
	// the same meaning, as under [[servers.locations]].
	BackendTLS *BackendTLSConfig `toml:"backend_tls"`

	// Resilience is the pool's admission and overload policy. It is pool-scoped
	// because the state it governs has exactly one owner; setting any of its
	// fields in a location that targets a named upstream is a validation error
	// rather than a silent ignore.
	Resilience *ResilienceConfig `toml:"resilience"`
}

// ResilienceConfig is the public [upstreams.resilience] block: admission and
// overload control for one pool (ADR 0017).
//
// Every zero value means "behave exactly as Jul does today", so an upstream
// without this block is unchanged. The one field whose zero is not "unlimited"
// is MaxPendingRequests: an unbounded pending queue is the memory failure this
// block exists to prevent, so it is deliberately unrepresentable.
type ResilienceConfig struct {
	// MaxFails is how many consecutive failures take a backend out of rotation.
	// 0 means the default of 1.
	//
	// It is the circuit breaker's failure threshold, not a second mechanism
	// beside it: two spellings for one threshold, plus a precedence rule, would
	// buy an operator nothing and cost them a way to be wrong.
	MaxFails int `toml:"max_fails"`
	// FailTimeout is how long a backend stays out of rotation before it is
	// probed. 0 means the default of 10s.
	FailTimeout Duration `toml:"fail_timeout"`

	// MaxActiveRequests bounds admitted logical requests, streams and
	// connections for the pool. 0 is unlimited.
	MaxActiveRequests int `toml:"max_active_requests"`
	// MaxActivePerBackend bounds admitted logical requests per backend. It is
	// applied as a selection filter, never as a second queue, so it can neither
	// deadlock nor block one backend's traffic behind another's. 0 is unlimited.
	MaxActivePerBackend int `toml:"max_active_per_backend"`
	// MaxPendingRequests bounds the queue of requests waiting for a slot. 0
	// means no queue: reject immediately.
	MaxPendingRequests int `toml:"max_pending_requests"`
	// PendingTimeout bounds how long a request may wait for a slot. 0 leaves the
	// request context as the only bound.
	PendingTimeout Duration `toml:"pending_timeout"`
	// MaxConnectionsPerBackend bounds physical sockets to one backend host on
	// one transport. It is stateless — transports are built per location — so a
	// location may override it. 0 is unlimited.
	MaxConnectionsPerBackend int `toml:"max_connections_per_backend"`
	// RetryAttempts caps total attempts for one retryable request. 0 means try
	// every distinct backend once, which is what Jul does today.
	RetryAttempts int `toml:"retry_attempts"`
	// RetryDeadline bounds the whole retry sequence, attempts and backoff sleeps
	// alike. 0 leaves the request context as the only bound.
	RetryDeadline Duration `toml:"retry_deadline"`
	// RetryBackoffInitial is the first backoff interval, doubling per attempt
	// with full jitter. 0 means immediate failover, which is today's behaviour.
	RetryBackoffInitial Duration `toml:"retry_backoff_initial"`
	// RetryBackoffMax clamps the doubling. It requires retry_backoff_initial.
	RetryBackoffMax Duration `toml:"retry_backoff_max"`
	// RetryBudgetPercent bounds retries as a percentage of primary attempts over
	// a trailing window. 0 is unbudgeted. It owns a window, so unlike the other
	// retry controls it is pool-scoped and no location may override it.
	RetryBudgetPercent int `toml:"retry_budget_percent"`

	// CircuitHalfOpenProbes bounds how many requests may test a recovering
	// backend at once. It defaults to 1; an explicit 0 means unbounded, which
	// is the pre-#294 behaviour: when a cooldown elapsed every concurrent
	// request saw the backend as available simultaneously and a backend that
	// had just come back took the full production load.
	//
	// It is a pointer because those two cases are different answers to the same
	// question and a plain int cannot tell them apart — an absent key would
	// read as 0 and silently restore the behaviour this setting exists to fix.
	//
	// Pool-scoped only. A per-location override would let two locations
	// disagree about how many probes one shared backend may take, and the
	// backend is what is recovering.
	CircuitHalfOpenProbes *int `toml:"circuit_half_open_probes"`
}

// LocationResilienceConfig is the public [servers.locations.resilience] block.
//
// It is a different, smaller type from ResilienceConfig on purpose. A control is
// location-overridable if and only if it owns no shared state, and the admission
// counters and the pending queue have exactly one owner — the pool. Encoding
// that in the type means a stateful key written under a location is rejected by
// strict decoding, instead of needing a validation rule that could drift from
// the scope rule it implements.
type LocationResilienceConfig struct {
	// MaxConnectionsPerBackend overrides the pool's socket bound for this
	// route's transport. 0 inherits the pool's value.
	MaxConnectionsPerBackend int `toml:"max_connections_per_backend"`
	// RetryAttempts overrides the pool's attempt cap for this route. 0 inherits.
	// It is the canonical spelling of the older proxy_retries; setting both is a
	// validation error.
	RetryAttempts int `toml:"retry_attempts"`
	// RetryDeadline overrides the pool's bound on the whole retry sequence.
	// 0 inherits.
	RetryDeadline Duration `toml:"retry_deadline"`
	// RetryBackoffInitial overrides the pool's first backoff interval.
	// 0 inherits.
	RetryBackoffInitial Duration `toml:"retry_backoff_initial"`
	// RetryBackoffMax overrides the pool's clamp on backoff growth. 0 inherits.
	RetryBackoffMax Duration `toml:"retry_backoff_max"`
}

// Options converts the location block into the shape internal/resilience
// consumes for the stateless controls it owns.
func (r *LocationResilienceConfig) Options() resilience.Options {
	if r == nil {
		return resilience.Options{}
	}
	return resilience.Options{
		MaxConnectionsPerBackend: r.MaxConnectionsPerBackend,
		RetryAttempts:            r.RetryAttempts,
		RetryDeadline:            r.RetryDeadline.Std(),
		RetryBackoffInitial:      r.RetryBackoffInitial.Std(),
		RetryBackoffMax:          r.RetryBackoffMax.Std(),
	}
}

// Options converts the public block into the shape internal/resilience
// consumes, so that package never imports config.
func (r *ResilienceConfig) Options() resilience.Options {
	if r == nil {
		return resilience.Options{}
	}
	return resilience.Options{
		MaxActiveRequests:   r.MaxActiveRequests,
		MaxActivePerBackend: r.MaxActivePerBackend,
		MaxPendingRequests:  r.MaxPendingRequests,
		PendingTimeout:      r.PendingTimeout.Std(),

		MaxConnectionsPerBackend: r.MaxConnectionsPerBackend,

		RetryAttempts:       r.RetryAttempts,
		RetryDeadline:       r.RetryDeadline.Std(),
		RetryBackoffInitial: r.RetryBackoffInitial.Std(),
		RetryBackoffMax:     r.RetryBackoffMax.Std(),
		RetryBudgetPercent:  r.RetryBudgetPercent,
	}
}

// DefaultCircuitHalfOpenProbes is the bound applied when the key is absent.
const DefaultCircuitHalfOpenProbes = 1

// DefaultMaxFails and DefaultFailTimeout are the breaker's thresholds when
// neither spelling sets them.
const (
	DefaultMaxFails    = 3
	DefaultFailTimeout = 10 * time.Second
)

// HalfOpenProbes resolves the half-open probe bound, distinguishing an absent
// key (default) from an explicit 0 (unbounded).
func (r *ResilienceConfig) HalfOpenProbes() int {
	if r == nil || r.CircuitHalfOpenProbes == nil {
		return DefaultCircuitHalfOpenProbes
	}
	return *r.CircuitHalfOpenProbes
}

// CircuitMaxFails resolves the failure threshold from either spelling. The
// deprecated top-level key is only consulted when the block does not set it,
// and both being set is a validation error, so this can never silently pick a
// winner between two values an operator wrote.
func (u UpstreamConfig) CircuitMaxFails() int {
	if u.Resilience != nil && u.Resilience.MaxFails > 0 {
		return u.Resilience.MaxFails
	}
	if u.MaxFails > 0 {
		return u.MaxFails
	}
	return DefaultMaxFails
}

// CircuitFailTimeout resolves the open duration from either spelling.
func (u UpstreamConfig) CircuitFailTimeout() time.Duration {
	if u.Resilience != nil && u.Resilience.FailTimeout.Std() > 0 {
		return u.Resilience.FailTimeout.Std()
	}
	if d := u.FailTimeout.Std(); d > 0 {
		return d
	}
	return DefaultFailTimeout
}

// BackendTLSConfig is the outbound (backend) TLS policy. It is deliberately a
// different key from the inbound [servers.tls] block: `tls` already means
// *inbound* termination under [[servers]], so reusing it would give one key
// opposite directions in two places.
//
// The same block appears under [[upstreams]] and [[servers.locations]] and is
// resolved by internal/backendtls into one immutable policy shared by the HTTP
// proxy, native gRPC, transcoding and active health checks. Transports never
// read these fields directly.
type BackendTLSConfig struct {
	// CAFile is a PEM bundle of trust roots. It is consulted only when CAMode
	// selects it — never inferred from its presence.
	CAFile string `toml:"ca_file"`
	// CAMode is "system" (default), "system_and_file" or "file_only". It is an
	// explicit enum because inferring augment-versus-replace from the presence
	// of ca_file cannot be changed later without silently altering which
	// backends verify.
	CAMode string `toml:"ca_mode"`
	// ClientCert and ClientKey are the client certificate presented to the
	// backend (mutual TLS). Both are required together.
	ClientCert string `toml:"client_cert"`
	ClientKey  string `toml:"client_key"`
	// ServerName overrides the verified identity and the SNI value. It matters
	// for a discovery-backed pool: the selected address is a dial destination,
	// while the configured logical name stays the verified identity.
	ServerName string `toml:"server_name"`
	// MinVersion is "1.2" (default) or "1.3".
	MinVersion string `toml:"min_version"`
	// PeerIdentities are prefixed identities ("dns:name", "uri:spiffe://...")
	// matched against the backend certificate after standard verification.
	// They are ORed and are never matched by regex or substring.
	PeerIdentities []string `toml:"peer_identities"`
	// InsecureSkipVerify disables backend certificate verification. It is
	// accepted by validation so an emergency path exists, but `jul lint`
	// reports it as an error and it cannot be combined with peer_identities or
	// a non-system ca_mode.
	InsecureSkipVerify bool `toml:"insecure_skip_verify"`
}

// Options converts the public block into the resolver's input. It exists so
// internal/backendtls never imports this package.
func (c *BackendTLSConfig) Options() backendtls.Options {
	if c == nil {
		return backendtls.Options{}
	}
	return backendtls.Options{
		CAFile:             c.CAFile,
		CAMode:             c.CAMode,
		ClientCert:         c.ClientCert,
		ClientKey:          c.ClientKey,
		ServerName:         c.ServerName,
		MinVersion:         c.MinVersion,
		PeerIdentities:     c.PeerIdentities,
		InsecureSkipVerify: c.InsecureSkipVerify,
	}
}

// DiscoveryConfig configures dynamic backend discovery for an upstream pool. A
// per-pool refresher goroutine polls the selected source on an interval and
// applies changes via the pool's state-preserving UpdateBackends, so backends
// come and go without a restart while passive/active health and load balancing
// continue to apply.
type DiscoveryConfig struct {
	// Type selects the resolver: "dns" (A/AAAA records), "dns_srv" (SRV
	// records), "consul", or "kubernetes". "static" (or empty) disables
	// discovery and uses the upstream's Servers list. The "consul" and
	// "kubernetes" providers require builds with the matching build tag.
	Type string `toml:"type"`
	// Target is the resolver query. For "dns" it is "host:port" (the port is
	// applied to every resolved address, since A/AAAA records carry no port).
	// For "dns_srv" it is the SRV name (e.g. "_grpc._tcp.svc.example.com"),
	// which carries port and weight. Unused for consul/kubernetes.
	Target string `toml:"target"`
	// Refresh is the polling interval (default 30s).
	Refresh Duration `toml:"refresh"`
	// Consul holds Consul-specific settings (Type "consul").
	Consul *ConsulDiscovery `toml:"consul"`
	// Kubernetes holds Kubernetes-specific settings (Type "kubernetes").
	Kubernetes *KubernetesDiscovery `toml:"kubernetes"`
}

// ConsulDiscovery configures discovery from a Consul service catalog (queried
// over Consul's HTTP API; no Consul client library is linked in).
type ConsulDiscovery struct {
	// Address is the Consul HTTP API base URL (default "http://127.0.0.1:8500").
	Address string `toml:"address"`
	// Service is the Consul service name to resolve (required).
	Service string `toml:"service"`
	// Tag, when set, restricts results to instances carrying this tag.
	Tag string `toml:"tag"`
	// Datacenter, when set, queries a specific datacenter.
	Datacenter string `toml:"datacenter"`
	// Token is an optional ACL token sent as X-Consul-Token.
	Token string `toml:"token"`
	// TLS configures the trust used to authenticate the Consul agent when
	// Address is https. It is the same block as [upstreams.backend_tls]: a
	// control-plane peer is authenticated by the same proof as a data-plane one
	// (ADR 0016 §14), and one normalized type keeps them from drifting apart.
	// Without it an https address verifies against the platform roots.
	TLS *BackendTLSConfig `toml:"tls"`
	// PassingOnly restricts results to instances whose health checks are passing
	// (default true).
	PassingOnly *bool `toml:"passing_only"`
}

// KubernetesDiscovery configures discovery from Kubernetes EndpointSlices
// (queried over the API server's REST API; client-go is not linked in). In a
// pod the API server URL and service-account credentials are read from the
// standard in-cluster locations; the fields below override them when running
// outside a cluster.
type KubernetesDiscovery struct {
	// Namespace of the target Service (required).
	Namespace string `toml:"namespace"`
	// Service is the Kubernetes Service name whose endpoints are resolved
	// (required).
	Service string `toml:"service"`
	// Port selects the endpoint port by name or number. When empty the first
	// port of each EndpointSlice is used.
	Port string `toml:"port"`
	// APIServer overrides the API server base URL (default from the in-cluster
	// KUBERNETES_SERVICE_HOST/PORT environment).
	APIServer string `toml:"api_server"`
	// Token overrides the bearer token (default: the mounted service-account
	// token).
	Token string `toml:"token"`
	// CAFile overrides the API server CA bundle (default: the mounted
	// service-account CA). Set InsecureSkipTLSVerify only for local testing.
	CAFile string `toml:"ca_file"`
	// InsecureSkipTLSVerify disables API server certificate verification. For
	// local/testing use only.
	InsecureSkipTLSVerify bool `toml:"insecure_skip_tls_verify"`
}

// HealthCheckConfig configures active health checking for an upstream pool.
// Probes run on a per-pool goroutine started when the pool is built and stopped
// on reload/shutdown. A backend is taken out of rotation after
// UnhealthyThreshold consecutive failed probes and returned after
// HealthyThreshold consecutive successful probes; this active signal combines
// with passive (live-traffic) health.
type HealthCheckConfig struct {
	Enabled bool `toml:"enabled"`
	// Type is the probe protocol: "http" (default) or "tcp".
	Type string `toml:"type"`
	// Path is the request path for HTTP probes (required for type "http").
	Path string `toml:"path"`
	// Interval is the delay between probe rounds (default 5s).
	Interval Duration `toml:"interval"`
	// Timeout bounds a single probe (default 2s); must be less than Interval.
	Timeout Duration `toml:"timeout"`
	// HealthyThreshold is the number of consecutive successes to mark a backend
	// healthy again (default 2).
	HealthyThreshold int `toml:"healthy_threshold"`
	// UnhealthyThreshold is the number of consecutive failures to take a backend
	// out of rotation (default 3).
	UnhealthyThreshold int `toml:"unhealthy_threshold"`
	// ExpectStatus lists acceptable HTTP status codes for a passing probe
	// (default [200]). Ignored for tcp probes.
	ExpectStatus []int `toml:"expect_status"`
	// ExpectBody, when set, requires the HTTP probe response body to contain this
	// substring. Ignored for tcp probes.
	ExpectBody string `toml:"expect_body"`
}

// UpstreamServer is one backend in a pool. It accepts either a bare address
// string ("127.0.0.1:3000") via UnmarshalTOML or a table with weight.
type UpstreamServer struct {
	Address string `toml:"address"`
	Weight  int    `toml:"weight"`
}

// TLSConfig configures TLS for a server block.
type TLSConfig struct {
	Enabled bool   `toml:"enabled"`
	Cert    string `toml:"cert"`
	Key     string `toml:"key"`
	// MinVersion is one of "1.2" or "1.3".
	MinVersion string `toml:"min_version"`
	// ACME, when enabled, obtains and renews certificates automatically from an
	// ACME certificate authority (e.g. Let's Encrypt) instead of loading a
	// static cert/key. It is mutually exclusive with cert/key in the same block.
	ACME *ACMEConfig `toml:"acme"`
	// ClientAuth, when set to a mode other than "none", enables mutual TLS:
	// client certificates are requested and verified against a CA bundle, and
	// the verified identity is exposed to proxy headers as $ssl_client_*
	// variables. A nil pointer (or "none" mode) leaves client authentication
	// off. Client-auth settings are applied when the listener binds, so changes
	// take effect on restart rather than hot reload.
	ClientAuth *ClientAuthConfig `toml:"client_auth"`
}

// ClientAuthConfig configures mutual TLS (client-certificate authentication)
// for a server block.
type ClientAuthConfig struct {
	// Mode selects enforcement at the TLS handshake:
	//   "none"    — off (the default).
	//   "request" — request a client certificate and verify it against ca_file
	//               if one is presented, but still admit connections without a
	//               certificate. Pair with a per-location require_client_cert to
	//               require certificates only on specific locations.
	//   "require" — reject the handshake unless the client presents a
	//               certificate that verifies against ca_file.
	Mode string `toml:"mode"`
	// CAFile is the PEM bundle of certificate authorities that client
	// certificates are verified against. Required unless Mode is "none".
	CAFile string `toml:"ca_file"`
	// VerifySAN, when non-empty, is an allow-list of subject alternative names
	// (DNS name, URI, email, or IP). A client certificate is rejected unless at
	// least one of its SANs matches an entry.
	VerifySAN []string `toml:"verify_san"`
	// CRLFile, when set, is a PEM- or DER-encoded certificate revocation list.
	// A client certificate whose serial number appears in it is rejected. The
	// CRL's signature is verified against ca_file.
	CRLFile string `toml:"crl_file"`
	// ForwardCertificate conveys the verified client certificate to backends
	// with the RFC 9440 Client-Cert header: "none" (default), "leaf", or
	// "chain" (which adds Client-Cert-Chain). Those headers are stripped from
	// every inbound request regardless of this setting, so a client can never
	// assert one; this only controls whether Jul emits its own.
	ForwardCertificate string `toml:"forward_certificate"`
}

// Active reports whether client-certificate authentication is enabled (a
// non-nil config with a mode other than "" or "none"). It is nil-safe and
// trims whitespace, matching validateClientAuth's and clientAuthMode's own
// trimming so a stray space in mode can never disagree with validation about
// whether client_auth is active.
func (c *ClientAuthConfig) Active() bool {
	if c == nil {
		return false
	}
	mode := strings.TrimSpace(c.Mode)
	return mode != "" && mode != "none"
}

// ACMEConfig configures automatic certificate management via the ACME protocol.
// Only the "http-01" and "tls-alpn-01" challenges are supported; "dns-01" is
// reserved for a future release and is rejected by validation today. The feature
// itself is compiled only into builds with the "acme" build tag; other builds
// reject an enabled block with a clear "not compiled in this build" error.
type ACMEConfig struct {
	Enabled bool `toml:"enabled"`
	// Email is the ACME account contact address (required when enabled).
	Email string `toml:"email"`
	// CA selects the directory: "letsencrypt", "letsencrypt-staging", or a
	// full ACME directory URL. Defaults to "letsencrypt-staging" so accidental
	// production rate-limit consumption is avoided until explicitly opted in.
	CA string `toml:"ca"`
	// Domains is the allow-list of hostnames to obtain certificates for.
	// Defaults to the server block's server_names.
	Domains []string `toml:"domains"`
	// Challenge selects the ACME challenge type: "http-01" (default) or
	// "tls-alpn-01". "dns-01" is reserved for a future release.
	Challenge string `toml:"challenge"`
	// DNSProvider names the DNS-01 provider plugin (e.g. "cloudflare"). It is a
	// reserved seam for a future DNS-01 release and is not implemented; setting it
	// is rejected by validation.
	DNSProvider string `toml:"dns_provider"`
	// CacheDir is where issued certificates and account keys are stored.
	// Defaults to "./jul-data/certs".
	CacheDir string `toml:"cache_dir"`
	// OCSPStapling enables OCSP stapling for ACME-issued certificates so clients
	// can verify revocation without a separate round-trip. It defaults to true;
	// set it to false to disable. Stapling degrades gracefully — a failed OCSP
	// fetch serves the certificate unstapled rather than breaking the handshake.
	OCSPStapling *bool `toml:"ocsp_stapling"`
}

// OCSPStaplingEnabled reports whether OCSP stapling should be active. It is on
// by default (a nil receiver or unset pointer), matching applyACMEDefaults,
// which materializes the pointer for an enabled block.
func (a *ACMEConfig) OCSPStaplingEnabled() bool {
	return a == nil || a.OCSPStapling == nil || *a.OCSPStapling
}

// HTTP3Config enables HTTP/3 over QUIC on a server block's listen address. The
// HTTP/3 listener runs on UDP at the same address as the TCP (HTTP/1.1 + HTTP/2)
// listener, shares its TLS certificates (including ACME), and is advertised to
// clients with an Alt-Svc response header so they upgrade on a later request.
// HTTP/3 requires TLS, so it may only be enabled on a TLS-enabled server block.
// The feature is compiled only into builds with the "http3" build tag; other
// builds reject an enabled block at startup with a clear error.
type HTTP3Config struct {
	Enabled bool `toml:"enabled"`
	// AltSvcMaxAge is the Alt-Svc advertisement lifetime in seconds (the "ma"
	// field), i.e. how long a client may keep using HTTP/3 before re-checking.
	// Defaults to 86400 (24h).
	AltSvcMaxAge int `toml:"alt_svc_max_age"`
}

// CompressionConfig configures negotiated response compression. The compression
// middleware selects the best encoder from the request's Accept-Encoding header.
// gzip is always available; brotli ("br") and zstd require their build tags and
// are reported as "not compiled in this build" at startup otherwise.
//
// Enabled semantics (auto-detect):
//   - nil (key absent): enabled when any other setting is non-zero; an empty
//     [compression] block or an absent block resolves to disabled.
//   - explicit true: always enabled regardless of other settings.
//   - explicit false: always disabled regardless of other settings.
//
// After config.Parse Enabled is always non-nil; callers should prefer
// IsEnabled() over dereferencing the pointer directly.
type CompressionConfig struct {
	Enabled *bool `toml:"enabled"`
	// Encoders lists allowed content codings in server-preference order, each
	// one of "gzip", "br", or "zstd". Defaults to ["gzip"] when enabled.
	Encoders []string `toml:"encoders"`
	// Level is the encoder compression level; 0 selects each encoder's default.
	// Values outside the public 0..11 contract are rejected during validation;
	// individual encoders may still defensively clamp internally.
	Level int `toml:"level"`
	// MinSize is the smallest response body that is compressed. Default 1k.
	MinSize Size `toml:"min_size"`
	// Types is the MIME allow-list matched against the response Content-Type.
	// A "type/*" entry matches a whole family. Defaults to a set covering text,
	// JSON, JavaScript, XML, SVG, and WASM.
	Types []string `toml:"types"`
	// Precompressed serves sidecar .gz/.br files for static responses when the
	// matching encoding is acceptable, avoiding on-the-fly recompression.
	Precompressed bool `toml:"precompressed"`
}

// IsEnabled reports whether compression is active. It honours explicit true/false
// values and returns false for a nil pointer (the zero value before Parse).
// Prefer this over dereferencing Enabled directly.
func (c CompressionConfig) IsEnabled() bool { return c.Enabled != nil && *c.Enabled }

// Bool returns a pointer to b. It is a convenience helper for constructing
// *bool config fields in struct literals and tests.
func Bool(b bool) *bool { return &b }

// Int returns a pointer to i, for the config fields where an absent key and an
// explicit zero are different answers.
func Int(i int) *int { return &i }

// RateLimitConfig configures request rate limiting (token bucket) plus a
// per-listener concurrent-connection cap. It applies globally and can be
// overridden per location (rate/burst/key only). Disabled by default.
type RateLimitConfig struct {
	Enabled bool `toml:"enabled"`
	// Key selects the bucket identity: "ip" (client address, the default),
	// "header:<Name>" (a request header value), or "jwt:<claim>" (a validated
	// JWT claim once auth is configured). Untrusted X-Forwarded-For is never
	// used implicitly; key on a header only behind a trusted proxy.
	Key string `toml:"key"`
	// Rate is the sustained requests/second allowed per key.
	Rate int `toml:"rate"`
	// Burst is the maximum momentary burst above Rate. Defaults to Rate.
	Burst int `toml:"burst"`
	// MaxConns caps concurrent connections per listener (0 = unlimited). Only
	// meaningful at global scope; ignored on a per-location override.
	MaxConns int `toml:"max_conns"`
}

// AuthConfig configures per-location access control. The configured methods are
// applied in a fixed order: CIDR allow/deny first (a network-level gate), then
// exactly one credential method (Basic, JWT, or forward-auth) if set. Auth is a
// modifier, not an action, so it composes with any location action. All fields
// are optional; an empty block authorizes every request.
type AuthConfig struct {
	// Allow and Deny are CIDR allow/deny lists evaluated before any credential
	// check. Deny wins over Allow. When Allow is non-empty, a client outside
	// every Allow range is rejected; Deny rejects matching clients outright.
	Allow []string `toml:"allow"`
	Deny  []string `toml:"deny"`

	// Basic enables HTTP Basic authentication against an htpasswd file.
	Basic *BasicAuthConfig `toml:"basic"`
	// JWT enables bearer-token validation against a JWKS endpoint.
	JWT *JWTAuthConfig `toml:"jwt"`
	// ForwardAuth delegates the decision to an external HTTP endpoint.
	ForwardAuth *ForwardAuthConfig `toml:"forward_auth"`
}

// BasicAuthConfig configures HTTP Basic authentication. Passwords are checked
// against an htpasswd file (bcrypt entries).
type BasicAuthConfig struct {
	// File is the path to an htpasswd file with bcrypt-hashed passwords.
	File string `toml:"file"`
	// Realm is the authentication realm presented in the challenge. Defaults to
	// "Restricted".
	Realm string `toml:"realm"`
}

// JWTAuthConfig configures JWT bearer-token validation. Signing keys are fetched
// from a JWKS endpoint and cached with periodic refresh.
type JWTAuthConfig struct {
	// JWKSURL is the JSON Web Key Set endpoint serving the issuer's public keys.
	JWKSURL string `toml:"jwks_url"`
	// Issuer, when set, must equal the token's "iss" claim.
	Issuer string `toml:"issuer"`
	// Audience, when set, must be present in the token's "aud" claim.
	Audience string `toml:"audience"`
	// Algorithms is the allow-list of accepted signing algorithms. It defaults
	// to the asymmetric set (RS256/384/512, ES256/384/512, PS256/384/512); the
	// insecure "none" algorithm is always rejected. Pinning algorithms prevents
	// algorithm-confusion attacks.
	Algorithms []string `toml:"algorithms"`
	// Timeout bounds one JWKS fetch. 0 keeps the 10s default this was hardcoded
	// to before it was configurable.
	Timeout Duration `toml:"timeout"`
}

// ForwardAuthConfig delegates the authentication decision to an external HTTP
// endpoint (like NGINX auth_request / Traefik ForwardAuth).
type ForwardAuthConfig struct {
	// URL receives a subrequest carrying the original request's method, path,
	// and headers. A 2xx response authorizes the request; any other status is
	// propagated to the client.
	URL string `toml:"url"`
	// AuthResponseHeaders lists response headers copied from the auth endpoint
	// onto the upstream request when the decision is allow.
	AuthResponseHeaders []string `toml:"auth_response_headers"`
	// Timeout bounds one forward-auth subrequest. 0 keeps the 10s default this
	// was hardcoded to before it was configurable.
	//
	// It is a ceiling on how long an unreachable auth service can hold a client
	// request open before it is denied — never a window in which the request is
	// let through.
	Timeout Duration `toml:"timeout"`
}

// CacheConfig configures the two-tier response cache.
type CacheConfig struct {
	Enabled bool `toml:"enabled"`
	// MemoryMaxSize is the in-memory tier cap.
	MemoryMaxSize Size `toml:"memory_max_size"`
	// DiskPath enables the disk overflow tier when non-empty.
	DiskPath string `toml:"disk_path"`
	// DiskMaxSize is the disk tier cap.
	DiskMaxSize Size `toml:"disk_max_size"`
	// DefaultTTL is applied when upstream gives no explicit freshness.
	DefaultTTL Duration `toml:"default_ttl"`
	// StaleWhileRevalidate serves stale entries for this grace period after
	// expiry while an async revalidation refreshes them.
	StaleWhileRevalidate Duration `toml:"stale_while_revalidate"`
	// StaleIfError extends the stale-serving window when a background
	// revalidation encounters an upstream error (5xx or timeout). The entry
	// remains servable for this additional duration from the point of failure,
	// shielding clients from backend outages.
	StaleIfError Duration `toml:"stale_if_error"`
}

// AdminConfig configures the separate admin/observability listener. It binds
// to loopback by default and must never be attached to the main listeners.
type AdminConfig struct {
	Enabled bool   `toml:"enabled"`
	Listen  string `toml:"listen"`
	// Token, when set, requires `Authorization: Bearer <token>`.
	Token string `toml:"token"`
	// RBAC, when enabled, replaces the single shared token with named principals,
	// predefined/custom roles, and per-route permission gates (Phase 3).
	RBAC AdminRBACConfig `toml:"rbac"`
	// Console toggles the web console dashboard at the admin root. It is a
	// pointer so an omitted value defaults to enabled (when admin is enabled)
	// while an explicit `console = false` serves only the basic config page.
	// The console UI is additionally gated by the `console` build tag.
	Console *bool `toml:"console"`
	// HistoryDir is the directory where the console snapshots the previous
	// configuration before each successful edit, enabling one-click rollback.
	// It defaults to "./jul-data/config-history" when admin is enabled.
	HistoryDir string `toml:"history_dir"`
	// HistoryKeep bounds how many configuration snapshots are retained; older
	// snapshots are pruned. It defaults to 50.
	HistoryKeep int `toml:"history_keep"`

	// RateLimitReadPerMin caps read (GET) admin/API requests per client per
	// minute. It defaults to 240 when admin is enabled; a non-positive value
	// disables read rate limiting. (Console v2 Milestone 1.6.)
	RateLimitReadPerMin int `toml:"rate_limit_read_per_min"`
	// RateLimitWritePerMin caps mutating (POST/PUT/DELETE) admin requests per
	// client per minute. It defaults to 60 when admin is enabled; a non-positive
	// value disables write rate limiting.
	RateLimitWritePerMin int `toml:"rate_limit_write_per_min"`
	// RateLimitApplyPerMin caps the high-impact config validate/diff/apply
	// endpoints per client per minute, separately and more strictly than other
	// mutations. It defaults to 30 when admin is enabled; a non-positive value
	// disables it.
	RateLimitApplyPerMin int `toml:"rate_limit_apply_per_min"`
	// MaxEventConns bounds concurrent /api/events SSE streams per client to
	// prevent resource exhaustion. It defaults to 4 when admin is enabled; a
	// non-positive value disables the connection cap.
	MaxEventConns int `toml:"max_event_conns"`

	// AuditLogFile, when set, enables a durable append-only audit sink: every
	// audit event is also written as one JSON object per line (JSONL) to this
	// file, in addition to the bounded in-memory ring buffer. This makes the
	// audit trail survive restarts and ring-buffer overwrite, for compliance and
	// incident review (P2-12). Empty disables the durable sink. The directory is
	// created if missing; a sink that cannot be opened or written is surfaced as
	// a degraded audit_sink in the runtime overview rather than silently dropped,
	// and never blocks request handling (P3-08 fail-loud).
	AuditLogFile string `toml:"audit_log_file"`
	// AuditLogRotateMaxMB is the size in megabytes at which the durable audit
	// sink rotates to a timestamped backup. Defaults to 100 when a durable sink
	// is configured. Only applies when AuditLogFile is set.
	AuditLogRotateMaxMB int `toml:"audit_log_rotate_max_mb"`
	// AuditLogRotateKeep bounds how many rotated audit backups are retained;
	// older backups are pruned. Defaults to 14 when a durable sink is configured.
	// Audit logs are intentionally not deleted by age (no MaxAge): retention is
	// governed only by size and backup count so a quiet system keeps its trail.
	AuditLogRotateKeep int `toml:"audit_log_rotate_keep"`

	// PluginUploadDir is the directory where uploaded .wasm files are stored
	// when operators use the Console Plugins panel upload feature. Defaults to
	// "./jul-data/plugins" when admin is enabled. The directory is created if
	// missing. Uploads are written atomically and set to owner-only (0o600).
	PluginUploadDir string `toml:"plugin_upload_dir"`
	// PluginUploadMaxSize caps the size of an uploaded .wasm file in megabytes.
	// Defaults to 32 when admin is enabled and upload is enabled.
	PluginUploadMaxSize int `toml:"plugin_upload_max_size"`
	// PluginUploadEnabled controls whether the admin console allows uploading
	// .wasm plugin modules. The parser always sets this to an explicit false when
	// the admin block is enabled and the key is absent, so upload is disabled by
	// default. An explicit true is required to allow uploads.
	PluginUploadEnabled *bool `toml:"plugin_upload_enabled"`

	// TLS terminates the admin listener with an operator-supplied certificate
	// and, optionally, requires or requests a client certificate (#336). Nil
	// means plaintext, the existing default.
	TLS *AdminTLSConfig `toml:"tls"`
}

// AdminTLSConfig configures TLS for the admin listener (#336). It reuses the
// data plane's CertProvider/DynamicCertProvider rotation seam (#100) rather
// than a second one: Cert/Key content and same-path rotation hot-apply,
// while Enabled is a structural transition that stays restart-required,
// matching servers.*.tls's own split. There is no ACME option here: an
// operator-supplied certificate is the bounded starting point.
type AdminTLSConfig struct {
	// Enabled terminates the admin listener with TLS instead of plaintext.
	// Turning it on or off changes the socket's protocol, a structural
	// transition applied only on restart.
	Enabled bool `toml:"enabled"`
	// Cert is the path to the PEM certificate file the admin listener
	// presents. Content and same-path rotation hot-apply.
	Cert string `toml:"cert"`
	// Key is the path to the PEM private key matching Cert. Content and
	// same-path rotation hot-apply.
	Key string `toml:"key"`
	// MinVersion is one of "1.2" or "1.3", defaulting like servers.*.tls.
	MinVersion string `toml:"min_version"`
	// ClientAuth optionally requires or requests a client certificate,
	// composing with — not replacing — the existing bearer-token/RBAC layer:
	// the handshake itself gates the connection, and every request that
	// reaches the handler still goes through the normal auth chokepoint. It
	// reuses servers.*.tls.client_auth's exact vocabulary and validation.
	// ForwardCertificate must stay "none": the admin API has no backend to
	// forward a client certificate to. Applied only on restart, like the
	// data plane's mutual TLS.
	ClientAuth *ClientAuthConfig `toml:"client_auth"`
}

// ConsoleEnabled reports whether the web console should be served: it defaults
// to true when unset and honors an explicit false. The `console` build tag
// still governs whether the console UI is compiled in.
func (a AdminConfig) ConsoleEnabled() bool { return a.Console == nil || *a.Console }

// AdminRBACConfig holds the opt-in named-principal, role-based access-control
// layer. When Enabled is false the server operates in legacy single-token mode.
type AdminRBACConfig struct {
	// Enabled activates named-principal RBAC. When false (default) the server
	// uses the legacy shared-token path and ignores Roles/Principals.
	Enabled bool `toml:"enabled"`
	// DefaultRole is the role assigned to the synthetic "shared" legacy principal
	// when RBAC is enabled alongside a legacy admin.token. Defaults to "admin".
	DefaultRole string `toml:"default_role"`
	// Roles defines custom roles available to principals. Predefined role names
	// (viewer/operator/admin/auditor) may not be redefined here.
	Roles []AdminRole `toml:"roles"`
	// Principals lists the named credentials. Each has exactly one token in
	// this phase; rotation is a future additive extension.
	Principals []AdminPrincipal `toml:"principals"`
}

// AdminRole defines a custom role and its permission set. Predefined role names
// are reserved; attempting to define them here is a validation error.
type AdminRole struct {
	// Name is the role identifier referenced by principals.
	Name string `toml:"name"`
	// Permissions is the list of permission strings. Wildcard "*" and
	// resource-scoped "<resource>:*" patterns are supported.
	Permissions []string `toml:"permissions"`
}

// AdminPrincipal defines a named identity with one bearer-token credential.
type AdminPrincipal struct {
	// Name is the human-readable identifier used in audit records.
	Name string `toml:"name"`
	// Role is the predefined or custom role name that governs this principal's
	// permission set.
	Role string `toml:"role"`
	// Token is the raw bearer token. ${env:}, ${file:}, and ${secret:}
	// references are resolved at startup/apply time. The plaintext value is
	// never logged, metricked, or returned by API projections.
	Token string `toml:"token"`
	// Disabled, when true, prevents this principal from authenticating even if
	// the correct token is supplied. Useful for emergency access revocation
	// without removing the config entry.
	Disabled bool `toml:"disabled"`
	// ExpiresAt, when non-zero, sets a hard expiry for this credential. After
	// this time the principal is treated as disabled.
	ExpiresAt time.Time `toml:"expires_at"`
}

// EgressConfig is the optional outbound-destination allow-list ([egress]). When
// Enabled, the server's config-driven auxiliary fetches (JWKS, forward-auth,
// service discovery) may only connect to destinations matching an Allow entry;
// every other destination is refused at dial time. When disabled (the default)
// no restriction is applied, so the block is fully backward-compatible.
type EgressConfig struct {
	// Enabled turns the allow-list on. Off by default.
	Enabled bool `toml:"enabled"`
	// Allow lists the permitted destinations. Each entry is one of:
	//   - a CIDR, e.g. "10.0.0.0/8" or "2001:db8::/32" (matches an IP)
	//   - a bare IP, e.g. "203.0.113.10" (treated as /32 or /128)
	//   - an exact hostname, e.g. "idp.example.com"
	//   - a leading-dot suffix, e.g. ".internal.corp" (matches any subdomain)
	// A hostname listed by name is trusted and resolved normally; a hostname not
	// listed by name is permitted only when every resolved IP is inside an
	// allowed CIDR.
	Allow []string `toml:"allow"`
}

// ObservabilityConfig groups distributed-tracing, metrics, and access-log sink
// settings under the [observability] table.
type ObservabilityConfig struct {
	Tracing   TracingConfig   `toml:"tracing"`
	Metrics   MetricsConfig   `toml:"metrics"`
	AccessLog AccessLogConfig `toml:"access_log"`
}

// MetricsConfig tunes the Prometheus metrics exposed at the admin /metrics
// endpoint, under the [observability.metrics] table.
type MetricsConfig struct {
	// HostLabel adds the request Host as the "host" label on
	// jul_http_requests_total and jul_http_request_duration_seconds. It is off
	// by default: the Host header is client-controlled, so enabling it on an
	// edge exposed to arbitrary Host values can drive unbounded metric
	// cardinality. Enable it only when the set of hosts is bounded (or pair it
	// with a scrape-time relabel/drop rule).
	HostLabel bool `toml:"host_label"`
}

// TracingConfig configures OpenTelemetry distributed tracing. Tracing is
// disabled by default and is only active in binaries built with the `otel`
// build tag; a binary without that tag rejects an enabled block at startup.
type TracingConfig struct {
	Enabled bool `toml:"enabled"`
	// Exporter selects the OTLP transport: "otlp-grpc" (default) or "otlp-http".
	Exporter string `toml:"exporter"`
	// Endpoint is the collector address: "host:port" for gRPC or a URL/host for
	// HTTP. Required when tracing is enabled.
	Endpoint string `toml:"endpoint"`
	// SampleRatio is the head-based sampling probability for root spans, in the
	// range [0,1]. It defaults to 1.0 (sample everything) when enabled; set a
	// fraction such as 0.1 to sample less.
	SampleRatio float64 `toml:"sample_ratio"`
	// ServiceName sets the OpenTelemetry resource service.name. Defaults to
	// "jul" when unset.
	ServiceName string `toml:"service_name"`
	// Insecure sends spans over plaintext instead of TLS. The exporter uses TLS
	// with the host's root CAs by default; set this for a local collector that
	// listens without TLS (for example localhost:4317).
	Insecure bool `toml:"insecure"`
}

// AccessLogConfig configures where the access log is written. By default the
// access log is emitted through the server's structured logger (the "stdout"
// sink, honoring [global].log_format). Additional sinks write a dedicated copy
// to a rotating file and/or the system log. Access-log settings are fixed at
// startup; changing them takes effect after a restart.
type AccessLogConfig struct {
	// Enabled controls whether Jul emits request access records. It is a pointer
	// so an omitted key preserves the v1 default (enabled), while an explicit
	// false disables stdout/file/syslog and the Console access-record tail.
	// Process, security, audit, health, metric, and trace output are independent.
	Enabled *bool `toml:"enabled"`
	// Sinks selects the active access-log destinations: any of "stdout" (the
	// server's structured logger), "file", and "syslog". Defaults to ["stdout"],
	// preserving the standard access line in the server log.
	Sinks []string `toml:"sinks"`
	// File is the path of the access-log file. Required when "file" is listed;
	// its parent directory is created if missing.
	File string `toml:"file"`
	// Format selects the encoding of the file and syslog sinks: "text" (logfmt,
	// the default) or "json". The stdout sink always follows [global].log_format.
	Format string `toml:"format"`
	// RotateMaxMB is the file size in megabytes at which the log is rotated.
	// Defaults to 100. Only affects the file sink.
	RotateMaxMB int `toml:"rotate_max_mb"`
	// RotateKeep is the maximum number of rotated files to retain. Defaults to 7.
	// Only affects the file sink.
	RotateKeep int `toml:"rotate_keep"`
}

// IsEnabled reports the effective access-record state. Access logging remains
// enabled when the key is omitted for backward compatibility; an explicit
// false is the only supported disable mechanism.
func (a AccessLogConfig) IsEnabled() bool { return a.Enabled == nil || *a.Enabled }
