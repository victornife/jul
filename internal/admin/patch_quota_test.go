// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"jul/internal/config"
)

func quotaInt(value int) *int { return &value }

func quotaString(value string) *string { return &value }

func quotaTestAdminConfig() *config.Config {
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{{
				Match: config.MatchConfig{Type: "prefix", Path: "/api"},
			}},
		}},
	}
}

func quotaTestAdminPolicy() config.QuotaPolicyConfig {
	return config.QuotaPolicyConfig{
		Mode:       "enforce",
		Rate:       100,
		Burst:      120,
		MaxCallers: 100,
		Classes: map[string]config.QuotaClassConfig{
			"public": {
				Shared:    config.QuotaLimitConfig{Rate: 60, Burst: 70},
				PerCaller: config.QuotaLimitConfig{Rate: 10, Burst: 12},
			},
			"account": {
				Shared:    config.QuotaLimitConfig{Rate: 40, Burst: 50},
				PerCaller: config.QuotaLimitConfig{Rate: 20, Burst: 25},
				Principals: []config.QuotaPrincipalMatch{{
					Method: "jwt", Domain: "accounts", Issuer: "issuer-a", Subject: "private-account-id",
				}},
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
	}
}

func TestQuotaPolicyPatchCreatesAndSparselyUpdates(t *testing.T) {
	cfg := quotaTestAdminConfig()
	summary, err := applyPatch(cfg, patchRequest{
		Op: "quota_policy_set",
		QuotaPolicy: &quotaPolicyPatch{
			Name:       "app",
			Mode:       quotaString("observe"),
			Rate:       quotaInt(100),
			Burst:      quotaInt(120),
			MaxCallers: quotaInt(100),
			Classes: map[string]quotaClassPatch{
				"public": {
					Shared:    &quotaLimitPatch{Rate: quotaInt(60), Burst: quotaInt(70)},
					PerCaller: &quotaLimitPatch{Rate: quotaInt(10), Burst: quotaInt(12)},
				},
				"account": {
					Shared:    &quotaLimitPatch{Rate: quotaInt(40), Burst: quotaInt(50)},
					PerCaller: &quotaLimitPatch{Rate: quotaInt(20), Burst: quotaInt(25)},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create quota policy: %v", err)
	}
	if strings.Contains(summary, "private-account-id") {
		t.Fatalf("patch summary exposed a principal: %q", summary)
	}
	policy := cfg.QuotaPolicies["app"]
	if policy.Mode != "observe" || policy.Classes["account"].Shared.Rate != 40 {
		t.Fatalf("created policy lost configuration: %+v", policy)
	}

	policy.Pools = quotaTestAdminPolicy().Pools
	policy.Operations = quotaTestAdminPolicy().Operations
	policy.Classes["account"] = quotaTestAdminPolicy().Classes["account"]
	cfg.QuotaPolicies["app"] = policy

	_, err = applyPatch(cfg, patchRequest{
		Op: "quota_policy_set",
		QuotaPolicy: &quotaPolicyPatch{
			Name: "app",
			Classes: map[string]quotaClassPatch{
				"account": {
					Shared: &quotaLimitPatch{Rate: quotaInt(35)},
				},
			},
			Operations: map[string]quotaOperationPatch{
				"export": {Shared: &quotaLimitPatch{Rate: quotaInt(4)}},
			},
		},
	})
	if err != nil {
		t.Fatalf("sparse quota policy update: %v", err)
	}
	got := cfg.QuotaPolicies["app"]
	account := got.Classes["account"]
	if account.Shared.Rate != 35 || account.Shared.Burst != 50 ||
		account.Principals[0].Subject != "private-account-id" {
		t.Fatalf("sparse class edit did not preserve unedited fields: %+v", account)
	}
	if got.Pools["office"].CIDRs[0] != "192.0.2.0/24" ||
		got.Operations["export"].Shared.Rate != 4 ||
		got.Operations["export"].PerCaller.Burst != 3 {
		t.Fatalf("sparse update did not preserve pools/operation fields: %+v", got)
	}
}

func TestQuotaPolicyPatchRejectsIncompleteAndUnsafeChanges(t *testing.T) {
	tests := []struct {
		name  string
		patch *quotaPolicyPatch
		want  string
	}{
		{
			name:  "incomplete policy",
			patch: &quotaPolicyPatch{Name: "new", Mode: quotaString("enforce")},
			want:  "new policy requires mode, rate, burst, and max_callers",
		},
		{
			name: "missing public class",
			patch: &quotaPolicyPatch{
				Name: "new", Mode: quotaString("enforce"), Rate: quotaInt(10), Burst: quotaInt(10), MaxCallers: quotaInt(10),
				Classes: map[string]quotaClassPatch{},
			},
			want: "requires the public class",
		},
		{
			name: "incomplete new class",
			patch: &quotaPolicyPatch{
				Name: "new", Mode: quotaString("enforce"), Rate: quotaInt(10), Burst: quotaInt(10), MaxCallers: quotaInt(10),
				Classes: map[string]quotaClassPatch{
					"public":  {Shared: &quotaLimitPatch{Rate: quotaInt(10), Burst: quotaInt(10)}, PerCaller: &quotaLimitPatch{Rate: quotaInt(10), Burst: quotaInt(10)}},
					"account": {Shared: &quotaLimitPatch{Rate: quotaInt(4), Burst: quotaInt(4)}},
				},
			},
			want: "a new class requires shared and per_caller limits",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyQuotaPolicySet(quotaTestAdminConfig(), tt.patch)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("applyQuotaPolicySet() error = %v, want substring %q", err, tt.want)
			}
		})
	}

	cfg := quotaTestAdminConfig()
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{"app": quotaTestAdminPolicy()}
	if _, err := applyQuotaPolicySet(cfg, &quotaPolicyPatch{
		Name:          "app",
		RemoveClasses: []string{"public"},
	}); err == nil || !strings.Contains(err.Error(), "public class cannot be removed") {
		t.Fatalf("removing public class error = %v", err)
	}
	before := cfg.QuotaPolicies["app"]
	if _, err := applyQuotaPolicySet(cfg, &quotaPolicyPatch{
		Name:          "app",
		RemoveClasses: []string{"account"},
		RemovePools:   []string{"missing"},
	}); err == nil {
		t.Fatal("removing a missing pool unexpectedly succeeded")
	}
	if got := cfg.QuotaPolicies["app"]; !reflect.DeepEqual(got, before) {
		t.Fatalf("rejected policy patch mutated the candidate: got %+v, want %+v", got, before)
	}
}

func TestRouteQuotaPolicyAttachDetachAndRemovalGuard(t *testing.T) {
	cfg := quotaTestAdminConfig()
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{"app": quotaTestAdminPolicy()}
	target := patchRequest{Listen: ":8080", MatchType: "prefix", Path: "/api"}

	if _, err := applyPatch(cfg, patchRequest{
		Op: "route_set_quota_policy", Listen: target.Listen, MatchType: target.MatchType, Path: target.Path,
		RouteQuota: &routeQuotaPatch{Policy: "app", Operation: "export"},
	}); err != nil {
		t.Fatalf("attach quota policy: %v", err)
	}
	loc := &cfg.Servers[0].Locations[0]
	if loc.QuotaPolicy != "app" || loc.QuotaOperation != "export" {
		t.Fatalf("attached route quota = %q/%q", loc.QuotaPolicy, loc.QuotaOperation)
	}
	if _, err := applyPatch(cfg, patchRequest{Op: "quota_policy_remove", QuotaPolicyName: "app"}); err == nil {
		t.Fatal("removing an attached policy unexpectedly succeeded")
	}
	if _, err := applyPatch(cfg, patchRequest{
		Op: "route_set_quota_policy", Listen: target.Listen, MatchType: target.MatchType, Path: target.Path,
		RouteQuota: &routeQuotaPatch{},
	}); err != nil {
		t.Fatalf("detach quota policy: %v", err)
	}
	if loc.QuotaPolicy != "" || loc.QuotaOperation != "" {
		t.Fatalf("detached route still has quota binding %q/%q", loc.QuotaPolicy, loc.QuotaOperation)
	}
	if _, err := applyPatch(cfg, patchRequest{Op: "quota_policy_remove", QuotaPolicyName: "app"}); err != nil {
		t.Fatalf("remove detached policy: %v", err)
	}
}

func TestQuotaPatchPreviewDiffShowsPolicyAndRouteChanges(t *testing.T) {
	before := quotaTestAdminConfig()
	after := quotaTestAdminConfig()
	after.QuotaPolicies = map[string]config.QuotaPolicyConfig{"app": quotaTestAdminPolicy()}
	after.Servers[0].Locations[0].QuotaPolicy = "app"
	after.Servers[0].Locations[0].QuotaOperation = "export"

	diff := diffConfigs(before, after)
	rendered, err := json.Marshal(diff)
	if err != nil {
		t.Fatalf("marshal quota diff: %v", err)
	}
	output := string(rendered)
	if !strings.Contains(output, "Add application quota policy app") ||
		!strings.Contains(output, "Change application quota attachment") {
		t.Fatalf("quota preview diff omitted policy or route change: %s", output)
	}
	if strings.Contains(output, "private-account-id") {
		t.Fatalf("quota preview diff exposed a principal: %s", output)
	}
}
