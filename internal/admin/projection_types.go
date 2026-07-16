// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds all the Go struct types that form the Console v2 JSON API
// contract (projection types, DTO view structs, and RuntimeOverview). They were
// originally in projections.go; keeping them here makes projections.go a pure
// "builder functions" file that is easier to navigate.
//
// Do not add projection logic here. Add types only.

// ── Projection types (v2 API contract) ──────────────────────────────────────

// RouteProjection is a structured route for the Console v2 Routes panel.
type RouteProjection struct {
	Name        string               `json:"name,omitempty"`
	Listen      string               `json:"listen"`
	ServerNames []string             `json:"server_names,omitempty"`
	TLS         *TLSProjection       `json:"tls,omitempty"`
	HTTP3       bool                 `json:"http3"`
	H2C         bool                 `json:"h2c"`
	Locations   []LocationProjection `json:"locations"`
}

// LocationProjection is a structured location within a route.
type LocationProjection struct {
	Index       int    `json:"index"`
	Match       string `json:"match"`
	Type        string `json:"type"`   // exact, prefix, regex
	Action      string `json:"action"` // static, proxy, grpc, grpc_transcode, fastcgi, redirect, deny, return
	Target      string `json:"target,omitempty"`
	Auth        bool   `json:"auth"`
	Cache       bool   `json:"cache"`
	Compression bool   `json:"compression"`
	RateLimit   bool   `json:"rate_limit"`
	// RateLimitDetail carries the per-location rate-limit configuration (rate,
	// burst, key) when the location has a rate_limit block, so the guided editor
	// can seed the detailed rate-limit form and round-trip values faithfully.
	RateLimitDetail *RateLimitProjection `json:"rate_limit_detail,omitempty"`
	Secure          bool                 `json:"secure"` // TLS required
	// RequireClientCert reports the location's require_client_cert flag, so the
	// route editor can offer the per-route mutual-TLS toggle (Phase 4j). It takes
	// effect on hot reload (enforced per request), unlike the server-level
	// client_auth that binds with the listener.
	RequireClientCert bool `json:"require_client_cert"`
	// Upstream is the referenced upstream pool name when Action proxies to a
	// named upstream (proxy_pass http://<name>); empty for direct host:port.
	Upstream string `json:"upstream,omitempty"`
	// WAF is the location's own [waf] override state, present only when the
	// location defines one (otherwise it inherits the global policy). It lets the
	// route editor truthfully offer to add, edit, or remove a per-location WAF
	// override and show its current mode/CRS.
	WAF *LocationWAFState `json:"waf,omitempty"`
	// AuthDetail is the location's access-control rule, present only when the
	// location defines one. It lets the guided auth editor seed truthfully; it
	// carries no secrets (htpasswd path, JWKS URL, issuer/audience, and CIDR
	// lists are not credentials).
	AuthDetail *LocationAuthState `json:"auth_detail,omitempty"`
	// Warnings flags likely-misconfigurations the operator should see before
	// editing (e.g. cache toggled on but the global cache is disabled).
	Warnings []string `json:"warnings,omitempty"`
	// Transcode is the full transcoding configuration when Action is
	// "grpc_transcode". It seeds the quick-edit form and the "Edit in
	// designer" deep-edit flow.
	Transcode *TranscodeProjection `json:"transcode,omitempty"`
}

// LocationAuthState summarises a location's access-control rule for the guided
// auth editor. Method is the dominant credential method ("basic"/"jwt"/
// "forward") or "cidr" when only IP rules are set; the remaining fields seed the
// form. No secret values are included.
type LocationAuthState struct {
	Method      string   `json:"method"`
	Allow       []string `json:"allow,omitempty"`
	Deny        []string `json:"deny,omitempty"`
	BasicFile   string   `json:"basic_file,omitempty"`
	BasicRealm  string   `json:"basic_realm,omitempty"`
	JWTJWKSURL  string   `json:"jwt_jwks_url,omitempty"`
	JWTIssuer   string   `json:"jwt_issuer,omitempty"`
	JWTAudience string   `json:"jwt_audience,omitempty"`
	ForwardURL  string   `json:"forward_url,omitempty"`
}

// LocationWAFState summarises a location's own [waf] override for the route
// editor. It carries the full override the guided per-location editor controls
// (Phase 4e) — the basic knobs plus the advanced SecLang fields — so the editor
// can seed every field and round-trip it without clobbering unshown rules. It
// mirrors the policy fields of LocationWAFProjection without repeating the
// location coordinates (the route already provides them).
type LocationWAFState struct {
	Enabled           bool     `json:"enabled"`
	Mode              string   `json:"mode,omitempty"`
	CRSEnabled        bool     `json:"crs_enabled"`
	BlockStatus       int      `json:"block_status,omitempty"`
	Paranoia          int      `json:"paranoia,omitempty"`
	RequestBodyLimit  string   `json:"request_body_limit,omitempty"`
	ResponseBodyCheck bool     `json:"response_body_check"`
	DirectivesFiles   []string `json:"directives_files,omitempty"`
	InlineRules       string   `json:"inline_rules,omitempty"`
}

// AppProjection is a structured upstream/app for the Console v2 Apps panel.
type AppProjection struct {
	Name        string              `json:"name"`
	Strategy    string              `json:"strategy"`
	Backends    []BackendProjection `json:"backends"`
	HealthCheck bool                `json:"health_check"`
	Discovery   string              `json:"discovery,omitempty"`
	// Detail fields (Milestone 2.4). Zero values render as "not configured".
	MaxFails         int      `json:"max_fails,omitempty"`
	FailTimeout      string   `json:"fail_timeout,omitempty"`
	HealthCheckType  string   `json:"health_check_type,omitempty"`
	HealthCheckPath  string   `json:"health_check_path,omitempty"`
	HealthCheckIntvl string   `json:"health_check_interval,omitempty"`
	DiscoveryTarget  string   `json:"discovery_target,omitempty"`
	RoutesUsing      []string `json:"routes_using,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`

	// Guided-editor seed fields (Phase 4b). These expose the full, non-secret
	// health-check and discovery detail so the structured Apps editor round-trips
	// the current values without clobbering knobs it does not display. Secret
	// tokens are never projected — DiscoveryConsul/Kubernetes carry only a
	// has_token flag so the editor can show "token set" and preserve it.
	HealthCheckTimeout      string               `json:"health_check_timeout,omitempty"`
	HealthCheckHealthyThr   int                  `json:"health_check_healthy_threshold,omitempty"`
	HealthCheckUnhealthyThr int                  `json:"health_check_unhealthy_threshold,omitempty"`
	HealthCheckExpectStatus []int                `json:"health_check_expect_status,omitempty"`
	HealthCheckExpectBody   string               `json:"health_check_expect_body,omitempty"`
	DiscoveryRefresh        string               `json:"discovery_refresh,omitempty"`
	DiscoveryConsul         *ConsulDiscoveryView `json:"discovery_consul,omitempty"`
	DiscoveryKubernetes     *K8sDiscoveryView    `json:"discovery_kubernetes,omitempty"`
}

// ConsulDiscoveryView is the non-secret Consul discovery state the Apps editor
// seeds from. HasToken reports whether an ACL token is configured (the token
// itself is never projected).
type ConsulDiscoveryView struct {
	Address     string `json:"address,omitempty"`
	Service     string `json:"service,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Datacenter  string `json:"datacenter,omitempty"`
	PassingOnly *bool  `json:"passing_only,omitempty"`
	HasToken    bool   `json:"has_token,omitempty"`
}

// K8sDiscoveryView is the non-secret Kubernetes discovery state the Apps editor
// seeds from. HasToken reports whether a bearer token is configured (the token
// itself is never projected).
type K8sDiscoveryView struct {
	Namespace             string `json:"namespace,omitempty"`
	Service               string `json:"service,omitempty"`
	Port                  string `json:"port,omitempty"`
	APIServer             string `json:"api_server,omitempty"`
	CAFile                string `json:"ca_file,omitempty"`
	InsecureSkipTLSVerify bool   `json:"insecure_skip_tls_verify,omitempty"`
	HasToken              bool   `json:"has_token,omitempty"`
}

// BackendProjection is one backend server in an upstream pool. Healthy is a
// pointer so the console can distinguish three states: nil means health is
// unknown (no live status — e.g. health checks disabled or the pool not yet
// observed), while a non-nil value reports a known healthy/unhealthy result.
// Omitting the field for false would conflate "unhealthy" with "unknown".
type BackendProjection struct {
	Address  string `json:"address"`
	Weight   int    `json:"weight"`
	Healthy  *bool  `json:"healthy,omitempty"`
	Inflight int64  `json:"inflight,omitempty"`
}

// TLSProjection is certificate/TLS state for the TLS & Certificates panel.
type TLSProjection struct {
	Enabled    bool   `json:"enabled"`
	ACME       bool   `json:"acme"`
	ClientAuth string `json:"client_auth,omitempty"`
	MinVersion string `json:"min_version,omitempty"`
}

// CertProjection is one certificate entry for the TLS panel.
type CertProjection struct {
	ServerNames []string `json:"server_names"`
	Source      string   `json:"source"`
	Issuer      string   `json:"issuer,omitempty"`
	NotAfter    string   `json:"not_after,omitempty"`
	DaysLeft    int      `json:"days_left,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// SecurityProjection is the security posture for the Security panel.
type SecurityProjection struct {
	AuthEnabled      bool   `json:"auth_enabled"`
	ClientAuth       string `json:"client_auth,omitempty"`
	BodyLimit        string `json:"body_limit,omitempty"`
	RequireCertCount int    `json:"require_cert_count"`
	// WAFEnabled reports whether any location is protected by the web
	// application firewall (the global [waf] or a per-location override).
	WAFEnabled bool `json:"waf_enabled"`
	// WAFCompiled reports whether this binary includes the WAF engine (the waf
	// build tag). When false the apply preflight rejects an enabled WAF, so the
	// Security panel warns up front rather than letting the operator configure a
	// policy this build cannot enforce.
	WAFCompiled bool `json:"waf_compiled"`
	// WAFMode is the enforcement mode ("block" or "detect") of the first
	// protected location seen, kept for backward compatibility; the per-mode
	// distribution below is the authoritative summary.
	WAFMode string `json:"waf_mode,omitempty"`
	// WAFLocations is the number of locations the WAF protects.
	WAFLocations int `json:"waf_locations"`
	// WAFBlockLocs/WAFDetectLocs/WAFCRSLocs break the protected locations down by
	// effective enforcement mode and CRS coverage, so the Security panel reports
	// the real mix rather than implying every route shares the global mode. They
	// are computed by the same wafDistribution helper the Overview status row
	// uses, so the two views can never disagree.
	WAFBlockLocs  int `json:"waf_block_locs"`
	WAFDetectLocs int `json:"waf_detect_locs"`
	WAFCRSLocs    int `json:"waf_crs_locs"`
	// The global [waf] policy verbatim, so the guided WAF editor can seed from
	// the real configuration instead of clobbering fields it does not display
	// (CRS, paranoia, response-body inspection, rule files, inline rules) when
	// the operator saves. WAFGlobalEnabled distinguishes "global WAF is on" from
	// the location-level WAFEnabled above (a location may opt in while [waf] is
	// off, and vice versa).
	WAFGlobalEnabled     bool     `json:"waf_global_enabled"`
	WAFGlobalMode        string   `json:"waf_global_mode,omitempty"`
	WAFBlockStatus       int      `json:"waf_block_status,omitempty"`
	WAFCRSEnabled        bool     `json:"waf_crs_enabled"`
	WAFParanoia          int      `json:"waf_paranoia,omitempty"`
	WAFRequestBodyLimit  string   `json:"waf_request_body_limit,omitempty"`
	WAFResponseBodyCheck bool     `json:"waf_response_body_check"`
	WAFDirectivesFiles   []string `json:"waf_directives_files,omitempty"`
	WAFInlineRules       string   `json:"waf_inline_rules,omitempty"`
	// LocationWAFs lists the locations that define their own [waf] override.
	// Such an override REPLACES the global policy for that location wholesale, so
	// surfacing it is the truthful disclosure that the WAF is configured
	// per-location — the Security panel must not present the single global "Edit"
	// as if it governed every route. Empty when only the global policy applies.
	LocationWAFs []LocationWAFProjection `json:"location_wafs,omitempty"`
	// SecretRefs is the number of ${env:}/${file:} secret references in the
	// configuration. The values themselves are never projected.
	SecretRefs int `json:"secret_refs"`
}

// LocationWAFProjection describes one location whose [waf] override differs from
// the global policy. The identity fields mirror the structured-patch location
// selector (listen + server_names + match type/path) so a future guided editor
// can target the exact block; the policy fields summarise the override so the
// panel can show its mode and CRS state without exposing rule contents.
type LocationWAFProjection struct {
	Listen      string   `json:"listen"`
	ServerNames []string `json:"server_names,omitempty"`
	MatchType   string   `json:"match_type,omitempty"`
	Path        string   `json:"path,omitempty"`
	Enabled     bool     `json:"enabled"`
	Mode        string   `json:"mode,omitempty"`
	CRSEnabled  bool     `json:"crs_enabled"`
	// Advanced override detail (Phase 4e) so the guided editor seeds and
	// round-trips every field rather than clobbering unshown SecLang rules.
	BlockStatus       int      `json:"block_status,omitempty"`
	Paranoia          int      `json:"paranoia,omitempty"`
	RequestBodyLimit  string   `json:"request_body_limit,omitempty"`
	ResponseBodyCheck bool     `json:"response_body_check"`
	DirectivesFiles   []string `json:"directives_files,omitempty"`
	InlineRules       string   `json:"inline_rules,omitempty"`
}

// TrafficControlsProjection is the traffic/observability settings panel.
type TrafficControlsProjection struct {
	Compression *CompressionProjection `json:"compression,omitempty"`
	RateLimit   *RateLimitProjection   `json:"rate_limit,omitempty"`
	Cache       *CacheProjection       `json:"cache,omitempty"`
	Tracing     *TracingProjection     `json:"tracing,omitempty"`
}

// CompressionProjection is compression configuration.
type CompressionProjection struct {
	Enabled  bool     `json:"enabled"`
	Encoders []string `json:"encoders,omitempty"`
}

// RateLimitProjection is rate limiting configuration.
type RateLimitProjection struct {
	Enabled bool   `json:"enabled"`
	Rate    int    `json:"rate,omitempty"`
	Burst   int    `json:"burst,omitempty"`
	Key     string `json:"key,omitempty"`
}

// CacheProjection is cache configuration.
type CacheProjection struct {
	Enabled    bool   `json:"enabled"`
	DefaultTTL string `json:"default_ttl,omitempty"`
	MemoryMax  string `json:"memory_max,omitempty"`
	DiskPath   string `json:"disk_path,omitempty"`
}

// TracingProjection is the OpenTelemetry distributed-tracing configuration that
// seeds the guided tracing editor. It carries no secrets: the endpoint is a
// collector address, not a credential. Values reflect the effective config
// after defaults (exporter, service name, and full sampling) are applied.
type TracingProjection struct {
	Enabled     bool    `json:"enabled"`
	Exporter    string  `json:"exporter,omitempty"`
	Endpoint    string  `json:"endpoint,omitempty"`
	SampleRatio float64 `json:"sample_ratio,omitempty"`
	ServiceName string  `json:"service_name,omitempty"`
	Insecure    bool    `json:"insecure,omitempty"`
}

// CertRiskProjection surfaces certificate health for the Overview summary card.
// When there are no TLS server blocks it is omitted so the card stays hidden.
type CertRiskProjection struct {
	Count        int    `json:"count"`
	ExpiringSoon int    `json:"expiring_soon"`
	Expired      int    `json:"expired"`
	Errors       int    `json:"errors"`
	Details      string `json:"details,omitempty"`
}

// RuntimeOverview is the top-level dashboard summary.
type RuntimeOverview struct {
	Product string          `json:"product"`
	Version string          `json:"version"`
	Status  []FeatureStatus `json:"status"` // existing 21-row backbone
	Stats   interface{}     `json:"stats,omitempty"`
	// TrafficSources is the bounded top-N projection of request hosts/origins/
	// referers for the Console Overview Traffic Sources panel (Milestone 1.4).
	TrafficSources interface{} `json:"traffic_sources,omitempty"`
	// StreamStatus is the most recent L4 stream-proxy reload outcome: "ok",
	// "failed: <reason>", or empty when no stream is configured. Because stream
	// listeners reload asynchronously after the HTTP swap, the console surfaces
	// the outcome here (polled) rather than in the apply response.
	StreamStatus string `json:"stream_status,omitempty"`
	// AuditSink reports durable audit-trail health (P3-08). It is present only
	// when a durable audit sink is configured, so an operator can see at a glance
	// whether the compliance trail is actually being persisted; a degraded sink
	// (open or write failure) is surfaced here rather than silently dropped.
	AuditSink *AuditSinkStatus `json:"audit_sink,omitempty"`
	// CertRisk surfaces real certificate health (counts, expiry, errors) so the
	// Overview "Certificates" card is truthful rather than just reporting TLS
	// configuration presence. Omitted when no TLS server blocks are configured.
	CertRisk *CertRiskProjection `json:"cert_risk,omitempty"`
}
