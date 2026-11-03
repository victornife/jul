// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package waf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

// Schema versions and bounds for synthetic WAF fixture packs and evaluation
// reports (#630, ADR 0031 §3.2).
const (
	FixturePackSchemaVersion      = 1
	EvaluationReportSchemaVersion = 1
	MaxFixturesPerPack            = 64
)

var validFixtureCategories = []string{
	"legitimate",
	"malicious",
	"boundary",
}

var validExpectedOutcomes = []string{
	OutcomeClean,
	OutcomeMatchedObserved,
	OutcomeInterruptedRequest,
	OutcomeInterruptedResponse,
	OutcomeRejectedBodyLimit,
	OutcomeInspectionError,
}

// WAFFixturePack is a versioned collection of synthetic legitimate, malicious,
// and boundary HTTP test cases for reproducible WAF candidate evaluation (#630).
type WAFFixturePack struct {
	SchemaVersion int              `json:"schema_version"`
	PackID        string           `json:"pack_id"`
	Description   string           `json:"description,omitempty"`
	Fixtures      []WAFFixtureCase `json:"fixtures"`
}

// WAFFixtureCase is one synthetic HTTP request/response test case in a
// WAFFixturePack (#630). It carries explicit expectations and zero production
// secrets.
type WAFFixtureCase struct {
	ID                    string            `json:"id"`
	Category              string            `json:"category"` // "legitimate", "malicious", or "boundary"
	Description           string            `json:"description"`
	Method                string            `json:"method"`
	Host                  string            `json:"host,omitempty"`
	Path                  string            `json:"path"`
	Headers               map[string]string `json:"headers,omitempty"`
	Body                  string            `json:"body,omitempty"`
	OriginStatus          int               `json:"origin_status,omitempty"`
	OriginHeaders         map[string]string `json:"origin_headers,omitempty"`
	OriginBody            string            `json:"origin_body,omitempty"`
	ExpectedOutcome       string            `json:"expected_outcome"`
	ExpectedStatus        int               `json:"expected_status"`
	ExpectedOriginReached bool              `json:"expected_origin_reached"`
	ExpectedRuleIDs       []int             `json:"expected_rule_ids,omitempty"`
}

// WAFFixtureCaseResult records the observed candidate (and optional baseline)
// behavior for one WAFFixtureCase (#630).
type WAFFixtureCaseResult struct {
	ID                     string `json:"id"`
	Category               string `json:"category"`
	PassedExpectation      bool   `json:"passed_expectation"`
	ExpectedOutcome        string `json:"expected_outcome"`
	ExpectedStatus         int    `json:"expected_status"`
	CandidateOutcome       string `json:"candidate_outcome"`
	CandidateStatus        int    `json:"candidate_status"`
	CandidateOriginReached bool   `json:"candidate_origin_reached"`
	CandidateMatchedRules  []int  `json:"candidate_matched_rules,omitempty"`
	BaselineOutcome        string `json:"baseline_outcome,omitempty"`
	BaselineStatus         int    `json:"baseline_status,omitempty"`
	BehaviorChanged        bool   `json:"behavior_changed,omitempty"`
	RegressionKind         string `json:"regression_kind,omitempty"` // "legitimate_blocked", "malicious_allowed", "expectation_mismatch"
}

// WAFEvaluationSummary is the bounded, status-safe summary of a candidate or
// serving WAF policy evaluation (#630). It carries context-keyed policy
// digests and never exposes raw file paths or unkeyed file digests (#649).
type WAFEvaluationSummary struct {
	State                string   `json:"state"`                  // "current", "stale_asset_changed", "stale_policy_changed", "stale_engine_or_crs_changed", "unrun", "unavailable"
	Source               string   `json:"source,omitempty"`       // "server_observed" or "supplied_report"
	EvaluatedAt          string   `json:"evaluated_at,omitempty"` // RFC 3339 UTC
	PackID               string   `json:"pack_id,omitempty"`
	Scope                string   `json:"scope,omitempty"`
	PolicyDigest         string   `json:"policy_digest,omitempty"`
	ExternalDigest       string   `json:"external_digest,omitempty"`
	EngineVersion        string   `json:"engine_version,omitempty"`
	EmbeddedCRSVersion   string   `json:"embedded_crs_version,omitempty"`
	AnalysisCompleteness string   `json:"analysis_completeness,omitempty"` // "complete" or "partial_unmodelled_custom"
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

// WAFEvaluationReport is the full machine-readable output of `jul waf evaluate`
// and `POST /api/v1/waf/evaluate` (#630).
type WAFEvaluationReport struct {
	SchemaVersion     int                    `json:"schema_version"`
	Summary           WAFEvaluationSummary   `json:"summary"`
	RawPolicySHA256   string                 `json:"raw_policy_sha256,omitempty"`
	RawExternalSHA256 string                 `json:"raw_external_sha256,omitempty"`
	Cases             []WAFFixtureCaseResult `json:"cases"`
}

// DefaultFixturePack returns the built-in versioned synthetic regression pack
// covering normal forms, JSON, multipart uploads, encoding/parser boundaries, and
// malicious CRS controls (#630).
func DefaultFixturePack() *WAFFixturePack {
	multipartBoundary := "----JulWAFFixtureBoundary7MA4YWxkTrZu0gW"
	multipartBody := strings.Join([]string{
		"--" + multipartBoundary,
		`Content-Disposition: form-data; name="description"`,
		"",
		"Quarterly project notes upload",
		"--" + multipartBoundary,
		`Content-Disposition: form-data; name="file"; filename="notes.txt"`,
		"Content-Type: text/plain",
		"",
		"Synthetic plain-text attachment content for WAF regression verification.",
		"--" + multipartBoundary + "--",
		"",
	}, "\r\n")

	return &WAFFixturePack{
		SchemaVersion: FixturePackSchemaVersion,
		PackID:        "jul-waf-default-v1",
		Description:   "Synthetic legitimate requests, encoding/parser boundaries, and malicious attack controls for OWASP CRS v4.25.0 and Coraza v3.8.1",
		Fixtures: []WAFFixtureCase{
			{
				ID:                    "legit-get-catalog",
				Category:              "legitimate",
				Description:           "Standard GET catalog query with pagination and sorting parameters",
				Method:                http.MethodGet,
				Host:                  "portal.example.test",
				Path:                  "/products?page=2&sort=name_asc",
				Headers:               map[string]string{"User-Agent": "Mozilla/5.0 (X11; Linux x86_64) JulFixture/1.0", "Accept": "text/html,application/xhtml+xml"},
				ExpectedOutcome:       OutcomeClean,
				ExpectedStatus:        http.StatusOK,
				ExpectedOriginReached: true,
			},
			{
				ID:                    "legit-form-contact",
				Category:              "legitimate",
				Description:           "URL-encoded POST contact form with normal prose",
				Method:                http.MethodPost,
				Host:                  "portal.example.test",
				Path:                  "/contact",
				Headers:               map[string]string{"Content-Type": "application/x-www-form-urlencoded", "User-Agent": "Mozilla/5.0 JulFixture/1.0"},
				Body:                  "name=Alice+Smith&subject=Service+inquiry&message=Hello+team%2C+we+would+like+to+schedule+a+review.",
				ExpectedOutcome:       OutcomeClean,
				ExpectedStatus:        http.StatusOK,
				ExpectedOriginReached: true,
			},
			{
				ID:                    "legit-json-api",
				Category:              "legitimate",
				Description:           "Structured JSON POST payload with nested array and numbers",
				Method:                http.MethodPost,
				Host:                  "app.example.test",
				Path:                  "/api/items",
				Headers:               map[string]string{"Content-Type": "application/json", "Accept": "application/json", "User-Agent": "Mozilla/5.0 JulFixture/1.0"},
				Body:                  `{"title":"Quarterly Report","tags":["finance","operations"],"count":12,"published":true}`,
				ExpectedOutcome:       OutcomeClean,
				ExpectedStatus:        http.StatusOK,
				ExpectedOriginReached: true,
			},
			{
				ID:                    "legit-multipart-upload",
				Category:              "legitimate",
				Description:           "Multipart form-data file upload with text attachment and metadata field",
				Method:                http.MethodPost,
				Host:                  "app.example.test",
				Path:                  "/api/upload",
				Headers:               map[string]string{"Content-Type": "multipart/form-data; boundary=" + multipartBoundary, "User-Agent": "Mozilla/5.0 JulFixture/1.0"},
				Body:                  multipartBody,
				ExpectedOutcome:       OutcomeClean,
				ExpectedStatus:        http.StatusOK,
				ExpectedOriginReached: true,
			},
			{
				ID:                    "legit-utf8-boundary",
				Category:              "boundary",
				Description:           "URL-encoded international UTF-8 characters within request body limit",
				Method:                http.MethodPost,
				Host:                  "portal.example.test",
				Path:                  "/profile",
				Headers:               map[string]string{"Content-Type": "application/x-www-form-urlencoded", "User-Agent": "Mozilla/5.0 JulFixture/1.0"},
				Body:                  "display_name=Victor+Niharra&city=Madrid+Espa%C3%B1a&bio=Caf%C3%A9+and+distributed+systems",
				ExpectedOutcome:       OutcomeClean,
				ExpectedStatus:        http.StatusOK,
				ExpectedOriginReached: true,
			},
			{
				ID:                    "attack-sqli-query",
				Category:              "malicious",
				Description:           "Classic boolean SQL injection probe in query parameter",
				Method:                http.MethodGet,
				Host:                  "app.example.test",
				Path:                  "/products?id=1%27%20OR%20%271%27%3D%271",
				Headers:               map[string]string{"User-Agent": "Mozilla/5.0 JulFixture/1.0"},
				ExpectedOutcome:       OutcomeInterruptedRequest,
				ExpectedStatus:        http.StatusForbidden,
				ExpectedOriginReached: false,
				ExpectedRuleIDs:       []int{942100},
			},
			{
				ID:                    "attack-xss-form",
				Category:              "malicious",
				Description:           "Reflected/stored script tag XSS payload in form body",
				Method:                http.MethodPost,
				Host:                  "app.example.test",
				Path:                  "/contact",
				Headers:               map[string]string{"Content-Type": "application/x-www-form-urlencoded", "User-Agent": "Mozilla/5.0 JulFixture/1.0"},
				Body:                  "name=attacker&message=%3Cscript%3Ealert%28document.cookie%29%3C%2Fscript%3E",
				ExpectedOutcome:       OutcomeInterruptedRequest,
				ExpectedStatus:        http.StatusForbidden,
				ExpectedOriginReached: false,
				ExpectedRuleIDs:       []int{941100},
			},
			{
				ID:                    "attack-rce-json",
				Category:              "malicious",
				Description:           "Unix shell command injection probe inside JSON field",
				Method:                http.MethodPost,
				Host:                  "app.example.test",
				Path:                  "/api/items",
				Headers:               map[string]string{"Content-Type": "application/json", "User-Agent": "Mozilla/5.0 JulFixture/1.0"},
				Body:                  `{"title":"; $(whoami) && id","count":1}`,
				ExpectedOutcome:       OutcomeInterruptedRequest,
				ExpectedStatus:        http.StatusForbidden,
				ExpectedOriginReached: false,
			},
			{
				ID:                    "attack-scanner-ua",
				Category:              "malicious",
				Description:           "Known automated vulnerability scanner User-Agent header",
				Method:                http.MethodGet,
				Host:                  "app.example.test",
				Path:                  "/",
				Headers:               map[string]string{"User-Agent": "sqlmap/1.7.2#stable (https://sqlmap.org)"},
				ExpectedOutcome:       OutcomeInterruptedRequest,
				ExpectedStatus:        http.StatusForbidden,
				ExpectedOriginReached: false,
				ExpectedRuleIDs:       []int{913100},
			},
			{
				ID:                    "boundary-malformed-json",
				Category:              "boundary",
				Description:           "Syntactically truncated JSON request body triggering Coraza rule 200002",
				Method:                http.MethodPost,
				Host:                  "app.example.test",
				Path:                  "/api/items",
				Headers:               map[string]string{"Content-Type": "application/json", "User-Agent": "Mozilla/5.0 JulFixture/1.0"},
				Body:                  `{"title":"unclosed`,
				ExpectedOutcome:       OutcomeInterruptedRequest,
				ExpectedStatus:        http.StatusBadRequest,
				ExpectedOriginReached: false,
				ExpectedRuleIDs:       []int{200002},
			},
		},
	}
}

// ParseFixturePack strictly decodes and validates a WAFFixturePack from JSON (#630).
func ParseFixturePack(data []byte) (*WAFFixturePack, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p WAFFixturePack
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("waf fixture pack: decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("waf fixture pack: trailing data after JSON object")
	}
	if err := ValidateFixturePack(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ValidateFixturePack enforces schema, bounds, and secret-safety invariants on
// a WAFFixturePack (#630). A malicious-only pack is rejected because it cannot
// evaluate false positives on legitimate traffic.
func ValidateFixturePack(p *WAFFixturePack) error {
	if p == nil {
		return errors.New("waf fixture pack is nil")
	}
	if p.SchemaVersion != FixturePackSchemaVersion {
		return fmt.Errorf("waf fixture pack: unsupported schema_version %d (want %d)", p.SchemaVersion, FixturePackSchemaVersion)
	}
	if strings.TrimSpace(p.PackID) == "" {
		return errors.New("waf fixture pack: pack_id is required")
	}
	if len(p.Fixtures) == 0 || len(p.Fixtures) > MaxFixturesPerPack {
		return fmt.Errorf("waf fixture pack: fixtures count %d must be between 1 and %d", len(p.Fixtures), MaxFixturesPerPack)
	}

	seen := make(map[string]struct{}, len(p.Fixtures))
	legitCount := 0
	maliciousCount := 0
	for i, f := range p.Fixtures {
		id := strings.TrimSpace(f.ID)
		if id == "" {
			return fmt.Errorf("waf fixture pack: fixtures[%d].id is empty", i)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("waf fixture pack: duplicate fixture id %q", id)
		}
		seen[id] = struct{}{}

		if !slices.Contains(validFixtureCategories, f.Category) {
			return fmt.Errorf("waf fixture pack: fixture %q has invalid category %q", id, f.Category)
		}
		if f.Category == "legitimate" {
			legitCount++
		}
		if f.Category == "malicious" {
			maliciousCount++
		}
		if err := validateFixtureHTTPTarget(id, f.Method, f.Host, f.Path); err != nil {
			return err
		}
		if !slices.Contains(validExpectedOutcomes, f.ExpectedOutcome) {
			return fmt.Errorf("waf fixture pack: fixture %q has invalid expected_outcome %q", id, f.ExpectedOutcome)
		}
		if f.ExpectedStatus < 100 || f.ExpectedStatus > 599 {
			return fmt.Errorf("waf fixture pack: fixture %q has invalid expected_status %d", id, f.ExpectedStatus)
		}
		if f.OriginStatus != 0 && (f.OriginStatus < 100 || f.OriginStatus > 599) {
			return fmt.Errorf("waf fixture pack: fixture %q has invalid origin_status %d (must be 0 or 100..599)", id, f.OriginStatus)
		}
		if strings.Contains(f.Body, "-----BEGIN ") || strings.Contains(f.OriginBody, "-----BEGIN ") {
			return fmt.Errorf("waf fixture pack: fixture %q contains a PEM private key block", id)
		}
	}
	if legitCount == 0 {
		return errors.New("waf fixture pack: at least one legitimate fixture is required (a malicious-only pack cannot evaluate false positives)")
	}
	if maliciousCount == 0 {
		return errors.New("waf fixture pack: at least one malicious control fixture is required")
	}
	return nil
}

func isValidHTTPMethodToken(m string) bool {
	if len(m) == 0 || len(m) > 32 {
		return false
	}
	for i := 0; i < len(m); i++ {
		c := m[i]
		isAlpha := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
		isDigit := c >= '0' && c <= '9'
		isTokenPunct := strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))
		if !isAlpha && !isDigit && !isTokenPunct {
			return false
		}
	}
	return true
}

func validateFixtureHTTPTarget(id, method, host, path string) error {
	if !isValidHTTPMethodToken(method) {
		return fmt.Errorf("waf fixture pack: fixture %q has invalid HTTP method %q", id, method)
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("waf fixture pack: fixture %q path %q must start with '/'", id, path)
	}
	for i := 0; i < len(path); i++ {
		if path[i] <= 0x20 || path[i] >= 0x7f {
			return fmt.Errorf("waf fixture pack: fixture %q path contains invalid control or whitespace byte", id)
		}
	}
	effHost := strings.TrimSpace(host)
	if effHost == "" {
		effHost = "fixture.example.test"
	}
	for i := 0; i < len(effHost); i++ {
		if effHost[i] <= 0x20 || effHost[i] >= 0x7f || effHost[i] == '/' || effHost[i] == '?' || effHost[i] == '#' {
			return fmt.Errorf("waf fixture pack: fixture %q has invalid host %q", id, host)
		}
	}
	if _, err := http.NewRequestWithContext(context.Background(), method, "http://"+effHost+path, nil); err != nil {
		return fmt.Errorf("waf fixture pack: fixture %q target cannot construct HTTP request: %w", id, err)
	}
	return nil
}

// EvaluatePolicy compiles candidateCfg (and optional baselineCfg) from their
// exact recorded inputs and executes every fixture in pack through real Jul
// WAF handlers and synthetic origin counters (#630).
func EvaluatePolicy(
	ctx context.Context,
	scope string,
	candidateCfg config.WAFConfig,
	baselineCfg *config.WAFConfig,
	pack *WAFFixturePack,
	versions *versionid.Context,
) (*WAFEvaluationReport, error) {
	if pack == nil {
		pack = DefaultFixturePack()
	}
	if err := ValidateFixturePack(pack); err != nil {
		return nil, err
	}
	if scope == "" {
		scope = "global"
	}
	if versions == nil {
		versions = versionid.Process()
	}

	compileStart := time.Now()
	candManifest, candFW, err := BuildPolicyManifest(ctx, scope, candidateCfg, versions)
	if err != nil {
		return nil, fmt.Errorf("candidate policy: %w", err)
	}
	defer func() { _ = candFW.Close() }()

	var baseFW *Firewall
	if baselineCfg != nil && baselineCfg.Enabled {
		_, bfw, bErr := BuildPolicyManifest(ctx, scope+":baseline", *baselineCfg, versions)
		if bErr != nil {
			return nil, fmt.Errorf("baseline policy: %w", bErr)
		}
		baseFW = bfw
		defer func() { _ = baseFW.Close() }()
	}
	compileDurationMs := float64(time.Since(compileStart).Microseconds()) / 1000.0

	evalStart := time.Now()
	cases := make([]WAFFixtureCaseResult, 0, len(pack.Fixtures))
	summary := WAFEvaluationSummary{
		State:                "current",
		Source:               "server_observed",
		EvaluatedAt:          time.Now().UTC().Format(time.RFC3339),
		PackID:               pack.PackID,
		Scope:                scope,
		PolicyDigest:         candManifest.PolicyDigest,
		ExternalDigest:       candManifest.ExternalDigest,
		EngineVersion:        candManifest.EngineVersion,
		EmbeddedCRSVersion:   candManifest.EmbeddedCRSVersion,
		AnalysisCompleteness: "complete",
		TotalCases:           len(pack.Fixtures),
		CompileDurationMs:    compileDurationMs,
	}

	completeness, warnings := inspectCustomSecLangCompleteness(candidateCfg)
	summary.AnalysisCompleteness = completeness
	summary.Warnings = warnings

	for _, fc := range pack.Fixtures {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candOut, candStatus, candOrigin, candRules := runSingleFixture(candFW, candidateCfg, fc)
		res := WAFFixtureCaseResult{
			ID:                     fc.ID,
			Category:               fc.Category,
			ExpectedOutcome:        fc.ExpectedOutcome,
			ExpectedStatus:         fc.ExpectedStatus,
			CandidateOutcome:       candOut,
			CandidateStatus:        candStatus,
			CandidateOriginReached: candOrigin,
			CandidateMatchedRules:  candRules,
		}

		passed := candOut == fc.ExpectedOutcome && candStatus == fc.ExpectedStatus && candOrigin == fc.ExpectedOriginReached
		if passed && len(fc.ExpectedRuleIDs) > 0 {
			for _, wantID := range fc.ExpectedRuleIDs {
				if !slices.Contains(candRules, wantID) {
					passed = false
					break
				}
			}
		}
		res.PassedExpectation = passed

		switch fc.Category {
		case "legitimate":
			summary.LegitimateTotal++
			if !candOrigin || candOut == OutcomeInterruptedRequest || candOut == OutcomeInterruptedResponse || candOut == OutcomeRejectedBodyLimit {
				summary.LegitimateBlocked++
				if !passed {
					res.RegressionKind = "legitimate_blocked"
				}
			}
		case "malicious":
			summary.MaliciousTotal++
			if candOrigin && candOut != OutcomeInterruptedRequest && candOut != OutcomeInterruptedResponse {
				summary.MaliciousAllowed++
				if !passed {
					res.RegressionKind = "malicious_allowed"
				}
			}
		}
		if !passed && res.RegressionKind == "" {
			res.RegressionKind = "expectation_mismatch"
		}

		if baseFW != nil {
			baseOut, baseStatus, _, _ := runSingleFixture(baseFW, *baselineCfg, fc)
			res.BaselineOutcome = baseOut
			res.BaselineStatus = baseStatus
			if baseOut != candOut || baseStatus != candStatus {
				res.BehaviorChanged = true
				summary.ChangedFromBaseline++
			}
		}

		if passed {
			summary.PassedCases++
		} else {
			summary.FailedCases++
		}
		cases = append(cases, res)
	}
	if summary.FailedCases > 0 {
		summary.State = "failed"
	}
	summary.EvalDurationMs = float64(time.Since(evalStart).Microseconds()) / 1000.0

	return &WAFEvaluationReport{
		SchemaVersion:     EvaluationReportSchemaVersion,
		Summary:           summary,
		RawPolicySHA256:   candManifest.RawPolicySHA256,
		RawExternalSHA256: candManifest.RawExternalSHA256,
		Cases:             cases,
	}, nil
}

func runSingleFixture(fw *Firewall, cfg config.WAFConfig, fc WAFFixtureCase) (outcome string, status int, originReached bool, matchedRules []int) {
	inv := NewInvestigationStore(4, time.Minute)
	var lastOutcome string
	localFW := *fw
	localFW.opts.Investigation = inv
	localFW.opts.Hooks = Hooks{
		OnTransaction: func(_, out string) {
			lastOutcome = out
		},
	}

	var hits atomic.Int64
	origStatus := fc.OriginStatus
	if origStatus == 0 {
		origStatus = http.StatusOK
	}
	if origStatus < 100 || origStatus > 599 {
		return OutcomeInspectionError, http.StatusBadRequest, false, nil
	}
	origBody := fc.OriginBody
	if origBody == "" {
		origBody = "fixture-origin-ok"
	}

	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		for k, v := range fc.OriginHeaders {
			w.Header().Set(k, v)
		}
		w.WriteHeader(origStatus)
		_, _ = io.WriteString(w, origBody)
	})

	h := localFW.RouteMiddleware(RouteAttribution{Scope: "eval"}, func() uint64 { return 1 })(origin)
	host := strings.TrimSpace(fc.Host)
	if host == "" {
		host = "fixture.example.test"
	}
	if err := validateFixtureHTTPTarget(fc.ID, fc.Method, host, fc.Path); err != nil {
		return OutcomeInspectionError, http.StatusBadRequest, false, nil
	}
	var bodyReader io.Reader
	if fc.Body != "" {
		bodyReader = strings.NewReader(fc.Body)
	}
	req, err := http.NewRequestWithContext(context.Background(), fc.Method, "http://"+host+fc.Path, bodyReader)
	if err != nil {
		return OutcomeInspectionError, http.StatusBadRequest, false, nil
	}
	req.RemoteAddr = "192.0.2.1:12345"
	req.Host = host
	for k, v := range fc.Headers {
		req.Header.Set(k, v)
	}
	if fc.Body != "" && req.Header.Get("Content-Length") == "" && len(req.TransferEncoding) == 0 {
		req.Header.Set("Content-Length", strconv.Itoa(len(fc.Body)))
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	snap := inv.Snapshot(time.Now().UTC(), InvestigationFilter{Limit: 1})
	if len(snap.Events) > 0 {
		ev := snap.Events[0]
		for _, mr := range ev.MatchedRules {
			if !slices.Contains(matchedRules, mr.RuleID) {
				matchedRules = append(matchedRules, mr.RuleID)
			}
		}
		if ev.InterruptedRuleID > 0 && !slices.Contains(matchedRules, ev.InterruptedRuleID) {
			matchedRules = append(matchedRules, ev.InterruptedRuleID)
		}
	}
	_ = cfg
	return lastOutcome, rr.Code, hits.Load() > 0, matchedRules
}

func inspectCustomSecLangCompleteness(cfg config.WAFConfig) (string, []string) {
	var warnings []string
	completeness := "complete"
	if len(cfg.DirectivesFiles) > 0 {
		warnings = append(warnings, "external directives_files are bound by exact byte digest; re-run evaluation whenever external files change")
	}
	if ir := strings.TrimSpace(cfg.InlineRules); ir != "" {
		upper := strings.ToUpper(ir)
		for _, dynamicToken := range []string{"SKIPAFTER", "ALLOW", "SECRULESCRIPT", "CTL:RULEENGINE", "EXEC"} {
			if strings.Contains(upper, dynamicToken) {
				completeness = "partial_unmodelled_custom"
				warnings = append(warnings, fmt.Sprintf("inline_rules contains advanced flow/dynamic directive %q; static equivalence cannot be guaranteed beyond the tested fixtures", dynamicToken))
			}
		}
	}
	return completeness, warnings
}

// ParseEvaluationReport strictly decodes and validates an imported or supplied
// WAFEvaluationReport as bounded untrusted input (#630).
func ParseEvaluationReport(data []byte) (*WAFEvaluationReport, error) {
	if len(data) > 256<<10 {
		return nil, errors.New("waf evaluation report exceeds 256 KiB bound")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rep WAFEvaluationReport
	if err := dec.Decode(&rep); err != nil {
		return nil, fmt.Errorf("waf evaluation report: decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("waf evaluation report: trailing data after JSON object")
	}
	if rep.SchemaVersion != EvaluationReportSchemaVersion {
		return nil, fmt.Errorf("waf evaluation report: unsupported schema_version %d (want %d)", rep.SchemaVersion, EvaluationReportSchemaVersion)
	}
	if len(rep.Cases) == 0 || len(rep.Cases) > MaxFixturesPerPack {
		return nil, fmt.Errorf("waf evaluation report: cases count %d must be between 1 and %d", len(rep.Cases), MaxFixturesPerPack)
	}
	if rep.Summary.TotalCases != len(rep.Cases) {
		return nil, fmt.Errorf("waf evaluation report: total_cases %d does not match len(cases) %d", rep.Summary.TotalCases, len(rep.Cases))
	}
	if rep.Summary.PolicyDigest == "" && rep.RawPolicySHA256 == "" {
		return nil, errors.New("waf evaluation report: policy_digest or raw_policy_sha256 is required")
	}
	if err := ValidateEvaluationReport(&rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

// ValidateEvaluationReport verifies that an evaluation report's case list and
// aggregate summary counts are internally consistent and within bounds (#630).
func ValidateEvaluationReport(rep *WAFEvaluationReport) error {
	if rep == nil {
		return errors.New("waf evaluation report is nil")
	}
	if len(rep.Cases) == 0 || len(rep.Cases) > MaxFixturesPerPack {
		return fmt.Errorf("waf evaluation report: cases count %d must be between 1 and %d", len(rep.Cases), MaxFixturesPerPack)
	}
	s := rep.Summary
	if s.TotalCases < 0 || s.PassedCases < 0 || s.FailedCases < 0 ||
		s.LegitimateTotal < 0 || s.LegitimateBlocked < 0 ||
		s.MaliciousTotal < 0 || s.MaliciousAllowed < 0 ||
		s.ChangedFromBaseline < 0 {
		return errors.New("waf evaluation report: summary counts must be non-negative")
	}
	if s.TotalCases != len(rep.Cases) {
		return fmt.Errorf("waf evaluation report: total_cases %d does not match len(cases) %d", s.TotalCases, len(rep.Cases))
	}

	seen := make(map[string]struct{}, len(rep.Cases))
	var passed, failed, legitTotal, legitBlocked, malTotal, malAllowed, changed int
	for i, c := range rep.Cases {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			return fmt.Errorf("waf evaluation report: cases[%d].id is empty", i)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("waf evaluation report: duplicate case id %q", id)
		}
		seen[id] = struct{}{}
		if !slices.Contains(validFixtureCategories, c.Category) {
			return fmt.Errorf("waf evaluation report: case %q has invalid category %q", id, c.Category)
		}
		if c.ExpectedOutcome != "" && !slices.Contains(validExpectedOutcomes, c.ExpectedOutcome) {
			return fmt.Errorf("waf evaluation report: case %q has invalid expected_outcome %q", id, c.ExpectedOutcome)
		}
		if c.CandidateOutcome != "" && !slices.Contains(validExpectedOutcomes, c.CandidateOutcome) {
			return fmt.Errorf("waf evaluation report: case %q has invalid candidate_outcome %q", id, c.CandidateOutcome)
		}
		if c.PassedExpectation {
			if (c.ExpectedOutcome != "" && c.CandidateOutcome != "" && c.CandidateOutcome != c.ExpectedOutcome) ||
				(c.ExpectedStatus != 0 && c.CandidateStatus != 0 && c.CandidateStatus != c.ExpectedStatus) ||
				c.RegressionKind != "" {
				return fmt.Errorf("waf evaluation report: case %q claims passed_expectation=true despite outcome/status/regression mismatch", id)
			}
			passed++
		} else {
			failed++
		}
		switch c.Category {
		case "legitimate":
			legitTotal++
			if (!c.CandidateOriginReached && c.CandidateOutcome != "") ||
				c.CandidateOutcome == OutcomeInterruptedRequest ||
				c.CandidateOutcome == OutcomeInterruptedResponse ||
				c.CandidateOutcome == OutcomeRejectedBodyLimit ||
				c.RegressionKind == "legitimate_blocked" {
				legitBlocked++
			}
		case "malicious":
			malTotal++
			if (c.CandidateOriginReached && c.CandidateOutcome != "" && c.CandidateOutcome != OutcomeInterruptedRequest && c.CandidateOutcome != OutcomeInterruptedResponse) ||
				c.RegressionKind == "malicious_allowed" {
				malAllowed++
			}
		}
		if c.BehaviorChanged {
			changed++
		}
	}
	if s.LegitimateTotal == 0 && s.MaliciousTotal == 0 {
		rep.Summary.LegitimateTotal = legitTotal
		rep.Summary.MaliciousTotal = malTotal
		s = rep.Summary
	}
	if s.PassedCases != passed || s.FailedCases != failed {
		return fmt.Errorf("waf evaluation report: summary passed/failed (%d/%d) contradicts case results (%d/%d)", s.PassedCases, s.FailedCases, passed, failed)
	}
	if s.LegitimateTotal != legitTotal {
		return fmt.Errorf("waf evaluation report: summary legitimate_total %d contradicts case categories (%d)", s.LegitimateTotal, legitTotal)
	}
	if s.MaliciousTotal != malTotal {
		return fmt.Errorf("waf evaluation report: summary malicious_total %d contradicts case categories (%d)", s.MaliciousTotal, malTotal)
	}
	if s.LegitimateBlocked > s.LegitimateTotal || s.LegitimateBlocked < legitBlocked {
		return fmt.Errorf("waf evaluation report: summary legitimate_blocked %d contradicts case outcomes (%d/%d)", s.LegitimateBlocked, legitBlocked, legitTotal)
	}
	if s.MaliciousAllowed > s.MaliciousTotal || s.MaliciousAllowed < malAllowed {
		return fmt.Errorf("waf evaluation report: summary malicious_allowed %d contradicts case outcomes (%d/%d)", s.MaliciousAllowed, malAllowed, malTotal)
	}
	if s.ChangedFromBaseline > s.TotalCases || (changed > 0 && s.ChangedFromBaseline != changed) {
		return fmt.Errorf("waf evaluation report: summary changed_from_baseline %d contradicts case results (%d)", s.ChangedFromBaseline, changed)
	}
	if failed > 0 {
		rep.Summary.State = "failed"
	}
	return nil
}

// VerifyEvaluationBinding re-verifies a stored or supplied WAFEvaluationSummary
// against the currently compiled policy's exact digests and binary versions
// (#630, ADR 0031 §3.2). Same-path external file edits change currentInfo's
// ExternalDigest / currentRawExternalSHA and mark the summary "stale_asset_changed".
func VerifyEvaluationBinding(
	summary *WAFEvaluationSummary,
	reportRawPolicySHA string,
	reportRawExternalSHA string,
	currentInfo PolicyInfo,
	currentRawPolicySHA string,
	currentRawExternalSHA string,
) WAFEvaluationSummary {
	if !Compiled {
		return WAFEvaluationSummary{State: "unavailable"}
	}
	if summary == nil || (summary.PolicyDigest == "" && reportRawPolicySHA == "") {
		return WAFEvaluationSummary{
			State:              "unrun",
			PolicyDigest:       currentInfo.PolicyDigest,
			ExternalDigest:     currentInfo.ExternalDigest,
			EngineVersion:      EngineVersion(),
			EmbeddedCRSVersion: EmbeddedCRSVersion(),
		}
	}
	out := *summary
	out.Warnings = append([]string(nil), summary.Warnings...)

	curEngine := EngineVersion()
	if out.EngineVersion != "" && curEngine != "" && out.EngineVersion != curEngine {
		out.State = "stale_engine_or_crs_changed"
		return out
	}
	curCRS := EmbeddedCRSVersion()
	if currentInfo.CRSEnabled && out.EmbeddedCRSVersion != "" && curCRS != "" && out.EmbeddedCRSVersion != curCRS {
		out.State = "stale_engine_or_crs_changed"
		return out
	}

	// Check external asset digest first so same-path file changes report
	// "stale_asset_changed" specifically.
	extChanged := false
	if reportRawExternalSHA != "" && currentRawExternalSHA != "" {
		if reportRawExternalSHA != currentRawExternalSHA {
			extChanged = true
		}
	} else if out.ExternalDigest != currentInfo.ExternalDigest {
		extChanged = true
	}
	if extChanged {
		out.State = "stale_asset_changed"
		return out
	}

	policyMatches := false
	if reportRawPolicySHA != "" && currentRawPolicySHA != "" {
		policyMatches = reportRawPolicySHA == currentRawPolicySHA
	} else if out.PolicyDigest != "" && currentInfo.PolicyDigest != "" {
		policyMatches = out.PolicyDigest == currentInfo.PolicyDigest
	}
	if !policyMatches {
		out.State = "stale_policy_changed"
		return out
	}

	if out.FailedCases > 0 {
		out.State = "failed"
		return out
	}
	out.State = "current"
	return out
}
