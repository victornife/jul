// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

// Package corpus defines the versioned, secret-safe fixture contract used by
// the NGINX migration corpus and selected-dimension runtime comparisons.
package corpus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// SchemaVersion is the current fixture manifest contract.
	SchemaVersion = 1

	maxDescriptionRunes = 500
	maxRequestBodyBytes = 64 << 10
)

var fixtureIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Tier controls where a fixture is expected to execute.
type Tier string

const (
	TierCore Tier = "core"
	TierFull Tier = "full"
)

// OriginKind records how a fixture entered the repository.
type OriginKind string

const (
	OriginRepositoryAuthored OriginKind = "repository_authored"
	OriginGenerated          OriginKind = "generated"
	OriginPublicDerived      OriginKind = "public_derived"
)

// Verdict is deliberately bounded so reports cannot overstate equivalence.
type Verdict string

const (
	VerdictEquivalent         Verdict = "equivalent_for_asserted_dimensions"
	VerdictExpectedDifference Verdict = "expected_difference"
	VerdictUnexpected         Verdict = "unexpected_difference"
	VerdictNotExecuted        Verdict = "not_executed"
	VerdictBlockingSource     Verdict = "blocking_source"
)

// Dimension is one explicitly asserted response property.
type Dimension string

const (
	DimensionStatus         Dimension = "status"
	DimensionHeaders        Dimension = "headers"
	DimensionBody           Dimension = "body"
	DimensionBodySHA256     Dimension = "body_sha256"
	DimensionRedirectTarget Dimension = "redirect_target"
)

// Origin documents fixture provenance and licensing.
type Origin struct {
	Kind    OriginKind `json:"kind"`
	License string     `json:"license"`
	Source  string     `json:"source"`
}

// Manifest is the complete versioned fixture contract. It is intentionally
// self-contained so review of one file shows provenance, expected assessment,
// candidate assertions, and every permitted replay scenario.
type Manifest struct {
	SchemaVersion  int                `json:"schema_version"`
	ID             string             `json:"id"`
	Tier           Tier               `json:"tier"`
	Description    string             `json:"description"`
	Origin         Origin             `json:"origin"`
	Categories     []string           `json:"categories"`
	BuildTags      []string           `json:"build_tags,omitempty"`
	Root           string             `json:"root"`
	FollowIncludes bool               `json:"follow_includes"`
	Assessment     ExpectedAssessment `json:"expected_assessment"`
	Candidate      CandidateGolden    `json:"expected_candidate"`
	Scenarios      []Scenario         `json:"scenarios,omitempty"`
}

// ExpectedAssessment is an exact semantic golden. Results are compared as a
// multiset so the corpus catches silently added, removed, or reclassified
// directives without coupling the fixture to human prose.
type ExpectedAssessment struct {
	Status    string           `json:"status"`
	Ready     bool             `json:"ready"`
	Complete  bool             `json:"complete"`
	FilesRead int              `json:"files_read"`
	Sources   []string         `json:"sources"`
	Results   []ExpectedResult `json:"results"`
}

// ExpectedResult identifies one or more equivalent assessment results.
type ExpectedResult struct {
	Source    string `json:"source"`
	Code      string `json:"code"`
	Class     string `json:"class"`
	Risk      string `json:"risk"`
	Context   string `json:"context"`
	Directive string `json:"directive"`
	Count     int    `json:"count,omitempty"`
}

// CandidateGolden asserts selected canonical TOML fragments without pretending
// that every defaulted field is a durable corpus contract.
type CandidateGolden struct {
	Required bool     `json:"required"`
	Contains []string `json:"contains,omitempty"`
}

// Scenario is one safe, selected-dimension behavior assertion. Reference is the
// approved NGINX-side expectation. Jul, when present, records an intentional
// expected difference; otherwise Jul is expected to match Reference.
type Scenario struct {
	ID                     string           `json:"id"`
	Safe                   bool             `json:"safe"`
	SideEffectFree         bool             `json:"side_effect_free,omitempty"`
	Request                RequestSpec      `json:"request"`
	Assert                 []Dimension      `json:"assert"`
	AssertHeaders          []string         `json:"assert_headers,omitempty"`
	IgnoreHeaders          []string         `json:"ignore_headers,omitempty"`
	Reference              ObservationSpec  `json:"reference"`
	Jul                    *ObservationSpec `json:"jul,omitempty"`
	ExpectedVerdict        Verdict          `json:"expected_verdict"`
	ExpectedDifferenceCode string           `json:"expected_difference_code,omitempty"`
	NotExecutedReason      string           `json:"not_executed_reason,omitempty"`
}

// RequestSpec contains only synthetic fixture traffic. Path includes an
// optional query string but never a scheme or authority.
type RequestSpec struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Host    string              `json:"host,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
}

// ObservationSpec is the stable JSON representation of asserted behavior.
type ObservationSpec struct {
	Status     int                 `json:"status,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       string              `json:"body,omitempty"`
	BodySHA256 string              `json:"body_sha256,omitempty"`
}

// DecodeManifest decodes exactly one strict JSON manifest.
func DecodeManifest(r io.Reader) (Manifest, error) {
	var manifest Manifest
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode corpus manifest: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, fmt.Errorf("decode corpus manifest: multiple JSON values")
		}
		return Manifest{}, fmt.Errorf("decode corpus manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate enforces the closed corpus, replay, and comparison contracts.
func (m Manifest) Validate() error {
	var errs []error
	if m.SchemaVersion != SchemaVersion {
		errs = append(errs, fmt.Errorf("schema_version must be %d", SchemaVersion))
	}
	if !fixtureIDPattern.MatchString(m.ID) {
		errs = append(errs, fmt.Errorf("id must match %s", fixtureIDPattern.String()))
	}
	switch m.Tier {
	case TierCore, TierFull:
	default:
		errs = append(errs, fmt.Errorf("tier must be %q or %q", TierCore, TierFull))
	}
	if strings.TrimSpace(m.Description) == "" || utf8.RuneCountInString(m.Description) > maxDescriptionRunes {
		errs = append(errs, fmt.Errorf("description must contain 1-%d runes", maxDescriptionRunes))
	}
	if err := validateOrigin(m.Origin); err != nil {
		errs = append(errs, err)
	}
	if err := validateSortedUniqueTokens("categories", m.Categories, true); err != nil {
		errs = append(errs, err)
	}
	if err := validateSortedUniqueTokens("build_tags", m.BuildTags, false); err != nil {
		errs = append(errs, err)
	}
	if err := validateRelativePath("root", m.Root); err != nil {
		errs = append(errs, err)
	}
	if err := validateAssessment(m.Assessment); err != nil {
		errs = append(errs, err)
	}
	if err := validateCandidate(m.Candidate); err != nil {
		errs = append(errs, err)
	}

	seenScenario := map[string]struct{}{}
	for i := range m.Scenarios {
		s := m.Scenarios[i]
		if _, exists := seenScenario[s.ID]; exists {
			errs = append(errs, fmt.Errorf("scenarios[%d].id %q is duplicated", i, s.ID))
		} else {
			seenScenario[s.ID] = struct{}{}
		}
		if err := s.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("scenario %q: %w", s.ID, err))
		}
	}
	if len(m.Scenarios) > 0 && !m.Candidate.Required {
		errs = append(errs, fmt.Errorf("runtime scenarios require expected_candidate.required=true"))
	}
	if m.Candidate.Required && !m.Assessment.Ready {
		errs = append(errs, fmt.Errorf("a required generated candidate needs expected_assessment.ready=true"))
	}
	if !m.Candidate.Required && m.Assessment.Ready && len(m.Scenarios) > 0 {
		errs = append(errs, fmt.Errorf("a ready fixture with scenarios must require its candidate"))
	}
	return errors.Join(errs...)
}

func validateOrigin(origin Origin) error {
	var errs []error
	switch origin.Kind {
	case OriginRepositoryAuthored, OriginGenerated, OriginPublicDerived:
	default:
		errs = append(errs, fmt.Errorf("origin.kind is not recognized"))
	}
	if strings.TrimSpace(origin.License) == "" {
		errs = append(errs, fmt.Errorf("origin.license is required"))
	}
	if strings.TrimSpace(origin.Source) == "" {
		errs = append(errs, fmt.Errorf("origin.source is required"))
	}
	if origin.Kind == OriginPublicDerived && !strings.HasPrefix(origin.Source, "https://") {
		errs = append(errs, fmt.Errorf("public_derived origin.source must be an https URL"))
	}
	return errors.Join(errs...)
}

func validateAssessment(expected ExpectedAssessment) error {
	var errs []error
	switch expected.Status {
	case "ready_for_review", "manual_action_required", "parse_error", "invalid_candidate":
	default:
		errs = append(errs, fmt.Errorf("expected_assessment.status is not recognized"))
	}
	if expected.FilesRead < 1 {
		errs = append(errs, fmt.Errorf("expected_assessment.files_read must be positive"))
	}
	if err := validateSortedUniquePaths("expected_assessment.sources", expected.Sources, true); err != nil {
		errs = append(errs, err)
	}
	if len(expected.Results) == 0 {
		errs = append(errs, fmt.Errorf("expected_assessment.results must not be empty"))
	}
	seen := map[string]struct{}{}
	for i, result := range expected.Results {
		count := result.Count
		if count == 0 {
			count = 1
		}
		if count < 1 {
			errs = append(errs, fmt.Errorf("expected_assessment.results[%d].count must be positive", i))
		}
		if err := validateRelativePath(fmt.Sprintf("expected_assessment.results[%d].source", i), result.Source); err != nil {
			errs = append(errs, err)
		}
		for name, value := range map[string]string{
			"code": result.Code, "class": result.Class, "risk": result.Risk,
			"context": result.Context, "directive": result.Directive,
		} {
			if strings.TrimSpace(value) == "" {
				errs = append(errs, fmt.Errorf("expected_assessment.results[%d].%s is required", i, name))
			}
		}
		key := strings.Join([]string{result.Source, result.Code, result.Class, result.Risk, result.Context, result.Directive}, "\x00")
		if _, exists := seen[key]; exists {
			errs = append(errs, fmt.Errorf("expected_assessment.results[%d] duplicates an earlier result; use count", i))
		}
		seen[key] = struct{}{}
	}
	if expected.Ready && expected.Status != "ready_for_review" {
		errs = append(errs, fmt.Errorf("ready assessments must use status ready_for_review"))
	}
	if !expected.Ready && expected.Status == "ready_for_review" {
		errs = append(errs, fmt.Errorf("status ready_for_review requires ready=true"))
	}
	return errors.Join(errs...)
}

func validateCandidate(candidate CandidateGolden) error {
	var errs []error
	if !candidate.Required && len(candidate.Contains) > 0 {
		errs = append(errs, fmt.Errorf("expected_candidate.contains requires required=true"))
	}
	if candidate.Required && len(candidate.Contains) == 0 {
		errs = append(errs, fmt.Errorf("a required candidate needs at least one contains assertion"))
	}
	if err := validateSortedUniqueTokens("expected_candidate.contains", candidate.Contains, false); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Validate enforces safe replay and bounded selected-dimension assertions.
func (s Scenario) Validate() error {
	var errs []error
	if !fixtureIDPattern.MatchString(s.ID) {
		errs = append(errs, fmt.Errorf("id must match %s", fixtureIDPattern.String()))
	}
	if !s.Safe {
		errs = append(errs, fmt.Errorf("safe must be true; arbitrary replay is forbidden"))
	}
	method := strings.ToUpper(strings.TrimSpace(s.Request.Method))
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	case http.MethodPost:
		if !s.SideEffectFree {
			errs = append(errs, fmt.Errorf("POST requires side_effect_free=true"))
		}
	default:
		errs = append(errs, fmt.Errorf("method %q is outside the safe replay allow-list", s.Request.Method))
	}
	if s.Request.Method != method {
		errs = append(errs, fmt.Errorf("request.method must be uppercase and trimmed"))
	}
	if err := validateRequestTarget(s.Request.Path); err != nil {
		errs = append(errs, err)
	}
	if err := validateBoundedText("request.host", s.Request.Host, 255, true); err != nil {
		errs = append(errs, err)
	}
	if strings.Contains(s.Request.Host, "://") {
		errs = append(errs, fmt.Errorf("request.host must not contain a scheme"))
	}
	if err := validateHeaderMap("request.headers", s.Request.Headers, true); err != nil {
		errs = append(errs, err)
	}
	if len(s.Request.Body) > maxRequestBodyBytes {
		errs = append(errs, fmt.Errorf("request.body exceeds %d bytes", maxRequestBodyBytes))
	}
	if s.Request.Body != "" && method != http.MethodPost {
		errs = append(errs, fmt.Errorf("request.body is allowed only for an explicitly side-effect-free POST"))
	}

	if len(s.Assert) == 0 {
		errs = append(errs, fmt.Errorf("assert must not be empty"))
	}
	seenDimension := map[Dimension]struct{}{}
	for _, dimension := range s.Assert {
		switch dimension {
		case DimensionStatus, DimensionHeaders, DimensionBody, DimensionBodySHA256, DimensionRedirectTarget:
		default:
			errs = append(errs, fmt.Errorf("assert dimension %q is not recognized", dimension))
		}
		if _, exists := seenDimension[dimension]; exists {
			errs = append(errs, fmt.Errorf("assert dimension %q is duplicated", dimension))
		}
		seenDimension[dimension] = struct{}{}
	}
	if _, body := seenDimension[DimensionBody]; body {
		if _, hash := seenDimension[DimensionBodySHA256]; hash {
			errs = append(errs, fmt.Errorf("body and body_sha256 cannot both be asserted"))
		}
	}
	if _, headers := seenDimension[DimensionHeaders]; headers {
		if err := validateHeaderNames("assert_headers", s.AssertHeaders, true); err != nil {
			errs = append(errs, err)
		}
	} else if len(s.AssertHeaders) > 0 {
		errs = append(errs, fmt.Errorf("assert_headers requires the headers dimension"))
	}
	if err := validateHeaderNames("ignore_headers", s.IgnoreHeaders, false); err != nil {
		errs = append(errs, err)
	}
	ignored := make(map[string]struct{}, len(s.IgnoreHeaders))
	for _, name := range s.IgnoreHeaders {
		ignored[name] = struct{}{}
	}
	for _, name := range s.AssertHeaders {
		if _, exists := ignored[name]; exists {
			errs = append(errs, fmt.Errorf("header %q cannot be both asserted and ignored", name))
		}
	}
	if err := validateObservation("reference", s.Reference, seenDimension, s.AssertHeaders); err != nil {
		errs = append(errs, err)
	}
	if s.Jul != nil {
		if err := validateObservation("jul", *s.Jul, seenDimension, s.AssertHeaders); err != nil {
			errs = append(errs, err)
		}
	}

	expectedJul := s.Reference
	if s.Jul != nil {
		expectedJul = *s.Jul
	}
	referenceDiffs := compareObservationSpecs(s, s.Reference, expectedJul)
	switch s.ExpectedVerdict {
	case VerdictEquivalent:
		if len(referenceDiffs) != 0 {
			errs = append(errs, fmt.Errorf("equivalent verdict requires identical reference and Jul expectations"))
		}
		if s.ExpectedDifferenceCode != "" {
			errs = append(errs, fmt.Errorf("equivalent verdict must not set expected_difference_code"))
		}
	case VerdictExpectedDifference:
		if len(referenceDiffs) == 0 {
			errs = append(errs, fmt.Errorf("expected_difference requires an explicit Jul expectation that differs"))
		}
		if strings.TrimSpace(s.ExpectedDifferenceCode) == "" {
			errs = append(errs, fmt.Errorf("expected_difference requires expected_difference_code"))
		}
	case VerdictNotExecuted:
		if strings.TrimSpace(s.NotExecutedReason) == "" {
			errs = append(errs, fmt.Errorf("not_executed requires not_executed_reason"))
		}
	case VerdictBlockingSource:
		if strings.TrimSpace(s.NotExecutedReason) == "" {
			errs = append(errs, fmt.Errorf("blocking_source requires not_executed_reason"))
		}
	case VerdictUnexpected:
		errs = append(errs, fmt.Errorf("unexpected_difference cannot be an approved expected verdict"))
	default:
		errs = append(errs, fmt.Errorf("expected_verdict is not recognized"))
	}
	return errors.Join(errs...)
}

func validateObservation(prefix string, observation ObservationSpec, dimensions map[Dimension]struct{}, assertedHeaders []string) error {
	var errs []error
	if _, ok := dimensions[DimensionStatus]; ok {
		if observation.Status < 100 || observation.Status > 599 {
			errs = append(errs, fmt.Errorf("%s.status must be an HTTP status", prefix))
		}
	}
	if _, ok := dimensions[DimensionHeaders]; ok {
		if err := validateHeaderMap(prefix+".headers", observation.Headers, false); err != nil {
			errs = append(errs, err)
		}
		for _, name := range assertedHeaders {
			if _, exists := observation.Headers[name]; !exists {
				errs = append(errs, fmt.Errorf("%s.headers is missing asserted header %q", prefix, name))
			}
		}
	}
	if _, ok := dimensions[DimensionRedirectTarget]; ok {
		if _, exists := observation.Headers["location"]; !exists {
			errs = append(errs, fmt.Errorf("%s.headers.location is required for redirect_target", prefix))
		}
	}
	if _, ok := dimensions[DimensionBody]; ok {
		if observation.BodySHA256 != "" {
			errs = append(errs, fmt.Errorf("%s.body_sha256 must be empty when body is asserted", prefix))
		}
	}
	if _, ok := dimensions[DimensionBodySHA256]; ok {
		if !validSHA256(observation.BodySHA256) {
			errs = append(errs, fmt.Errorf("%s.body_sha256 must use sha256:<64 lowercase hex>", prefix))
		}
		if observation.Body != "" {
			errs = append(errs, fmt.Errorf("%s.body must be empty when body_sha256 is asserted", prefix))
		}
	}
	return errors.Join(errs...)
}

func validateRequestTarget(target string) error {
	if target == "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return fmt.Errorf("request.path must be an origin-form target beginning with one slash")
	}
	if strings.Contains(target, "://") || hasControl(target) || len(target) > 4096 {
		return fmt.Errorf("request.path is invalid or unbounded")
	}
	return nil
}

func validateHeaderMap(field string, headers map[string][]string, rejectSensitive bool) error {
	var errs []error
	for name, values := range headers {
		if name != strings.ToLower(name) || http.CanonicalHeaderKey(name) == "" {
			errs = append(errs, fmt.Errorf("%s header %q must be a valid lowercase field name", field, name))
		}
		if rejectSensitive && sensitiveHeader(name) {
			errs = append(errs, fmt.Errorf("%s header %q is secret-bearing and forbidden", field, name))
		}
		if len(values) == 0 {
			errs = append(errs, fmt.Errorf("%s header %q has no values", field, name))
		}
		for _, value := range values {
			if err := validateBoundedText(field+"."+name, value, 4096, false); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func validateHeaderNames(field string, names []string, required bool) error {
	if required && len(names) == 0 {
		return fmt.Errorf("%s must not be empty", field)
	}
	var errs []error
	if !sort.StringsAreSorted(names) {
		errs = append(errs, fmt.Errorf("%s must be sorted", field))
	}
	seen := map[string]struct{}{}
	for _, name := range names {
		if name != strings.ToLower(name) || http.CanonicalHeaderKey(name) == "" {
			errs = append(errs, fmt.Errorf("%s value %q must be a valid lowercase header", field, name))
		}
		if sensitiveHeader(name) {
			errs = append(errs, fmt.Errorf("%s value %q is secret-bearing and forbidden", field, name))
		}
		if _, exists := seen[name]; exists {
			errs = append(errs, fmt.Errorf("%s contains duplicate %q", field, name))
		}
		seen[name] = struct{}{}
	}
	return errors.Join(errs...)
}

func sensitiveHeader(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}
	return strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "api-key") || strings.Contains(name, "apikey")
}

func validateSortedUniqueTokens(field string, values []string, required bool) error {
	if required && len(values) == 0 {
		return fmt.Errorf("%s must not be empty", field)
	}
	var errs []error
	if !sort.StringsAreSorted(values) {
		errs = append(errs, fmt.Errorf("%s must be sorted", field))
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || hasControl(value) {
			errs = append(errs, fmt.Errorf("%s contains an empty or control-bearing value", field))
		}
		if _, exists := seen[value]; exists {
			errs = append(errs, fmt.Errorf("%s contains duplicate %q", field, value))
		}
		seen[value] = struct{}{}
	}
	return errors.Join(errs...)
}

func validateSortedUniquePaths(field string, values []string, required bool) error {
	var errs []error
	if !sort.StringsAreSorted(values) {
		errs = append(errs, fmt.Errorf("%s must be sorted", field))
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if err := validateRelativePath(field, value); err != nil {
			errs = append(errs, err)
		}
		if _, exists := seen[value]; exists {
			errs = append(errs, fmt.Errorf("%s contains duplicate %q", field, value))
		}
		seen[value] = struct{}{}
	}
	if required && len(values) == 0 {
		errs = append(errs, fmt.Errorf("%s must not be empty", field))
	}
	return errors.Join(errs...)
}

func validateRelativePath(field, value string) error {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) || hasControl(value) {
		return fmt.Errorf("%s must be a non-empty relative path", field)
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s must remain inside its fixture", field)
	}
	if filepath.ToSlash(clean) != value {
		return fmt.Errorf("%s must be clean and use slash separators", field)
	}
	return nil
}

func validateBoundedText(field, value string, maxRunes int, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if hasControl(value) || utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s contains control characters or exceeds %d runes", field, maxRunes)
	}
	return nil
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
