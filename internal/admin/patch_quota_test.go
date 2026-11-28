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

func TestQuotaPolicyPatchRejectsMalformedChanges(t *testing.T) {
	tests := []struct {
		name  string
		patch *quotaPolicyPatch
		want  string
	}{
		{name: "missing payload", want: "payload is required"},
		{name: "empty name", patch: &quotaPolicyPatch{Name: " "}, want: "policy name is required"},
		{name: "empty patch", patch: &quotaPolicyPatch{Name: "app"}, want: "at least one policy field"},
		{name: "invalid mode", patch: &quotaPolicyPatch{Name: "app", Mode: quotaString("disabled")}, want: "mode must be"},
		{name: "invalid rate", patch: &quotaPolicyPatch{Name: "app", Rate: quotaInt(0)}, want: "rate must be greater than zero"},
		{name: "invalid burst", patch: &quotaPolicyPatch{Name: "app", Burst: quotaInt(0)}, want: "burst must be greater than zero"},
		{name: "invalid caller cap", patch: &quotaPolicyPatch{Name: "app", MaxCallers: quotaInt(0)}, want: "max_callers must be greater than zero"},
		{
			name: "invalid class name",
			patch: &quotaPolicyPatch{Name: "app", Classes: map[string]quotaClassPatch{
				" account": {},
			}},
			want: "class name must be non-empty",
		},
		{
			name: "invalid class shared limit",
			patch: &quotaPolicyPatch{Name: "app", Classes: map[string]quotaClassPatch{
				"account": {Shared: &quotaLimitPatch{Rate: quotaInt(0)}},
			}},
			want: `class "account" shared: rate must be greater than zero`,
		},
		{
			name: "invalid class caller limit",
			patch: &quotaPolicyPatch{Name: "app", Classes: map[string]quotaClassPatch{
				"account": {PerCaller: &quotaLimitPatch{Burst: quotaInt(0)}},
			}},
			want: `class "account" per_caller: burst must be greater than zero`,
		},
		{
			name:  "class update and removal overlap",
			patch: &quotaPolicyPatch{Name: "app", Classes: map[string]quotaClassPatch{"account": {}}, RemoveClasses: []string{"account"}},
			want:  "class \"account\" cannot be both updated and removed",
		},
		{
			name:  "pool update and removal overlap",
			patch: &quotaPolicyPatch{Name: "app", Pools: map[string]quotaPoolPatch{"office": {}}, RemovePools: []string{"office"}},
			want:  "pool \"office\" cannot be both updated and removed",
		},
		{
			name:  "operation update and removal overlap",
			patch: &quotaPolicyPatch{Name: "app", Operations: map[string]quotaOperationPatch{"export": {}}, RemoveOperations: []string{"export"}},
			want:  "operation \"export\" cannot be both updated and removed",
		},
		{
			name: "invalid pool name",
			patch: &quotaPolicyPatch{Name: "app", Pools: map[string]quotaPoolPatch{
				" office": {},
			}},
			want: "pool name must be non-empty",
		},
		{
			name: "incomplete new pool",
			patch: &quotaPolicyPatch{Name: "app", Pools: map[string]quotaPoolPatch{
				"new": {Class: quotaString("public")},
			}},
			want: "a new pool requires class and cidrs",
		},
		{
			name: "invalid operation name",
			patch: &quotaPolicyPatch{Name: "app", Operations: map[string]quotaOperationPatch{
				" export": {},
			}},
			want: "operation name must be non-empty",
		},
		{
			name: "incomplete new operation",
			patch: &quotaPolicyPatch{Name: "app", Operations: map[string]quotaOperationPatch{
				"import": {Shared: &quotaLimitPatch{Rate: quotaInt(1), Burst: quotaInt(1)}},
			}},
			want: "a new operation requires shared and per_caller limits",
		},
		{
			name: "invalid operation shared limit",
			patch: &quotaPolicyPatch{Name: "app", Operations: map[string]quotaOperationPatch{
				"export": {Shared: &quotaLimitPatch{Rate: quotaInt(0)}},
			}},
			want: `operation "export" shared: rate must be greater than zero`,
		},
		{
			name: "invalid operation caller limit",
			patch: &quotaPolicyPatch{Name: "app", Operations: map[string]quotaOperationPatch{
				"export": {PerCaller: &quotaLimitPatch{Burst: quotaInt(0)}},
			}},
			want: `operation "export" per_caller: burst must be greater than zero`,
		},
		{
			name:  "invalid removal name",
			patch: &quotaPolicyPatch{Name: "app", RemoveOperations: []string{" export"}},
			want:  "remove_operation requires a name",
		},
		{
			name:  "duplicate removal",
			patch: &quotaPolicyPatch{Name: "app", RemoveOperations: []string{"export", "export"}},
			want:  "duplicate operation removal",
		},
		{
			name:  "missing removal",
			patch: &quotaPolicyPatch{Name: "app", RemoveOperations: []string{"missing"}},
			want:  `operation "missing" does not exist`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := quotaTestAdminConfig()
			cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{"app": quotaTestAdminPolicy()}
			_, err := applyQuotaPolicySet(cfg, tt.patch)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("applyQuotaPolicySet() error = %v, want substring %q", err, tt.want)
			}
		})
	}

	if err := applyQuotaLimitPatch(&config.QuotaLimitConfig{}, nil, false); err == nil ||
		!strings.Contains(err.Error(), "limit payload is required") {
		t.Fatalf("nil limit payload error = %v", err)
	}
	if err := applyQuotaLimitPatch(&config.QuotaLimitConfig{}, &quotaLimitPatch{Rate: quotaInt(1)}, true); err == nil ||
		!strings.Contains(err.Error(), "new limit requires rate and burst") {
		t.Fatalf("incomplete new limit error = %v", err)
	}
	if err := applyQuotaLimitPatch(&config.QuotaLimitConfig{}, &quotaLimitPatch{}, false); err == nil ||
		!strings.Contains(err.Error(), "rate or burst must be provided") {
		t.Fatalf("empty limit patch error = %v", err)
	}
}

func TestQuotaPolicyPatchAddsPoolAndOperationAndRemovesEntries(t *testing.T) {
	cfg := quotaTestAdminConfig()
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{"app": quotaTestAdminPolicy()}
	cidrs := []string{"198.51.100.0/24"}
	_, err := applyQuotaPolicySet(cfg, &quotaPolicyPatch{
		Name: "app",
		Pools: map[string]quotaPoolPatch{
			"new": {Class: quotaString("account"), CIDRs: &cidrs},
		},
		Operations: map[string]quotaOperationPatch{
			"import": {
				Shared:    &quotaLimitPatch{Rate: quotaInt(3), Burst: quotaInt(4)},
				PerCaller: &quotaLimitPatch{Rate: quotaInt(1), Burst: quotaInt(2)},
			},
		},
		RemoveOperations: []string{"export"},
	})
	if err != nil {
		t.Fatalf("add pool and operation, remove operation: %v", err)
	}
	policy := cfg.QuotaPolicies["app"]
	if got := policy.Pools["new"]; got.Class != "account" || !reflect.DeepEqual(got.CIDRs, cidrs) {
		t.Fatalf("added pool = %+v", got)
	}
	if got := policy.Operations["import"]; got.Shared.Rate != 3 || got.PerCaller.Burst != 2 {
		t.Fatalf("added operation = %+v", got)
	}
	if _, ok := policy.Operations["export"]; ok {
		t.Fatal("removed operation remains in policy")
	}
	cidrs[0] = "203.0.113.0/24"
	if got := cfg.QuotaPolicies["app"].Pools["new"].CIDRs[0]; got != "198.51.100.0/24" {
		t.Fatalf("pool CIDRs alias patch input: %q", got)
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

func TestRouteQuotaPolicyRejectsInvalidTargetsAndBindings(t *testing.T) {
	cfg := quotaTestAdminConfig()
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{"app": quotaTestAdminPolicy()}
	target := patchRequest{Listen: ":8080", MatchType: "prefix", Path: "/api"}
	tests := []struct {
		name string
		req  patchRequest
		want string
	}{
		{name: "missing payload", req: target, want: "route_quota payload is required"},
		{name: "operation without policy", req: patchRequest{Listen: target.Listen, MatchType: target.MatchType, Path: target.Path, RouteQuota: &routeQuotaPatch{Operation: "export"}}, want: "operation requires a policy"},
		{name: "missing policy", req: patchRequest{Listen: target.Listen, MatchType: target.MatchType, Path: target.Path, RouteQuota: &routeQuotaPatch{Policy: "missing"}}, want: `policy "missing" does not exist`},
		{name: "missing operation", req: patchRequest{Listen: target.Listen, MatchType: target.MatchType, Path: target.Path, RouteQuota: &routeQuotaPatch{Policy: "app", Operation: "missing"}}, want: `operation "missing" is not defined`},
		{name: "missing route", req: patchRequest{Listen: target.Listen, MatchType: target.MatchType, Path: "/missing", RouteQuota: &routeQuotaPatch{Policy: "app"}}, want: "no route found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyRouteQuotaPolicy(cfg, tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("applyRouteQuotaPolicy() error = %v, want substring %q", err, tt.want)
			}
		})
	}

	if _, err := applyQuotaPolicyRemove(cfg, " "); err == nil || !strings.Contains(err.Error(), "policy name is required") {
		t.Fatalf("empty policy removal error = %v", err)
	}
	if _, err := applyQuotaPolicyRemove(cfg, "missing"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing policy removal error = %v", err)
	}
	if _, err := applyRouteQuotaPolicy(cfg, patchRequest{
		Listen: target.Listen, MatchType: target.MatchType, Path: target.Path,
		RouteQuota: &routeQuotaPatch{Policy: "app"},
	}); err != nil {
		t.Fatalf("attach policy without operation: %v", err)
	}
}

func TestQuotaPatchPreviewDiffShowsPolicyAndRouteChanges(t *testing.T) {
	before := quotaTestAdminConfig()
	before.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"removed": quotaTestAdminPolicy(),
		"changed": quotaTestAdminPolicy(),
	}
	before.Servers[0].Locations[0].QuotaPolicy = "old"
	before.Servers[0].Locations[0].QuotaOperation = "read"
	after := quotaTestAdminConfig()
	changed := quotaTestAdminPolicy()
	changed.Rate++
	after.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"app":     quotaTestAdminPolicy(),
		"changed": changed,
	}
	after.Servers[0].Locations[0].QuotaPolicy = "app"
	after.Servers[0].Locations[0].QuotaOperation = "export"

	diff := diffConfigs(before, after)
	rendered, err := json.Marshal(diff)
	if err != nil {
		t.Fatalf("marshal quota diff: %v", err)
	}
	output := string(rendered)
	if !strings.Contains(output, "Add application quota policy app") ||
		!strings.Contains(output, "Remove application quota policy removed") ||
		!strings.Contains(output, "Change application quota policy changed") ||
		!strings.Contains(output, "Change application quota attachment") {
		t.Fatalf("quota preview diff omitted policy or route change: %s", output)
	}
	if !strings.Contains(output, "old / read") {
		t.Fatalf("quota preview diff omitted the previous route operation: %s", output)
	}
	if strings.Contains(output, "private-account-id") {
		t.Fatalf("quota preview diff exposed a principal: %s", output)
	}
}
