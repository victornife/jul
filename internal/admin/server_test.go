// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/observability"
)

// fakePurger records purge operations for assertions.
type fakePurger struct {
	purged  atomic.Int64
	deleted atomic.Value // last deleted key
}

func (f *fakePurger) Purge()            { f.purged.Add(1) }
func (f *fakePurger) Delete(key string) { f.deleted.Store(key) }

func newTestServer(t *testing.T, cfg config.AdminConfig, deps Deps) *Server {
	t.Helper()
	cfg.Enabled = true
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	s := New(cfg, nil, deps)
	if s == nil {
		t.Fatal("New returned nil for enabled config")
	}
	return s
}

func TestNewDisabledReturnsNil(t *testing.T) {
	if New(config.AdminConfig{Enabled: false}, nil, Deps{}) != nil {
		t.Fatal("expected nil server when admin disabled")
	}
}

func TestHealthzAndReadyz(t *testing.T) {
	var ready atomic.Bool
	s := newTestServer(t, config.AdminConfig{}, Deps{Ready: ready.Load})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", rr.Code)
	}

	// Not ready yet.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz (not ready) = %d, want 503", rr.Code)
	}

	ready.Store(true)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("readyz (ready) = %d, want 200", rr.Code)
	}
}

func TestBearerAuth(t *testing.T) {
	purger := &fakePurger{}
	s := newTestServer(t, config.AdminConfig{Token: "secret"}, Deps{Cache: purger})
	h := s.routes()

	// Missing token -> 401.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/cache/purge", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", rr.Code)
	}

	// Wrong token -> 401.
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/cache/purge", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", rr.Code)
	}

	// Correct token -> 200 and purge invoked.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/cache/purge", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("good token = %d, want 200", rr.Code)
	}
	if purger.purged.Load() != 1 {
		t.Fatalf("Purge called %d times, want 1", purger.purged.Load())
	}
}

func TestPurgeMethodAndKey(t *testing.T) {
	purger := &fakePurger{}
	s := newTestServer(t, config.AdminConfig{}, Deps{Cache: purger})
	h := s.routes()

	// GET not allowed.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/cache/purge", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET purge = %d, want 405", rr.Code)
	}

	// Key-scoped purge -> Delete.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/cache/purge?key=GET%5Cnexample%5Cn/a", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("key purge = %d, want 200", rr.Code)
	}
	if got, _ := purger.deleted.Load().(string); got == "" {
		t.Fatal("expected Delete to be called with a key")
	}
	if purger.purged.Load() != 0 {
		t.Fatal("full Purge should not run for key-scoped request")
	}
}

func TestReloadTriggersHook(t *testing.T) {
	var called atomic.Int64
	s := newTestServer(t, config.AdminConfig{}, Deps{Reload: func() { called.Add(1) }})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/reload", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("reload = %d, want 202", rr.Code)
	}
	if called.Load() != 1 {
		t.Fatalf("reload hook called %d times, want 1", called.Load())
	}

	// GET not allowed.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/reload", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET reload = %d, want 405", rr.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	metricsBody := "jul_test 1\n"
	metricsH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, metricsBody)
	})
	s := newTestServer(t, config.AdminConfig{}, Deps{Metrics: metricsH})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("metrics = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "jul_test") {
		t.Fatalf("metrics body missing expected content: %q", rr.Body.String())
	}
}

func TestMetricsDisabled(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	h := s.routes()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("metrics disabled = %d, want 404", rr.Code)
	}
}

func TestUIServed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("ui = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Jul.IA") {
		t.Fatalf("ui body missing brand: %q", rr.Body.String()[:60])
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))
	if consoleV2Compiled {
		// With the SPA active, unknown paths are client-side routes: the shell is
		// served (200) so deep links and refreshes boot the app.
		if rr.Code != http.StatusOK {
			t.Fatalf("unknown path (SPA) = %d, want 200", rr.Code)
		}
	} else if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", rr.Code)
	}
}

func TestConfigGet(t *testing.T) {
	raw := []byte("# sample\n")
	cfg := &config.Config{}
	cfg.Global.LogLevel = "info"
	cfg.Global.LogFormat = "json"
	s := newTestServer(t, config.AdminConfig{}, Deps{
		Product:       "Jul.IA",
		Version:       "9.9.9",
		ConfigPath:    "server.toml",
		ReadConfigRaw: func() ([]byte, error) { return raw, nil },
		LoadConfig:    func() (*config.Config, error) { return cfg, nil },
		SaveConfig:    func(*config.Config) error { return nil },
	})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("config get = %d, want 200", rr.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["product"] != "Jul.IA" || got["version"] != "9.9.9" {
		t.Fatalf("metadata mismatch: %v", got)
	}
	if got["raw"] != "# sample\n" {
		t.Fatalf("raw mismatch: %v", got["raw"])
	}
	st, ok := got["settings"].(map[string]any)
	if !ok || st["log_format"] != "json" {
		t.Fatalf("settings mismatch: %v", got["settings"])
	}
}

func TestConfigSettingsApply(t *testing.T) {
	cfg := &config.Config{}
	cfg.Global.LogLevel = "info"
	var saved *config.Config
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
		SaveConfig: func(c *config.Config) error { saved = c; return nil },
	})
	h := s.routes()

	body := `{"log_level":"warn","log_format":"json","shutdown_timeout":"15s","cache_enabled":true,"cache_default_ttl":"30s","cache_memory_max_size":"32m","admin_listen":"127.0.0.1:9090"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/settings", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("settings apply = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if saved == nil {
		t.Fatal("SaveConfig was not called")
	}
	if saved.Global.LogLevel != "warn" || saved.Global.ShutdownTimeout.Std() != 15*time.Second {
		t.Fatalf("settings not applied: %+v", saved.Global)
	}
}

func TestConfigSettingsBadInput(t *testing.T) {
	cfg := &config.Config{}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
		SaveConfig: func(*config.Config) error { return nil },
	})
	h := s.routes()

	body := `{"shutdown_timeout":"not-a-duration"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/settings", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad settings = %d, want 400", rr.Code)
	}
}

func TestConfigRawValidatesBeforeSaving(t *testing.T) {
	var written []byte
	s := newTestServer(t, config.AdminConfig{}, Deps{
		WriteConfigRaw: func(b []byte) error { written = b; return nil },
	})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/raw", strings.NewReader("listen = \":80\"\n")))
	if rr.Code != http.StatusOK {
		t.Fatalf("raw save = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if string(written) != "listen = \":80\"\n" {
		t.Fatalf("raw not forwarded: %q", written)
	}
}

func TestConfigEndpointsRequireAuth(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Token: "secret"}, Deps{
		LoadConfig:     func() (*config.Config, error) { return &config.Config{}, nil },
		SaveConfig:     func(*config.Config) error { return nil },
		ReadConfigRaw:  func() ([]byte, error) { return nil, nil },
		WriteConfigRaw: func([]byte) error { return nil },
	})
	h := s.routes()

	for _, path := range []string{"/api/config", "/api/config/raw", "/api/config/settings"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token = %d, want 401", path, rr.Code)
		}
	}
}

func TestStatsEndpointWithHook(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		Stats: func() observability.StatsSnapshot {
			return observability.StatsSnapshot{Available: true, RequestsTotal: 42, RequestsPerSec: 3.5}
		},
	})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("stats = %d, want 200", rr.Code)
	}
	var got observability.StatsSnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Available || got.RequestsTotal != 42 || got.RequestsPerSec != 3.5 {
		t.Fatalf("stats payload mismatch: %+v", got)
	}
}

func TestStatsEndpointNoHook(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("stats (no hook) = %d, want 200", rr.Code)
	}
	var got observability.StatsSnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Available {
		t.Fatalf("expected unavailable snapshot when no Stats hook, got %+v", got)
	}
}

func TestStatsRequiresAuth(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Token: "secret"}, Deps{
		Stats: func() observability.StatsSnapshot { return observability.StatsSnapshot{Available: true} },
	})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("stats without token = %d, want 401", rr.Code)
	}
}

// TestConfigPageReachable verifies that /config and /ui resolve to a 200 admin
// surface. In the lean build (or with the console disabled) they serve the
// dependency-free configuration GUI; with the Console v2 SPA active they serve
// the SPA shell so a hard refresh on those client-side routes resolves.
func TestConfigPageReachable(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{Product: "Jul.IA"})
	h := s.routes()

	spaActive := consoleV2Compiled // the default config leaves the console enabled
	for _, path := range []string{"/config", "/ui"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, rr.Code)
		}
		body := rr.Body.String()
		if spaActive {
			if !strings.Contains(body, `id="root"`) {
				t.Fatalf("%s did not serve the SPA shell", path)
			}
		} else if !strings.Contains(body, "Advanced (raw TOML)") {
			t.Fatalf("%s did not serve the configuration page", path)
		}
	}
}

// TestRootServesConfigWithoutConsoleTag verifies that in the default build
// (console compiled out) the admin root serves the configuration page.
func TestRootServesConfigWithoutConsoleTag(t *testing.T) {
	if consoleV2Compiled {
		t.Skip("console compiled in; covered by console_test.go")
	}
	enabled := true
	s := newTestServer(t, config.AdminConfig{Console: &enabled}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("root = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Advanced (raw TOML)") {
		t.Fatal("root should serve the configuration page without the console tag")
	}
}

func TestAdminHTMLSecurityHeaders(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rr.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("missing/weak CSP: %q", got)
	}
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options: nosniff")
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing X-Frame-Options: DENY")
	}
}
