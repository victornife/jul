package admin

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"jul/internal/config"
)

// This file holds the read-only status/projection handlers (upstreams, certs,
// and the runtime feature-status overview), split out of api.go to keep each
// admin API file focused and under the size bar (Finding CQ-3).

// handleUpstreams serves the live upstream pools and backend health as JSON. It
// returns an empty array (not null) when no hook is wired so the console renders
// a clean empty state.
func (s *Server) handleUpstreams(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	out := []UpstreamStatus{}
	if s.deps.Upstreams != nil {
		if got := s.deps.Upstreams(); got != nil {
			out = got
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCerts serves the configured-certificate panel data as JSON.
func (s *Server) handleCerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	out := []CertStatus{}
	if s.deps.Certs != nil {
		if got := s.deps.Certs(); got != nil {
			out = got
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// FeatureStatus is one row of the console runtime-status overview: a shipped
// capability, the group it belongs to, whether it is active in the running
// configuration, and a short human-readable detail. It never carries secrets
// (tokens, file paths, or backend addresses) — only counts and flags.
type FeatureStatus struct {
	Group  string `json:"group"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Detail string `json:"detail,omitempty"`
}

// handleStatus serves a grouped overview of which shipped capabilities are
// active in the running configuration, for the console Status panel. It derives
// everything from the parsed config via the LoadConfig hook; when that hook is
// unwired it returns an empty list so the console renders a clean empty state.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	out := []FeatureStatus{}
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			out = s.runtimeStatus(cfg)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// countUnit renders "<n> <unit>" with a naive plural so details read naturally.
func countUnit(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// runtimeStatus inspects the parsed configuration and reports, per shipped
// capability, whether it is active and a short detail. The console renders this
// as the Status overview so an operator can see at a glance what the running
// build is doing without reading the raw TOML.
//
// INVARIANT (ADR 0004, GA criterion 9 in ADR 0003): every user-facing
// capability MUST appear here as a FeatureStatus row. This is the minimum
// "self-explanatory Console surface" that is part of every feature's Definition
// of Done — a feature is not done until an operator can see it is active from
// the Console. When you add a feature, add its row in the matching group below
// and assert it in TestStatusAPI; do not ship the feature without it.
func (s *Server) runtimeStatus(c *config.Config) []FeatureStatus {
	var (
		tlsServers      int
		mtlsServers     int
		acmeServers     int
		http3Servers    int
		h2cServers      int
		staticLocs      int
		proxyLocs       int
		fastcgiLocs     int
		grpcProxy       int
		grpcTranscode   int
		authLocs        int
		requireCertLocs int
		cacheLocs       int
		pluginLocs      int
		totalLocs       int
	)
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.TLS != nil && srv.TLS.Enabled {
			tlsServers++
			if srv.TLS.ClientAuth.Active() {
				mtlsServers++
			}
			if srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
				acmeServers++
			}
		}
		if srv.HTTP3 != nil && srv.HTTP3.Enabled {
			http3Servers++
		}
		if srv.H2C {
			h2cServers++
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			totalLocs++
			switch {
			case loc.GRPCTranscode != nil:
				grpcTranscode++
			case loc.GRPC:
				grpcProxy++
			case loc.ProxyPass != "":
				proxyLocs++
			case loc.FastCGIPass != "" || loc.UWSGIPass != "":
				fastcgiLocs++
			case loc.Root != "":
				staticLocs++
			}
			if loc.Auth != nil {
				authLocs++
			}
			if loc.RequireClientCert {
				requireCertLocs++
			}
			if loc.Cache {
				cacheLocs++
			}
			if loc.Plugin != "" || len(loc.Plugins) > 0 {
				pluginLocs++
			}
		}
	}

	var healthPools, discoveryPools int
	discoveryKinds := map[string]bool{}
	for i := range c.Upstreams {
		up := &c.Upstreams[i]
		if up.HealthCheck != nil && up.HealthCheck.Enabled {
			healthPools++
		}
		if up.Discovery != nil {
			if t := strings.ToLower(strings.TrimSpace(up.Discovery.Type)); t != "" && t != "static" {
				discoveryPools++
				discoveryKinds[t] = true
			}
		}
	}

	// Build the grouped rows. Detail strings stay abstract (counts and kinds).
	vhostDetail := ""
	if len(c.Servers) > 0 {
		vhostDetail = countUnit(len(c.Servers), "server block") + ", " + countUnit(totalLocs, "location")
	}

	cacheDetail := ""
	if c.Cache.Enabled {
		cacheDetail = "in-memory tier"
		if c.Cache.DiskPath != "" {
			cacheDetail += " + disk overflow"
		}
		if cacheLocs > 0 {
			cacheDetail += "; " + countUnit(cacheLocs, "location") + " opt-in"
		}
	}

	comprDetail := ""
	if c.Compression.Enabled {
		enc := c.Compression.Encoders
		if len(enc) == 0 {
			enc = []string{"gzip"}
		}
		comprDetail = "encoders: " + strings.Join(enc, ", ")
	}

	rlDetail := ""
	if c.RateLimit.Enabled {
		key := c.RateLimit.Key
		if key == "" {
			key = "ip"
		}
		rlDetail = fmt.Sprintf("key=%s, rate=%d/s", key, c.RateLimit.Rate)
		if c.RateLimit.MaxConns > 0 {
			rlDetail += fmt.Sprintf(", max %d conns", c.RateLimit.MaxConns)
		}
	}

	trDetail := ""
	if c.Observability.Tracing.Enabled {
		exp := c.Observability.Tracing.Exporter
		if exp == "" {
			exp = "otlp-grpc"
		}
		trDetail = "exporter: " + exp
	}

	sinks := c.Observability.AccessLog.Sinks
	if len(sinks) == 0 {
		sinks = []string{"stdout"}
	}

	mtlsDetail := ""
	if mtlsServers > 0 {
		mtlsDetail = countUnit(mtlsServers, "server block")
		if requireCertLocs > 0 {
			mtlsDetail += "; " + countUnit(requireCertLocs, "location") + " require cert"
		}
	}

	discDetail := ""
	if discoveryPools > 0 {
		kinds := make([]string, 0, len(discoveryKinds))
		for k := range discoveryKinds {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		discDetail = countUnit(discoveryPools, "pool") + " (" + strings.Join(kinds, ", ") + ")"
	}

	// Secret references (${env:}/${file:}) resolved at load time; counted from
	// the unresolved config so the panel shows usage without exposing values.
	secretRefs := config.CountSecretRefs(c)

	// WAF detail reports the real distribution of effective enforcement modes
	// across protected locations, so the panel never claims a single global
	// mode that some locations do not actually run in. A mixed deployment shows
	// "3 locations: 2 block, 1 detect"; a uniform one collapses to a single
	// mode. CRS coverage is appended when any protected location enables it.
	//
	// The coverage counts come from wafDistribution, the same helper
	// projectSecurity uses, so this status row and the Security panel always
	// agree on the WAF mix.
	wd := wafDistribution(c)
	wafLocs := wd.Locations
	wafBlockLocs := wd.BlockLocs
	wafDetectLocs := wd.DetectLocs
	wafCRSLocs := wd.CRSLocs
	wafDetail := ""
	if wafLocs > 0 {
		modes := make([]string, 0, 2)
		if wafBlockLocs > 0 {
			modes = append(modes, fmt.Sprintf("%d block", wafBlockLocs))
		}
		if wafDetectLocs > 0 {
			modes = append(modes, fmt.Sprintf("%d detect", wafDetectLocs))
		}
		wafDetail = countUnit(wafLocs, "location")
		switch {
		case wafBlockLocs > 0 && wafDetectLocs > 0:
			// Mixed modes: spell out the split so an operator is not misled
			// into thinking every route blocks (or every route only detects).
			wafDetail += ": " + strings.Join(modes, ", ")
		case wafDetectLocs > 0:
			wafDetail += " (detect)"
		default:
			wafDetail += " (block)"
		}
		if wafCRSLocs > 0 {
			wafDetail += "; CRS on " + countUnit(wafCRSLocs, "location")
		}
	}

	return []FeatureStatus{
		{Group: "Traffic", Name: "Virtual hosts", Active: len(c.Servers) > 0, Detail: vhostDetail},
		{Group: "Traffic", Name: "Static file serving", Active: staticLocs > 0, Detail: countDetailIf(staticLocs, "location")},
		{Group: "Traffic", Name: "Reverse proxy", Active: proxyLocs > 0, Detail: countDetailIf(proxyLocs, "location")},
		{Group: "Traffic", Name: "FastCGI / uWSGI", Active: fastcgiLocs > 0, Detail: countDetailIf(fastcgiLocs, "location")},
		{Group: "Traffic", Name: "Response cache", Active: c.Cache.Enabled, Detail: cacheDetail},
		{Group: "Traffic", Name: "Compression", Active: c.Compression.Enabled, Detail: comprDetail},
		{Group: "Traffic", Name: "Rate limiting", Active: c.RateLimit.Enabled, Detail: rlDetail},

		{Group: "Security", Name: "TLS", Active: tlsServers > 0, Detail: countDetailIf(tlsServers, "server block")},
		{Group: "Security", Name: "Mutual TLS (client certs)", Active: mtlsServers > 0, Detail: mtlsDetail},
		{Group: "Security", Name: "Automatic HTTPS (ACME)", Active: acmeServers > 0, Detail: countDetailIf(acmeServers, "server block")},
		{Group: "Security", Name: "Access control (auth)", Active: authLocs > 0, Detail: countDetailIf(authLocs, "location")},
		{Group: "Security", Name: "Web application firewall (WAF)", Active: wafLocs > 0, Detail: wafDetail},
		{Group: "Security", Name: "Secret references", Active: secretRefs > 0, Detail: countDetailIf(secretRefs, "reference")},

		{Group: "Protocols", Name: "HTTP/3 (QUIC)", Active: http3Servers > 0, Detail: countDetailIf(http3Servers, "server block")},
		{Group: "Protocols", Name: "Cleartext HTTP/2 (h2c)", Active: h2cServers > 0, Detail: countDetailIf(h2cServers, "server block")},
		{Group: "Protocols", Name: "gRPC transcoding", Active: grpcTranscode > 0, Detail: countDetailIf(grpcTranscode, "location")},
		{Group: "Protocols", Name: "gRPC passthrough", Active: grpcProxy > 0, Detail: countDetailIf(grpcProxy, "location")},
		{Group: "Protocols", Name: "L4 stream proxy", Active: len(c.Streams) > 0, Detail: countDetailIf(len(c.Streams), "listener")},

		{Group: "Upstreams", Name: "Upstream pools", Active: len(c.Upstreams) > 0, Detail: countDetailIf(len(c.Upstreams), "pool")},
		{Group: "Upstreams", Name: "Active health checks", Active: healthPools > 0, Detail: countDetailIf(healthPools, "pool")},
		{Group: "Upstreams", Name: "Service discovery", Active: discoveryPools > 0, Detail: discDetail},

		{Group: "Observability", Name: "Prometheus metrics", Active: s.deps.Metrics != nil, Detail: metricsDetail(s.deps.Metrics != nil)},
		{Group: "Observability", Name: "Distributed tracing", Active: c.Observability.Tracing.Enabled, Detail: trDetail},
		{Group: "Observability", Name: "Access log", Active: true, Detail: "sinks: " + strings.Join(sinks, ", ")},

		{Group: "Extensibility", Name: "WASM plugins", Active: len(c.Plugins) > 0, Detail: pluginDetail(len(c.Plugins), pluginLocs)},
	}
}

// countDetailIf returns "<n> <unit>(s)" when n > 0, else an empty detail.
func countDetailIf(n int, unit string) string {
	if n <= 0 {
		return ""
	}
	return countUnit(n, unit)
}

// metricsDetail describes the Prometheus surface when it is wired.
func metricsDetail(on bool) string {
	if on {
		return "exposed at /metrics"
	}
	return ""
}

// pluginDetail summarizes declared plugins and how many locations reference one.
func pluginDetail(declared, locs int) string {
	if declared == 0 {
		return ""
	}
	d := countUnit(declared, "module")
	if locs > 0 {
		d += "; " + countUnit(locs, "location")
	}
	return d
}
