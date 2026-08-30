// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"jul/internal/config"
)

// crudConfig returns a small valid config with one server (":8080", names
// app.example) carrying a single "/" proxy route, plus an unreferenced "cache"
// upstream, for exercising the structured create/delete patch ops (HP-06).
func crudConfig() *config.Config {
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      ":8080",
			ServerNames: []string{"app.example"},
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://127.0.0.1:9000"},
			},
		}},
		Upstreams: []config.UpstreamConfig{{
			Name:    "cache",
			Servers: []config.UpstreamServer{{Address: "127.0.0.1:6000", Weight: 1}},
		}},
	}
}

// assertValidCandidate marshals the mutated config, then re-parses and validates
// it, proving the structured op produced a candidate the apply path accepts —
// exactly the preflight /api/config/patch/apply performs before persisting.
func assertValidCandidate(t *testing.T, c *config.Config) {
	t.Helper()
	raw, err := config.Marshal(c)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	parsed, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse candidate: %v", err)
	}
	if err := config.Validate(parsed); err != nil {
		t.Fatalf("candidate failed validation: %v\n%s", err, raw)
	}
}

// ── server_add ────────────────────────────────────────────────────────────────

func TestApplyPatchServerAddCreatesBlock(t *testing.T) {
	c := crudConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "server_add", Listen: ":9443", ServerNames: []string{"new.example"},
	})
	if err != nil {
		t.Fatalf("server_add: %v", err)
	}
	if len(c.Servers) != 2 {
		t.Fatalf("want 2 servers, got %d", len(c.Servers))
	}
	added := c.Servers[1]
	if added.Listen != ":9443" || len(added.ServerNames) != 1 || added.ServerNames[0] != "new.example" {
		t.Errorf("unexpected added server: %+v", added)
	}
	if !strings.Contains(summary, ":9443") {
		t.Errorf("summary = %q, want :9443", summary)
	}
	// A locationless server is a lint warning only, so the candidate is valid.
	assertValidCandidate(t, c)
}

func TestApplyPatchServerAddRejectsDuplicate(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "server_add", Listen: ":8080", ServerNames: []string{"app.example"},
	}); err == nil {
		t.Fatal("expected error: duplicate listen + server_names")
	}
}

func TestApplyPatchServerAddRequiresListen(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "server_add", ServerNames: []string{"x.example"},
	}); err == nil {
		t.Fatal("expected error: listen is required")
	}
}

// ── server_remove ─────────────────────────────────────────────────────────────

func TestApplyPatchServerRemoveDeletesBlock(t *testing.T) {
	c := crudConfig()
	c.Servers = append(c.Servers, config.ServerConfig{
		Listen:      ":9090",
		ServerNames: []string{"b.example"},
		Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Deny: true}},
	})
	summary, err := applyPatch(c, patchRequest{
		Op: "server_remove", Listen: ":9090", ServerNames: []string{"b.example"},
	})
	if err != nil {
		t.Fatalf("server_remove: %v", err)
	}
	if len(c.Servers) != 1 || c.Servers[0].Listen != ":8080" {
		t.Fatalf("wrong server removed: %+v", c.Servers)
	}
	if !strings.Contains(summary, ":9090") {
		t.Errorf("summary = %q, want :9090", summary)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchServerRemoveRefusesLastBlock(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "server_remove", Listen: ":8080", ServerNames: []string{"app.example"},
	}); err == nil {
		t.Fatal("expected error: cannot remove the only server block")
	}
}

func TestApplyPatchServerRemoveMissing(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "server_remove", Listen: ":9999", ServerNames: []string{"nope.example"},
	}); err == nil {
		t.Fatal("expected error: no server found")
	}
}

// ── location_add ──────────────────────────────────────────────────────────────

func TestApplyPatchLocationAddCreatesRoute(t *testing.T) {
	c := crudConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:  &locationMatch{Type: "prefix", Path: "/api"},
		Action: &locationActionPayload{Kind: "proxy", Target: "http://127.0.0.1:9100"},
	})
	if err != nil {
		t.Fatalf("location_add: %v", err)
	}
	locs := c.Servers[0].Locations
	if len(locs) != 2 {
		t.Fatalf("want 2 locations, got %d", len(locs))
	}
	added := locs[1]
	if added.Match.Path != "/api" || added.ProxyPass != "http://127.0.0.1:9100" {
		t.Errorf("unexpected added location: %+v", added)
	}
	if !strings.Contains(summary, "/api") {
		t.Errorf("summary = %q, want /api", summary)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchLocationAddDefaultsMatchType(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:  &locationMatch{Path: "/assets"}, // no type → prefix
		Action: &locationActionPayload{Kind: "static", Target: "/var/www"},
	}); err != nil {
		t.Fatalf("location_add: %v", err)
	}
	if got := c.Servers[0].Locations[1].Match.Type; got != "prefix" {
		t.Errorf("match type = %q, want prefix (default)", got)
	}
}

func TestApplyPatchLocationAddRejectsDuplicateMatch(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:  &locationMatch{Type: "prefix", Path: "/"},
		Action: &locationActionPayload{Kind: "deny"},
	}); err == nil {
		t.Fatal("expected error: route with match prefix / already exists")
	}
}

func TestApplyPatchLocationAddRequiresMatchAndAction(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Action: &locationActionPayload{Kind: "deny"},
	}); err == nil {
		t.Error("expected error: match_set is required")
	}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match: &locationMatch{Type: "prefix", Path: "/api"},
	}); err == nil {
		t.Error("expected error: action is required")
	}
}

func TestApplyPatchLocationAddServerNotFound(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":9999",
		Match:  &locationMatch{Type: "prefix", Path: "/x"},
		Action: &locationActionPayload{Kind: "deny"},
	}); err == nil {
		t.Fatal("expected error: no server found")
	}
}

func TestApplyPatchLocationAddMintsRouteIDWhenOmitted(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:  &locationMatch{Type: "prefix", Path: "/api"},
		Action: &locationActionPayload{Kind: "deny"},
	}); err != nil {
		t.Fatalf("location_add: %v", err)
	}
	added := c.Servers[0].Locations[1]
	if added.RouteID == nil || *added.RouteID == "" {
		t.Fatal("location_add should mint a non-empty route_id when omitted")
	}
	if !strings.HasPrefix(*added.RouteID, "r-") {
		t.Errorf("minted route_id = %q, want an \"r-\" prefix", *added.RouteID)
	}
	assertValidCandidate(t, c)

	// Minting must be non-deterministic across calls (CSPRNG-backed, not a
	// counter), otherwise two location_add calls in the same process could
	// collide.
	c2 := crudConfig()
	if _, err := applyPatch(c2, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:  &locationMatch{Type: "prefix", Path: "/api"},
		Action: &locationActionPayload{Kind: "deny"},
	}); err != nil {
		t.Fatalf("location_add: %v", err)
	}
	if *c2.Servers[0].Locations[1].RouteID == *added.RouteID {
		t.Error("two location_add calls minted the same route_id")
	}
}

func TestApplyPatchLocationAddAcceptsCallerSuppliedRouteID(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:   &locationMatch{Type: "prefix", Path: "/api"},
		Action:  &locationActionPayload{Kind: "deny"},
		RouteID: sp("checkout-api"),
	}); err != nil {
		t.Fatalf("location_add: %v", err)
	}
	added := c.Servers[0].Locations[1]
	if added.RouteID == nil || *added.RouteID != "checkout-api" {
		t.Errorf("route_id = %v, want \"checkout-api\"", added.RouteID)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchLocationAddCallerSuppliedIDIsNotNormalized(t *testing.T) {
	// A caller-supplied route_id must reach config.Validate byte-for-byte:
	// whitespace/case are not silently trimmed or folded into something
	// legal, they must be rejected as the malformed value they are.
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:   &locationMatch{Type: "prefix", Path: "/api"},
		Action:  &locationActionPayload{Kind: "deny"},
		RouteID: sp("abc "),
	}); err != nil {
		t.Fatalf("location_add: %v", err)
	}
	added := c.Servers[0].Locations[1]
	if added.RouteID == nil || *added.RouteID != "abc " {
		t.Fatalf("route_id = %v, want the exact unnormalized bytes %q", added.RouteID, "abc ")
	}
	if err := config.Validate(c); err == nil {
		t.Fatal("expected config.Validate to reject the untrimmed route_id")
	}
}

func TestApplyPatchLocationAddRejectsPresentEmptyRouteID(t *testing.T) {
	// route_id omitted (mint) and route_id = "" (present-empty, invalid) are
	// different requests and must not collapse onto the same behavior.
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:   &locationMatch{Type: "prefix", Path: "/api"},
		Action:  &locationActionPayload{Kind: "deny"},
		RouteID: sp(""),
	}); err != nil {
		t.Fatalf("location_add: %v", err)
	}
	added := c.Servers[0].Locations[1]
	if added.RouteID == nil || *added.RouteID != "" {
		t.Fatalf("route_id = %v, want a present-and-empty pointer", added.RouteID)
	}
	if err := config.Validate(c); err == nil {
		t.Fatal("expected config.Validate to reject a present-and-empty route_id")
	}
}

func TestApplyPatchLocationAddRejectsDuplicateRouteID(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:   &locationMatch{Type: "prefix", Path: "/api"},
		Action:  &locationActionPayload{Kind: "deny"},
		RouteID: sp("dup-id"),
	}); err != nil {
		t.Fatalf("location_add: %v", err)
	}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:   &locationMatch{Type: "prefix", Path: "/other"},
		Action:  &locationActionPayload{Kind: "deny"},
		RouteID: sp("dup-id"),
	}); err != nil {
		t.Fatalf("location_add: %v", err)
	}
	if err := config.Validate(c); err == nil {
		t.Fatal("expected config.Validate to reject the duplicate route_id")
	}
}

func TestApplyPatchLocationAddFailsClosedWhenCSPRNGUnavailable(t *testing.T) {
	old := routeIDRandReader
	defer func() { routeIDRandReader = old }()
	routeIDRandReader = iotest.ErrReader(errors.New("boom"))

	c := crudConfig()
	_, err := applyPatch(c, patchRequest{
		Op: "location_add", Listen: ":8080", ServerNames: []string{"app.example"},
		Match:  &locationMatch{Type: "prefix", Path: "/api"},
		Action: &locationActionPayload{Kind: "deny"},
	})
	if err == nil {
		t.Fatal("expected location_add to fail when the CSPRNG is unavailable, not mint a weaker id")
	}
	if len(c.Servers[0].Locations) != 1 {
		t.Fatalf("a failed mint must not append a location: got %d", len(c.Servers[0].Locations))
	}
}

func TestApplyPatchOnlyLocationAddMintsRouteID(t *testing.T) {
	// Every other patch op that touches a location must leave its route_id
	// untouched: durable identity is create-only (ADR 0019 §4).
	c := crudConfig()
	before := c.Servers[0].Locations[0].RouteID
	if before != nil {
		t.Fatal("test fixture route unexpectedly already has a route_id")
	}
	ops := []patchRequest{
		{Op: "route_set_target", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/", Target: "http://127.0.0.1:9200"},
		{Op: "route_toggle_cache", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/", Enabled: boolPtr(true)},
		{Op: "location_set_match", Listen: ":8080", ServerNames: []string{"app.example"}, MatchType: "prefix", Path: "/", Match: &locationMatch{Type: "prefix", Path: "/renamed"}},
	}
	for _, op := range ops {
		if _, err := applyPatch(c, op); err != nil {
			t.Fatalf("op %s: %v", op.Op, err)
		}
	}
	if c.Servers[0].Locations[0].RouteID != nil {
		t.Errorf("op %s minted or set a route_id on an existing location", ops[len(ops)-1].Op)
	}
}

// ── location_remove ───────────────────────────────────────────────────────────

func TestApplyPatchLocationRemoveDeletesRoute(t *testing.T) {
	c := crudConfig()
	c.Servers[0].Locations = append(c.Servers[0].Locations, config.LocationConfig{
		Match: config.MatchConfig{Type: "prefix", Path: "/api"}, Deny: true,
	})
	summary, err := applyPatch(c, patchRequest{
		Op: "location_remove", Listen: ":8080", ServerNames: []string{"app.example"},
		MatchType: "prefix", Path: "/api",
	})
	if err != nil {
		t.Fatalf("location_remove: %v", err)
	}
	locs := c.Servers[0].Locations
	if len(locs) != 1 || locs[0].Match.Path != "/" {
		t.Fatalf("wrong location removed: %+v", locs)
	}
	if !strings.Contains(summary, "/api") {
		t.Errorf("summary = %q, want /api", summary)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchLocationRemoveMissing(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_remove", Listen: ":8080", ServerNames: []string{"app.example"},
		MatchType: "prefix", Path: "/nope",
	}); err == nil {
		t.Fatal("expected error: no route found")
	}
}

// ── upstream_add ──────────────────────────────────────────────────────────────

func TestApplyPatchUpstreamAddCreatesPool(t *testing.T) {
	c := crudConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "upstream_add", Upstream: "web", Address: "127.0.0.1:9001", Weight: 3, Strategy: "least_conn",
	})
	if err != nil {
		t.Fatalf("upstream_add: %v", err)
	}
	if len(c.Upstreams) != 2 {
		t.Fatalf("want 2 upstreams, got %d", len(c.Upstreams))
	}
	added := c.Upstreams[1]
	if added.Name != "web" || added.Strategy != "least_conn" {
		t.Errorf("unexpected pool: %+v", added)
	}
	if len(added.Servers) != 1 || added.Servers[0].Address != "127.0.0.1:9001" || added.Servers[0].Weight != 3 {
		t.Errorf("unexpected backend: %+v", added.Servers)
	}
	if !strings.Contains(summary, "web") {
		t.Errorf("summary = %q, want web", summary)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchUpstreamAddDefaultsWeight(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "upstream_add", Upstream: "web", Address: "127.0.0.1:9001",
	}); err != nil {
		t.Fatalf("upstream_add: %v", err)
	}
	if got := c.Upstreams[1].Servers[0].Weight; got != 1 {
		t.Errorf("weight = %d, want default 1", got)
	}
}

func TestApplyPatchUpstreamAddRejectsDuplicate(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "upstream_add", Upstream: "cache", Address: "127.0.0.1:1",
	}); err == nil {
		t.Fatal("expected error: upstream named cache already exists")
	}
}

func TestApplyPatchUpstreamAddRequiresAddress(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "upstream_add", Upstream: "web",
	}); err == nil {
		t.Fatal("expected error: address is required")
	}
}

func TestApplyPatchUpstreamAddRejectsInvalidStrategy(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "upstream_add", Upstream: "web", Address: "127.0.0.1:1", Strategy: "random",
	}); err == nil {
		t.Fatal("expected error: invalid strategy")
	}
}

// ── upstream_remove ───────────────────────────────────────────────────────────

func TestApplyPatchUpstreamRemoveDeletesPool(t *testing.T) {
	c := crudConfig()
	summary, err := applyPatch(c, patchRequest{Op: "upstream_remove", Upstream: "cache"})
	if err != nil {
		t.Fatalf("upstream_remove: %v", err)
	}
	if len(c.Upstreams) != 0 {
		t.Fatalf("want 0 upstreams, got %d", len(c.Upstreams))
	}
	if !strings.Contains(summary, "cache") {
		t.Errorf("summary = %q, want cache", summary)
	}
	assertValidCandidate(t, c)
}

func TestApplyPatchUpstreamRemoveRefusesReferenced(t *testing.T) {
	c := crudConfig()
	c.Servers[0].Locations[0].ProxyPass = "http://cache"
	_, err := applyPatch(c, patchRequest{Op: "upstream_remove", Upstream: "cache"})
	if err == nil {
		t.Fatal("expected error: upstream still referenced")
	}
	if !strings.Contains(err.Error(), "/") {
		t.Errorf("error should name the referencing route, got %v", err)
	}
	if len(c.Upstreams) != 1 {
		t.Errorf("pool should be untouched after refusal, got %d", len(c.Upstreams))
	}
}

func TestApplyPatchUpstreamRemoveMissing(t *testing.T) {
	c := crudConfig()
	if _, err := applyPatch(c, patchRequest{Op: "upstream_remove", Upstream: "nope"}); err == nil {
		t.Fatal("expected error: no upstream named nope")
	}
}
