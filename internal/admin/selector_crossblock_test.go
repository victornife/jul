// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"jul/internal/config"
	"jul/internal/router"
)

const duplicateBlockFixture = `
[[servers]]
listen = "127.0.0.1:8080"
server_names = ["a.test"]
[[servers.locations]]
match = { type = "prefix", path = "/" }
return = 200
[[servers]]
listen = "127.0.0.1:8080"
server_names = ["a.test"]
[[servers.locations]]
match = { type = "prefix", path = "/" }
return = 201
`

func TestProjectedSelectorRoundTripsAcrossBlocks(t *testing.T) {
	c, err := config.Parse([]byte(duplicateBlockFixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(c); err != nil {
		t.Fatal(err)
	}
	for si, srv := range projectRoutes(c) {
		for li, loc := range srv.Locations {
			ordinal := loc.MatchOrdinal
			gotServer, gotLocation, err := findLocationIndex(c, routeTarget{
				Listen: srv.Listen, ServerNames: srv.ServerNames,
				MatchType: loc.Type, Path: loc.Match, Ordinal: &ordinal, SelectorVersion: loc.SelectorVersion,
			})
			if err != nil || gotServer != si || gotLocation != li {
				t.Fatalf("projection [%d][%d] ordinal %d resolves to [%d][%d]: %v", si, li, ordinal, gotServer, gotLocation, err)
			}
		}
	}
}

// The oracle enumerates preceding declarations directly. It does not use the
// production counter, key encoding, or server-name equality helper.
func expectedSelectorOrdinal(c *config.Config, si, li int) int {
	targetServer := c.Servers[si]
	target := targetServer.Locations[li].Match
	names := append([]string{}, targetServer.ServerNames...)
	sort.Strings(names)
	ordinal := 0
	for i, srv := range c.Servers {
		otherNames := append([]string{}, srv.ServerNames...)
		sort.Strings(otherNames)
		if srv.Listen != targetServer.Listen || !reflect.DeepEqual(names, otherNames) {
			continue
		}
		for j, loc := range srv.Locations {
			if i == si && j == li {
				return ordinal
			}
			if loc.Match.Type == target.Type && loc.Match.Path == target.Path {
				ordinal++
			}
		}
	}
	panic("target missing")
}

func TestSelectorGeneratedRoundTrips(t *testing.T) {
	rng := rand.New(rand.NewSource(643))
	nameSets := [][]string{nil, {}, {""}, {"a.test"}, {"b.test", "a.test"}, {"a.test", "b.test"}, {"a.test", "a.test"}, {" a.test "}, {"A.test"}}
	for trial := 0; trial < 100; trial++ {
		c := &config.Config{}
		for si := 0; si < 12; si++ {
			srv := config.ServerConfig{Listen: []string{":8080", ":8081"}[rng.Intn(2)], ServerNames: nameSets[rng.Intn(len(nameSets))]}
			for li := 0; li < 6; li++ {
				srv.Locations = append(srv.Locations, config.LocationConfig{
					Match: config.MatchConfig{Type: []string{"exact", "prefix", "regex"}[rng.Intn(3)], Path: []string{"/", "/a", "/b"}[rng.Intn(3)]}, Return: 200,
				})
			}
			c.Servers = append(c.Servers, srv)
		}
		flat := v1Routes(c)
		n := 0
		for si, srv := range projectRoutes(c) {
			for li, loc := range srv.Locations {
				want := expectedSelectorOrdinal(c, si, li)
				if loc.MatchOrdinal != want || flat[n].Selector.MatchOrdinal != want {
					t.Fatalf("trial %d [%d][%d]: want %d, internal %d v1 %d", trial, si, li, want, loc.MatchOrdinal, flat[n].Selector.MatchOrdinal)
				}
				target := routeTarget{Listen: srv.Listen, ServerNames: srv.ServerNames, MatchType: loc.Type, Path: loc.Match, Ordinal: &loc.MatchOrdinal, SelectorVersion: loc.SelectorVersion}
				i, j, err := findLocationIndex(c, target)
				if err != nil || i != si || j != li {
					t.Fatalf("trial %d [%d][%d] -> [%d][%d]: %v", trial, si, li, i, j, err)
				}
				n++
			}
		}
		// Exercise actual emitted candidates from the real router. A shadowed
		// block need not be reachable; any emitted selector must round-trip.
		result := testRoute(c, routeTestRequest{Host: "a.test", Path: "/a"})
		rt, err := router.New(c, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		explained := rt.Explain("a.test", syntheticRequest(routeTestRequest{Host: "a.test", Path: "/a"}))
		if len(result.Candidates) != len(explained.Candidates) {
			t.Fatal("tester candidate count differs from real router")
		}
		if selected, ok := explained.SelectedCandidate(); ok {
			ordinal := result.MatchOrdinal
			i, j, err := findLocationIndex(c, routeTarget{Listen: result.Listen, ServerNames: result.ServerNames, MatchType: result.MatchType, Path: result.Match, Ordinal: &ordinal, SelectorVersion: result.SelectorVersion})
			if err != nil || i != explained.ServerIndex || j != selected.LocationIndex {
				t.Fatalf("selected tester result points to [%d][%d]: %v", i, j, err)
			}
		}
		for n, candidate := range result.Candidates {
			ord := candidate.MatchOrdinal
			i, j, err := findLocationIndex(c, routeTarget{Listen: result.Listen, ServerNames: result.ServerNames, MatchType: candidate.MatchType, Path: candidate.Match, Ordinal: &ord, SelectorVersion: candidate.SelectorVersion})
			if err != nil {
				t.Fatal(err)
			}
			if i != explained.ServerIndex || j != explained.Candidates[n].LocationIndex {
				t.Fatalf("tester candidate %d resolved to [%d][%d], router emitted [%d][%d]", n, i, j, explained.ServerIndex, explained.Candidates[n].LocationIndex)
			}
			if got := expectedSelectorOrdinal(c, i, j); got != ord {
				t.Fatalf("candidate ordinal %d, want %d", ord, got)
			}
		}
	}
}

func TestLegacyCrossBlockSelectorRefusedWithUnchangedRevision(t *testing.T) {
	c, err := config.Parse([]byte(duplicateBlockFixture))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := config.Marshal(c)
	version := configVersion(before)
	// Exactly the payload an old Console/API projection issued for block 2.
	legacy := patchRequest{Op: "location_set_action", Listen: c.Servers[1].Listen, ServerNames: c.Servers[1].ServerNames, MatchType: "prefix", Path: "/", MatchOrdinal: ordinalPtr(0), Action: &locationActionPayload{Kind: "return", Status: 202}}
	if _, err := executePatchBatch(context.Background(), patchBatchBaseline{Config: c, Version: version}, version, []patchRequest{legacy}); err == nil {
		t.Fatal("old selector silently accepted")
	}
	after, _ := config.Marshal(c)
	if string(before) != string(after) {
		t.Fatal("refusal mutated baseline")
	}
	for _, format := range []string{"", "future-format"} {
		_, _, err := findLocationIndex(c, routeTarget{Listen: c.Servers[0].Listen, ServerNames: c.Servers[0].ServerNames, MatchType: "prefix", Path: "/", Ordinal: ordinalPtr(0), SelectorVersion: format})
		if err == nil {
			t.Fatalf("unsafe format %q accepted", format)
		}
	}
}

func TestPublishedOrdinalCannotShiftInsideBatch(t *testing.T) {
	c, err := config.Parse([]byte(duplicateBlockFixture))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := config.Marshal(c)
	for _, first := range []patchRequest{
		{Op: "location_remove", Listen: c.Servers[0].Listen, ServerNames: c.Servers[0].ServerNames, MatchType: "prefix", Path: "/", MatchOrdinal: ordinalPtr(0), SelectorVersion: routeSelectorVersion},
		{Op: "location_set_match", Listen: c.Servers[0].Listen, ServerNames: c.Servers[0].ServerNames, MatchType: "prefix", Path: "/", MatchOrdinal: ordinalPtr(0), SelectorVersion: routeSelectorVersion, Match: &locationMatch{Type: "prefix", Path: "/moved"}},
	} {
		second := patchRequest{Op: "location_set_action", Listen: c.Servers[1].Listen, ServerNames: c.Servers[1].ServerNames, MatchType: "prefix", Path: "/", MatchOrdinal: ordinalPtr(1), SelectorVersion: routeSelectorVersion, Action: &locationActionPayload{Kind: "return", Status: 202}}
		if _, err := executePatchBatch(context.Background(), patchBatchBaseline{Config: c}, configVersion(base), []patchRequest{first, second}); err == nil {
			t.Fatalf("ordinal after %s was reinterpreted", first.Op)
		}
		after, _ := config.Marshal(c)
		if string(after) != string(base) {
			t.Fatal("rejected batch mutated baseline")
		}
	}
}

func TestParsedDefaultMatchSelector(t *testing.T) {
	c, err := config.Parse([]byte(`[[servers]]
listen = ":8080"
[[servers.locations]]
match = {path = "/"}
return = 200
[[servers]]
listen = ":8080"
server_names = []
[[servers.locations]]
match = {path = "/"}
return = 201
`))
	if err != nil {
		t.Fatal(err)
	}
	projected := projectRoutes(c)
	if projected[0].Locations[0].Type != "" || projected[0].Locations[0].Match != "/" {
		t.Fatal("fixture did not exercise parsed defaults")
	}
	if got := projected[1].Locations[0].MatchOrdinal; got != 1 {
		t.Fatalf("nil/empty parsed defaults ordinal %d", got)
	}
	result := testRoute(c, routeTestRequest{Path: "/", Host: "any.test"})
	if !result.Matched || result.MatchOrdinal != 0 || result.BaseVersion != projected[0].BaseVersion {
		t.Fatalf("tester did not preserve parsed selector: %+v", result)
	}
}
