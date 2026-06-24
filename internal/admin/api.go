package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"jul/internal/config"
)

// UpstreamStatus is the console view of one upstream pool. The composition root
// adapts its internal pool snapshot into this shape so the admin package stays
// decoupled from the upstream package.
type UpstreamStatus struct {
	Name     string          `json:"name"`
	Strategy string          `json:"strategy"`
	Backends []BackendStatus `json:"backends"`
}

// BackendStatus is the console view of one backend within a pool.
type BackendStatus struct {
	Address  string `json:"address"`
	Weight   int    `json:"weight"`
	Healthy  bool   `json:"healthy"`
	Inflight int64  `json:"inflight"`
}

// CertStatus is the console view of one configured certificate. It never
// carries private-key material.
type CertStatus struct {
	ServerNames []string  `json:"server_names"`
	Source      string    `json:"source"`
	Subject     string    `json:"subject,omitempty"`
	Issuer      string    `json:"issuer,omitempty"`
	DNSNames    []string  `json:"dns_names,omitempty"`
	NotBefore   time.Time `json:"not_before,omitempty"`
	NotAfter    time.Time `json:"not_after,omitempty"`
	Error       string    `json:"error,omitempty"`
}

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

// wizardInput is the setup-wizard request. It supports several guided flows
// beyond the original serve/proxy: putting an application behind Jul (an
// upstream pool with one or more backends, an optional health check, and a
// proxy route), with framework presets supplying friendly defaults.
type wizardInput struct {
	// Mode selects the flow: "serve" (static directory), "proxy" (single
	// target), or "app" (an application behind Jul with an upstream pool).
	Mode   string `json:"mode"`
	Path   string `json:"path"`   // serve: directory to serve
	Target string `json:"target"` // proxy: upstream target
	Listen string `json:"listen"` // optional listen address

	// App-mode fields.
	Name        string   `json:"name"`         // app/upstream name (app mode)
	Backends    []string `json:"backends"`     // backend host:port list (app mode)
	Preset      string   `json:"preset"`       // framework preset (app mode)
	RoutePath   string   `json:"route_path"`   // path prefix to mount the app on (app mode)
	HealthCheck bool     `json:"health_check"` // enable active health checks (app mode)
	HealthPath  string   `json:"health_path"`  // health-check path (app mode)
	Strategy    string   `json:"strategy"`     // load-balancing strategy (app mode)
}

// appPreset captures the friendly defaults a framework preset contributes. The
// presets only influence copy and defaults — they never create
// framework-specific magic.
type appPreset struct {
	Strategy    string
	HealthPath  string
	HealthCheck bool
}

// appPresets maps the supported framework presets to friendly defaults. An
// unknown or empty preset falls back to a generic HTTP app.
var appPresets = map[string]appPreset{
	"node":    {Strategy: "round_robin", HealthPath: "/health", HealthCheck: true},
	"express": {Strategy: "round_robin", HealthPath: "/health", HealthCheck: true},
	"apollo":  {Strategy: "round_robin", HealthPath: "/.well-known/apollo/server-health", HealthCheck: true},
	"fastapi": {Strategy: "round_robin", HealthPath: "/health", HealthCheck: true},
	"django":  {Strategy: "round_robin", HealthPath: "/healthz", HealthCheck: true},
	"flask":   {Strategy: "round_robin", HealthPath: "/healthz", HealthCheck: true},
	"go":      {Strategy: "least_conn", HealthPath: "/healthz", HealthCheck: true},
	"grpc":    {Strategy: "least_conn", HealthPath: "", HealthCheck: false},
	"generic": {Strategy: "round_robin", HealthPath: "/health", HealthCheck: false},
}

// handleWizard synthesizes a starter configuration from the wizard inputs and
// returns it as TOML for review in the editor. It is non-mutating: the operator
// applies it through the validated /api/config/raw path, which also snapshots
// the prior config for rollback. The generated config is validated here so the
// wizard never proposes a config the editor would reject.
func (s *Server) handleWizard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var in wizardInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var (
		cfg    *config.Config
		bagErr string
	)
	switch strings.ToLower(strings.TrimSpace(in.Mode)) {
	case "serve":
		if strings.TrimSpace(in.Path) == "" {
			bagErr = "serve mode requires a directory path"
			break
		}
		cfg = config.ServeDir(in.Path, in.Listen)
	case "proxy":
		if strings.TrimSpace(in.Target) == "" {
			bagErr = "proxy mode requires a target"
			break
		}
		cfg = config.ProxyTarget(in.Target, in.Listen)
	case "app":
		cfg, bagErr = wizardAppConfig(in)
	default:
		bagErr = `mode must be "serve", "proxy", or "app"`
	}
	if bagErr != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": bagErr})
		return
	}
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	toml, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"toml": string(toml)})
}

// wizardAppConfig builds an "app behind Jul" configuration: a named upstream
// pool with the supplied backends, an optional active health check, and a
// reverse-proxy route mounting the app at route_path. Framework presets supply
// friendly defaults for strategy and health-check path. It returns a non-empty
// error string when the inputs are insufficient.
func wizardAppConfig(in wizardInput) (*config.Config, string) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "app mode requires a name"
	}
	var backends []config.UpstreamServer
	for _, b := range in.Backends {
		addr := strings.TrimSpace(b)
		if addr == "" {
			continue
		}
		backends = append(backends, config.UpstreamServer{Address: addr, Weight: 1})
	}
	if len(backends) == 0 {
		return nil, "app mode requires at least one backend (host:port)"
	}

	preset := appPresets[strings.ToLower(strings.TrimSpace(in.Preset))]
	if preset.Strategy == "" {
		preset = appPresets["generic"]
	}
	strategy := strings.TrimSpace(in.Strategy)
	if strategy == "" {
		strategy = preset.Strategy
	}

	listen := in.Listen
	if strings.TrimSpace(listen) == "" {
		listen = config.DefaultZeroConfigListen
	}
	routePath := strings.TrimSpace(in.RoutePath)
	if routePath == "" {
		routePath = "/"
	}

	up := config.UpstreamConfig{
		Name:     name,
		Strategy: strategy,
		Servers:  backends,
	}

	// Health checks: enable when the operator asked for it or the preset
	// defaults to it and a path is resolvable.
	wantHC := in.HealthCheck || preset.HealthCheck
	hcPath := strings.TrimSpace(in.HealthPath)
	if hcPath == "" {
		hcPath = preset.HealthPath
	}
	if wantHC && hcPath != "" {
		up.HealthCheck = &config.HealthCheckConfig{
			Enabled:            true,
			Type:               "http",
			Path:               hcPath,
			Interval:           config.Duration(5 * time.Second),
			Timeout:            config.Duration(2 * time.Second),
			HealthyThreshold:   2,
			UnhealthyThreshold: 3,
			ExpectStatus:       []int{200},
		}
	}

	loc := config.LocationConfig{
		Match:     config.MatchConfig{Type: "prefix", Path: routePath},
		ProxyPass: "http://" + name,
	}
	if strings.EqualFold(strings.TrimSpace(in.Preset), "grpc") {
		loc.GRPC = true
	}

	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:    listen,
			Locations: []config.LocationConfig{loc},
		}},
		Upstreams: []config.UpstreamConfig{up},
		Compression: config.CompressionConfig{
			Enabled:  true,
			Encoders: []string{"gzip"},
			MinSize:  config.Size(1 << 10),
			Types: []string{
				"text/html", "text/css", "text/plain",
				"application/json", "application/javascript",
				"application/xml", "image/svg+xml",
			},
		},
	}
	return cfg, ""
}

// handleHistoryList serves the configuration snapshot index, newest first.
func (s *Server) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	entries, err := s.hist.list()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []historyEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleHistoryGet serves the raw TOML of a single snapshot for preview, keyed
// by the ?id= query parameter.
func (s *Server) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.URL.Query().Get("id")
	raw, err := s.hist.get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "raw": string(raw)})
}

// handleHistoryRollback re-applies a stored snapshot through the validated raw
// write path, which reloads on success. The running config is snapshotted first
// so a rollback is itself reversible.
func (s *Server) handleHistoryRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	raw, err := s.hist.get(req.ID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	prev := s.currentRaw()
	if err := s.deps.WriteConfigRaw(raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.recordHistory(prev)
	writeJSON(w, http.StatusOK, map[string]string{"status": "rolled back", "id": req.ID})
}

// currentRaw reads the running raw configuration, or nil when unavailable. It is
// used to snapshot the prior config just before a successful edit.
func (s *Server) currentRaw() []byte {
	if s.deps.ReadConfigRaw == nil {
		return nil
	}
	raw, err := s.deps.ReadConfigRaw()
	if err != nil {
		return nil
	}
	return raw
}

// recordHistory snapshots the prior configuration after a successful edit.
// Snapshot failures are logged but never surfaced to the operator: the edit
// already succeeded, and a missing snapshot must not look like a failed save.
func (s *Server) recordHistory(prev []byte) {
	if len(prev) == 0 || !s.hist.enabled() {
		return
	}
	if _, err := s.hist.snapshot(prev); err != nil && s.log != nil {
		s.log.Warn("config history snapshot failed", "error", err)
	}
}

// methodNotAllowed writes a 405 with the permitted method advertised.
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
}

// ── Console v2 API handlers ─────────────────────────────────────────────────

// withConfig wraps a handler that needs a parsed config. When LoadConfig is
// unavailable it returns a clean empty-state response.
func (s *Server) withConfig(next func(*config.Config, http.ResponseWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		if s.deps.LoadConfig == nil {
			writeJSON(w, http.StatusOK, map[string]any{"loaded": false})
			return
		}
		cfg, err := s.deps.LoadConfig()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		next(cfg, w)
	}
}

func (s *Server) handleRuntimeOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	var status []FeatureStatus
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			status = s.runtimeStatus(cfg)
		}
	}
	out := RuntimeOverview{
		Product: s.deps.Product,
		Version: s.deps.Version,
		Status:  status,
	}
	if s.deps.Stats != nil {
		out.Stats = s.deps.Stats()
	}
	if s.deps.TrafficSources != nil {
		out.TrafficSources = s.deps.TrafficSources()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, projectRoutes(c))
	})(w, r)
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if s.deps.LoadConfig == nil {
		writeJSON(w, http.StatusOK, map[string]any{"loaded": false})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ups := map[string]UpstreamStatus{}
	if s.deps.Upstreams != nil {
		for _, u := range s.deps.Upstreams() {
			ups[u.Name] = u
		}
	}
	writeJSON(w, http.StatusOK, projectApps(cfg, ups))
}

func (s *Server) handleTLS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if s.deps.LoadConfig == nil {
		writeJSON(w, http.StatusOK, map[string]any{"loaded": false})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var certs []CertStatus
	if s.deps.Certs != nil {
		certs = s.deps.Certs()
	}
	writeJSON(w, http.StatusOK, projectTLS(cfg, certs))
}

func (s *Server) handleSecurity(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, projectSecurity(c))
	})(w, r)
}

func (s *Server) handleTrafficControls(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, projectTrafficControls(c))
	})(w, r)
}

// handleConfigValidate accepts a candidate config and returns structured
// human-readable validation errors without persisting anything. POST /api/config/validate
func (s *Server) handleConfigValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{OK: false, Message: err.Error()})
		return
	}
	// Validation is a pure function of the candidate bytes: parse + validate
	// with no persistence and no reload. It must never call WriteConfigRaw,
	// which would briefly apply (and reload) the draft as live configuration.
	// This keeps /api/config/validate side-effect-free and safe under
	// concurrent validate/apply requests.
	if err := validateRaw(body); err != nil {
		writeJSON(w, http.StatusOK, validationErrorResponse{
			OK:      false,
			Message: "The draft configuration contains errors.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	writeJSON(w, http.StatusOK, validationErrorResponse{OK: true, Message: "Configuration is valid."})
}

// validateRaw parses and fully validates candidate configuration bytes without
// mutating any runtime state. It mirrors the parse+validate that a write path
// performs internally, minus persistence and reload, so callers can check a
// draft safely and idempotently.
func validateRaw(body []byte) error {
	cfg, err := config.Parse(body)
	if err != nil {
		return err
	}
	return config.Validate(cfg)
}

// handleConfigDiff accepts a candidate config and returns a structured diff
// against the current running config. POST /api/config/diff
func (s *Server) handleConfigDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	before, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot load current config: " + err.Error()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	after, err := config.Parse(body)
	if err != nil {
		writeJSON(w, http.StatusOK, validationErrorResponse{
			OK:      false,
			Message: "The draft is not valid TOML / config.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	writeJSON(w, http.StatusOK, diffConfigs(before, after))
}

// handleConfigApply is the authoritative v2 write path: validate → snapshot →
// write (which triggers reload) → return post-apply runtime delta.
// POST /api/config/apply
func (s *Server) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Snapshot the current config before applying so the apply is reversible.
	prev := s.currentRaw()

	if err := s.deps.WriteConfigRaw(body); err != nil {
		s.recordAudit("config.apply", "config", "failure", "rejected: invalid configuration", adminClientIP(r))
		s.emit("config", "apply_failed", "error", "Configuration apply was rejected (invalid).")
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The configuration contains errors; no change was applied.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	s.recordHistory(prev)
	s.recordAudit("config.apply", "config", "success", "configuration applied and reloaded", adminClientIP(r))

	// Record the apply on the timeline and broadcast it to SSE subscribers.
	s.emit("config", "apply", "info", "Configuration applied and reloaded.")

	// Return a post-apply status delta so the UI can reflect what changed.
	var status []FeatureStatus
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			status = s.runtimeStatus(cfg)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": status,
	})
}

// handleConfigHistoryList serves the v2 snapshot index at GET /api/config/history.
func (s *Server) handleConfigHistoryList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	entries, err := s.hist.list()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []historyEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleConfigHistoryGet serves a single snapshot by path parameter at
// GET /api/config/history/{id}. The id is validated to prevent path traversal.
func (s *Server) handleConfigHistoryGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	// Go 1.22+ ServeMux path parameter extraction.
	id := r.PathValue("id")
	if id == "" {
		// Fallback: accept ?id= for compatibility.
		id = r.URL.Query().Get("id")
	}
	raw, err := s.hist.get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "raw": string(raw)})
}

// handleConfigRollback re-applies a stored snapshot via the validated write path
// at POST /api/config/rollback. The running config is snapshotted first so the
// rollback is itself reversible.
func (s *Server) handleConfigRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	raw, err := s.hist.get(req.ID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	prev := s.currentRaw()
	if err := s.deps.WriteConfigRaw(raw); err != nil {
		s.recordAudit("config.rollback", "config", "failure", "rollback rejected for snapshot "+req.ID, adminClientIP(r))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.recordHistory(prev)
	s.recordAudit("config.rollback", "config", "success", "rolled back to snapshot "+req.ID, adminClientIP(r))

	s.emit("config", "rollback", "warning", "Configuration rolled back to a previous snapshot.")
	writeJSON(w, http.StatusOK, map[string]string{"status": "rolled back", "id": req.ID})
}

// handleWizardGenerate is the non-mutating v2 TOML generation endpoint.
// It supersedes /api/wizard: identical logic but at POST /api/wizard/generate.
// POST /api/wizard/generate
func (s *Server) handleWizardGenerate(w http.ResponseWriter, r *http.Request) {
	// Delegate to the same logic as the v1 wizard.
	s.handleWizard(w, r)
}
