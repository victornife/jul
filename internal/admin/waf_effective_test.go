// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/rbac"
)

// The Security projection overlays the serving generation's compiled policy
// (#440): even when the on-disk config says otherwise, waf_effective reports
// what is enforced, and it is omitted when no source is wired.
func TestSecurityProjectionOverlaysServingWAFPolicy(t *testing.T) {
	onDisk := &config.Config{WAF: config.WAFConfig{Enabled: true, Mode: "block", CRSEnabled: true}}
	serving := &WAFEffectivePolicy{
		Compiled:           true,
		EmbeddedCRSVersion: "4.25.0",
		Generation:         9,
		GlobalEnabled:      true,
		InheritingRoutes:   1,
		Global:             &WAFPolicySummary{Mode: "detect", BlockStatus: 403, Rules: WAFRuleCounts{Total: 1, Inline: 1}},
	}
	get := func(deps Deps) SecurityProjection {
		t.Helper()
		deps.LoadConfig = func() (*config.Config, error) { return onDisk, nil }
		s := newTestServer(t, config.AdminConfig{}, deps)
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/security", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		var out SecurityProjection
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := get(Deps{}); out.WAFEffective != nil {
		t.Fatalf("unwired waf_effective = %+v", out.WAFEffective)
	}
	out := get(Deps{WAFEffective: func() *WAFEffectivePolicy { return serving }})
	if out.WAFGlobalMode != "block" {
		t.Fatalf("configuration projection changed: %q", out.WAFGlobalMode)
	}
	e := out.WAFEffective
	if e == nil || e.Generation != 9 || e.Global == nil || e.Global.Mode != "detect" || e.EmbeddedCRSVersion != "4.25.0" {
		t.Fatalf("waf_effective = %+v", e)
	}
}

func TestHandleWAFEvaluateEndpoint(t *testing.T) {
	// 1. Unwired EvaluateWAF returns 503.
	sUnwired := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	sUnwired.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/security/waf/evaluate", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired status = %d, want 503", rr.Code)
	}

	// 2. Method not allowed returns 405.
	rrGet := httptest.NewRecorder()
	sUnwired.handleWAFEvaluate(rrGet, httptest.NewRequest(http.MethodGet, "/api/security/waf/evaluate", nil))
	if rrGet.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rrGet.Code)
	}

	// 3. Wired EvaluateWAF handles success, unavailable error (503), and bad request (400).
	sWired := newTestServer(t, config.AdminConfig{}, Deps{
		EvaluateWAF: func(_ context.Context, body []byte) (*WAFEvaluationSummaryView, error) {
			switch strings.TrimSpace(string(body)) {
			case "unavailable":
				return nil, ErrWAFEvaluationUnavailable
			case "bad":
				return nil, errors.New("invalid pack")
			default:
				return &WAFEvaluationSummaryView{State: "current", TotalCases: 10, PassedCases: 10}, nil
			}
		},
	})
	rrOK := httptest.NewRecorder()
	sWired.routes().ServeHTTP(rrOK, httptest.NewRequest(http.MethodPost, "/api/security/waf/evaluate", strings.NewReader("{}")))
	if rrOK.Code != http.StatusOK {
		t.Fatalf("OK status = %d, want 200", rrOK.Code)
	}

	rrUnavail := httptest.NewRecorder()
	sWired.routes().ServeHTTP(rrUnavail, httptest.NewRequest(http.MethodPost, "/api/security/waf/evaluate", strings.NewReader("unavailable")))
	if rrUnavail.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status = %d, want 503", rrUnavail.Code)
	}

	rrBad := httptest.NewRecorder()
	sWired.routes().ServeHTTP(rrBad, httptest.NewRequest(http.MethodPost, "/api/security/waf/evaluate", strings.NewReader("bad")))
	if rrBad.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d, want 400", rrBad.Code)
	}
}

func TestWAFExceptionPatchAndDiff(t *testing.T) {
	before := &config.Config{
		WAF: config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			BlockStatus:       403,
			CRSEnabled:        true,
			BlockingParanoia:  2,
			DetectionParanoia: 3,
		},
		Servers: []config.ServerConfig{
			{
				Listen: ":8080",
				Locations: []config.LocationConfig{
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/api"},
						Return: 200,
						WAF: &config.WAFConfig{
							Enabled:           true,
							Mode:              "block",
							BlockStatus:       403,
							CRSEnabled:        true,
							BlockingParanoia:  2,
							DetectionParanoia: 2,
						},
					},
				},
			},
		},
	}

	after := &config.Config{
		WAF: config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			BlockStatus:       403,
			CRSEnabled:        true,
			BlockingParanoia:  1,
			DetectionParanoia: 4,
			Exceptions: []config.WAFException{
				{
					ID:       "glob-remove",
					RuleID:   942100,
					Scope:    "rule_remove",
					ReviewAt: "2027-01-01",
				},
			},
		},
		Servers: []config.ServerConfig{
			{
				Listen: ":8080",
				Locations: []config.LocationConfig{
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/api"},
						Return: 200,
						WAF:    before.Servers[0].Locations[0].WAF,
					},
				},
			},
		},
	}

	// Mutate the route override via applyPatch(location_waf_set).
	summary, err := applyPatch(after, patchRequest{
		Op:        "location_waf_set",
		Listen:    ":8080",
		MatchType: "prefix",
		Path:      "/api",
		WAF: &locationWAF{
			Enabled:           true,
			Mode:              "block",
			CRSEnabled:        true,
			BlockStatus:       403,
			BlockingParanoia:  1,
			DetectionParanoia: 3,
			Exceptions: []config.WAFException{
				{
					ID:        "loc-bio",
					RuleID:    942100,
					Scope:     "narrow_target",
					Target:    "ARGS:bio",
					Methods:   []string{"POST"},
					Rationale: "Allow bio markdown",
					Owner:     "secops",
					Reference: "SEC-631",
					ReviewAt:  "2027-03-01",
				},
				{
					ID:     "loc-remove",
					RuleID: 941100,
					Scope:  "rule_remove",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("applyPatch(location_waf_set): %v", err)
	}
	if !strings.Contains(summary, "WAF override set") {
		t.Fatalf("unexpected patch summary: %q", summary)
	}

	diff := DiffConfigs(before, after)
	if len(diff.Modifications) < 4 {
		t.Fatalf("expected global and location paranoia + exception modifications, got %+v", diff.Modifications)
	}
	var sawRuleRemoveWarn, sawBlockingLowerWarn bool
	for _, w := range diff.Warnings {
		if strings.Contains(w, "scope=rule_remove") {
			sawRuleRemoveWarn = true
		}
		if strings.Contains(w, "blocking paranoia") {
			sawBlockingLowerWarn = true
		}
	}
	if !sawRuleRemoveWarn || !sawBlockingLowerWarn {
		t.Fatalf("expected rule_remove and blocking paranoia warnings, got %+v", diff.Warnings)
	}
}

func TestSecurityProjectionExceptionDisclosureBoundary(t *testing.T) {
	cfg := &config.Config{
		WAF: config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			CRSEnabled:        true,
			BlockingParanoia:  1,
			DetectionParanoia: 2,
			DirectivesFiles:   []string{"/etc/jul/secret-path.conf"},
			InlineRules:       "SecAction \"id:1001,phase:1,pass\"",
			Exceptions: []config.WAFException{
				{
					ID:        "exc-global",
					RuleID:    942100,
					Scope:     "narrow_target",
					Target:    "ARGS:bio",
					Rationale: "Internal ticket rationale",
					Owner:     "secops-internal",
					Reference: "INC-9999",
				},
			},
		},
		Servers: []config.ServerConfig{
			{
				Listen: ":8080",
				Locations: []config.LocationConfig{
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/api"},
						Return: 200,
						WAF: &config.WAFConfig{
							Enabled: true,
							Mode:    "block",
							Exceptions: []config.WAFException{
								{
									ID:        "exc-loc",
									RuleID:    941100,
									Scope:     "narrow_target",
									Target:    "ARGS:comment",
									Rationale: "Route-level rationale",
									Owner:     "app-team",
								},
							},
						},
					},
				},
			},
		},
	}

	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:  func() (*config.Config, error) { return cfg, nil },
		WAFCompiled: true,
	})

	// 1. Principal with config:read (e.g. operator) receives full exception metadata.
	reqOperator := httptest.NewRequest(http.MethodGet, "/api/security", nil)
	reqOperator = reqOperator.WithContext(rbac.WithIdentity(reqOperator.Context(), rbac.Identity{
		Principal:   "op",
		Role:        rbac.RoleOperator,
		Permissions: []rbac.Permission{rbac.StatusRead, rbac.ConfigRead},
	}))
	recOperator := httptest.NewRecorder()
	s.handleSecurity(recOperator, reqOperator)
	if recOperator.Code != http.StatusOK {
		t.Fatalf("operator status = %d", recOperator.Code)
	}
	if !strings.Contains(recOperator.Body.String(), "Internal ticket rationale") {
		t.Fatalf("expected config:read caller to see exception rationale, got %s", recOperator.Body.String())
	}

	// 2. Principal with status:read ONLY (e.g. auditor) must NOT receive exception
	// rationale/owner/reference or rule file paths.
	reqAuditor := httptest.NewRequest(http.MethodGet, "/api/security", nil)
	reqAuditor = reqAuditor.WithContext(rbac.WithIdentity(reqAuditor.Context(), rbac.Identity{
		Principal:   "aud",
		Role:        rbac.RoleAuditor,
		Permissions: []rbac.Permission{rbac.StatusRead},
	}))
	recAuditor := httptest.NewRecorder()
	s.handleSecurity(recAuditor, reqAuditor)
	if recAuditor.Code != http.StatusOK {
		t.Fatalf("auditor status = %d", recAuditor.Code)
	}
	bodyAuditor := recAuditor.Body.String()
	for _, forbidden := range []string{"Internal ticket rationale", "secops-internal", "INC-9999", "Route-level rationale", "/etc/jul/secret-path.conf"} {
		if strings.Contains(bodyAuditor, forbidden) {
			t.Fatalf("status:read-only response leaked %q: %s", forbidden, bodyAuditor)
		}
	}
}

func TestGlobalWAFTOMLRoundTripAndDormantPolicyWithServerParser(t *testing.T) {
	// Item 7: Verify that the TOML emitted by generateWafToml (including escaped
	// SecLang backslashes @rx ^\\d+$, triple quotes, dormant disabled fields, and
	// [[waf.exceptions]]) parses cleanly through config.Parse + config.Validate
	// and round-trips through projectSecurity.
	dormantTOML := `[waf]
enabled = false
mode = "block"
crs_enabled = true
blocking_paranoia = 2
detection_paranoia = 3
directives_files = ["/etc/jul/waf/custom.conf"]
inline_rules = """
SecRule ARGS:id "@rx ^\\d+$" "id:1001,phase:2,deny,msg:'quote \"\"\" test'"
"""

[[waf.exceptions]]
id = "exc-1"
rule_id = 942100
scope = "narrow_target"
target = "ARGS:bio"

[[servers]]
listen = "127.0.0.1:8080"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200
`
	parsedDormant, err := config.Parse([]byte(dormantTOML))
	if err != nil {
		t.Fatalf("config.Parse dormantTOML failed: %v", err)
	}
	if err := config.Validate(parsedDormant); err != nil {
		t.Fatalf("config.Validate dormantTOML failed: %v", err)
	}
	if parsedDormant.WAF.Enabled || parsedDormant.WAF.BlockingParanoia != 2 || parsedDormant.WAF.DetectionParanoia != 3 || len(parsedDormant.WAF.Exceptions) != 1 {
		t.Fatalf("dormant WAF fields lost on parse: %+v", parsedDormant.WAF)
	}
	if !strings.Contains(parsedDormant.WAF.InlineRules, `@rx ^\d+$`) {
		t.Fatalf("escaped SecLang backslash did not unescape to @rx ^\\d+$: %q", parsedDormant.WAF.InlineRules)
	}
	proj := projectSecurity(parsedDormant, true)
	if proj.WAFBlockingParanoia != 2 || proj.WAFDetectionParanoia != 3 || len(proj.WAFExceptions) != 1 {
		t.Fatalf("projectSecurity did not preserve dormant WAF policy: %+v", proj)
	}

	// Re-enabling the dormant policy in TOML must pass config.Parse + config.Validate with all settings intact.
	reenabledTOML := strings.Replace(dormantTOML, "enabled = false", "enabled = true", 1)
	parsedReenabled, err := config.Parse([]byte(reenabledTOML))
	if err != nil {
		t.Fatalf("config.Parse reenabledTOML failed: %v", err)
	}
	if err := config.Validate(parsedReenabled); err != nil {
		t.Fatalf("config.Validate re-enabled WAF failed: %v", err)
	}
	if !parsedReenabled.WAF.Enabled || parsedReenabled.WAF.BlockingParanoia != 2 || parsedReenabled.WAF.DetectionParanoia != 3 || len(parsedReenabled.WAF.Exceptions) != 1 {
		t.Fatalf("re-enabled WAF lost dormant settings: %+v", parsedReenabled.WAF)
	}

	// P2: Semantic round-trip when root waf.* assignments preceded root admin.* and global.*
	// assignments: after upsertTopLevelTable places [waf] after the root preamble, config.Parse
	// + config.Validate must preserve admin.listen and global.log_level at the root scope.
	reorderedRootTOML := `admin.enabled = true
admin.listen = "127.0.0.1:9090"
global.log_level = "debug"

[waf]
enabled = true
mode = "detect"
crs_enabled = true
paranoia = 1

[[servers]]
listen = "127.0.0.1:8080"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200
`
	parsedRoot, err := config.Parse([]byte(reorderedRootTOML))
	if err != nil {
		t.Fatalf("config.Parse reorderedRootTOML failed: %v", err)
	}
	if err := config.Validate(parsedRoot); err != nil {
		t.Fatalf("config.Validate reorderedRootTOML failed: %v", err)
	}
	if !parsedRoot.Admin.Enabled || parsedRoot.Admin.Listen != "127.0.0.1:9090" || parsedRoot.Global.LogLevel != "debug" || !parsedRoot.WAF.Enabled {
		t.Fatalf("root admin/global settings were not preserved alongside [waf]: admin=%+v global=%+v waf=%+v",
			parsedRoot.Admin, parsedRoot.Global, parsedRoot.WAF)
	}
}
