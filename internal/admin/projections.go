package admin

import (
	"fmt"
	"jul/internal/config"
	"math"
	"strconv"
	"strings"
	"time"
)

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
}

// ── Projection helpers ──────────────────────────────────────────────────────

func projectRoutes(c *config.Config) []RouteProjection {
	out := make([]RouteProjection, 0, len(c.Servers))
	for i := range c.Servers {
		srv := &c.Servers[i]
		rp := RouteProjection{
			Name:        srv.Name,
			Listen:      srv.Listen,
			ServerNames: srv.ServerNames,
			H2C:         srv.H2C,
			Locations:   make([]LocationProjection, 0, len(srv.Locations)),
		}
		if srv.HTTP3 != nil && srv.HTTP3.Enabled {
			rp.HTTP3 = true
		}
		if srv.TLS != nil && srv.TLS.Enabled {
			tls := &TLSProjection{Enabled: true, MinVersion: srv.TLS.MinVersion}
			tls.ACME = srv.TLS.ACME != nil && srv.TLS.ACME.Enabled
			if srv.TLS.ClientAuth != nil && srv.TLS.ClientAuth.Active() {
				tls.ClientAuth = srv.TLS.ClientAuth.Mode
			}
			rp.TLS = tls
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			lp := LocationProjection{
				Index:             j,
				Match:             loc.Match.Path,
				Type:              loc.Match.Type,
				Auth:              loc.Auth != nil,
				Cache:             loc.Cache,
				RateLimit:         loc.RateLimit != nil && loc.RateLimit.Enabled,
				Compression:       c.Compression.Enabled,
				Secure:            srv.TLS != nil && srv.TLS.Enabled,
				RequireClientCert: loc.RequireClientCert,
			}
			if loc.RateLimit != nil {
				lp.RateLimitDetail = &RateLimitProjection{
					Enabled: loc.RateLimit.Enabled,
					Rate:    loc.RateLimit.Rate,
					Burst:   loc.RateLimit.Burst,
					Key:     loc.RateLimit.Key,
				}
			}
			switch {
			case loc.GRPCTranscode != nil:
				lp.Action = "grpc_transcode"
				lp.Target = loc.GRPCTranscode.Target
				tc := &TranscodeProjection{
					DescriptorSet:      loc.GRPCTranscode.DescriptorSet,
					UseReflection:      loc.GRPCTranscode.UseReflection,
					TLS:                loc.GRPCTranscode.TLS,
					PreserveProtoNames: loc.GRPCTranscode.PreserveNames,
					Streaming:          loc.GRPCTranscode.Streaming,
					StreamMode:         loc.GRPCTranscode.StreamMode,
				}
				if loc.GRPCTranscode.MaxMessageSize != 0 {
					tc.MaxMessageSize = loc.GRPCTranscode.MaxMessageSize.String()
				}
				lp.Transcode = tc
			case loc.GRPC:
				lp.Action = "grpc"
				lp.Target = loc.ProxyPass
			case loc.ProxyPass != "":
				lp.Action = "proxy"
				lp.Target = loc.ProxyPass
			case loc.FastCGIPass != "":
				lp.Action = "fastcgi"
				lp.Target = loc.FastCGIPass
			case loc.UWSGIPass != "":
				lp.Action = "uwsgi"
				lp.Target = loc.UWSGIPass
			case loc.Redirect != "":
				lp.Action = "redirect"
				lp.Target = loc.Redirect
			case loc.Deny:
				lp.Action = "deny"
			case loc.Root != "":
				lp.Action = "static"
				lp.Target = loc.Root
			case loc.Return != 0:
				lp.Action = "return"
				lp.Target = strconv.Itoa(loc.Return)
			case loc.Plugin != "":
				lp.Action = "plugin"
				lp.Target = loc.Plugin
			default:
				lp.Action = "unknown"
			}
			lp.Upstream = upstreamRef(lp.Target, lp.Action)
			if loc.WAF != nil {
				lp.WAF = &LocationWAFState{
					Enabled:           loc.WAF.Enabled,
					Mode:              wafModeOrDefault(loc.WAF.Mode),
					CRSEnabled:        loc.WAF.CRSEnabled,
					BlockStatus:       loc.WAF.BlockStatus,
					Paranoia:          loc.WAF.Paranoia,
					RequestBodyLimit:  wafBodyLimitStr(loc.WAF.RequestBodyLimit),
					ResponseBodyCheck: loc.WAF.ResponseBodyCheck,
					DirectivesFiles:   loc.WAF.DirectivesFiles,
					InlineRules:       loc.WAF.InlineRules,
				}
			}
			if loc.Auth != nil {
				lp.AuthDetail = locationAuthState(loc.Auth)
			}
			lp.Warnings = locationWarnings(c, srv, loc, &lp)
			rp.Locations = append(rp.Locations, lp)
		}
		out = append(out, rp)
	}
	return out
}

// upstreamRef extracts the upstream pool name a proxied target references. A
// proxy_pass of "http://<name>" without a port and without a dotted host is
// treated as an upstream reference; concrete host:port targets return "".
func upstreamRef(target, action string) string {
	if action != "proxy" && action != "grpc" {
		return ""
	}
	host := target
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/"); i >= 0 {
		host = host[:i]
	}
	// A bare name (no port, no dot, not an IP) references an upstream pool.
	if host == "" || strings.Contains(host, ":") || strings.Contains(host, ".") {
		return ""
	}
	return host
}

// locationWarnings collects likely-misconfiguration notes for a location so the
// operator sees them before editing (Milestone 2.1 acceptance criteria).
func locationWarnings(c *config.Config, srv *config.ServerConfig, loc *config.LocationConfig, lp *LocationProjection) []string {
	var w []string
	if loc.Cache && !c.Cache.Enabled {
		w = append(w, "Cache is toggled on for this route, but the global [cache] block is disabled.")
	}
	if lp.RateLimit && !c.RateLimit.Enabled && (loc.RateLimit == nil || loc.RateLimit.Rate <= 0) {
		w = append(w, "Rate limiting is referenced but no rate is configured.")
	}
	if loc.RequireClientCert && (srv.TLS == nil || srv.TLS.ClientAuth == nil || !srv.TLS.ClientAuth.Active()) {
		w = append(w, "This route requires a client certificate, but the server block does not enable mutual TLS.")
	}
	if up := upstreamRef(lp.Target, lp.Action); up != "" {
		found := false
		for i := range c.Upstreams {
			if c.Upstreams[i].Name == up {
				found = true
				break
			}
		}
		if !found {
			w = append(w, "This route proxies to upstream \""+up+"\", which is not defined.")
		}
	}
	return w
}

func projectApps(c *config.Config, live map[string]UpstreamStatus) []AppProjection {
	routesByUpstream := routesUsingUpstreams(c)
	out := make([]AppProjection, 0, len(c.Upstreams))
	for i := range c.Upstreams {
		up := &c.Upstreams[i]
		ap := AppProjection{
			Name:        up.Name,
			Strategy:    up.Strategy,
			Backends:    make([]BackendProjection, 0, len(up.Servers)),
			MaxFails:    up.MaxFails,
			RoutesUsing: routesByUpstream[up.Name],
		}
		if up.FailTimeout > 0 {
			ap.FailTimeout = string(mustMarshal(up.FailTimeout.MarshalText()))
		}
		if up.HealthCheck != nil {
			ap.HealthCheck = up.HealthCheck.Enabled
			if up.HealthCheck.Enabled {
				ap.HealthCheckType = up.HealthCheck.Type
				ap.HealthCheckPath = up.HealthCheck.Path
				if up.HealthCheck.Interval > 0 {
					ap.HealthCheckIntvl = string(mustMarshal(up.HealthCheck.Interval.MarshalText()))
				}
				if up.HealthCheck.Timeout > 0 {
					ap.HealthCheckTimeout = string(mustMarshal(up.HealthCheck.Timeout.MarshalText()))
				}
				ap.HealthCheckHealthyThr = up.HealthCheck.HealthyThreshold
				ap.HealthCheckUnhealthyThr = up.HealthCheck.UnhealthyThreshold
				if len(up.HealthCheck.ExpectStatus) > 0 {
					ap.HealthCheckExpectStatus = append([]int(nil), up.HealthCheck.ExpectStatus...)
				}
				ap.HealthCheckExpectBody = up.HealthCheck.ExpectBody
			}
		}
		if up.Discovery != nil {
			ap.Discovery = up.Discovery.Type
			ap.DiscoveryTarget = up.Discovery.Target
			if up.Discovery.Refresh > 0 {
				ap.DiscoveryRefresh = string(mustMarshal(up.Discovery.Refresh.MarshalText()))
			}
			if up.Discovery.Consul != nil {
				cd := up.Discovery.Consul
				ap.DiscoveryConsul = &ConsulDiscoveryView{
					Address:     cd.Address,
					Service:     cd.Service,
					Tag:         cd.Tag,
					Datacenter:  cd.Datacenter,
					PassingOnly: cd.PassingOnly,
					HasToken:    strings.TrimSpace(cd.Token) != "",
				}
			}
			if up.Discovery.Kubernetes != nil {
				kd := up.Discovery.Kubernetes
				ap.DiscoveryKubernetes = &K8sDiscoveryView{
					Namespace:             kd.Namespace,
					Service:               kd.Service,
					Port:                  kd.Port,
					APIServer:             kd.APIServer,
					CAFile:                kd.CAFile,
					InsecureSkipTLSVerify: kd.InsecureSkipTLSVerify,
					HasToken:              strings.TrimSpace(kd.Token) != "",
				}
			}
		}
		livePool := live[up.Name]
		liveMap := make(map[string]BackendStatus, len(livePool.Backends))
		for _, b := range livePool.Backends {
			liveMap[b.Address] = b
		}
		for _, b := range up.Servers {
			bp := BackendProjection{Address: b.Address, Weight: b.Weight}
			if lb, ok := liveMap[b.Address]; ok {
				healthy := lb.Healthy
				bp.Healthy = &healthy
				bp.Inflight = lb.Inflight
			}
			ap.Backends = append(ap.Backends, bp)
		}
		ap.Warnings = appWarnings(up)
		out = append(out, ap)
	}
	return out
}

// routesUsingUpstreams maps each upstream pool name to the list of route
// match patterns that proxy to it, so the App detail view can show which
// routes depend on an app (Milestone 2.4 acceptance criterion).
func routesUsingUpstreams(c *config.Config) map[string][]string {
	out := map[string][]string{}
	for i := range c.Servers {
		srv := &c.Servers[i]
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			action := "proxy"
			if loc.GRPC {
				action = "grpc"
			}
			if name := upstreamRef(loc.ProxyPass, action); name != "" {
				label := srv.Listen + " " + loc.Match.Path
				out[name] = append(out[name], strings.TrimSpace(label))
			}
		}
	}
	return out
}

// appWarnings collects likely-misconfiguration notes for an upstream pool.
func appWarnings(up *config.UpstreamConfig) []string {
	var w []string
	hasDiscovery := up.Discovery != nil && up.Discovery.Type != "" && up.Discovery.Type != "static"
	if len(up.Servers) == 0 && !hasDiscovery {
		w = append(w, "This app has no backends and no discovery source configured.")
	}
	if up.HealthCheck != nil && up.HealthCheck.Enabled && up.HealthCheck.Type == "http" && up.HealthCheck.Path == "" {
		w = append(w, "HTTP health checks are enabled but no probe path is set.")
	}
	return w
}

func projectTLS(c *config.Config, live []CertStatus) []CertProjection {
	now := time.Now().UTC()
	liveMap := make(map[string]CertStatus, len(live))
	for _, cs := range live {
		for _, sn := range cs.ServerNames {
			if existing, ok := liveMap[sn]; !ok || existing.NotAfter.IsZero() {
				liveMap[sn] = cs
			}
		}
	}

	var certs []CertProjection
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		cp := CertProjection{
			ServerNames: srv.ServerNames,
		}
		if srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
			cp.Source = "acme"
		} else {
			cp.Source = "file"
		}
		// Merge live metadata when available.
		for _, sn := range srv.ServerNames {
			if l, ok := liveMap[sn]; ok {
				if cp.Issuer == "" {
					cp.Issuer = l.Issuer
				}
				if !l.NotAfter.IsZero() {
					cp.NotAfter = l.NotAfter.UTC().Format(time.RFC3339)
					cp.DaysLeft = int(math.Round(l.NotAfter.Sub(now).Hours() / 24))
				}
			}
		}
		certs = append(certs, cp)
	}
	return certs
}

func projectSecurity(c *config.Config, wafCompiled bool) SecurityProjection {
	sp := SecurityProjection{WAFCompiled: wafCompiled}
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.TLS != nil && srv.TLS.ClientAuth != nil && srv.TLS.ClientAuth.Mode != "" && srv.TLS.ClientAuth.Mode != "none" {
			sp.ClientAuth = srv.TLS.ClientAuth.Mode
		}
		if srv.ClientMaxBodySize > 0 {
			sp.BodyLimit = fmt.Sprintf("%d", srv.ClientMaxBodySize)
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			if loc.Auth != nil {
				sp.AuthEnabled = true
			}
			if loc.RequireClientCert {
				sp.RequireCertCount++
			}
			// A non-nil loc.WAF is a per-location override that replaces the
			// global policy for this location, so disclose it explicitly.
			if loc.WAF != nil {
				sp.LocationWAFs = append(sp.LocationWAFs, LocationWAFProjection{
					Listen:            srv.Listen,
					ServerNames:       srv.ServerNames,
					MatchType:         loc.Match.Type,
					Path:              loc.Match.Path,
					Enabled:           loc.WAF.Enabled,
					Mode:              wafModeOrDefault(loc.WAF.Mode),
					CRSEnabled:        loc.WAF.CRSEnabled,
					BlockStatus:       loc.WAF.BlockStatus,
					Paranoia:          loc.WAF.Paranoia,
					RequestBodyLimit:  wafBodyLimitStr(loc.WAF.RequestBodyLimit),
					ResponseBodyCheck: loc.WAF.ResponseBodyCheck,
					DirectivesFiles:   loc.WAF.DirectivesFiles,
					InlineRules:       loc.WAF.InlineRules,
				})
			}
		}
	}
	// WAF coverage across all locations, shared with runtimeStatus so the
	// Security panel and the Overview status row can never disagree.
	d := wafDistribution(c)
	sp.WAFEnabled = d.Locations > 0
	sp.WAFLocations = d.Locations
	sp.WAFMode = d.FirstMode
	sp.WAFBlockLocs = d.BlockLocs
	sp.WAFDetectLocs = d.DetectLocs
	sp.WAFCRSLocs = d.CRSLocs
	// The global [waf] policy verbatim, so the guided WAF editor seeds from the
	// real configuration instead of clobbering unshown fields on save.
	g := c.WAF
	sp.WAFGlobalEnabled = g.Enabled
	sp.WAFGlobalMode = g.Mode
	sp.WAFBlockStatus = g.BlockStatus
	sp.WAFCRSEnabled = g.CRSEnabled
	sp.WAFParanoia = g.Paranoia
	sp.WAFResponseBodyCheck = g.ResponseBodyCheck
	sp.WAFDirectivesFiles = g.DirectivesFiles
	sp.WAFInlineRules = g.InlineRules
	if g.RequestBodyLimit > 0 {
		if b, err := g.RequestBodyLimit.MarshalText(); err == nil {
			sp.WAFRequestBodyLimit = string(b)
		}
	}
	sp.SecretRefs = config.CountSecretRefs(c)
	return sp
}

// wafBodyLimitStr renders a WAF request-body limit as a size string (e.g.
// "128k"), or "" when zero so the guided editor shows the default rather than a
// literal 0. It mirrors the global [waf] projection's encoding.
func wafBodyLimitStr(s config.Size) string {
	if s.Bytes() == 0 {
		return ""
	}
	if b, err := s.MarshalText(); err == nil {
		return string(b)
	}
	return ""
}

// effectiveWAFPolicy returns the WAF policy that applies to a location: its own
// [waf] override when present, otherwise the global [waf] policy. It returns nil
// when no policy applies.
func effectiveWAFPolicy(c *config.Config, loc *config.LocationConfig) *config.WAFConfig {
	if loc.WAF != nil {
		return loc.WAF
	}
	if c.WAF.Enabled {
		return &c.WAF
	}
	return nil
}

// wafModeOrDefault returns the configured WAF mode, defaulting to "block".
func wafModeOrDefault(mode string) string {
	if mode == "" {
		return "block"
	}
	return mode
}

// locationAuthState derives the guided-auth seed from a location's AuthConfig.
// Method reflects the dominant credential ("basic"/"jwt"/"forward"); a rule with
// only IP allow/deny lists is reported as "cidr". It carries no secret values.
func locationAuthState(a *config.AuthConfig) *LocationAuthState {
	s := &LocationAuthState{Allow: a.Allow, Deny: a.Deny}
	switch {
	case a.Basic != nil:
		s.Method = "basic"
		s.BasicFile = a.Basic.File
		s.BasicRealm = a.Basic.Realm
	case a.JWT != nil:
		s.Method = "jwt"
		s.JWTJWKSURL = a.JWT.JWKSURL
		s.JWTIssuer = a.JWT.Issuer
		s.JWTAudience = a.JWT.Audience
	case a.ForwardAuth != nil:
		s.Method = "forward"
		s.ForwardURL = a.ForwardAuth.URL
	default:
		s.Method = "cidr"
	}
	return s
}

// wafDist summarizes how the effective WAF policy is applied across all
// locations: how many are protected and, of those, how many enforce (block) vs.
// only detect, plus how many load the CRS. FirstMode is the enforcement mode of
// the first protected location seen.
type wafDist struct {
	Locations  int
	BlockLocs  int
	DetectLocs int
	CRSLocs    int
	FirstMode  string
}

// wafDistribution computes the per-location WAF coverage. Both projectSecurity
// (Security panel) and runtimeStatus (Overview status row) resolve WAF coverage
// through this single helper so the two surfaces never report a different mix.
func wafDistribution(c *config.Config) wafDist {
	var d wafDist
	for i := range c.Servers {
		srv := &c.Servers[i]
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			w := effectiveWAFPolicy(c, loc)
			if w == nil || !w.Enabled {
				continue
			}
			d.Locations++
			mode := wafModeOrDefault(w.Mode)
			if d.FirstMode == "" {
				d.FirstMode = mode
			}
			if mode == "detect" {
				d.DetectLocs++
			} else {
				d.BlockLocs++
			}
			if w.CRSEnabled {
				d.CRSLocs++
			}
		}
	}
	return d
}

func projectTrafficControls(c *config.Config) TrafficControlsProjection {
	tcp := TrafficControlsProjection{}
	if c.Compression.Enabled {
		tcp.Compression = &CompressionProjection{}
		tcp.Compression.Enabled = true
		tcp.Compression.Encoders = c.Compression.Encoders
	}
	if c.RateLimit.Enabled {
		tcp.RateLimit = &RateLimitProjection{}
		tcp.RateLimit.Enabled = true
		tcp.RateLimit.Rate = c.RateLimit.Rate
		tcp.RateLimit.Burst = c.RateLimit.Burst
		tcp.RateLimit.Key = c.RateLimit.Key
	}
	if c.Cache.Enabled {
		tcp.Cache = &CacheProjection{}
		tcp.Cache.Enabled = true
		tcp.Cache.DefaultTTL = string(mustMarshal(c.Cache.DefaultTTL.MarshalText()))
		tcp.Cache.MemoryMax = string(mustMarshal(c.Cache.MemoryMaxSize.MarshalText()))
		tcp.Cache.DiskPath = c.Cache.DiskPath
	}
	if c.Observability.Tracing.Enabled {
		t := c.Observability.Tracing
		tcp.Tracing = &TracingProjection{
			Enabled:     true,
			Exporter:    t.Exporter,
			Endpoint:    t.Endpoint,
			SampleRatio: t.SampleRatio,
			ServiceName: t.ServiceName,
			Insecure:    t.Insecure,
		}
	}
	return tcp
}

func mustMarshal(b []byte, _ error) []byte { return b }
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// TranscodeProjection exposes the fields of a grpc_transcode location so the
// console can offer quick edits for target/descriptor/options and seed the
// full designer when the operator chooses deep editing.
type TranscodeProjection struct {
	DescriptorSet      string `json:"descriptor_set"`
	UseReflection      bool   `json:"use_reflection"`
	TLS                bool   `json:"tls"`
	PreserveProtoNames bool   `json:"preserve_proto_field_names"`
	Streaming          bool   `json:"streaming"`
	StreamMode         string `json:"stream_mode,omitempty"`
	MaxMessageSize     string `json:"max_message_size,omitempty"`
}
