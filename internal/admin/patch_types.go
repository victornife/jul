// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds the wire/DTO types for the structured configuration-patch API
// (the `/api/config/patch` surface). They are the JSON envelope and per-operation
// payloads decoded from the console; the mutation logic that consumes them lives
// in patch.go (applyPatch and friends). Keeping the transport shapes here — apart
// from the apply logic — keeps patch.go focused on behaviour, not schema.

// patchRequest is a structured, comment-free edit to the running configuration
// (Wave B). Rather than rewriting raw TOML (which risks mangling comments and
// formatting), each operation mutates the PARSED config model; the result is
// re-serialized and routed through the same validated SaveConfig path as the
// settings form, and the operator reviews the full generated diff before it is
// applied. This trades raw-comment preservation for safe, structured edits of
// the most common fields, exactly the P1-4 recommendation.
//
// Targets address an existing object:
//   - a route location by its server Listen + ServerNames set and the location's
//     Match type + Path (all four, so a patch is never silently applied to the
//     wrong vhost when listens repeat or the wrong location when paths repeat)
//   - an upstream pool by Name
//
// Exactly one operation is performed per request.
type patchRequest struct {
	Op string `json:"op"`

	// Route-location target (route_* ops). A location is addressed by its
	// server's Listen + ServerNames set and the location's Match type + Path.
	// ServerNames disambiguates name-based virtual hosts that share a listen;
	// MatchType disambiguates locations that share a path under different match
	// types (prefix/exact/regex). The console sends all of them from the route
	// projection so the target resolves to exactly one location.
	Listen      string   `json:"listen,omitempty"`
	ServerNames []string `json:"server_names,omitempty"`
	MatchType   string   `json:"match_type,omitempty"`
	Path        string   `json:"path,omitempty"`

	// Upstream target (upstream_* ops).
	Upstream string `json:"upstream,omitempty"`

	// Operation payloads (only the field relevant to Op is read).
	Target    string          `json:"target,omitempty"`     // route_set_target: new proxy_pass
	Enabled   *bool           `json:"enabled,omitempty"`    // route_toggle_cache / route_toggle_rate_limit / server_toggle_http3 / server_toggle_h2c / location_toggle_require_client_cert
	RateLimit *rateLimitPatch `json:"rate_limit,omitempty"` // route_set_rate_limit
	Address   string          `json:"address,omitempty"`    // upstream_add_backend / upstream_remove_backend
	Weight    int             `json:"weight,omitempty"`     // upstream_add_backend (defaults to 1)
	Strategy  string          `json:"strategy,omitempty"`   // upstream_set_strategy

	// server_set_limits payload. Each field is an optional string-typed size or
	// duration (e.g. "10m", "30s"); only non-empty fields are applied, so the
	// edit is sparse. An empty string leaves the existing value untouched.
	Limits *serverLimits `json:"limits,omitempty"`

	// location_waf_set payload: the per-location [waf] override knobs the guided
	// editor controls. location_waf_clear ignores it.
	WAF *locationWAF `json:"waf,omitempty"`

	// location_set_auth payload: the per-location access-control rule the guided
	// auth editor controls. location_clear_auth ignores it.
	Auth *locationAuth `json:"auth,omitempty"`

	// upstream_set_health_check payload: the pool's active health-check block.
	// nil/disabled removes the block (passive health only).
	HealthCheck *upstreamHealthCheck `json:"health_check,omitempty"`

	// upstream_set_discovery payload: the pool's dynamic discovery block. Type
	// "static"/"" removes it (the static Servers list is used instead).
	Discovery *upstreamDiscovery `json:"discovery,omitempty"`

	// route_rename payload: the server block's new host names (server_names).
	// An empty list renames the block to the catch-all (any host).
	NewServerNames []string `json:"new_server_names,omitempty"`

	// location_set_match payload: the route's new match (type + path). Changing
	// the match changes the route's identity, so the diff lists the old route
	// removed and the renamed route added.
	Match *locationMatch `json:"match_set,omitempty"`

	// location_set_action payload: the route's new action (proxy/static/
	// redirect/return/deny). The op clears every other action field first.
	Action *locationActionPayload `json:"action,omitempty"`

	// location_set_transcode payload: the route's grpc_transcode settings.
	// Only relevant when Op == "location_set_transcode".
	Transcode *transcodePatch `json:"transcode,omitempty"`

	// Plugin target. PluginName is the [plugins.NAME] key for plugin_set /
	// plugin_remove, and the plugin to attach/detach for
	// location_attach_plugin / location_detach_plugin (the location is
	// addressed by the route coordinates above).
	PluginName string `json:"plugin_name,omitempty"`

	// plugin_set payload: the plugin declaration to add or replace.
	PluginDef *pluginDef `json:"plugin,omitempty"`

	// Stream target (stream_set / stream_remove). A [[stream]] block is
	// addressed by its listen (reusing Listen above) and protocol; the protocol
	// defaults to tcp. stream_add has no target.
	StreamProtocol string `json:"stream_protocol,omitempty"`

	// stream_add / stream_set payload: the L4 listener to add or replace.
	Stream *streamDef `json:"stream,omitempty"`

	// server_set_client_auth payload: the server block's mutual-TLS (client
	// certificate) settings. A "none"/empty mode disables it. The server is
	// addressed by Listen + ServerNames above.
	ClientAuth *clientAuthDef `json:"client_auth,omitempty"`

	// compression_set payload: the global [compression] block. The op replaces
	// the block wholesale so the editor can toggle or reconfigure compression in
	// a single structured edit.
	Compression *compressionPatch `json:"compression,omitempty"`
}

// compressionPatch carries the global [compression] settings the guided editor
// controls. Replacing the block wholesale keeps the patch semantics simple and
// matches how the wizard emits a complete compression block.
type compressionPatch struct {
	Enabled       *bool    `json:"enabled,omitempty"`
	Encoders      []string `json:"encoders,omitempty"`
	Level         int      `json:"level,omitempty"`
	MinSize       string   `json:"min_size,omitempty"` // size string, e.g. "1k"
	Types         []string `json:"types,omitempty"`
	Precompressed bool     `json:"precompressed,omitempty"`
}

// locationMatch is the new match (type + path) for location_set_match. It
// replaces the location's Match in place — effectively renaming the route's
// matching pattern. Type is one of exact/prefix/regex (empty defaults to
// prefix); the validated re-parse rejects an invalid regex.
type locationMatch struct {
	Type string `json:"type,omitempty"`
	Path string `json:"path"`
}

// locationActionPayload is the new action for location_set_action. Kind selects
// which action the location performs; the op clears every other action field so
// exactly one remains, then sets the chosen one. It covers the actions the
// console edits structurally (proxy / gRPC / static / redirect / return / deny);
// richer actions (transcode, FastCGI/uWSGI, handler plugin) stay raw and the
// editor leaves them read-only.
type locationActionPayload struct {
	Kind   string `json:"kind"`             // proxy | grpc | static | redirect | return | deny
	Target string `json:"target,omitempty"` // proxy_pass / gRPC backend / root / redirect URL
	Status int    `json:"status,omitempty"` // return status, or optional redirect code
}

// transcodePatch carries the grpc_transcode fields the two-tier editor mutates
// in-place (location_set_transcode). It does NOT carry the full descriptor —
// that is uploaded separately — only the configuration knobs that the
// RouteDetail quick-edit form and the designer both surface.
type transcodePatch struct {
	Target         string `json:"target,omitempty"`
	DescriptorPath string `json:"descriptor_path,omitempty"`
	UseReflection  bool   `json:"use_reflection,omitempty"`
	TLS            bool   `json:"tls,omitempty"`
	PreserveNames  bool   `json:"preserve_names,omitempty"`
	Streaming      bool   `json:"streaming,omitempty"`
	StreamMode     string `json:"stream_mode,omitempty"`
	MaxMessageSize string `json:"max_message_size,omitempty"` // size string, e.g. "4m"
}

// locationWAF carries the per-location WAF override fields the guided editor
// exposes. As of Phase 4e the editor surfaces the full override — the three
// basic knobs (enabled, mode, CRS) plus the advanced SecLang fields (block
// status, paranoia, request-body limit, response-body inspection, rule files,
// and inline rules). location_waf_set therefore REPLACES the override from this
// payload wholesale; the editor seeds every field from the security projection
// first, so a round-trip is faithful rather than clobbering unshown rules.
type locationWAF struct {
	Enabled           bool     `json:"enabled"`
	Mode              string   `json:"mode,omitempty"`        // "block" (default) or "detect"
	CRSEnabled        bool     `json:"crs_enabled,omitempty"` // load the embedded OWASP CRS
	BlockStatus       int      `json:"block_status,omitempty"`
	Paranoia          int      `json:"paranoia,omitempty"`
	RequestBodyLimit  string   `json:"request_body_limit,omitempty"` // size string, e.g. "128k"
	ResponseBodyCheck bool     `json:"response_body_check,omitempty"`
	DirectivesFiles   []string `json:"directives_files,omitempty"`
	InlineRules       string   `json:"inline_rules,omitempty"`
}

// rateLimitPatch carries the per-location rate-limit fields the guided editor
// controls. The patch replaces the location's rate_limit wholesale (it does not
// merge), which matches how the WAF override works.
type rateLimitPatch struct {
	Enabled bool   `json:"enabled"`
	Rate    int    `json:"rate,omitempty"`
	Burst   int    `json:"burst,omitempty"`
	Key     string `json:"key,omitempty"`
}

// locationAuth carries the per-location access-control fields the guided auth
// editor controls. Like the route-creation form, exactly one Method is chosen:
// "cidr" (IP allow/deny), "basic" (htpasswd), "jwt" (JWKS), or "forward"
// (forward-auth). location_set_auth builds a fresh *config.AuthConfig from the
// method's fields and replaces the location's auth wholesale; the editor warns
// before discarding a combination it cannot represent (e.g. IP rules plus a
// credential method on the same location).
type locationAuth struct {
	Method      string   `json:"method"` // cidr | basic | jwt | forward
	Allow       []string `json:"allow,omitempty"`
	Deny        []string `json:"deny,omitempty"`
	BasicFile   string   `json:"basic_file,omitempty"`
	BasicRealm  string   `json:"basic_realm,omitempty"`
	JWTJWKSURL  string   `json:"jwt_jwks_url,omitempty"`
	JWTIssuer   string   `json:"jwt_issuer,omitempty"`
	JWTAudience string   `json:"jwt_audience,omitempty"`
	ForwardURL  string   `json:"forward_url,omitempty"`
}

// serverLimits carries the per-server limit/timeout fields the editor can set.
type serverLimits struct {
	ClientMaxBodySize string `json:"client_max_body_size,omitempty"`
	ReadHeaderTimeout string `json:"read_header_timeout,omitempty"`
	ReadTimeout       string `json:"read_timeout,omitempty"`
	WriteTimeout      string `json:"write_timeout,omitempty"`
	IdleTimeout       string `json:"idle_timeout,omitempty"`
	MaxHeaderBytes    string `json:"max_header_bytes,omitempty"`
}

// upstreamHealthCheck carries the active health-check fields the guided Apps
// editor controls. It maps 1:1 to config.HealthCheckConfig; durations are
// strings (e.g. "5s") parsed on apply. Empty/zero fields are left for the
// re-parse defaulting (interval 5s, timeout 2s, thresholds 2/3, expect [200]),
// and the validated SaveConfig path rejects an inconsistent combination (e.g.
// timeout >= interval, or http with no path).
type upstreamHealthCheck struct {
	Enabled            bool   `json:"enabled"`
	Type               string `json:"type,omitempty"` // "http" (default) or "tcp"
	Path               string `json:"path,omitempty"`
	Interval           string `json:"interval,omitempty"`
	Timeout            string `json:"timeout,omitempty"`
	HealthyThreshold   int    `json:"healthy_threshold,omitempty"`
	UnhealthyThreshold int    `json:"unhealthy_threshold,omitempty"`
	ExpectStatus       []int  `json:"expect_status,omitempty"`
	ExpectBody         string `json:"expect_body,omitempty"`
}

// upstreamDiscovery carries the dynamic-discovery fields the guided Apps editor
// controls. Secret tokens are intentionally NOT carried on the wire: when the
// edit keeps the same provider type, upstream_set_discovery preserves the
// existing Consul/Kubernetes token rather than clobbering it.
type upstreamDiscovery struct {
	Type       string                 `json:"type"` // static | dns | dns_srv | consul | kubernetes
	Target     string                 `json:"target,omitempty"`
	Refresh    string                 `json:"refresh,omitempty"`
	Consul     *consulDiscoveryFields `json:"consul,omitempty"`
	Kubernetes *k8sDiscoveryFields    `json:"kubernetes,omitempty"`
}

// consulDiscoveryFields are the non-secret Consul discovery knobs (the ACL
// token is preserved server-side, never sent to or from the console).
type consulDiscoveryFields struct {
	Address     string `json:"address,omitempty"`
	Service     string `json:"service,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Datacenter  string `json:"datacenter,omitempty"`
	PassingOnly *bool  `json:"passing_only,omitempty"`
}

// k8sDiscoveryFields are the non-secret Kubernetes discovery knobs (the bearer
// token is preserved server-side, never sent to or from the console).
type k8sDiscoveryFields struct {
	Namespace             string `json:"namespace,omitempty"`
	Service               string `json:"service,omitempty"`
	Port                  string `json:"port,omitempty"`
	APIServer             string `json:"api_server,omitempty"`
	CAFile                string `json:"ca_file,omitempty"`
	InsecureSkipTLSVerify bool   `json:"insecure_skip_tls_verify,omitempty"`
}
