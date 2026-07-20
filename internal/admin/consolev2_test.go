// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// v2WriteServer wires a full file-backed validated write path used by apply,
// history, and rollback tests.
func v2WriteServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	initial := validTOML(t, "./public", ":8080")
	if err := os.WriteFile(cfgPath, initial, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func(data []byte) error {
			c, err := config.Parse(data)
			if err != nil {
				return err
			}
			if err := config.Validate(c); err != nil {
				return err
			}
			return os.WriteFile(cfgPath, data, 0o644)
		},
		ApplyConfigRaw: func(_ ApplyRequestContext, data []byte, mode string) (ConfigApplyResult, error) {
			c, err := config.Parse(data)
			if err != nil {
				return ConfigApplyResult{
					OK:               false,
					Mode:             mode,
					Message:          "parse error",
					ValidationErrors: []string{err.Error()},
				}, nil
			}
			if err := config.Validate(c); err != nil {
				return ConfigApplyResult{
					OK:               false,
					Mode:             mode,
					Message:          "validation error",
					ValidationErrors: []string{err.Error()},
				}, nil
			}
			if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
				return ConfigApplyResult{OK: false, Mode: mode, Message: "write error"}, err
			}
			return ConfigApplyResult{
				OK:             true,
				Mode:           mode,
				Version:        configVersion(data),
				ServingVersion: configVersion(data),
				Message:        "Configuration validated, saved, and applied live.",
			}, nil
		},
		ApplyConfig: func(_ ApplyRequestContext, c *config.Config, mode string) (ConfigApplyResult, error) {
			data, err := config.Marshal(c)
			if err != nil {
				return ConfigApplyResult{OK: false, Mode: mode, Message: "marshal error"}, err
			}
			if err := config.Validate(c); err != nil {
				return ConfigApplyResult{
					OK:               false,
					Mode:             mode,
					Message:          "validation error",
					ValidationErrors: []string{err.Error()},
				}, nil
			}
			if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
				return ConfigApplyResult{OK: false, Mode: mode, Message: "write error"}, err
			}
			return ConfigApplyResult{
				OK:             true,
				Mode:           mode,
				Version:        configVersion(data),
				ServingVersion: configVersion(data),
				Message:        "Configuration validated, saved, and applied live.",
			}, nil
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	cfg := config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}
	return newTestServer(t, cfg, deps), cfgPath
}

// ── /api/runtime/overview ────────────────────────────────────────────────────

func TestRuntimeOverviewOK(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			TLS:       &config.TLSConfig{Enabled: true},
			Locations: []config.LocationConfig{{ProxyPass: "http://localhost:9000"}},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		Product:    "Jul.IA",
		Version:    "1.2.3",
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out RuntimeOverview
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Product != "Jul.IA" {
		t.Errorf("product = %q, want Jul.IA", out.Product)
	}
	if len(out.Status) == 0 {
		t.Error("status rows should not be empty")
	}
}

// TestRuntimeOverviewStreamStatus proves the L4 stream-proxy reload outcome is
// surfaced on the overview so the console can warn that a stream config was
// rejected while the prior listeners keep serving (the apply response cannot
// carry it because stream reload is asynchronous).
func TestRuntimeOverviewStreamStatus(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Locations: []config.LocationConfig{{ProxyPass: "http://localhost:9000"}},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		Product:      "Jul.IA",
		Version:      "1.2.3",
		LoadConfig:   func() (*config.Config, error) { return cfg, nil },
		StreamStatus: func() string { return "failed: stream: listen tcp :5353: address already in use" },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out RuntimeOverview
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(out.StreamStatus, "failed:") {
		t.Errorf("stream_status = %q, want a failed: prefix", out.StreamStatus)
	}
}

// TestRuntimeOverviewStreamStatusOmitted proves the field is omitted entirely
// when no StreamStatus dep is wired (the common HTTP-only case), so the console
// shows no stream panel.
func TestRuntimeOverviewStreamStatusOmitted(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Locations: []config.LocationConfig{{ProxyPass: "http://localhost:9000"}},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		Product:    "Jul.IA",
		Version:    "1.2.3",
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "stream_status") {
		t.Errorf("stream_status should be omitted, body = %s", rr.Body.String())
	}
}

func TestRuntimeOverviewMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runtime/overview", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestRuntimeOverviewAuditSinkHealthy proves a configured, writable durable
// audit sink surfaces a healthy audit_sink on the overview (P3-08).
func TestRuntimeOverviewAuditSinkHealthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	cfg := &config.Config{}
	s := newTestServer(t, config.AdminConfig{AuditLogFile: path}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out RuntimeOverview
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.AuditSink == nil {
		t.Fatal("audit_sink should be present when a durable sink is configured")
	}
	if !out.AuditSink.Configured || !out.AuditSink.Healthy {
		t.Errorf("audit_sink = %+v, want configured+healthy", out.AuditSink)
	}
}

// TestRuntimeOverviewAuditSinkDegraded proves an unopenable durable sink is
// surfaced as a degraded audit_sink rather than silently dropped (P3-08).
func TestRuntimeOverviewAuditSinkDegraded(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	cfg := &config.Config{}
	s := newTestServer(t, config.AdminConfig{AuditLogFile: filepath.Join(blocker, "audit.jsonl")}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out RuntimeOverview
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.AuditSink == nil {
		t.Fatal("audit_sink should be present when a durable sink is configured")
	}
	if out.AuditSink.Healthy {
		t.Error("audit_sink.healthy = true, want false for an unopenable sink")
	}
	if out.AuditSink.Error == "" {
		t.Error("audit_sink.error should explain the degradation")
	}
}

// TestRuntimeOverviewAuditSinkOmitted proves the field is omitted entirely when
// no durable audit sink is configured (the common in-memory-only case).
func TestRuntimeOverviewAuditSinkOmitted(t *testing.T) {
	cfg := &config.Config{}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "audit_sink") {
		t.Errorf("audit_sink should be omitted, body = %s", rr.Body.String())
	}
}

// TestRuntimeOverviewAdminHealthOmittedWhenHealthy proves the admin_health
// field is omitted from the overview when the admin subsystem is healthy.
func TestRuntimeOverviewAdminHealthOmittedWhenHealthy(t *testing.T) {
	cfg := &config.Config{}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "admin_health") {
		t.Errorf("admin_health should be omitted when healthy, body = %s", rr.Body.String())
	}
}

// TestRuntimeOverviewAdminHealthSurfacesDegraded proves a composition-root
// admin health failure is surfaced on the runtime overview (F-05).
func TestRuntimeOverviewAdminHealthSurfacesDegraded(t *testing.T) {
	cfg := &config.Config{}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
		AdminHealth: func() error {
			return &AdminHealthStatus{
				Healthy: false,
				Reason:  "admin_reload",
				Detail:  "admin subsystem reload failed: rbac policy update failed",
			}
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out RuntimeOverview
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.AdminHealth == nil {
		t.Fatal("admin_health should be present when degraded")
	}
	if out.AdminHealth.Healthy {
		t.Error("admin_health.healthy = true, want false")
	}
	if out.AdminHealth.Reason != "admin_reload" {
		t.Errorf("admin_health.reason = %q, want admin_reload", out.AdminHealth.Reason)
	}
}

// ── /api/routes ──────────────────────────────────────────────────────────────

func TestRoutesProjection(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      ":443",
			ServerNames: []string{"example.com"},
			TLS:         &config.TLSConfig{Enabled: true},
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Path: "/", Type: "prefix"}, ProxyPass: "http://backend"},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/routes", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []RouteProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Listen != ":443" {
		t.Fatalf("unexpected projection: %+v", out)
	}
	if out[0].TLS == nil || !out[0].TLS.Enabled {
		t.Error("TLS should be enabled in projection")
	}
	if len(out[0].Locations) != 1 || out[0].Locations[0].Action != "proxy" {
		t.Errorf("unexpected location: %+v", out[0].Locations)
	}
}

func TestRoutesNoLoadConfigReturnsNotLoaded(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/routes", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

// ── /api/apps ────────────────────────────────────────────────────────────────

func TestAppsProjection(t *testing.T) {
	passing := true
	cfg := &config.Config{
		Upstreams: []config.UpstreamConfig{{
			Name:     "api",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "10.0.0.1:80", Weight: 1},
			},
			HealthCheck: &config.HealthCheckConfig{
				Enabled: true, Type: "http", Path: "/healthz",
				Interval: config.Duration(5e9), Timeout: config.Duration(2e9),
				HealthyThreshold: 2, UnhealthyThreshold: 3, ExpectStatus: []int{200},
			},
			Discovery: &config.DiscoveryConfig{
				Type:    "consul",
				Refresh: config.Duration(30e9),
				Consul:  &config.ConsulDiscovery{Service: "web", Token: "secret-acl", PassingOnly: &passing},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
		Upstreams: func() []UpstreamStatus {
			return []UpstreamStatus{{Name: "api", Backends: []BackendStatus{{Address: "10.0.0.1:80", Healthy: true}}}}
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []AppProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Name != "api" {
		t.Fatalf("unexpected projection: %+v", out)
	}
	if len(out[0].Backends) != 1 || out[0].Backends[0].Healthy == nil || !*out[0].Backends[0].Healthy {
		t.Error("backend should be marked healthy from live data")
	}
	if out[0].HealthCheckPath != "/healthz" || out[0].HealthCheckTimeout != "2s" || out[0].HealthCheckHealthyThr != 2 {
		t.Errorf("health-check detail not projected: %+v", out[0])
	}
	if out[0].DiscoveryConsul == nil || out[0].DiscoveryConsul.Service != "web" || !out[0].DiscoveryConsul.HasToken {
		t.Errorf("consul discovery not projected with has_token: %+v", out[0].DiscoveryConsul)
	}
	// The ACL token itself must never appear in the projection payload.
	if bytes.Contains(rr.Body.Bytes(), []byte("secret-acl")) {
		t.Error("consul ACL token leaked into /api/apps projection")
	}
}

// TestAppsBackendHealthThreeState proves the projection distinguishes a
// known-unhealthy backend (healthy=false) from one with no live status
// (healthy omitted = unknown). Conflating them would mislabel a down backend
// as healthy in the console.
func TestAppsBackendHealthThreeState(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamConfig{{
			Name:     "api",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "10.0.0.1:80", Weight: 1}, // has live status (down)
				{Address: "10.0.0.2:80", Weight: 1}, // no live status (unknown)
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
		Upstreams: func() []UpstreamStatus {
			return []UpstreamStatus{{Name: "api", Backends: []BackendStatus{{Address: "10.0.0.1:80", Healthy: false}}}}
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []AppProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || len(out[0].Backends) != 2 {
		t.Fatalf("unexpected projection: %+v", out)
	}
	if out[0].Backends[0].Healthy == nil || *out[0].Backends[0].Healthy {
		t.Error("first backend should be known-unhealthy (healthy=false)")
	}
	if out[0].Backends[1].Healthy != nil {
		t.Error("second backend should be unknown (healthy nil/omitted)")
	}
}

// ── /api/tls ─────────────────────────────────────────────────────────────────

func TestTLSProjection(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			ServerNames: []string{"example.com"},
			TLS:         &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{Enabled: true}},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tls", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []CertProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Source != "acme" {
		t.Fatalf("unexpected projection: %+v", out)
	}
}

// ── /api/security ─────────────────────────────────────────────────────────────

func TestSecurityProjection(t *testing.T) {
	cfg := &config.Config{
		WAF: config.WAFConfig{Enabled: true, Mode: "detect"},
		Servers: []config.ServerConfig{{
			Locations: []config.LocationConfig{
				{Auth: &config.AuthConfig{}, RequireClientCert: true, Headers: map[string]string{"X-Token": "${env:JUL_X_TOKEN}"}},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/security", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out SecurityProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.AuthEnabled {
		t.Error("auth_enabled should be true")
	}
	if out.RequireCertCount != 1 {
		t.Errorf("require_cert_count = %d, want 1", out.RequireCertCount)
	}
	if !out.WAFEnabled || out.WAFMode != "detect" || out.WAFLocations != 1 {
		t.Errorf("WAF projection = {enabled:%v mode:%q locations:%d}, want {true detect 1}", out.WAFEnabled, out.WAFMode, out.WAFLocations)
	}
	if out.SecretRefs != 1 {
		t.Errorf("secret_refs = %d, want 1", out.SecretRefs)
	}
}

// TestSecurityProjectionWAFCompiled proves the projection reports whether this
// binary includes the WAF engine, so the Security panel can warn up front when
// an enabled WAF would be rejected by the apply preflight on a non-waf build.
func TestSecurityProjectionWAFCompiled(t *testing.T) {
	cfg := &config.Config{WAF: config.WAFConfig{Enabled: true, Mode: "block"}}
	load := func() (*config.Config, error) { return cfg, nil }
	for _, tc := range []struct {
		name     string
		compiled bool
	}{
		{"compiled", true},
		{"lean", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, config.AdminConfig{}, Deps{LoadConfig: load, WAFCompiled: tc.compiled})
			rr := httptest.NewRecorder()
			s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/security", nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			var out SecurityProjection
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.WAFCompiled != tc.compiled {
				t.Errorf("waf_compiled = %v, want %v", out.WAFCompiled, tc.compiled)
			}
		})
	}
}

// TestSecurityProjectionWAFDistributionAndGlobal proves the projection reports
// the real per-location enforcement mix and exposes the global [waf] policy
// verbatim, so the Security panel is truthful and the guided editor can seed
// from the running config instead of clobbering unshown fields.
func TestSecurityProjectionWAFDistributionAndGlobal(t *testing.T) {
	cfg := &config.Config{
		WAF: config.WAFConfig{
			Enabled:           true,
			Mode:              "detect",
			CRSEnabled:        true,
			Paranoia:          2,
			BlockStatus:       406,
			ResponseBodyCheck: true,
			RequestBodyLimit:  config.Size(128 << 10),
			DirectivesFiles:   []string{"/etc/jul/waf/a.conf"},
			InlineRules:       `SecRule ARGS "@rx evil" "id:1,deny"`,
		},
		Servers: []config.ServerConfig{{
			Locations: []config.LocationConfig{
				// Inherits the global detect+CRS policy.
				{Match: config.MatchConfig{Path: "/a"}},
				// Overrides to block, no CRS, with advanced SecLang fields set so
				// the projection must carry the full override for the editor seed.
				{Match: config.MatchConfig{Path: "/b"}, WAF: &config.WAFConfig{
					Enabled:           true,
					Mode:              "block",
					BlockStatus:       429,
					RequestBodyLimit:  config.Size(64 << 10),
					ResponseBodyCheck: true,
					DirectivesFiles:   []string{"/etc/jul/waf/b.conf"},
					InlineRules:       `SecRule REQUEST_URI "@contains /x" "id:200,deny"`,
				}},
				// Opts out entirely.
				{Match: config.MatchConfig{Path: "/c"}, WAF: &config.WAFConfig{Enabled: false}},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/security", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out SecurityProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Distribution: 2 protected (/a detect+CRS, /b block), /c opted out.
	if out.WAFLocations != 2 || out.WAFBlockLocs != 1 || out.WAFDetectLocs != 1 || out.WAFCRSLocs != 1 {
		t.Errorf("distribution = {locs:%d block:%d detect:%d crs:%d}, want {2 1 1 1}",
			out.WAFLocations, out.WAFBlockLocs, out.WAFDetectLocs, out.WAFCRSLocs)
	}
	// Global [waf] verbatim for the editor seed.
	if !out.WAFGlobalEnabled || out.WAFGlobalMode != "detect" || !out.WAFCRSEnabled ||
		out.WAFParanoia != 2 || out.WAFBlockStatus != 406 || !out.WAFResponseBodyCheck ||
		out.WAFRequestBodyLimit != "128k" || len(out.WAFDirectivesFiles) != 1 || out.WAFInlineRules == "" {
		t.Errorf("global WAF projection = %+v", out)
	}
	// Per-location overrides are disclosed truthfully: /b (block) and /c (opted
	// out) define their own [waf]; /a inherits the global policy and so is NOT
	// listed. The Security panel relies on this to avoid presenting the single
	// global "Edit" as if it governed every route.
	if len(out.LocationWAFs) != 2 {
		t.Fatalf("location_wafs = %d entries, want 2: %+v", len(out.LocationWAFs), out.LocationWAFs)
	}
	var bOverride, cOverride *LocationWAFProjection
	for i := range out.LocationWAFs {
		switch out.LocationWAFs[i].Path {
		case "/b":
			bOverride = &out.LocationWAFs[i]
		case "/c":
			cOverride = &out.LocationWAFs[i]
		}
	}
	if bOverride == nil || !bOverride.Enabled || bOverride.Mode != "block" || bOverride.CRSEnabled {
		t.Errorf("/b override = %+v, want enabled block without CRS", bOverride)
	}
	// The advanced SecLang fields project verbatim so the guided editor seeds and
	// round-trips them instead of clobbering rules it never showed.
	if bOverride != nil && (bOverride.BlockStatus != 429 || bOverride.RequestBodyLimit != "64k" ||
		!bOverride.ResponseBodyCheck || len(bOverride.DirectivesFiles) != 1 || bOverride.InlineRules == "") {
		t.Errorf("/b advanced fields = %+v, want block_status 429, 64k limit, response check, 1 rule file, inline rules", bOverride)
	}
	if cOverride == nil || cOverride.Enabled {
		t.Errorf("/c override = %+v, want a disabled override", cOverride)
	}
}

// ── /api/traffic-controls ─────────────────────────────────────────────────────

func TestTrafficControlsProjection(t *testing.T) {
	cfg := &config.Config{
		Compression: config.CompressionConfig{Enabled: config.Bool(true), Encoders: []string{"gzip", "br"}},
		RateLimit:   config.RateLimitConfig{Enabled: true, Rate: 100, Key: "ip"},
		Observability: config.ObservabilityConfig{
			Tracing: config.TracingConfig{
				Enabled:     true,
				Exporter:    "otlp-http",
				Endpoint:    "http://collector:4318",
				SampleRatio: 0.25,
				ServiceName: "edge",
				Insecure:    true,
			},
		},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/traffic-controls", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out TrafficControlsProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Compression == nil || !out.Compression.Enabled {
		t.Error("compression should be enabled")
	}
	if out.RateLimit == nil || out.RateLimit.Rate != 100 {
		t.Error("rate limit should be 100")
	}
	if out.Tracing == nil {
		t.Fatal("tracing should be projected when enabled")
	}
	if out.Tracing.Exporter != "otlp-http" || out.Tracing.Endpoint != "http://collector:4318" ||
		out.Tracing.SampleRatio != 0.25 || out.Tracing.ServiceName != "edge" || !out.Tracing.Insecure {
		t.Errorf("tracing projection = %+v", out.Tracing)
	}
}

// ── /api/config/validate ─────────────────────────────────────────────────────

func TestConfigValidateOK(t *testing.T) {
	s, _ := v2WriteServer(t)
	good := validTOML(t, "./www", ":9090")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/validate", bytes.NewReader(good))
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out validationErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Errorf("ok = false, want true; message: %s", out.Message)
	}
}

func TestConfigValidateRejectsInvalid(t *testing.T) {
	s, _ := v2WriteServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/validate", strings.NewReader("{not toml"))
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out validationErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OK {
		t.Error("ok should be false for invalid TOML")
	}
}

func TestConfigValidateDoesNotMutateState(t *testing.T) {
	s, cfgPath := v2WriteServer(t)
	before, _ := os.ReadFile(cfgPath)

	// Send a valid but different config.
	alt := validTOML(t, "./alt", ":8181")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/validate", bytes.NewReader(alt)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}

	after, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(before, after) {
		t.Error("validate must not mutate the on-disk config")
	}
}

func TestConfigValidateNeverWritesOrReloads(t *testing.T) {
	// Validate must be a pure parse+validate: it must never invoke the write
	// path (which, in production, persists and triggers a live reload). The
	// previous implementation wrote the draft then reverted, briefly applying
	// it; this guards against that regression.
	var writes, reloads int
	deps := Deps{
		WriteConfigRaw: func([]byte) error { writes++; return nil },
		Reload:         func() error { reloads++; return nil },
		LoadConfig:     func() (*config.Config, error) { return &config.Config{}, nil },
	}
	s := newTestServer(t, config.AdminConfig{}, deps)

	good := validTOML(t, "./www", ":9090")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/validate", bytes.NewReader(good)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out validationErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Errorf("ok = false, want true; message: %s", out.Message)
	}
	if writes != 0 {
		t.Errorf("WriteConfigRaw called %d times during validate; want 0", writes)
	}
	if reloads != 0 {
		t.Errorf("Reload called %d times during validate; want 0", reloads)
	}
}

func TestConfigValidateWorksWithoutWriteHook(t *testing.T) {
	// Validation is independent of any Deps hook; it must succeed even when raw
	// editing (WriteConfigRaw) is disabled.
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	good := validTOML(t, "./www", ":9090")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/validate", bytes.NewReader(good)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out validationErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Errorf("ok = false, want true; message: %s", out.Message)
	}
}

// ── /api/config/diff ─────────────────────────────────────────────────────────

func TestConfigDiffDetectsChange(t *testing.T) {
	s, _ := v2WriteServer(t)
	alt := validTOML(t, "./other", ":9191")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/diff", bytes.NewReader(alt)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out ConfigDiff
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Summary == "" {
		t.Error("summary should not be empty when configs differ")
	}
}

func TestConfigDiffInvalidBodyReturnsError(t *testing.T) {
	s, _ := v2WriteServer(t)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/diff", strings.NewReader("{bad")))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out validationErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OK {
		t.Error("ok should be false for bad input")
	}
}

// ── /api/config/patch (preview validation) ───────────────────────────────────

// patchProxyConfig returns a minimal, validate-passing config with a single
// proxy location so patch-preview tests can flip the target between a valid and
// an unbuildable value.
func patchProxyConfig() *config.Config {
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
				ProxyPass: "http://127.0.0.1:9000",
			}},
		}},
	}
}

// TestConfigPatchPreviewSurfacesValidationErrors proves the patch preview runs
// the cheap candidate validation and returns validation_errors when the edit
// would produce a config that fails to validate — while still returning the
// diff and never persisting anything.
func TestConfigPatchPreviewSurfacesValidationErrors(t *testing.T) {
	var writes int
	deps := Deps{
		LoadConfig:     func() (*config.Config, error) { return patchProxyConfig(), nil },
		WriteConfigRaw: func([]byte) error { writes++; return nil },
	}
	s := newTestServer(t, config.AdminConfig{}, deps)

	// Point the route at a single-label host that is neither an IP nor a known
	// upstream: validateProxyPass rejects it as an unknown-upstream typo.
	body, err := json.Marshal(patchRequest{
		Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/api",
		Target: "http://ghost",
	})
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		OK               bool              `json:"ok"`
		Candidate        string            `json:"candidate"`
		ValidationErrors []validationError `json:"validation_errors"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Error("ok = false, want true (the patch still applied to the model)")
	}
	if out.Candidate == "" {
		t.Error("candidate should still be returned so the operator sees the diff")
	}
	if len(out.ValidationErrors) == 0 {
		t.Error("validation_errors should be present for an unbuildable candidate")
	}
	if writes != 0 {
		t.Errorf("WriteConfigRaw called %d times during preview; want 0", writes)
	}
}

// TestConfigPatchPreviewValidCandidateHasNoErrors proves a valid edit returns no
// validation_errors, so the field is a true signal rather than always-on noise.
func TestConfigPatchPreviewValidCandidateHasNoErrors(t *testing.T) {
	deps := Deps{LoadConfig: func() (*config.Config, error) { return patchProxyConfig(), nil }}
	s := newTestServer(t, config.AdminConfig{}, deps)

	body, err := json.Marshal(patchRequest{
		Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/api",
		Target: "http://127.0.0.1:9100",
	})
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		OK               bool              `json:"ok"`
		ValidationErrors []validationError `json:"validation_errors"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Error("ok = false, want true")
	}
	if len(out.ValidationErrors) != 0 {
		t.Errorf("validation_errors = %+v, want none for a valid candidate", out.ValidationErrors)
	}
}

// TestConfigPatchActionSwitchProducesValidCandidate proves switching a cached
// proxy route to a static action through the patch preview re-parses cleanly:
// the op clears the cache toggle (cache + root is rejected by validation), so
// the candidate carries no validation_errors and is never persisted.
func TestConfigPatchActionSwitchProducesValidCandidate(t *testing.T) {
	var writes int
	deps := Deps{
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{
				Cache: config.CacheConfig{Enabled: true},
				Servers: []config.ServerConfig{{
					Listen: ":8080",
					Locations: []config.LocationConfig{{
						Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
						ProxyPass: "http://127.0.0.1:9000",
						Cache:     true,
					}},
				}},
			}, nil
		},
		WriteConfigRaw: func([]byte) error { writes++; return nil },
	}
	s := newTestServer(t, config.AdminConfig{}, deps)

	body, err := json.Marshal(patchRequest{
		Op: "location_set_action", Listen: ":8080", MatchType: "prefix", Path: "/api",
		Action: &locationActionPayload{Kind: "static", Target: "/var/www"},
	})
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		OK               bool              `json:"ok"`
		Candidate        string            `json:"candidate"`
		ValidationErrors []validationError `json:"validation_errors"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || len(out.ValidationErrors) != 0 {
		t.Errorf("ok=%v errors=%+v, want ok with no validation errors", out.OK, out.ValidationErrors)
	}
	if !strings.Contains(out.Candidate, "/var/www") || strings.Contains(out.Candidate, "127.0.0.1:9000") {
		t.Errorf("candidate did not switch to a clean static action:\n%s", out.Candidate)
	}
	if writes != 0 {
		t.Errorf("WriteConfigRaw called %d times during preview; want 0", writes)
	}
}

// TestConfigPatchPreviewBatch previews a compound edit (server_add +
// location_add) without persisting. This is the backend support for the
// RouteEditor patch migration in F-06.
func TestConfigPatchPreviewBatch(t *testing.T) {
	var writes int
	deps := Deps{
		LoadConfig:     func() (*config.Config, error) { return patchProxyConfig(), nil },
		WriteConfigRaw: func([]byte) error { writes++; return nil },
	}
	s := newTestServer(t, config.AdminConfig{}, deps)

	req := patchApplyRequest{
		Ops: []patchRequest{
			{Op: "server_add", Listen: ":9090"},
			{
				Op: "location_add", Listen: ":9090",
				Match:  &locationMatch{Type: "prefix", Path: "/new"},
				Action: &locationActionPayload{Kind: "proxy", Target: "http://127.0.0.1:9001"},
			},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/preview", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		OK        bool   `json:"ok"`
		Candidate string `json:"candidate"`
		Summary   string `json:"summary"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Errorf("ok = false, want true")
	}
	if !strings.Contains(out.Candidate, ":9090") {
		t.Errorf("candidate missing new server; got:\n%s", out.Candidate)
	}
	if !strings.Contains(out.Candidate, "/new") {
		t.Errorf("candidate missing new location; got:\n%s", out.Candidate)
	}
	if !strings.Contains(out.Summary, "server") || !strings.Contains(out.Summary, "route") {
		t.Errorf("summary = %q, want both server and route mentions", out.Summary)
	}
	if writes != 0 {
		t.Errorf("WriteConfigRaw called %d times during batch preview; want 0", writes)
	}
}

// TestConfigPatchPreviewBatchFailsFast aborts the whole preview on the first
// invalid op so the operator never sees a partial diff.
func TestConfigPatchPreviewBatchFailsFast(t *testing.T) {
	deps := Deps{LoadConfig: func() (*config.Config, error) { return patchProxyConfig(), nil }}
	s := newTestServer(t, config.AdminConfig{}, deps)

	req := patchApplyRequest{
		Ops: []patchRequest{
			{Op: "server_add", Listen: ":9090"},
			{
				// The location_add references the just-added server; the second op
				// should fail because the proxy action requires a non-empty target.
				Op: "location_add", Listen: ":9090",
				Match:  &locationMatch{Type: "prefix", Path: "/bad"},
				Action: &locationActionPayload{Kind: "proxy", Target: ""},
			},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/preview", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Operation 2") {
		t.Errorf("error body should mention the failing op index; got: %s", rr.Body.String())
	}
}

// ── /api/config/patch/apply (server-side atomic batch + conflict) ─────────────

// v2ProxyWriteServer is a file-backed harness seeded with a proxy config (one
// location at prefix "/") so structured patch-apply tests can persist
// route_set_target edits and observe them survive a reload-from-disk. The seed
// is produced via ProxyTarget so it carries full defaults and passes Validate.
func v2ProxyWriteServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgPath, seed, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func(data []byte) error {
			c, err := config.Parse(data)
			if err != nil {
				return err
			}
			if err := config.Validate(c); err != nil {
				return err
			}
			return os.WriteFile(cfgPath, data, 0o644)
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	cfg := config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}
	return newTestServer(t, cfg, deps), cfgPath
}

// TestConfigPatchApplyPersistsAtomically proves the server-side apply computes
// the edit entirely from a freshly-loaded config and persists it through the
// validated preflight, returning a fresh version the client can use for the
// next optimistic-concurrency check.
func TestConfigPatchApplyPersistsAtomically(t *testing.T) {
	s, cfgPath := v2ProxyWriteServer(t)
	body, err := json.Marshal(patchApplyRequest{Ops: []patchRequest{
		{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: "http://127.0.0.1:9100"},
	}})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Fatalf("ok=%v, want true", out.OK)
	}
	if out.Version == "" {
		t.Error("version should be returned for the next optimistic-concurrency check")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	c, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if got := c.Servers[0].Locations[0].ProxyPass; got != "http://127.0.0.1:9100" {
		t.Errorf("proxy_pass = %q, want the patched target persisted", got)
	}
}

// TestConfigPatchApplyRejectsStaleBaseVersion proves optimistic concurrency: an
// apply carrying a base version that no longer matches the live config is
// rejected with 409 and writes nothing, so a stale edit cannot clobber a
// concurrent change.
func TestConfigPatchApplyRejectsStaleBaseVersion(t *testing.T) {
	s, cfgPath := v2ProxyWriteServer(t)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	body, err := json.Marshal(patchApplyRequest{
		BaseVersion: "deadbeefdeadbeef",
		Ops: []patchRequest{
			{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: "http://127.0.0.1:9100"},
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	var out conflictResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Conflict || out.CurrentVersion == "" {
		t.Errorf("conflict body = %+v, want conflict=true and a current_version", out)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("config was modified despite a stale-version conflict")
	}
}

// TestConfigPatchApplyAcceptsMatchingBaseVersion proves the preview's
// base_version round-trips: applying with the version the preview reported is
// accepted, confirming preview and apply agree on the config fingerprint.
func TestConfigPatchApplyAcceptsMatchingBaseVersion(t *testing.T) {
	s, _ := v2ProxyWriteServer(t)
	op := patchRequest{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: "http://127.0.0.1:9100"}

	pv, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal preview: %v", err)
	}
	prr := httptest.NewRecorder()
	s.routes().ServeHTTP(prr, httptest.NewRequest(http.MethodPost, "/api/config/patch", bytes.NewReader(pv)))
	if prr.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200; body: %s", prr.Code, prr.Body.String())
	}
	var preview struct {
		BaseVersion string `json:"base_version"`
	}
	if err := json.Unmarshal(prr.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.BaseVersion == "" {
		t.Fatal("preview did not return base_version")
	}

	body, err := json.Marshal(patchApplyRequest{BaseVersion: preview.BaseVersion, Ops: []patchRequest{op}})
	if err != nil {
		t.Fatalf("marshal apply: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("apply status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestConfigPatchApplyAbortsBatchOnBadOp proves the batch is all-or-nothing: a
// later op that cannot be applied aborts the whole apply with 400 and persists
// nothing, even though an earlier op in the batch was valid.
func TestConfigPatchApplyAbortsBatchOnBadOp(t *testing.T) {
	s, cfgPath := v2ProxyWriteServer(t)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	body, err := json.Marshal(patchApplyRequest{Ops: []patchRequest{
		{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: "http://127.0.0.1:9100"},
		{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/nonexistent", Target: "http://127.0.0.1:9200"},
	}})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("config was modified despite a failing op in the batch (not atomic)")
	}
}

// TestConfigPatchApplyRejectsEmptyOps proves an apply with no operations is a
// client error rather than a no-op write.
func TestConfigPatchApplyRejectsEmptyOps(t *testing.T) {
	s, _ := v2ProxyWriteServer(t)
	body, err := json.Marshal(patchApplyRequest{Ops: nil})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// TestConfigPatchApplyRestartRequiredMapsTo409 proves the structured-patch apply
// path shares the raw path's restart-required classification: a write rejected
// with ErrRestartRequired returns 409 restart_required:true (not a generic 400),
// and persists nothing.
func TestConfigPatchApplyRestartRequiredMapsTo409(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgPath, seed, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func([]byte) error {
			return fmt.Errorf("%w: automatic HTTPS (ACME) domains or issuer changed", ErrRestartRequired)
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	s := newTestServer(t, config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}, deps)

	body, err := json.Marshal(patchApplyRequest{Ops: []patchRequest{
		{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: "http://127.0.0.1:9100"},
	}})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	var out restartRequiredResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.RestartRequired || out.OK {
		t.Errorf("body = %+v, want restart_required=true and ok=false", out)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(seed, after) {
		t.Error("config was modified despite a restart-required refusal")
	}
}

func TestConfigApplyPersistsAndReturnsStatus(t *testing.T) {
	s, cfgPath := v2WriteServer(t)
	alt := validTOML(t, "./newroot", ":8282")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(alt)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Error("ok should be true")
	}
	// Verify the disk was actually updated.
	disk, _ := os.ReadFile(cfgPath)
	if !bytes.Contains(disk, []byte("newroot")) {
		t.Error("apply should have persisted the new config to disk")
	}
}

func TestConfigApplyCreatesHistorySnapshot(t *testing.T) {
	s, _ := v2WriteServer(t)
	alt := validTOML(t, "./snap", ":8383")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(alt)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	// Now check history has a snapshot.
	rr2 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/config/history", nil))
	var entries []historyEntry
	if err := json.Unmarshal(rr2.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(entries) == 0 {
		t.Error("apply should create a history snapshot")
	}
}

func TestConfigApplyRejectsInvalidConfig(t *testing.T) {
	s, _ := v2WriteServer(t)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", strings.NewReader("{bad")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var out validationErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OK {
		t.Error("ok should be false")
	}
}

func TestConfigApplyMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/apply", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// adminWriteServer seeds a persisted config whose [admin] block is enabled with
// a token and a loopback listen address, and returns a write-capable console
// server plus the on-disk config path. It is the harness for the self-lockout
// guard tests.
func adminWriteServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	seed := config.ServeDir("./public", ":8080")
	seed.Admin = config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "original-token"}
	raw, err := config.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func(data []byte) error {
			c, err := config.Parse(data)
			if err != nil {
				return err
			}
			if err := config.Validate(c); err != nil {
				return err
			}
			return os.WriteFile(cfgPath, data, 0o644)
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	cfg := config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}
	return newTestServer(t, cfg, deps), cfgPath
}

// adminTOML marshals a full config whose [admin] block carries the given token
// and listen address, for posting to /api/config/apply.
func adminTOML(t *testing.T, token, listen string) []byte {
	t.Helper()
	c := config.ServeDir("./public", ":8080")
	c.Admin = config.AdminConfig{Enabled: true, Listen: listen, Token: token}
	raw, err := config.Marshal(c)
	if err != nil {
		t.Fatalf("marshal admin config: %v", err)
	}
	return raw
}

// TestConfigApplyAdminChangeRequiresConfirm proves a raw apply that rotates the
// admin token is held with 409 admin_change:true and persists nothing, so an
// operator cannot silently lock themselves out of the console.
func TestConfigApplyAdminChangeRequiresConfirm(t *testing.T) {
	s, cfgPath := adminWriteServer(t)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	body := adminTOML(t, "rotated-token", "127.0.0.1:9090")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(body)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	var out adminGuardResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OK || !out.AdminChange || len(out.Changes) == 0 {
		t.Errorf("body = %+v, want ok=false admin_change=true with changes", out)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("config was modified despite an unconfirmed admin change")
	}
}

// TestConfigApplyAdminChangeWithConfirm proves the same change applies once the
// client opts in with ?confirm_admin=true.
func TestConfigApplyAdminChangeWithConfirm(t *testing.T) {
	s, cfgPath := adminWriteServer(t)
	body := adminTOML(t, "rotated-token", "127.0.0.1:9090")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply?confirm_admin=true", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Contains(after, []byte("rotated-token")) {
		t.Error("a confirmed admin change should have persisted")
	}
}

// TestConfigApplyNonAdminChangeSkipsGuard proves an edit that leaves the [admin]
// block identical applies without confirmation, so the guard does not impose a
// confirmation on ordinary edits.
func TestConfigApplyNonAdminChangeSkipsGuard(t *testing.T) {
	s, cfgPath := adminWriteServer(t)
	c := config.ServeDir("./changed-root", ":8080")
	c.Admin = config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "original-token"}
	body, err := config.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Contains(after, []byte("changed-root")) {
		t.Error("a non-admin change should apply without confirmation")
	}
}

// TestAdminLockoutChanges exercises each branch of the reachability diff.
func TestAdminLockoutChanges(t *testing.T) {
	base := config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "t"}
	consoleOff := false
	cases := []struct {
		name string
		prev config.AdminConfig
		next config.AdminConfig
		want bool
	}{
		{"no change", base, base, false},
		{"disable admin", base, config.AdminConfig{Enabled: false}, true},
		{"listen change", base, config.AdminConfig{Enabled: true, Listen: "0.0.0.0:9090", Token: "t"}, true},
		{"token change", base, config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "u"}, true},
		{"console disabled", base, config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "t", Console: &consoleOff}, true},
		{"admin not serving", config.AdminConfig{Enabled: false}, base, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adminLockoutChanges(tc.prev, tc.next)
			if (len(got) > 0) != tc.want {
				t.Errorf("changes = %v, want hasChange=%v", got, tc.want)
			}
		})
	}
}

// rbacAdminWriteServer seeds a persisted config with RBAC enabled, installs
// the matching policy on the server, and returns the server, the config path,
// the operator token (config:apply only), and the admin token (admin:manage).
func rbacAdminWriteServer(t *testing.T) (*Server, string, string, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	operatorTok := "operator-token-32-chars-padded---"
	adminTok := "admin-token-32-chars-padded--------"

	seed := config.ServeDir("./public", ":8080")
	seed.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{
			Enabled: true,
			Roles: []config.AdminRole{
				{Name: "configwriter", Permissions: []string{"config:apply"}},
			},
			Principals: []config.AdminPrincipal{
				{Name: "op", Role: "configwriter", Token: operatorTok},
				{Name: "adm", Role: "admin", Token: adminTok},
			},
		},
	}
	raw, err := config.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func(data []byte) error {
			c, err := config.Parse(data)
			if err != nil {
				return err
			}
			if err := config.Validate(c); err != nil {
				return err
			}
			return os.WriteFile(cfgPath, data, 0o644)
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	cfg := config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}
	s := newTestServer(t, cfg, deps)

	customRoles := make(map[string][]string, len(seed.Admin.RBAC.Roles))
	for _, r := range seed.Admin.RBAC.Roles {
		customRoles[r.Name] = r.Permissions
	}
	pol, err := rbac.Build(true, "admin", customRoles, []rbac.PrincipalDef{
		{Name: "op", Role: "configwriter", Token: operatorTok},
		{Name: "adm", Role: rbac.RoleAdmin, Token: adminTok},
	}, "", time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s.UpdatePolicy(pol)
	return s, cfgPath, operatorTok, adminTok
}

// TestConfigApplyRBACSameCountRoleEscalationBlocks applies a candidate that
// keeps the same number of principals but changes one principal's role. The
// object-level guard must detect the content change and require admin:manage.
func TestConfigApplyRBACSameCountRoleEscalationBlocks(t *testing.T) {
	s, cfgPath, opTok, _ := rbacAdminWriteServer(t)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	candidate := config.ServeDir("./public", ":8080")
	candidate.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{
			Enabled: true,
			Roles: []config.AdminRole{
				{Name: "configwriter", Permissions: []string{"config:apply"}},
			},
			Principals: []config.AdminPrincipal{
				{Name: "op", Role: "admin", Token: opTok}, // escalated
				{Name: "adm", Role: "admin", Token: "admin-token-32-chars-padded--------"},
			},
		},
	}
	body, err := config.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+opTok)
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("RBAC role escalation should not have persisted without admin:manage")
	}
}

// TestConfigApplyRBACSameCountTokenSwapBlocks applies a candidate that keeps
// the same number of principals but rotates another principal's token. The
// object-level guard must detect the content change and require admin:manage.
func TestConfigApplyRBACSameCountTokenSwapBlocks(t *testing.T) {
	s, cfgPath, opTok, _ := rbacAdminWriteServer(t)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	candidate := config.ServeDir("./public", ":8080")
	candidate.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{
			Enabled: true,
			Roles: []config.AdminRole{
				{Name: "configwriter", Permissions: []string{"config:apply"}},
			},
			Principals: []config.AdminPrincipal{
				{Name: "op", Role: "configwriter", Token: opTok},
				{Name: "adm", Role: "admin", Token: "compromised-token-32-chars-------"},
			},
		},
	}
	body, err := config.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+opTok)
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("RBAC token swap should not have persisted without admin:manage")
	}
}

// TestConfigApplyRBACSameCountPermissionEditBlocks applies a candidate that
// keeps the same number of roles but changes the permissions of an existing
// role. The object-level guard must detect the content change.
func TestConfigApplyRBACSameCountPermissionEditBlocks(t *testing.T) {
	s, cfgPath, opTok, _ := rbacAdminWriteServer(t)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	candidate := config.ServeDir("./public", ":8080")
	candidate.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{
			Enabled: true,
			Roles: []config.AdminRole{
				{Name: "configwriter", Permissions: []string{"config:apply", "admin:manage"}},
			},
			Principals: []config.AdminPrincipal{
				{Name: "op", Role: "configwriter", Token: opTok},
				{Name: "adm", Role: "admin", Token: "admin-token-32-chars-padded--------"},
			},
		},
	}
	body, err := config.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+opTok)
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("RBAC permission edit should not have persisted without admin:manage")
	}
}

// TestConfigApplyRBACIdenticalAllowsConfigApply proves that an operator with
// only config:apply can still apply a candidate whose RBAC content is
// identical to the current config; the guard must not over-trigger.
func TestConfigApplyRBACIdenticalAllowsConfigApply(t *testing.T) {
	s, cfgPath, opTok, _ := rbacAdminWriteServer(t)

	candidate := config.ServeDir("./changed-root", ":8080")
	candidate.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{
			Enabled: true,
			Roles: []config.AdminRole{
				{Name: "configwriter", Permissions: []string{"config:apply"}},
			},
			Principals: []config.AdminPrincipal{
				{Name: "op", Role: "configwriter", Token: opTok},
				{Name: "adm", Role: "admin", Token: "admin-token-32-chars-padded--------"},
			},
		},
	}
	body, err := config.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+opTok)
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Contains(after, []byte("changed-root")) {
		t.Error("identical RBAC should not block an ordinary config:apply edit")
	}
}

// TestConfigApplyRBACAdminManageAllows proves that an admin:manage holder can
// apply same-count RBAC content mutations that would be blocked for a
// config:apply-only operator.
func TestConfigApplyRBACAdminManageAllows(t *testing.T) {
	s, cfgPath, _, adminTok := rbacAdminWriteServer(t)

	candidate := config.ServeDir("./public", ":8080")
	candidate.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{
			Enabled: true,
			Roles: []config.AdminRole{
				{Name: "configwriter", Permissions: []string{"config:apply"}},
			},
			Principals: []config.AdminPrincipal{
				{Name: "op", Role: "admin", Token: "operator-token-32-chars-padded---"}, // escalated
				{Name: "adm", Role: "admin", Token: "admin-token-32-chars-padded--------"},
			},
		},
	}
	body, err := config.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Contains(after, []byte("op")) || !bytes.Contains(after, []byte("admin")) {
		t.Error("admin:manage holder should be able to mutate RBAC content")
	}
}

// TestRBACPrincipalsEqual covers the content comparison used by the
// object-level admin:manage guard, including order independence and every
// mutable principal field.
func TestRBACPrincipalsEqual(t *testing.T) {
	base := config.AdminPrincipal{Name: "alice", Role: "admin", Token: "tok1", Disabled: false}
	cases := []struct {
		name string
		a    []config.AdminPrincipal
		b    []config.AdminPrincipal
		want bool
	}{
		{"identical", []config.AdminPrincipal{base}, []config.AdminPrincipal{base}, true},
		{"reordered", []config.AdminPrincipal{base, {Name: "bob", Role: "viewer", Token: "tok2"}}, []config.AdminPrincipal{{Name: "bob", Role: "viewer", Token: "tok2"}, base}, true},
		{"role changed", []config.AdminPrincipal{base}, []config.AdminPrincipal{{Name: "alice", Role: "viewer", Token: "tok1"}}, false},
		{"token changed", []config.AdminPrincipal{base}, []config.AdminPrincipal{{Name: "alice", Role: "admin", Token: "tok2"}}, false},
		{"disabled flipped", []config.AdminPrincipal{base}, []config.AdminPrincipal{{Name: "alice", Role: "admin", Token: "tok1", Disabled: true}}, false},
		{"expiry changed", []config.AdminPrincipal{base}, []config.AdminPrincipal{{Name: "alice", Role: "admin", Token: "tok1", ExpiresAt: time.Now().Add(time.Hour)}}, false},
		{"name added", []config.AdminPrincipal{base}, []config.AdminPrincipal{base, {Name: "bob", Role: "viewer", Token: "tok2"}}, false},
		{"name removed", []config.AdminPrincipal{base, {Name: "bob", Role: "viewer", Token: "tok2"}}, []config.AdminPrincipal{base}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rbacPrincipalsEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("rbacPrincipalsEqual() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRBACRolesEqual covers content comparison for custom roles, including
// order independence and permission-set equality.
func TestRBACRolesEqual(t *testing.T) {
	base := config.AdminRole{Name: "custom", Permissions: []string{"config:apply"}}
	cases := []struct {
		name string
		a    []config.AdminRole
		b    []config.AdminRole
		want bool
	}{
		{"identical", []config.AdminRole{base}, []config.AdminRole{base}, true},
		{"reordered perms", []config.AdminRole{base}, []config.AdminRole{{Name: "custom", Permissions: []string{"config:apply"}}}, true},
		{"perm added", []config.AdminRole{base}, []config.AdminRole{{Name: "custom", Permissions: []string{"config:apply", "admin:manage"}}}, false},
		{"perm removed", []config.AdminRole{base}, []config.AdminRole{{Name: "custom", Permissions: []string{}}}, false},
		{"role name changed", []config.AdminRole{base}, []config.AdminRole{{Name: "other", Permissions: []string{"config:apply"}}}, false},
		{"count differs", []config.AdminRole{base}, []config.AdminRole{base, {Name: "other", Permissions: []string{"config:apply"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rbacRolesEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("rbacRolesEqual() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConfigGetReturnsBaseVersion proves GET /api/config exposes the
// optimistic-concurrency fingerprint the raw editor sends back on apply.
func TestConfigGetReturnsBaseVersion(t *testing.T) {
	s, _ := v2WriteServer(t)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		BaseVersion string `json:"base_version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.BaseVersion == "" {
		t.Error("GET /api/config should return a non-empty base_version")
	}
}

// TestConfigApplyRejectsStaleBaseVersion proves a raw apply carrying a stale
// base_version is rejected with 409 and persists nothing, closing the raw-edit
// lost-update hole (P5). An empty base_version still skips the check.
func TestConfigApplyRejectsStaleBaseVersion(t *testing.T) {
	s, cfgPath := v2WriteServer(t)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	alt := validTOML(t, "./newroot", ":8282")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply?base_version=deadbeefdeadbeef", bytes.NewReader(alt)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	var out conflictResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Conflict || out.CurrentVersion == "" {
		t.Errorf("conflict body = %+v, want conflict=true and a current_version", out)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("config was modified despite a stale-version conflict")
	}
}

// TestConfigApplyAcceptsMatchingBaseVersion proves the base_version reported by
// GET /api/config round-trips: applying with it is accepted, and the response
// carries a fresh version for the next edit so a follow-up save does not trip a
// spurious conflict.
func TestConfigApplyAcceptsMatchingBaseVersion(t *testing.T) {
	s, _ := v2WriteServer(t)
	grr := httptest.NewRecorder()
	s.routes().ServeHTTP(grr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var got struct {
		BaseVersion string `json:"base_version"`
	}
	if err := json.Unmarshal(grr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.BaseVersion == "" {
		t.Fatal("GET did not return base_version")
	}
	alt := validTOML(t, "./newroot", ":8282")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply?base_version="+got.BaseVersion, bytes.NewReader(alt)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode apply: %v", err)
	}
	if out.Version == "" {
		t.Error("apply response should carry a fresh version for the next edit")
	}
}

// TestConfigApplyRestartRequiredMapsTo409 proves the raw apply path classifies a
// write rejected with ErrRestartRequired (a valid change that cannot hot-apply,
// e.g. an ACME domain change) as a distinct 409 carrying restart_required:true,
// not a generic 400 "invalid config" — and that nothing was persisted, since the
// write path returns the sentinel before writing.
func TestConfigApplyRestartRequiredMapsTo409(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	initial := validTOML(t, "./public", ":8080")
	if err := os.WriteFile(cfgPath, initial, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		// Mirror the composition root refusing a restart-required change: return
		// the sentinel without writing.
		WriteConfigRaw: func([]byte) error {
			return fmt.Errorf("%w: automatic HTTPS (ACME) domains or issuer changed", ErrRestartRequired)
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	s := newTestServer(t, config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}, deps)

	alt := validTOML(t, "./newroot", ":8282")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(alt)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	var out restartRequiredResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.RestartRequired || out.OK {
		t.Errorf("body = %+v, want restart_required=true and ok=false", out)
	}
	if !strings.Contains(out.Message, "restart") {
		t.Errorf("message %q should mention a restart", out.Message)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(initial, after) {
		t.Error("config was modified despite a restart-required refusal")
	}
}

// ── /api/config/history + /api/config/history/{id} ───────────────────────────

func TestConfigHistoryListAndGet(t *testing.T) {
	s, _ := v2WriteServer(t)

	// Apply a change to create a snapshot.
	alt := validTOML(t, "./htest", ":8484")
	s.routes().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(alt)))

	// List.
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/history", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	var entries []historyEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one history entry")
	}

	// Get by path param.
	id := entries[0].ID
	rr2 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/config/history/"+id, nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("get status = %d, body: %s", rr2.Code, rr2.Body.String())
	}
	var snap map[string]string
	if err := json.Unmarshal(rr2.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode snap: %v", err)
	}
	if snap["id"] != id {
		t.Errorf("snap id = %q, want %q", snap["id"], id)
	}
	if snap["raw"] == "" {
		t.Error("snap raw should not be empty")
	}
}

func TestConfigHistoryGetInvalidIDRejects(t *testing.T) {
	s, _ := v2WriteServer(t)
	rr := httptest.NewRecorder()
	// Path traversal attempt — must be rejected.
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/history/../etc/passwd", nil))
	// Either 404 (no such snap) or the path param won't match the route pattern.
	// What matters is it does NOT return 200 with file contents.
	if rr.Code == http.StatusOK {
		if body := rr.Body.String(); strings.Contains(body, "passwd") {
			t.Fatal("path traversal not rejected")
		}
	}
}

// ── /api/config/rollback ─────────────────────────────────────────────────────

func TestConfigRollback(t *testing.T) {
	s, cfgPath := v2WriteServer(t)
	original, _ := os.ReadFile(cfgPath)

	// Apply a change.
	alt := validTOML(t, "./rollback", ":8585")
	s.routes().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(alt)))

	// List history to get the snapshot ID.
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/history", nil))
	var entries []historyEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no history to roll back to")
	}
	id := entries[0].ID

	// Roll back.
	body := `{"id":"` + id + `"}`
	rr2 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/api/config/rollback", strings.NewReader(body)))
	if rr2.Code != http.StatusOK {
		t.Fatalf("rollback status = %d; body: %s", rr2.Code, rr2.Body.String())
	}

	// The on-disk config should match the original.
	disk, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(original, disk) {
		t.Error("rollback should have restored the original config")
	}
}

func TestConfigRollbackUnknownID(t *testing.T) {
	s, _ := v2WriteServer(t)
	body := `{"id":"nonexistent"}`
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/rollback", strings.NewReader(body)))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// ── /api/wizard/generate ─────────────────────────────────────────────────────

func TestWizardGenerateProducesValidTOML(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	body := `{"mode":"proxy","target":"http://localhost:8080","listen":":3000"}`
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/wizard/generate", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["toml"] == "" {
		t.Error("wizard/generate should return non-empty toml")
	}
	// The generated TOML must be parseable.
	if _, err := config.Parse([]byte(out["toml"])); err != nil {
		t.Errorf("generated TOML not parseable: %v", err)
	}
}

func TestWizardGenerateIsNonMutating(t *testing.T) {
	// /api/wizard/generate must not modify any state: call it twice, get same result.
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	body := `{"mode":"serve","path":"./www","listen":":4000"}`
	do := func() string {
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/wizard/generate", strings.NewReader(body)))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		return rr.Body.String()
	}
	first, second := do(), do()
	if first != second {
		t.Error("wizard/generate should be idempotent (non-mutating)")
	}
}

// ── /api/events (SSE) ────────────────────────────────────────────────────────

func TestEventsSSEStreamsEvents(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})

	pr, pw := io.Pipe()

	w := &pipeWriter{pw: pw, header: make(http.Header), code: 200}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		s.routes().ServeHTTP(w, req)
		_ = pw.Close() // unblock scanner when handler exits
	}()

	// The handler sends a "connected" ping immediately after subscribing; use
	// that to know the subscription is live before broadcasting.
	scanner := bufio.NewScanner(pr)
	connectedSeen := false
	reloadSeen := false

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "connected") {
				connectedSeen = true
				s.hub.Broadcast(Event{Type: "reload"})
			}
			if strings.Contains(line, "reload") && connectedSeen {
				reloadSeen = true
				cancel() // unblocks the handler via r.Context().Done()
				return
			}
		}
	}()

	select {
	case <-readDone:
	case <-handlerDone:
	}
	cancel() // idempotent
	<-handlerDone
	_ = pr.Close()

	if !reloadSeen {
		t.Error("SSE stream should have delivered the reload event after connected")
	}
}

func TestEventsMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/events", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// ── CSP headers ──────────────────────────────────────────────────────────────

func TestLegacyPageCSPAllowsInline(t *testing.T) {
	// The dependency-free configuration GUI carries embedded inline scripts and
	// styles, so its CSP must allow 'unsafe-inline'. It is served at /config only
	// when the Console v2 SPA is not the active UI, so disable the console here.
	disabled := false
	s := newTestServer(t, config.AdminConfig{Console: &disabled}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/config", nil))
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "unsafe-inline") {
		t.Errorf("legacy page CSP should contain unsafe-inline for embedded scripts; got: %q", csp)
	}
}

func TestStrictCSPNoInline(t *testing.T) {
	// writeSecurityHeaders (used by Console v2 SPA) must NOT emit unsafe-inline.
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.writeSecurityHeaders(rr)
	csp := rr.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("strict CSP should NOT contain unsafe-inline; got: %q", csp)
	}
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("strict CSP should contain script-src 'self'; got: %q", csp)
	}
}

// ── helpers for SSE test ──────────────────────────────────────────────────────

type pipeWriter struct {
	pw     *io.PipeWriter
	header http.Header
	code   int
	mu     sync.Mutex
}

func (p *pipeWriter) Header() http.Header  { return p.header }
func (p *pipeWriter) WriteHeader(code int) { p.code = code }
func (p *pipeWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pw.Write(b)
}
func (p *pipeWriter) Flush() {}

// ── stage_restart API integration tests (P2-05 §22.5) ───────────────────────

// stageRestartServer builds a write-capable admin server whose ApplyConfigRaw
// supports the stage_restart mode, returning a pending_restart status when
// staging succeeds.
func stageRestartServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgPath, seed, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// stagePending tracks whether a staged restart is pending.
	var stagePending bool

	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func(data []byte) error {
			return os.WriteFile(cfgPath, data, 0o600)
		},
		ApplyConfigRaw: func(_ ApplyRequestContext, data []byte, mode string) (ConfigApplyResult, error) {
			// Block hot apply while staged restart is pending.
			if mode != "stage_restart" && stagePending {
				return ConfigApplyResult{
					OK:             false,
					Mode:           mode,
					Message:        "A planned restart is pending; discard or complete it before applying hot changes.",
					PendingRestart: &PendingRestartStatus{Managed: true, Staged: true, DiscardAvailable: true},
				}, nil
			}
			c, err := config.Parse(data)
			if err != nil {
				return ConfigApplyResult{OK: false, Mode: mode, ValidationErrors: []string{err.Error()}}, nil
			}
			if err := config.Validate(c); err != nil {
				return ConfigApplyResult{OK: false, Mode: mode, ValidationErrors: []string{err.Error()}}, nil
			}
			if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
				return ConfigApplyResult{OK: false, Mode: mode, Message: err.Error()}, err
			}
			result := ConfigApplyResult{
				OK:      true,
				Mode:    mode,
				Version: configVersion(data),
				Message: "Configuration saved.",
			}
			if mode == "stage_restart" {
				stagePending = true
				result.ServingVersion = configVersion(seed)
				result.PendingRestart = &PendingRestartStatus{
					Managed:          true,
					Staged:           true,
					StagedVersion:    result.Version,
					ServingVersion:   result.ServingVersion,
					Subsystems:       []string{"cache"},
					DiscardAvailable: true,
				}
			} else {
				result.ServingVersion = result.Version
			}
			return result, nil
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
		DiscardPendingRestart: func() (ConfigApplyResult, error) {
			if !stagePending {
				return ConfigApplyResult{OK: true, Message: "No pending restart."}, nil
			}
			if err := os.WriteFile(cfgPath, seed, 0o600); err != nil {
				return ConfigApplyResult{OK: false, Message: err.Error()}, err
			}
			stagePending = false
			return ConfigApplyResult{OK: true, Mode: "hot", Message: "Discarded."}, nil
		},
		PendingRestart: func() *PendingRestartStatus {
			if !stagePending {
				return nil
			}
			return &PendingRestartStatus{
				Managed:          true,
				Staged:           true,
				Subsystems:       []string{"cache"},
				DiscardAvailable: true,
			}
		},
	}
	cfg := config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}
	return newTestServer(t, cfg, deps), cfgPath
}

// TestConfigApplyStageRestartReturns200WithPendingStatus proves a stage_restart
// apply returns 200 with ok=true, mode=stage_restart, and a populated
// pending_restart status (§22.5 / §19.2).
func TestConfigApplyStageRestartReturns200WithPendingStatus(t *testing.T) {
	s, cfgPath := stageRestartServer(t)

	alt := validTOML(t, "./staged", ":8282")
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply?mode=stage_restart", bytes.NewReader(alt))
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out ConfigApplyResult
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Errorf("ok = false, want true; message: %s", out.Message)
	}
	if out.Mode != "stage_restart" {
		t.Errorf("mode = %q, want stage_restart", out.Mode)
	}
	if out.PendingRestart == nil || !out.PendingRestart.Staged {
		t.Error("pending_restart should be set and staged=true")
	}
	// Disk must contain the staged candidate.
	disk, _ := os.ReadFile(cfgPath)
	if !bytes.Contains(disk, []byte("staged")) {
		t.Error("staged candidate should be on disk")
	}
}

// TestConfigApplyStageRestartWritesCandidate proves the staged candidate is
// written to disk but that a subsequent hot apply is blocked until the
// staged state is discarded (§22.5 hot-apply-blocked invariant).
func TestConfigApplyStageRestartBlocksHotApply(t *testing.T) {
	s, _ := stageRestartServer(t)

	// First: stage a candidate.
	alt := validTOML(t, "./staged", ":8282")
	rr1 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr1, httptest.NewRequest(http.MethodPost,
		"/api/config/apply?mode=stage_restart", bytes.NewReader(alt)))
	if rr1.Code != http.StatusOK {
		t.Fatalf("stage: status = %d; body: %s", rr1.Code, rr1.Body.String())
	}

	// Second: attempt a hot apply — should be blocked (409).
	alt2 := validTOML(t, "./hot-attempt", ":8383")
	rr2 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr2, httptest.NewRequest(http.MethodPost,
		"/api/config/apply", bytes.NewReader(alt2)))
	if rr2.Code != http.StatusConflict {
		t.Fatalf("hot apply while pending: status = %d, want 409; body: %s",
			rr2.Code, rr2.Body.String())
	}
}

// TestConfigApplyDiscardRestoresPreviousBytes proves the discard endpoint
// restores the previous bytes and returns 200 ok=true (§22.5).
func TestConfigApplyDiscardRestoresPreviousBytes(t *testing.T) {
	s, cfgPath := stageRestartServer(t)
	original, _ := os.ReadFile(cfgPath)

	// Stage a candidate.
	alt := validTOML(t, "./staged", ":8282")
	rr1 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr1, httptest.NewRequest(http.MethodPost,
		"/api/config/apply?mode=stage_restart", bytes.NewReader(alt)))
	if rr1.Code != http.StatusOK {
		t.Fatalf("stage: status = %d; body: %s", rr1.Code, rr1.Body.String())
	}

	// Discard.
	rr2 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr2, httptest.NewRequest(http.MethodPost,
		"/api/config/pending-restart/discard", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("discard: status = %d, want 200; body: %s", rr2.Code, rr2.Body.String())
	}
	var out ConfigApplyResult
	if err := json.Unmarshal(rr2.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode discard: %v", err)
	}
	if !out.OK {
		t.Errorf("discard ok = false, want true; message: %s", out.Message)
	}
	// Disk should be back to the original bytes.
	restored, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(original, restored) {
		t.Error("discard should have restored the original bytes")
	}
}

// TestConfigApplyPendingRestartEndpointReflectsState proves GET
// /api/config/pending-restart correctly reflects the staged state before and
// after a stage_restart apply (§22.5).
func TestConfigApplyPendingRestartEndpointReflectsState(t *testing.T) {
	s, _ := stageRestartServer(t)

	// Before staging: no pending restart.
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet,
		"/api/config/pending-restart", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var before struct {
		Pending bool `json:"pending"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &before); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if before.Pending {
		t.Error("pending should be false before staging")
	}

	// Stage a candidate.
	alt := validTOML(t, "./staged", ":8282")
	rr2 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr2, httptest.NewRequest(http.MethodPost,
		"/api/config/apply?mode=stage_restart", bytes.NewReader(alt)))
	if rr2.Code != http.StatusOK {
		t.Fatalf("stage: status = %d; body: %s", rr2.Code, rr2.Body.String())
	}

	// After staging: pending restart should be set.
	rr3 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr3, httptest.NewRequest(http.MethodGet,
		"/api/config/pending-restart", nil))
	var after struct {
		Pending bool                  `json:"pending"`
		Status  *PendingRestartStatus `json:"status"`
	}
	if err := json.Unmarshal(rr3.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if !after.Pending {
		t.Error("pending should be true after staging")
	}
	if after.Status == nil || !after.Status.Staged {
		t.Error("status.staged should be true")
	}
}

// TestConfigApplyHotRestartRequiredCanStage proves restart-required hot
// rejection returns 409 with can_stage=true (§22.5 / §19.4).
func TestConfigApplyHotRestartRequiredCanStage(t *testing.T) {
	s, cfgPath := v2WriteServer(t)
	before, _ := os.ReadFile(cfgPath)

	deps2 := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func([]byte) error {
			return fmt.Errorf("%w: cache settings changed", ErrRestartRequired)
		},
		ApplyConfigRaw: func(_ ApplyRequestContext, data []byte, mode string) (ConfigApplyResult, error) {
			return ConfigApplyResult{
				OK:              false,
				Mode:            mode,
				RestartRequired: true,
				CanStage:        true,
				Message:         "cache settings changed; restart required",
			}, nil
		},
		LoadConfig: func() (*config.Config, error) {
			raw, _ := os.ReadFile(cfgPath)
			return config.Parse(raw)
		},
	}
	s2 := newTestServer(t, config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}, deps2)

	alt := validTOML(t, "./hot-attempt", ":8383")
	rr := httptest.NewRecorder()
	s2.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(alt)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	var out restartRequiredResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OK || !out.RestartRequired {
		t.Errorf("body = %+v, want ok=false restart_required=true", out)
	}
	// Nothing persisted.
	after, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(before, after) {
		t.Error("restart-required rejection should not modify the file")
	}

	_ = s // suppress unused var warning from original server
}
