// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/versionid"
	"jul/internal/waf"
)

func TestWAFReportClassifiesRoutes(t *testing.T) {
	c := &config.Config{WAF: config.WAFConfig{Enabled: true}}
	srv := config.ServerConfig{Listen: ":8443", ServerNames: []string{"shop.example"}}
	inherit := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/"}}
	override := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, WAF: &config.WAFConfig{Enabled: true, Mode: "detect"}}
	off := config.LocationConfig{Match: config.MatchConfig{Type: "exact", Path: "/health"}, WAF: &config.WAFConfig{Enabled: false}}

	r := newWAFReport(c)
	global := waf.PolicyInfo{Mode: "block", BlockStatus: 403, CRSEnabled: true, CRSVersion: "4.25.0", Paranoia: 1, ParanoiaDefault: true,
		RequestBodyLimitBytes: 131072, Rules: waf.RuleCounts{Total: 3, Embedded: 3}}
	const sampleDigest = "ev1:0123456789abcdef0123456789abcdef:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	r.protected(srv, inherit, global)
	r.protected(srv, inherit, waf.PolicyInfo{Mode: "ignored"})
	r.protected(srv, override, waf.PolicyInfo{Mode: "detect", BlockStatus: 403, ExternalFiles: 1, ExternalDigest: sampleDigest, RuleFilesConfigured: 1,
		Rules: waf.RuleCounts{Total: 1, External: 1}})
	r.unprotected(srv, off)
	r.unprotected(srv, inherit)
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.FixedZone("x", 3600))
	got := r.finish(at)

	if got.CompiledAt != "2026-09-27T09:00:00Z" || !got.GlobalEnabled {
		t.Fatalf("report = %+v", got)
	}
	if got.InheritingRoutes != 2 || got.OverrideRoutes != 1 || got.DisabledOverrideRoutes != 1 || got.UnprotectedRoutes != 2 {
		t.Fatalf("coverage = %+v", got)
	}
	if got.Global == nil || got.Global.Mode != "block" || got.Global.CRSVersion != "4.25.0" || got.Global.Rules.Embedded != 3 {
		t.Fatalf("global = %+v", got.Global)
	}
	if len(got.Overrides) != 2 {
		t.Fatalf("overrides = %+v", got.Overrides)
	}
	o := got.Overrides[0]
	if o.Path != "/api" || !o.Enabled || o.Policy == nil || o.Policy.Mode != "detect" || o.Policy.ExternalDigest != sampleDigest {
		t.Fatalf("override = %+v / %+v", o, o.Policy)
	}
	if d := got.Overrides[1]; d.Path != "/health" || d.Enabled || d.Policy != nil {
		t.Fatalf("disabled override = %+v", d)
	}
}

func TestWAFEffectivePublishesServingGeneration(t *testing.T) {
	f := &HandlerFactory{}
	before := f.WAFEffective()
	if before.Compiled != waf.Compiled || before.EmbeddedCRSVersion != waf.EmbeddedCRSVersion() || before.Generation != 0 || before.Global != nil {
		t.Fatalf("before the first generation = %+v", before)
	}
	if waf.Compiled == (before.EmbeddedCRSVersion == "") {
		t.Fatalf("embedded CRS version %q inconsistent with Compiled=%v", before.EmbeddedCRSVersion, waf.Compiled)
	}

	f.publishWAF(3, nil, [4]int{}, nil)
	if f.WAFEffective().Generation != 0 {
		t.Fatal("a nil policy must not replace the serving one")
	}
	p := &admin.WAFEffectivePolicy{GlobalEnabled: true, InheritingRoutes: 1, Overrides: []admin.WAFRouteOverride{{Path: "/a"}}}
	f.publishWAF(7, p, [4]int{1, 0, 0, 0}, nil)
	got := f.WAFEffective()
	if got.Generation != 7 || got.InheritingRoutes != 1 || p.Generation != 0 {
		t.Fatalf("published = %+v (source mutated: %d)", got, p.Generation)
	}
	got.Overrides[0].Path = "/mutated"
	if f.WAFEffective().Overrides[0].Path != "/a" {
		t.Fatal("WAFEffective must return a copy")
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"generation":7`) {
		t.Fatalf("json = %s", raw)
	}

	f.publishWAFDigests(7, "g-dig", map[[2]int]string{{0, 1}: "loc-dig"})
	if gDig, byLoc := f.WAFSourceDigests(7); gDig != "g-dig" || byLoc[[2]int{0, 1}] != "loc-dig" {
		t.Fatalf("WAFSourceDigests(7) = %q, %v", gDig, byLoc)
	}
	if gDig, byLoc := f.WAFSourceDigests(999); gDig != "" || byLoc != nil {
		t.Fatalf("WAFSourceDigests(999) = %q, %v; want empty", gDig, byLoc)
	}
	if computeWAFRouteOrdinals(nil) != nil {
		t.Fatal("computeWAFRouteOrdinals(nil) must be nil")
	}
	snap := f.WAFInvestigationSnapshot(waf.InvestigationFilter{})
	if !snap.Enabled || snap.Capacity != waf.MaxInvestigationEvents {
		t.Fatalf("unexpected default WAFInvestigationSnapshot: %+v", snap)
	}
	var nilGen *Generation
	if nilGen.PublishedGeneration() != 0 {
		t.Fatal("nil Generation PublishedGeneration must be 0")
	}

	// Test classifyWAFProtectionTransition across initial, unchanged, tightened, weakened, and reloaded.
	pBlock2 := &admin.WAFEffectivePolicy{BlockRoutes: 2, DetectRoutes: 0, Global: &admin.WAFPolicySummary{PolicyDigest: "d1"}}
	pBlock1 := &admin.WAFEffectivePolicy{BlockRoutes: 1, DetectRoutes: 1, Global: &admin.WAFPolicySummary{PolicyDigest: "d1"}}
	pBlock2Reload := &admin.WAFEffectivePolicy{BlockRoutes: 2, DetectRoutes: 0, Global: &admin.WAFPolicySummary{PolicyDigest: "d2"}}
	pBlock2OvA := &admin.WAFEffectivePolicy{BlockRoutes: 2, Overrides: []admin.WAFRouteOverride{{Enabled: true, Policy: &admin.WAFPolicySummary{PolicyDigest: "o1"}}}}
	pBlock2OvB := &admin.WAFEffectivePolicy{BlockRoutes: 2, Overrides: []admin.WAFRouteOverride{{Enabled: true, Policy: &admin.WAFPolicySummary{PolicyDigest: "o2"}}}}
	if got := classifyWAFProtectionTransition(nil, pBlock2); got != "initial" {
		t.Fatalf("initial = %q", got)
	}
	if got := classifyWAFProtectionTransition(pBlock2, pBlock2); got != "unchanged" {
		t.Fatalf("unchanged = %q", got)
	}
	if got := classifyWAFProtectionTransition(pBlock2, pBlock1); got != "weakened" {
		t.Fatalf("weakened = %q", got)
	}
	if got := classifyWAFProtectionTransition(pBlock1, pBlock2); got != "tightened" {
		t.Fatalf("tightened = %q", got)
	}
	if got := classifyWAFProtectionTransition(pBlock2, pBlock2Reload); got != "reloaded" {
		t.Fatalf("reloaded global = %q", got)
	}
	if got := classifyWAFProtectionTransition(pBlock2OvA, pBlock2OvB); got != "reloaded" {
		t.Fatalf("reloaded override = %q", got)
	}

	// Finding 6: Changing route A from block to detect while changing route B from
	// detect to block preserves aggregate counts (block=1, detect=1) but MUST
	// report "weakened" rather than "reloaded".
	rPrev := newWAFReport(&config.Config{WAF: config.WAFConfig{Enabled: true}})
	srv := config.ServerConfig{Listen: ":8443", ServerNames: []string{"app.example"}}
	locA := config.LocationConfig{RouteID: config.String("route-a"), Match: config.MatchConfig{Type: "prefix", Path: "/a"}, WAF: &config.WAFConfig{Enabled: true, Mode: "block"}}
	locB := config.LocationConfig{RouteID: config.String("route-b"), Match: config.MatchConfig{Type: "prefix", Path: "/b"}, WAF: &config.WAFConfig{Enabled: true, Mode: "detect"}}
	rPrev.protected(srv, locA, waf.PolicyInfo{Mode: "block", PolicyDigest: "d-block"})
	rPrev.protected(srv, locB, waf.PolicyInfo{Mode: "detect", PolicyDigest: "d-detect"})
	polPrev := rPrev.finish(time.Now())

	rNext := newWAFReport(&config.Config{WAF: config.WAFConfig{Enabled: true}})
	locANext := config.LocationConfig{RouteID: config.String("route-a"), Match: config.MatchConfig{Type: "prefix", Path: "/a"}, WAF: &config.WAFConfig{Enabled: true, Mode: "detect"}}
	locBNext := config.LocationConfig{RouteID: config.String("route-b"), Match: config.MatchConfig{Type: "prefix", Path: "/b"}, WAF: &config.WAFConfig{Enabled: true, Mode: "block"}}
	rNext.protected(srv, locANext, waf.PolicyInfo{Mode: "detect", PolicyDigest: "d-detect"})
	rNext.protected(srv, locBNext, waf.PolicyInfo{Mode: "block", PolicyDigest: "d-block"})
	polNext := rNext.finish(time.Now())

	if polPrev.BlockRoutes != 1 || polPrev.DetectRoutes != 1 || polNext.BlockRoutes != 1 || polNext.DetectRoutes != 1 {
		t.Fatalf("unexpected counts prev=(%d,%d) next=(%d,%d)", polPrev.BlockRoutes, polPrev.DetectRoutes, polNext.BlockRoutes, polNext.DetectRoutes)
	}
	if got := classifyWAFProtectionTransition(polPrev, polNext); got != "weakened" {
		t.Fatalf("swapped route A block->detect + route B detect->block = %q, want weakened", got)
	}

	f.RecordWAFEvaluation(nil)
	f.RecordWAFEvaluationForScope("", nil)
}

func TestWAFScopedEvaluationAndRollupCoverage(t *testing.T) {
	for _, st := range []string{"failed", "stale_asset_changed", "stale_policy_changed", "stale_engine_or_crs_changed", "current", "unrun", "other"} {
		_ = evalStatePriority(st)
	}
	if matchKey, sum, amb, found := matchEffectiveWAFScope(nil, ""); matchKey != "" || sum != nil || amb || found {
		t.Fatalf("nil matchEffectiveWAFScope = %q, %v, %v, %v", matchKey, sum, amb, found)
	}

	effOverridesOnly := &admin.WAFEffectivePolicy{
		OverrideRoutes: 2,
		Overrides: []admin.WAFRouteOverride{
			{
				Listen:      ":8443",
				ServerNames: []string{"a.example"},
				RouteID:     "checkout",
				MatchType:   "prefix",
				Path:        "/pay",
				Enabled:     true,
				Policy:      &admin.WAFPolicySummary{Mode: "block", CRSEnabled: true, PolicyDigest: "d-pay"},
			},
			{
				Listen:      ":8443",
				ServerNames: []string{"b.example"},
				RouteID:     "pay-b",
				MatchType:   "prefix",
				Path:        "/pay",
				Enabled:     true,
				Policy:      &admin.WAFPolicySummary{Mode: "detect", CRSEnabled: true, PolicyDigest: "d-pay-b"},
			},
			{
				Listen:    ":8443",
				RouteID:   "off-route",
				MatchType: "exact",
				Path:      "/off",
				Enabled:   false,
			},
		},
	}
	if _, _, _, found := matchEffectiveWAFScope(effOverridesOnly, "global"); found {
		t.Fatal("expected global scope not found on overrides-only effective policy")
	}
	if _, _, _, found := matchEffectiveWAFScope(effOverridesOnly, "off-route"); found {
		t.Fatal("expected disabled route_id not found")
	}
	if _, sum, amb, found := matchEffectiveWAFScope(effOverridesOnly, "checkout"); !found || amb || sum == nil || sum.PolicyDigest != "d-pay" {
		t.Fatalf("expected checkout route_id match, got found=%v amb=%v sum=%+v", found, amb, sum)
	}
	if _, sum, amb, found := matchEffectiveWAFScope(effOverridesOnly, "a.example/pay"); !found || amb || sum == nil || sum.PolicyDigest != "d-pay" {
		t.Fatalf("expected server_name+path match, got found=%v amb=%v sum=%+v", found, amb, sum)
	}
	if _, _, amb, found := matchEffectiveWAFScope(effOverridesOnly, "/pay"); found || !amb {
		t.Fatalf("expected ambiguous /pay match, got found=%v amb=%v", found, amb)
	}
	if _, _, amb, found := matchEffectiveWAFScope(effOverridesOnly, "/unknown"); found || amb {
		t.Fatalf("expected unknown path not found, got found=%v amb=%v", found, amb)
	}

	// Test resolveConfigWAFScope across single-override, multi-override, route_id, server_name+path, ambiguous, and disabled.
	singleOvCfg := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":8443",
				ServerNames: []string{"shop.example"},
				Locations: []config.LocationConfig{
					{
						RouteID: config.String("checkout"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/pay"},
						WAF:     &config.WAFConfig{Enabled: true, Mode: "block", CRSEnabled: true},
					},
					{
						RouteID: config.String("disabled-route"),
						Match:   config.MatchConfig{Type: "exact", Path: "/health"},
						WAF:     &config.WAFConfig{Enabled: false},
					},
				},
			},
		},
	}
	if _, key, label, err := resolveConfigWAFScope(singleOvCfg, nil, ""); err != nil || key == "" || label != "checkout" {
		t.Fatalf("single override default scope = key=%q label=%q err=%v", key, label, err)
	}
	if _, _, _, err := resolveConfigWAFScope(singleOvCfg, nil, "global"); err == nil {
		t.Fatal("expected error for explicit global scope when global WAF is disabled")
	}
	if _, _, _, err := resolveConfigWAFScope(singleOvCfg, nil, "disabled-route"); err == nil {
		t.Fatal("expected error for disabled route_id")
	}
	if _, _, label, err := resolveConfigWAFScope(singleOvCfg, nil, "checkout"); err != nil || label != "checkout" {
		t.Fatalf("checkout route_id = label=%q err=%v", label, err)
	}
	if _, _, label, err := resolveConfigWAFScope(singleOvCfg, nil, "shop.example/pay"); err != nil || label != "checkout" {
		t.Fatalf("shop.example/pay = label=%q err=%v", label, err)
	}
	if _, _, _, err := resolveConfigWAFScope(singleOvCfg, nil, "/nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent scope")
	}

	multiOvCfg := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":8443",
				ServerNames: []string{"a.example"},
				Locations: []config.LocationConfig{
					{
						RouteID: config.String("pay-a"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/pay"},
						WAF:     &config.WAFConfig{Enabled: true, Mode: "block"},
					},
				},
			},
			{
				Listen:      ":8443",
				ServerNames: []string{"b.example"},
				Locations: []config.LocationConfig{
					{
						RouteID: config.String("pay-b"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/pay"},
						WAF:     &config.WAFConfig{Enabled: true, Mode: "detect"},
					},
				},
			},
		},
	}
	if _, _, _, err := resolveConfigWAFScope(multiOvCfg, nil, ""); err == nil {
		t.Fatal("expected error when multiple overrides exist and scope is empty")
	}
	if _, _, _, err := resolveConfigWAFScope(multiOvCfg, nil, "/pay"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous error for /pay, got %v", err)
	}

	// Exercise RecordWAFEvaluation and overrides-only WAFEffective rollup priority.
	f := &HandlerFactory{}
	f.publishWAF(1, effOverridesOnly, [4]int{}, nil)
	f.RecordWAFEvaluation(&waf.WAFEvaluationReport{
		Summary: waf.WAFEvaluationSummary{
			State:        "current",
			Scope:        "checkout",
			PolicyDigest: "d-pay",
		},
	})
	f.RecordWAFEvaluation(&waf.WAFEvaluationReport{
		Summary: waf.WAFEvaluationSummary{
			State:        "failed",
			Scope:        "pay-b",
			PolicyDigest: "d-pay-b",
			FailedCases:  1,
		},
	})
	effAfter := f.WAFEffective()
	wantRollupState := "failed"
	if !waf.Compiled {
		wantRollupState = "unavailable"
	}
	if effAfter.Evaluation == nil || effAfter.Evaluation.State != wantRollupState {
		t.Fatalf("expected overrides-only rollup evaluation state %s, got %+v", wantRollupState, effAfter.Evaluation)
	}
	if findScopedEvaluationView(nil, "global") != nil {
		t.Fatal("findScopedEvaluationView(nil) must be nil")
	}
	_ = findScopedEvaluationView(effAfter, "checkout")

	if diskAssetsMatchRecorded([]waf.ExternalAssetRecord{{Path: "relative/missing/asset.conf"}}) {
		t.Fatal("expected relative missing asset to return false")
	}
	fNoOverrides := &HandlerFactory{
		liveWAF:            &admin.WAFEffectivePolicy{Generation: 1},
		liveGlobalAssets:   []waf.ExternalAssetRecord{{Path: "relative/missing/asset.conf"}},
		liveOverrideAssets: map[string][]waf.ExternalAssetRecord{},
	}
	effStaleAsset := fNoOverrides.WAFEffective()
	if effStaleAsset.Evaluation == nil || effStaleAsset.Evaluation.State != "stale_asset_changed" {
		t.Fatalf("expected stale_asset_changed when out.Global == nil and asset is missing, got %+v", effStaleAsset.Evaluation)
	}

	// Item 10A, 10B, 10C:
	// - 10A: Inherited-route CLI report import resolves through liveCfg to "global"
	//        (both by durable route_id and by unambiguous path), while wrong context
	//        returns stale_policy_changed and ambiguous path returns error.
	// - 10B: Deterministic barrier (onBeforeEvalCommit) during live evaluation and
	//        report import verifies that concurrent reload disabling, removing, or
	//        reassigning the target route returns stale_policy_changed without
	//        recording the retired report or substituting another active scope's summary.
	// - 10C: Reload churn replacing route IDs prunes retired scoped reports from
	//        lastWAFEvalByScope and rejects late retired-scope completions.
	if waf.Compiled {
		testItem10ScopedLifecycleAndConcurrency(t)
	}
}

func testItem10ScopedLifecycleAndConcurrency(t *testing.T) {
	ctx := context.Background()
	vCtx := versionid.Process()
	wrongVCtx, err := versionid.Random()
	if err != nil {
		t.Fatal(err)
	}

	globalWAF := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		CRSEnabled:  true,
		Paranoia:    1,
	}
	overrideWAF := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		CRSEnabled:  true,
		Paranoia:    1,
		InlineRules: `SecAction "id:199999,phase:1,pass,nolog"`,
	}
	serveCfg := &config.Config{
		WAF: globalWAF,
		Servers: []config.ServerConfig{
			{
				Listen:      ":8443",
				ServerNames: []string{"app.example"},
				Locations: []config.LocationConfig{
					{
						RouteID: config.String("inherit-docs"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/docs"},
						Root:    t.TempDir(),
					},
					{
						RouteID: config.String("override-pay"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/pay"},
						Root:    t.TempDir(),
						WAF:     &overrideWAF,
					},
					{
						RouteID: config.String("shared-a"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/shared"},
						Root:    t.TempDir(),
					},
				},
			},
			{
				Listen:      ":8444",
				ServerNames: []string{"other.example"},
				Locations: []config.LocationConfig{
					{
						RouteID: config.String("shared-b"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/shared"},
						Root:    t.TempDir(),
					},
				},
			},
		},
	}

	globalRep, err := waf.EvaluatePolicy(ctx, "inherit-docs", globalWAF, nil, nil, vCtx)
	if err != nil {
		t.Fatalf("EvaluatePolicy globalWAF: %v", err)
	}
	overrideRep, err := waf.EvaluatePolicy(ctx, "override-pay", overrideWAF, nil, nil, vCtx)
	if err != nil {
		t.Fatalf("EvaluatePolicy overrideWAF: %v", err)
	}

	f := &HandlerFactory{Versions: vCtx}
	f.liveConfig = serveCfg
	ordinals := computeWAFRouteOrdinals(serveCfg)
	payKey := wafRouteIdentityKey(serveCfg.Servers[0], serveCfg.Servers[0].Locations[1], ordinals[0][1])
	eff := &admin.WAFEffectivePolicy{
		Generation:       1,
		GlobalEnabled:    true,
		InheritingRoutes: 3,
		OverrideRoutes:   1,
		BlockRoutes:      3,
		DetectRoutes:     1,
		Global: &admin.WAFPolicySummary{
			Mode:         "block",
			BlockStatus:  403,
			CRSEnabled:   true,
			CRSVersion:   globalRep.Summary.EmbeddedCRSVersion,
			PolicyDigest: globalRep.Summary.PolicyDigest,
		},
		Overrides: []admin.WAFRouteOverride{
			{
				RouteID:      "override-pay",
				Listen:       ":8443",
				ServerNames:  []string{"app.example"},
				MatchType:    "prefix",
				Path:         "/pay",
				MatchOrdinal: ordinals[0][1],
				Enabled:      true,
				Policy: &admin.WAFPolicySummary{
					Mode:         "block",
					BlockStatus:  403,
					CRSEnabled:   true,
					CRSVersion:   overrideRep.Summary.EmbeddedCRSVersion,
					PolicyDigest: overrideRep.Summary.PolicyDigest,
				},
			},
		},
	}
	f.publishWAF(1, eff, [4]int{}, nil)

	// 10A-1: Importing a separate-process CLI report for an inheriting route by durable route_id ("inherit-docs")
	// and by unambiguous path ("/docs") must bind to the serving global policy and return State="current".
	repBytes, _ := json.Marshal(globalRep)
	gotInheritID, err := f.HandleAdminWAFEvaluate(ctx, repBytes)
	if err != nil || gotInheritID.State != "current" || gotInheritID.Scope != "inherit-docs" {
		t.Fatalf("10A inherited route_id import = %+v, err=%v", gotInheritID, err)
	}
	pathRep := *globalRep
	pathRep.Summary.Scope = "/docs"
	pathRepBytes, _ := json.Marshal(pathRep)
	gotInheritPath, err := f.HandleAdminWAFEvaluate(ctx, pathRepBytes)
	if err != nil || gotInheritPath.State != "current" || gotInheritPath.Scope != "/docs" {
		t.Fatalf("10A inherited path import = %+v, err=%v", gotInheritPath, err)
	}

	// 10A-2: Importing an override route report by route_id ("override-pay") and path ("/pay") binds to the override.
	ovBytes, _ := json.Marshal(overrideRep)
	gotOvID, err := f.HandleAdminWAFEvaluate(ctx, ovBytes)
	if err != nil || gotOvID.State != "current" || gotOvID.Scope != "override-pay" {
		t.Fatalf("10A override route_id import = %+v, err=%v", gotOvID, err)
	}

	// 10A-3: Wrong version context report returns stale_policy_changed.
	wrongRep, err := waf.EvaluatePolicy(ctx, "inherit-docs", globalWAF, nil, nil, wrongVCtx)
	if err != nil {
		t.Fatalf("EvaluatePolicy wrongVCtx: %v", err)
	}
	wrongBytes, _ := json.Marshal(wrongRep)
	gotWrong, err := f.HandleAdminWAFEvaluate(ctx, wrongBytes)
	if err != nil || gotWrong.State != "stale_policy_changed" {
		t.Fatalf("10A wrong context import = %+v, err=%v", gotWrong, err)
	}

	// 10A-4: Ambiguous path ("/shared" across two servers) is rejected on both import and live evaluate.
	ambRep := *globalRep
	ambRep.Summary.Scope = "/shared"
	ambBytes, _ := json.Marshal(ambRep)
	if _, err := f.HandleAdminWAFEvaluate(ctx, ambBytes); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("10A expected ambiguous error on import, got %v", err)
	}

	// 10B-1: Concurrent reload removes "override-pay" while global WAF remains active during live evaluation.
	// Must return stale_policy_changed (NEVER global's "current" summary!) and must not retain override-pay in lastWAFEvalByScope.
	f.onBeforeEvalCommit = func(scope string) {
		cfgWithoutPay := *serveCfg
		srv0 := serveCfg.Servers[0]
		srv0.Locations = []config.LocationConfig{serveCfg.Servers[0].Locations[0]}
		cfgWithoutPay.Servers = []config.ServerConfig{srv0}
		f.moduleMu.Lock()
		f.liveConfig = &cfgWithoutPay
		f.moduleMu.Unlock()
		effWithoutPay := *eff
		effWithoutPay.OverrideRoutes = 0
		effWithoutPay.Overrides = nil
		f.publishWAF(2, &effWithoutPay, [4]int{}, nil)
	}
	gotRemovedDuringLive, err := f.HandleAdminWAFEvaluate(ctx, []byte(`{"scope":"override-pay"}`))
	f.onBeforeEvalCommit = nil
	if err != nil || gotRemovedDuringLive.State != "stale_policy_changed" || gotRemovedDuringLive.Scope != "override-pay" {
		t.Fatalf("10B live eval with concurrent route removal = %+v, err=%v", gotRemovedDuringLive, err)
	}
	f.moduleMu.Lock()
	_, hasRetiredPay := f.lastWAFEvalByScope[payKey]
	f.moduleMu.Unlock()
	if hasRetiredPay {
		t.Fatal("10B retired override-pay scope was retained in lastWAFEvalByScope after concurrent removal")
	}

	// 10B-2: Restore serveCfg/eff, then during report import for "override-pay", concurrent reload disables all WAF.
	f.moduleMu.Lock()
	f.liveConfig = serveCfg
	f.moduleMu.Unlock()
	f.publishWAF(3, eff, [4]int{}, nil)
	f.onBeforeEvalCommit = func(scope string) {
		f.moduleMu.Lock()
		f.liveConfig = &config.Config{}
		f.moduleMu.Unlock()
		f.publishWAF(4, &admin.WAFEffectivePolicy{Generation: 4}, [4]int{}, nil)
	}
	gotDisabledDuringImport, err := f.HandleAdminWAFEvaluate(ctx, ovBytes)
	f.onBeforeEvalCommit = nil
	if err != nil || gotDisabledDuringImport.State != "stale_policy_changed" {
		t.Fatalf("10B import with concurrent all-WAF disablement = %+v, err=%v", gotDisabledDuringImport, err)
	}

	// 10D: Malformed origin_status in POST /api/security/waf/evaluate returns a structured error without panicking.
	f.moduleMu.Lock()
	f.liveConfig = serveCfg
	f.moduleMu.Unlock()
	f.publishWAF(5, eff, [4]int{}, nil)
	badPackJSON := `{"schema_version":1,"pack_id":"bad-origin","fixtures":[{"id":"legit-1","category":"legitimate","method":"GET","path":"/ok","origin_status":99,"expected_outcome":"clean","expected_status":200},{"id":"mal-1","category":"malicious","method":"GET","path":"/bad","expected_outcome":"interrupted_request","expected_status":403}]}`
	if _, err := f.HandleAdminWAFEvaluate(ctx, []byte(badPackJSON)); err == nil || !strings.Contains(err.Error(), "origin_status") {
		t.Fatalf("10D expected origin_status error from HandleAdminWAFEvaluate, got %v", err)
	}

	// 10C: Reload churn across 50 replaced route override IDs must prune retired scopes
	// and reject late completions for retired scopes.
	for i := 0; i < 50; i++ {
		rid := "churn-route-" + string(rune('a'+(i%26))) + string(rune('0'+(i/26)))
		churnCfg := &config.Config{
			Servers: []config.ServerConfig{
				{
					Listen: ":8443",
					Locations: []config.LocationConfig{
						{
							RouteID: config.String(rid),
							Match:   config.MatchConfig{Type: "prefix", Path: "/churn"},
							WAF:     &overrideWAF,
						},
					},
				},
			},
		}
		f.moduleMu.Lock()
		f.liveConfig = churnCfg
		f.moduleMu.Unlock()
		churnEff := &admin.WAFEffectivePolicy{
			Generation:     uint64(10 + i),
			OverrideRoutes: 1,
			Overrides: []admin.WAFRouteOverride{
				{
					RouteID:   rid,
					Listen:    ":8443",
					MatchType: "prefix",
					Path:      "/churn",
					Enabled:   true,
					Policy: &admin.WAFPolicySummary{
						Mode:         "detect",
						CRSEnabled:   true,
						PolicyDigest: overrideRep.Summary.PolicyDigest,
					},
				},
			},
		}
		f.publishWAF(uint64(10+i), churnEff, [4]int{}, nil)
		f.RecordWAFEvaluationForScope(rid, overrideRep)
		// Attempt to record a retired route ID from a previous iteration.
		f.RecordWAFEvaluationForScope("retired-ghost-route", overrideRep)
		f.moduleMu.Lock()
		scopeCount := len(f.lastWAFEvalByScope)
		f.moduleMu.Unlock()
		if scopeCount > 1 {
			t.Fatalf("10C iteration %d: lastWAFEvalByScope accumulated %d entries, want <= 1", i, scopeCount)
		}
	}
}
