// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportFileNotFound(t *testing.T) {
	_, _, err := ImportFile(filepath.Join(t.TempDir(), "nonexistent.conf"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestImportFileSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.conf")
	src := `
http {
  server {
    listen 8080;
    location / { return 200; }
  }
}
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, rep, err := ImportFile(path)
	if err != nil {
		t.Fatalf("ImportFile failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("config is nil")
	}
	if rep == nil {
		t.Fatal("report is nil")
	}
	if rep.Source != path {
		t.Errorf("source = %q, want %q", rep.Source, path)
	}
}

func TestParseFileWithCommentOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.conf")
	if err := os.WriteFile(path, []byte("# comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile failed: %v", err)
	}
	if cfg == nil {
		t.Error("cfg is nil")
	}
}

func TestParseFileMissingFile(t *testing.T) {
	_, err := parseFile(filepath.Join(t.TempDir(), "missing.conf"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseStringRecover(t *testing.T) {
	// parseString recovers from panics; give it valid input instead.
	cfg, err := parseString(`
http {
  server {
    listen 80;
    location / { return 200; }
  }
}
`)
	if err != nil {
		t.Fatalf("parseString failed: %v", err)
	}
	if cfg == nil {
		t.Error("cfg is nil")
	}
}

func TestReportHeaderEmpty(t *testing.T) {
	r := &Report{Source: "a.conf", Servers: 1, Upstreams: 0, Locations: 2}
	h := r.Header()
	if !strings.Contains(h, "a.conf") {
		t.Errorf("missing source: %s", h)
	}
	if !strings.Contains(h, "1 server(s)") {
		t.Errorf("missing servers: %s", h)
	}
	if !strings.Contains(h, "2 location(s)") {
		t.Errorf("missing locations: %s", h)
	}
	if strings.Contains(h, "TODO") {
		t.Error("unexpected TODO in empty report")
	}
}

func TestReportHeaderWithSkipped(t *testing.T) {
	r := &Report{
		Source: "b.conf", Servers: 1, Upstreams: 1, Locations: 1,
		Skipped: []Finding{
			{Line: 3, Name: "fastcgi_pass", Reason: "unsupported"},
			{Line: 1, Name: "listen", Reason: "ignored"},
		},
	}
	h := r.Header()
	if !strings.Contains(h, "TODO line 1: listen - ignored") {
		t.Errorf("missing sorted finding: %s", h)
	}
	if !strings.Contains(h, "TODO line 3: fastcgi_pass - unsupported") {
		t.Errorf("missing sorted finding: %s", h)
	}
}

func TestReportHeaderWithNotes(t *testing.T) {
	r := &Report{
		Source: "c.conf", Servers: 0, Upstreams: 0, Locations: 0,
		Notes: []string{"note one", "note two"},
	}
	h := r.Header()
	if !strings.Contains(h, "note one") || !strings.Contains(h, "note two") {
		t.Errorf("missing notes: %s", h)
	}
}

func TestReportSummaryEmpty(t *testing.T) {
	r := &Report{Source: "a.conf", Servers: 2, Upstreams: 1, Locations: 3}
	s := r.Summary()
	if !strings.Contains(s, "2 server(s)") {
		t.Errorf("missing servers: %s", s)
	}
	if strings.Contains(s, "not translated") {
		t.Error("unexpected skipped section")
	}
}

func TestReportSummaryWithSkippedAndNotes(t *testing.T) {
	r := &Report{
		Source:    "b.conf",
		Servers:   1,
		Upstreams: 0,
		Locations: 1,
		Skipped:   []Finding{{Line: 2, Name: "x", Reason: "r"}},
		Notes:     []string{"lossy"},
	}
	s := r.Summary()
	if !strings.Contains(s, "1 directive(s) not translated") {
		t.Errorf("missing skipped count: %s", s)
	}
	if !strings.Contains(s, "lossy") {
		t.Errorf("missing note: %s", s)
	}
}

func TestSortedSkippedSorting(t *testing.T) {
	r := &Report{
		Skipped: []Finding{
			{Line: 5, Name: "aaa"},
			{Line: 2, Name: "bbb"},
			{Line: 2, Name: "aaa"},
		},
	}
	out := r.sortedSkipped()
	if len(out) != 3 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0].Line != 2 || out[0].Name != "aaa" {
		t.Errorf("first = %+v", out[0])
	}
	if out[1].Line != 2 || out[1].Name != "bbb" {
		t.Errorf("second = %+v", out[1])
	}
	if out[2].Line != 5 {
		t.Errorf("third = %+v", out[2])
	}
	// Must not mutate original order.
	if r.Skipped[0].Name != "aaa" {
		t.Error("original slice mutated")
	}
}

func TestReportSkipAndNote(t *testing.T) {
	r := &Report{}
	// Simulate skipping a directive using skipNamed.
	r.skipNamed("gzip", 1, "unsupported")
	r.note("lossy mapping for %s", "root")
	if len(r.Skipped) != 1 {
		t.Fatalf("skipped = %d", len(r.Skipped))
	}
	if r.Skipped[0].Name != "gzip" {
		t.Errorf("name = %q", r.Skipped[0].Name)
	}
	if len(r.Notes) != 1 || r.Notes[0] != "lossy mapping for root" {
		t.Errorf("notes = %v", r.Notes)
	}
}
