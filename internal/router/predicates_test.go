// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"jul/internal/config"
)

// These tests pin ADR 0018 §2-§5: the method rule, the three header operations,
// the two query operations, and the one Boolean rule that composes them.

func strPtr(s string) *string { return &s }

// predicateRoute compiles one location's predicates the way router.New does.
func predicateRoute(t *testing.T, m config.MatchConfig) *compiledPredicates {
	t.Helper()
	p, err := compilePredicates(config.LocationConfig{Match: m})
	if err != nil {
		t.Fatalf("compile predicates: %v", err)
	}
	return p
}

// matches evaluates a predicate set against a request.
func matches(p *compiledPredicates, req *http.Request) bool {
	q := requestQuery{raw: req.URL.RawQuery}
	ok, _ := p.match(req, &q)
	return ok
}

func TestMethodPredicateIsByteExact(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{Type: "prefix", Path: "/", Methods: []string{"GET", "POST"}})

	cases := map[string]bool{
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodPut:    false,
		http.MethodDelete: false,
		// HTTP methods are case-sensitive (RFC 9110 §9.1); nothing is folded.
		"get":  false,
		"Post": false,
	}
	for method, want := range cases {
		req := selectRequest("/x", "")
		req.Method = method
		if got := matches(p, req); got != want {
			t.Errorf("method %q matched = %v, want %v", method, got, want)
		}
	}
}

// TestAMethodPredicateListingGETAlsoMatchesHEAD pins §2's compatibility rule.
// RFC 9110 §9.3.2 defines HEAD as GET without a body, and a route that answers
// GET but 404s HEAD is a defect an operator would discover in production.
func TestAMethodPredicateListingGETAlsoMatchesHEAD(t *testing.T) {
	withGET := predicateRoute(t, config.MatchConfig{Type: "prefix", Path: "/", Methods: []string{"GET"}})
	req := selectRequest("/x", "")
	req.Method = http.MethodHead
	if !matches(withGET, req) {
		t.Error("a route listing GET should also match HEAD")
	}

	// HEAD alone matches HEAD only.
	headOnly := predicateRoute(t, config.MatchConfig{Type: "prefix", Path: "/", Methods: []string{"HEAD"}})
	if !matches(headOnly, req) {
		t.Error("a route listing HEAD should match HEAD")
	}
	req.Method = http.MethodGet
	if matches(headOnly, req) {
		t.Error("a route listing HEAD alone should not match GET")
	}
}

func TestAnExtensionMethodMatchesItselfOnly(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{Type: "prefix", Path: "/", Methods: []string{"PURGE"}})
	req := selectRequest("/x", "")
	req.Method = "PURGE"
	if !matches(p, req) {
		t.Error("an extension method should match itself")
	}
	req.Method = http.MethodGet
	if matches(p, req) {
		t.Error("an extension method should not match GET")
	}
}

// TestPreflightWideningAcceptsACORSPreflight pins §2's second compatibility
// rule at the level it is implemented, because the [servers.locations.cors]
// block that turns it on is ROUTE-02's (#146) to add. Without the rule a
// CORS-enabled route with methods = ["GET"] could never be selected for its own
// preflight, and the feature would silently not work on exactly the routes most
// likely to configure it.
func TestPreflightWideningAcceptsACORSPreflight(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{Type: "prefix", Path: "/", Methods: []string{"GET"}})
	p.preflightWidening = true

	preflight := selectRequest("/api/users", "")
	preflight.Method = http.MethodOptions
	preflight.Header.Set("Origin", "https://app.example.test")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	if !matches(p, preflight) {
		t.Error("a CORS-enabled route should be selected for its own preflight")
	}

	// A plain OPTIONS that is not a preflight still obeys the methods predicate.
	plain := selectRequest("/api/users", "")
	plain.Method = http.MethodOptions
	if matches(p, plain) {
		t.Error("a non-preflight OPTIONS should still be rejected by the methods predicate")
	}

	// Two Origin field lines is not "exactly one Origin".
	two := selectRequest("/api/users", "")
	two.Method = http.MethodOptions
	two.Header.Add("Origin", "https://a.example.test")
	two.Header.Add("Origin", "https://b.example.test")
	two.Header.Set("Access-Control-Request-Method", "POST")
	if matches(p, two) {
		t.Error("two Origin field lines is not a preflight")
	}

	// Without the widening bit the same preflight is rejected.
	p.preflightWidening = false
	if matches(p, preflight) {
		t.Error("without cors.enabled the preflight should obey the methods predicate")
	}
}

// TestCompilePredicatesReadsCORSEnabledFromConfig pins the config-level half of
// the rule above: compilePredicates derives preflightWidening from the real
// [servers.locations.cors] block via config.LocationPreflightWidening, not from
// a hand-set field. This is the one seam #146 turns on (ADR 0018 §2, §14).
func TestCompilePredicatesReadsCORSEnabledFromConfig(t *testing.T) {
	loc := config.LocationConfig{
		Match: config.MatchConfig{Type: "prefix", Path: "/", Methods: []string{"GET"}},
		CORS:  &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
	}
	p, err := compilePredicates(loc)
	if err != nil {
		t.Fatalf("compile predicates: %v", err)
	}

	preflight := selectRequest("/api/users", "")
	preflight.Method = http.MethodOptions
	preflight.Header.Set("Origin", "https://app.example.test")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	if !matches(p, preflight) {
		t.Error("a cors.enabled route with a methods predicate should be selected for its own preflight")
	}

	loc.CORS.Enabled = false
	p, err = compilePredicates(loc)
	if err != nil {
		t.Fatalf("compile predicates: %v", err)
	}
	if matches(p, preflight) {
		t.Error("cors.enabled = false should not widen the methods predicate for a preflight")
	}
}

func TestHeaderPresentIncludesPresentEmpty(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/",
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "present"}},
	})

	absent := selectRequest("/x", "")
	if matches(p, absent) {
		t.Error("an absent header should not be present")
	}
	empty := selectRequest("/x", "")
	empty.Header.Set("X-Tenant", "")
	if !matches(p, empty) {
		t.Error("a present-but-empty header should be present")
	}
}

// TestHeaderExactWithAnEmptyValueMatchesOnlyPresentEmpty is what makes absent
// and present-empty distinguishable, and is why the value is a pointer in the
// schema: an omitted value and an explicitly empty one never collapse.
func TestHeaderExactWithAnEmptyValueMatchesOnlyPresentEmpty(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/",
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: strPtr("")}},
	})

	absent := selectRequest("/x", "")
	if matches(p, absent) {
		t.Error("an absent header should not match an empty exact value")
	}
	empty := selectRequest("/x", "")
	empty.Header.Set("X-Tenant", "")
	if !matches(p, empty) {
		t.Error("a present-but-empty header should match an empty exact value")
	}
	nonEmpty := selectRequest("/x", "")
	nonEmpty.Header.Set("X-Tenant", "public")
	if matches(p, nonEmpty) {
		t.Error("a non-empty header should not match an empty exact value")
	}
}

func TestHeaderNamesMatchCaseInsensitivelyAndValuesDoNot(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/",
		Headers: []config.HeaderMatch{{Name: "x-TENANT", Op: "exact", Value: strPtr("public")}},
	})

	req := selectRequest("/x", "")
	req.Header.Set("X-Tenant", "public")
	if !matches(p, req) {
		t.Error("header names must match case-insensitively, as HTTP requires")
	}
	req.Header.Set("X-Tenant", "PUBLIC")
	if matches(p, req) {
		t.Error("header values are compared byte-exactly")
	}
}

// TestHeaderExactMatchesAnyOneFieldLineAndNeverSplitsOnCommas pins the two
// halves of §3's exact rule that an implementer is most likely to get wrong.
func TestHeaderExactMatchesAnyOneFieldLineAndNeverSplitsOnCommas(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/",
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: strPtr("b")}},
	})

	multi := selectRequest("/x", "")
	multi.Header.Add("X-Tenant", "a")
	multi.Header.Add("X-Tenant", "b")
	if !matches(p, multi) {
		t.Error("any one field line matching is a match")
	}

	// "a, b" is one value. Splitting it would be wrong for Date, Set-Cookie and
	// every other field whose grammar admits a comma.
	combined := selectRequest("/x", "")
	combined.Header.Set("X-Tenant", "a, b")
	if matches(p, combined) {
		t.Error("a comma-combined value must not be split")
	}
}

func TestHeaderRegexIsUnanchoredAndPerValue(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/",
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "regex", Value: strPtr("pub")}},
	})

	req := selectRequest("/x", "")
	req.Header.Set("X-Tenant", "not-public-either")
	if !matches(p, req) {
		t.Error("header regexes are unanchored, like the existing path matcher")
	}

	anchored := predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/",
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "regex", Value: strPtr("^public$")}},
	})
	if matches(anchored, req) {
		t.Error("an operator who means the whole value writes ^…$")
	}

	multi := selectRequest("/x", "")
	multi.Header.Add("X-Tenant", "private")
	multi.Header.Add("X-Tenant", "public")
	if !matches(anchored, multi) {
		t.Error("a regex applies to each value independently and matches when any does")
	}
}

func TestQueryPredicateSemantics(t *testing.T) {
	present := predicateRoute(t, config.MatchConfig{
		Type:  "prefix",
		Path:  "/",
		Query: []config.QueryMatch{{Name: "version", Op: "present"}},
	})
	exact := predicateRoute(t, config.MatchConfig{
		Type:  "prefix",
		Path:  "/",
		Query: []config.QueryMatch{{Name: "version", Op: "exact", Value: strPtr("v2")}},
	})
	empty := predicateRoute(t, config.MatchConfig{
		Type:  "prefix",
		Path:  "/",
		Query: []config.QueryMatch{{Name: "version", Op: "exact", Value: strPtr("")}},
	})

	cases := []struct {
		raw                               string
		wantPresent, wantExact, wantEmpty bool
	}{
		{"", false, false, false},
		{"version", true, false, true},               // "?x" is present, with an empty value
		{"version=", true, false, true},              // and so is "?x="
		{"version=v2", true, true, false},            //
		{"version=v1&version=v2", true, true, false}, // any occurrence matching is a match
		{"other=v2", false, false, false},
		{"version=v%32", true, true, false},  // %32 decodes to "2"
		{"version=a+b", true, false, false},  // "+" decodes to a space
		{"version=%zz", false, false, false}, // a malformed escape makes only that pair absent
		{"version=%zz&version=v2", true, true, false},
		{"a;version=v2", false, false, false}, // ";" is not a separator
	}
	for _, tc := range cases {
		req := selectRequest("/x", tc.raw)
		if got := matches(present, req); got != tc.wantPresent {
			t.Errorf("query %q present = %v, want %v", tc.raw, got, tc.wantPresent)
		}
		if got := matches(exact, selectRequest("/x", tc.raw)); got != tc.wantExact {
			t.Errorf("query %q exact = %v, want %v", tc.raw, got, tc.wantExact)
		}
		if got := matches(empty, selectRequest("/x", tc.raw)); got != tc.wantEmpty {
			t.Errorf("query %q exact-empty = %v, want %v", tc.raw, got, tc.wantEmpty)
		}
	}
}

// TestQueryParsingIsBounded pins §16's per-request cap. max_header_bytes bounds
// the request line at a size that still admits hundreds of thousands of empty
// pairs, which is an allocation amplifier a routing decision must not expose.
func TestQueryParsingIsBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < config.MaxQueryPairsParsed+64; i++ {
		fmt.Fprintf(&b, "k%d=v&", i)
	}
	b.WriteString("last=1")
	q := requestQuery{raw: b.String()}

	q.ensureParsed()
	if len(q.pairs) != config.MaxQueryPairsParsed {
		t.Errorf("parsed %d pairs, want the %d-pair cap", len(q.pairs), config.MaxQueryPairsParsed)
	}
	if q.present("last") {
		t.Error("a pair past the cap must not be parsed")
	}
	if !q.present("k0") {
		t.Error("the pairs within the cap must be parsed")
	}
}

// TestQueryIsParsedAtMostOnceAndOnlyWhenNeeded pins the property that keeps the
// path-only fast path untouched: a configuration with no query predicate never
// parses a query string at all.
func TestQueryIsParsedAtMostOnceAndOnlyWhenNeeded(t *testing.T) {
	// Evaluating a route that carries no query predicate must leave the query
	// untouched, however many other predicates it has to check.
	noQuery := predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/",
		Methods: []string{"GET"},
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "present"}},
	})
	req := selectRequest("/x", "a=1")
	req.Header.Set("X-Tenant", "public")
	unused := requestQuery{raw: req.URL.RawQuery}
	if ok, _ := noQuery.match(req, &unused); !ok {
		t.Fatal("expected the request to match")
	}
	if unused.parsed {
		t.Error("a configuration with no query predicate must never parse a query string")
	}

	q := requestQuery{raw: "a=1"}
	if q.parsed {
		t.Error("the query must not be parsed before it is used")
	}
	if !q.present("a") {
		t.Fatal("expected a to be present")
	}
	// Changing the raw string after the first use must not change the answer:
	// the query is parsed at most once per request.
	q.raw = "b=2"
	if q.present("b") {
		t.Error("the query was reparsed on a later predicate")
	}
}

// TestPredicatesAreANDedAndListsAreORSets is §5's whole Boolean model.
func TestPredicatesAreANDedAndListsAreORSets(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/",
		Methods: []string{"GET", "POST"},
		Headers: []config.HeaderMatch{
			{Name: "X-Tenant", Op: "present"},
			{Name: "X-Tenant", Op: "exact", Value: strPtr("public")},
		},
		Query: []config.QueryMatch{{Name: "version", Op: "exact", Value: strPtr("v2")}},
	})

	full := selectRequest("/x", "version=v2")
	full.Method = http.MethodPost
	full.Header.Set("X-Tenant", "public")
	if !matches(p, full) {
		t.Fatal("every predicate satisfied should match")
	}

	// Dropping any one of them fails the whole route: separate fields and
	// separate table entries are ANDed.
	noQuery := selectRequest("/x", "")
	noQuery.Method = http.MethodPost
	noQuery.Header.Set("X-Tenant", "public")
	if matches(p, noQuery) {
		t.Error("a missing query predicate must fail the route")
	}
	wrongTenant := selectRequest("/x", "version=v2")
	wrongTenant.Method = http.MethodPost
	wrongTenant.Header.Set("X-Tenant", "private")
	if matches(p, wrongTenant) {
		t.Error("two entries naming the same header are two predicates and are ANDed")
	}
	wrongMethod := selectRequest("/x", "version=v2")
	wrongMethod.Method = http.MethodPut
	wrongMethod.Header.Set("X-Tenant", "public")
	if matches(p, wrongMethod) {
		t.Error("a method outside the OR-set must fail the route")
	}
}

// TestPredicateFailureNamesTheFailingPredicate pins the diagnostic the
// route-test surface renders. Nothing here is logged per request.
func TestPredicateFailureNamesTheFailingPredicate(t *testing.T) {
	p := predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/",
		Methods: []string{"POST"},
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "present"}},
		Query:   []config.QueryMatch{{Name: "version", Op: "present"}},
	})

	cases := []struct {
		name   string
		mutate func(*http.Request)
		want   string
	}{
		{"method", func(r *http.Request) {}, "match.methods"},
		{"header", func(r *http.Request) { r.Method = http.MethodPost }, "match.headers[0]"},
		{"query", func(r *http.Request) {
			r.Method = http.MethodPost
			r.Header.Set("X-Tenant", "public")
		}, "match.query[0]"},
	}
	for _, tc := range cases {
		req := selectRequest("/x", "")
		tc.mutate(req)
		q := requestQuery{raw: req.URL.RawQuery}
		ok, failure := p.match(req, &q)
		if ok {
			t.Fatalf("%s: expected the request to be rejected", tc.name)
		}
		if got := failure.field(); got != tc.want {
			t.Errorf("%s: rejection = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPathSpecificityOutranksPredicates is the property that makes the model
// explainable: predicates filter candidates within a tier and never promote one
// across tiers or across prefix lengths. There is no scoring anywhere.
func TestPathSpecificityOutranksPredicates(t *testing.T) {
	specific := &locationRoute{matchType: "prefix", path: "/api/v2/"}
	general := &locationRoute{matchType: "prefix", path: "/api/"}
	general.predicates = predicateRoute(t, config.MatchConfig{
		Type:    "prefix",
		Path:    "/api/",
		Methods: []string{"GET"},
		Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "present"}},
		Query:   []config.QueryMatch{{Name: "version", Op: "present"}},
	})
	s := testServerRoute(general, specific)

	req := selectRequest("/api/v2/users", "version=v2")
	req.Header.Set("X-Tenant", "public")
	if loc := s.selectLocation(req); loc != specific {
		t.Fatalf("the longer prefix must win even when the shorter one's predicates all match, got %s", describe(loc))
	}
}
