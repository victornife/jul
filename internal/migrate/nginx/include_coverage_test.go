// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

func TestIncludeLimitNormalization(t *testing.T) {
	defaults := DefaultIncludeLimits()
	if defaults.MaxDepth != defaultMaxIncludeDepth || defaults.MaxFiles != defaultMaxIncludeFiles || defaults.MaxFileBytes != defaultMaxIncludeFileBytes || defaults.MaxTotalBytes != defaultMaxIncludeTotalBytes || defaults.MaxGlobMatches != defaultMaxIncludeGlobMatches {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if got := normalizedIncludeLimits(IncludeLimits{}); got != defaults {
		t.Fatalf("zero limits = %+v, want %+v", got, defaults)
	}
	custom := IncludeLimits{MaxDepth: 1, MaxFiles: 2, MaxFileBytes: 3, MaxTotalBytes: 4, MaxGlobMatches: 5}
	if got := normalizedIncludeLimits(custom); got != custom {
		t.Fatalf("custom limits = %+v, want %+v", got, custom)
	}
}

func TestIncludeRootAndPathHelpers(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "nginx.conf")
	if err := os.WriteFile(file, []byte("events {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lexical, evaluated, err := resolveIncludeRoots(file, "")
	if err != nil || lexical == "" || evaluated == "" {
		t.Fatalf("default roots = %q %q %v", lexical, evaluated, err)
	}
	if !pathWithinRoot(lexical, file) || pathWithinRoot(lexical, filepath.Join(filepath.Dir(lexical), "outside.conf")) {
		t.Fatal("pathWithinRoot returned an unsafe result")
	}
	if _, _, err := resolveIncludeRoots(file, filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing root unexpectedly accepted")
	}
	if _, _, err := resolveIncludeRoots(file, file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file root error = %v", err)
	}
}

func TestReadBoundedSourceAndParseSourceBytes(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "source.conf")
	if err := os.WriteFile(file, []byte("gzip on;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readBoundedSource(file, 64)
	if err != nil || string(data) != "gzip on;\n" {
		t.Fatalf("bounded read = %q, %v", data, err)
	}
	if _, err := readBoundedSource(file, 2); err == nil {
		t.Fatal("oversized source unexpectedly accepted")
	}
	if _, err := readBoundedSource(root, 64); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory read error = %v", err)
	}
	cfg, err := parseSourceBytes([]byte("events {}\n"), file)
	if err != nil || cfg == nil || cfg.FilePath != file {
		t.Fatalf("parsed config = %+v, %v", cfg, err)
	}
	if _, err := parseSourceBytes([]byte("server {\n"), file); err == nil {
		t.Fatal("invalid source unexpectedly parsed")
	}
}

func TestIncludeTraversalErrorAndClassification(t *testing.T) {
	var nilErr *includeTraversalError
	if nilErr.Error() != "include traversal failed" {
		t.Fatalf("nil traversal error = %q", nilErr.Error())
	}
	limitErr := &includeTraversalError{Code: "NGX_INCLUDE_FILE_LIMIT", Message: "limit"}
	if limitErr.Error() != "limit" {
		t.Fatalf("traversal error = %q", limitErr.Error())
	}

	tests := []struct {
		name    string
		err     error
		wantCode string
	}{
		{"typed", limitErr, "NGX_INCLUDE_FILE_LIMIT"},
		{"missing", os.ErrNotExist, "NGX_INCLUDE_MISSING"},
		{"path", &os.PathError{Op: "open", Path: "x", Err: os.ErrPermission}, "NGX_INCLUDE_UNREADABLE"},
		{"parse", errors.New("bad parse"), "NGX_INCLUDE_PARSE_ERROR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, message := classifyIncludeReadError(tc.err)
			if code != tc.wantCode || message == "" {
				t.Fatalf("classification = %q %q, want %q", code, message, tc.wantCode)
			}
		})
	}
}

func TestIncludeResolverAdditionalFailures(t *testing.T) {
	tests := []struct {
		name    string
		include string
		limits  IncludeLimits
		setup   func(t *testing.T, root string)
		want    string
	}{
		{name: "empty", include: "include ;\n", want: "NGX_INCLUDE_MISSING"},
		{name: "network", include: "include https://example.test/x.conf;\n", want: "NGX_INCLUDE_ROOT_ESCAPE"},
		{name: "invalid glob", include: "include conf.d/[;\n", want: "NGX_INCLUDE_GLOB_INVALID"},
		{
			name:    "glob limit",
			include: "include conf.d/*.conf;\n",
			limits:  IncludeLimits{MaxGlobMatches: 1},
			setup: func(t *testing.T, root string) {
				writeIncludeFixture(t, root, "conf.d/a.conf", "gzip on;\n")
				writeIncludeFixture(t, root, "conf.d/b.conf", "gzip off;\n")
			},
			want: "NGX_INCLUDE_FILE_LIMIT",
		},
		{
			name:    "total bytes",
			include: "include child.conf;\n",
			limits:  IncludeLimits{MaxFileBytes: 256, MaxTotalBytes: 40},
			setup: func(t *testing.T, root string) {
				writeIncludeFixture(t, root, "child.conf", strings.Repeat("#", 80)+"\n")
			},
			want: "NGX_INCLUDE_BYTE_LIMIT",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeIncludeFixture(t, root, "nginx.conf", tc.include)
			if tc.setup != nil {
				tc.setup(t, root)
			}
			_, report, err := ImportFileWithImportOptions(filepath.Join(root, "nginx.conf"), ImportOptions{
				FollowIncludes: true,
				IncludeRoot:    root,
				IncludeLimits:  tc.limits,
			})
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if countAssessmentCode(report.Assessment, tc.want) != 1 || report.Assessment.SourcePolicy.Complete {
				t.Fatalf("assessment = %+v", report.Assessment)
			}
		})
	}
}

func TestResolvedAssessmentNilAndIncludeOutcomeContracts(t *testing.T) {
	if got := buildAssessmentForResolvedTree(nil, "x", nil, AssessmentOptions{}, nil); got == nil {
		// BuildAssessmentWithOptions deliberately returns a safe assessment even
		// for a nil tree; the nil resolved tree must not panic.
	} else if got.SchemaVersion != AssessmentSchemaVersion {
		t.Fatalf("unexpected nil-tree assessment: %+v", got)
	}
	decorateResolvedAssessment(nil, nil, nil)
	applyIncludeResolution(nil, includeResolution{})

	result := AssessmentResult{Directive: "include", Class: AssessmentSupported, Severity: AssessmentInfo}
	applyIncludeResolution(&result, includeResolution{})
	if result.Code != "NGX_INCLUDE_DISABLED" || result.Class != AssessmentBlocking || result.Severity != AssessmentError {
		t.Fatalf("default include outcome = %+v", result)
	}
	applyIncludeResolution(&result, includeResolution{Code: "NGX_INCLUDE_RESOLVED", Message: "resolved"})
	if result.Class != AssessmentInformational || result.Severity != AssessmentInfo || result.Message != "resolved" {
		t.Fatalf("resolved include outcome = %+v", result)
	}
	if includeGuidanceCodes("NGX_INCLUDE_RESOLVED") != nil {
		t.Fatal("resolved include unexpectedly has guidance")
	}
	if got := includeGuidanceCodes("NGX_INCLUDE_DISABLED"); len(got) != 1 || got[0] != "GUIDE_INCLUDE_ENABLE" {
		t.Fatalf("disabled guidance = %#v", got)
	}
	if got := includeGuidanceCodes("NGX_INCLUDE_CYCLE"); len(got) != 1 || got[0] != "GUIDE_INCLUDE_RESOLVE" {
		t.Fatalf("failure guidance = %#v", got)
	}
}

func TestResolvedTreePolicyAndTranslationReport(t *testing.T) {
	catalog, err := newSourceCatalog(t.TempDir(), AssessmentPathRelative)
	if err != nil {
		t.Fatal(err)
	}
	tree := &resolvedSourceTree{
		catalog:        catalog,
		limits:         IncludeLimits{MaxDepth: 1, MaxFiles: 2, MaxFileBytes: 3, MaxTotalBytes: 4, MaxGlobMatches: 5},
		followIncludes: true,
		complete:       false,
		filesRead:      2,
		totalBytes:     4,
		includeResolution: map[ngx.IDirective]includeResolution{},
	}
	policy := tree.policy()
	if policy.Complete || !policy.FollowInclude || policy.FilesRead != 2 || policy.TotalBytes != 4 || policy.Limits == nil || policy.Limits.MaxGlobMatches != 5 {
		t.Fatalf("policy = %+v", policy)
	}

	include := &ngx.Include{Directive: ngx.Directive{Name: "include", Line: 7}}
	tree.includeResolution[include] = includeResolution{Code: "NGX_INCLUDE_CYCLE", Message: "cycle"}
	report := &Report{Skipped: []Finding{
		{Line: 7, Name: "include", Reason: "include not followed (unsupported)"},
		{Line: 8, Name: "map", Reason: "manual"},
	}}
	tree.applyTranslationReport(report)
	if len(report.Skipped) != 2 || report.Skipped[0].Name != "map" || report.Skipped[1].Reason != "cycle" {
		t.Fatalf("translation report = %+v", report.Skipped)
	}
	tree.applyTranslationReport(nil)
	var nilTree *resolvedSourceTree
	nilTree.applyTranslationReport(report)
}
