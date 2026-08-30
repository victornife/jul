// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIncludeGlobRejectsEscapingDirectorySymlinkBeforeRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows CI")
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeIncludeFixture(t, outside, "secret.conf", "gzip on;\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeIncludeFixture(t, root, "nginx.conf", `http {
    include linked/*.conf;
    server { listen 8080; location / { return 204; } }
}
`)

	_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
		FollowIncludes: true,
		IncludeRoot:    root,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if countAssessmentCode(report.Assessment, "NGX_INCLUDE_SYMLINK_ESCAPE") != 1 {
		t.Fatalf("assessment did not reject symlinked glob directory: %+v", report.Assessment.Results)
	}
	if report.Assessment.SourcePolicy.FilesRead != 1 {
		t.Fatalf("escaping directory was read: policy=%+v", report.Assessment.SourcePolicy)
	}
}

func TestIncludeGlobRejectsWildcardSelectedEscapingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows CI")
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeIncludeFixture(t, outside, "secret.conf", "gzip on;\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeIncludeFixture(t, root, "nginx.conf", `http {
    include */*.conf;
    server { listen 8080; location / { return 204; } }
}
`)

	_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
		FollowIncludes: true,
		IncludeRoot:    root,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if countAssessmentCode(report.Assessment, "NGX_INCLUDE_SYMLINK_ESCAPE") != 1 {
		t.Fatalf("assessment did not reject wildcard-selected symlink: %+v", report.Assessment.Results)
	}
}

func TestIncludeGlobAllowsSymlinkedDirectoryInsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows CI")
	}
	root := t.TempDir()
	writeIncludeFixture(t, root, "conf.d/site.conf", `server {
    listen 8080;
    location / { return 204; }
}
`)
	if err := os.Symlink(filepath.Join(root, "conf.d"), filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeIncludeFixture(t, root, "nginx.conf", `http {
    include alias/*.conf;
}
`)

	_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
		FollowIncludes: true,
		IncludeRoot:    root,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !report.Assessment.SourcePolicy.Complete || countAssessmentCode(report.Assessment, "NGX_INCLUDE_RESOLVED") != 1 {
		t.Fatalf("safe in-root symlink was not resolved: %+v", report.Assessment)
	}
	if len(report.Assessment.Sources) != 2 || report.Assessment.Sources[1].DisplayPath != "alias/site.conf" {
		t.Fatalf("unexpected lexical source identity: %+v", report.Assessment.Sources)
	}
}

func TestIncludeTotalByteLimitCountsRootSource(t *testing.T) {
	root := t.TempDir()
	writeIncludeFixture(t, root, "nginx.conf", `http {
    server { listen 8080; location / { return 204; } }
}
`)

	_, err := resolveSourceTree(filepath.Join(root, "nginx.conf"), ImportOptions{
		FollowIncludes: true,
		IncludeRoot:    root,
		IncludeLimits: IncludeLimits{
			MaxFileBytes:  1024,
			MaxTotalBytes: 8,
		},
	})
	var traversalErr *includeTraversalError
	if !errors.As(err, &traversalErr) || traversalErr.Code != "NGX_INCLUDE_BYTE_LIMIT" {
		t.Fatalf("root total-byte error = %v", err)
	}
}

func TestIncludeFailureLegacyReportOrderIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeIncludeFixture(t, root, "nginx.conf", `http {
    include z.conf;
    include a.conf;
    server { listen 8080; location / { return 204; } }
}
`)

	var first string
	for run := 0; run < 5; run++ {
		_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
			FollowIncludes: true,
			IncludeRoot:    root,
		})
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		parts := make([]string, 0, len(report.Skipped))
		for _, finding := range report.Skipped {
			if finding.Name == "include" {
				parts = append(parts, strings.Join([]string{finding.Name, finding.Reason}, ":"))
			}
		}
		got := strings.Join(parts, "|")
		if run == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("run %d legacy report order = %q, want %q", run, got, first)
		}
	}
}
