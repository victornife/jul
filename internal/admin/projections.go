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
	Secure      bool   `json:"secure"` // TLS required
	// Upstream is the referenced upstream pool name when Action proxies to a
	// named upstream (proxy_pass http://<name>); empty for direct host:port.
	Upstream string `json:"upstream,omitempty"`
	// Warnings flags likely-misconfigurations the operator should see before
	// editing (e.g. cache toggled on but the global cache is disabled).
	Warnings []string `json:"warnings,omitempty"`
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
}

// BackendProjection is one backend server in an upstream pool.
type BackendProjection struct {
	Address  string `json:"address"`
	Weight   int    `json:"weight"`
	Healthy  bool   `json:"healthy,omitempty"`
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
	// WAFMode is the enforcement mode ("block" or "detect") of the effective
	// WAF policy, when one is enabled.
	WAFMode string `json:"waf_mode,omitempty"`
	// WAFLocations is the number of locations the WAF protects.
	WAFLocations int `json:"waf_locations"`
	// SecretRefs is the number of ${env:}/${file:} secret references in the
	// configuration. The values themselves are never projected.
	SecretRefs int `json:"secret_refs"`
}

// TrafficControlsProjection is the traffic/observability settings panel.
type TrafficControlsProjection struct {
	Compression *CompressionProjection `json:"compression,omitempty"`
	RateLimit   *RateLimitProjection   `json:"rate_limit,omitempty"`
	Cache       *CacheProjection       `json:"cache,omitempty"`
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

// RuntimeOverview is the top-level dashboard summary.
type RuntimeOverview struct {
	Product string          `json:"product"`
	Version string          `json:"version"`
	Status  []FeatureStatus `json:"status"` // existing 21-row backbone
	Stats   interface{}     `json:"stats,omitempty"`
	// TrafficSources is the bounded top-N projection of request hosts/origins/
	// referers for the Console Overview Traffic Sources panel (Milestone 1.4).
	TrafficSources interface{} `json:"traffic_sources,omitempty"`
}

// ── Projection helpers ──────────────────────────────────────────────────────

func projectRoutes(c *config.Config) []RouteProjection {
	out := make([]RouteProjection, 0, len(c.Servers))
	for i := range c.Servers {
		srv := &c.Servers[i]
		rp := RouteProjection{
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
				Index:       j,
				Match:       loc.Match.Path,
				Type:        loc.Match.Type,
				Auth:        loc.Auth != nil,
				Cache:       loc.Cache,
				RateLimit:   loc.RateLimit != nil && loc.RateLimit.Enabled,
				Compression: c.Compression.Enabled,
				Secure:      srv.TLS != nil && srv.TLS.Enabled,
			}
			switch {
			case loc.GRPCTranscode != nil:
				lp.Action = "grpc_transcode"
				lp.Target = loc.ProxyPass
			case loc.GRPC:
				lp.Action = "grpc"
				lp.Target = loc.ProxyPass
			case loc.ProxyPass != "":
				lp.Action = "proxy"
				lp.Target = loc.ProxyPass
			case loc.FastCGIPass != "", loc.UWSGIPass != "":
				lp.Action = "fastcgi"
				lp.Target = firstNonEmpty(loc.FastCGIPass, loc.UWSGIPass)
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
			default:
				lp.Action = "unknown"
			}
			lp.Upstream = upstreamRef(lp.Target, lp.Action)
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
			}
		}
		if up.Discovery != nil {
			ap.Discovery = up.Discovery.Type
			ap.DiscoveryTarget = up.Discovery.Target
		}
		livePool, _ := live[up.Name]
		liveMap := make(map[string]BackendStatus, len(livePool.Backends))
		for _, b := range livePool.Backends {
			liveMap[b.Address] = b
		}
		for _, b := range up.Servers {
			bp := BackendProjection{Address: b.Address, Weight: b.Weight}
			if lb, ok := liveMap[b.Address]; ok {
				bp.Healthy = lb.Healthy
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

func projectSecurity(c *config.Config) SecurityProjection {
	sp := SecurityProjection{}
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
			// Resolve the WAF policy that applies to this location (its own
			// override or the global policy) and count protected locations,
			// recording the mode of the first one seen.
			if w := effectiveWAFPolicy(c, loc); w != nil && w.Enabled {
				sp.WAFEnabled = true
				sp.WAFLocations++
				if sp.WAFMode == "" {
					sp.WAFMode = wafModeOrDefault(w.Mode)
				}
			}
		}
	}
	sp.SecretRefs = config.CountSecretRefs(c)
	return sp
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
	return tcp
}

func mustMarshal(b []byte, _ error) []byte { return b }
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
