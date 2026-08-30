// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"testing"
)

func TestAssessmentV2HelperContracts(t *testing.T) {
	if got := normalizedAssessmentOptions(AssessmentOptions{PathStyle: AssessmentPathStyle("invalid")}); got.PathStyle != AssessmentPathRelative {
		t.Fatalf("invalid path style normalized to %q, want %q", got.PathStyle, AssessmentPathRelative)
	}
	if got := normalizedAssessmentOptions(AssessmentOptions{PathStyle: AssessmentPathAbsolute}); got.PathStyle != AssessmentPathAbsolute {
		t.Fatalf("absolute path style normalized to %q", got.PathStyle)
	}

	var nilMatcher *sourceIndexMatcher
	if _, ok := nilMatcher.match("http", 1); ok {
		t.Fatal("nil matcher unexpectedly matched")
	}
	if _, ok := (&sourceIndexMatcher{}).match("http", 1); ok {
		t.Fatal("empty matcher unexpectedly matched")
	}

	matcher := &sourceIndexMatcher{items: []indexedDirective{
		{Name: "events", Start: AssessmentPosition{Line: 1, Column: 1}},
		{Name: "http", Start: AssessmentPosition{Line: 2, Column: 1}},
		{Name: "server", Start: AssessmentPosition{Line: 3, Column: 3}},
	}}
	item, ok := matcher.match("http", 2)
	if !ok || item.Name != "http" || matcher.next != 2 {
		t.Fatalf("search match = %+v, %v, next=%d", item, ok, matcher.next)
	}
	if _, ok := matcher.match("server", 99); ok {
		t.Fatal("line-mismatched direct item unexpectedly matched")
	}
	if _, ok := matcher.match("missing", 0); ok {
		t.Fatal("missing directive unexpectedly matched")
	}

	applyDirectiveDecoration(nil, directiveDecoration{}, AssessmentSource{})
	fallback := fallbackProvenance(AssessmentResult{Directive: "worker_processes", Line: 7}, AssessmentSource{ID: "source-1", DisplayPath: "nginx.conf"})
	if fallback.ContextPath != "main" || fallback.Start.Line != 7 || fallback.SourceID != "source-1" {
		t.Fatalf("unexpected fallback provenance: %+v", fallback)
	}

	if values := safeContextValues([]string{"a"}, 0); values != nil {
		t.Fatalf("zero-limit context values = %#v, want nil", values)
	}
	values := safeContextValues([]string{" a ", "", "b", "c"}, 2)
	if len(values) != 1 || values[0] != "a" {
		t.Fatalf("bounded context values = %#v", values)
	}

	if got := uniqueSortedStrings(nil); got != nil {
		t.Fatalf("nil unique strings = %#v", got)
	}
	got := uniqueSortedStrings([]string{" z ", "", "a", "z", "a"})
	if strings.Join(got, ",") != "a,z" {
		t.Fatalf("unique sorted strings = %#v", got)
	}
}

func TestAssessmentV2TargetMappingRelationsAndInference(t *testing.T) {
	tests := []struct {
		name     string
		result   AssessmentResult
		relation string
		paths    string
	}{
		{name: "direct", result: AssessmentResult{TargetPaths: []string{"servers[].listen"}}, relation: "direct", paths: "servers[].listen"},
		{name: "realip combines", result: AssessmentResult{Code: "NGX_REALIP_SET_FROM", TargetPaths: []string{"servers[].client_address.trusted_proxies"}}, relation: "combines_with_siblings", paths: "servers[].client_address.trusted_proxies"},
		{name: "expands", result: AssessmentResult{TargetPaths: []string{"b", "a", "a"}}, relation: "expands_to_multiple", paths: "a,b"},
		{name: "approximate", result: AssessmentResult{Class: AssessmentApproximated, TargetPaths: []string{"servers[].locations[].root"}}, relation: "approximate", paths: "servers[].locations[].root"},
		{name: "inferred", result: AssessmentResult{Code: "NGX_LOCATION_PROXY_PASS_URI"}, relation: "expands_to_multiple", paths: "servers[].locations[].proxy_pass,servers[].locations[].rewrites[]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mappings := targetMappingsForResult(tc.result)
			if len(mappings) != 1 || mappings[0].Relation != tc.relation || strings.Join(mappings[0].Paths, ",") != tc.paths {
				t.Fatalf("mappings = %+v", mappings)
			}
		})
	}
	if mappings := targetMappingsForResult(AssessmentResult{Code: "NGX_UNKNOWN"}); mappings != nil {
		t.Fatalf("unknown mapping = %+v, want nil", mappings)
	}

	inferred := map[string]string{
		"NGX_SERVER_EXTRA_LISTEN":   "servers[].listen,servers[].tls.enabled",
		"NGX_SERVER_LISTEN_OPTION":  "servers[].listen,servers[].tls.enabled",
		"NGX_SERVER_RETURN":         "servers[].locations[]",
		"NGX_LOCATION_ALIAS":        "servers[].locations[].root",
		"NGX_LOCATION_MATCH":        "servers[].locations[].match",
		"NGX_LOCATION_RETURN_BODY":  "servers[].locations[].return",
		"NGX_LOCATION_REWRITE_FLAG": "servers[].locations[].rewrites[]",
		"NGX_LOCATION_LIMIT_EXCEPT": "servers[].locations[].match.methods",
		"NGX_UPSTREAM_IP_HASH":      "upstreams[].strategy",
		"NGX_UPSTREAM_HASH":         "upstreams[].strategy",
		"NGX_UPSTREAM_RANDOM":       "upstreams[].strategy",
		"NGX_UPSTREAM_SERVER_DOWN":  "upstreams[].servers[]",
	}
	for code, want := range inferred {
		if got := strings.Join(inferredTargetPaths(AssessmentResult{Code: code}), ","); got != want {
			t.Errorf("%s inferred paths = %q, want %q", code, got, want)
		}
	}
}

func TestAssessmentV2GuidanceSelection(t *testing.T) {
	tests := []struct {
		name   string
		result AssessmentResult
		want   string
	}{
		{name: "candidate", result: AssessmentResult{Code: "JUL_CANDIDATE_VALIDATION"}, want: "GUIDE_CANDIDATE_VALIDATION"},
		{name: "include directive", result: AssessmentResult{Directive: "include"}, want: "GUIDE_INCLUDE_ENABLE"},
		{name: "include code", result: AssessmentResult{Code: "NGX_MAIN_INCLUDE"}, want: "GUIDE_INCLUDE_ENABLE"},
		{name: "non actionable", result: AssessmentResult{Class: AssessmentSupported}, want: ""},
		{name: "realip header", result: AssessmentResult{Class: AssessmentBlocking, Code: "NGX_REALIP_HEADER"}, want: "GUIDE_REALIP_HEADER"},
		{name: "realip conflict", result: AssessmentResult{Class: AssessmentBlocking, Code: "NGX_REAL_IP_CONFLICT"}, want: "GUIDE_REALIP_LISTENER"},
		{name: "conditional code", result: AssessmentResult{Class: AssessmentBlocking, Code: "NGX_LOCATION_IF"}, want: "GUIDE_CONDITIONAL_MANUAL"},
		{name: "conditional context", result: AssessmentResult{Class: AssessmentBlocking, Context: ContextVariable}, want: "GUIDE_CONDITIONAL_MANUAL"},
		{name: "conditional directive", result: AssessmentResult{Class: AssessmentBlocking, Directive: "split_clients"}, want: "GUIDE_CONDITIONAL_MANUAL"},
		{name: "dynamic proxy", result: AssessmentResult{Class: AssessmentBlocking, Code: "NGX_PROXY_PASS_DYNAMIC"}, want: "GUIDE_PROXY_TARGET_MANUAL"},
		{name: "header", result: AssessmentResult{Class: AssessmentApproximated, Code: "NGX_RESPONSE_HEADER"}, want: "GUIDE_HEADER_POLICY_MANUAL"},
		{name: "cors", result: AssessmentResult{Class: AssessmentBlocking, Code: "NGX_CORS_PARTIAL"}, want: "GUIDE_HEADER_POLICY_MANUAL"},
		{name: "auth", result: AssessmentResult{Class: AssessmentBlocking, Code: "NGX_AUTH_BASIC"}, want: "GUIDE_AUTH_MANUAL"},
		{name: "deny", result: AssessmentResult{Class: AssessmentBlocking, Directive: "deny"}, want: "GUIDE_AUTH_MANUAL"},
		{name: "limits", result: AssessmentResult{Class: AssessmentBlocking, Code: "NGX_LIMIT_REQ"}, want: "GUIDE_LIMITS_MANUAL"},
		{name: "body limit", result: AssessmentResult{Class: AssessmentBlocking, Directive: "client_max_body_size"}, want: "GUIDE_LIMITS_MANUAL"},
		{name: "location", result: AssessmentResult{Class: AssessmentApproximated, Code: "NGX_LOCATION_MATCH"}, want: "GUIDE_LOCATION_REVIEW"},
		{name: "rewrite", result: AssessmentResult{Class: AssessmentBlocking, Code: "NGX_REWRITE_FLAG"}, want: "GUIDE_LOCATION_REVIEW"},
		{name: "fallback", result: AssessmentResult{Class: AssessmentBlocking, Code: "NGX_UNKNOWN"}, want: "GUIDE_MANUAL_REVIEW"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			codes := guidanceCodesForResult(tc.result)
			if tc.want == "" {
				if codes != nil {
					t.Fatalf("guidance = %#v, want nil", codes)
				}
				return
			}
			if len(codes) != 1 || codes[0] != tc.want {
				t.Fatalf("guidance = %#v, want %q", codes, tc.want)
			}
		})
	}
}

func TestAssessmentV2SyntheticRelationshipsAndGuidanceRefresh(t *testing.T) {
	provenance := &AssessmentProvenance{
		SourceID:    "source-0001",
		DisplayPath: "nginx.conf",
		Start:       AssessmentPosition{Line: 20, Column: 3},
		ContextPath: "http > server",
		Directive:   "proxy_pass",
		Summary:     "proxy_pass <target>",
	}
	assessment := &Assessment{Results: []AssessmentResult{
		{ID: "result-source-0001-0001", Code: "NGX_REALIP_SET_FROM", Directive: "set_real_ip_from", Line: 10, Class: AssessmentSupported},
		{ID: "result-source-0001-0002", Code: "NGX_LOCATION_PROXY_PASS", Directive: "proxy_pass", Line: 20, Class: AssessmentSupported, Provenance: provenance},
		{Code: "NGX_REALIP_CONFLICT", Class: AssessmentBlocking, Synthetic: true},
		{Code: "JUL_CANDIDATE_VALIDATION", Class: AssessmentValidationError, Directive: "proxy_pass", Line: 20, Synthetic: true},
	}}
	attachSyntheticRelationships(assessment)

	realip := assessment.Results[2]
	if realip.Scope != "derived" || !containsString(realip.RelatedResultIDs, "result-source-0001-0001") || !containsString(realip.GuidanceCodes, "GUIDE_REALIP_LISTENER") {
		t.Fatalf("realip synthetic relationship = %+v", realip)
	}
	validation := assessment.Results[3]
	if validation.Scope != "derived" || validation.Provenance == nil || validation.Provenance == provenance || !containsString(validation.RelatedResultIDs, "result-source-0001-0002") {
		t.Fatalf("validation synthetic relationship = %+v", validation)
	}

	assessment.Results = append(assessment.Results,
		AssessmentResult{GuidanceCodes: []string{"GUIDE_MANUAL_REVIEW", "UNKNOWN_GUIDANCE"}},
		AssessmentResult{GuidanceCodes: []string{"GUIDE_AUTH_MANUAL", "GUIDE_MANUAL_REVIEW"}},
	)
	assessment.refreshGuidance()
	if len(assessment.Guidance) != 4 {
		t.Fatalf("guidance catalogue = %+v", assessment.Guidance)
	}
	for i := 1; i < len(assessment.Guidance); i++ {
		if assessment.Guidance[i-1].Code > assessment.Guidance[i].Code {
			t.Fatalf("guidance catalogue is not sorted: %+v", assessment.Guidance)
		}
	}
}

func TestAssessmentV2ResultUtilitiesAndFinalization(t *testing.T) {
	var nilAssessment *Assessment
	nilAssessment.SetValidation(nil, nil)
	nilAssessment.SetSourceOrder(true)
	if _, err := nilAssessment.JSON(); err == nil {
		t.Fatal("nil assessment JSON unexpectedly succeeded")
	}
	if nilAssessment.Human() != "" || nilAssessment.HasWarnings() || nilAssessment.HasBlocking() {
		t.Fatal("nil assessment utilities returned non-zero values")
	}

	if got := resultLocation(AssessmentResult{}); got != "global" {
		t.Fatalf("global location = %q", got)
	}
	if got := resultLocation(AssessmentResult{Line: 9}); got != "line 9" {
		t.Fatalf("line location = %q", got)
	}
	if got := resultLocation(AssessmentResult{Provenance: &AssessmentProvenance{DisplayPath: "nginx.conf", Start: AssessmentPosition{Line: 4, Column: 2}}}); got != "nginx.conf:4:2" {
		t.Fatalf("provenance location = %q", got)
	}
	if got := strings.Join(resultMappedPaths(AssessmentResult{TargetMappings: []AssessmentTargetMapping{{Paths: []string{"b", "a"}}}}), ","); got != "a,b" {
		t.Fatalf("mapped paths = %q", got)
	}
	if got := strings.Join(resultMappedPaths(AssessmentResult{TargetPaths: []string{"z"}}), ","); got != "z" {
		t.Fatalf("legacy mapped paths = %q", got)
	}

	ids := &Assessment{Results: []AssessmentResult{
		{Synthetic: true},
		{},
		{ID: "duplicate"},
		{ID: "duplicate"},
	}}
	ids.assignMissingResultIDs()
	if ids.Results[0].ID != "result-synthetic-0001" || ids.Results[1].ID != "result-unmatched-0001" || ids.Results[3].ID != "duplicate-02" {
		t.Fatalf("assigned IDs = %+v", ids.Results)
	}

	statuses := []struct {
		class AssessmentClass
		want  string
	}{
		{AssessmentParseError, "parse_error"},
		{AssessmentValidationError, "invalid_candidate"},
		{AssessmentBlocking, "manual_action_required"},
		{AssessmentSupported, "ready_for_review"},
	}
	for _, tc := range statuses {
		a := &Assessment{Results: []AssessmentResult{{Class: tc.class}}}
		a.finalize()
		if a.Status != tc.want {
			t.Errorf("class %q status = %q, want %q", tc.class, a.Status, tc.want)
		}
	}

	ready := &Assessment{Results: []AssessmentResult{
		{Class: AssessmentSupported},
		{Class: AssessmentApproximated},
		{Class: AssessmentIgnored},
		{Class: AssessmentInformational},
	}}
	ready.finalize()
	if !ready.Summary.Ready || ready.Summary.Total != 4 || ready.Summary.Supported != 1 || ready.Summary.Approximated != 1 || ready.Summary.Ignored != 1 || ready.Summary.Informational != 1 {
		t.Fatalf("ready summary = %+v", ready.Summary)
	}
	if !ready.HasWarnings() || ready.HasBlocking() {
		t.Fatalf("unexpected warning/blocking flags: %+v", ready.Summary)
	}
}

func TestAssessmentV2ClassificationUtilities(t *testing.T) {
	priorities := map[AssessmentClass]int{
		AssessmentParseError:      0,
		AssessmentValidationError: 0,
		AssessmentBlocking:        0,
		AssessmentApproximated:    1,
		AssessmentIgnored:         2,
		AssessmentSupported:       3,
		AssessmentInformational:   4,
	}
	for class, want := range priorities {
		if got := resultPriority(class); got != want {
			t.Errorf("priority(%q) = %d, want %d", class, got, want)
		}
	}

	risks := map[string]AssessmentRisk{
		"auth_basic":    RiskSecurity,
		"ssl_protocols": RiskSecurity,
		"access_log":    RiskObservability,
		"proxy_cache":   RiskPerformance,
		"proxy_buffer":  RiskPerformance,
		"proxy_pass":    RiskRouting,
	}
	for name, want := range risks {
		if got := defaultRisk(name); got != want {
			t.Errorf("defaultRisk(%q) = %q, want %q", name, got, want)
		}
	}
	if got := assessmentText("  servers[0].listen\r\nforged  "); got != "servers[0].listen  forged" {
		t.Fatalf("assessmentText = %q", got)
	}
}
