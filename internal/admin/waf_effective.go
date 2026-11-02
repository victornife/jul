// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// WAFEffectivePolicy is the WAF policy the serving handler generation
// actually enforces (#440), built from the compiled engines when that
// generation was prepared — never reconstructed from the on-disk config, so
// a staged, failed or not-yet-reloaded candidate cannot appear active. It
// carries no rule file path, rule text or rule message.
type WAFEffectivePolicy struct {
	// Compiled reports whether this binary links the WAF engine at all.
	Compiled bool `json:"compiled"`
	// EmbeddedCRSVersion is the OWASP CRS version compiled into the binary,
	// read from the embedded rule set; "" without the waf build tag.
	EmbeddedCRSVersion string `json:"embedded_crs_version,omitempty"`
	// EngineVersion is the linked Coraza version from build info, when known.
	EngineVersion string `json:"engine_version,omitempty"`
	// Generation is the serving handler generation; CompiledAt is when its
	// policies were compiled (RFC 3339, UTC).
	Generation uint64 `json:"generation"`
	CompiledAt string `json:"compiled_at,omitempty"`
	// Route coverage of the serving generation.
	InheritingRoutes       int `json:"inheriting_routes"`
	OverrideRoutes         int `json:"override_routes"`
	DisabledOverrideRoutes int `json:"disabled_override_routes"`
	UnprotectedRoutes      int `json:"unprotected_routes"`
	BlockRoutes            int `json:"block_routes"`
	DetectRoutes           int `json:"detect_routes"`
	// GlobalEnabled is the serving generation's [waf].enabled. Global is the
	// compiled global policy, present only when at least one route serves it.
	GlobalEnabled bool                      `json:"global_enabled"`
	Global        *WAFPolicySummary         `json:"global,omitempty"`
	Evaluation    *WAFEvaluationSummaryView `json:"evaluation,omitempty"`
	// Overrides lists routes whose own [waf] block replaces the global policy.
	Overrides []WAFRouteOverride `json:"overrides,omitempty"`
	// RouteProtection maps canonical route identity -> protection mode
	// ("block", "detect", "unprotected") for per-route transition classification.
	RouteProtection map[string]string `json:"-"`
}

// WAFEvaluationSummaryView is the status-safe projection of the latest WAF
// fixture evaluation bound to the serving or candidate policy (#630).
type WAFEvaluationSummaryView struct {
	State                string   `json:"state"`
	Source               string   `json:"source,omitempty"`
	EvaluatedAt          string   `json:"evaluated_at,omitempty"`
	PackID               string   `json:"pack_id,omitempty"`
	Scope                string   `json:"scope,omitempty"`
	PolicyDigest         string   `json:"policy_digest,omitempty"`
	ExternalDigest       string   `json:"external_digest,omitempty"`
	EngineVersion        string   `json:"engine_version,omitempty"`
	EmbeddedCRSVersion   string   `json:"embedded_crs_version,omitempty"`
	AnalysisCompleteness string   `json:"analysis_completeness,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
	TotalCases           int      `json:"total_cases"`
	PassedCases          int      `json:"passed_cases"`
	FailedCases          int      `json:"failed_cases"`
	LegitimateTotal      int      `json:"legitimate_total"`
	LegitimateBlocked    int      `json:"legitimate_blocked"`
	MaliciousTotal       int      `json:"malicious_total"`
	MaliciousAllowed     int      `json:"malicious_allowed"`
	ChangedFromBaseline  int      `json:"changed_from_baseline"`
	CompileDurationMs    float64  `json:"compile_duration_ms"`
	EvalDurationMs       float64  `json:"eval_duration_ms"`
}

// WAFPolicySummary is one compiled policy's bounded effective settings.
type WAFPolicySummary struct {
	Mode                      string `json:"mode"`
	BlockStatus               int    `json:"block_status"`
	CRSEnabled                bool   `json:"crs_enabled"`
	CRSVersion                string `json:"crs_version,omitempty"`
	Paranoia                  int    `json:"paranoia,omitempty"`
	ParanoiaDefault           bool   `json:"paranoia_default,omitempty"`
	BlockingParanoia          int    `json:"blocking_paranoia,omitempty"`
	DetectionParanoia         int    `json:"detection_paranoia,omitempty"`
	ExceptionsCount           int    `json:"exceptions_count,omitempty"`
	ExceptionsReviewDue       int    `json:"exceptions_review_due,omitempty"`
	HasCustomTuningDirectives bool   `json:"has_custom_tuning_directives,omitempty"`
	RequestBodyLimitBytes     int64  `json:"request_body_limit_bytes,omitempty"`
	ResponseBodyInspection    bool   `json:"response_body_inspection"`
	RuleFilesConfigured       int    `json:"rule_files_configured"`
	// ExternalFiles/ExternalDigest identify the exact bytes read from disk at
	// compile time under the server's private version context ("ev1:<ctx>:<tag>",
	// #649): same path with changed content yields a new digest without exposing
	// an unkeyed offline dictionary verifier.
	ExternalFiles  int           `json:"external_files"`
	ExternalDigest string        `json:"external_digest,omitempty"`
	PolicyDigest   string        `json:"policy_digest,omitempty"`
	InlineRules    bool          `json:"inline_rules"`
	Rules          WAFRuleCounts `json:"rules"`
}

// WAFRuleCounts classifies compiled rules by bounded source class.
type WAFRuleCounts struct {
	Total     int `json:"total"`
	Embedded  int `json:"embedded"`
	External  int `json:"external"`
	Inline    int `json:"inline"`
	Generated int `json:"generated"`
}

// WAFRouteOverride identifies a route by its configured listener, server
// names and match — the same identity the Security panel already shows —
// and its compiled override policy (nil when the override disables the WAF).
type WAFRouteOverride struct {
	RouteID      string                    `json:"route_id,omitempty"`
	Listen       string                    `json:"listen"`
	ServerNames  []string                  `json:"server_names,omitempty"`
	MatchType    string                    `json:"match_type,omitempty"`
	Path         string                    `json:"path,omitempty"`
	MatchOrdinal int                       `json:"match_ordinal"`
	Enabled      bool                      `json:"enabled"`
	Policy       *WAFPolicySummary         `json:"policy,omitempty"`
	Evaluation   *WAFEvaluationSummaryView `json:"evaluation,omitempty"`
}

func (s *Server) handleWAFEvaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.EvaluateWAF == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "waf evaluation is not available"})
		return
	}
	body, err := readBoundedBody(r, 256<<10)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if qScope := strings.TrimSpace(r.URL.Query().Get("scope")); qScope != "" {
		if len(body) == 0 || string(body) == "{}" {
			body, _ = json.Marshal(map[string]string{"scope": qScope})
		} else {
			var obj map[string]json.RawMessage
			if json.Unmarshal(body, &obj) == nil && obj != nil && obj["scope"] == nil {
				rawQ, _ := json.Marshal(qScope)
				obj["scope"] = rawQ
				if merged, mErr := json.Marshal(obj); mErr == nil {
					body = merged
				}
			}
		}
	}
	summary, err := s.deps.EvaluateWAF(r.Context(), body)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrWAFEvaluationUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// ErrWAFEvaluationUnavailable indicates that WAF evaluation is unavailable in
// a binary built without the waf tag or when no WAF policy is enabled.
var ErrWAFEvaluationUnavailable = errors.New("waf evaluation is unavailable (requires -tags waf and an enabled WAF policy)")
