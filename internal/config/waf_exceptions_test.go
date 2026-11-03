// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

func TestValidateWAFExceptionsAndTargets(t *testing.T) {
	base := func(w WAFConfig) *Config {
		return &Config{
			WAF: w,
			Servers: []ServerConfig{
				{
					Listen: ":8080",
					Locations: []LocationConfig{
						{Match: MatchConfig{Type: "prefix", Path: "/"}, Return: 200},
					},
				},
			},
		}
	}

	// Valid narrow exception + separate blocking/detection paranoia.
	valid := base(WAFConfig{
		Enabled:           true,
		Mode:              "block",
		BlockStatus:       403,
		CRSEnabled:        true,
		BlockingParanoia:  1,
		DetectionParanoia: 3,
		Exceptions: []WAFException{
			{
				ID:        "exc-bio",
				RuleID:    942100,
				Scope:     "narrow_target",
				Target:    "ARGS:profile_bio",
				Methods:   []string{"POST", "PUT"},
				Rationale: "Allow SQL keywords in profile bio markdown",
				Owner:     "secops",
				Reference: "SEC-631",
				CreatedAt: "2026-11-01",
				ReviewAt:  "2027-02-01",
				Fixtures:  []string{"benign-bio"},
			},
		},
	})
	if err := Validate(valid); err != nil {
		t.Fatalf("expected valid config to pass Validate, got %v", err)
	}

	invalidCases := []struct {
		name    string
		waf     WAFConfig
		wantSub string
	}{
		{
			name: "detection_paranoia below blocking_paranoia",
			waf: WAFConfig{
				Enabled:           true,
				Mode:              "block",
				BlockStatus:       403,
				CRSEnabled:        true,
				BlockingParanoia:  3,
				DetectionParanoia: 2,
			},
			wantSub: "detection_paranoia (2) must be >= blocking_paranoia (3)",
		},
		{
			name: "conflicting legacy paranoia and blocking_paranoia",
			waf: WAFConfig{
				Enabled:          true,
				Mode:             "block",
				BlockStatus:      403,
				CRSEnabled:       true,
				Paranoia:         2,
				BlockingParanoia: 1,
			},
			wantSub: "cannot combine legacy paranoia",
		},
		{
			name: "empty target key ARGS: rejected",
			waf: WAFConfig{
				Enabled:     true,
				Mode:        "block",
				BlockStatus: 403,
				CRSEnabled:  true,
				Exceptions: []WAFException{
					{RuleID: 942100, Scope: "narrow_target", Target: "ARGS:"},
				},
			},
			wantSub: "non-empty field/header key",
		},
		{
			name: "bare collection ARGS without colon rejected",
			waf: WAFConfig{
				Enabled:     true,
				Mode:        "block",
				BlockStatus: 403,
				CRSEnabled:  true,
				Exceptions: []WAFException{
					{RuleID: 942100, Scope: "narrow_target", Target: "ARGS"},
				},
			},
			wantSub: "<COLLECTION>:<KEY>",
		},
		{
			name: "regex or semicolon in target key rejected",
			waf: WAFConfig{
				Enabled:     true,
				Mode:        "block",
				BlockStatus: 403,
				CRSEnabled:  true,
				Exceptions: []WAFException{
					{RuleID: 942100, Scope: "narrow_target", Target: "ARGS:/bio.*/;ctl:ruleEngine=Off"},
				},
			},
			wantSub: "unsupported character",
		},
		{
			name: "reserved generated rule ID rejected",
			waf: WAFConfig{
				Enabled:     true,
				Mode:        "block",
				BlockStatus: 403,
				CRSEnabled:  true,
				Exceptions: []WAFException{
					{RuleID: 890001, Scope: "narrow_target", Target: "ARGS:foo"},
				},
			},
			wantSub: "reserved generated rule ID range",
		},
		{
			name: "duplicate exception tuple rejected",
			waf: WAFConfig{
				Enabled:     true,
				Mode:        "block",
				BlockStatus: 403,
				CRSEnabled:  true,
				Exceptions: []WAFException{
					{RuleID: 942100, Scope: "narrow_target", Target: "ARGS:foo", Methods: []string{"POST"}},
					{RuleID: 942100, Scope: "narrow_target", Target: "args:foo", Methods: []string{"post"}},
				},
			},
			wantSub: "duplicates exception",
		},
		{
			name: "review_at before created_at rejected",
			waf: WAFConfig{
				Enabled:     true,
				Mode:        "block",
				BlockStatus: 403,
				CRSEnabled:  true,
				Exceptions: []WAFException{
					{
						RuleID:    942100,
						Scope:     "narrow_target",
						Target:    "ARGS:foo",
						CreatedAt: "2026-11-01",
						ReviewAt:  "2026-10-01",
					},
				},
			},
			wantSub: "cannot precede created_at",
		},
		{
			name: "blocking_paranoia out of range and detection_paranoia without CRS",
			waf: WAFConfig{
				Enabled:           true,
				Mode:              "block",
				BlockStatus:       403,
				CRSEnabled:        false,
				InlineRules:       `SecAction "id:1001,phase:1,pass"`,
				BlockingParanoia:  5,
				DetectionParanoia: 2,
			},
			wantSub: "blocking_paranoia must be between 1 and 4",
		},
		{
			name: "detection_paranoia out of range",
			waf: WAFConfig{
				Enabled:           true,
				Mode:              "block",
				BlockStatus:       403,
				CRSEnabled:        true,
				DetectionParanoia: 5,
			},
			wantSub: "detection_paranoia must be between 1 and 4",
		},
		{
			name: "unsupported target collection, bad method, duplicate ID, control chars, bad dates",
			waf: WAFConfig{
				Enabled:     true,
				Mode:        "block",
				BlockStatus: 403,
				CRSEnabled:  true,
				Exceptions: []WAFException{
					{
						ID:        "bad id!",
						RuleID:    0,
						Scope:     "narrow_target",
						Target:    "UNKNOWN_COL:foo",
						Methods:   []string{"POST", "POST", "TRACE"},
						Rationale: "bad\x00rationale",
						Owner:     "bad\x00owner",
						Reference: "bad\x00ref",
						CreatedAt: "not-a-date",
						ReviewAt:  "not-a-date",
						Fixtures:  []string{"bad fixture!"},
					},
					{
						ID:     "dup-id",
						RuleID: 942100,
						Scope:  "rule_remove",
						Target: "ARGS:should_be_empty",
					},
					{
						ID:     "dup-id",
						RuleID: 942101,
						Scope:  "unknown_scope",
					},
				},
			},
			wantSub: "unsupported collection",
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(base(tc.waf))
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Validate(%s) = %v, want substring %q", tc.name, err, tc.wantSub)
			}
		})
	}

	// Also verify detection_paranoia alone (with blocking_paranoia = 0) defaults blocking to 1.
	detectOnlyParanoia := base(WAFConfig{
		Enabled:           true,
		Mode:              "block",
		BlockStatus:       403,
		CRSEnabled:        true,
		DetectionParanoia: 2,
	})
	if err := Validate(detectOnlyParanoia); err != nil {
		t.Fatalf("expected detection_paranoia=2 alone to pass Validate, got %v", err)
	}
	if got := (WAFConfig{Paranoia: 2}).EffectiveDetectionParanoia(); got != 2 {
		t.Fatalf("EffectiveDetectionParanoia(Paranoia=2) = %d, want 2", got)
	}
}

func TestWAFExceptionReviewDueMetadataAndLint(t *testing.T) {
	tr := true
	cfg := &Config{
		Compression: CompressionConfig{Enabled: &tr},
		WAF: WAFConfig{
			Enabled:     true,
			Mode:        "block",
			BlockStatus: 403,
			CRSEnabled:  true,
			Exceptions: []WAFException{
				{
					ID:        "past-due-exc",
					RuleID:    942100,
					Scope:     "narrow_target",
					Target:    "ARGS:bio",
					ReviewAt:  "2024-01-01",
					Rationale: "Legacy form compatibility",
				},
				{
					ID:        "future-exc",
					RuleID:    941100,
					Scope:     "narrow_target",
					Target:    "ARGS:note",
					ReviewAt:  "2099-01-01",
					Rationale: "Current editor field",
				},
			},
		},
		Servers: []ServerConfig{
			{
				Listen: ":8080",
				Locations: []LocationConfig{
					{
						Match:  MatchConfig{Type: "prefix", Path: "/"},
						Return: 200,
						WAF: &WAFConfig{
							Enabled:     true,
							Mode:        "block",
							BlockStatus: 403,
							CRSEnabled:  true,
							Exceptions: []WAFException{
								{
									RuleID:   942200,
									Scope:    "rule_remove",
									ReviewAt: "2023-06-01",
								},
							},
						},
					},
				},
			},
		},
	}

	diags := Lint(cfg)
	var foundGlobal, foundLoc bool
	for _, d := range diags {
		if d.Code == "waf_exception_review_due" {
			if strings.Contains(d.Message, "past-due-exc") {
				foundGlobal = true
			}
			if strings.Contains(d.Message, "rule 942200") {
				foundLoc = true
			}
		}
		if strings.Contains(d.Message, "future-exc") {
			t.Fatalf("future review_at should not warn: %+v", d)
		}
	}
	if !foundGlobal || !foundLoc {
		t.Fatalf("expected global and location waf_exception_review_due diagnostics, got %+v", diags)
	}
}
