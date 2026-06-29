package router

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestMatchLocationExact(t *testing.T) {
	s := &serverRoute{
		locations: []*locationRoute{
			{matchType: "exact", path: "/api"},
			{matchType: "exact", path: "/api/users"},
		},
		fallback: &locationRoute{matchType: "prefix", path: "/"},
	}

	if loc := s.matchLocation("/api"); loc == nil || loc.path != "/api" {
		t.Fatalf("expected exact match /api, got %v", loc)
	}
	if loc := s.matchLocation("/api/users"); loc == nil || loc.path != "/api/users" {
		t.Fatalf("expected exact match /api/users, got %v", loc)
	}
}

func TestMatchLocationLongestPrefix(t *testing.T) {
	s := &serverRoute{
		locations: []*locationRoute{
			{matchType: "prefix", path: "/api"},
			{matchType: "prefix", path: "/api/v1"},
		},
		fallback: &locationRoute{matchType: "prefix", path: "/"},
	}

	loc := s.matchLocation("/api/v1/users")
	if loc == nil || loc.path != "/api/v1" {
		t.Fatalf("expected longest prefix /api/v1, got %v", loc)
	}
}

func TestMatchLocationPrefixSkipsRootDuringSearch(t *testing.T) {
	s := &serverRoute{
		locations: []*locationRoute{
			{matchType: "prefix", path: "/"},
			{matchType: "prefix", path: "/docs"},
		},
		fallback: &locationRoute{matchType: "prefix", path: "/"},
	}

	loc := s.matchLocation("/docs/readme")
	if loc == nil || loc.path != "/docs" {
		t.Fatalf("expected /docs, got %v", loc)
	}
}

func TestMatchLocationRegex(t *testing.T) {
	s := &serverRoute{
		locations: []*locationRoute{
			{matchType: "regex", re: regexp.MustCompile(`^/users/\d+$`)},
		},
		fallback: &locationRoute{matchType: "prefix", path: "/"},
	}

	loc := s.matchLocation("/users/123")
	if loc == nil {
		t.Fatal("expected regex match")
	}

	loc = s.matchLocation("/users/abc")
	if loc != s.fallback {
		t.Fatal("expected fallback for non-matching regex")
	}
}

func TestMatchLocationFallback(t *testing.T) {
	fallback := &locationRoute{matchType: "prefix", path: "/"}
	s := &serverRoute{
		locations: []*locationRoute{},
		fallback:  fallback,
	}

	loc := s.matchLocation("/anything")
	if loc != fallback {
		t.Fatal("expected fallback")
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
