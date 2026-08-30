// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseAssessmentPathStyle(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    AssessmentPathStyle
		wantErr bool
	}{
		{name: "default", input: "", want: AssessmentPathRelative},
		{name: "relative", input: " relative ", want: AssessmentPathRelative},
		{name: "absolute case insensitive", input: "ABSOLUTE", want: AssessmentPathAbsolute},
		{name: "invalid", input: "host", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAssessmentPathStyle(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseAssessmentPathStyle(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ParseAssessmentPathStyle(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestAssessmentDisplayPathModesAndEscape(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "conf.d", "api.conf")
	got, err := assessmentDisplayPath(root, inside, AssessmentPathRelative)
	if err != nil {
		t.Fatalf("assessmentDisplayPath: %v", err)
	}
	if got != "conf.d/api.conf" {
		t.Fatalf("display path = %q, want conf.d/api.conf", got)
	}

	absolute, err := assessmentDisplayPath(root, inside, AssessmentPathAbsolute)
	if err != nil {
		t.Fatalf("absolute assessmentDisplayPath: %v", err)
	}
	wantAbsolute, err := filepath.Abs(inside)
	if err != nil {
		t.Fatal(err)
	}
	if absolute != filepath.ToSlash(wantAbsolute) {
		t.Fatalf("absolute display path = %q, want %q", absolute, filepath.ToSlash(wantAbsolute))
	}

	rootDisplay, err := assessmentDisplayPath(root, root, AssessmentPathRelative)
	if err != nil {
		t.Fatalf("root assessmentDisplayPath: %v", err)
	}
	if rootDisplay != filepath.Base(root) {
		t.Fatalf("root display path = %q, want %q", rootDisplay, filepath.Base(root))
	}

	outside := filepath.Join(filepath.Dir(root), "outside.conf")
	if _, err := assessmentDisplayPath(root, outside, AssessmentPathRelative); err == nil {
		t.Fatal("expected root escape to be rejected")
	}
}

func TestSourceCatalogUsesDeterministicTraversalIDsAndSnapshots(t *testing.T) {
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

	policy := catalog.policy(true)
	if policy.PathStyle != AssessmentPathRelative || policy.Root != "." || !policy.FollowInclude {
		t.Fatalf("unexpected relative policy: %+v", policy)
	}

	sources := catalog.sources()
	if len(sources) != 2 || sources[1] != second {
		t.Fatalf("unexpected source snapshot: %+v", sources)
	}
	sources[0].DisplayPath = "mutated"
	if catalog.sources()[0].DisplayPath == "mutated" {
		t.Fatal("sources returned the mutable internal slice")
	}
}

func TestSourceCatalogAbsolutePolicyAndFailures(t *testing.T) {
	root := t.TempDir()
	catalog, err := newSourceCatalog(root, AssessmentPathAbsolute)
	if err != nil {
		t.Fatal(err)
	}
	policy := catalog.policy(false)
	wantRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if policy.PathStyle != AssessmentPathAbsolute || policy.Root != filepath.ToSlash(wantRoot) || policy.FollowInclude {
		t.Fatalf("unexpected absolute policy: %+v", policy)
	}

	registered, err := catalog.register(filepath.Join(root, "nginx.conf"), "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if registered.DisplayPath != filepath.ToSlash(filepath.Join(root, "nginx.conf")) {
		t.Fatalf("absolute source path = %q", registered.DisplayPath)
	}

	if _, err := newSourceCatalog(root, AssessmentPathStyle("invalid")); err == nil {
		t.Fatal("expected invalid style to be rejected")
	}
	var nilCatalog *sourceCatalog
	if got := nilCatalog.sources(); got != nil {
		t.Fatalf("nil catalog sources = %+v, want nil", got)
	}
	if _, err := nilCatalog.register("nginx.conf", "", 0, nil); err == nil {
		t.Fatal("expected nil catalog registration to fail")
	}
}

func TestSafeDirectiveSummaryCoversBoundedForms(t *testing.T) {
	const secret = "VERY-SECRET-TOKEN"
	tests := []struct {
		name      string
		directive string
		params    []string
		contains  string
		not       string
	}{
		{name: "header no args", directive: "proxy_set_header", contains: "proxy_set_header"},
		{name: "sensitive header", directive: "proxy_set_header", params: []string{"Authorization", "Bearer " + secret}, contains: "<redacted>", not: secret},
		{name: "header value omitted", directive: "add_header", params: []string{"X-Frame-Options", "DENY", "always"}, contains: "<value-omitted>", not: "DENY"},
		{name: "proxy no args", directive: "proxy_pass", contains: "proxy_pass"},
		{name: "proxy bare target", directive: "proxy_pass", params: []string{"backend"}, contains: "<target-omitted>"},
		{name: "proxy userinfo", directive: "proxy_pass", params: []string{"https://alice:" + secret + "@backend.example/api"}, contains: "redacted-userinfo", not: secret},
		{name: "proxy valid target", directive: "proxy_pass", params: []string{"https://backend.example/api"}, contains: "backend.example/api"},
		{name: "listen no args", directive: "listen", contains: "listen"},
		{name: "listen sanitized", directive: "listen", params: []string{"  127.0.0.1:8080\nforged "}, contains: "127.0.0.1:8080 forged"},
		{name: "return no args", directive: "return", contains: "return"},
		{name: "return target omitted", directive: "return", params: []string{"302", "https://example.test/" + secret}, contains: "return 302 <target-omitted>", not: secret},
		{name: "include", directive: "include", params: []string{"/etc/nginx/" + secret}, contains: "<path-omitted>", not: secret},
		{name: "private key", directive: "ssl_certificate_key", params: []string{"/run/secrets/" + secret}, contains: "<redacted>", not: secret},
		{name: "real ip no args", directive: "set_real_ip_from", contains: "set_real_ip_from"},
		{name: "real ip", directive: "set_real_ip_from", params: []string{"10.0.0.0/8"}, contains: "10.0.0.0/8"},
		{name: "unknown no args", directive: "custom_directive", contains: "custom_directive"},
		{name: "unknown args", directive: "custom_directive", params: []string{secret}, contains: "<arguments-omitted>", not: secret},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeDirectiveSummary(tc.directive, tc.params)
			if tc.contains != "" && !strings.Contains(got, tc.contains) {
				t.Fatalf("summary %q does not contain %q", got, tc.contains)
			}
			if tc.not != "" && strings.Contains(got, tc.not) {
				t.Fatalf("summary leaked %q: %q", tc.not, got)
			}
			if utf8.RuneCountInString(got) > maxAssessmentSummaryRunes {
				t.Fatalf("summary is not bounded: %d %q", utf8.RuneCountInString(got), got)
			}
		})
	}
}

func TestSensitiveHeaderNameAndURLRedaction(t *testing.T) {
	for _, name := range []string{"Authorization", "proxy-authorization", "Cookie", "Set-Cookie", "X-Api-Token", "X-Client-Secret", "X-Api-Key", "X-APIKEY"} {
		if !sensitiveHeaderName(name) {
			t.Errorf("%q should be sensitive", name)
		}
	}
	if sensitiveHeaderName("X-Frame-Options") {
		t.Fatal("ordinary security header name should not be treated as a secret value")
	}

	const secret = "url-secret"
	cases := []string{
		"https://alice:" + secret + "@backend.example/path",
		"https://alice:" + secret + "@backend.example/%zz",
		"alice:" + secret + "@backend.example",
	}
	for _, raw := range cases {
		got := redactURLUserinfo(raw)
		if strings.Contains(got, secret) {
			t.Fatalf("redactURLUserinfo(%q) leaked secret in %q", raw, got)
		}
	}
	if got := redactURLUserinfo("backend.example"); got != "<target-omitted>" {
		t.Fatalf("bare target = %q, want omitted", got)
	}
	if got := redactURLUserinfo("https://backend.example/path"); !strings.Contains(got, "backend.example/path") {
		t.Fatalf("safe URL was not retained: %q", got)
	}
}

func TestBoundedSummaryUsesRuneLimit(t *testing.T) {
	input := strings.Repeat("界", maxAssessmentSummaryRunes+20)
	got := boundedSummary(input)
	if utf8.RuneCountInString(got) != maxAssessmentSummaryRunes {
		t.Fatalf("bounded runes = %d, want %d", utf8.RuneCountInString(got), maxAssessmentSummaryRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded summary lacks ellipsis: %q", got)
	}
	if got := boundedSummary("short"); got != "short" {
		t.Fatalf("short summary changed: %q", got)
	}
}

func TestSourceIndexTracksNestedQuotedEscapedAndIncompleteInput(t *testing.T) {
	items := indexSourceDirectives([]byte("# comment\r\nhttp {\r\n  server {\r\n    listen 8080;\r\n    add_header X-Test \"a;{b}\\\"c\" always;\r\n  }\r\n}\r\n"))
	want := []struct {
		name      string
		line, col int
		endLine   int
		endColumn int
	}{
		{"http", 2, 1, 7, 1},
		{"server", 3, 3, 6, 3},
		{"listen", 4, 5, 4, 16},
		{"add_header", 5, 5, 5, 40},
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

	if got := indexSourceDirectives([]byte("# trailing comment without newline")); len(got) != 0 {
		t.Fatalf("comment-only source produced directives: %+v", got)
	}
	incomplete := indexSourceDirectives([]byte("http { server_name 'unterminated\\"))
	if len(incomplete) != 2 || incomplete[0].Name != "http" || incomplete[1].Name != "server_name" {
		t.Fatalf("unexpected incomplete index: %+v", incomplete)
	}
	// A stray closing brace is ignored safely rather than panicking.
	if got := indexSourceDirectives([]byte("}\nlisten 80;")); len(got) != 1 || got[0].Name != "listen" {
		t.Fatalf("unexpected stray-brace index: %+v", got)
	}
}

func TestGuidanceCatalogDeterministicCompleteAndMissingLookup(t *testing.T) {
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
	if _, ok := lookupAssessmentGuidance("GUIDE_DOES_NOT_EXIST"); ok {
		t.Fatal("missing guidance unexpectedly resolved")
	}
}
