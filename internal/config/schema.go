// Package config defines the configuration schema for the edge server and the
// machinery to load, validate, and hot-reload it.
//
// The schema mirrors common NGINX concepts (listen, server_name, location,
// proxy_pass, upstream, ...) but deliberately uses TOML rather than NGINX
// syntax. The [[stream]] table configures L4 (TCP/UDP) proxying and is active
// in builds with the "stream" tag; [[mail]] is reserved for a future version:
// it is parsed but rejected during validation so configs written today fail
// loudly rather than silently. The [plugins] table configures WASM plugins and
// is active in builds with the "wasmplugins" tag.
package config

// Config is the root configuration document.
type Config struct {
	Global        GlobalConfig        `toml:"global"`
	Servers       []ServerConfig      `toml:"servers"`
	Upstreams     []UpstreamConfig    `toml:"upstreams"`
	Cache         CacheConfig         `toml:"cache"`
	Admin         AdminConfig         `toml:"admin"`
	Compression   CompressionConfig   `toml:"compression"`
	RateLimit     RateLimitConfig     `toml:"rate_limit"`
	Observability ObservabilityConfig `toml:"observability"`

	// WAF is the global web-application-firewall policy ([waf]). It applies to
	// every location unless a location sets its own [servers.locations.waf]
	// override. It is enforced only in builds with the "waf" tag; a lean build
	// refuses any configuration that enables it (see internal/waf.Check).
	WAF WAFConfig `toml:"waf"`

	// Plugins declares WASM plugins by name ([plugins.NAME]). Locations and
	// servers reference them by name. Plugins are loaded only in builds with the
	// "wasmplugins" tag; a lean build refuses any config that declares them.
	Plugins map[string]PluginConfig `toml:"plugins"`

	// Streams declares L4 (TCP/UDP) reverse-proxy listeners ([[stream]]). They
	// are served only in builds with the "stream" tag; a lean build refuses any
	// config that declares them (see internal/stream.Check).
	Streams []StreamServer `toml:"stream"`

	// Reserved for future versions. Parsed so that presence can be detected
	// and rejected with a clear message during validation (see validate.go).
	Mail []map[string]any `toml:"mail"`
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
}

// MatchConfig selects requests for a location.
type MatchConfig struct {
	// Type is one of "exact", "prefix", or "regex".
	Type string `toml:"type"`
	Path string `toml:"path"`
}

// LocationConfig describes how to handle requests matching Match. Exactly one
// action (root/proxy_pass/fastcgi_pass/redirect/return) should be set.
type LocationConfig struct {
	Match MatchConfig `toml:"match"`

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

	// Passive health-check tuning.
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
}

// Active reports whether client-certificate authentication is enabled (a
// non-nil config with a mode other than "" or "none"). It is nil-safe.
func (c *ClientAuthConfig) Active() bool {
	if c == nil {
		return false
	}
	return c.Mode != "" && c.Mode != "none"
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
type CompressionConfig struct {
	Enabled bool `toml:"enabled"`
	// Encoders lists allowed content codings in server-preference order, each
	// one of "gzip", "br", or "zstd". Defaults to ["gzip"] when enabled.
	Encoders []string `toml:"encoders"`
	// Level is the encoder compression level; 0 selects each encoder's default.
	// Out-of-range values are clamped to the encoder's valid range.
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

// RateLimitConfig configures request rate limiting (token bucket) plus a
// per-listener concurrent-connection cap. It applies globally and can be
// overridden per location (rate/burst/key only). Disabled by default.
type RateLimitConfig struct {
	Enabled bool `tomm:"enabled"`
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
	// .wasm plugin modules. When nil (default) and admin is enabled, upload is
	// enabled. An explicit false disables the upload endpoint regardless of
	// PluginUploadMaxSize.
	PluginUploadEnabled *bool `toml:"plugin_upload_enabled"`
}

// ConsoleEnabled reports whether the web console should be served: it defaults
// to true when unset and honors an explicit false. The `console` build tag
// still governs whether the console UI is compiled in.
func (a AdminConfig) ConsoleEnabled() bool { return a.Console == nil || *a.Console }

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
