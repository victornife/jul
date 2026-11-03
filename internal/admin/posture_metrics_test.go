// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/server"
)

func TestPostureSnapshotAvailabilityForAPIAndMetrics(t *testing.T) {
	cfg := &config.Config{}
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		deps   Deps
		method string
		status int
		state  string
	}{
		{name: "persisted only", deps: Deps{ReadConfigRaw: func() ([]byte, error) { return raw, nil }}, method: http.MethodGet, status: 200, state: "ok"},
		{name: "loader fallback", deps: Deps{LoadConfig: func() (*config.Config, error) { return cfg, nil }}, method: http.MethodGet, status: 200, state: "ok"},
		{name: "live with unavailable persisted source", deps: Deps{ReadConfigRaw: func() ([]byte, error) { return nil, errors.New("private-source-canary") }, LiveSnapshot: func() server.LiveSnapshot { return server.LiveSnapshot{EffectiveConfig: cfg} }}, method: http.MethodGet, status: 200, state: "stale"},
		{name: "unavailable", deps: Deps{ReadConfigRaw: func() ([]byte, error) { return nil, errors.New("private-source-canary") }}, method: http.MethodGet, status: 503, state: "unavailable"},
		{name: "read only method", method: http.MethodPost, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active := ""
			tc.deps.ObserveSecurityEvidenceState = func(producer, state string) {
				if producer == "posture_evaluator" {
					active = state
				}
			}
			s := newTestServer(t, config.AdminConfig{}, tc.deps)
			w := httptest.NewRecorder()
			s.handleV1SecurityPosture(w, httptest.NewRequest(tc.method, "/api/v1/security/posture", nil))
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private-source-canary") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.state != "" {
				if active != tc.state {
					t.Fatalf("API state=%s want%s", active, tc.state)
				}
				active = ""
				s.RefreshSecurityPostureMetrics()
				if active != tc.state {
					t.Fatalf("scrape state=%s want%s", active, tc.state)
				}
			}
		})
	}
}
