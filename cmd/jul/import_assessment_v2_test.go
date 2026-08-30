// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/migrate/nginx"
)

func TestCmdImportAssessmentSchemaV2RelativePaths(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	code, out, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", "--path-style", "relative", in})
	})
	if code != importExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s\nstdout:\n%s", code, errOut, out)
	}
	var assessment nginx.Assessment
	if err := json.Unmarshal([]byte(out), &assessment); err != nil {
		t.Fatalf("decode assessment: %v\n%s", err, out)
	}
	if assessment.SchemaVersion != 2 || assessment.SourcePolicy.PathStyle != nginx.AssessmentPathRelative {
		t.Fatalf("unexpected v2 contract: schema=%d policy=%+v", assessment.SchemaVersion, assessment.SourcePolicy)
	}
	if filepath.IsAbs(filepath.FromSlash(assessment.Source)) || len(assessment.Sources) != 1 {
		t.Fatalf("relative source metadata is not shareable: source=%q sources=%+v", assessment.Source, assessment.Sources)
	}
	for _, result := range assessment.Results {
		if !result.Synthetic && result.Provenance == nil {
			t.Fatalf("parsed result lacks provenance: %+v", result)
		}
	}
}

func TestCmdImportAssessmentAbsolutePathsRequireExplicitFlag(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	code, out, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", "--path-style=absolute", in})
	})
	if code != importExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s\nstdout:\n%s", code, errOut, out)
	}
	var assessment nginx.Assessment
	if err := json.Unmarshal([]byte(out), &assessment); err != nil {
		t.Fatal(err)
	}
	if assessment.SourcePolicy.PathStyle != nginx.AssessmentPathAbsolute || !filepath.IsAbs(filepath.FromSlash(assessment.Source)) {
		t.Fatalf("absolute path mode not applied: source=%q policy=%+v", assessment.Source, assessment.SourcePolicy)
	}
}

func TestCmdImportAssessmentSourceOrder(t *testing.T) {
	in := writeNginx(t, `http { server { listen 8080; location / { alias /srv; if ($x) { return 403; } } } }`)
	code, out, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--assess", "--source-order", in})
	})
	if code != importExitFindings {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s\nstdout:\n%s", code, importExitFindings, errOut, out)
	}
	if !strings.Contains(out, "SOURCE ORDER:") || !strings.Contains(out, "guidance:") {
		t.Fatalf("source-order assessment is incomplete:\n%s", out)
	}
}

func TestCmdImportAssessmentRejectsInvalidPathStyle(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	code, _, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", "--path-style", "portable-ish", in})
	})
	if code != importExitUsage || !strings.Contains(errOut, "relative or absolute") {
		t.Fatalf("invalid path style was not rejected: exit=%d stderr=%q", code, errOut)
	}
}

func TestCmdImportAssessmentRejectsSourceOrderWithoutHumanMode(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	code, _, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", "--source-order", in})
	})
	if code != importExitUsage || !strings.Contains(errOut, "requires --assess") {
		t.Fatalf("source-order misuse was not rejected: exit=%d stderr=%q", code, errOut)
	}
}
