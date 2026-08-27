// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

// The unreachable-route rule is deliberately incomplete (ADR 0018 §15). A false
// "this route is unreachable" is worse than silence: an operator who acts on it
// deletes a route that was in fact reachable and loses traffic. The negative
// cases below are therefore as load-bearing as the positive ones.

// shadowConfig builds one server block holding the given locations in order.
func shadowConfig(matches ...MatchConfig) *Config {
	locs := make([]LocationConfig, 0, len(matches))
	for _, m := range matches {
		if m.Type == "" {
			m.Type = "prefix"
		}
		if m.Path == "" {
			m.Path = "/api/"
		}
		locs = append(locs, LocationConfig{Match: m, Return: 200})
	}
	return &Config{Servers: []ServerConfig{{Listen: ":8080", Locations: locs}}}
}

func requireUnreachable(t *testing.T, cfg *Config) {
	t.Helper()
	requireDiagnostic(t, Lint(cfg), SeverityWarning, "servers[0].locations[1]", "unreachable")
}

func requireReachable(t *testing.T, cfg *Config) {
	t.Helper()
	for _, d := range Lint(cfg) {
		if d.Field == "servers[0].locations[1]" && strings.Contains(d.Message, "unreachable") {
			t.Fatalf("locations[1] was reported unreachable, but the shadowing is not provable: %s", d.Message)
		}
	}
}

func TestUnreachableWhenTheEarlierRouteHasNoPredicates(t *testing.T) {
	requireUnreachable(t, shadowConfig(
		MatchConfig{},
		MatchConfig{Methods: []string{"GET"}},
	))
}

func TestUnreachableOnStructuralEquality(t *testing.T) {
	requireUnreachable(t, shadowConfig(
		MatchConfig{Methods: []string{"GET"}, Headers: []HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: sp("public")}}},
		MatchConfig{Methods: []string{"GET"}, Headers: []HeaderMatch{{Name: "x-tenant", Op: "exact", Value: sp("public")}}},
	))
}

// methods is an OR-set, so containment is the direction that matters: ["GET"]
// does not shadow ["GET", "POST"], because the later route is the only thing
// answering POST.
func TestMethodSubsumptionIsContainmentNotSubset(t *testing.T) {
	t.Run("the earlier set containing the later one shadows", func(t *testing.T) {
		requireUnreachable(t, shadowConfig(
			MatchConfig{Methods: []string{"GET", "POST"}},
			MatchConfig{Methods: []string{"GET"}},
		))
	})
	t.Run("a subset of methods shadows nothing", func(t *testing.T) {
		requireReachable(t, shadowConfig(
			MatchConfig{Methods: []string{"GET"}},
			MatchConfig{Methods: []string{"GET", "POST"}},
		))
	})
	t.Run("an unconstrained later route is not shadowed by a constrained earlier one", func(t *testing.T) {
		requireReachable(t, shadowConfig(
			MatchConfig{Methods: []string{"GET"}},
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "present"}}},
		))
	})
}

func TestPresentSubsumesAnyPredicateOnTheSameName(t *testing.T) {
	requireUnreachable(t, shadowConfig(
		MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "present"}}},
		MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "regex", Value: sp("^pub")}}},
	))
	requireUnreachable(t, shadowConfig(
		MatchConfig{Query: []QueryMatch{{Name: "v", Op: "present"}}},
		MatchConfig{Query: []QueryMatch{{Name: "v", Op: "exact", Value: sp("2")}}},
	))
}

// Everything else is unprovable and must produce no finding.
func TestUnprovableShadowingIsNotReported(t *testing.T) {
	cases := []struct {
		name   string
		config *Config
	}{
		{"two different regexes on the same header", shadowConfig(
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "regex", Value: sp("^pub")}}},
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "regex", Value: sp("^public$")}}},
		)},
		{"a regex against an exact", shadowConfig(
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "regex", Value: sp("^public$")}}},
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: sp("public")}}},
		)},
		{"an exact against a regex", shadowConfig(
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: sp("public")}}},
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "regex", Value: sp("^public$")}}},
		)},
		{"disjoint header names", shadowConfig(
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "present"}}},
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Region", Op: "present"}}},
		)},
		{"different exact values", shadowConfig(
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: sp("a")}}},
			MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: sp("b")}}},
		)},
		{"disjoint query names", shadowConfig(
			MatchConfig{Query: []QueryMatch{{Name: "a", Op: "present"}}},
			MatchConfig{Query: []QueryMatch{{Name: "b", Op: "present"}}},
		)},
		{"a different path is not a clash at all", shadowConfig(
			MatchConfig{Path: "/api/"},
			MatchConfig{Path: "/api/v2/"},
		)},
		{"a different match type is not a clash at all", shadowConfig(
			MatchConfig{Type: "exact", Path: "/api"},
			MatchConfig{Type: "prefix", Path: "/api"},
		)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { requireReachable(t, tc.config) })
	}
}

// TestExactDuplicateCoordinatesAreStillReported keeps the pre-ADR-0018 rule
// working: a plain duplicate is the structural-equality case.
func TestExactDuplicateCoordinatesAreStillReported(t *testing.T) {
	requireUnreachable(t, shadowConfig(MatchConfig{Path: "/api"}, MatchConfig{Path: "/api"}))
}

// TestCanonicalPredicatesIsOrderIndependent pins the normalization the policy
// scope and the lint both rely on, so the two can never disagree about what a
// predicate set is.
func TestCanonicalPredicatesIsOrderIndependent(t *testing.T) {
	a := MatchConfig{
		Methods: []string{"POST", "GET"},
		Headers: []HeaderMatch{
			{Name: "X-Region", Op: "present"},
			{Name: "x-tenant", Op: "exact", Value: sp("public")},
		},
		Query: []QueryMatch{{Name: "v", Op: "exact", Value: sp("2")}},
	}
	b := MatchConfig{
		Methods: []string{"GET", "POST"},
		Headers: []HeaderMatch{
			{Name: "X-Tenant", Op: "exact", Value: sp("public")},
			{Name: "X-Region", Op: "present"},
		},
		Query: []QueryMatch{{Name: "v", Op: "exact", Value: sp("2")}},
	}
	if a.CanonicalPredicates() != b.CanonicalPredicates() {
		t.Errorf("declaration order changed the canonical form:\n%s\n%s", a.CanonicalPredicates(), b.CanonicalPredicates())
	}

	// Omitted methods and an unconstrained-by-omission route are different from
	// any explicit set, and a value that contains the separator cannot
	// impersonate another predicate.
	none := MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "present"}}}
	if none.CanonicalPredicates() == (MatchConfig{Methods: []string{}, Headers: none.Headers}).CanonicalPredicates() {
		t.Error("omitted methods must not canonicalize like an empty list")
	}
	spoof := MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: sp("a|header|X-Other|present|-")}}}
	honest := MatchConfig{Headers: []HeaderMatch{
		{Name: "X-Tenant", Op: "exact", Value: sp("a")},
		{Name: "X-Other", Op: "present"},
	}}
	if spoof.CanonicalPredicates() == honest.CanonicalPredicates() {
		t.Error("a value containing the separator must not impersonate another predicate set")
	}
}
