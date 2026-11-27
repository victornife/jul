// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/clientaddr"
	"jul/internal/config"
	"jul/internal/quota"
)

func quotaRuntimeConfig(mode string) *config.Config {
	return &config.Config{
		QuotaPolicies: map[string]config.QuotaPolicyConfig{
			"app": {
				Mode:       mode,
				Rate:       3,
				Burst:      3,
				MaxCallers: 4,
				Classes: map[string]config.QuotaClassConfig{
					"public": {
						Shared:    config.QuotaLimitConfig{Rate: 1, Burst: 1},
						PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
					},
					"trusted": {
						Shared:    config.QuotaLimitConfig{Rate: 1, Burst: 1},
						PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
						Principals: []config.QuotaPrincipalMatch{
							{Method: "basic", Domain: "staff", Subject: "alice"},
							{Method: "forward", Domain: "services", Subject: "exporter"},
						},
					},
					"network": {
						Shared:    config.QuotaLimitConfig{Rate: 1, Burst: 1},
						PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
					},
				},
				Pools: map[string]config.QuotaPoolConfig{
					"trusted": {Class: "network", CIDRs: []string{"10.0.0.0/8", "192.168.0.0/16"}},
				},
			},
		},
	}
}

func prepareQuotaCandidate(t *testing.T, runtime *quotaRuntime, cfg *config.Config) *quotaCandidate {
	t.Helper()
	candidate, err := runtime.prepare(cfg, &Generation{basicByLoc: make(map[[2]int][]byte)})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func quotaRequest(binding *quotaBinding) (int, bool) {
	var dispatched atomic.Int32
	handler := quotaMiddleware(binding, "", nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		dispatched.Add(1)
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	handler.ServeHTTP(recorder, request)
	return recorder.Code, dispatched.Load() == 1
}

func TestQuotaRuntimePreservesStateAcrossEquivalentReload(t *testing.T) {
	runtime := newQuotaRuntime()
	cfg := quotaRuntimeConfig("enforce")
	first := prepareQuotaCandidate(t, runtime, cfg)
	firstBinding := first.Binding("app")
	first.Commit()

	if code, dispatched := quotaRequest(firstBinding); code != http.StatusOK || !dispatched {
		t.Fatalf("first request: status=%d dispatched=%v", code, dispatched)
	}

	second := prepareQuotaCandidate(t, runtime, quotaRuntimeConfig("enforce"))
	secondBinding := second.Binding("app")
	second.Commit()
	if code, dispatched := quotaRequest(secondBinding); code != http.StatusTooManyRequests || dispatched {
		t.Fatalf("request after equivalent reload: status=%d dispatched=%v", code, dispatched)
	}
}

func TestQuotaRuntimeEquivalentSecurityInputPreservesVersion(t *testing.T) {
	runtime := newQuotaRuntime()
	first := prepareQuotaCandidate(t, runtime, quotaRuntimeConfig("enforce"))
	first.Commit()
	oldBinding := first.Binding("app")
	oldVersion := oldBinding.version.Load()

	equivalentCfg := quotaRuntimeConfig("enforce")
	policy := equivalentCfg.QuotaPolicies["app"]
	pool := policy.Pools["trusted"]
	pool.CIDRs = []string{"192.168.0.0/16", "10.2.3.4/8"}
	policy.Pools["trusted"] = pool
	class := policy.Classes["trusted"]
	class.Principals[0], class.Principals[1] = class.Principals[1], class.Principals[0]
	policy.Classes["trusted"] = class
	equivalentCfg.QuotaPolicies["app"] = policy

	second := prepareQuotaCandidate(t, runtime, equivalentCfg)
	newBinding := second.Binding("app")
	second.Commit()
	if got := newBinding.version.Load(); got != oldVersion {
		t.Fatalf("equivalent security policy advanced version from %d to %d", oldVersion, got)
	}
}

func TestQuotaRuntimeFencesOldBindingsOnSecurityChangeAndRemoval(t *testing.T) {
	runtime := newQuotaRuntime()
	cfg := quotaRuntimeConfig("enforce")
	first := prepareQuotaCandidate(t, runtime, cfg)
	oldBinding := first.Binding("app")
	first.Commit()

	changedCfg := quotaRuntimeConfig("enforce")
	pool := changedCfg.QuotaPolicies["app"].Pools["trusted"]
	pool.CIDRs = []string{"10.1.0.0/16"}
	changedCfg.QuotaPolicies["app"].Pools["trusted"] = pool
	changed := prepareQuotaCandidate(t, runtime, changedCfg)
	newBinding := changed.Binding("app")
	changed.Commit()

	if code, dispatched := quotaRequest(oldBinding); code != http.StatusServiceUnavailable || dispatched {
		t.Fatalf("old binding after security change: status=%d dispatched=%v", code, dispatched)
	}
	if code, dispatched := quotaRequest(newBinding); code != http.StatusOK || !dispatched {
		t.Fatalf("new binding after security change: status=%d dispatched=%v", code, dispatched)
	}

	removed := prepareQuotaCandidate(t, runtime, &config.Config{})
	removed.Commit()
	if code, dispatched := quotaRequest(newBinding); code != http.StatusServiceUnavailable || dispatched {
		t.Fatalf("old binding after removal: status=%d dispatched=%v", code, dispatched)
	}
}

func TestQuotaRuntimePolicyRollbackRetainsBalanceAndFence(t *testing.T) {
	runtime := newQuotaRuntime()
	cfg := quotaRuntimeConfig("enforce")
	first := prepareQuotaCandidate(t, runtime, cfg)
	oldBinding := first.Binding("app")
	first.Commit()
	if code, dispatched := quotaRequest(oldBinding); code != http.StatusOK || !dispatched {
		t.Fatalf("initial request: status=%d dispatched=%v", code, dispatched)
	}
	oldVersion := oldBinding.version.Load()
	if full, _ := oldBinding.state.enforce.RefillStatus(); full {
		t.Fatal("enforcement buckets unexpectedly full after admission")
	}

	removed := prepareQuotaCandidate(t, runtime, &config.Config{})
	removed.Commit()
	retired := runtime.policies["app"]
	if retired == nil || retired.active {
		t.Fatalf("removed policy was not retained as retired state: %+v", retired)
	}
	retiredVersion := retired.enforce.AuthorizationVersion()
	if retiredVersion != oldVersion+1 {
		t.Fatalf("removal version = %d, want %d", retiredVersion, oldVersion+1)
	}
	if code, dispatched := quotaRequest(oldBinding); code != http.StatusServiceUnavailable || dispatched {
		t.Fatalf("removed policy binding: status=%d dispatched=%v", code, dispatched)
	}

	restored := prepareQuotaCandidate(t, runtime, cfg)
	newBinding := restored.Binding("app")
	restored.Commit()
	if newBinding.state.enforce != oldBinding.state.enforce {
		t.Fatal("rollback replaced the retained policy coordinator")
	}
	if got := newBinding.version.Load(); got != retiredVersion {
		t.Fatalf("restored binding version = %d, want retained version %d", got, retiredVersion)
	}
	if full, _ := newBinding.state.enforce.RefillStatus(); full {
		t.Fatal("rollback restored a depleted policy with full buckets")
	}
	for name, binding := range map[string]*quotaBinding{"pre-removal": oldBinding, "restored": newBinding} {
		code, dispatched := quotaRequest(binding)
		switch name {
		case "pre-removal":
			if code != http.StatusServiceUnavailable || dispatched {
				t.Fatalf("pre-removal binding after rollback: status=%d dispatched=%v", code, dispatched)
			}
		case "restored":
			if code != http.StatusTooManyRequests || dispatched {
				t.Fatalf("restored depleted state: status=%d dispatched=%v", code, dispatched)
			}
		}
	}
}

func TestQuotaRuntimeAbortPreservesLivePolicyState(t *testing.T) {
	runtime := newQuotaRuntime()
	first := prepareQuotaCandidate(t, runtime, quotaRuntimeConfig("enforce"))
	binding := first.Binding("app")
	first.Commit()
	if code, dispatched := quotaRequest(binding); code != http.StatusOK || !dispatched {
		t.Fatalf("initial request: status=%d dispatched=%v", code, dispatched)
	}
	version := binding.state.enforce.AuthorizationVersion()

	candidate := prepareQuotaCandidate(t, runtime, &config.Config{})
	candidate.Abort()
	if runtime.policies["app"] == nil || !runtime.policies["app"].active {
		t.Fatal("aborting removal changed the live policy registry")
	}
	if got := binding.state.enforce.AuthorizationVersion(); got != version {
		t.Fatalf("aborting removal changed authorization version from %d to %d", version, got)
	}
	if full, _ := binding.state.enforce.RefillStatus(); full {
		t.Fatal("aborting removal restored depleted quota state")
	}
}

func TestQuotaRuntimeEvictsOnlyRefilledPoliciesUnderPressure(t *testing.T) {
	runtime := newQuotaRuntime()
	policyConfig := func(name string) *config.Config {
		cfg := quotaRuntimeConfig("enforce")
		policy := cfg.QuotaPolicies["app"]
		delete(cfg.QuotaPolicies, "app")
		cfg.QuotaPolicies[name] = policy
		return cfg
	}
	var oldest *quotaBinding
	for i := 0; i < maxQuotaPolicyStates; i++ {
		name := fmt.Sprintf("policy-%d", i)
		candidate := prepareQuotaCandidate(t, runtime, policyConfig(name))
		binding := candidate.Binding(name)
		candidate.Commit()
		if code, dispatched := quotaRequest(binding); code != http.StatusOK || !dispatched {
			t.Fatalf("deplete %s: status=%d dispatched=%v", name, code, dispatched)
		}
		if i == 0 {
			oldest = binding
		}
		removed := prepareQuotaCandidate(t, runtime, &config.Config{})
		removed.Commit()
	}
	if _, err := runtime.prepare(policyConfig("blocked"), &Generation{basicByLoc: make(map[[2]int][]byte)}); err == nil || !strings.Contains(err.Error(), "retired policy") {
		t.Fatalf("depleted policy registry did not reject overflow: %v", err)
	}

	time.Sleep(1050 * time.Millisecond)
	restored := prepareQuotaCandidate(t, runtime, policyConfig("policy-0"))
	binding := restored.Binding("policy-0")
	if binding.state.enforce == oldest.state.enforce {
		t.Fatal("fully refilled policy state was not safely evicted")
	}
	restored.Commit()
	if code, dispatched := quotaRequest(oldest); code != http.StatusServiceUnavailable || dispatched {
		t.Fatalf("evicted policy's stale binding was revived: status=%d dispatched=%v", code, dispatched)
	}
	if code, dispatched := quotaRequest(binding); code != http.StatusOK || !dispatched {
		t.Fatalf("safe full-state recreation: status=%d dispatched=%v", code, dispatched)
	}
}

func TestQuotaObserveUsesCounterfactualStateAndStillServes(t *testing.T) {
	runtime := newQuotaRuntime()
	first := prepareQuotaCandidate(t, runtime, quotaRuntimeConfig("observe"))
	observeBinding := first.Binding("app")
	first.Commit()

	var dispatched atomic.Int32
	var observed atomic.Int32
	handler := quotaMiddleware(observeBinding, "", nil, func(_ string, mode, reason string) {
		if mode == "observe" && reason == string(quota.ReasonClass) {
			observed.Add(1)
		}
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		dispatched.Add(1)
	}))
	for i := 0; i < 2; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.test/", nil))
	}
	if dispatched.Load() != 2 || observed.Load() != 1 {
		t.Fatalf("observe mode dispatched=%d counterfactual denials=%d", dispatched.Load(), observed.Load())
	}
	if got := runtime.decisionSnapshot()["app"]["observe:class"]; got != 1 {
		t.Fatalf("per-policy observe denial count = %v, want 1", got)
	}

	enforce := prepareQuotaCandidate(t, runtime, quotaRuntimeConfig("enforce"))
	enforceBinding := enforce.Binding("app")
	enforce.Commit()
	if code, dispatched := quotaRequest(enforceBinding); code != http.StatusOK || !dispatched {
		t.Fatalf("observe traffic changed enforcement tokens: status=%d dispatched=%v", code, dispatched)
	}
	if code, dispatched := quotaRequest(enforceBinding); code != http.StatusTooManyRequests || dispatched {
		t.Fatalf("enforcement state failed after mode transition: status=%d dispatched=%v", code, dispatched)
	}
}

func TestQuotaClassificationUsesLongestAttributedPool(t *testing.T) {
	binding := &quotaBinding{
		principals: map[quotaPrincipalKey]string{},
		pools: []quotaPool{
			{prefix: netip.MustParsePrefix("10.1.0.0/16"), class: "narrow"},
			{prefix: netip.MustParsePrefix("10.0.0.0/8"), class: "broad"},
		},
	}

	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	request = request.WithContext(clientaddr.NewContext(request.Context(), clientaddr.Identity{
		Client: netip.MustParseAddr("10.1.2.3"),
		Peer:   netip.MustParseAddr("192.0.2.1"),
		Result: clientaddr.ResultAccepted,
	}))
	if class, caller := binding.classify(request); class != "narrow" || caller != "client:10.1.2.3" {
		t.Fatalf("attributed classification = %q, %q", class, caller)
	}

	request = request.WithContext(clientaddr.NewContext(request.Context(), clientaddr.Identity{
		Client: netip.MustParseAddr("10.1.2.3"),
		Peer:   netip.MustParseAddr("192.0.2.1"),
		Result: clientaddr.ResultMalformed,
	}))
	if class, caller := binding.classify(request); class != "public" || caller != "unattributed:192.0.2.1" {
		t.Fatalf("unattributed classification = %q, %q", class, caller)
	}
}

func TestQuotaClassificationDoesNotTrustCIDRWithoutAttributionContext(t *testing.T) {
	binding := &quotaBinding{
		principals: map[quotaPrincipalKey]string{},
		pools: []quotaPool{
			{prefix: netip.MustParsePrefix("192.0.2.0/24"), class: "network"},
		},
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.RemoteAddr = "192.0.2.1:1234"

	if class, caller := binding.classify(request); class != "public" || caller != "unattributed:192.0.2.1" {
		t.Fatalf("unattributed classification = %q, %q", class, caller)
	}
}
