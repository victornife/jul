// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/migrate/nginx"
)

func TestCmdImportAssessmentHumanNoWrite(t *testing.T) {
	in := writeNginx(t, `http { server { listen 8080; location / { if ($request_method = POST) { return 403; } } } }`)
	code, out, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--assess", in})
	})
	if code != importExitFindings {
		t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, importExitFindings, out, errOut)
	}
	if !strings.Contains(out, "NGINX migration assessment") || !strings.Contains(out, "BLOCKING:") {
		t.Fatalf("missing human assessment:\n%s", out)
	}
	if strings.Contains(out, "[[servers]]") {
		t.Fatalf("assessment mode mixed TOML into stdout:\n%s", out)
	}
}

func TestCmdImportAssessmentJSONOnly(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	code, out, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", in})
	})
	if code != importExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s\nstdout:\n%s", code, errOut, out)
	}
	var assessment nginx.Assessment
	if err := json.Unmarshal([]byte(out), &assessment); err != nil {
		t.Fatalf("stdout is not assessment JSON: %v\n%s", err, out)
	}
	if assessment.SchemaVersion != nginx.AssessmentSchemaVersion {
		t.Fatalf("schema version = %d", assessment.SchemaVersion)
	}
	if strings.Contains(out, "[[servers]]") {
		t.Fatalf("JSON output mixed TOML into stdout:\n%s", out)
	}
}

func TestCmdImportReportAndGeneratedConfig(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "jul.toml")
	reportPath := filepath.Join(dir, "assessment.json")
	code, _, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--input", in, "--output", outPath, "--report", reportPath})
	})
	if code != importExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, errOut)
	}
	generated, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(generated)
	if err != nil {
		t.Fatalf("generated config does not parse: %v", err)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("generated config does not validate: %v", err)
	}
	reportData, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var assessment nginx.Assessment
	if err := json.Unmarshal(reportData, &assessment); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	if assessment.Validation.Status != "valid" {
		t.Fatalf("candidate validation = %q", assessment.Validation.Status)
	}
}

func TestCmdImportReportBlockingStillWritesCandidate(t *testing.T) {
	in := writeNginx(t, `http { server { listen 8080; location / { if ($x) { return 403; } } } }`)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "jul.toml")
	reportPath := filepath.Join(dir, "assessment.json")
	code, _, _ := capture(t, func() int {
		return cmdImport([]string{"nginx", "-o", outPath, "--report", reportPath, in})
	})
	if code != importExitFindings {
		t.Fatalf("exit = %d, want %d", code, importExitFindings)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("generated candidate was not written: %v", err)
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("assessment report was not written: %v", err)
	}
}

func TestCmdImportAssessmentOutputConflicts(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	code, _, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", "-o", filepath.Join(t.TempDir(), "out.toml"), in})
	})
	if code != importExitUsage {
		t.Fatalf("exit = %d, want %d", code, importExitUsage)
	}
	if !strings.Contains(errOut, "assessment-only mode") {
		t.Fatalf("missing conflict explanation:\n%s", errOut)
	}
}

func TestCmdImportAssessmentParseAndIOExitCodes(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.conf")
	code, _, _ := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", missing})
	})
	if code != importExitIO {
		t.Fatalf("missing-file exit = %d, want %d", code, importExitIO)
	}

	broken := writeNginx(t, `http { server { listen 8080;`)
	code, out, _ := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", broken})
	})
	if code != importExitParse {
		t.Fatalf("parse exit = %d, want %d\n%s", code, importExitParse, out)
	}
	var assessment nginx.Assessment
	if err := json.Unmarshal([]byte(out), &assessment); err != nil {
		t.Fatalf("parse-error output is not JSON: %v\n%s", err, out)
	}
	if assessment.Status != "parse_error" {
		t.Fatalf("status = %q", assessment.Status)
	}
}

func TestCmdImportAssessmentDoesNotEchoSecret(t *testing.T) {
	const secret = "MIGRATION-SECRET-987654"
	in := writeNginx(t, `http { server { listen 8080; location / { proxy_set_header Authorization "Bearer MIGRATION-SECRET-987654"; } } }`)
	code, out, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", in})
	})
	if code != importExitFindings {
		t.Fatalf("exit = %d, want %d", code, importExitFindings)
	}
	if strings.Contains(out, secret) || strings.Contains(errOut, secret) {
		t.Fatalf("assessment leaked secret\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
}
