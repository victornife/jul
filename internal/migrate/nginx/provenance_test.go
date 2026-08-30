// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAssessmentDisplayPathRelativeAndEscape(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "conf.d", "api.conf")
	got, err := assessmentDisplayPath(root, inside, AssessmentPathRelative)
	if err != nil {
		t.Fatalf("assessmentDisplayPath: %v", err)
	}
	if got != "conf.d/api.conf" {
		t.Fatalf("display path = %q, want conf.d/api.conf", got)
	}
	outside := filepath.Join(filepath.Dir(root), "outside.conf")
	if _, err := assessmentDisplayPath(root, outside, AssessmentPathRelative); err == nil {
		t.Fatal("expected root escape to be rejected")
	}
}

func TestSourceCatalogUsesDeterministicTraversalIDs(t *testing.T) {
	root := t.TempDir()
	catalog, err := newSourceCatalog(root, AssessmentPathRelative)
	if err != nil {
		t.Fatal(err)
	}
	first, err := catalog.register(filepath.Join(root, "nginx.conf"), "", 0, []byte("http {}"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.register(filepath.Join(root, "conf.d", "api.conf"), first.ID, 4, []byte("server {}"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "source-0001" || second.ID != "source-0002" {
		t.Fatalf("unexpected IDs: %q %q", first.ID, second.ID)
	}
	if second.ParentID != first.ID || second.IncludeLine != 4 {
		t.Fatalf("include ancestry lost: %+v", second)
	}
	if !strings.HasPrefix(first.Digest, "sha256:") || len(first.Digest) != len("sha256:")+64 {
		t.Fatalf("unexpected digest: %q", first.Digest)
	}
}

func TestSafeDirectiveSummaryRedactsSecrets(t *testing.T) {
	const secret = "VERY-SECRET-TOKEN"
	cases := []struct {
		name   string
		params []string
	}{
		{"proxy_set_header", []string{"Authorization", "Bearer " + secret}},
		{"add_header", []string{"X-Api-Token", secret, "always"}},
		{"proxy_pass", []string{"https://alice:" + secret + "@backend.example/api"}},
		{"ssl_certificate_key", []string{"/run/secrets/" + secret}},
		{"content_by_lua_block", []string{"return '" + secret + "'"}},
	}
	for _, tc := range cases {
		got := safeDirectiveSummary(tc.name, tc.params)
		if strings.Contains(got, secret) {
			t.Fatalf("%s summary leaked secret: %q", tc.name, got)
		}
		if len(got) > maxAssessmentSummaryRunes+3 {
			t.Fatalf("summary is not bounded: %d %q", len(got), got)
		}
	}
}

func TestSourceIndexTracksNestedAndQuotedBoundaries(t *testing.T) {
	items := indexSourceDirectives([]byte("# comment\r\nhttp {\r\n  server {\r\n    listen 8080;\r\n    add_header X-Test \"a;{b}\" always;\r\n  }\r\n}\r\n"))
	want := []struct {
		name      string
		line, col int
		endLine   int
		endColumn int
	}{
		{"http", 2, 1, 7, 1},
		{"server", 3, 3, 6, 3},
		{"listen", 4, 5, 4, 16},
		{"add_header", 5, 5, 5, 37},
	}
	if len(items) != len(want) {
		t.Fatalf("indexed %d directives, want %d: %+v", len(items), len(want), items)
	}
	for i, expected := range want {
		got := items[i]
		if got.Name != expected.name || got.Start.Line != expected.line || got.Start.Column != expected.col || got.End.Line != expected.endLine || got.End.Column != expected.endColumn {
			t.Errorf("item %d = %+v, want %s %d:%d-%d:%d", i, got, expected.name, expected.line, expected.col, expected.endLine, expected.endColumn)
		}
	}
}

func TestGuidanceCatalogDeterministicAndComplete(t *testing.T) {
	all := allAssessmentGuidance()
	if len(all) < 8 {
		t.Fatalf("guidance catalogue unexpectedly small: %d", len(all))
	}
	for i, guidance := range all {
		if guidance.Code == "" || guidance.Title == "" || guidance.Action == "" {
			t.Fatalf("incomplete guidance: %+v", guidance)
		}
		if i > 0 && all[i-1].Code >= guidance.Code {
			t.Fatalf("guidance not strictly sorted: %q then %q", all[i-1].Code, guidance.Code)
		}
		if got, ok := lookupAssessmentGuidance(guidance.Code); !ok || got != guidance {
			t.Fatalf("lookup mismatch for %q", guidance.Code)
		}
	}
}
