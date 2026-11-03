// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/rbac"
	"jul/internal/server"
)

func TestSecuritySnapshotKeepsSelectorsAndVersionTogether(t *testing.T) {
	makeConfig := func(first, second string) *config.Config {
		return &config.Config{Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "prefix", Path: "/api", Methods: []string{first}}, WAF: &config.WAFConfig{Enabled: true, Mode: "block", CRSEnabled: true}},
				{Match: config.MatchConfig{Type: "prefix", Path: "/api", Methods: []string{second}}, WAF: &config.WAFConfig{Enabled: true, Mode: "block", CRSEnabled: true}},
			},
		}}}
	}
	old := makeConfig("GET", "POST")
	raw, err := config.Marshal(makeConfig("POST", "GET"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var loadCalls, rawCalls int
	s := newTestServer(t, config.AdminConfig{}, Deps{
		// Deterministic equivalent of a file replacement between the two old
		// reads: the loader sees A, while the authoritative raw reader sees B.
		LoadConfig:    func() (*config.Config, error) { loadCalls++; return old, nil },
		ReadConfigRaw: func() ([]byte, error) { rawCalls++; return raw, nil },
		WAFCompiled:   true,
	})
	loadCalls, rawCalls = 0, 0
	r := httptest.NewRequest(http.MethodGet, "/api/security", nil)
	r = r.WithContext(rbac.WithIdentity(r.Context(), rbac.Identity{Permissions: []rbac.Permission{rbac.ConfigRead}}))
	w := httptest.NewRecorder()
	s.handleSecurity(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("security status=%d: %s", w.Code, w.Body.String())
	}
	var got SecurityProjection
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.BaseVersion != server.CanonicalVersion(current) || len(got.WAFRoutes) != 2 || len(got.LocationWAFs) != 2 {
		t.Fatalf("inconsistent snapshot: %+v", got)
	}
	if got.WAFRoutes[0].Methods[0] != "POST" || got.LocationWAFs[0].Methods[0] != "POST" || got.LocationWAFs[0].MatchOrdinal != 0 {
		t.Fatalf("new version was paired with old route selectors: %+v", got.LocationWAFs)
	}
	if loadCalls != 0 || rawCalls != 1 {
		t.Fatalf("snapshot must be read once, got loader=%d raw=%d", loadCalls, rawCalls)
	}
}

func TestSecuritySnapshotAvailabilityAndMethod(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		deps   Deps
		status int
	}{
		{"no source", http.MethodGet, Deps{}, http.StatusOK},
		{"wrong method", http.MethodPost, Deps{}, http.StatusMethodNotAllowed},
		{"read failure", http.MethodGet, Deps{ReadConfigRaw: func() ([]byte, error) { return nil, errors.New("private-source-canary") }}, http.StatusServiceUnavailable},
		{"parse failure", http.MethodGet, Deps{ReadConfigRaw: func() ([]byte, error) { return []byte("[invalid"), nil }}, http.StatusServiceUnavailable},
		{"loader fallback", http.MethodGet, Deps{LoadConfig: func() (*config.Config, error) { return &config.Config{}, nil }}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, config.AdminConfig{}, tc.deps)
			w := httptest.NewRecorder()
			s.handleSecurity(w, httptest.NewRequest(tc.method, "/api/security", nil))
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private-source-canary") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
