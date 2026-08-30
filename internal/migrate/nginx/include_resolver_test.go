// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestImportIncludesExplicitNestedAndProvenance(t *testing.T) {
	root := t.TempDir()
	writeIncludeFixture(t, root, "nginx.conf", `http {
    include conf.d/http.conf;
}
`)
	writeIncludeFixture(t, root, "conf.d/http.conf", `upstream app {
    server 127.0.0.1:9000;
}
server {
    listen 8080;
    include extra/server.conf;
    location / {
        proxy_pass http://app;
    }
}
`)
	writeIncludeFixture(t, root, "conf.d/extra/server.conf", `server_name example.test;
`)

	cfg, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
		FollowIncludes: true,
		IncludeRoot:    root,
	})
	if err != nil {
		t.Fatalf("ImportFileWithImportOptions: %v", err)
	}
	if cfg == nil || report == nil || report.Assessment == nil {
		t.Fatal("missing translated config/report/assessment")
	}
	assessment := report.Assessment
	if !assessment.SourcePolicy.Complete || !assessment.SourcePolicy.FollowInclude {
		t.Fatalf("source policy = %+v", assessment.SourcePolicy)
	}
	if assessment.SourcePolicy.FilesRead != 3 || len(assessment.Sources) != 3 {
		t.Fatalf("sources = %+v, policy=%+v", assessment.Sources, assessment.SourcePolicy)
	}
	if assessment.Sources[1].DisplayPath != "conf.d/http.conf" || assessment.Sources[1].ParentID != assessment.Sources[0].ID {
		t.Fatalf("first include source = %+v", assessment.Sources[1])
	}
	if assessment.Sources[2].DisplayPath != "conf.d/extra/server.conf" || assessment.Sources[2].ParentID != assessment.Sources[1].ID {
		t.Fatalf("nested include source = %+v", assessment.Sources[2])
	}
	if report.Servers != 1 || report.Upstreams != 1 || report.Locations != 1 {
		t.Fatalf("translation counts = servers:%d upstreams:%d locations:%d", report.Servers, report.Upstreams, report.Locations)
	}
	if countAssessmentCode(assessment, "NGX_INCLUDE_RESOLVED") != 2 {
		t.Fatalf("include results = %+v", assessment.Results)
	}
	serverName := findAssessmentDirective(assessment, "server_name")
	if serverName == nil || serverName.Provenance == nil || serverName.Provenance.DisplayPath != "conf.d/extra/server.conf" {
		t.Fatalf("server_name provenance = %+v", serverName)
	}
	if assessment.HasBlocking() {
		t.Fatalf("unexpected blocking results: %+v", assessment.Results)
	}
}

func TestImportIncludesDisabledAndMissingAreBlocking(t *testing.T) {
	root := t.TempDir()
	writeIncludeFixture(t, root, "nginx.conf", "include conf.d/missing.conf;\n")

	_, disabledReport, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{IncludeRoot: root})
	if err != nil {
		t.Fatalf("disabled import: %v", err)
	}
	if disabledReport.Assessment.SourcePolicy.Complete || countAssessmentCode(disabledReport.Assessment, "NGX_INCLUDE_DISABLED") != 1 {
		t.Fatalf("disabled assessment = %+v", disabledReport.Assessment)
	}

	_, missingReport, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
		FollowIncludes: true,
		IncludeRoot:    root,
	})
	if err != nil {
		t.Fatalf("missing import: %v", err)
	}
	if missingReport.Assessment.SourcePolicy.Complete || countAssessmentCode(missingReport.Assessment, "NGX_INCLUDE_MISSING") != 1 {
		t.Fatalf("missing assessment = %+v", missingReport.Assessment)
	}
}

func TestImportIncludeGlobOrderAndRepeatedInstances(t *testing.T) {
	root := t.TempDir()
	writeIncludeFixture(t, root, "nginx.conf", `http {
    include conf.d/*.conf;
    include conf.d/a.conf;
}
`)
	writeIncludeFixture(t, root, "conf.d/b.conf", "gzip on;\n")
	writeIncludeFixture(t, root, "conf.d/a.conf", "gzip off;\n")
	writeIncludeFixture(t, root, "conf.d/.hidden.conf", "gzip on;\n")

	_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
		FollowIncludes: true,
		IncludeRoot:    root,
	})
	if err != nil {
		t.Fatalf("glob import: %v", err)
	}
	got := make([]string, 0, len(report.Assessment.Sources))
	for _, source := range report.Assessment.Sources {
		got = append(got, source.DisplayPath)
	}
	want := []string{"nginx.conf", "conf.d/a.conf", "conf.d/b.conf", "conf.d/a.conf"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("source order = %#v, want %#v", got, want)
	}
	if report.Assessment.Sources[1].ID == report.Assessment.Sources[3].ID {
		t.Fatal("repeated include reused a source instance ID")
	}
}

func TestImportIncludeCycleAndLimits(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		root := t.TempDir()
		writeIncludeFixture(t, root, "nginx.conf", "include a.conf;\n")
		writeIncludeFixture(t, root, "a.conf", "include b.conf;\n")
		writeIncludeFixture(t, root, "b.conf", "include a.conf;\n")
		_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{FollowIncludes: true, IncludeRoot: root})
		if err != nil {
			t.Fatalf("cycle import: %v", err)
		}
		if countAssessmentCode(report.Assessment, "NGX_INCLUDE_CYCLE") != 1 || report.Assessment.SourcePolicy.Complete {
			t.Fatalf("cycle assessment = %+v", report.Assessment)
		}
	})

	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		writeIncludeFixture(t, root, "nginx.conf", "include a.conf;\n")
		writeIncludeFixture(t, root, "a.conf", "include b.conf;\n")
		writeIncludeFixture(t, root, "b.conf", "gzip on;\n")
		_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
			FollowIncludes: true,
			IncludeRoot:    root,
			IncludeLimits:  IncludeLimits{MaxDepth: 1},
		})
		if err != nil {
			t.Fatalf("depth import: %v", err)
		}
		if countAssessmentCode(report.Assessment, "NGX_INCLUDE_DEPTH_LIMIT") != 1 {
			t.Fatalf("depth assessment = %+v", report.Assessment)
		}
	})

	t.Run("file count", func(t *testing.T) {
		root := t.TempDir()
		writeIncludeFixture(t, root, "nginx.conf", "include conf.d/*.conf;\n")
		writeIncludeFixture(t, root, "conf.d/a.conf", "gzip on;\n")
		writeIncludeFixture(t, root, "conf.d/b.conf", "gzip on;\n")
		_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
			FollowIncludes: true,
			IncludeRoot:    root,
			IncludeLimits:  IncludeLimits{MaxFiles: 2},
		})
		if err != nil {
			t.Fatalf("file-count import: %v", err)
		}
		if countAssessmentCode(report.Assessment, "NGX_INCLUDE_FILE_LIMIT") != 1 {
			t.Fatalf("file-count assessment = %+v", report.Assessment)
		}
	})

	t.Run("bytes", func(t *testing.T) {
		root := t.TempDir()
		writeIncludeFixture(t, root, "nginx.conf", "include a.conf;\n")
		writeIncludeFixture(t, root, "a.conf", strings.Repeat("#", 64)+"\n")
		_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
			FollowIncludes: true,
			IncludeRoot:    root,
			IncludeLimits:  IncludeLimits{MaxFileBytes: 16},
		})
		if err != nil {
			t.Fatalf("byte-limit import: %v", err)
		}
		if countAssessmentCode(report.Assessment, "NGX_INCLUDE_BYTE_LIMIT") != 1 {
			t.Fatalf("byte-limit assessment = %+v", report.Assessment)
		}
	})
}

func TestImportIncludeRootAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeIncludeFixture(t, outside, "outside.conf", "gzip on;\n")
	writeIncludeFixture(t, root, "nginx.conf", "include ../outside.conf;\n")

	_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{FollowIncludes: true, IncludeRoot: root})
	if err != nil {
		t.Fatalf("root-escape import: %v", err)
	}
	if countAssessmentCode(report.Assessment, "NGX_INCLUDE_ROOT_ESCAPE") != 1 {
		t.Fatalf("root-escape assessment = %+v", report.Assessment)
	}

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows CI")
	}
	writeIncludeFixture(t, root, "nginx.conf", "include linked.conf;\n")
	if err := os.Symlink(filepath.Join(outside, "outside.conf"), filepath.Join(root, "linked.conf")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, report, err = ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{FollowIncludes: true, IncludeRoot: root})
	if err != nil {
		t.Fatalf("symlink import: %v", err)
	}
	if countAssessmentCode(report.Assessment, "NGX_INCLUDE_SYMLINK_ESCAPE") != 1 {
		t.Fatalf("symlink assessment = %+v", report.Assessment)
	}
}

func TestImportIncludeParseErrorAndSecretRedaction(t *testing.T) {
	root := t.TempDir()
	writeIncludeFixture(t, root, "nginx.conf", `http {
    include secret.conf;
    include broken.conf;
}
`)
	secret := "super-secret-token"
	writeIncludeFixture(t, root, "secret.conf", "proxy_set_header Authorization "+secret+";\n")
	writeIncludeFixture(t, root, "broken.conf", "server {\n")

	_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{FollowIncludes: true, IncludeRoot: root})
	if err != nil {
		t.Fatalf("parse-error import: %v", err)
	}
	if countAssessmentCode(report.Assessment, "NGX_INCLUDE_PARSE_ERROR") != 1 {
		t.Fatalf("parse-error assessment = %+v", report.Assessment)
	}
	jsonReport, err := report.Assessment.JSON()
	if err != nil {
		t.Fatalf("assessment JSON: %v", err)
	}
	if strings.Contains(string(jsonReport), secret) {
		t.Fatalf("assessment leaked fixture secret: %s", jsonReport)
	}
}

func writeIncludeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func countAssessmentCode(assessment *Assessment, code string) int {
	if assessment == nil {
		return 0
	}
	count := 0
	for _, result := range assessment.Results {
		if result.Code == code {
			count++
		}
	}
	return count
}

func findAssessmentDirective(assessment *Assessment, name string) *AssessmentResult {
	if assessment == nil {
		return nil
	}
	for i := range assessment.Results {
		if assessment.Results[i].Directive == name {
			return &assessment.Results[i]
		}
	}
	return nil
}
