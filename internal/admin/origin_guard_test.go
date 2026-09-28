// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func refusedByOriginGuard(rr *httptest.ResponseRecorder) bool {
	return rr.Code == http.StatusForbidden && strings.Contains(rr.Body.String(), "browser requests are refused")
}

// TestOriginGuardRefusesDNSRebindingInOpenMode reproduces the rebinding shape:
// a real loopback connection whose Host is the attacker's name.
func TestOriginGuardRefusesDNSRebindingInOpenMode(t *testing.T) {
	h := newTestServer(t, config.AdminConfig{Listen: "127.0.0.1:9090"}, Deps{}).routes()
	for _, tc := range []struct {
		method, target string
		refused        bool
	}{
		{http.MethodGet, "http://evil.example:9090/api/config", true},
		{http.MethodPost, "http://evil.example:9090/api/config/apply", true},
		{http.MethodGet, "http://127.0.0.1:9090/api/status", false},
		{http.MethodGet, "http://localhost:9090/api/status", false},
		{http.MethodGet, "http://[::1]:9090/api/status", false},
		{http.MethodGet, "http://evil.example:9090/healthz", false},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, withLocalAddr(httptest.NewRequest(tc.method, tc.target, nil), "127.0.0.1:9090"))
		if got := refusedByOriginGuard(rr); got != tc.refused {
			t.Errorf("%s %s refused=%v, want %v (%d %s)", tc.method, tc.target, got, tc.refused, rr.Code, rr.Body.String())
		}
	}

	// Over TLS a rebound name cannot match the certificate, so Host is not policed.
	rr := httptest.NewRecorder()
	req := withLocalAddr(httptest.NewRequest(http.MethodGet, "https://admin.example:9443/api/status", nil), "203.0.113.7:9443")
	req.TLS = &tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13}
	h.ServeHTTP(rr, req)
	if refusedByOriginGuard(rr) {
		t.Fatalf("TLS request with its own Host was refused: %s", rr.Body.String())
	}
}

// TestOriginGuardHostCheckOnlyInOpenMode: with a credential configured a
// rebound page gains nothing, and a proxied deployment keeps its own Host.
func TestOriginGuardHostCheckOnlyInOpenMode(t *testing.T) {
	h := newTestServer(t, config.AdminConfig{Listen: "127.0.0.1:9090", Token: "secret-token"}, Deps{}).routes()
	rr := httptest.NewRecorder()
	req := withLocalAddr(httptest.NewRequest(http.MethodGet, "http://admin.internal:9090/api/status", nil), "127.0.0.1:9090")
	req.Header.Set("Authorization", "Bearer secret-token")
	h.ServeHTTP(rr, req)
	if refusedByOriginGuard(rr) || rr.Code != http.StatusOK {
		t.Fatalf("token-mode request with a non-loopback Host = %d %s", rr.Code, rr.Body.String())
	}
}

// TestOriginGuardRefusesCrossOriginMutation covers blind CSRF: a simple
// text/plain POST from another origin never reaches a handler, in any mode,
// while same-origin (Console) and header-less (CLI) requests do.
func TestOriginGuardRefusesCrossOriginMutation(t *testing.T) {
	for _, token := range []string{"", "secret-token"} {
		h := newTestServer(t, config.AdminConfig{Listen: "127.0.0.1:9090", Token: token}, Deps{}).routes()
		for _, tc := range []struct {
			name    string
			headers map[string]string
			refused bool
		}{
			{"sec-fetch cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
			{"sec-fetch same-site", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
			{"origin mismatch", map[string]string{"Origin": "https://evil.example"}, true},
			{"sec-fetch same-origin", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:9090"}, false},
			{"origin matches host", map[string]string{"Origin": "http://127.0.0.1:9090"}, false},
			{"no browser headers (CLI)", nil, false},
		} {
			rr := httptest.NewRecorder()
			req := withLocalAddr(httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9090/reload", strings.NewReader("x")), "127.0.0.1:9090")
			req.Header.Set("Content-Type", "text/plain")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			h.ServeHTTP(rr, req)
			if got := refusedByOriginGuard(rr); got != tc.refused {
				t.Errorf("token=%t %s: refused=%v, want %v (%d %s)", token != "", tc.name, got, tc.refused, rr.Code, rr.Body.String())
			}
		}
	}
}

func TestHostIsLoopback(t *testing.T) {
	for host, want := range map[string]bool{
		"":                 true,
		"localhost":        true,
		"LOCALHOST:9090":   true,
		"app.localhost":    true,
		"127.0.0.1":        true,
		"127.1.2.3:9090":   true,
		"[::1]:9090":       true,
		"::1":              true,
		"localhost.":       true,
		"evil.example":     false,
		"localhost.evil":   false,
		"10.0.0.1:9090":    false,
		"[::ffff:1.2.3.4]": false,
	} {
		if got := hostIsLoopback(host); got != want {
			t.Errorf("hostIsLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}
