// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file contains the Console v2 projection builder functions that transform
// the in-memory config and runtime state into the typed JSON shapes defined in
// projection_types.go. Types live there; logic lives here.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"jul/internal/config"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
				Compression:       c.Compression.IsEnabled(),
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

	certs := make([]CertProjection, 0)
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
				if l.Error != "" {
					cp.Error = l.Error
				}
			}
		}
		certs = append(certs, cp)
	}
	return certs
}

func projectCertRisk(certs []CertProjection) *CertRiskProjection {
	if len(certs) == 0 {
		return nil
	}
	cr := CertRiskProjection{Count: len(certs)}
	for _, cp := range certs {
		if cp.NotAfter != "" {
			// Live metadata available — days_left is authoritative.
			if cp.DaysLeft < 0 {
				cr.Expired++
			} else if cp.DaysLeft <= 7 {
				cr.ExpiringSoon++
			}
		} else {
			// No live metadata for this cert.
			cr.Errors++
		}
	}
	if cr.Expired > 0 {
		cr.Details = fmt.Sprintf("%d expired, %d expiring ≤ 7d", cr.Expired, cr.ExpiringSoon)
	} else if cr.ExpiringSoon > 0 {
		cr.Details = fmt.Sprintf("%d expiring ≤ 7d", cr.ExpiringSoon)
	} else if cr.Errors > 0 {
		cr.Details = fmt.Sprintf("%d with no live expiry data", cr.Errors)
	} else {
		cr.Details = "all certs valid"
	}
	return &cr
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
	// Outbound egress allow-list posture (P4-01). Counts only; the recent-blocked
	// breakdown is overlaid from the running process by the HTTP handler.
	sp.Egress = EgressProjection{
		Enabled:        c.Egress.Enabled,
		AllowRuleCount: countEgressAllow(c.Egress.Allow),
	}
	// Default posture: serving == persisted. The HTTP handler overlays the real
	// installed (serving) snapshot; this default keeps the pure projection
	// self-contained for callers/tests without a live server.
	persisted := rbacStatusFromAdmin(c.Admin)
	sp.RBAC = RBACPostureProjection{Serving: persisted, Persisted: persisted}
	return sp
}

// countEgressAllow counts the non-blank [egress].allow entries for the Security
// panel without inspecting or exposing any destination.
func countEgressAllow(allow []string) int {
	n := 0
	for _, a := range allow {
		if strings.TrimSpace(a) != "" {
			n++
		}
	}
	return n
}

// projectRBAC summarises the admin RBAC posture for the Security/Overview
// surfaces. It exposes only counts and booleans derived from the effective
// [admin] configuration and never any credential, token ID, or hash.
func projectRBAC(c *config.Config) RBACStatusProjection {
	return rbacStatusFromAdmin(c.Admin)
}

// rbacStatusFromAdmin builds the secret-free RBAC status from an admin config.
func rbacStatusFromAdmin(a config.AdminConfig) RBACStatusProjection {
	return RBACStatusProjection{
		Enabled:           a.RBAC.Enabled,
		PrincipalCount:    len(a.RBAC.Principals),
		RoleCount:         len(a.RBAC.Roles),
		LegacyTokenActive: strings.TrimSpace(a.Token) != "",
	}
}

// rbacPosture reports the serving vs persisted RBAC authentication posture.
// Serving is derived from the installed authentication snapshot — what the
// admin API actually enforces right now — while persisted reflects the on-disk
// configuration. Pending is true when a staged change has not yet been
// installed, so the Security panel never presents a staged policy as active
// (N-03).
func (s *Server) rbacPosture(persisted *config.Config) RBACPostureProjection {
	snap := s.currentAuth()
	serving := rbacStatusFromAdmin(snap.cfg)
	serving.Generation = rbacAuthSignature(snap.cfg)
	persistedStatus := rbacStatusFromAdmin(persisted.Admin)
	persistedStatus.Generation = rbacAuthSignature(persisted.Admin)
	return RBACPostureProjection{
		Serving:   serving,
		Persisted: persistedStatus,
		Pending:   serving.Generation != persistedStatus.Generation,
	}
}

// rbacAuthSignature is a deterministic, secret-free digest of the
// authentication-relevant admin configuration: whether the admin API and RBAC
// are enabled, the default role, each role's permission set, and each
// principal's role/disabled/expiry plus token PRESENCE (never the value). It is
// used only to detect whether the serving snapshot differs from the persisted
// config (N-03); excluding token values keeps it robust to secret-reference
// expansion and free of any credential material.
func rbacAuthSignature(a config.AdminConfig) string {
	h := sha256.New()
	field := func(v string) { _, _ = h.Write([]byte(v)); _, _ = h.Write([]byte{0}) }
	field(strconv.FormatBool(a.Enabled))
	field(strconv.FormatBool(strings.TrimSpace(a.Token) != ""))
	field(strconv.FormatBool(a.RBAC.Enabled))
	field(a.RBAC.DefaultRole)
	roles := append([]config.AdminRole(nil), a.RBAC.Roles...)
	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
	for _, r := range roles {
		field("role")
		field(r.Name)
		for _, p := range rbacSortedUniq(r.Permissions) {
			field(p)
		}
	}
	principals := append([]config.AdminPrincipal(nil), a.RBAC.Principals...)
	sort.Slice(principals, func(i, j int) bool { return principals[i].Name < principals[j].Name })
	for _, p := range principals {
		field("principal")
		field(p.Name)
		field(p.Role)
		field(strconv.FormatBool(p.Disabled))
		field(p.ExpiresAt.UTC().Format(time.RFC3339))
		field(strconv.FormatBool(strings.TrimSpace(p.Token) != ""))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
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
	if c.Compression.IsEnabled() {
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
