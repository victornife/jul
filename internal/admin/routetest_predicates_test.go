// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"

	"jul/internal/config"
)

// The route-test surface is now the router's own selection rather than a second
// implementation of it (ADR 0014, ADR 0018 §14), and its request type gained
// the two fields §14 froze. These tests pin both halves: predicates are real
// inputs, and the result explains the candidates it rejected.

func routeTestStrPtr(s string) *string { return &s }

// predicateRouteTestCfg has two routes on one path, distinguished only by a
// method predicate, plus a catch-all — the shape the whole record exists for.
func predicateRouteTestCfg() *config.Config {
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      ":8080",
			ServerNames: []string{"example.com"},
			Locations: []config.LocationConfig{
				{
					Match: config.MatchConfig{
						Type:    "prefix",
						Path:    "/api/",
						Methods: []string{"POST"},
						Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: routeTestStrPtr("public")}},
						Query:   []config.QueryMatch{{Name: "version", Op: "exact", Value: routeTestStrPtr("v2")}},
					},
					ProxyPass: "http://writes",
				},
				{
					Match:     config.MatchConfig{Type: "prefix", Path: "/api/"},
					ProxyPass: "http://reads",
				},
				{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "./public"},
			},
		}},
	}
}

func predicateRouteTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := predicateRouteTestCfg()
	return newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
}

// TestRouteTestTreatsMethodHeadersAndQueryAsRealInputs pins the change: Method
// and Headers were accepted and discarded before, and there was no query input
// at all.
func TestRouteTestTreatsMethodHeadersAndQueryAsRealInputs(t *testing.T) {
	s := predicateRouteTestServer(t)

	matching := postRouteTest(t, s, `{
		"method":"POST",
		"path":"/api/users",
		"host":"example.com",
		"raw_query":"version=v2",
		"headers":{"X-Tenant":"public"}
	}`)
	if !matching.Matched || matching.Target != "http://writes" {
		t.Fatalf("a request satisfying every predicate should select the predicate-bearing route; got %+v", matching)
	}

	// Drop the method and the same request falls through to the route below it,
	// which is what the enumeration exists for.
	fallthrough_ := postRouteTest(t, s, `{
		"method":"GET",
		"path":"/api/users",
		"host":"example.com",
		"raw_query":"version=v2",
		"headers":{"X-Tenant":"public"}
	}`)
	if !fallthrough_.Matched || fallthrough_.Target != "http://reads" {
		t.Fatalf("a method mismatch should fall through to the next candidate; got %+v", fallthrough_)
	}
}

// TestRouteTestRawQueryIsNotDerivedFromThePath pins §14's rule that a "?" in
// path stays a literal, so today's callers keep working unchanged.
func TestRouteTestRawQueryIsNotDerivedFromThePath(t *testing.T) {
	s := predicateRouteTestServer(t)

	out := postRouteTest(t, s, `{
		"method":"POST",
		"path":"/api/users?version=v2",
		"host":"example.com",
		"headers":{"X-Tenant":"public"}
	}`)
	if out.Target == "http://writes" {
		t.Fatal("a \"?\" inside path must stay a literal rather than becoming a query string")
	}
}

// TestRouteTestHeaderValuesExpressRepeatedFieldLines is the case a
// map[string]string cannot carry, which is why the field was added.
func TestRouteTestHeaderValuesExpressRepeatedFieldLines(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{
					Match: config.MatchConfig{
						Type:    "prefix",
						Path:    "/",
						Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: routeTestStrPtr("second")}},
					},
					Return: 204,
				},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})

	out := postRouteTest(t, s, `{
		"path":"/x",
		"header_values":[{"name":"X-Tenant","value":"first"},{"name":"X-Tenant","value":"second"}]
	}`)
	if !out.Matched {
		t.Fatalf("any one field line matching is a match; got %+v", out)
	}
}

// TestRouteTestNamesTheRejectedCandidateAndItsFailingPredicate is the whole
// point of the surface: a predicate mismatch is never logged per request, so
// this is the only place an operator can see one.
func TestRouteTestNamesTheRejectedCandidateAndItsFailingPredicate(t *testing.T) {
	s := predicateRouteTestServer(t)

	out := postRouteTest(t, s, `{"method":"GET","path":"/api/users","host":"example.com"}`)
	if len(out.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want the rejected route and the selected one", out.Candidates)
	}
	rejected := out.Candidates[0]
	if rejected.Selected || rejected.Rejection != "match.methods" {
		t.Errorf("rejected candidate = %+v, want a match.methods rejection", rejected)
	}
	if rejected.Tier != 2 {
		t.Errorf("tier = %d, want 2 (prefix)", rejected.Tier)
	}
	if !out.Candidates[1].Selected {
		t.Errorf("selected candidate = %+v, want the route below it", out.Candidates[1])
	}

	// A header mismatch is reported at its exact index, not as a generic failure.
	headerMiss := postRouteTest(t, s, `{
		"method":"POST",
		"path":"/api/users",
		"host":"example.com",
		"raw_query":"version=v2",
		"headers":{"X-Tenant":"private"}
	}`)
	if got := headerMiss.Candidates[0].Rejection; got != "match.headers[0]" {
		t.Errorf("rejection = %q, want match.headers[0]", got)
	}
}

// TestRouteTestReportsTheMatchOrdinal gives the Console the coordinate a typed
// patch needs to address one of several same-path routes.
func TestRouteTestReportsTheMatchOrdinal(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "prefix", Path: "/api/", Methods: []string{"POST"}}, Return: 201},
				{Match: config.MatchConfig{Type: "prefix", Path: "/api/", Methods: []string{"GET"}}, Return: 200},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})

	out := postRouteTest(t, s, `{"method":"GET","path":"/api/users"}`)
	if !out.Matched || out.MatchOrdinal != 1 {
		t.Fatalf("match_ordinal = %d, want 1 (the second route sharing these coordinates); got %+v", out.MatchOrdinal, out)
	}
}

// TestRouteTestExplainsAPredicateOnlyMiss keeps §7 visible: the path matched,
// the method did not, and the answer is still a 404 with no Allow header.
func TestRouteTestExplainsAPredicateOnlyMiss(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "prefix", Path: "/api/", Methods: []string{"POST"}}, Return: 201},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})

	out := postRouteTest(t, s, `{"method":"GET","path":"/api/users"}`)
	if out.Matched {
		t.Fatalf("expected no match; got %+v", out)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].Rejection != "match.methods" {
		t.Fatalf("candidates = %+v, want the path candidate and its failing predicate", out.Candidates)
	}
	for _, want := range []string{"404", "no 405"} {
		if !strings.Contains(out.Explanation, want) {
			t.Errorf("explanation %q should mention %q", out.Explanation, want)
		}
	}
}
