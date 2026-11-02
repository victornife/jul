// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package waf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFixturePackAndParseEvaluationReport(t *testing.T) {
	defPack := DefaultFixturePack()
	if err := ValidateFixturePack(defPack); err != nil {
		t.Fatalf("DefaultFixturePack validation failed: %v", err)
	}

	// Verify examples/waf-policy/fixtures.json parses and validates cleanly.
	exampleBytes, err := os.ReadFile(filepath.Join("..", "..", "examples", "waf-policy", "fixtures.json"))
	if err != nil {
		t.Fatalf("read examples/waf-policy/fixtures.json: %v", err)
	}
	parsedExample, err := ParseFixturePack(exampleBytes)
	if err != nil {
		t.Fatalf("ParseFixturePack(examples/waf-policy/fixtures.json): %v", err)
	}
	if len(parsedExample.Fixtures) != len(defPack.Fixtures) {
		t.Fatalf("example fixtures count = %d, want %d", len(parsedExample.Fixtures), len(defPack.Fixtures))
	}

	// Negative ParseFixturePack and ValidateFixturePack cases.
	if _, err := ParseFixturePack([]byte(`{"schema_version":1,"unknown":true}`)); err == nil {
		t.Error("expected error for unknown JSON field in fixture pack")
	}
	if _, err := ParseFixturePack([]byte(`{"schema_version":1}{"extra":1}`)); err == nil {
		t.Error("expected error for trailing JSON in fixture pack")
	}
	if err := ValidateFixturePack(nil); err == nil {
		t.Error("expected error for nil fixture pack")
	}

	badPacks := []func(*WAFFixturePack){
		func(p *WAFFixturePack) { p.SchemaVersion = 99 },
		func(p *WAFFixturePack) { p.PackID = "" },
		func(p *WAFFixturePack) { p.Fixtures = nil },
		func(p *WAFFixturePack) { p.Fixtures[0].ID = "" },
		func(p *WAFFixturePack) { p.Fixtures[1].ID = p.Fixtures[0].ID },
		func(p *WAFFixturePack) { p.Fixtures[0].Category = "invalid" },
		func(p *WAFFixturePack) { p.Fixtures[0].Method = "" },
		func(p *WAFFixturePack) { p.Fixtures[0].Path = "relative" },
		func(p *WAFFixturePack) { p.Fixtures[0].ExpectedOutcome = "invalid" },
		func(p *WAFFixturePack) { p.Fixtures[0].ExpectedStatus = 99 },
		func(p *WAFFixturePack) { p.Fixtures[0].Body = "-----BEGIN RSA PRIVATE KEY-----" },
		func(p *WAFFixturePack) {
			// Malicious-only pack must be rejected.
			var onlyMal []WAFFixtureCase
			for _, f := range p.Fixtures {
				if f.Category == "malicious" {
					onlyMal = append(onlyMal, f)
				}
			}
			p.Fixtures = onlyMal
		},
		func(p *WAFFixturePack) {
			// Legitimate-only pack must be rejected.
			var onlyLegit []WAFFixtureCase
			for _, f := range p.Fixtures {
				if f.Category == "legitimate" {
					onlyLegit = append(onlyLegit, f)
				}
			}
			p.Fixtures = onlyLegit
		},
	}
	for i, mutate := range badPacks {
		cp := *defPack
		cp.Fixtures = append([]WAFFixtureCase(nil), defPack.Fixtures...)
		mutate(&cp)
		if err := ValidateFixturePack(&cp); err == nil {
			t.Errorf("badPacks[%d] unexpectedly passed validation", i)
		}
	}

	// ParsePolicyManifest validation checks.
	validManifestJSON := `{"schema_version":1,"generated_at":"2026-11-02T00:00:00Z","scope":"global","waf_compiled":true,"mode":"block","block_status":403,"crs_enabled":true,"response_body_inspection":false,"raw_policy_sha256":"abcd","rules":{"total":1}}`
	pm, err := ParsePolicyManifest([]byte(validManifestJSON))
	if err != nil || pm.Scope != "global" {
		t.Fatalf("ParsePolicyManifest valid: %v", err)
	}
	for _, bad := range []string{
		`{"schema_version":1,"unknown":1}`,
		validManifestJSON + `{"extra":1}`,
		`{"schema_version":2,"scope":"global","mode":"block","raw_policy_sha256":"abcd"}`,
		`{"schema_version":1,"scope":"","mode":"block","raw_policy_sha256":"abcd"}`,
		`{"schema_version":1,"scope":"global","mode":"bad","raw_policy_sha256":"abcd"}`,
		`{"schema_version":1,"scope":"global","mode":"block","raw_policy_sha256":""}`,
	} {
		if _, err := ParsePolicyManifest([]byte(bad)); err == nil {
			t.Errorf("ParsePolicyManifest(%s) unexpectedly succeeded", bad)
		}
	}

	// ParseEvaluationReport validation checks.
	validRepJSON := `{"schema_version":1,"summary":{"state":"current","policy_digest":"ev1:a:b","total_cases":1,"passed_cases":1,"failed_cases":0},"cases":[{"id":"c1","category":"legitimate","passed_expectation":true,"expected_outcome":"clean","expected_status":200,"candidate_outcome":"clean","candidate_status":200,"candidate_origin_reached":true}]}`
	rep, err := ParseEvaluationReport([]byte(validRepJSON))
	if err != nil || rep.Summary.TotalCases != 1 {
		t.Fatalf("ParseEvaluationReport valid: %v", err)
	}
	for _, bad := range []string{
		strings.Repeat("x", (256<<10)+10),
		`{"schema_version":1,"unknown":1}`,
		validRepJSON + `{"extra":1}`,
		`{"schema_version":2,"summary":{"policy_digest":"ev1:a:b","total_cases":1},"cases":[{"id":"c1"}]}`,
		`{"schema_version":1,"summary":{"policy_digest":"ev1:a:b","total_cases":0},"cases":[]}`,
		`{"schema_version":1,"summary":{"policy_digest":"ev1:a:b","total_cases":2},"cases":[{"id":"c1"}]}`,
		`{"schema_version":1,"summary":{"total_cases":1},"cases":[{"id":"c1"}]}`,
	} {
		if _, err := ParseEvaluationReport([]byte(bad)); err == nil {
			t.Errorf("ParseEvaluationReport unexpectedly succeeded for bad input len=%d", len(bad))
		}
	}

	// VerifyEvaluationBinding state transitions.
	curInfo := PolicyInfo{
		Mode:           "block",
		CRSEnabled:     true,
		CRSVersion:     EmbeddedCRSVersion(),
		PolicyDigest:   "ev1:ctx:pol1",
		ExternalDigest: "ev1:ctx:ext1",
	}
	if got := VerifyEvaluationBinding(nil, "", "", curInfo, "rawpol1", "rawext1"); Compiled && got.State != "unrun" {
		t.Fatalf("nil summary binding = %+v, want unrun", got)
	}
	baseSum := &WAFEvaluationSummary{
		State:              "current",
		PolicyDigest:       "ev1:ctx:pol1",
		ExternalDigest:     "ev1:ctx:ext1",
		EngineVersion:      EngineVersion(),
		EmbeddedCRSVersion: EmbeddedCRSVersion(),
	}
	if Compiled {
		if got := VerifyEvaluationBinding(baseSum, "rawpol1", "rawext1", curInfo, "rawpol1", "rawext1"); got.State != "current" {
			t.Fatalf("matching binding = %s, want current", got.State)
		}
		if got := VerifyEvaluationBinding(baseSum, "rawpol1", "rawext1", curInfo, "rawpol2", "rawext2"); got.State != "stale_asset_changed" {
			t.Fatalf("changed external SHA = %s, want stale_asset_changed", got.State)
		}
		if got := VerifyEvaluationBinding(baseSum, "rawpol1", "rawext1", curInfo, "rawpol2", "rawext1"); got.State != "stale_policy_changed" {
			t.Fatalf("changed policy SHA = %s, want stale_policy_changed", got.State)
		}
		wrongEngine := *baseSum
		wrongEngine.EngineVersion = "v0.0.1-old"
		if EngineVersion() != "" {
			if got := VerifyEvaluationBinding(&wrongEngine, "rawpol1", "rawext1", curInfo, "rawpol1", "rawext1"); got.State != "stale_engine_or_crs_changed" {
				t.Fatalf("wrong engine = %s, want stale_engine_or_crs_changed", got.State)
			}
		}
		wrongCRS := *baseSum
		wrongCRS.EmbeddedCRSVersion = "3.0.0-old"
		if got := VerifyEvaluationBinding(&wrongCRS, "rawpol1", "rawext1", curInfo, "rawpol1", "rawext1"); got.State != "stale_engine_or_crs_changed" {
			t.Fatalf("wrong CRS = %s, want stale_engine_or_crs_changed", got.State)
		}
	}
}
