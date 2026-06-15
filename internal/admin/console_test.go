//go:build console

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func boolPtr(b bool) *bool { return &b }

// TestRootServesConsoleWhenEnabled verifies that with the console tag and
// console enabled (the default), the admin root serves the console shell.
func TestRootServesConsoleWhenEnabled(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Console: boolPtr(true)}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("root = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Jul.IA Console") || !strings.Contains(body, `id="view-dashboard"`) {
		t.Fatal("root should serve the console shell when console is enabled")
	}
}

// TestRootServesConsoleByDefault verifies the unset (nil) console flag defaults
// to enabled so the console shell is served when compiled in.
func TestRootServesConsoleByDefault(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rr.Body.String(), "Jul.IA Console") {
		t.Fatal("console should be served by default (nil flag) when compiled in")
	}
}

// TestRootServesConfigWhenConsoleDisabled verifies that an explicit
// console = false serves the configuration page even when compiled in.
func TestRootServesConfigWhenConsoleDisabled(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Console: boolPtr(false)}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("root = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Advanced (raw TOML)") {
		t.Fatal("root should serve the configuration page when console = false")
	}
}

// TestConfigPageStillReachableWithConsoleOn verifies the editor stays at
// /config even when the console takes over the root.
func TestConfigPageStillReachableWithConsoleOn(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Console: boolPtr(true)}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/config = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Advanced (raw TOML)") {
		t.Fatal("/config should always serve the configuration page")
	}
}
