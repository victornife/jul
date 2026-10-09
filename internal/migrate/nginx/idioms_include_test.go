// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMIMEIncludeContentsAreAuthoritative(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		wantMIME      bool
	}{
		{"custom-table.conf", "types {video/mp2t ts;}", true},
		{"mime.types", "lua_shared_dict sessions 1m;", false},
	} {
		root := t.TempDir()
		path := filepath.Join(root, "nginx.conf")
		if err := os.WriteFile(path, []byte("http {include "+tc.name+";server {listen 8080;location / {root /tmp;}}}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, tc.name), []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, rep, err := ImportFileWithImportOptions(path, ImportOptions{FollowIncludes: true, IncludeRoot: root, Assessment: AssessmentOptions{PathStyle: AssessmentPathRelative}})
		if err != nil {
			t.Fatal(err)
		}
		if (cfg.MIME != nil) != tc.wantMIME || rep.Assessment.HasBlocking() == tc.wantMIME {
			t.Fatalf("%s: MIME %v blocking %v", tc.name, cfg.MIME, rep.Assessment.HasBlocking())
		}
		cfg, rep, err = ImportFileWithImportOptions(path, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MIME != nil || !rep.Assessment.HasBlocking() {
			t.Fatal("disabled traversal inferred MIME by filename")
		}
	}
}

func TestMIMEIncludeInsideTypes(t *testing.T) {
	for _, follow := range []bool{false, true} {
		root := t.TempDir()
		path := filepath.Join(root, "nginx.conf")
		if err := os.WriteFile(path, []byte("http {types {include custom-map.conf;}server {listen 8080;location / {root /tmp;}}}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "custom-map.conf"), []byte("video/mp2t ts;application/x-custom foo;"), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, rep, err := ImportFileWithImportOptions(path, ImportOptions{FollowIncludes: follow, IncludeRoot: root, Assessment: AssessmentOptions{PathStyle: AssessmentPathRelative}})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Assessment.HasBlocking() == follow {
			t.Fatalf("follow=%v: %+v", follow, rep.Assessment.Results)
		}
		if follow && (cfg.MIME == nil || cfg.MIME.Types == nil || (*cfg.MIME.Types)[".ts"] != "video/mp2t" || (*cfg.MIME.Types)[".foo"] != "application/x-custom") {
			t.Fatalf("MIME: %+v", cfg.MIME)
		}
		found := false
		for _, result := range rep.Assessment.Results {
			if result.Directive == "include" {
				found = true
				if follow && (result.Code != "NGX_INCLUDE_RESOLVED" || result.Provenance == nil) {
					t.Fatalf("include result: %+v", result)
				}
			}
		}
		if !found {
			t.Fatal("include resolution result missing")
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, "nginx.conf")
	if err := os.WriteFile(path, []byte("http {types {include missing.conf;}server {listen 8080;location / {root /tmp;}}}"), 0600); err != nil {
		t.Fatal(err)
	}
	_, rep, err := ImportFileWithImportOptions(path, ImportOptions{FollowIncludes: true, IncludeRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Assessment.HasBlocking() || rep.Assessment.SourcePolicy.Complete {
		t.Fatal("missing table include accepted")
	}
}

func TestMIMEIncludeDeclarationOrder(t *testing.T) {
	for _, tc := range []struct {
		body, want string
	}{
		{"text/plain ts;\ninclude custom-map.conf;", "video/mp2t"},
		{"include custom-map.conf;\ntext/plain ts;", "text/plain"},
	} {
		root := t.TempDir()
		path := filepath.Join(root, "nginx.conf")
		source := "http {\ntypes {\n" + tc.body + "\n}\nserver {listen 8080;location / {root /tmp;}}}"
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "custom-map.conf"), []byte("video/mp2t ts;"), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, rep, err := ImportFileWithImportOptions(path, ImportOptions{FollowIncludes: true, IncludeRoot: root})
		if err != nil || rep.Assessment.HasBlocking() {
			t.Fatal(err, rep)
		}
		if (*cfg.MIME.Types)[".ts"] != tc.want {
			t.Fatalf("include declaration order: %v want %s", cfg.MIME.Types, tc.want)
		}
	}
}
