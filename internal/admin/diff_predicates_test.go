// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"

	"jul/internal/config"
)

// ADR 0018 §14: the diff correlates routes by their normalized predicate set,
// not by coordinates and not by ordinal. Coordinates alone stopped being unique
// the moment a location could carry predicates, and a colliding key drops one of
// two same-path routes out of the preview entirely — which is an operator
// approving a change they were never shown.

func diffStrPtr(s string) *string { return &s }

func samePathServer(locs ...config.LocationConfig) config.ServerConfig {
	return config.ServerConfig{Listen: ":8080", Locations: locs}
}

func route(methods []string, target string) config.LocationConfig {
	return config.LocationConfig{
		Match:     config.MatchConfig{Type: "prefix", Path: "/api/", Methods: methods},
		ProxyPass: target,
	}
}

// diffOf runs the location diff for one server block pair.
func diffOf(t *testing.T, before, after config.ServerConfig) *ConfigDiff {
	t.Helper()
	d := &ConfigDiff{}
	diffLocations(":8080", before.Locations, after.Locations, config.WAFConfig{}, config.WAFConfig{}, d)
	return d
}

func diffText(d *ConfigDiff) string {
	var b strings.Builder
	for _, group := range [][]DiffEntry{d.Additions, d.Removals, d.Modifications} {
		for _, e := range group {
			b.WriteString(e.Detail + " | " + e.Name + " | " + e.Before + " → " + e.After + "\n")
		}
	}
	for _, w := range d.Warnings {
		b.WriteString("WARN " + w + "\n")
	}
	return b.String()
}

// TestDiffDoesNotCollapseTwoSamePathRoutes is the regression: before the
// predicate-aware key, locationIndex kept only the last of two routes sharing a
// match type and path, so editing the first showed nothing at all.
func TestDiffDoesNotCollapseTwoSamePathRoutes(t *testing.T) {
	before := samePathServer(
		route([]string{"POST"}, "http://127.0.0.1:9001"),
		route([]string{"GET"}, "http://127.0.0.1:9002"),
	)
	after := samePathServer(
		route([]string{"POST"}, "http://127.0.0.1:9003"),
		route([]string{"GET"}, "http://127.0.0.1:9002"),
	)

	got := diffText(diffOf(t, before, after))
	if !strings.Contains(got, "9003") {
		t.Fatalf("editing the first of two same-path routes produced no diff entry:\n%s", got)
	}
	if strings.Contains(got, "9002 →") {
		t.Errorf("the untouched sibling route was reported as changed:\n%s", got)
	}
}

// TestDiffLabelsSamePathRoutesDistinguishably keeps the preview readable: two
// routes on one path must not render as the same line twice.
func TestDiffLabelsSamePathRoutesDistinguishably(t *testing.T) {
	post := route([]string{"POST"}, "http://127.0.0.1:9001")
	get := route([]string{"GET"}, "http://127.0.0.1:9002")

	if locationLabel(&post) == locationLabel(&get) {
		t.Fatalf("both routes render as %q", locationLabel(&post))
	}
	if !strings.Contains(locationLabel(&post), "POST") {
		t.Errorf("label %q should carry the predicate summary", locationLabel(&post))
	}

	// The summary names predicates without printing their values: a diff line is
	// not the place to echo a header value an operator may consider sensitive.
	withHeader := config.LocationConfig{Match: config.MatchConfig{
		Type:    "prefix",
		Path:    "/api/",
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: diffStrPtr("secret-tenant")}},
	}}
	label := locationLabel(&withHeader)
	if !strings.Contains(label, "X-Tenant exact") {
		t.Errorf("label %q should name the predicate", label)
	}
	if strings.Contains(label, "secret-tenant") {
		t.Errorf("label %q must not print the predicate value", label)
	}
}

// TestDiffCorrelationIsStableWhenASamePathRouteIsInserted is why the key is a
// predicate set rather than an ordinal: an ordinal would re-key every route
// below an insertion, rendering one added route as a mutation of all of them.
func TestDiffCorrelationIsStableWhenASamePathRouteIsInserted(t *testing.T) {
	before := samePathServer(route([]string{"GET"}, "http://127.0.0.1:9002"))
	after := samePathServer(
		route([]string{"POST"}, "http://127.0.0.1:9001"),
		route([]string{"GET"}, "http://127.0.0.1:9002"),
	)

	d := diffOf(t, before, after)
	if len(d.Additions) != 1 {
		t.Errorf("additions = %d, want exactly 1:\n%s", len(d.Additions), diffText(d))
	}
	if len(d.Modifications) != 0 {
		t.Errorf("inserting a route above an existing one reported %d mutations of it:\n%s", len(d.Modifications), diffText(d))
	}
}

// TestDiffDoesNotWarnWhenTheCoordinatesSurvive keeps the removal warning
// truthful. Editing a predicate re-keys a route, so it renders as a removal plus
// an addition; warning that traffic will stop being handled would be false when
// another route still covers the same path.
func TestDiffDoesNotWarnWhenTheCoordinatesSurvive(t *testing.T) {
	before := samePathServer(route([]string{"GET"}, "http://127.0.0.1:9002"))
	after := samePathServer(route([]string{"GET", "POST"}, "http://127.0.0.1:9002"))

	for _, w := range diffOf(t, before, after).Warnings {
		if strings.Contains(w, "stop matching requests") {
			t.Errorf("widening a method predicate warned about traffic loss: %s", w)
		}
	}

	// A genuine removal still warns.
	gone := diffOf(t, before, samePathServer())
	found := false
	for _, w := range gone.Warnings {
		if strings.Contains(w, "stop matching requests") {
			found = true
		}
	}
	if !found {
		t.Error("removing the only route on a path should still warn")
	}
}
