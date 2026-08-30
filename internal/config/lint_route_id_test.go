// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import "testing"

func routeIDLintConfig(authority string, routeID *string) *Config {
	return &Config{
		Global: GlobalConfig{ConfigAuthority: authority},
		Servers: []ServerConfig{{
			Listen: ":8080",
			Locations: []LocationConfig{{
				Match:     MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: "http://127.0.0.1:9000",
				RouteID:   routeID,
			}},
		}},
	}
}

// TestLintSuggestsRouteIDInManagedModeOnly pins ADR 0019 §4.13: a route with
// no durable route_id gets an informational (not warning, not error) lint
// suggestion, and only in managed mode — file_owned mode must never nag an
// operator to edit a file Jul does not own.
func TestLintSuggestsRouteIDInManagedModeOnly(t *testing.T) {
	t.Run("managed with no route_id: info", func(t *testing.T) {
		diags := Lint(routeIDLintConfig("managed", nil))
		requireDiagnostic(t, diags, SeverityInfo, "servers[0].locations[0]", "route_id")
	})

	t.Run("managed with a route_id: no suggestion", func(t *testing.T) {
		id := "r-existing"
		diags := Lint(routeIDLintConfig("managed", &id))
		for _, d := range diags {
			if d.Severity == SeverityInfo {
				t.Errorf("unexpected info diagnostic for a route with a route_id: %+v", d)
			}
		}
	})

	t.Run("file_owned with no route_id: no suggestion", func(t *testing.T) {
		diags := Lint(routeIDLintConfig("file_owned", nil))
		for _, d := range diags {
			if d.Severity == SeverityInfo {
				t.Errorf("file_owned mode must never suggest editing route_id, got %+v", d)
			}
		}
	})

	t.Run("omitted config_authority (not managed): no suggestion", func(t *testing.T) {
		diags := Lint(routeIDLintConfig("", nil))
		for _, d := range diags {
			if d.Severity == SeverityInfo {
				t.Errorf("unexpected info diagnostic with config_authority omitted: %+v", d)
			}
		}
	})
}
