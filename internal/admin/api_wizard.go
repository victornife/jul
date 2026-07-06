// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"jul/internal/config"
)

// This file holds the setup-wizard config generators (serve/proxy/app flows),
// split out of api.go to keep each admin API file focused and under the size bar
// (Finding CQ-3). The wizard is non-mutating: it returns TOML for review; the
// operator applies it through the validated write path.

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
	HealthCheck *bool    `json:"health_check"` // enable active health checks (app mode); nil = use preset default
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

	// Health checks: use the operator's explicit choice when supplied;
	// otherwise fall back to the preset default. A path is still required.
	wantHC := preset.HealthCheck
	if in.HealthCheck != nil {
		wantHC = *in.HealthCheck
	}
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
	isGRPC := strings.EqualFold(strings.TrimSpace(in.Preset), "grpc")
	if isGRPC {
		loc.GRPC = true
	}

	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:    listen,
			H2C:       isGRPC,
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

// handleWizardGenerate is the non-mutating v2 TOML generation endpoint.
// It supersedes /api/wizard: identical logic but at POST /api/wizard/generate.
// POST /api/wizard/generate
func (s *Server) handleWizardGenerate(w http.ResponseWriter, r *http.Request) {
	// Delegate to the same logic as the v1 wizard.
	s.handleWizard(w, r)
}
