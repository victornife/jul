// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

// validTOML returns a marshaled, validate-passing starter config for tests.
func validTOML(t *testing.T, dir, listen string) []byte {
	t.Helper()
	raw, err := config.Marshal(config.ServeDir(dir, listen))
	if err != nil {
		t.Fatalf("marshal starter config: %v", err)
	}
	return raw
}

func TestUpstreamsAPI(t *testing.T) {
	want := []UpstreamStatus{{
		Name:     "api",
		Strategy: "round_robin",
		Backends: []BackendStatus{
			{Address: "10.0.0.1:80", Weight: 1, Healthy: true, Inflight: 2},
			{Address: "10.0.0.2:80", Weight: 3, Healthy: false, Inflight: 0},
		},
	}}
	s := newTestServer(t, config.AdminConfig{}, Deps{Upstreams: func() []UpstreamStatus { return want }})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/upstreams", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got []UpstreamStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Name != "api" || len(got[0].Backends) != 2 {
		t.Fatalf("unexpected payload: %+v", got)
	}
	if got[0].Backends[1].Healthy {
		t.Error("second backend should be unhealthy")
	}
}

func TestUpstreamsAPINilHookReturnsEmptyArray(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/upstreams", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", rr.Body.String())
	}
}

func TestCertsAPI(t *testing.T) {
	want := []CertStatus{{ServerNames: []string{"example.com"}, Source: "file", Subject: "example.com"}}
	s := newTestServer(t, config.AdminConfig{}, Deps{Certs: func() []CertStatus { return want }})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/certs", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got []CertStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Source != "file" {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestWizardGeneratesValidConfig(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	h := s.routes()

	cases := []struct {
		name string
		body string
		want string
	}{
		{"serve", `{"mode":"serve","path":"./public","listen":":8080"}`, "[[servers]]"},
		{"proxy", `{"mode":"proxy","target":":3000"}`, "proxy_pass"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/wizard", strings.NewReader(tc.body))
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
			}
			var resp struct {
				TOML string `json:"toml"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !strings.Contains(resp.TOML, tc.want) {
				t.Errorf("generated TOML missing %q:\n%s", tc.want, resp.TOML)
			}
			// The generated config must itself pass validation.
			c, err := config.Parse([]byte(resp.TOML))
			if err != nil {
				t.Fatalf("parse generated: %v", err)
			}
			if err := config.Validate(c); err != nil {
				t.Fatalf("generated config invalid: %v", err)
			}
		})
	}
}

func TestStatusAPI(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:        ":8443",
			TLS:           &config.TLSConfig{Enabled: true, ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: "ca.pem"}},
			HTTP3:         &config.HTTP3Config{Enabled: true},
			ClientAddress: &config.ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}},
			Locations: []config.LocationConfig{
				{Root: "./public", Cache: true},
				{ProxyPass: "http://127.0.0.1:9000", Auth: &config.AuthConfig{}, RequireClientCert: true},
				{GRPCTranscode: &config.GRPCTranscodeConfig{Target: "127.0.0.1:50051"}},
			},
		}},
		Upstreams: []config.UpstreamConfig{{
			Name:        "api",
			HealthCheck: &config.HealthCheckConfig{Enabled: true},
			Discovery:   &config.DiscoveryConfig{Type: "dns"},
		}},
		Compression: config.CompressionConfig{Enabled: config.Bool(true), Encoders: []string{"gzip", "br"}},
		RateLimit:   config.RateLimitConfig{Enabled: true, Rate: 100},
		Cache:       config.CacheConfig{Enabled: true},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		Metrics:    http.NewServeMux(),
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})

	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got []FeatureStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	active := map[string]bool{}
	for _, f := range got {
		active[f.Name] = f.Active
	}
	wantActive := []string{
		"Virtual hosts", "Static file serving", "Reverse proxy", "Response cache",
		"Compression", "Rate limiting", "TLS", "Mutual TLS (client certs)",
		"Access control (auth)", "Trusted client address", "HTTP/3 (QUIC)",
		"gRPC transcoding", "Upstream pools", "Active health checks", "Service discovery",
		"Prometheus metrics", "Access log", "Backend dial-failure accounting",
	}
	for _, name := range wantActive {
		if v, ok := active[name]; !ok {
			t.Errorf("missing capability %q", name)
		} else if !v {
			t.Errorf("capability %q = inactive, want active", name)
		}
	}
	wantInactive := []string{"FastCGI / uWSGI", "gRPC passthrough", "L4 stream proxy", "WASM plugins", "Secret references", "Web application firewall (WAF)"}
	for _, name := range wantInactive {
		if active[name] {
			t.Errorf("capability %q = active, want inactive", name)
		}
	}
}

func TestStatusAPIAccessLogDisabled(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{Listen: ":80"}},
		Observability: config.ObservabilityConfig{AccessLog: config.AccessLogConfig{
			Enabled: config.Bool(false),
			Sinks:   []string{"file"},
			File:    "/var/log/jul/access.log",
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{LoadConfig: func() (*config.Config, error) { return cfg, nil }})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got []FeatureStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, feature := range got {
		if feature.Name == "Access log" {
			if feature.Active {
				t.Fatal("disabled access log reported active")
			}
			if !strings.Contains(feature.Detail, "dormant sinks: file") {
				t.Fatalf("detail = %q, want dormant sink detail", feature.Detail)
			}
			return
		}
	}
	t.Fatal("Access log feature missing")
}

func TestStatusAPINilHookReturnsEmptyArray(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", rr.Body.String())
	}
}

func TestStatusAPIMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/status", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestWizardRejectsBadInput(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	h := s.routes()
	bad := []string{
		`{"mode":"bogus"}`,
		`{"mode":"serve"}`, // missing path
		`{"mode":"proxy"}`, // missing target
		`{not json`,        // malformed
	}
	for _, body := range bad {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/wizard", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rr.Code)
		}
	}
}

func TestWizardMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/wizard", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// historyTestServer wires a file-backed validated write path with history,
// mirroring the composition root, so the snapshot-on-write behavior is exercised
// end to end through the HTTP API.
func historyTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	initial := validTOML(t, "./public", ":8080")
	if err := os.WriteFile(cfgPath, initial, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		LoadConfig: func() (*config.Config, error) {
			return config.Parse(initial)
		},
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
	}
	cfg := config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}
	return newTestServer(t, cfg, deps), cfgPath
}

func TestHistorySnapshotOnWriteAndRollback(t *testing.T) {
	s, cfgPath := historyTestServer(t)
	h := s.routes()
	initial, _ := os.ReadFile(cfgPath)

	// 1) Save a new config via the raw write path; the prior config is snapshotted.
	updated := validTOML(t, "./web", ":9090")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/raw", bytes.NewReader(updated)))
	if rr.Code != http.StatusOK {
		t.Fatalf("write status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}

	// 2) History now lists exactly one snapshot (the prior config).
	var list []historyEntry
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("history list status = %d", rr.Code)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("history len = %d, want 1", len(list))
	}
	snapID := list[0].ID

	// 3) The snapshot content equals the original config.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/history/get?id="+snapID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("history get status = %d", rr.Code)
	}
	var got struct{ Raw string }
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.Raw != string(initial) {
		t.Errorf("snapshot content mismatch:\n got %q\nwant %q", got.Raw, initial)
	}

	// 4) Roll back; the file returns to the original and a fresh snapshot is taken.
	rr = httptest.NewRecorder()
	body := strings.NewReader(`{"id":"` + snapID + `"}`)
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/history/rollback", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("rollback status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	after, _ := os.ReadFile(cfgPath)
	if string(after) != string(initial) {
		t.Errorf("after rollback config = %q, want %q", after, initial)
	}
	// Rollback snapshots the pre-rollback config, so there are now two entries.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Errorf("history len after rollback = %d, want 2", len(list))
	}
}

func TestHistoryRollbackUnknownID(t *testing.T) {
	s, _ := historyTestServer(t)
	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"id":"20990101T000000.000Z"}`)
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/history/rollback", body))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestConsoleAPIRequiresToken(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Token: "secret"}, Deps{
		Upstreams: func() []UpstreamStatus { return nil },
		Certs:     func() []CertStatus { return nil },
	})
	h := s.routes()
	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/upstreams"},
		{http.MethodGet, "/api/certs"},
		{http.MethodGet, "/api/history"},
		{http.MethodGet, "/api/status"},
		{http.MethodPost, "/api/wizard"},
		{http.MethodPost, "/api/history/rollback"},
	}
	for _, p := range paths {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(p.method, p.path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without token = %d, want 401", p.method, p.path, rr.Code)
		}
	}
}
