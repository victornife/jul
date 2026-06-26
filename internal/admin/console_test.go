//go:build console

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

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
	if !strings.Contains(rr.Body.String(), `id="root"`) {
		t.Fatal("root should serve the Console v2 SPA shell when console is enabled")
	}
}

// TestRootServesConsoleByDefault verifies the unset (nil) console flag defaults
// to enabled so the console shell is served when compiled in.
func TestRootServesConsoleByDefault(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rr.Body.String(), `id="root"`) {
		t.Fatal("the SPA shell should be served by default (nil flag) when compiled in")
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

// TestConfigRouteServesSpaWhenConsoleOn verifies that with the console active
// the /config client-side route resolves to the SPA shell, so a hard refresh
// there boots the app instead of 404ing.
func TestConfigRouteServesSpaWhenConsoleOn(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Console: boolPtr(true)}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/config = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `id="root"`) {
		t.Fatal("/config should serve the SPA shell when the console is active")
	}
}

// cspStyleNonce extracts the style-src nonce from a CSP header, or "" if absent.
func cspStyleNonce(csp string) string {
	const marker = "style-src 'self' 'nonce-"
	i := strings.Index(csp, marker)
	if i < 0 {
		return ""
	}
	rest := csp[i+len(marker):]
	j := strings.IndexByte(rest, '\'')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// TestConsoleV2IndexInjectsStyleNonce verifies the SPA shell advertises a CSP
// style nonce that matches the injected <meta name="csp-nonce"> tag, and that a
// fresh nonce is minted per response.
func TestConsoleV2IndexInjectsStyleNonce(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Console: boolPtr(true)}, Deps{Product: "Jul.IA"})
	h := s.routes()

	serve := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		return rr
	}

	rr := serve()
	if rr.Code != http.StatusOK {
		t.Fatalf("root = %d, want 200", rr.Code)
	}
	nonce := cspStyleNonce(rr.Header().Get("Content-Security-Policy"))
	if nonce == "" {
		t.Fatalf("CSP missing style nonce: %q", rr.Header().Get("Content-Security-Policy"))
	}
	if want := `<meta name="csp-nonce" content="` + nonce + `"`; !strings.Contains(rr.Body.String(), want) {
		t.Fatalf("shell body missing matching nonce meta %q", nonce)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("shell Cache-Control = %q, want no-store", cc)
	}

	if nonce2 := cspStyleNonce(serve().Header().Get("Content-Security-Policy")); nonce2 == nonce {
		t.Fatal("style nonce must be unique per response")
	}
}

// TestConsoleV2ClientRouteFallsBackToShell verifies that an unknown deep path
// (a client-side route) serves the SPA shell with a nonce rather than 404.
func TestConsoleV2ClientRouteFallsBackToShell(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Console: boolPtr(true)}, Deps{Product: "Jul.IA"})
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("client route = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `id="root"`) {
		t.Fatal("client route should serve the SPA shell (root div)")
	}
	if cspStyleNonce(rr.Header().Get("Content-Security-Policy")) == "" {
		t.Fatal("client route shell should carry a style nonce")
	}
}

// TestConsoleV2HashedAssetUsesStrictPolicy verifies that content-addressed
// assets are served with the strict nonce-free style policy.
func TestConsoleV2HashedAssetUsesStrictPolicy(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Console: boolPtr(true)}, Deps{Product: "Jul.IA"})
	h := s.routes()

	idx := httptest.NewRecorder()
	h.ServeHTTP(idx, httptest.NewRequest(http.MethodGet, "/", nil))
	asset := firstAssetPath(t, idx.Body.String())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, asset, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("asset %s = %d, want 200", asset, rr.Code)
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'self';") {
		t.Fatalf("asset CSP should use strict style-src 'self': %q", csp)
	}
	if cspStyleNonce(csp) != "" {
		t.Fatalf("hashed asset should not carry a style nonce: %q", csp)
	}
}

// firstAssetPath extracts the first /assets/* URL from the shell.
func firstAssetPath(t *testing.T, body string) string {
	t.Helper()
	const marker = "/assets/"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no asset reference found in shell body")
	}
	rest := body[i:]
	j := strings.IndexAny(rest, `"'`)
	if j < 0 {
		t.Fatalf("unterminated asset reference in shell body")
	}
	return rest[:j]
}
