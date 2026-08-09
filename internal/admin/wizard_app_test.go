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
	if !cfg.Compression.IsEnabled() {
		t.Error("wizard app config should enable compression")
	}
}

func TestWizardPatchIncludesCompression(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	body := `{"mode":"proxy","target":"http://localhost:8080","listen":":3000"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/wizard/generate?format=patch", strings.NewReader(body))
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Ops []patchRequest `json:"ops"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, op := range resp.Ops {
		if op.Op == "compression_set" && op.Compression != nil && *op.Compression.Enabled {
			found = true
			if op.Compression.Encoders == nil || len(*op.Compression.Encoders) == 0 {
				t.Error("compression_set op should include encoders")
			}
		}
	}
	if !found {
		t.Fatalf("wizard patch output missing compression_set op; ops=%+v", resp.Ops)
	}
}

// TestWizardGRPCAppEndToEnd is the direct Wizard gRPC regression test the audit
// flagged as missing (H-02). The action setter is unit-tested separately; this
// exercises the full wizard endpoint for the grpc preset in both output formats
// and asserts the gRPC discriminators survive end to end:
//
//   - TOML format: the server enables H2C and the location carries GRPC=true
//     with proxy_pass to the upstream (so Jul speaks h2c to the backend rather
//     than falling through to a plain HTTP/1.1 proxy).
//   - patch format: the location_add carries the grpc_proxy action and a
//     server_toggle_h2c op is emitted, so applying the patch reproduces the same
//     gRPC-capable server rather than a plain proxy.
func TestWizardGRPCAppEndToEnd(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	body := `{"mode":"app","name":"grpcbackend","preset":"grpc","backends":["127.0.0.1:50051"],"route_path":"/","listen":":8080"}`

	t.Run("toml format sets H2C and GRPC", func(t *testing.T) {
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
		cfg, err := config.Parse([]byte(resp.TOML))
		if err != nil {
			t.Fatalf("generated config does not parse: %v\n%s", err, resp.TOML)
		}
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("generated config does not validate: %v\n%s", err, resp.TOML)
		}
		if len(cfg.Servers) != 1 || len(cfg.Servers[0].Locations) != 1 {
			t.Fatalf("expected one server with one location, got %+v", cfg.Servers)
		}
		if !cfg.Servers[0].H2C {
			t.Error("grpc preset must enable H2C on the server")
		}
		loc := cfg.Servers[0].Locations[0]
		if !loc.GRPC {
			t.Error("grpc preset must set GRPC=true on the location")
		}
		if loc.ProxyPass != "http://grpcbackend" {
			t.Errorf("proxy_pass = %q, want http://grpcbackend", loc.ProxyPass)
		}
	})

	t.Run("patch format emits grpc_proxy action and h2c toggle", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/wizard/generate?format=patch", strings.NewReader(body)))
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		var resp struct {
			Ops []patchRequest `json:"ops"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		var grpcAction, h2cToggle bool
		for _, op := range resp.Ops {
			if op.Op == "location_add" && op.Action != nil && op.Action.Kind == "grpc_proxy" {
				grpcAction = true
				if op.Action.Target != "http://grpcbackend" {
					t.Errorf("grpc_proxy target = %q, want http://grpcbackend", op.Action.Target)
				}
			}
			if op.Op == "server_toggle_h2c" && op.Enabled != nil && *op.Enabled {
				h2cToggle = true
			}
		}
		if !grpcAction {
			t.Error("patch ops must include a location_add with the grpc_proxy action")
		}
		if !h2cToggle {
			t.Error("patch ops must include a server_toggle_h2c(enabled=true) op for gRPC")
		}
	})
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
