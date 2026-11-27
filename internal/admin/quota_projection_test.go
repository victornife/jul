// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestQuotaProjectionShowsEffectiveBindingsWithoutPrincipalValues(t *testing.T) {
	cfg := &config.Config{
		QuotaPolicies: map[string]config.QuotaPolicyConfig{
			"app": {
				Mode: "enforce", Rate: 100, Burst: 120, MaxCallers: 500,
				Classes: map[string]config.QuotaClassConfig{
					"account": {
						Shared:    config.QuotaLimitConfig{Rate: 60, Burst: 70},
						PerCaller: config.QuotaLimitConfig{Rate: 20, Burst: 25},
						Principals: []config.QuotaPrincipalMatch{{
							Method: "jwt", Domain: "accounts", Issuer: "issuer", Subject: "do-not-project-this-subject",
						}},
					},
					"public": {
						Shared:    config.QuotaLimitConfig{Rate: 40, Burst: 50},
						PerCaller: config.QuotaLimitConfig{Rate: 10, Burst: 12},
					},
				},
				Pools: map[string]config.QuotaPoolConfig{
					"office": {Class: "public", CIDRs: []string{"192.0.2.0/24"}},
				},
				Operations: map[string]config.QuotaOperationConfig{
					"export": {
						Shared:    config.QuotaLimitConfig{Rate: 5, Burst: 8},
						PerCaller: config.QuotaLimitConfig{Rate: 2, Burst: 3},
					},
				},
			},
		},
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{{
				Match:       config.MatchConfig{Type: "prefix", Path: "/api"},
				QuotaPolicy: "app", QuotaOperation: "export",
			}},
		}},
	}

	routes := projectRoutes(cfg)
	location := routes[0].Locations[0]
	if location.QuotaPolicy != "app" || location.QuotaOperation != "export" {
		t.Fatalf("route quota projection = %q/%q", location.QuotaPolicy, location.QuotaOperation)
	}
	policies := projectQuotaPolicies(cfg)
	if len(policies) != 1 || policies[0].Name != "app" || len(policies[0].Classes) != 2 {
		t.Fatalf("quota policy projection = %+v", policies)
	}
	if policies[0].Classes[0].Name != "account" || policies[0].Classes[0].PrincipalCount != 1 {
		t.Fatalf("class projection order/count = %+v", policies[0].Classes)
	}
	if len(policies[0].Operations) != 1 || policies[0].Operations[0].Name != "export" {
		t.Fatalf("operation projection = %+v", policies[0].Operations)
	}
	if len(policies[0].Pools) != 1 || policies[0].Pools[0].Name != "office" ||
		policies[0].Pools[0].CIDRs[0] != "192.0.2.0/24" {
		t.Fatalf("pool projection = %+v", policies[0].Pools)
	}

	encoded, err := json.Marshal(projectIssue81TrafficControls(cfg))
	if err != nil {
		t.Fatalf("marshal quota projection: %v", err)
	}
	if strings.Contains(string(encoded), "do-not-project-this-subject") {
		t.Fatalf("quota projection exposed a principal value: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"quota_policies"`) {
		t.Fatalf("traffic-controls projection omitted quota policies: %s", encoded)
	}
}
