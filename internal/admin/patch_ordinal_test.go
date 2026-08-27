// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

// match_ordinal is the additive extension that keeps a route addressable once
// predicates let two locations share all four coordinates (ADR 0018 §14). It is
// a revision-relative *selector*, not an identity, which is why it is bound to a
// base_version and why the internal policy scopes use a fingerprint instead.

func ordinalPtr(i int) *int { return &i }

// samePathConfig has two routes on identical coordinates, distinguished only by
// their method predicate.
func samePathConfig() *config.Config {
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "prefix", Path: "/api/", Methods: []string{"POST"}}, ProxyPass: "http://127.0.0.1:9001"},
				{Match: config.MatchConfig{Type: "prefix", Path: "/api/", Methods: []string{"GET"}}, ProxyPass: "http://127.0.0.1:9002"},
			},
		}},
	}
}

func TestPatchWithoutAnOrdinalStillRejectsAnAmbiguousTarget(t *testing.T) {
	cfg := samePathConfig()
	_, err := applyPatch(cfg, patchRequest{
		Op:        "route_set_target",
		Listen:    ":8080",
		MatchType: "prefix",
		Path:      "/api/",
		Target:    "http://elsewhere",
	})
	if err == nil {
		t.Fatal("an ambiguous target must be rejected rather than guessed")
	}
	if !strings.Contains(err.Error(), "match_ordinal") {
		t.Errorf("error = %v, want it to point at match_ordinal", err)
	}
}

func TestPatchOrdinalSelectsTheIntendedRoute(t *testing.T) {
	for ordinal, want := range map[int]string{0: "http://127.0.0.1:9001", 1: "http://127.0.0.1:9002"} {
		cfg := samePathConfig()
		if _, err := applyPatch(cfg, patchRequest{
			Op:           "route_set_target",
			Listen:       ":8080",
			MatchType:    "prefix",
			Path:         "/api/",
			MatchOrdinal: ordinalPtr(ordinal),
			Target:       "http://127.0.0.1:9999",
		}); err != nil {
			t.Fatalf("ordinal %d: %v", ordinal, err)
		}
		if got := cfg.Servers[0].Locations[ordinal].ProxyPass; got != "http://127.0.0.1:9999" {
			t.Errorf("ordinal %d: target = %q, want the patched value (was %q)", ordinal, got, want)
		}
		other := 1 - ordinal
		if got := cfg.Servers[0].Locations[other].ProxyPass; got == "http://127.0.0.1:9999" {
			t.Errorf("ordinal %d: the sibling route at index %d was edited too", ordinal, other)
		}
	}
}

func TestPatchOrdinalOutOfRangeIsRejected(t *testing.T) {
	cfg := samePathConfig()
	_, err := applyPatch(cfg, patchRequest{
		Op:           "route_set_target",
		Listen:       ":8080",
		MatchType:    "prefix",
		Path:         "/api/",
		MatchOrdinal: ordinalPtr(2),
		Target:       "http://127.0.0.1:9999",
	})
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("error = %v, want an out-of-range rejection", err)
	}
}

// TestPatchOrdinalRequiresABaseVersion is the CAS binding. An empty
// base_version is an explicit force-apply, which is safe for a coordinate tuple
// that names a route and unsafe for an ordinal: inserting a same-path route
// above the target shifts every later ordinal, so a force-applied ordinal patch
// would edit a route the operator never previewed.
func TestPatchOrdinalRequiresABaseVersion(t *testing.T) {
	ops := []patchRequest{{
		Op:           "route_set_target",
		Listen:       ":8080",
		MatchType:    "prefix",
		Path:         "/api/",
		MatchOrdinal: ordinalPtr(0),
		Target:       "http://127.0.0.1:9999",
	}}

	_, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: samePathConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", ops)

	var conflict *patchVersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want a version conflict (409)", err)
	}
	if !strings.Contains(conflict.Error(), "base_version") {
		t.Errorf("conflict message = %q, want it to name base_version", conflict.Error())
	}
	if conflict.CurrentVersion == "" {
		t.Error("the conflict should carry the current version so the client can refresh")
	}
}

func TestPatchOrdinalWithAStaleBaseVersionIsRejected(t *testing.T) {
	ops := []patchRequest{{
		Op:           "route_set_target",
		Listen:       ":8080",
		MatchType:    "prefix",
		Path:         "/api/",
		MatchOrdinal: ordinalPtr(0),
		Target:       "http://127.0.0.1:9999",
	}}

	_, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config:  samePathConfig(),
		Version: "abc123",
		Live:    lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "stale000", ops)

	var conflict *patchVersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want a version conflict (409)", err)
	}
}

func TestPatchOrdinalWithACurrentBaseVersionApplies(t *testing.T) {
	ops := []patchRequest{{
		Op:           "route_set_target",
		Listen:       ":8080",
		MatchType:    "prefix",
		Path:         "/api/",
		MatchOrdinal: ordinalPtr(1),
		Target:       "http://127.0.0.1:9999",
	}}

	execution, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config:  samePathConfig(),
		Version: "abc123",
		Live:    lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "abc123", ops)
	if err != nil {
		t.Fatalf("executePatchBatch: %v", err)
	}
	if !execution.Valid {
		t.Fatalf("candidate valid = false; validation errors: %+v", execution.ValidationErrors)
	}
	if got := execution.CandidateConfig.Servers[0].Locations[1].ProxyPass; got != "http://127.0.0.1:9999" {
		t.Errorf("target = %q, want the ordinal-selected route patched", got)
	}
	if got := execution.CandidateConfig.Servers[0].Locations[0].ProxyPass; got != "http://127.0.0.1:9001" {
		t.Errorf("the sibling route was edited: %q", got)
	}
}

// TestPatchWithoutAnOrdinalIsUnaffected keeps every payload written before
// predicates existed working unchanged, including a force-apply.
func TestPatchWithoutAnOrdinalIsUnaffected(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://127.0.0.1:9001"},
			},
		}},
	}
	execution, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: cfg,
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{{
		Op:        "route_set_target",
		Listen:    ":8080",
		MatchType: "prefix",
		Path:      "/",
		Target:    "http://127.0.0.1:9002",
	}})
	if err != nil {
		t.Fatalf("a force-applied patch without an ordinal must still work: %v", err)
	}
	if got := execution.CandidateConfig.Servers[0].Locations[0].ProxyPass; got != "http://127.0.0.1:9002" {
		t.Errorf("target = %q, want http://127.0.0.1:9002", got)
	}
}
