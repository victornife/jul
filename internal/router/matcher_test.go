// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
)

// testServerRoute assembles a server block from declaration-ordered locations
// and derives its selection tiers exactly as buildServerRoute does.
func testServerRoute(locs ...*locationRoute) *serverRoute {
	s := &serverRoute{}
	for i, loc := range locs {
		loc.index = i
		s.locations = append(s.locations, loc)
	}
	s.indexLocations()
	return s
}

// selectRequest builds a bare GET for a path without going through URL
// parsing, so an arbitrary fuzzer-supplied path reaches the matcher verbatim.
func selectRequest(path, rawQuery string) *http.Request {
	return &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: path, RawQuery: rawQuery},
		Header: http.Header{},
	}
}

// selectPath resolves a bare GET for the given path.
func selectPath(s *serverRoute, path string) *locationRoute {
	return s.selectLocation(selectRequest(path, ""))
}

func TestSelectLocationExact(t *testing.T) {
	s := testServerRoute(
		&locationRoute{matchType: "exact", path: "/api"},
		&locationRoute{matchType: "exact", path: "/api/users"},
		&locationRoute{matchType: "prefix", path: "/"},
	)

	if loc := selectPath(s, "/api"); loc == nil || loc.path != "/api" {
		t.Fatalf("expected exact match /api, got %v", loc)
	}
	if loc := selectPath(s, "/api/users"); loc == nil || loc.path != "/api/users" {
		t.Fatalf("expected exact match /api/users, got %v", loc)
	}
}

func TestSelectLocationLongestPrefix(t *testing.T) {
	s := testServerRoute(
		&locationRoute{matchType: "prefix", path: "/api"},
		&locationRoute{matchType: "prefix", path: "/api/v1"},
		&locationRoute{matchType: "prefix", path: "/"},
	)

	loc := selectPath(s, "/api/v1/users")
	if loc == nil || loc.path != "/api/v1" {
		t.Fatalf("expected longest prefix /api/v1, got %v", loc)
	}
}

// TestSelectLocationRootIsConsultedLast pins that `prefix "/"` is the last
// candidate of all: a longer prefix and a regex both outrank it, whatever order
// they are declared in.
func TestSelectLocationRootIsConsultedLast(t *testing.T) {
	s := testServerRoute(
		&locationRoute{matchType: "prefix", path: "/"},
		&locationRoute{matchType: "prefix", path: "/docs"},
		&locationRoute{matchType: "regex", path: `\.png$`, re: regexp.MustCompile(`\.png$`)},
	)

	if loc := selectPath(s, "/docs/readme"); loc == nil || loc.path != "/docs" {
		t.Fatalf("expected /docs, got %v", loc)
	}
	if loc := selectPath(s, "/img/logo.png"); loc == nil || loc.matchType != "regex" {
		t.Fatalf("expected the regex candidate to outrank the catch-all, got %v", loc)
	}
	if loc := selectPath(s, "/other"); loc == nil || loc.path != "/" {
		t.Fatalf("expected /, got %v", loc)
	}
}

// TestSelectLocationDuplicateRootTakesTheFirstDeclared pins the one path-only
// behaviour ADR 0018 §6 changes: the old sr.fallback was reassigned on every
// `prefix "/"` location so the LAST one won, while lint has always told the
// operator the first one wins. The router now agrees with the lint.
func TestSelectLocationDuplicateRootTakesTheFirstDeclared(t *testing.T) {
	first := &locationRoute{matchType: "prefix", path: "/"}
	s := testServerRoute(first, &locationRoute{matchType: "prefix", path: "/"})

	if loc := selectPath(s, "/anything"); loc != first {
		t.Fatal("expected the first declared \"/\" location, not the last")
	}
}

// TestSelectLocationRejectsANonRootedPath pins the property ADR 0018 §2 rests
// on: Go gives an authority-form CONNECT an empty URL path, and a server-wide
// OPTIONS carries "*". Neither is a prefix of any configured path, so both fall
// through to the router's 404 instead of reaching the catch-all.
func TestSelectLocationRejectsANonRootedPath(t *testing.T) {
	s := testServerRoute(&locationRoute{matchType: "prefix", path: "/"})

	for _, path := range []string{"", "*"} {
		if loc := selectPath(s, path); loc != nil {
			t.Errorf("path %q selected %v, want no candidate", path, loc)
		}
	}
}

func TestSelectLocationRegex(t *testing.T) {
	root := &locationRoute{matchType: "prefix", path: "/"}
	s := testServerRoute(
		&locationRoute{matchType: "regex", path: `^/users/\d+$`, re: regexp.MustCompile(`^/users/\d+$`)},
		root,
	)

	if loc := selectPath(s, "/users/123"); loc == nil || loc.matchType != "regex" {
		t.Fatalf("expected regex match, got %v", loc)
	}
	if loc := selectPath(s, "/users/abc"); loc != root {
		t.Fatal("expected the catch-all for a non-matching regex")
	}
}

// TestSelectLocationPrefixOutranksRegex pins the tier order: a non-root prefix
// candidate is consulted before any regex, however specific the pattern.
func TestSelectLocationPrefixOutranksRegex(t *testing.T) {
	s := testServerRoute(
		&locationRoute{matchType: "regex", path: `^/users/\d+$`, re: regexp.MustCompile(`^/users/\d+$`)},
		&locationRoute{matchType: "prefix", path: "/users/"},
	)

	if loc := selectPath(s, "/users/123"); loc == nil || loc.matchType != "prefix" {
		t.Fatalf("expected the prefix candidate to outrank the regex, got %v", loc)
	}
}

func TestSelectLocationNoCandidate(t *testing.T) {
	s := testServerRoute(&locationRoute{matchType: "exact", path: "/only"})

	if loc := selectPath(s, "/anything"); loc != nil {
		t.Fatal("expected no selection, which Router.For answers with a 404")
	}
}

func TestApplyRewritesRedirect(t *testing.T) {
	rules := []compiledRewrite{
		{re: regexp.MustCompile(`^/old$`), replacement: "/new", flag: "redirect"},
	}

	req := httptest.NewRequest(http.MethodGet, "/old", nil)
	rec := httptest.NewRecorder()

	if !applyRewrites(rules, rec, req) {
		t.Fatal("expected rewrite to return true (handled)")
	}

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/new" {
		t.Fatalf("location = %q, want /new", loc)
	}
}

func TestApplyRewritesPermanent(t *testing.T) {
	rules := []compiledRewrite{
		{re: regexp.MustCompile(`^/old$`), replacement: "/new", flag: "permanent"},
	}

	req := httptest.NewRequest(http.MethodGet, "/old", nil)
	rec := httptest.NewRecorder()

	if !applyRewrites(rules, rec, req) {
		t.Fatal("expected rewrite to return true (handled)")
	}

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
}

func TestApplyRewritesLastBreak(t *testing.T) {
	rules := []compiledRewrite{
		{re: regexp.MustCompile(`^/api/v1/(.*)$`), replacement: "/api/v2/$1", flag: "last"},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()

	if applyRewrites(rules, rec, req) {
		t.Fatal("expected rewrite to return false (continue)")
	}
	if req.URL.Path != "/api/v2/users" {
		t.Fatalf("path = %q, want /api/v2/users", req.URL.Path)
	}
}

func TestApplyRewritesDefaultRewrite(t *testing.T) {
	rules := []compiledRewrite{
		{re: regexp.MustCompile(`^/a$`), replacement: "/b", flag: ""},
		{re: regexp.MustCompile(`^/b$`), replacement: "/c", flag: ""},
	}

	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	rec := httptest.NewRecorder()

	if applyRewrites(rules, rec, req) {
		t.Fatal("expected false")
	}
	if req.URL.Path != "/c" {
		t.Fatalf("path = %q, want /c", req.URL.Path)
	}
}

func TestApplyRewritesNoMatch(t *testing.T) {
	rules := []compiledRewrite{
		{re: regexp.MustCompile(`^/other$`), replacement: "/x", flag: "redirect"},
	}

	req := httptest.NewRequest(http.MethodGet, "/nomatch", nil)
	rec := httptest.NewRecorder()

	if applyRewrites(rules, rec, req) {
		t.Fatal("expected false when no rule matches")
	}
}
