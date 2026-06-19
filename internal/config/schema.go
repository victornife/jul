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
}

// ServerConfig is a virtual host bound to one listen address.
type ServerConfig struct {
	Listen      string           `toml:"listen"`
	ServerNames []string         `toml:"server_names"`
	Locations   []LocationConfig `toml:"locations"`
	TLS         *TLSConfig       `toml:"tls"`
	// HTTP3, when enabled, starts a parallel HTTP/3 (QUIC) listener on the same
	// address over UDP and advertises it via Alt-Svc. It requires TLS on this
	// server block and is compiled only into builds with the "http3" tag.
	HTTP3 *HTTP3Config `toml:"http3"`

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

	// GRPCTranscode, when set, turns this location into a gRPC-JSON transcoder:
	// it accepts REST/JSON requests, maps them to a unary gRPC method via
	// google.api.http annotations, calls the gRPC backend, and returns the reply
	// as JSON. It is an action (mutually exclusive with root/proxy_pass/etc.) and
	// requires a build with the "grpc" tag.
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
	// streaming method returns 501 Not Implemented as in the unary-only MVP.
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
}

// ACMEConfig configures automatic certificate management via the ACME protocol.
// The "http-01" and "tls-alpn-01" challenges are supported; "dns-01" requires a
// build with DNS provider support (the "acme_dns" tag) and is rejected by other
// builds with a clear error. The feature itself is compiled only into builds
// with the "acme" build tag; other builds reject an enabled block with a clear
// "not compiled in this build" error.
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
	// "tls-alpn-01". "dns-01" needs a build with DNS provider support.
	Challenge string `toml:"challenge"`
	// DNSProvider names the DNS-01 provider plugin (e.g. "cloudflare"). It is a
	// forward-looking seam: required by the "dns-01" challenge, which is only
	// available in builds compiled with DNS provider support. Ignored otherwise.
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
}

// ConsoleEnabled reports whether the web console should be served: it defaults
// to true when unset and honors an explicit false. The `console` build tag
// still governs whether the console UI is compiled in.
func (a AdminConfig) ConsoleEnabled() bool { return a.Console == nil || *a.Console }

// ObservabilityConfig groups distributed-tracing and access-log sink settings
// under the [observability] table.
type ObservabilityConfig struct {
	Tracing   TracingConfig   `toml:"tracing"`
	AccessLog AccessLogConfig `toml:"access_log"`
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
