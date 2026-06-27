package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"jul/internal/config"
)

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
	Target   string `json:"target,omitempty"`   // route_set_target: new proxy_pass
	Enabled  *bool  `json:"enabled,omitempty"`  // route_toggle_cache / route_toggle_rate_limit / server_toggle_http3 / server_toggle_h2c
	Address  string `json:"address,omitempty"`  // upstream_add_backend / upstream_remove_backend
	Weight   int    `json:"weight,omitempty"`   // upstream_add_backend (defaults to 1)
	Strategy string `json:"strategy,omitempty"` // upstream_set_strategy

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

// applyPatch mutates c in place according to req, returning a human-readable
// description of the change for the audit log, or an error when the target is
// not found or the operation is unknown.
func applyPatch(c *config.Config, req patchRequest) (string, error) {
	switch req.Op {
	case "route_set_target":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(req.Target) == "" {
			return "", fmt.Errorf("route_set_target: target is required")
		}
		loc.ProxyPass = req.Target
		return fmt.Sprintf("route %s%s proxy_pass set to %s", req.Listen, req.Path, req.Target), nil

	case "route_toggle_cache":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("route_toggle_cache: enabled is required")
		}
		loc.Cache = *req.Enabled
		return fmt.Sprintf("route %s%s cache %s", req.Listen, req.Path, onOff(*req.Enabled)), nil

	case "route_toggle_rate_limit":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("route_toggle_rate_limit: enabled is required")
		}
		if *req.Enabled {
			if loc.RateLimit == nil {
				loc.RateLimit = &config.RateLimitConfig{}
			}
			loc.RateLimit.Enabled = true
		} else if loc.RateLimit != nil {
			loc.RateLimit.Enabled = false
		}
		return fmt.Sprintf("route %s%s rate limit %s", req.Listen, req.Path, onOff(*req.Enabled)), nil

	case "location_waf_set":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.WAF == nil {
			return "", fmt.Errorf("location_waf_set: waf payload is required")
		}
		mode := strings.TrimSpace(req.WAF.Mode)
		if mode == "" {
			mode = "block"
		}
		if mode != "block" && mode != "detect" {
			return "", fmt.Errorf("location_waf_set: mode must be %q or %q", "block", "detect")
		}
		// The override REPLACES the global policy for this location wholesale (it
		// is not merged), which is exactly the semantics the security panel
		// discloses. As of Phase 4e the guided editor surfaces every override
		// field and seeds them from the projection, so building a fresh
		// config.WAFConfig from the full payload round-trips faithfully rather
		// than clobbering unshown rules. Defaults (block_status 403, body limit
		// 128 KiB, CRS paranoia) are applied by the parser on re-parse.
		var bodyLimit config.Size
		if raw := strings.TrimSpace(req.WAF.RequestBodyLimit); raw != "" {
			if err := bodyLimit.UnmarshalText([]byte(raw)); err != nil {
				return "", fmt.Errorf("location_waf_set: request_body_limit: %w", err)
			}
		}
		loc.WAF = &config.WAFConfig{
			Enabled:           req.WAF.Enabled,
			Mode:              mode,
			BlockStatus:       req.WAF.BlockStatus,
			DirectivesFiles:   trimNonEmpty(req.WAF.DirectivesFiles),
			InlineRules:       strings.TrimSpace(req.WAF.InlineRules),
			CRSEnabled:        req.WAF.CRSEnabled,
			Paranoia:          req.WAF.Paranoia,
			RequestBodyLimit:  bodyLimit,
			ResponseBodyCheck: req.WAF.ResponseBodyCheck,
		}
		return fmt.Sprintf("route %s%s WAF override set (%s%s)", req.Listen, req.Path,
			onOff(req.WAF.Enabled), wafModeNote(req.WAF.Enabled, mode, req.WAF.CRSEnabled)), nil

	case "location_waf_clear":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if loc.WAF == nil {
			return "", fmt.Errorf("route %s%s has no WAF override to clear", req.Listen, req.Path)
		}
		loc.WAF = nil
		return fmt.Sprintf("route %s%s WAF override cleared (inherits the global [waf])", req.Listen, req.Path), nil

	case "location_set_auth":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Auth == nil {
			return "", fmt.Errorf("location_set_auth: auth payload is required")
		}
		ac, summary, err := buildLocationAuth(*req.Auth)
		if err != nil {
			return "", err
		}
		loc.Auth = ac
		return fmt.Sprintf("route %s%s auth set (%s)", req.Listen, req.Path, summary), nil

	case "location_clear_auth":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if loc.Auth == nil {
			return "", fmt.Errorf("route %s%s has no auth rule to clear", req.Listen, req.Path)
		}
		loc.Auth = nil
		return fmt.Sprintf("route %s%s auth cleared", req.Listen, req.Path), nil

	case "upstream_add_backend":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		addr := strings.TrimSpace(req.Address)
		if addr == "" {
			return "", fmt.Errorf("upstream_add_backend: address is required")
		}
		for _, s := range up.Servers {
			if s.Address == addr {
				return "", fmt.Errorf("upstream %q already has backend %q", req.Upstream, addr)
			}
		}
		weight := req.Weight
		if weight < 1 {
			weight = 1
		}
		up.Servers = append(up.Servers, config.UpstreamServer{Address: addr, Weight: weight})
		return fmt.Sprintf("upstream %s added backend %s (weight %d)", req.Upstream, addr, weight), nil

	case "upstream_remove_backend":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		addr := strings.TrimSpace(req.Address)
		idx := -1
		for i, s := range up.Servers {
			if s.Address == addr {
				idx = i
				break
			}
		}
		if idx < 0 {
			return "", fmt.Errorf("upstream %q has no backend %q", req.Upstream, addr)
		}
		if len(up.Servers) == 1 {
			return "", fmt.Errorf("cannot remove the last backend of upstream %q", req.Upstream)
		}
		up.Servers = append(up.Servers[:idx], up.Servers[idx+1:]...)
		return fmt.Sprintf("upstream %s removed backend %s", req.Upstream, addr), nil

	case "upstream_set_strategy":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		strat := strings.TrimSpace(req.Strategy)
		switch strat {
		case "", "round_robin", "weighted_round_robin", "least_conn":
		default:
			return "", fmt.Errorf("upstream_set_strategy: invalid strategy %q (want round_robin|weighted_round_robin|least_conn)", strat)
		}
		up.Strategy = strat
		return fmt.Sprintf("upstream %s strategy set to %s", req.Upstream, orDefault(strat, "round_robin")), nil

	case "upstream_set_health_check":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		if req.HealthCheck == nil {
			return "", fmt.Errorf("upstream_set_health_check: health_check payload is required")
		}
		hc, summary, err := buildHealthCheck(*req.HealthCheck)
		if err != nil {
			return "", err
		}
		up.HealthCheck = hc
		return fmt.Sprintf("upstream %s active health checks %s", req.Upstream, summary), nil

	case "upstream_set_discovery":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		if req.Discovery == nil {
			return "", fmt.Errorf("upstream_set_discovery: discovery payload is required")
		}
		disc, summary, err := buildDiscovery(*req.Discovery, up.Discovery)
		if err != nil {
			return "", err
		}
		up.Discovery = disc
		return fmt.Sprintf("upstream %s discovery %s", req.Upstream, summary), nil

	case "server_set_limits":
		return applyServerLimits(c, req)

	case "server_toggle_http3":
		srv, err := findServer(c, req.Listen)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("server_toggle_http3: enabled is required")
		}
		if *req.Enabled {
			// HTTP/3 shares the block's TLS certificates, so it can only run on a
			// TLS-enabled listener. The validated apply path also rejects this (and
			// an enabled block in a build without the "http3" tag); the near-side
			// check just gives a clearer message before the diff is generated.
			if srv.TLS == nil || !srv.TLS.Enabled {
				return "", fmt.Errorf("server_toggle_http3: HTTP/3 requires TLS on server %s — enable TLS first", req.Listen)
			}
			if srv.HTTP3 == nil {
				srv.HTTP3 = &config.HTTP3Config{}
			}
			srv.HTTP3.Enabled = true
		} else {
			// Disabling removes the block entirely so the serialized config stays
			// clean (rather than leaving an inert [http3] enabled = false).
			srv.HTTP3 = nil
		}
		return fmt.Sprintf("server %s HTTP/3 %s", req.Listen, onOff(*req.Enabled)), nil

	case "server_toggle_h2c":
		srv, err := findServer(c, req.Listen)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("server_toggle_h2c: enabled is required")
		}
		// h2c is cleartext HTTP/2: it only applies to a plaintext listener, since a
		// TLS listener already negotiates HTTP/2 via ALPN.
		if *req.Enabled && srv.TLS != nil && srv.TLS.Enabled {
			return "", fmt.Errorf("server_toggle_h2c: h2c applies only to a plaintext listener; server %s already negotiates HTTP/2 over TLS", req.Listen)
		}
		srv.H2C = *req.Enabled
		return fmt.Sprintf("server %s h2c %s", req.Listen, onOff(*req.Enabled)), nil

	default:
		return "", fmt.Errorf("unknown patch op %q", req.Op)
	}
}

// applyServerLimits sets the per-server limit/timeout fields on the server block
// addressed by Listen. Only non-empty fields in req.Limits are applied, so the
// edit is sparse; each value is parsed through its config type so a malformed
// size/duration is rejected (without mutating) rather than silently dropped.
func applyServerLimits(c *config.Config, req patchRequest) (string, error) {
	if req.Limits == nil {
		return "", fmt.Errorf("server_set_limits: limits payload is required")
	}
	srv, err := findServer(c, req.Listen)
	if err != nil {
		return "", err
	}
	applied := make([]string, 0, 6)
	setSize := func(name, val string, dst *config.Size) error {
		if strings.TrimSpace(val) == "" {
			return nil
		}
		var s config.Size
		if err := s.UnmarshalText([]byte(val)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = s
		applied = append(applied, fmt.Sprintf("%s=%s", name, val))
		return nil
	}
	setDur := func(name, val string, dst *config.Duration) error {
		if strings.TrimSpace(val) == "" {
			return nil
		}
		var d config.Duration
		if err := d.UnmarshalText([]byte(val)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = d
		applied = append(applied, fmt.Sprintf("%s=%s", name, val))
		return nil
	}
	if err := setSize("client_max_body_size", req.Limits.ClientMaxBodySize, &srv.ClientMaxBodySize); err != nil {
		return "", err
	}
	if err := setDur("read_header_timeout", req.Limits.ReadHeaderTimeout, &srv.ReadHeaderTimeout); err != nil {
		return "", err
	}
	if err := setDur("read_timeout", req.Limits.ReadTimeout, &srv.ReadTimeout); err != nil {
		return "", err
	}
	if err := setDur("write_timeout", req.Limits.WriteTimeout, &srv.WriteTimeout); err != nil {
		return "", err
	}
	if err := setDur("idle_timeout", req.Limits.IdleTimeout, &srv.IdleTimeout); err != nil {
		return "", err
	}
	if err := setSize("max_header_bytes", req.Limits.MaxHeaderBytes, &srv.MaxHeaderBytes); err != nil {
		return "", err
	}
	if len(applied) == 0 {
		return "", fmt.Errorf("server_set_limits: no limit fields provided")
	}
	return fmt.Sprintf("server %s limits updated (%s)", req.Listen, strings.Join(applied, ", ")), nil
}

// findServer returns a pointer to the first server block bound to listen.
func findServer(c *config.Config, listen string) (*config.ServerConfig, error) {
	if strings.TrimSpace(listen) == "" {
		return nil, fmt.Errorf("server target requires a listen address")
	}
	for i := range c.Servers {
		if c.Servers[i].Listen == listen {
			return &c.Servers[i], nil
		}
	}
	return nil, fmt.Errorf("no server found for listen %q", listen)
}

// buildLocationAuth converts the guided auth payload into a *config.AuthConfig
// for exactly one method, mirroring the route-creation form. It returns a short
// human label for the audit summary, and rejects a method whose required fields
// are missing rather than persisting an inert auth block.
func buildLocationAuth(a locationAuth) (*config.AuthConfig, string, error) {
	switch strings.TrimSpace(a.Method) {
	case "cidr":
		allow := trimNonEmpty(a.Allow)
		deny := trimNonEmpty(a.Deny)
		if len(allow) == 0 && len(deny) == 0 {
			return nil, "", fmt.Errorf("location_set_auth: the cidr method needs at least one allow or deny entry")
		}
		return &config.AuthConfig{Allow: allow, Deny: deny}, "IP allow/deny", nil
	case "basic":
		if strings.TrimSpace(a.BasicFile) == "" {
			return nil, "", fmt.Errorf("location_set_auth: the basic method needs an htpasswd file")
		}
		return &config.AuthConfig{Basic: &config.BasicAuthConfig{
			File:  strings.TrimSpace(a.BasicFile),
			Realm: strings.TrimSpace(a.BasicRealm),
		}}, "HTTP Basic", nil
	case "jwt":
		if strings.TrimSpace(a.JWTJWKSURL) == "" {
			return nil, "", fmt.Errorf("location_set_auth: the jwt method needs a jwks_url")
		}
		return &config.AuthConfig{JWT: &config.JWTAuthConfig{
			JWKSURL:  strings.TrimSpace(a.JWTJWKSURL),
			Issuer:   strings.TrimSpace(a.JWTIssuer),
			Audience: strings.TrimSpace(a.JWTAudience),
		}}, "JWT", nil
	case "forward":
		if strings.TrimSpace(a.ForwardURL) == "" {
			return nil, "", fmt.Errorf("location_set_auth: the forward method needs a url")
		}
		return &config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{
			URL: strings.TrimSpace(a.ForwardURL),
		}}, "forward-auth", nil
	default:
		return nil, "", fmt.Errorf("location_set_auth: unknown method %q (want cidr, basic, jwt, or forward)", a.Method)
	}
}

// trimNonEmpty returns the non-blank, space-trimmed entries of in.
func trimNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

// orDefault returns s, or def when s is empty — used to echo the effective value
// (after re-parse defaulting) in an audit summary.
func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// buildHealthCheck turns the editor payload into a *config.HealthCheckConfig.
// A disabled payload returns nil so the serialized pool drops the [health_check]
// block entirely (passive health only). Durations are parsed here; everything
// else (defaulting, timeout < interval, http-needs-path) is enforced by the
// validated SaveConfig re-parse, so the structured edit never bypasses it.
func buildHealthCheck(in upstreamHealthCheck) (*config.HealthCheckConfig, string, error) {
	if !in.Enabled {
		return nil, "disabled", nil
	}
	typ := strings.TrimSpace(in.Type)
	if typ == "" {
		typ = "http"
	}
	if typ != "http" && typ != "tcp" {
		return nil, "", fmt.Errorf("upstream_set_health_check: type must be %q or %q", "http", "tcp")
	}
	hc := &config.HealthCheckConfig{
		Enabled:            true,
		Type:               typ,
		Path:               strings.TrimSpace(in.Path),
		HealthyThreshold:   in.HealthyThreshold,
		UnhealthyThreshold: in.UnhealthyThreshold,
		ExpectBody:         strings.TrimSpace(in.ExpectBody),
	}
	if typ == "http" && hc.Path == "" {
		return nil, "", fmt.Errorf("upstream_set_health_check: path is required for http probes")
	}
	if err := parseDurInto(in.Interval, &hc.Interval, "interval"); err != nil {
		return nil, "", fmt.Errorf("upstream_set_health_check: %w", err)
	}
	if err := parseDurInto(in.Timeout, &hc.Timeout, "timeout"); err != nil {
		return nil, "", fmt.Errorf("upstream_set_health_check: %w", err)
	}
	if len(in.ExpectStatus) > 0 {
		hc.ExpectStatus = append([]int(nil), in.ExpectStatus...)
	}
	note := typ
	if typ == "http" && hc.Path != "" {
		note = typ + " " + hc.Path
	}
	return hc, "enabled (" + note + ")", nil
}

// buildDiscovery turns the editor payload into a *config.DiscoveryConfig. A
// static/empty type returns nil so the pool falls back to its static Servers
// list. Secret tokens are never carried on the wire: when the provider type is
// unchanged, the existing Consul/Kubernetes token is preserved from prev rather
// than wiped. Per-provider required fields and refresh range are enforced by the
// validated SaveConfig re-parse.
func buildDiscovery(in upstreamDiscovery, prev *config.DiscoveryConfig) (*config.DiscoveryConfig, string, error) {
	typ := strings.ToLower(strings.TrimSpace(in.Type))
	switch typ {
	case "", "static":
		return nil, "disabled (static backends)", nil
	case "dns", "dns_srv", "consul", "kubernetes":
	default:
		return nil, "", fmt.Errorf("upstream_set_discovery: invalid type %q (want static|dns|dns_srv|consul|kubernetes)", in.Type)
	}
	d := &config.DiscoveryConfig{Type: typ, Target: strings.TrimSpace(in.Target)}
	if err := parseDurInto(in.Refresh, &d.Refresh, "refresh"); err != nil {
		return nil, "", fmt.Errorf("upstream_set_discovery: %w", err)
	}
	sameType := prev != nil && strings.EqualFold(strings.TrimSpace(prev.Type), typ)
	if typ == "consul" {
		cd := &config.ConsulDiscovery{}
		if in.Consul != nil {
			cd.Address = strings.TrimSpace(in.Consul.Address)
			cd.Service = strings.TrimSpace(in.Consul.Service)
			cd.Tag = strings.TrimSpace(in.Consul.Tag)
			cd.Datacenter = strings.TrimSpace(in.Consul.Datacenter)
			cd.PassingOnly = in.Consul.PassingOnly
		}
		if sameType && prev.Consul != nil {
			cd.Token = prev.Consul.Token // preserve the secret ACL token
		}
		d.Consul = cd
	}
	if typ == "kubernetes" {
		kd := &config.KubernetesDiscovery{}
		if in.Kubernetes != nil {
			kd.Namespace = strings.TrimSpace(in.Kubernetes.Namespace)
			kd.Service = strings.TrimSpace(in.Kubernetes.Service)
			kd.Port = strings.TrimSpace(in.Kubernetes.Port)
			kd.APIServer = strings.TrimSpace(in.Kubernetes.APIServer)
			kd.CAFile = strings.TrimSpace(in.Kubernetes.CAFile)
			kd.InsecureSkipTLSVerify = in.Kubernetes.InsecureSkipTLSVerify
		}
		if sameType && prev.Kubernetes != nil {
			kd.Token = prev.Kubernetes.Token // preserve the secret bearer token
		}
		d.Kubernetes = kd
	}
	return d, "set to " + typ, nil
}

// parseDurInto parses an optional duration string (e.g. "5s") into dst. An empty
// string leaves dst at its zero value so the re-parse defaulting applies.
func parseDurInto(val string, dst *config.Duration, name string) error {
	if strings.TrimSpace(val) == "" {
		return nil
	}
	var d config.Duration
	if err := d.UnmarshalText([]byte(val)); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	*dst = d
	return nil
}

// wafModeNote renders the mode/CRS suffix for a location_waf_set audit summary,
// e.g. " — block, CRS". It is empty when the override is disabled, since mode
// and CRS do not apply to a switched-off firewall.
func wafModeNote(enabled bool, mode string, crs bool) string {
	if !enabled {
		return ""
	}
	if crs {
		return fmt.Sprintf(" — %s, CRS", mode)
	}
	return fmt.Sprintf(" — %s", mode)
}

// findLocation returns a pointer to the single location uniquely identified by
// its server's listen address and ServerNames set plus the location's match
// type and path, so a mutation updates exactly the intended route in place.
// Matching on listen + path alone (the earlier behavior) could silently target
// the wrong virtual host when several server blocks share a listen, or the
// wrong location when a path repeats under different match types. The console
// always sends the full coordinates from the route projection; a target that
// resolves to more than one location is rejected rather than guessed.
func findLocation(c *config.Config, listen string, serverNames []string, matchType, path string) (*config.LocationConfig, error) {
	if strings.TrimSpace(listen) == "" || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("route target requires both listen and path")
	}
	var found *config.LocationConfig
	matches := 0
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.Listen != listen || !stringSetsEqual(srv.ServerNames, serverNames) {
			continue
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			if loc.Match.Path == path && loc.Match.Type == matchType {
				found = loc
				matches++
			}
		}
	}
	switch {
	case matches == 0:
		return nil, fmt.Errorf("no route found for listen %q names %v match %q path %q", listen, serverNames, matchType, path)
	case matches > 1:
		return nil, fmt.Errorf("route target is ambiguous: %d locations match listen %q names %v match %q path %q", matches, listen, serverNames, matchType, path)
	default:
		return found, nil
	}
}

// stringSetsEqual reports whether a and b contain the same elements regardless
// of order (multiset equality, so repeated server names are compared exactly).
func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// findUpstream returns a pointer to the upstream pool with the given name.
func findUpstream(c *config.Config, name string) (*config.UpstreamConfig, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("upstream name is required")
	}
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name {
			return &c.Upstreams[i], nil
		}
	}
	return nil, fmt.Errorf("no upstream named %q", name)
}

// handleConfigPatch applies a single structured edit to the running config and
// returns the generated full diff for review BEFORE the change is applied — it
// does not persist. The UI shows the diff and the operator confirms via the
// existing /api/config/apply path with the returned candidate TOML.
// POST /api/config/patch
func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req patchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Snapshot the pre-patch config so the diff reflects exactly this edit.
	before, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	summary, err := applyPatch(cfg, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The edit could not be applied.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	candidate, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Re-parse the before/after so the diff is computed over parsed models,
	// mirroring handleConfigDiff.
	beforeCfg, err := config.Parse(before)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]any{
		"ok":        true,
		"summary":   summary,
		"candidate": string(candidate),
		"diff":      diffConfigs(beforeCfg, cfg),
		// base_version is the version of the config this candidate was computed
		// from. A client echoes it back to /api/config/patch/apply so a stale
		// edit is rejected (409) instead of silently clobbering a concurrent
		// change (P2-12 optimistic concurrency).
		"base_version": configVersion(before),
	}
	// Cheap preview validation: parse the candidate and run the same structural,
	// secret-expansion, and WAF/auth dry-run checks as /api/config/validate, so
	// the operator sees problems BEFORE confirming the apply. This is advisory —
	// the authoritative, heavier full-factory preflight still runs at apply time
	// (WriteConfigRaw -> applyPreflight). A failing check does not block the
	// preview: the diff is still returned so the operator can see what the edit
	// would do, with the errors surfaced alongside it.
	if verr := validateRaw(candidate); verr != nil {
		resp["validation_errors"] = humanizeErr(verr.Error())
	}
	writeJSON(w, http.StatusOK, resp)
}

// configVersion is a short, stable fingerprint of a configuration used for
// optimistic concurrency. It is computed over the canonical marshaled form, so
// it is insensitive to comments and whitespace in the on-disk file and matches
// between a preview and a later apply of the same logical config.
func configVersion(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// patchApplyRequest is a server-side, atomic, conflict-checked batch of patch
// operations. Unlike the preview endpoint it persists the result: every op is
// applied to a single freshly-loaded config under a lock, and the result is
// written through the same validated preflight as /api/config/apply.
type patchApplyRequest struct {
	// BaseVersion is the config version the ops were computed against (returned
	// by the preview as base_version, or by a config read). When non-empty the
	// apply is rejected with 409 Conflict if the live config has changed since,
	// preventing a stale edit from silently clobbering a concurrent change. An
	// empty value skips the check (an explicit force-apply).
	BaseVersion string `json:"base_version,omitempty"`
	// Ops are applied in order to one config; a failure in any op aborts the
	// whole batch before anything is written (all-or-nothing).
	Ops []patchRequest `json:"ops"`
}

// conflictResponse is the 409 body when an apply is rejected because the live
// config changed since the edit was prepared. CurrentVersion lets the client
// reload, recompute, and retry.
type conflictResponse struct {
	OK             bool   `json:"ok"`
	Conflict       bool   `json:"conflict"`
	Message        string `json:"message"`
	CurrentVersion string `json:"current_version,omitempty"`
}

// handleConfigPatchApply applies a batch of structured patch operations
// atomically and entirely server-side — it never trusts a client-rendered
// candidate. All ops are applied to one freshly-loaded config under s.applyMu,
// and the result is persisted through the same validated WriteConfigRaw
// preflight as /api/config/apply, so a config that passes cannot fail the
// subsequent build. Optimistic concurrency (base_version) prevents a stale edit
// from silently clobbering a concurrent change (P2-12 lost update).
// POST /api/config/patch/apply
func (s *Server) handleConfigPatchApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil || s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req patchApplyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Ops) == 0 {
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "No patch operations were provided.",
			Errors:  humanizeErr("patch: at least one operation is required"),
		})
		return
	}

	// Serialize the whole read-modify-write so the version check and the write
	// are atomic. Without this, two concurrent applies could both read the same
	// base version, both pass the conflict check, and the second would silently
	// clobber the first.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	before, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	currentVersion := configVersion(before)
	if req.BaseVersion != "" && req.BaseVersion != currentVersion {
		s.recordAudit("config.patch", "config", "failure", "rejected: base version stale (concurrent change)", adminClientIP(r))
		writeJSON(w, http.StatusConflict, conflictResponse{
			OK:             false,
			Conflict:       true,
			Message:        "The configuration changed since this edit was prepared; reload and try again.",
			CurrentVersion: currentVersion,
		})
		return
	}

	// Apply every op to the single loaded config. A failure in any op aborts the
	// whole batch before anything is written, so the apply is all-or-nothing.
	summaries := make([]string, 0, len(req.Ops))
	for i, op := range req.Ops {
		summary, aerr := applyPatch(cfg, op)
		if aerr != nil {
			writeJSON(w, http.StatusBadRequest, validationErrorResponse{
				OK:      false,
				Message: fmt.Sprintf("Operation %d could not be applied; no change was made.", i+1),
				Errors:  humanizeErr(aerr.Error()),
			})
			return
		}
		summaries = append(summaries, summary)
	}

	candidate, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Snapshot the prior config, then persist through the authoritative preflight
	// (WriteConfigRaw). A rejection here means nothing was written, preserving
	// the all-or-nothing guarantee.
	prev := s.currentRaw()
	if err := s.deps.WriteConfigRaw(candidate); err != nil {
		if errors.Is(err, ErrRestartRequired) {
			s.writeRestartRequired(w, r, "config.patch", err)
			return
		}
		s.recordAudit("config.patch", "config", "failure", "rejected: invalid configuration", adminClientIP(r))
		s.emit("config", "apply_failed", "error", "Structured patch apply was rejected (invalid).")
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The configuration contains errors; no change was applied.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	s.recordHistory(prev)
	s.recordAudit("config.patch", "config", "success", strings.Join(summaries, "; "), adminClientIP(r))
	s.emit("config", "apply", "info", "Structured patch validated and saved; the live runtime is reloading.")

	beforeCfg, _ := config.Parse(before)
	// Return a post-apply status delta so the UI can reflect what changed. It is
	// derived from the persisted configuration: the apply preflight guarantees
	// the runtime will build this config, but the reload that swaps it in is
	// asynchronous, so "pending_reload" tells the UI this is the configuration
	// taking effect rather than a confirmation that the swap has completed.
	var status []FeatureStatus
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			status = s.runtimeStatus(cfg)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"pending_reload": true,
		"version":        configVersion(candidate),
		"summary":        summaries,
		"diff":           diffConfigs(beforeCfg, cfg),
		"status":         status,
		"message":        "Structured patch validated and saved. The live runtime is reloading to apply it.",
	})
}
