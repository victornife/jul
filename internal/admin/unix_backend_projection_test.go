// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"testing"
	"time"

	"jul/internal/config"
)

func TestAppsProjectionSeparatesServingDiscoveryFromStagedTarget(t *testing.T) {
	lastSuccess := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{Upstreams: []config.UpstreamConfig{
		{Name: "api", Discovery: &config.DiscoveryConfig{Type: "dns", Target: "staged.example.test:8080"}},
		{Name: "pending"},
		{Name: "static"},
	}}
	live := map[string]UpstreamStatus{
		"api":     {ServingDiscovery: &DiscoveryObservation{LastSuccess: lastSuccess}},
		"pending": {ServingDiscovery: &DiscoveryObservation{}},
	}

	apps := projectApps(cfg, live)
	if len(apps) != 3 {
		t.Fatalf("apps = %+v", apps)
	}
	if apps[0].DiscoveryTarget != "staged.example.test:8080" ||
		apps[0].ServingDiscovery == nil || !apps[0].ServingDiscovery.LastSuccess.Equal(lastSuccess) {
		t.Errorf("staged target and serving discovery projection = %+v", apps[0])
	}
	if apps[1].ServingDiscovery == nil || !apps[1].ServingDiscovery.LastSuccess.IsZero() {
		t.Errorf("pending discovery projection = %+v", apps[1])
	}
	if apps[2].ServingDiscovery != nil {
		t.Errorf("static discovery projection = %+v", apps[2])
	}
	for i, want := range []string{`{"last_success":"2026-11-24T12:00:00Z"}`, `{}`, ""} {
		data, err := json.Marshal(apps[i])
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if got := string(fields["serving_discovery"]); got != want {
			t.Errorf("app %d serving discovery JSON = %q, want %q", i, got, want)
		}
	}
}

func TestAppsProjectionJoinsUnixBackendByNetworkAndNormalizedAddress(t *testing.T) {
	cfg := &config.Config{Upstreams: []config.UpstreamConfig{{
		Name:     "local-app",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "unix:/tmp/local-app.sock", Weight: 2}},
	}}}
	live := map[string]UpstreamStatus{
		"local-app": {
			Name: "local-app",
			Backends: []BackendStatus{{
				Address:  "/tmp/local-app.sock",
				Network:  "unix",
				Weight:   2,
				State:    "available",
				Inflight: 3,
			}},
		},
	}

	apps := projectApps(cfg, live)
	if len(apps) != 1 || len(apps[0].Backends) != 1 {
		t.Fatalf("apps = %+v", apps)
	}
	got := apps[0].Backends[0]
	if got.Address != "unix:/tmp/local-app.sock" || got.Network != "unix" || got.State != "available" || got.Inflight != 3 {
		t.Fatalf("Unix backend projection = %+v", got)
	}
}

func TestAppsProjectionIncludesLiveOnlyBackendAndDefaultsLegacyNetworkToTCP(t *testing.T) {
	cfg := &config.Config{Upstreams: []config.UpstreamConfig{{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "127.0.0.1:8080", Weight: 1}},
	}}}
	live := map[string]UpstreamStatus{
		"api": {
			Name: "api",
			Backends: []BackendStatus{
				{Address: "127.0.0.1:8080", Weight: 1, State: "available"},
				{Address: "127.0.0.1:8081", Weight: 3, State: "unavailable", Inflight: 2},
			},
		},
	}

	apps := projectApps(cfg, live)
	if len(apps) != 1 || len(apps[0].Backends) != 2 {
		t.Fatalf("apps = %+v", apps)
	}
	got := apps[0].Backends[1]
	if got.Address != "127.0.0.1:8081" || got.Network != "tcp" || got.Weight != 3 || got.State != "unavailable" || got.Inflight != 2 {
		t.Fatalf("live-only backend projection = %+v", got)
	}
}

func TestV1UpstreamsJoinsUnixBackendByNetworkAndNormalizedAddress(t *testing.T) {
	cfg := &config.Config{Upstreams: []config.UpstreamConfig{{
		Name:     "local-app",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "unix:/tmp/local-app.sock", Weight: 2}},
	}}}
	s := &Server{deps: Deps{Upstreams: func() []UpstreamStatus {
		return []UpstreamStatus{{
			Name: "local-app",
			Backends: []BackendStatus{{
				Address:  "/tmp/local-app.sock",
				Network:  "unix",
				Weight:   2,
				State:    "available",
				Inflight: 4,
			}},
		}}
	}}}

	out := s.v1Upstreams(cfg)
	if len(out) != 1 || len(out[0].Backends) != 1 {
		t.Fatalf("upstreams = %+v", out)
	}
	got := out[0].Backends[0]
	if got.Address != "unix:/tmp/local-app.sock" || got.Network != "unix" || got.State != "available" || got.InFlight != 4 {
		t.Fatalf("Unix v1 backend = %+v", got)
	}
}
