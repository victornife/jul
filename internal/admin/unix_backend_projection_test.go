// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"testing"

	"jul/internal/config"
)

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
