// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func resilienceServer(t *testing.T, cfg *config.Config, pools []PoolResilience) *Server {
	t.Helper()
	return newTestServer(t, config.AdminConfig{Token: "tok"}, Deps{
		LoadConfig:         func() (*config.Config, error) { return cfg, nil },
		UpstreamResilience: func(string) []PoolResilience { return pools },
	})
}

func getResilience(t *testing.T, s *Server, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://h/api/upstreams/"+name+"/resilience", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// A name with no live pool is a 404. A live pool with every backend down is a
// 200 with verdict "down". Collapsing the two would tell an operator their pool
// had vanished at the exact moment it was failing.
func TestResilienceUnknownPoolIs404(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Token: "tok"}, Deps{
		UpstreamResilience: func(string) []PoolResilience { return nil },
	})
	if rec := getResilience(t, s, "nope"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestResilienceReportsVerdictAndBackends(t *testing.T) {
	pools := []PoolResilience{{
		Name: "api",
		Backends: []BackendResilience{
			{Address: "10.0.0.1:80", State: "available"},
			{Address: "10.0.0.2:80", State: "circuit_open", OpenUntil: "2026-08-19T20:00:00Z", Fails: 3},
		},
	}}
	s := resilienceServer(t, &config.Config{}, pools)

	rec := getResilience(t, s, "api")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got []PoolResilience
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d pools, want 1", len(got))
	}
	// One available and one tripped is degraded. Reporting healthy here would
	// let a single surviving backend mask the outage.
	if got[0].Verdict != "degraded" {
		t.Errorf("verdict = %q, want degraded", got[0].Verdict)
	}
	if got[0].Backends[1].OpenUntil == "" {
		t.Error("open_until was dropped for a tripped backend")
	}
}

// The two endpoints that report pool health must never disagree: they run the
// same classifier over the same states.
func TestResilienceVerdictMatchesTheAppsProjection(t *testing.T) {
	states := []string{"available", "circuit_open", "circuit_half_open", "health_unhealthy", "at_capacity", ""}
	for _, a := range states {
		for _, b := range states {
			viaApps := poolVerdict([]BackendProjection{{Address: "1", State: a}, {Address: "2", State: b}})
			viaResilience := poolVerdictFromStates([]BackendResilience{{Address: "1", State: a}, {Address: "2", State: b}})
			if viaApps != viaResilience {
				t.Errorf("states (%q,%q): /api/apps says %q, /resilience says %q", a, b, viaApps, viaResilience)
			}
		}
	}
}

// "The value is 3" does not tell an operator which of the two spellings won,
// which is exactly the question they have when a change appears to do nothing.
func TestResilienceReportsWhichKeySuppliedEachCircuitLimit(t *testing.T) {
	probes := 4
	cases := []struct {
		name string
		up   config.UpstreamConfig
		want map[string]string
	}{
		{
			name: "nothing set",
			up:   config.UpstreamConfig{Name: "api"},
			want: map[string]string{"circuit_max_fails": "default", "circuit_fail_timeout": "default", "circuit_half_open_probes": "default"},
		},
		{
			name: "deprecated spelling",
			up:   config.UpstreamConfig{Name: "api", MaxFails: 7, FailTimeout: config.Duration(1)},
			want: map[string]string{"circuit_max_fails": "upstream", "circuit_fail_timeout": "upstream", "circuit_half_open_probes": "default"},
		},
		{
			name: "resilience spelling",
			up: config.UpstreamConfig{Name: "api", Resilience: &config.ResilienceConfig{
				MaxFails: 5, FailTimeout: config.Duration(1), CircuitHalfOpenProbes: &probes,
			}},
			want: map[string]string{"circuit_max_fails": "resilience", "circuit_fail_timeout": "resilience", "circuit_half_open_probes": "resilience"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := circuitLimitSources(&tc.up)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("sources[%s] = %q, want %q", k, got[k], want)
				}
			}
		})
	}
}

// An explicit zero for circuit_half_open_probes is the documented way to ask for
// the old unbounded behaviour. It must read as configured, not as absent — the
// pointer exists for exactly this distinction.
func TestExplicitZeroHalfOpenProbesIsReportedAsConfigured(t *testing.T) {
	zero := 0
	up := config.UpstreamConfig{Name: "api", Resilience: &config.ResilienceConfig{CircuitHalfOpenProbes: &zero}}
	if got := circuitLimitSources(&up)["circuit_half_open_probes"]; got != "resilience" {
		t.Errorf("source = %q, want resilience for an explicit 0", got)
	}
}

func TestResilienceRejectsABadPoolName(t *testing.T) {
	s := resilienceServer(t, &config.Config{}, []PoolResilience{{Name: "api"}})
	req := httptest.NewRequest(http.MethodGet, "http://h/api/upstreams/%20/resilience", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a blank name", rec.Code)
	}
}
