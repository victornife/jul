// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"math"
	"strings"
	"testing"
)

const testHeaderCeiling = 64 << 10

func TestRouteMinimumUninspectedNeedsNothing(t *testing.T) {
	req, err := MinimumBudget([]RouteFacts{{Route: "a"}, {Route: "b", RequestHeaderBytes: 1 << 30}}, BaseChargeBytes, testHeaderCeiling)
	if err != nil || req.Bytes != 0 || req.InspectedRoutes != 0 {
		t.Fatalf("uninspected routes = %+v, %v", req, err)
	}
	limits := Limits{Mode: Enforce, MaxBytes: 1}
	if err := CheckBudget(limits, req); err != nil {
		t.Fatalf("uninspected configuration rejected: %v", err)
	}
}

func TestRouteMinimumPluginBodyReader(t *testing.T) {
	route := RouteFacts{Route: "servers[0].locations[0]", Plugins: []PluginFacts{{
		Name: "guard", MaxRequestBody: 16 << 20, MaxResponseBody: 8 << 20, ReadsBody: true,
	}}}
	req, err := RouteMinimum(route, BaseChargeBytes, testHeaderCeiling)
	if err != nil {
		t.Fatal(err)
	}
	// Prepared body plus its final growth copy; no WAF terms.
	want := BaseChargeBytes + 2*(16<<20+1)
	if req.Bytes != want {
		t.Fatalf("plugin body route = %d, want %d (%s)", req.Bytes, want, req.Describe())
	}
	err = CheckBudget(Limits{Mode: Enforce, MaxBytes: 4 << 20}, Requirement{RouteRequirement: req, InspectedRoutes: 1})
	if err == nil || !strings.Contains(err.Error(), "servers[0].locations[0]") || !strings.Contains(err.Error(), "plugin request bodies") {
		t.Fatalf("4 MiB budget accepted for a 16 MiB plugin body: %v", err)
	}
	if err := CheckBudget(Limits{Mode: Enforce, MaxBytes: want}, Requirement{RouteRequirement: req}); err != nil {
		t.Fatalf("exact requirement rejected: %v", err)
	}
	if err := CheckBudget(Limits{Mode: Enforce, MaxBytes: want - 1}, Requirement{RouteRequirement: req}); err == nil {
		t.Fatal("one byte below the requirement accepted")
	}
}

func TestRouteMinimumPluginChainTerms(t *testing.T) {
	route := RouteFacts{Route: "r", Plugins: []PluginFacts{
		{Name: "a", MaxRequestBody: 100, ReadsBody: true, SetsRequestState: true},
		{Name: "b", MaxRequestBody: 300, ReadsBody: true, MaxResponseBody: 50, WritesResponseBody: true},
		{Name: "c", MaxFetchResponse: 1000, MemoryLimit: 2000, Fetch: true, MaxResponseBody: 70, SubscribesResponse: true, ReplacesResponseBody: true},
		{Name: "d", MaxResponseBody: 40, SubscribesResponse: true},
	}}
	req, err := RouteMinimum(route, 10, testHeaderCeiling)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(10) +
		(101 + 301) + 301 + // body readers + largest growth copy
		PluginRequestStateBytes +
		3*50 + // generated response
		1001 + 2000 + // fetch response + guest-bounded request copy
		3*70 + 2*40 // response subscriptions (c replaces)
	if req.Bytes != want {
		t.Fatalf("plugin chain = %d, want %d (%s)", req.Bytes, want, req.Describe())
	}
}

func TestRouteMinimumWAFTerms(t *testing.T) {
	// Compiled facts of Jul's default CRS policy: request_body_limit defaults
	// to 128 KiB, CRS sets a 128 KiB in-memory limit, and Coraza's response
	// body limit is 512 KiB.
	w := &WAFFacts{RequestBodyLimit: 128 << 10, RequestBodyInMemoryLimit: 128 << 10, ResponseBodyLimit: 512 << 10, ArgumentLimit: 1000}
	route := RouteFacts{Route: "r", WAF: w, RequestHeaderBytes: 1 << 20}
	req, err := RouteMinimum(route, BaseChargeBytes, testHeaderCeiling)
	if err != nil {
		t.Fatal(err)
	}
	args := WAFArgumentBytes * 1000
	want := BaseChargeBytes +
		2*(128<<10) + // request buffer, in-memory limit
		WAFReadBufferBytes + // Jul's read-loop scratch buffer
		WAFStateBaseBytes + args + 48*(1<<20+4096) + // R1
		args + 48*(128<<10) + // R2 uses B_total = request body limit
		48*testHeaderCeiling + // R3
		args + 48*(512<<10) + // R4
		2*(512<<10) + // response buffer
		WAFR5FixedBytes // R5 fixed
	if req.Bytes != want {
		t.Fatalf("WAF route = %d, want %d (%s)", req.Bytes, want, req.Describe())
	}
	if want > 256<<20 {
		t.Fatalf("default-sized WAF route %d B exceeds the 256 MiB default", want)
	}
	if !strings.HasPrefix(req.Describe(), "WAF R1") {
		t.Fatalf("dominant term not listed first: %s", req.Describe())
	}

	// An unreadable in-memory limit charges the whole body as buffered, and
	// the listener default applies when max_header_bytes is unset.
	w2 := *w
	w2.RequestBodyInMemoryLimit = 0
	w2.RequestBodyLimit = 1 << 20
	req2, err := RouteMinimum(RouteFacts{Route: "r", WAF: &w2}, BaseChargeBytes, testHeaderCeiling)
	if err != nil {
		t.Fatal(err)
	}
	if req2.Bytes != want-2*(128<<10)+2*(1<<20)+48*(1<<20-128<<10) {
		t.Fatalf("unknown in-memory limit = %d", req2.Bytes)
	}
}

func TestMinimumBudgetPicksWorstRoute(t *testing.T) {
	routes := []RouteFacts{
		{Route: "small", Plugins: []PluginFacts{{Name: "a", MaxRequestBody: 10, ReadsBody: true}}},
		{Route: "none"},
		{Route: "large", Plugins: []PluginFacts{{Name: "b", MaxRequestBody: 1000, ReadsBody: true}}},
		{Route: "tie", Plugins: []PluginFacts{{Name: "c", MaxRequestBody: 1000, ReadsBody: true}}},
	}
	req, err := MinimumBudget(routes, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if req.Route != "large" || req.InspectedRoutes != 3 || req.Bytes != 1+2*1001 {
		t.Fatalf("worst route = %+v", req)
	}
}

func TestRouteMinimumOverflowFailsClosed(t *testing.T) {
	cases := []RouteFacts{
		{Route: "body", Plugins: []PluginFacts{{Name: "a", MaxRequestBody: math.MaxUint64, ReadsBody: true}}},
		{Route: "resp", Plugins: []PluginFacts{{Name: "a", MaxResponseBody: math.MaxUint64 / 2, WritesResponseBody: true}}},
		{Route: "waf", WAF: &WAFFacts{RequestBodyLimit: math.MaxUint64 / 4}},
		{Route: "sum", Plugins: []PluginFacts{
			{Name: "a", MaxRequestBody: math.MaxUint64 / 2, ReadsBody: true},
			{Name: "b", MaxRequestBody: math.MaxUint64 / 2, ReadsBody: true},
		}},
	}
	for _, route := range cases {
		if _, err := RouteMinimum(route, BaseChargeBytes, testHeaderCeiling); err == nil || !strings.Contains(err.Error(), "overflows") {
			t.Fatalf("%s: overflow not reported: %v", route.Route, err)
		}
	}
}
