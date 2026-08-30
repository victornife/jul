// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/migrate/nginx"
)

type importFailWriter struct{}

func (importFailWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestResolveImportInputTable(t *testing.T) {
	tests := []struct {
		name       string
		flagPath   string
		positional []string
		want       string
		wantErr    bool
	}{
		{"flag", "nginx.conf", nil, "nginx.conf", false},
		{"positional", "", []string{"nginx.conf"}, "nginx.conf", false},
		{"both", "flag.conf", []string{"pos.conf"}, "", true},
		{"missing", "", nil, "", true},
		{"too many", "", []string{"a", "b"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveImportInput(tt.flagPath, tt.positional)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("resolveImportInput = (%q,%v), want (%q, error=%v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestValidateImportOutputsTable(t *testing.T) {
	tests := []struct {
		name       string
		out        string
		report     string
		assess     bool
		jsonOut    bool
		wantErrSub string
	}{
		{"ordinary", "out.toml", "", false, false, ""},
		{"report and config", "out.toml", "report.json", false, false, ""},
		{"json and assess", "", "", true, true, "alternative"},
		{"assess with output", "out.toml", "", true, false, "does not write"},
		{"json with report", "", "report.json", false, true, "already writes"},
		{"report stdout", "", "-", false, false, "requires a file"},
		{"same output", "same", "same", false, false, "different paths"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateImportOutputs(tt.out, tt.report, tt.assess, tt.jsonOut)
			if tt.wantErrSub == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErrSub)
			}
		})
	}
}

func TestClassifyImportReadError(t *testing.T) {
	code, class, findingCode, message := classifyImportReadError(&os.PathError{Op: "open", Path: "missing", Err: os.ErrNotExist})
	if code != importExitIO || class != nginx.AssessmentParseError || findingCode != "NGX_INPUT_IO" || message == "" {
		t.Fatalf("unexpected path error classification: %d %q %q %q", code, class, findingCode, message)
	}
	code, class, findingCode, message = classifyImportReadError(errors.New("broken parse"))
	if code != importExitParse || class != nginx.AssessmentParseError || findingCode != "NGX_PARSE_ERROR" || message == "" {
		t.Fatalf("unexpected parse classification: %d %q %q %q", code, class, findingCode, message)
	}
}

func TestImportAssessmentExitTable(t *testing.T) {
	ready := nginx.FailureAssessment("x", nginx.AssessmentInformational, "INFO", "info")
	ready.SetValidation(nil, nil)
	blocking := nginx.FailureAssessment("x", nginx.AssessmentBlocking, "BLOCK", "block")
	invalid := nginx.FailureAssessment("x", nginx.AssessmentInformational, "INFO", "info")
	invalid.SetValidation([]error{errors.New("invalid")}, nil)
	approx := nginx.FailureAssessment("x", nginx.AssessmentApproximated, "APPROX", "approx")
	approx.SetValidation(nil, nil)

	tests := []struct {
		name       string
		assessment *nginx.Assessment
		strict     bool
		warnings   []config.Diagnostic
		want       int
	}{
		{"nil", nil, false, nil, importExitInternal},
		{"ready", ready, false, nil, importExitOK},
		{"blocking", blocking, false, nil, importExitFindings},
		{"invalid", invalid, false, nil, importExitValidation},
		{"approx non-strict", approx, false, nil, importExitOK},
		{"approx strict", approx, true, nil, importExitFindings},
		{"lint strict", ready, true, []config.Diagnostic{{Severity: config.SeverityWarning, Message: "warn"}}, importExitFindings},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := importAssessmentExit(tt.assessment, tt.strict, tt.warnings); got != tt.want {
				t.Fatalf("exit = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestWriteAssessmentFailures(t *testing.T) {
	a := nginx.FailureAssessment("fixture.conf", nginx.AssessmentInformational, "INFO", "info")
	if err := writeAssessment(importFailWriter{}, a); err == nil || !strings.Contains(err.Error(), "write assessment") {
		t.Fatalf("unexpected writeAssessment error: %v", err)
	}
	badPath := filepath.Join(t.TempDir(), "missing", "assessment.json")
	if err := writeAssessmentFile(badPath, a); err == nil || !strings.Contains(err.Error(), "write assessment") {
		t.Fatalf("unexpected writeAssessmentFile error: %v", err)
	}
}

func TestEmitImportAssessmentReportAndNoop(t *testing.T) {
	a := nginx.FailureAssessment("fixture.conf", nginx.AssessmentInformational, "INFO", "info")
	if err := emitImportAssessment(a, false, false, ""); err != nil {
		t.Fatalf("no-op emit failed: %v", err)
	}
	reportPath := filepath.Join(t.TempDir(), "assessment.json")
	if err := emitImportAssessment(a, false, false, reportPath); err != nil {
		t.Fatalf("report emit failed: %v", err)
	}
	if data, err := os.ReadFile(reportPath); err != nil || !strings.Contains(string(data), `"schema_version": 2`) {
		t.Fatalf("report not written: err=%v data=%s", err, data)
	}
}

func TestCmdImportStrictApproximation(t *testing.T) {
	in := writeNginx(t, `http { server { listen 8080; location /static { alias /srv/static; } } }`)
	code, _, _ := capture(t, func() int {
		return cmdImport([]string{"nginx", "--assess", in})
	})
	if code != importExitOK {
		t.Fatalf("non-strict approximation exit = %d, want 0", code)
	}
	code, _, _ = capture(t, func() int {
		return cmdImport([]string{"nginx", "--assess", "--strict", in})
	})
	if code != importExitFindings {
		t.Fatalf("strict approximation exit = %d, want %d", code, importExitFindings)
	}
}

func TestCmdImportOutputAndReportIOFailures(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	missingDir := filepath.Join(t.TempDir(), "missing")
	code, _, _ := capture(t, func() int {
		return cmdImport([]string{"nginx", "--report", filepath.Join(missingDir, "assessment.json"), in})
	})
	if code != importExitIO {
		t.Fatalf("report I/O exit = %d, want %d", code, importExitIO)
	}
	code, _, _ = capture(t, func() int {
		return cmdImport([]string{"nginx", "-o", filepath.Join(missingDir, "jul.toml"), in})
	})
	if code != importExitIO {
		t.Fatalf("config I/O exit = %d, want %d", code, importExitIO)
	}
}

func TestCmdImportUsageConflictMatrix(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	tests := [][]string{
		{"nginx", "--input", in, in},
		{"nginx", "--json", "--assess", in},
		{"nginx", "--json", "--report", filepath.Join(t.TempDir(), "r.json"), in},
		{"nginx", "--report", "-", in},
		{"nginx", "--output", "same", "--report", "same", in},
		{"nginx", in, "extra.conf"},
	}
	for _, args := range tests {
		code, _, _ := capture(t, func() int { return cmdImport(args) })
		if code != importExitUsage {
			t.Errorf("cmdImport(%v) exit = %d, want %d", args, code, importExitUsage)
		}
	}
}

func TestCmdImportHumanAssessmentReady(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	code, out, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--assess", in})
	})
	if code != importExitOK {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "status: ready_for_review") || !strings.Contains(out, "candidate validation: valid") {
		t.Fatalf("human assessment missing ready/validation state:\n%s", out)
	}
}
