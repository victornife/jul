// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveIncludeRootFlags(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	tests := []struct {
		name        string
		root        string
		includeRoot string
		want        string
		wantErr     bool
	}{
		{name: "empty"},
		{name: "root", root: root, want: root},
		{name: "include root", includeRoot: root, want: root},
		{name: "same aliases", root: root, includeRoot: filepath.Join(root, "."), want: root},
		{name: "conflicting aliases", root: root, includeRoot: other, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveIncludeRootFlags(tc.root, tc.includeRoot)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveIncludeRootFlags error = %v, wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("resolveIncludeRootFlags = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCmdImportIncludeUsageValidation(t *testing.T) {
	in := writeNginx(t, sampleNginx)
	otherRoot := t.TempDir()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "source order without assess", args: []string{"nginx", "--source-order", in}, want: "requires --assess"},
		{name: "invalid path style", args: []string{"nginx", "--path-style", "private", in}, want: "relative or absolute"},
		{name: "root without traversal", args: []string{"nginx", "--root", filepath.Dir(in), in}, want: "requires --follow-includes"},
		{name: "conflicting root aliases", args: []string{"nginx", "--follow-includes", "--root", filepath.Dir(in), "--include-root", otherRoot, in}, want: "same directory"},
		{name: "zero depth", args: []string{"nginx", "--max-include-depth", "0", in}, want: "must be positive"},
		{name: "zero files", args: []string{"nginx", "--max-include-files", "0", in}, want: "must be positive"},
		{name: "zero file bytes", args: []string{"nginx", "--max-include-file-bytes", "0", in}, want: "must be positive"},
		{name: "zero total bytes", args: []string{"nginx", "--max-include-total-bytes", "0", in}, want: "must be positive"},
		{name: "zero glob matches", args: []string{"nginx", "--max-include-glob-matches", "0", in}, want: "must be positive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := capture(t, func() int { return cmdImport(tc.args) })
			if code != importExitUsage || !strings.Contains(errOut, tc.want) {
				t.Fatalf("exit=%d stderr=%q, want usage containing %q", code, errOut, tc.want)
			}
		})
	}
}

func TestCmdImportFollowIncludesJSONAndConfig(t *testing.T) {
	root := t.TempDir()
	writeCLIIncludeFixture(t, root, "nginx.conf", `http {
    include conf.d/site.conf;
}
`)
	writeCLIIncludeFixture(t, root, "conf.d/site.conf", `server {
    listen 8080;
    server_name example.test;
    location / { return 204; }
}
`)
	in := filepath.Join(root, "nginx.conf")

	code, out, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--json", "--follow-includes", "--root", root, in})
	})
	if code != importExitOK {
		t.Fatalf("JSON exit=%d stdout=%s stderr=%s", code, out, errOut)
	}
	for _, want := range []string{`"follow_includes": true`, `"complete": true`, `"files_read": 2`, `"code": "NGX_INCLUDE_RESOLVED"`, `"display_path": "conf.d/site.conf"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("JSON output missing %q:\n%s", want, out)
		}
	}

	output := filepath.Join(root, "jul.toml")
	code, _, errOut = capture(t, func() int {
		return cmdImport([]string{"nginx", "--follow-includes", "--include-root", root, "-o", output, in})
	})
	if code != importExitOK {
		t.Fatalf("config exit=%d stderr=%s", code, errOut)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if !strings.Contains(string(data), `listen = ":8080"`) || !strings.Contains(string(data), "example.test") {
		t.Fatalf("generated config omitted included source:\n%s", data)
	}
}

func TestCmdImportIncompleteTraversalDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	writeCLIIncludeFixture(t, root, "nginx.conf", "include missing.conf;\n")
	in := filepath.Join(root, "nginx.conf")
	output := filepath.Join(root, "jul.toml")

	code, _, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--follow-includes", "--root", root, "-o", output, in})
	})
	if code != importExitFindings || !strings.Contains(errOut, "incomplete") {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("incomplete traversal wrote output: %v", err)
	}
}

func TestCmdImportFollowIncludesDefaultRootAndHuman(t *testing.T) {
	root := t.TempDir()
	writeCLIIncludeFixture(t, root, "nginx.conf", "include child.conf;\n")
	writeCLIIncludeFixture(t, root, "child.conf", "events {}\n")
	in := filepath.Join(root, "nginx.conf")

	code, out, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "--assess", "--source-order", "--follow-includes", in})
	})
	if code != importExitOK {
		t.Fatalf("human exit=%d stdout=%s stderr=%s", code, out, errOut)
	}
	if !strings.Contains(out, "SOURCE ORDER") || !strings.Contains(out, "NGX_INCLUDE_RESOLVED") {
		t.Fatalf("human output missing include navigation:\n%s", out)
	}
}

func writeCLIIncludeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir include fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write include fixture: %v", err)
	}
}
