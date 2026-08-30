// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import "sort"

// guidanceCatalog is closed and reviewable. Results reference these stable
// codes instead of duplicating long remediation prose in classifiers.
var guidanceCatalog = map[string]AssessmentGuidance{
	"GUIDE_INCLUDE_ENABLE": {
		Code:     "GUIDE_INCLUDE_ENABLE",
		Title:    "Assess included files explicitly",
		Action:   "Run the importer with bounded include traversal and an explicit assessment root, then review every included source result.",
		Docs:     "nginx-importer#include-security",
		Blocking: true,
	},
	"GUIDE_INCLUDE_RESOLVE": {
		Code:        "GUIDE_INCLUDE_RESOLVE",
		Title:       "Resolve an include failure",
		Action:      "Correct the include path or permissions inside the configured root and rerun the assessment.",
		Consequence: "The generated candidate is incomplete while an included source cannot be read and classified.",
		Docs:        "nginx-importer#include-security",
		Blocking:    true,
	},
	"GUIDE_CONDITIONAL_MANUAL": {
		Code:        "GUIDE_CONDITIONAL_MANUAL",
		Title:       "Rewrite conditional routing manually",
		Action:      "Express the intended bounded method, header, query or route behavior directly in Jul; do not copy an arbitrary NGINX if/map expression as a guessed static branch.",
		Consequence: "Guessing a conditional can route, authorize or redirect requests differently from NGINX.",
		Docs:        "nginx-importer#conditional-and-variable-driven-configuration",
		Blocking:    true,
	},
	"GUIDE_PROXY_TARGET_MANUAL": {
		Code:        "GUIDE_PROXY_TARGET_MANUAL",
		Title:       "Replace a dynamic proxy target",
		Action:      "Create explicit Jul routes and upstreams for the finite destinations, or retain the source deployment until the dynamic behavior is redesigned.",
		Consequence: "A variable-derived destination cannot be represented safely as one static proxy target.",
		Docs:        "nginx-importer#proxy-pass",
		Blocking:    true,
	},
	"GUIDE_HEADER_POLICY_MANUAL": {
		Code:        "GUIDE_HEADER_POLICY_MANUAL",
		Title:       "Port header policy explicitly",
		Action:      "Configure a bounded static Jul request/response header policy and preserve any authentication or cookie semantics explicitly.",
		Consequence: "Dropping or widening a security-sensitive header policy may change backend authentication or browser behavior.",
		Docs:        "nginx-importer#headers-and-cors",
		Blocking:    true,
	},
	"GUIDE_AUTH_MANUAL": {
		Code:        "GUIDE_AUTH_MANUAL",
		Title:       "Recreate the authentication boundary",
		Action:      "Configure a supported Jul authentication mechanism and test denial as well as success paths before cutover.",
		Consequence: "The generated candidate does not preserve the NGINX authentication boundary.",
		Docs:        "nginx-importer#security-controls",
		Blocking:    true,
	},
	"GUIDE_LIMITS_MANUAL": {
		Code:        "GUIDE_LIMITS_MANUAL",
		Title:       "Recreate request and connection limits",
		Action:      "Configure the corresponding Jul body, rate, concurrency or connection limits and validate overload behavior.",
		Consequence: "Missing limits can weaken abuse protection or change availability under load.",
		Docs:        "nginx-importer#limits",
		Blocking:    true,
	},
	"GUIDE_LOCATION_REVIEW": {
		Code:        "GUIDE_LOCATION_REVIEW",
		Title:       "Review location and path semantics",
		Action:      "Compare location precedence and backend-received paths, then adjust Jul match and rewrite configuration explicitly.",
		Consequence: "Alias, regex case sensitivity, prefix precedence and proxy_pass URI rules can select a different route or path.",
		Docs:        "nginx-importer#location-and-proxy-path-semantics",
		Blocking:    false,
	},
	"GUIDE_REALIP_HEADER": {
		Code:        "GUIDE_REALIP_HEADER",
		Title:       "Declare the trusted forwarded header",
		Action:      "Set real_ip_header to Forwarded or X-Forwarded-For and verify that every trusted proxy overwrites it.",
		Consequence: "Inventing a different header would change the client-identity trust boundary.",
		Docs:        "nginx-importer#realip-set_real_ip_from--real_ip_header",
		Blocking:    true,
	},
	"GUIDE_REALIP_LISTENER": {
		Code:        "GUIDE_REALIP_LISTENER",
		Title:       "Reconcile listener-scoped trusted-proxy policy",
		Action:      "Make every virtual host sharing a listen address use one identical policy, or split them onto different listeners.",
		Consequence: "Jul derives client identity before host routing and cannot safely choose between conflicting policies.",
		Docs:        "nginx-importer#realip-set_real_ip_from--real_ip_header",
		Blocking:    true,
	},
	"GUIDE_CANDIDATE_VALIDATION": {
		Code:        "GUIDE_CANDIDATE_VALIDATION",
		Title:       "Correct the generated Jul candidate",
		Action:      "Resolve the reported canonical Jul field errors and rerun the assessment; invalid output is never written automatically.",
		Consequence: "The current generated candidate cannot be loaded by Jul.",
		Docs:        "nginx-importer#candidate-validation",
		Blocking:    true,
	},
	"GUIDE_MANUAL_REVIEW": {
		Code:        "GUIDE_MANUAL_REVIEW",
		Title:       "Review this semantic difference manually",
		Action:      "Inspect the source location, compare the intended NGINX behavior with the generated Jul target, and configure or test the missing behavior before cutover.",
		Consequence: "The importer cannot prove that this source construct is represented with equivalent behavior.",
		Docs:        "nginx-assessment#guidance-and-manual-action",
		Blocking:    true,
	},
}

func lookupAssessmentGuidance(code string) (AssessmentGuidance, bool) {
	guidance, ok := guidanceCatalog[code]
	return guidance, ok
}

func allAssessmentGuidance() []AssessmentGuidance {
	codes := make([]string, 0, len(guidanceCatalog))
	for code := range guidanceCatalog {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	out := make([]AssessmentGuidance, 0, len(codes))
	for _, code := range codes {
		out = append(out, guidanceCatalog[code])
	}
	return out
}
