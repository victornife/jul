// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/versionid"
)

func TestEvaluatePolicyAndExternalAssetRecovery(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	rulePath := filepath.Join(tmpDir, "site-rules.conf")
	initialRule := `SecRule REQUEST_HEADERS:X-Custom-Scanner "@streq bad-bot" "id:106001,phase:1,deny,status:403,log,msg:'blocked custom scanner'"` + "\n"
	if err := os.WriteFile(rulePath, []byte(initialRule), 0o600); err != nil {
		t.Fatalf("write rulePath: %v", err)
	}

	baseCfg := config.WAFConfig{
		Enabled:          true,
		Mode:             "block",
		BlockStatus:      403,
		CRSEnabled:       true,
		Paranoia:         1,
		RequestBodyLimit: config.Size(1 << 20),
		DirectivesFiles:  []string{rulePath},
	}

	// 1. Evaluate baseline policy against the default synthetic fixture pack.
	rep, err := EvaluatePolicy(ctx, "global", baseCfg, nil, nil, versionid.Process())
	if err != nil {
		t.Fatalf("EvaluatePolicy baseline: %v", err)
	}
	if rep.Summary.TotalCases != 10 || rep.Summary.PassedCases != 10 || rep.Summary.FailedCases != 0 {
		t.Fatalf("unexpected default fixture evaluation summary: %+v, cases=%+v", rep.Summary, rep.Cases)
	}
	if rep.Summary.LegitimateBlocked != 0 || rep.Summary.MaliciousAllowed != 0 {
		t.Fatalf("unexpected regressions on baseline CRS: %+v", rep.Summary)
	}

	// 2. Compare a weakened candidate (mode="detect") against the blocking baseline:
	// malicious fixtures are now allowed through to the origin (malicious_allowed > 0).
	detectCand := baseCfg
	detectCand.Mode = "detect"
	detectCand.InlineRules = `SecAction "id:106002,phase:1,pass,nolog,skipAfter:END_MARKER"` + "\n" + `SecMarker "END_MARKER"`
	repWeak, err := EvaluatePolicy(ctx, "global", detectCand, &baseCfg, nil, versionid.Process())
	if err != nil {
		t.Fatalf("EvaluatePolicy detectCand: %v", err)
	}
	if repWeak.Summary.MaliciousAllowed == 0 || repWeak.Summary.ChangedFromBaseline == 0 || repWeak.Summary.AnalysisCompleteness != "partial_unmodelled_custom" {
		t.Fatalf("expected malicious_allowed>0, changed_from_baseline>0, partial_unmodelled_custom; got %+v", repWeak.Summary)
	}

	// 3. Compare an overly aggressive candidate that blocks legitimate POSTs (legitimate_blocked > 0).
	overblockCand := baseCfg
	overblockCand.InlineRules = `SecRule REQUEST_METHOD "@streq POST" "id:106003,phase:1,deny,status:403,log"`
	repOver, err := EvaluatePolicy(ctx, "global", overblockCand, &baseCfg, nil, versionid.Process())
	if err != nil {
		t.Fatalf("EvaluatePolicy overblockCand: %v", err)
	}
	if repOver.Summary.LegitimateBlocked == 0 || repOver.Summary.FailedCases == 0 {
		t.Fatalf("expected legitimate_blocked>0 on overblocking candidate, got %+v", repOver.Summary)
	}

	// 4. Build policy manifest and verify exact external-asset recovery across same-path edits.
	manifest, fw, err := BuildPolicyManifest(ctx, "global", baseCfg, versionid.Process())
	if err != nil {
		t.Fatalf("BuildPolicyManifest: %v", err)
	}
	_ = fw.Close()
	if len(manifest.ExternalAssets) != 1 || manifest.ExternalAssets[0].SHA256 == "" {
		t.Fatalf("expected 1 external asset in manifest, got %+v", manifest.ExternalAssets)
	}

	// Initial verification succeeds.
	vRes, err := VerifyPolicyAssets(ctx, baseCfg, manifest, versionid.Process())
	if err != nil || !vRes.Verified || vRes.Status != "verified" {
		t.Fatalf("initial VerifyPolicyAssets = %+v, err=%v", vRes, err)
	}

	// Modify the external rule file at the SAME path -> VerifyPolicyAssets detects asset_changed!
	modifiedRule := `SecRule REQUEST_HEADERS:X-Custom-Scanner "@streq other-bot" "id:106001,phase:1,deny,status:403,log"` + "\n"
	if err := os.WriteFile(rulePath, []byte(modifiedRule), 0o600); err != nil {
		t.Fatalf("overwrite rulePath: %v", err)
	}
	vChanged, err := VerifyPolicyAssets(ctx, baseCfg, manifest, versionid.Process())
	if err != nil || vChanged.Verified || vChanged.Status != "asset_changed" {
		t.Fatalf("same-path edit VerifyPolicyAssets = %+v, err=%v; want asset_changed", vChanged, err)
	}

	// Write corrupt SecLang to the file -> VerifyPolicyAssets reports compile_failed!
	if err := os.WriteFile(rulePath, []byte(`SecRule INVALID_DIRECTIVE_SYNTAX`), 0o600); err != nil {
		t.Fatalf("corrupt rulePath: %v", err)
	}
	vCorrupt, err := VerifyPolicyAssets(ctx, baseCfg, manifest, versionid.Process())
	if err != nil || vCorrupt.Verified || vCorrupt.Status != "compile_failed" {
		t.Fatalf("corrupt file VerifyPolicyAssets = %+v, err=%v; want compile_failed", vCorrupt, err)
	}

	// Remove the external file -> VerifyPolicyAssets reports asset_missing!
	if err := os.Remove(rulePath); err != nil {
		t.Fatalf("remove rulePath: %v", err)
	}
	vMissing, err := VerifyPolicyAssets(ctx, baseCfg, manifest, versionid.Process())
	if err != nil || vMissing.Verified || vMissing.Status != "asset_missing" {
		t.Fatalf("missing file VerifyPolicyAssets = %+v, err=%v; want asset_missing", vMissing, err)
	}

	// Restore exact original external file bytes -> VerifyPolicyAssets returns verified=true!
	if err := os.WriteFile(rulePath, []byte(initialRule), 0o600); err != nil {
		t.Fatalf("restore rulePath: %v", err)
	}
	vRestored, err := VerifyPolicyAssets(ctx, baseCfg, manifest, versionid.Process())
	if err != nil || !vRestored.Verified || vRestored.Status != "verified" {
		t.Fatalf("restored VerifyPolicyAssets = %+v, err=%v; want verified", vRestored, err)
	}

	// Change inline_rules while keeping external file identical -> policy_mismatch!
	changedInline := baseCfg
	changedInline.InlineRules = `SecAction "id:106099,phase:1,pass,nolog"`
	vPolMismatch, err := VerifyPolicyAssets(ctx, changedInline, manifest, versionid.Process())
	if err != nil || vPolMismatch.Verified || vPolMismatch.Status != "policy_mismatch" {
		t.Fatalf("policy mismatch VerifyPolicyAssets = %+v, err=%v; want policy_mismatch", vPolMismatch, err)
	}

	// Simulate binary CRS version mismatch -> package_mismatch!
	pkgManifest := *manifest
	pkgManifest.EmbeddedCRSVersion = "3.3.0-legacy"
	vPkgMismatch, err := VerifyPolicyAssets(ctx, baseCfg, &pkgManifest, versionid.Process())
	if err != nil || vPkgMismatch.Verified || vPkgMismatch.Status != "package_mismatch" {
		t.Fatalf("package mismatch VerifyPolicyAssets = %+v, err=%v; want package_mismatch", vPkgMismatch, err)
	}

	// Finding 10: Verify Firewall.Close() is idempotent and marks the engine closed,
	// and repeated VerifyPolicyAssets calls clean up their temporary engines.
	if err := fw.Close(); err != nil || !fw.IsClosed() {
		t.Fatalf("fw.Close() err=%v IsClosed=%v", err, fw.IsClosed())
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("second fw.Close() err=%v", err)
	}
	for i := 0; i < 15; i++ {
		vr, err := VerifyPolicyAssets(ctx, baseCfg, manifest, versionid.Process())
		if err != nil || !vr.Verified {
			t.Fatalf("iter %d VerifyPolicyAssets = %+v, err=%v", i, vr, err)
		}
	}
}

func TestValidateEvaluationReportAndMalformedFixtures(t *testing.T) {
	// Item 10 Subcases 1 & 5:
	// 1. Malformed fixture methods/paths/hosts must return structured errors without panicking.
	for _, badCase := range []WAFFixtureCase{
		{ID: "bad-method-space", Category: "legitimate", Method: "BAD METHOD", Path: "/ok", ExpectedOutcome: OutcomeClean, ExpectedStatus: 200},
		{ID: "bad-method-slash", Category: "legitimate", Method: "GET/POST", Path: "/ok", ExpectedOutcome: OutcomeClean, ExpectedStatus: 200},
		{ID: "bad-path-space", Category: "legitimate", Method: "GET", Path: "/bad path", ExpectedOutcome: OutcomeClean, ExpectedStatus: 200},
		{ID: "bad-path-escape", Category: "legitimate", Method: "GET", Path: "/%ZZ", ExpectedOutcome: OutcomeClean, ExpectedStatus: 200},
		{ID: "bad-path-ctrl", Category: "legitimate", Method: "GET", Path: "/\x00", ExpectedOutcome: OutcomeClean, ExpectedStatus: 200},
		{ID: "bad-host", Category: "legitimate", Method: "GET", Host: "bad/host", Path: "/ok", ExpectedOutcome: OutcomeClean, ExpectedStatus: 200},
	} {
		pack := &WAFFixturePack{
			SchemaVersion: FixturePackSchemaVersion,
			PackID:        "bad-pack",
			Fixtures: []WAFFixtureCase{
				badCase,
				{ID: "mal-1", Category: "malicious", Method: "GET", Path: "/?id=1", ExpectedOutcome: OutcomeInterruptedRequest, ExpectedStatus: 403},
			},
		}
		if err := ValidateFixturePack(pack); err == nil {
			t.Fatalf("expected ValidateFixturePack error for %+v", badCase)
		}
		// Direct call to runSingleFixture must also return OutcomeInspectionError without panicking.
		fw, err := New(context.Background(), config.WAFConfig{Enabled: true, Mode: "block"}, Options{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		out, status, reached, _ := runSingleFixture(fw, config.WAFConfig{Enabled: true, Mode: "block"}, badCase)
		_ = fw.Close()
		if out != OutcomeInspectionError || status != 400 || reached {
			t.Fatalf("runSingleFixture(%q) = (%q, %d, %v), want (inspection_error, 400, false)", badCase.ID, out, status, reached)
		}
	}

	// 2. Contradictory, negative, or duplicate-ID evaluation reports must be rejected.
	validRep := WAFEvaluationReport{
		SchemaVersion: EvaluationReportSchemaVersion,
		Summary: WAFEvaluationSummary{
			State:           "current",
			Scope:           "global",
			PolicyDigest:    "ev1:ctx:pol1",
			TotalCases:      2,
			PassedCases:     2,
			FailedCases:     0,
			LegitimateTotal: 1,
			MaliciousTotal:  1,
		},
		Cases: []WAFFixtureCaseResult{
			{ID: "legit-1", Category: "legitimate", PassedExpectation: true, ExpectedOutcome: OutcomeClean, CandidateOutcome: OutcomeClean, CandidateOriginReached: true},
			{ID: "mal-1", Category: "malicious", PassedExpectation: true, ExpectedOutcome: OutcomeInterruptedRequest, CandidateOutcome: OutcomeInterruptedRequest, CandidateOriginReached: false},
		},
	}
	if err := ValidateEvaluationReport(&validRep); err != nil {
		t.Fatalf("validRep failed validation: %v", err)
	}

	// Failed case with PassedCases=2 / FailedCases=0 -> rejected!
	contradictCounts := validRep
	contradictCounts.Cases = []WAFFixtureCaseResult{
		{ID: "legit-1", Category: "legitimate", PassedExpectation: false, ExpectedOutcome: OutcomeClean, CandidateOutcome: OutcomeInterruptedRequest, CandidateOriginReached: false, RegressionKind: "legitimate_blocked"},
		validRep.Cases[1],
	}
	if err := ValidateEvaluationReport(&contradictCounts); err == nil {
		t.Fatal("expected error for failed case contradicting PassedCases=2/FailedCases=0")
	}

	// Failed case with matching FailedCases=1 but State="current" -> normalized to State="failed"!
	contradictState := contradictCounts
	contradictState.Summary.PassedCases = 1
	contradictState.Summary.FailedCases = 1
	contradictState.Summary.LegitimateBlocked = 1
	contradictState.Summary.State = "current"
	if err := ValidateEvaluationReport(&contradictState); err != nil || contradictState.Summary.State != "failed" {
		t.Fatalf("expected failed_cases > 0 to normalize state to failed, got state=%q err=%v", contradictState.Summary.State, err)
	}

	// Duplicate case IDs -> rejected!
	dupIDs := validRep
	dupIDs.Cases = []WAFFixtureCaseResult{
		validRep.Cases[0],
		{ID: "legit-1", Category: "malicious", PassedExpectation: true, ExpectedOutcome: OutcomeInterruptedRequest, CandidateOutcome: OutcomeInterruptedRequest},
	}
	if err := ValidateEvaluationReport(&dupIDs); err == nil {
		t.Fatal("expected error for duplicate case ID")
	}

	// Negative counts -> rejected!
	negCounts := validRep
	negCounts.Summary.LegitimateBlocked = -1
	if err := ValidateEvaluationReport(&negCounts); err == nil {
		t.Fatal("expected error for negative count")
	}

	// VerifyEvaluationBinding preserves "failed" state when FailedCases > 0 even when digests match.
	failedSummary := contradictState.Summary
	failedSummary.State = "failed"
	boundFailed := VerifyEvaluationBinding(&failedSummary, "", "", PolicyInfo{PolicyDigest: "ev1:ctx:pol1"}, "", "")
	if boundFailed.State != "failed" {
		t.Fatalf("VerifyEvaluationBinding with FailedCases=1 returned state %q, want failed", boundFailed.State)
	}

	// Item 10D: ValidateFixturePack must reject invalid origin_status (-1, 99, 1000)
	// and accept 0 (default 200) and valid 100..599 boundaries without panicking.
	for _, badOriginStatus := range []int{-1, 99, 600, 1000} {
		badOriginPack := WAFFixturePack{
			SchemaVersion: FixturePackSchemaVersion,
			PackID:        "bad-origin-status",
			Fixtures: []WAFFixtureCase{
				{ID: "legit-1", Category: "legitimate", Method: "GET", Path: "/ok", OriginStatus: badOriginStatus, ExpectedOutcome: OutcomeClean, ExpectedStatus: 200},
				{ID: "mal-1", Category: "malicious", Method: "GET", Path: "/bad", ExpectedOutcome: OutcomeInterruptedRequest, ExpectedStatus: 403},
			},
		}
		if err := ValidateFixturePack(&badOriginPack); err == nil || !strings.Contains(err.Error(), "origin_status") {
			t.Fatalf("expected origin_status validation error for %d, got %v", badOriginStatus, err)
		}
	}
	goodOriginPack := WAFFixturePack{
		SchemaVersion: FixturePackSchemaVersion,
		PackID:        "good-origin-status",
		Fixtures: []WAFFixtureCase{
			{ID: "legit-default", Category: "legitimate", Method: "GET", Path: "/ok", OriginStatus: 0, ExpectedOutcome: OutcomeClean, ExpectedStatus: 200},
			{ID: "legit-204", Category: "legitimate", Method: "GET", Path: "/no-content", OriginStatus: 204, ExpectedOutcome: OutcomeClean, ExpectedStatus: 204},
			{ID: "mal-1", Category: "malicious", Method: "GET", Path: "/bad?q=<script>", ExpectedOutcome: OutcomeInterruptedRequest, ExpectedStatus: 403},
		},
	}
	if err := ValidateFixturePack(&goodOriginPack); err != nil {
		t.Fatalf("expected valid origin_status pack to pass, got %v", err)
	}
}
