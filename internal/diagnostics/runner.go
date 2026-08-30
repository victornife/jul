// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package diagnostics

import (
	"context"
	"fmt"
	"time"
)

const defaultPerCheckTimeout = 5 * time.Second

// Runner executes a fixed registry in order. Timeouts are cooperative: checks
// receive a deadline-bearing context and must use context-aware APIs. This avoids
// abandoning goroutines merely to manufacture a timeout result.
type Runner struct {
	PerCheckTimeout time.Duration
}

// Run executes checks in the supplied order and returns a sanitized report.
func (runner Runner) Run(ctx context.Context, scope, source string, checks []Check) Report {
	results := make([]Result, 0, len(checks))
	for _, check := range checks {
		spec := check.Spec()
		if err := ctx.Err(); err != nil {
			result := Result{
				Code:        spec.Code,
				Phase:       spec.Phase,
				Status:      StatusError,
				Severity:    SeverityError,
				Message:     "diagnostic run canceled before this check could execute",
				Evidence:    map[string]any{"reason": err.Error()},
				Remediation: "rerun the command with a larger total timeout after resolving any slow prerequisite",
				Docs:        spec.Docs,
			}
			results = append(results, SanitizeResult(result))
			continue
		}
		results = append(results, runner.runOne(ctx, check))
	}
	return Report{
		SchemaVersion: 1,
		Scope:         scope,
		Source:        SanitizeString(source),
		Checks:        results,
		Summary:       Summarize(results),
	}
}

func (runner Runner) runOne(parent context.Context, check Check) (result Result) {
	spec := check.Spec()
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = runner.PerCheckTimeout
	}
	if timeout <= 0 {
		timeout = defaultPerCheckTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	defer func() {
		if recovered := recover(); recovered != nil {
			result = Result{
				Code:        spec.Code,
				Phase:       spec.Phase,
				Status:      StatusError,
				Severity:    SeverityError,
				Message:     "diagnostic check panicked",
				Evidence:    map[string]any{"panic": fmt.Sprint(recovered)},
				Remediation: "report this as an internal Jul diagnostic defect",
				Docs:        spec.Docs,
			}
		}
		result = SanitizeResult(normalizeResult(spec, result))
	}()

	result = check.Run(ctx)
	if err := ctx.Err(); err != nil && result.Status != StatusError {
		result = Result{
			Code:        spec.Code,
			Phase:       spec.Phase,
			Status:      StatusError,
			Severity:    SeverityError,
			Message:     "diagnostic check exceeded its time or cancellation bound",
			Evidence:    map[string]any{"reason": err.Error()},
			Remediation: "resolve the slow dependency or rerun with a larger per-check timeout",
			Docs:        spec.Docs,
		}
	}
	return result
}
