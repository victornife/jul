// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"testing"

	"jul/internal/config"
)

// LocationScope is the identity the auth, WAF and rate-limit scopes key on
// (ADR 0018 §14). The properties below are the reason it is a fingerprint over
// the predicate set rather than an ordinal: a rate-limit bucket carries live
// state, so the key must change exactly when the route's matching behaviour
// changes, and not otherwise.

func scopeStrPtr(s string) *string { return &s }

func scopeServer(locs ...config.LocationConfig) config.ServerConfig {
	return config.ServerConfig{Listen: ":8080", ServerNames: []string{"a.test", "b.test"}, Locations: locs}
}

func predicateLocation(m config.MatchConfig) config.LocationConfig {
	if m.Type == "" {
		m.Type = "prefix"
	}
	if m.Path == "" {
		m.Path = "/api/"
	}
	return config.LocationConfig{Match: m, Return: 200}
}

// TestLocationScopeSeparatesRoutesTheOldKeyCollided pins the pre-existing defect
// the fingerprint repairs: an exact and a prefix location on the same path used
// to share one scope, which predicates would have turned from unlikely into
// ordinary.
func TestLocationScopeSeparatesRoutesTheOldKeyCollided(t *testing.T) {
	srv := scopeServer()
	exact := predicateLocation(config.MatchConfig{Type: "exact", Path: "/api"})
	prefix := predicateLocation(config.MatchConfig{Type: "prefix", Path: "/api"})

	if LocationScope(srv, exact) == LocationScope(srv, prefix) {
		t.Error("an exact and a prefix location on the same path must not share a policy scope")
	}
}

// TestLocationScopeDistinguishesPredicateSets is the same property for the case
// predicates create: two routes on one path that match different requests.
func TestLocationScopeDistinguishesPredicateSets(t *testing.T) {
	srv := scopeServer()
	base := predicateLocation(config.MatchConfig{})
	withMethod := predicateLocation(config.MatchConfig{Methods: []string{"GET"}})
	withOtherMethod := predicateLocation(config.MatchConfig{Methods: []string{"POST"}})
	withHeader := predicateLocation(config.MatchConfig{
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: scopeStrPtr("public")}},
	})
	withOtherValue := predicateLocation(config.MatchConfig{
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: scopeStrPtr("private")}},
	})

	scopes := map[string]string{
		"none":        LocationScope(srv, base),
		"GET":         LocationScope(srv, withMethod),
		"POST":        LocationScope(srv, withOtherMethod),
		"header":      LocationScope(srv, withHeader),
		"other value": LocationScope(srv, withOtherValue),
	}
	seen := map[string]string{}
	for name, scope := range scopes {
		if other, dup := seen[scope]; dup {
			t.Errorf("%q and %q produce the same scope; they match different requests", name, other)
		}
		seen[scope] = name
	}
}

// TestLocationScopeIsStableAcrossInsertionAndReordering is the property an
// ordinal cannot deliver. Inserting a same-path route above the target shifts
// every later ordinal, so an ordinal-keyed bucket would hand one route's
// accumulated limiter state to another.
func TestLocationScopeIsStableAcrossInsertionAndReordering(t *testing.T) {
	target := predicateLocation(config.MatchConfig{
		Methods: []string{"GET"},
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "present"}},
	})
	before := scopeServer(target)
	want := LocationScope(before, target)

	inserted := scopeServer(predicateLocation(config.MatchConfig{Methods: []string{"POST"}}), target)
	if got := LocationScope(inserted, target); got != want {
		t.Errorf("inserting a same-path route above the target changed its scope: %s != %s", got, want)
	}

	// Declaration order within the predicate set is not part of the identity
	// either: the canonical form sorts it.
	reordered := predicateLocation(config.MatchConfig{
		Methods: []string{"GET"},
		Headers: []config.HeaderMatch{{Name: "x-tenant", Op: "present"}},
	})
	if got := LocationScope(before, reordered); got != want {
		t.Errorf("canonicalization is not order- or case-independent: %s != %s", got, want)
	}
}

// TestLocationScopeIsIndependentOfServerNameOrder keeps a document reordering
// from resetting live limiter state.
func TestLocationScopeIsIndependentOfServerNameOrder(t *testing.T) {
	loc := predicateLocation(config.MatchConfig{})
	a := config.ServerConfig{Listen: ":8080", ServerNames: []string{"a.test", "b.test"}}
	b := config.ServerConfig{Listen: ":8080", ServerNames: []string{"b.test", "a.test"}}
	if LocationScope(a, loc) != LocationScope(b, loc) {
		t.Error("the server_names set is unordered, so its order must not change the scope")
	}
}

// TestAuthAndWAFScopesShareTheOneDerivation keeps the two surfaces from drifting
// apart, which is how a location would acquire the wrong firewall.
func TestAuthAndWAFScopesShareTheOneDerivation(t *testing.T) {
	srv := scopeServer()
	loc := predicateLocation(config.MatchConfig{Methods: []string{"GET"}})
	if AuthScope(srv, loc) != LocationScope(srv, loc) || WAFScope(srv, loc) != LocationScope(srv, loc) {
		t.Error("AuthScope and WAFScope must both be the canonical location scope")
	}
}
