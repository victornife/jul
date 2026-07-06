// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestWizardAppModeGeneratesUpstreamAndRoute(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	body := `{"mode":"app","name":"backend","preset":"express","backends":["127.0.0.1:3000","127.0.0.1:3001"],"route_path":"/api","listen":":8080"}`
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/wizard/generate", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		TOML string `json:"toml"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The generated config must parse and validate (handler validates before
	// returning, so this is a redundant guard) and reference the upstream.
	cfg, err := config.Parse([]byte(resp.TOML))
	if err != nil {
		t.Fatalf("generated config does not parse: %v\n%s", err, resp.TOML)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("generated config does not validate: %v\n%s", err, resp.TOML)
	}
	if len(cfg.Upstreams) != 1 || cfg.Upstreams[0].Name != "backend" {
		t.Fatalf("expected one upstream named backend, got %+v", cfg.Upstreams)
	}
	if len(cfg.Upstreams[0].Servers) != 2 {
		t.Errorf("expected 2 backends, got %d", len(cfg.Upstreams[0].Servers))
	}
	if cfg.Upstreams[0].HealthCheck == nil || !cfg.Upstreams[0].HealthCheck.Enabled {
		t.Errorf("express preset should enable health check, got %+v", cfg.Upstreams[0].HealthCheck)
	}
	if len(cfg.Servers) != 1 || len(cfg.Servers[0].Locations) != 1 {
		t.Fatalf("expected one server with one location, got %+v", cfg.Servers)
	}
	if cfg.Servers[0].Locations[0].ProxyPass != "http://backend" {
		t.Errorf("expected proxy_pass http://backend, got %q", cfg.Servers[0].Locations[0].ProxyPass)
	}
	if cfg.Servers[0].Locations[0].Match.Path != "/api" {
		t.Errorf("expected route path /api, got %q", cfg.Servers[0].Locations[0].Match.Path)
	}
}

func TestWizardAppModeRequiresNameAndBackends(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	for _, body := range []string{
		`{"mode":"app"}`,
		`{"mode":"app","name":"x"}`,
		`{"mode":"app","backends":["127.0.0.1:3000"]}`,
	} {
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/wizard/generate", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for %s, got %d", body, rr.Code)
		}
	}
}
