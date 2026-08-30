// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"jul/internal/config"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

// AssessmentSchemaVersion is the machine-readable NGINX assessment contract.
const AssessmentSchemaVersion = 1

// AssessmentClass is the bounded translation result for one source directive.
type AssessmentClass string

const (
	AssessmentSupported       AssessmentClass = "supported"
	AssessmentApproximated    AssessmentClass = "approximated"
	AssessmentIgnored         AssessmentClass = "ignored"
	AssessmentBlocking        AssessmentClass = "blocking"
	AssessmentInformational   AssessmentClass = "informational"
	AssessmentParseError      AssessmentClass = "parse_error"
	AssessmentValidationError AssessmentClass = "validation_error"
)

// AssessmentSeverity is the bounded operator urgency for a finding.
type AssessmentSeverity string

const (
	AssessmentInfo    AssessmentSeverity = "info"
	AssessmentWarning AssessmentSeverity = "warning"
	AssessmentError   AssessmentSeverity = "error"
)

// AssessmentRisk is the bounded impact category for a finding.
type AssessmentRisk string

const (
	RiskSecurity      AssessmentRisk = "security"
	RiskRouting       AssessmentRisk = "routing"
	RiskAvailability  AssessmentRisk = "availability"
	RiskObservability AssessmentRisk = "observability"
	RiskPerformance   AssessmentRisk = "performance"
	RiskOperational   AssessmentRisk = "operational"
	RiskCosmetic      AssessmentRisk = "cosmetic"
)

// AssessmentContext identifies the NGINX block containing a directive.
type AssessmentContext string

const (
	ContextMain        AssessmentContext = "main"
	ContextEvents      AssessmentContext = "events"
	ContextHTTP        AssessmentContext = "http"
	ContextServer      AssessmentContext = "server"
	ContextLocation    AssessmentContext = "location"
	ContextUpstream    AssessmentContext = "upstream"
	ContextLimitExcept AssessmentContext = "limit_except"
	ContextStream      AssessmentContext = "stream"
	ContextMail        AssessmentContext = "mail"
	ContextVariable    AssessmentContext = "variable_block"
)

// AssessmentResult is one stable, secret-safe result for a parsed directive.
type AssessmentResult struct {
	ID          string             `json:"id"`
	Code        string             `json:"code"`
	Class       AssessmentClass    `json:"class"`
	Severity    AssessmentSeverity `json:"severity"`
	Risk        AssessmentRisk     `json:"risk"`
	Context     AssessmentContext  `json:"context"`
	Directive   string             `json:"directive"`
	Line        int                `json:"line,omitempty"`
	Message     string             `json:"message"`
	TargetPaths []string           `json:"target_paths,omitempty"`
	Synthetic   bool               `json:"synthetic,omitempty"`
}

// AssessmentSummary contains fixed fields rather than a map so JSON is stable.
type AssessmentSummary struct {
	Total            int  `json:"total"`
	Supported        int  `json:"supported"`
	Approximated     int  `json:"approximated"`
	Ignored          int  `json:"ignored"`
	Blocking         int  `json:"blocking"`
	Informational    int  `json:"informational"`
	ParseErrors      int  `json:"parse_errors"`
	ValidationErrors int  `json:"validation_errors"`
	Ready            bool `json:"ready"`
}

// AssessmentDiagnostic is a safe projection of candidate validation/lint data.
type AssessmentDiagnostic struct {
	Severity string `json:"severity"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
}

// AssessmentValidation records whether the generated Jul candidate is valid.
type AssessmentValidation struct {
	Status   string                 `json:"status"`
	Errors   []AssessmentDiagnostic `json:"errors,omitempty"`
	Warnings []AssessmentDiagnostic `json:"warnings,omitempty"`
}

// Assessment is the versioned human- and machine-readable migration report.
type Assessment struct {
	SchemaVersion int                  `json:"schema_version"`
	Source        string               `json:"source"`
	Status        string               `json:"status"`
	Summary       AssessmentSummary    `json:"summary"`
	Results       []AssessmentResult   `json:"results"`
	Validation    AssessmentValidation `json:"validation"`
}

// BuildAssessment walks every parsed directive once and applies the registry.
func BuildAssessment(src *ngx.Config, source string, rep *Report) (a *Assessment) {
	defer func() {
		if r := recover(); r != nil {
			a = FailureAssessment(source, AssessmentValidationError, "NGX_ASSESSMENT_INTERNAL", "assessment could not be completed for the parsed directive tree")
		}
	}()
	a = &Assessment{
		SchemaVersion: AssessmentSchemaVersion,
		Source:        source,
		Validation:    AssessmentValidation{Status: "not_run"},
	}
	w := assessmentWalker{assessment: a}
	for _, d := range orderedDirectives(topLevelDirectives(src)) {
		w.walk(ContextMain, d, walkFacts{})
	}
	w.addTranslationSynthetic(rep)
	a.finalize()
	return a
}

// FailureAssessment returns a safe report for failures before translation.
func FailureAssessment(source string, class AssessmentClass, code, message string) *Assessment {
	a := &Assessment{
		SchemaVersion: AssessmentSchemaVersion,
		Source:        source,
		Validation:    AssessmentValidation{Status: "not_run"},
		Results: []AssessmentResult{{
			ID:        "result-0001",
			Code:      code,
			Class:     class,
			Severity:  AssessmentError,
			Risk:      RiskOperational,
			Context:   ContextMain,
			Directive: "<input>",
			Message:   message,
			Synthetic: true,
		}},
	}
	a.finalize()
	return a
}

// SetValidation adds a secret-safe projection of canonical Jul validation and
// lint results. Raw validator/linter prose is deliberately not copied into the
// assessment: it may contain user-controlled configuration values. Canonical
// field names, severities, counts, and the ordinary CLI diagnostics remain
// available without creating a second secret-bearing machine report.
func (a *Assessment) SetValidation(verrs []error, warnings []config.Diagnostic) {
	if a == nil {
		return
	}
	a.Validation = AssessmentValidation{Status: "valid"}
	for range verrs {
		a.Validation.Errors = append(a.Validation.Errors, AssessmentDiagnostic{
			Severity: "error",
			Message:  "generated Jul configuration failed authoritative validation",
		})
		a.Results = append(a.Results, AssessmentResult{
			Code:      "JUL_CANDIDATE_VALIDATION",
			Class:     AssessmentValidationError,
			Severity:  AssessmentError,
			Risk:      RiskOperational,
			Context:   ContextMain,
			Directive: "<generated-candidate>",
			Message:   "generated Jul configuration failed authoritative validation",
			Synthetic: true,
		})
	}
	for _, d := range warnings {
		a.Validation.Warnings = append(a.Validation.Warnings, AssessmentDiagnostic{
			Severity: d.Severity.String(),
			Field:    assessmentText(d.Field),
			Message:  "generated Jul configuration triggered a lint finding",
		})
	}
	if len(a.Validation.Errors) > 0 {
		a.Validation.Status = "invalid"
	}
	a.resequence()
	a.finalize()
}

// JSON returns deterministic indented JSON with a trailing newline.
func (a *Assessment) JSON() ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("nil assessment")
	}
	out, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Human returns a deterministic report with blocking findings first.
func (a *Assessment) Human() string {
	if a == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "NGINX migration assessment: %s\n", a.Source)
	fmt.Fprintf(&b, "status: %s\n", a.Status)
	fmt.Fprintf(&b, "results: %d supported, %d approximated, %d ignored, %d blocking, %d informational\n",
		a.Summary.Supported, a.Summary.Approximated, a.Summary.Ignored, a.Summary.Blocking, a.Summary.Informational)

	ordered := append([]AssessmentResult(nil), a.Results...)
	sort.SliceStable(ordered, func(i, j int) bool {
		pi, pj := resultPriority(ordered[i].Class), resultPriority(ordered[j].Class)
		if pi != pj {
			return pi < pj
		}
		if ordered[i].Line != ordered[j].Line {
			return ordered[i].Line < ordered[j].Line
		}
		return ordered[i].ID < ordered[j].ID
	})
	last := AssessmentClass("")
	for _, r := range ordered {
		if r.Class != last {
			fmt.Fprintf(&b, "\n%s:\n", strings.ToUpper(string(r.Class)))
			last = r.Class
		}
		where := r.Context
		if r.Line > 0 {
			fmt.Fprintf(&b, "  line %d [%s] %s (%s): %s\n", r.Line, where, r.Directive, r.Code, r.Message)
		} else {
			fmt.Fprintf(&b, "  [%s] %s (%s): %s\n", where, r.Directive, r.Code, r.Message)
		}
	}
	if a.Validation.Status != "not_run" {
		fmt.Fprintf(&b, "\ncandidate validation: %s", a.Validation.Status)
		if len(a.Validation.Errors) > 0 || len(a.Validation.Warnings) > 0 {
			fmt.Fprintf(&b, " (%d error(s), %d warning(s))", len(a.Validation.Errors), len(a.Validation.Warnings))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// HasWarnings reports findings that strict mode treats as non-clean.
func (a *Assessment) HasWarnings() bool {
	return a != nil && (a.Summary.Approximated > 0 || a.Summary.Ignored > 0 || a.Summary.Blocking > 0)
}

// HasBlocking reports whether migration requires manual resolution.
func (a *Assessment) HasBlocking() bool {
	return a != nil && (a.Summary.Blocking > 0 || a.Summary.ParseErrors > 0 || a.Summary.ValidationErrors > 0)
}

func (a *Assessment) resequence() {
	for i := range a.Results {
		a.Results[i].ID = fmt.Sprintf("result-%04d", i+1)
	}
}

func (a *Assessment) finalize() {
	if a == nil {
		return
	}
	a.resequence()
	var s AssessmentSummary
	for _, r := range a.Results {
		s.Total++
		switch r.Class {
		case AssessmentSupported:
			s.Supported++
		case AssessmentApproximated:
			s.Approximated++
		case AssessmentIgnored:
			s.Ignored++
		case AssessmentBlocking:
			s.Blocking++
		case AssessmentInformational:
			s.Informational++
		case AssessmentParseError:
			s.ParseErrors++
		case AssessmentValidationError:
			s.ValidationErrors++
		}
	}
	s.Ready = s.Blocking == 0 && s.ParseErrors == 0 && s.ValidationErrors == 0
	a.Summary = s
	switch {
	case s.ParseErrors > 0:
		a.Status = "parse_error"
	case s.ValidationErrors > 0:
		a.Status = "invalid_candidate"
	case s.Blocking > 0:
		a.Status = "manual_action_required"
	default:
		a.Status = "ready_for_review"
	}
}

func resultPriority(class AssessmentClass) int {
	switch class {
	case AssessmentParseError, AssessmentValidationError, AssessmentBlocking:
		return 0
	case AssessmentApproximated:
		return 1
	case AssessmentIgnored:
		return 2
	case AssessmentSupported:
		return 3
	default:
		return 4
	}
}

func defaultRisk(name string) AssessmentRisk {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "auth"), strings.Contains(lower, "allow"), strings.Contains(lower, "deny"), strings.Contains(lower, "ssl"), strings.Contains(lower, "tls"), strings.Contains(lower, "header"), strings.Contains(lower, "limit"):
		return RiskSecurity
	case strings.Contains(lower, "log"):
		return RiskObservability
	case strings.Contains(lower, "cache"), strings.Contains(lower, "buffer"):
		return RiskPerformance
	default:
		return RiskRouting
	}
}

func assessmentText(s string) string {
	// Canonical field names may be surfaced, but collapse control characters so
	// one finding cannot forge additional report lines.
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
