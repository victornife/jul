// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestCanonicalPath(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"*":                      "*",
		"/":                      "/",
		"/a/b":                   "/a/b",
		"/a/b/":                  "/a/b/",
		"/.well-known/x":         "/.well-known/x",
		"/a/..b/c":               "/a/..b/c",
		"//a":                    "/a",
		"/a//b///c":              "/a/b/c",
		"/a/./b":                 "/a/b",
		"/a/../b":                "/b",
		"/a/b/..":                "/a/",
		"/a/b/.":                 "/a/b/",
		"/..":                    "/",
		"/../../x":               "/x",
		"/pub/../private/s.txt":  "/private/s.txt",
		"/a/b/../../../c/":       "/c/",
		"/a//":                   "/a/",
		"/./":                    "/",
		"/x/../":                 "/",
		"/api/v1/../../admin/..": "/",
	}
	for in, want := range cases {
		if got := canonicalPath(in); got != want {
			t.Errorf("canonicalPath(%q) = %q, want %q", in, got, want)
		}
		if got := canonicalPath(want); got != want {
			t.Errorf("canonicalPath not idempotent on %q: %q", want, got)
		}
	}
}

// TestDotSegmentsCannotBypassLocation proves a dot-segment or repeated-slash
// request target selects the location its canonical path names, and that the
// handler (static root, upstream) receives that same canonical path.
func TestDotSegmentsCannotBypassLocation(t *testing.T) {
	var gotPath, gotRaw string
	capture := func(_ config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
		tag := loc.Match.Path
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotRaw = r.URL.Path, r.URL.EscapedPath()
			w.Header().Set("X-Matched", tag)
		}), nil
	}
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/private/"}, Root: "/r"},
			{Match: config.MatchConfig{Type: "exact", Path: "/admin"}, Root: "/r"},
			{Match: config.MatchConfig{Type: "prefix", Path: "/pub/"}, Root: "/r"},
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/r"},
		},
	}}}
	r, err := New(cfg, map[string]Builder{ActionStatic: capture}, capture, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ target, loc, path string }{
		{"/pub/../private/s.txt", "/private/", "/private/s.txt"},
		{"/pub/%2e%2e/private/s.txt", "/private/", "/private/s.txt"},
		{"/pub/..%2fprivate/s.txt", "/private/", "/private/s.txt"},
		{"//private/s.txt", "/private/", "/private/s.txt"},
		{"/pub/./../admin", "/admin", "/admin"},
		{"/pub/p.txt", "/pub/", "/pub/p.txt"},
		{"/pub/a%2Fb", "/pub/", "/pub/a/b"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://h"+c.target, nil)
		rec := httptest.NewRecorder()
		r.For("127.0.0.1:80").ServeHTTP(rec, req)
		if m := rec.Header().Get("X-Matched"); m != c.loc {
			t.Errorf("%s matched %q, want %q", c.target, m, c.loc)
		}
		if gotPath != c.path {
			t.Errorf("%s handler saw path %q, want %q", c.target, gotPath, c.path)
		}
		if ex := r.Explain("h", httptest.NewRequest(http.MethodGet, "http://h"+c.target, nil)); ex.Selected < 0 || ex.Candidates[ex.Selected].Path != c.loc {
			t.Errorf("%s: Explain disagrees with For: %+v", c.target, ex)
		}
	}
	// An unchanged path keeps its original escaping.
	req := httptest.NewRequest(http.MethodGet, "http://h/pub/a%2Fb", nil)
	r.For("127.0.0.1:80").ServeHTTP(httptest.NewRecorder(), req)
	if gotRaw != "/pub/a%2Fb" {
		t.Errorf("escaped path of an already-canonical target = %q, want /pub/a%%2Fb", gotRaw)
	}
}

func FuzzCanonicalPath(f *testing.F) {
	for _, s := range []string{"/", "/a/../b", "//x//y/", "/./.././", "/a/b/..", "*"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		c := canonicalPath(p)
		if p == "" || p[0] != '/' {
			if c != p {
				t.Fatalf("unrooted %q changed to %q", p, c)
			}
			return
		}
		if c == "" || c[0] != '/' || needsCanonical(c) {
			t.Fatalf("canonicalPath(%q) = %q is not canonical", p, c)
		}
		if canonicalPath(c) != c {
			t.Fatalf("not idempotent: %q -> %q", p, c)
		}
	})
}
