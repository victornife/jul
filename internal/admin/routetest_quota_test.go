// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"

	"jul/internal/config"
)

func TestRouteTestExplainsEffectiveQuotaClassification(t *testing.T) {
	cfg := config.ProxyTarget("http://127.0.0.1:8081", "127.0.0.1:8080")
	cfg.Servers[0].ServerNames = []string{"api.example"}
	cfg.Servers[0].Locations[0].QuotaPolicy = "api"
	cfg.Servers[0].Locations[0].QuotaOperation = "export"
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"api": {
			Mode:       "enforce",
			Rate:       100,
			Burst:      100,
			MaxCallers: 100,
			Classes: map[string]config.QuotaClassConfig{
				"public":  {},
				"network": {},
				"trusted": {},
				"account": {Principals: []config.QuotaPrincipalMatch{{Method: "forward", Domain: "services", Subject: "alice"}}},
			},
			Pools: map[string]config.QuotaPoolConfig{
				"office":  {Class: "network", CIDRs: []string{"192.0.2.0/24"}},
				"finance": {Class: "trusted", CIDRs: []string{"192.0.2.128/25"}},
			},
		},
	}

	testCases := []struct {
		name        string
		input       routeTestRequest
		wantClass   string
		wantRule    string
		wantWarning bool
	}{
		{
			name: "principal takes precedence",
			input: routeTestRequest{
				Host:          "api.example",
				Path:          "/",
				ClientAddress: "192.0.2.200",
				QuotaPrincipal: &routeTestQuotaPrincipal{
					Method:  "forward",
					Domain:  "services",
					Subject: "alice",
				},
			},
			wantClass: "account",
			wantRule:  "exact verified principal mapping",
		},
		{
			name:      "longest matching pool wins",
			input:     routeTestRequest{Host: "api.example", Path: "/", ClientAddress: "192.0.2.200"},
			wantClass: "trusted",
			wantRule:  `longest matching CIDR pool "finance" (192.0.2.128/25)`,
		},
		{
			name:      "public fallback",
			input:     routeTestRequest{Host: "api.example", Path: "/", ClientAddress: "198.51.100.10"},
			wantClass: "public",
			wantRule:  "public fallback",
		},
		{
			name:        "invalid address is explicit",
			input:       routeTestRequest{Host: "api.example", Path: "/", ClientAddress: "not-an-address"},
			wantClass:   "public",
			wantRule:    "public fallback",
			wantWarning: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := testRoute(cfg, tc.input)
			if !result.Matched {
				t.Fatalf("route did not match: %s", result.Explanation)
			}
			classification := result.QuotaClassification
			if classification == nil {
				t.Fatal("quota classification missing")
			}
			if classification.Policy != "api" || classification.Operation != "export" {
				t.Fatalf("policy/operation = %q/%q, want api/export", classification.Policy, classification.Operation)
			}
			if classification.Class != tc.wantClass || classification.WinningRule != tc.wantRule {
				t.Fatalf("classification = %q by %q, want %q by %q", classification.Class, classification.WinningRule, tc.wantClass, tc.wantRule)
			}
			if tc.wantWarning && len(classification.Warnings) == 0 {
				t.Fatal("invalid address produced no warning")
			}
			if !strings.Contains(classification.Precedence, "longest matching attributed CIDR") {
				t.Fatalf("precedence explanation missing: %q", classification.Precedence)
			}
			if tc.input.QuotaPrincipal != nil && strings.Contains(classification.Note, tc.input.QuotaPrincipal.Subject) {
				t.Fatal("classification note disclosed the hypothetical principal subject")
			}
		})
	}
}
