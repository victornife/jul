// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package diagnostics defines the shared, secret-safe result model used by
// operator diagnostics. It deliberately contains no configuration or runtime
// business logic: doctor checks and support-bundle collectors depend on this
// package, never the other way around.
package diagnostics

import (
	"context"
	"strings"
	"time"
)

// Status is the outcome of one diagnostic check.
type Status string

const (
	StatusPass    Status = "pass"
	StatusWarning Status = "warning"
	StatusError   Status = "error"
	StatusSkipped Status = "skipped"
)

// Severity describes the operational importance of a result.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Result is the stable machine-readable result of one check. Codes, statuses,
// severities and evidence keys are machine contract; human messages and
// remediation text may improve additively.
type Result struct {
	Code        string         `json:"code"`
	Phase       string         `json:"phase"`
	Status      Status         `json:"status"`
	Severity    Severity       `json:"severity"`
	Message     string         `json:"message"`
	Evidence    map[string]any `json:"evidence,omitempty"`
	Remediation string         `json:"remediation,omitempty"`
	Docs        string         `json:"docs,omitempty"`
}

// Summary is the bounded aggregate shown to humans and automation.
type Summary struct {
	Status   Status `json:"status"`
	Passed   int    `json:"passed"`
	Warnings int    `json:"warnings"`
	Errors   int    `json:"errors"`
	Skipped  int    `json:"skipped"`
}

// Report is the versioned result envelope shared by jul doctor and the support
// bundle. Checks remain in registry order so repeated runs are deterministic.
type Report struct {
	SchemaVersion int      `json:"schema_version"`
	Scope         string   `json:"scope"`
	Source        string   `json:"source,omitempty"`
	Summary       Summary  `json:"summary"`
	Checks        []Result `json:"checks"`
}

// Spec is immutable metadata for one registered check.
type Spec struct {
	Code     string
	Phase    string
	Timeout  time.Duration
	Docs     string
	Severity Severity
}

// Check is the closed execution seam consumed by Runner. Production registries
// are fixed slices; this interface exists to keep checks independently testable,
// not to provide dynamic plugins or command execution.
type Check interface {
	Spec() Spec
	Run(context.Context) Result
}

// CheckFunc adapts a function into a Check.
type CheckFunc struct {
	Metadata Spec
	Fn       func(context.Context) Result
}

// Spec returns the check metadata.
func (c CheckFunc) Spec() Spec { return c.Metadata }

// Run executes the adapted function.
func (c CheckFunc) Run(ctx context.Context) Result {
	if c.Fn == nil {
		return Result{
			Status:   StatusError,
			Severity: SeverityError,
			Message:  "diagnostic check has no implementation",
		}
	}
	return c.Fn(ctx)
}

// Summarize computes a deterministic aggregate over results.
func Summarize(results []Result) Summary {
	out := Summary{Status: StatusPass}
	for _, result := range results {
		switch result.Status {
		case StatusPass:
			out.Passed++
		case StatusWarning:
			out.Warnings++
		case StatusError:
			out.Errors++
		case StatusSkipped:
			out.Skipped++
		default:
			out.Errors++
		}
	}
	switch {
	case out.Errors > 0:
		out.Status = StatusError
	case out.Warnings > 0:
		out.Status = StatusWarning
	default:
		out.Status = StatusPass
	}
	return out
}

func normalizeResult(spec Spec, result Result) Result {
	if strings.TrimSpace(result.Code) == "" {
		result.Code = spec.Code
	}
	if strings.TrimSpace(result.Phase) == "" {
		result.Phase = spec.Phase
	}
	if strings.TrimSpace(result.Docs) == "" {
		result.Docs = spec.Docs
	}
	if result.Status == "" {
		result.Status = StatusError
	}
	if result.Severity == "" {
		result.Severity = defaultSeverity(result.Status, spec.Severity)
	}
	if strings.TrimSpace(result.Message) == "" {
		result.Message = "diagnostic check returned no message"
	}
	if !validStatus(result.Status) {
		result.Status = StatusError
		result.Severity = SeverityError
		result.Message = "diagnostic check returned an invalid status"
	}
	if !validSeverity(result.Severity) {
		result.Severity = defaultSeverity(result.Status, spec.Severity)
	}
	return result
}

func defaultSeverity(status Status, configured Severity) Severity {
	if validSeverity(configured) {
		return configured
	}
	switch status {
	case StatusError:
		return SeverityError
	case StatusWarning:
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

func validStatus(status Status) bool {
	switch status {
	case StatusPass, StatusWarning, StatusError, StatusSkipped:
		return true
	default:
		return false
	}
}

func validSeverity(severity Severity) bool {
	switch severity {
	case SeverityInfo, SeverityWarning, SeverityError:
		return true
	default:
		return false
	}
}
