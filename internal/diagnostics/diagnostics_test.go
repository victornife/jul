// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	t.Parallel()
	got := Summarize([]Result{
		{Status: StatusPass},
		{Status: StatusWarning},
		{Status: StatusSkipped},
		{Status: StatusError},
	})
	if got.Status != StatusError || got.Passed != 1 || got.Warnings != 1 || got.Errors != 1 || got.Skipped != 1 {
		t.Fatalf("unexpected summary: %#v", got)
	}
	if got := Summarize([]Result{{Status: StatusPass}, {Status: StatusWarning}}); got.Status != StatusWarning {
		t.Fatalf("warning summary status = %q", got.Status)
	}
	if got := Summarize(nil); got.Status != StatusPass {
		t.Fatalf("empty summary status = %q", got.Status)
	}
}

func TestSanitizeStringAndEvidence(t *testing.T) {
	t.Parallel()
	secret := "fixture-super-secret"
	input := "Authorization: Bearer " + secret + " https://user:" + secret + "@example.test/?token=" + secret + " password=" + secret + "\n-----BEGIN PRIVATE KEY-----\n" + secret + "\n-----END PRIVATE KEY-----"
	got := SanitizeResult(Result{
		Message: input,
		Evidence: map[string]any{
			"token": secret,
			"nested": map[string]any{
				"password": secret,
				"safe":     "cookie=" + secret,
			},
			"slice": []any{"api_key=" + secret, errors.New("Bearer " + secret)},
		},
	})
	encoded := got.Message + " " + stringify(got.Evidence)
	if strings.Contains(encoded, secret) {
		t.Fatalf("secret survived sanitization: %s", encoded)
	}
	if !strings.Contains(encoded, redacted) {
		t.Fatalf("redaction marker missing: %s", encoded)
	}
}

func TestSanitizeStringBoundsOutput(t *testing.T) {
	t.Parallel()
	got := SanitizeString(strings.Repeat("x", maxResultString+25))
	if len(got) <= maxResultString || !strings.Contains(got, "truncated 25 bytes") {
		t.Fatalf("unexpected bounded output: len=%d tail=%q", len(got), got[len(got)-30:])
	}
}

func TestRunnerPreservesOrderAndNormalizes(t *testing.T) {
	t.Parallel()
	checks := []Check{
		CheckFunc{Metadata: Spec{Code: "FIRST", Phase: "one"}, Fn: func(context.Context) Result {
			return Result{Status: StatusPass, Message: "ok"}
		}},
		CheckFunc{Metadata: Spec{Code: "SECOND", Phase: "two"}, Fn: func(context.Context) Result {
			return Result{Status: "bogus", Message: "bad"}
		}},
	}
	report := (Runner{}).Run(context.Background(), "local", "server.toml", checks)
	if len(report.Checks) != 2 || report.Checks[0].Code != "FIRST" || report.Checks[1].Code != "SECOND" {
		t.Fatalf("registry order not preserved: %#v", report.Checks)
	}
	if report.Checks[1].Status != StatusError || report.Summary.Errors != 1 {
		t.Fatalf("invalid status not normalized: %#v", report)
	}
}

func TestRunnerConvertsPanicAndCooperativeTimeout(t *testing.T) {
	t.Parallel()
	checks := []Check{
		CheckFunc{Metadata: Spec{Code: "PANIC", Phase: "test"}, Fn: func(context.Context) Result {
			panic("token=panic-secret")
		}},
		CheckFunc{Metadata: Spec{Code: "TIMEOUT", Phase: "test", Timeout: time.Millisecond}, Fn: func(ctx context.Context) Result {
			<-ctx.Done()
			return Result{Status: StatusPass, Message: "late"}
		}},
	}
	report := (Runner{}).Run(context.Background(), "local", "", checks)
	if report.Summary.Errors != 2 {
		t.Fatalf("errors = %d, report=%#v", report.Summary.Errors, report)
	}
	if strings.Contains(stringify(report.Checks[0].Evidence), "panic-secret") {
		t.Fatalf("panic detail was not sanitized: %#v", report.Checks[0])
	}
	if report.Checks[1].Status != StatusError {
		t.Fatalf("timeout status = %q", report.Checks[1].Status)
	}
}

func TestRunnerHandlesCanceledParentAndNilFunction(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := (Runner{}).Run(ctx, "local", "", []Check{
		CheckFunc{Metadata: Spec{Code: "CANCEL", Phase: "test"}},
	})
	if report.Checks[0].Status != StatusError || report.Checks[0].Code != "CANCEL" {
		t.Fatalf("unexpected canceled result: %#v", report.Checks[0])
	}

	report = (Runner{}).Run(context.Background(), "local", "", []Check{
		CheckFunc{Metadata: Spec{Code: "NIL", Phase: "test"}},
	})
	if report.Checks[0].Status != StatusError {
		t.Fatalf("nil function status = %q", report.Checks[0].Status)
	}
}

func TestSanitizeEvidenceShapesAndDefaults(t *testing.T) {
	t.Parallel()
	secret := "shape-secret"
	result := SanitizeResult(Result{Evidence: map[string]any{
		"strings": []string{"safe", "password=" + secret},
		"map": map[string]string{
			"safe":       "Bearer " + secret,
			"credential": secret,
		},
		"number": 7,
		"nil":    nil,
	}})
	if strings.Contains(fmt.Sprint(result.Evidence), secret) {
		t.Fatalf("secret survived shape sanitization: %#v", result.Evidence)
	}
	if result.Evidence["number"] != 7 || result.Evidence["nil"] != nil {
		t.Fatalf("non-string evidence changed: %#v", result.Evidence)
	}

	normalized := normalizeResult(Spec{Code: "DEFAULT", Phase: "phase", Docs: "docs", Severity: SeverityWarning}, Result{})
	if normalized.Code != "DEFAULT" || normalized.Phase != "phase" || normalized.Docs != "docs" || normalized.Status != StatusError || normalized.Severity != SeverityWarning {
		t.Fatalf("unexpected defaults: %#v", normalized)
	}
	if got := defaultSeverity(StatusPass, ""); got != SeverityInfo {
		t.Fatalf("pass default severity = %q", got)
	}
	if got := defaultSeverity(StatusWarning, ""); got != SeverityWarning {
		t.Fatalf("warning default severity = %q", got)
	}
	if got := defaultSeverity(StatusError, ""); got != SeverityError {
		t.Fatalf("error default severity = %q", got)
	}
}

func stringify(value any) string { return fmt.Sprint(value) }
