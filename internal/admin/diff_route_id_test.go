// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"testing"

	"jul/internal/config"
)

func sp(s string) *string { return &s }

func locWithID(id, path, target string) config.LocationConfig {
	l := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: path}, ProxyPass: target}
	if id != "" {
		l.RouteID = sp(id)
	}
	return l
}

func serverCfg(locs ...config.LocationConfig) *config.Config {
	return &config.Config{Servers: []config.ServerConfig{{Listen: ":8080", Locations: locs}}}
}

// TestDiffLocationsSameRouteIDCorrelatesAcrossPredicateChange proves a
// durable route_id correlates a route even when every predicate changed
// (ADR 0019 §7): this must render as a modification, not remove+add.
func TestDiffLocationsSameRouteIDCorrelatesAcrossPredicateChange(t *testing.T) {
	before := serverCfg(locWithID("r-same", "/old-path", "http://127.0.0.1:3000"))
	after := serverCfg(locWithID("r-same", "/new-path", "http://127.0.0.1:4000"))
	d := diffConfigs(before, after)
	if len(d.Additions) != 0 || len(d.Removals) != 0 {
		t.Fatalf("same route_id across a predicate change should correlate as a modification, got additions=%v removals=%v", d.Additions, d.Removals)
	}
	if !diffHas(d, "Change target of route") {
		t.Errorf("expected a target-change modification, got %+v", d.Modifications)
	}
}

// TestDiffLocationsDifferingRouteIDIsRemoveAndAdd proves two different
// route_ids are never the same resource even if the fingerprint matches.
func TestDiffLocationsDifferingRouteIDIsRemoveAndAdd(t *testing.T) {
	before := serverCfg(locWithID("r-old", "/api", "http://127.0.0.1:3000"))
	after := serverCfg(locWithID("r-new", "/api", "http://127.0.0.1:3000"))
	d := diffConfigs(before, after)
	if len(d.Additions) != 1 || len(d.Removals) != 1 {
		t.Fatalf("differing route_id should be remove+add, got additions=%d removals=%d", len(d.Additions), len(d.Removals))
	}
	if len(d.Modifications) != 0 {
		t.Errorf("differing route_id should not be reported as a modification, got %+v", d.Modifications)
	}
}

// TestDiffLocationsRouteIDIntroducedIsRemoveAndAdd proves a route_id
// appearing where there was none before is not a coincidental fingerprint
// match — it is a remove+add per ADR 0019 §7.
func TestDiffLocationsRouteIDIntroducedIsRemoveAndAdd(t *testing.T) {
	before := serverCfg(locWithID("", "/api", "http://127.0.0.1:3000"))
	after := serverCfg(locWithID("r-new", "/api", "http://127.0.0.1:3000"))
	d := diffConfigs(before, after)
	if len(d.Additions) != 1 || len(d.Removals) != 1 {
		t.Fatalf("introducing a route_id should be remove+add, got additions=%d removals=%d", len(d.Additions), len(d.Removals))
	}
}

// TestDiffLocationsRouteIDRemovedIsRemoveAndAdd mirrors the introduced case:
// a route_id disappearing is also not the same resource.
func TestDiffLocationsRouteIDRemovedIsRemoveAndAdd(t *testing.T) {
	before := serverCfg(locWithID("r-old", "/api", "http://127.0.0.1:3000"))
	after := serverCfg(locWithID("", "/api", "http://127.0.0.1:3000"))
	d := diffConfigs(before, after)
	if len(d.Additions) != 1 || len(d.Removals) != 1 {
		t.Fatalf("removing a route_id should be remove+add, got additions=%d removals=%d", len(d.Additions), len(d.Removals))
	}
}

// TestDiffLocationsNeitherHasRouteIDFallsBackToFingerprint proves the
// pre-route_id fingerprint-based correlation is unchanged when neither side
// carries a durable identity.
func TestDiffLocationsNeitherHasRouteIDFallsBackToFingerprint(t *testing.T) {
	before := serverCfg(locWithID("", "/api", "http://127.0.0.1:3000"))
	after := serverCfg(locWithID("", "/api", "http://127.0.0.1:4000"))
	d := diffConfigs(before, after)
	if len(d.Additions) != 0 || len(d.Removals) != 0 {
		t.Fatalf("matching fingerprint with neither side carrying a route_id should correlate as a modification, got additions=%v removals=%v", d.Additions, d.Removals)
	}
	if !diffHas(d, "Change target of route") {
		t.Errorf("expected a target-change modification, got %+v", d.Modifications)
	}
}

// TestDiffLocationsOneSidedRouteIDIsNotSameRouteEvenWithMatchingFingerprint
// is the most direct statement of the ADR 0019 §7 rule this task calls out
// by name: identical type+path+predicates does not make two locations the
// same resource when only one side has a route_id.
func TestDiffLocationsOneSidedRouteIDIsNotSameRouteEvenWithMatchingFingerprint(t *testing.T) {
	before := serverCfg(locWithID("", "/checkout", "http://127.0.0.1:3000"))
	after := serverCfg(locWithID("r-checkout", "/checkout", "http://127.0.0.1:3000"))
	d := diffConfigs(before, after)
	if len(d.Additions) != 1 {
		t.Fatalf("want exactly one addition, got %d: %+v", len(d.Additions), d.Additions)
	}
	if len(d.Removals) != 1 {
		t.Fatalf("want exactly one removal, got %d: %+v", len(d.Removals), d.Removals)
	}
	if len(d.Modifications) != 0 {
		t.Errorf("one-sided route_id must not render as a modification, got %+v", d.Modifications)
	}
}
