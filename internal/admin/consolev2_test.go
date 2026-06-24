package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"jul/internal/config"
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

func TestRuntimeOverviewMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runtime/overview", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
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
	cfg := &config.Config{
		Upstreams: []config.UpstreamConfig{{
			Name:     "api",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "10.0.0.1:80", Weight: 1},
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
	if len(out[0].Backends) != 1 || !out[0].Backends[0].Healthy {
		t.Error("backend should be marked healthy from live data")
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

// ── /api/traffic-controls ─────────────────────────────────────────────────────

func TestTrafficControlsProjection(t *testing.T) {
	cfg := &config.Config{
		Compression: config.CompressionConfig{Enabled: true, Encoders: []string{"gzip", "br"}},
		RateLimit:   config.RateLimitConfig{Enabled: true, Rate: 100, Key: "ip"},
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
		Reload:         func() { reloads++ },
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

// ── /api/config/apply ────────────────────────────────────────────────────────

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
	if do() != do() {
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
