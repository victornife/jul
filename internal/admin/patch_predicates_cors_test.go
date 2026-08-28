// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

// ── location_set_predicates / location_clear_predicates (#147) ────────────────

func strp(s string) *string { return &s }

func TestApplyPatchSetPredicatesMethodsOnly(t *testing.T) {
	c := crudConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Methods: &[]string{"GET", "HEAD"}},
	})
	if err != nil {
		t.Fatalf("location_set_predicates: %v", err)
	}
	loc := &c.Servers[0].Locations[0]
	if len(loc.Match.Methods) != 2 {
		t.Fatalf("methods = %v, want [GET HEAD]", loc.Match.Methods)
	}
	if loc.Match.Headers != nil || loc.Match.Query != nil {
		t.Errorf("headers/query facets should be untouched, got headers=%v query=%v", loc.Match.Headers, loc.Match.Query)
	}
	if !strings.Contains(summary, "methods=2") {
		t.Errorf("summary = %q, want methods=2", summary)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchSetPredicatesHeadersAndQuery(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{
			Headers: &[]headerPredicate{{Name: "X-Api-Version", Op: "exact", Value: strp("2")}},
			Query:   &[]queryPredicate{{Name: "debug", Op: "present"}},
		},
	}); err != nil {
		t.Fatalf("location_set_predicates: %v", err)
	}
	loc := &c.Servers[0].Locations[0]
	if len(loc.Match.Headers) != 1 || loc.Match.Headers[0].Name != "X-Api-Version" {
		t.Fatalf("headers = %+v", loc.Match.Headers)
	}
	if len(loc.Match.Query) != 1 || loc.Match.Query[0].Name != "debug" {
		t.Fatalf("query = %+v", loc.Match.Query)
	}
	if loc.Match.Methods != nil {
		t.Errorf("methods facet should be untouched, got %v", loc.Match.Methods)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchSetPredicatesSparseLeavesOtherFacetsAlone(t *testing.T) {
	c := crudConfig()
	loc := &c.Servers[0].Locations[0]
	loc.Match.Methods = []string{"POST"}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Query: &[]queryPredicate{{Name: "v", Op: "present"}}},
	}); err != nil {
		t.Fatalf("location_set_predicates: %v", err)
	}
	if len(loc.Match.Methods) != 1 || loc.Match.Methods[0] != "POST" {
		t.Errorf("methods facet should survive an edit that only names query, got %v", loc.Match.Methods)
	}
}

func TestApplyPatchSetPredicatesRejectsBadHeaderOp(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Headers: &[]headerPredicate{{Name: "X-A", Op: "bogus"}}},
	}); err == nil {
		t.Fatal("expected error: invalid header op")
	}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Headers: &[]headerPredicate{{Name: "X-A", Op: "exact"}}},
	}); err == nil {
		t.Fatal("expected error: value required for exact")
	}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Headers: &[]headerPredicate{{Name: "X-A", Op: "present", Value: strp("x")}}},
	}); err == nil {
		t.Fatal("expected error: value forbidden for present")
	}
}

func TestApplyPatchSetPredicatesRequiresAtLeastOneFacet(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{},
	}); err == nil {
		t.Fatal("expected error: at least one facet is required")
	}
}

func TestApplyPatchClearPredicates(t *testing.T) {
	c := crudConfig()
	c.Servers[0].Locations[0].Match.Methods = []string{"GET"}
	summary, err := applyPatch(c, patchRequest{
		Op: "location_clear_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
	})
	if err != nil {
		t.Fatalf("location_clear_predicates: %v", err)
	}
	loc := &c.Servers[0].Locations[0]
	if loc.Match.HasPredicates() {
		t.Errorf("expected no predicates after clear, got %+v", loc.Match)
	}
	if !strings.Contains(summary, "cleared") {
		t.Errorf("summary = %q, want mention of cleared", summary)
	}
}

func TestApplyPatchClearPredicatesRefusesWhenNone(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_clear_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
	}); err == nil {
		t.Fatal("expected error: no predicates to clear")
	}
}

// ── location_response_headers_set / _clear (#147) ──────────────────────────────

func TestApplyPatchResponseHeadersSet(t *testing.T) {
	c := crudConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "location_response_headers_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		ResponseHeaders: &[]responseHeaderOpPatch{
			{Op: "set", Name: "X-Frame-Options", Value: strp("DENY")},
			{Op: "remove", Name: "Server"},
		},
	})
	if err != nil {
		t.Fatalf("location_response_headers_set: %v", err)
	}
	ops := c.Servers[0].Locations[0].ResponseHeaders
	if len(ops) != 2 || ops[0].Name != "X-Frame-Options" || ops[1].Op != "remove" {
		t.Fatalf("unexpected response headers: %+v", ops)
	}
	if !strings.Contains(summary, "2 operation") {
		t.Errorf("summary = %q, want 2 operation(s)", summary)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchResponseHeadersSetRejectsBadOp(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_response_headers_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		ResponseHeaders: &[]responseHeaderOpPatch{{Op: "bogus", Name: "X-A", Value: strp("v")}},
	}); err == nil {
		t.Fatal("expected error: invalid op")
	}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_response_headers_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		ResponseHeaders: &[]responseHeaderOpPatch{{Op: "set", Name: "X-A"}},
	}); err == nil {
		t.Fatal("expected error: value required for set")
	}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_response_headers_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		ResponseHeaders: &[]responseHeaderOpPatch{{Op: "remove", Name: "X-A", Value: strp("v")}},
	}); err == nil {
		t.Fatal("expected error: value forbidden for remove")
	}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_response_headers_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		ResponseHeaders: &[]responseHeaderOpPatch{{Op: "set", Value: strp("v")}},
	}); err == nil {
		t.Fatal("expected error: name required")
	}
}

func TestApplyPatchResponseHeadersClear(t *testing.T) {
	c := crudConfig()
	c.Servers[0].Locations[0].ResponseHeaders = []config.ResponseHeaderOp{{Op: "set", Name: "X-A", Value: strp("v")}}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_response_headers_clear", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
	}); err != nil {
		t.Fatalf("location_response_headers_clear: %v", err)
	}
	if c.Servers[0].Locations[0].ResponseHeaders != nil {
		t.Errorf("expected response headers cleared, got %+v", c.Servers[0].Locations[0].ResponseHeaders)
	}
}

func TestApplyPatchResponseHeadersClearRefusesWhenNone(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_response_headers_clear", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
	}); err == nil {
		t.Fatal("expected error: nothing to clear")
	}
}

// ── location_cors_set / location_cors_clear (#147) ─────────────────────────────

func TestApplyPatchCORSSet(t *testing.T) {
	c := crudConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "location_cors_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		CORS: &corsPatch{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.test"},
			MaxAge:         strp("10m"),
		},
	})
	if err != nil {
		t.Fatalf("location_cors_set: %v", err)
	}
	cors := c.Servers[0].Locations[0].CORS
	if cors == nil || !cors.Enabled || len(cors.AllowedOrigins) != 1 {
		t.Fatalf("unexpected cors: %+v", cors)
	}
	if cors.MaxAge == nil || cors.MaxAge.Std() != 10*time.Minute {
		t.Errorf("max_age = %v, want 10m", cors.MaxAge)
	}
	if !strings.Contains(summary, "enabled") {
		t.Errorf("summary = %q, want enabled", summary)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchCORSSetRejectsBadMaxAge(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_cors_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		CORS: &corsPatch{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}, MaxAge: strp("not-a-duration")},
	}); err == nil {
		t.Fatal("expected error: invalid max_age")
	}
}

func TestApplyPatchCORSSetInvalidCandidateRejectedByValidate(t *testing.T) {
	c := crudConfig()
	// enabled with no allowed_origins is invalid per ADR 0018 §9; applyPatch
	// itself does not duplicate that check, but the validated re-parse must
	// still reject the candidate before it is ever applied.
	if _, err := applyPatch(c, patchRequest{
		Op: "location_cors_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		CORS: &corsPatch{Enabled: true},
	}); err != nil {
		t.Fatalf("location_cors_set: %v", err)
	}
	raw, err := config.Marshal(c)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	parsed, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse candidate: %v", err)
	}
	if err := config.Validate(parsed); err == nil {
		t.Fatal("expected the validated re-parse to reject cors.enabled with no allowed_origins")
	}
}

func TestApplyPatchCORSClear(t *testing.T) {
	c := crudConfig()
	c.Servers[0].Locations[0].CORS = &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_cors_clear", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
	}); err != nil {
		t.Fatalf("location_cors_clear: %v", err)
	}
	if c.Servers[0].Locations[0].CORS != nil {
		t.Errorf("expected cors cleared, got %+v", c.Servers[0].Locations[0].CORS)
	}
}

func TestApplyPatchCORSClearRefusesWhenNone(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_cors_clear", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
	}); err == nil {
		t.Fatal("expected error: no cors policy to clear")
	}
}

// ── missing-payload and route-not-found branches (#147, Codecov patch coverage) ─

func TestApplyPatchSetPredicatesRequiresPayload(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
	}); err == nil {
		t.Fatal("expected error: predicates payload is required")
	}
}

func TestApplyPatchResponseHeadersSetRequiresPayload(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_response_headers_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
	}); err == nil {
		t.Fatal("expected error: response_headers payload is required")
	}
}

func TestApplyPatchCORSSetRequiresPayload(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_cors_set", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
	}); err == nil {
		t.Fatal("expected error: cors_set payload is required")
	}
}

func TestApplyPatchNewOpsRouteNotFound(t *testing.T) {
	c := crudConfig()
	missing := patchRequest{Op: "", Listen: ":9999", MatchType: "prefix", Path: "/nope"}
	cases := []patchRequest{
		{Op: "location_set_predicates", Predicates: &locationPredicates{Methods: &[]string{"GET"}}},
		{Op: "location_clear_predicates"},
		{Op: "location_response_headers_set", ResponseHeaders: &[]responseHeaderOpPatch{{Op: "set", Name: "X-A", Value: strp("v")}}},
		{Op: "location_response_headers_clear"},
		{Op: "location_cors_set", CORS: &corsPatch{Enabled: true, AllowedOrigins: []string{"https://a.example"}}},
		{Op: "location_cors_clear"},
	}
	for _, tc := range cases {
		req := tc
		req.Listen, req.MatchType, req.Path = missing.Listen, missing.MatchType, missing.Path
		if _, err := applyPatch(c, req); err == nil {
			t.Errorf("%s: expected a route-not-found error, got none", tc.Op)
		}
	}
}

// ── applyLocationPredicates: header/query name and query-op validation ─────────

func TestApplyPatchSetPredicatesRejectsEmptyHeaderName(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Headers: &[]headerPredicate{{Name: "  ", Op: "present"}}},
	}); err == nil {
		t.Fatal("expected error: header name is required")
	}
}

func TestApplyPatchSetPredicatesRejectsEmptyQueryName(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Query: &[]queryPredicate{{Name: "  ", Op: "present"}}},
	}); err == nil {
		t.Fatal("expected error: query name is required")
	}
}

func TestApplyPatchSetPredicatesRejectsBadQueryOp(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Query: &[]queryPredicate{{Name: "v", Op: "bogus"}}},
	}); err == nil {
		t.Fatal("expected error: invalid query op")
	}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Query: &[]queryPredicate{{Name: "v", Op: "exact"}}},
	}); err == nil {
		t.Fatal("expected error: value required for exact")
	}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_set_predicates", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/",
		Predicates: &locationPredicates{Query: &[]queryPredicate{{Name: "v", Op: "present", Value: strp("x")}}},
	}); err == nil {
		t.Fatal("expected error: value forbidden for present")
	}
}
