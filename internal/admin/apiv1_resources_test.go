// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// resourceConfig is a configuration whose declaration order is deliberately not
// its lexical order, so a collection that sorted would be caught.
const resourceConfig = `
[global]
log_level = "info"

[[upstreams]]
name = "zebra"
strategy = "round_robin"
  [[upstreams.servers]]
  address = "127.0.0.1:9001"
  weight = 3
  [[upstreams.servers]]
  address = "127.0.0.1:9002"

[[upstreams]]
name = "alpha"
  [[upstreams.servers]]
  address = "127.0.0.1:9003"

[[servers]]
listen = "127.0.0.1:8443"
server_names = ["zeta.example.com"]

  [[servers.locations]]
  match = { type = "prefix", path = "/zzz" }
  route_id = "r-abcdefghijklmnopqrstuvwxyz"
  proxy_pass = "zebra"

  [[servers.locations]]
  match = { type = "prefix", path = "/aaa" }
  return = 204

[[servers]]
listen = "127.0.0.1:8080"
server_names = ["alpha.example.com"]

  [[servers.locations]]
  match = { type = "exact", path = "/mmm" }
  return = 200
`

func resourceServer(t *testing.T) *Server {
	t.Helper()
	cfg, err := config.Parse([]byte(resourceConfig))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
		Upstreams: func() []UpstreamStatus {
			// The runtime reports in a different order and only knows about one
			// pool, which is exactly the case a collection must not inherit.
			return []UpstreamStatus{{
				Name: "alpha", Strategy: "round_robin",
				Backends: []BackendStatus{{Address: "127.0.0.1:9003", State: "available", Inflight: 4}},
			}}
		},
	})
}

// TestV1RoutesAreInDeclarationOrder is the contract ADR 0018 makes part of
// routing itself: precedence is declaration order, so a collection that sorted
// or iterated a map would misrepresent which route wins.
func TestV1RoutesAreInDeclarationOrder(t *testing.T) {
	s := resourceServer(t)
	got := decodeInto[adminapi.RoutesResponse](t, getV1(t, s, "/api/v1/routes", ""))

	if len(got.Routes) != 3 {
		t.Fatalf("routes = %d, want 3: %+v", len(got.Routes), got.Routes)
	}
	wantPaths := []string{"/zzz", "/aaa", "/mmm"}
	for i, want := range wantPaths {
		if got.Routes[i].Selector.Path != want {
			t.Fatalf("route %d path = %q, want %q — the collection is not in declaration order (%v)",
				i, got.Routes[i].Selector.Path, want, wantPaths)
		}
	}
	// Server blocks too: 8443 is declared before 8080 despite sorting the other
	// way.
	if got.Routes[0].Selector.Listen != "127.0.0.1:8443" {
		t.Fatalf("first route listens on %q; server blocks are not in declaration order", got.Routes[0].Selector.Listen)
	}
}

// TestV1RoutesCarryASelectorAndABaseVersion. The selector is revision-scoped,
// so it is only usable with the version it was read at — publishing them
// together is what makes a selector-targeted mutation possible at all
// (ADR 0019 §32).
func TestV1RoutesCarryASelectorAndABaseVersion(t *testing.T) {
	s := resourceServer(t)
	got := decodeInto[adminapi.RoutesResponse](t, getV1(t, s, "/api/v1/routes", ""))

	if got.BaseVersion == "" {
		t.Fatal("the collection carries no base_version; its selectors would be unusable")
	}
	for i, route := range got.Routes {
		if route.Selector.Listen == "" || route.Selector.MatchType == "" || route.Selector.Path == "" {
			t.Errorf("route %d has an incomplete selector: %+v", i, route.Selector)
		}
	}

	// Exactly one route in the fixture has a durable id; the others are
	// collection-only.
	withID := 0
	for _, route := range got.Routes {
		if route.RouteID != "" {
			withID++
		}
	}
	if withID != 1 {
		t.Fatalf("%d routes carry a route_id, want 1", withID)
	}
}

// TestV1RouteResolvesFromTheIDAlone is what makes the id durable: no listen, no
// server name, no ordinal (ADR 0019 §4.13).
func TestV1RouteResolvesFromTheIDAlone(t *testing.T) {
	s := resourceServer(t)
	got := decodeInto[adminapi.RouteResponse](t, getV1(t, s, "/api/v1/routes/r-abcdefghijklmnopqrstuvwxyz", ""))

	if got.Route.RouteID != "r-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("route_id = %q", got.Route.RouteID)
	}
	if got.Route.Selector.Path != "/zzz" {
		t.Fatalf("resolved the wrong route: %+v", got.Route.Selector)
	}
	if got.BaseVersion == "" {
		t.Error("the per-route response carries no base_version")
	}
	if got.Route.Action != "proxy" || got.Route.Upstream != "zebra" {
		t.Errorf("action/upstream = %q/%q", got.Route.Action, got.Route.Upstream)
	}
}

// TestV1RouteWithoutADurableIDIsNotAddressable. A route with no route_id is
// collection-only; inventing an addressing scheme for it — an index, a hash, a
// coordinate encoding — is what ADR 0019 §32 forbids.
func TestV1RouteWithoutADurableIDIsNotAddressable(t *testing.T) {
	s := resourceServer(t)
	// Each of these is a plausible invented encoding: an index, an ordinal, a
	// path, a listen address, a well-formed but unassigned id.
	for _, attempt := range []string{"0", "1", "aaa", "127.0.0.1:8443", "r-doesnotexistdoesnotexist"} {
		rr := getV1(t, s, "/api/v1/routes/"+attempt, "")
		if rr.Code != http.StatusNotFound {
			t.Errorf("%q resolved with %d; only a durable route_id addresses a route", attempt, rr.Code)
			continue
		}
		env := decodeEnvelope(t, rr)
		if env.Error.Code != adminapi.CodeNotFound {
			t.Errorf("%q: code = %q", attempt, env.Error.Code)
		}
		if env.Error.Details.Kind != "route" {
			t.Errorf("%q: details.kind = %q", attempt, env.Error.Details.Kind)
		}
	}

	// The message must say what to do instead, or the operator has no next step.
	env := decodeEnvelope(t, getV1(t, s, "/api/v1/routes/r-doesnotexistdoesnotexist", ""))
	if !strings.Contains(env.Error.Message, "selector") {
		t.Errorf("the message does not point at the selector: %q", env.Error.Message)
	}
}

// TestV1UpstreamsAreInConfigurationOrderNotRuntimeOrder. The runtime's ordering
// is not part of any contract this package controls, so the collection is built
// from the configuration and enriched by name.
func TestV1UpstreamsAreInConfigurationOrderNotRuntimeOrder(t *testing.T) {
	s := resourceServer(t)
	got := decodeInto[adminapi.UpstreamsResponse](t, getV1(t, s, "/api/v1/upstreams", ""))

	if len(got.Upstreams) != 2 {
		t.Fatalf("upstreams = %d, want 2", len(got.Upstreams))
	}
	if got.Upstreams[0].Name != "zebra" || got.Upstreams[1].Name != "alpha" {
		t.Fatalf("order = %q, %q; want declaration order zebra, alpha",
			got.Upstreams[0].Name, got.Upstreams[1].Name)
	}
	if got.BaseVersion == "" {
		t.Error("the collection carries no base_version")
	}
}

// TestV1UpstreamsListAPoolTheRuntimeHasNotReported. A collection that claims to
// describe the configuration must not drop a pool because the runtime has not
// mentioned it — that would make an operator think they had deleted something.
func TestV1UpstreamsListAPoolTheRuntimeHasNotReported(t *testing.T) {
	s := resourceServer(t)
	got := decodeInto[adminapi.UpstreamsResponse](t, getV1(t, s, "/api/v1/upstreams", ""))

	zebra := got.Upstreams[0]
	if zebra.Name != "zebra" {
		t.Fatalf("first pool = %q", zebra.Name)
	}
	if len(zebra.Backends) != 2 {
		t.Fatalf("zebra has %d backends, want its 2 configured ones", len(zebra.Backends))
	}
	if zebra.Backends[0].State != "" {
		t.Errorf("a pool the runtime has not reported claims a live state: %q", zebra.Backends[0].State)
	}
	if zebra.Backends[0].Weight != 3 {
		t.Errorf("configured weight lost: %d", zebra.Backends[0].Weight)
	}
	if zebra.Strategy != "round_robin" {
		t.Errorf("strategy = %q", zebra.Strategy)
	}

	// The pool the runtime does know about carries its live state.
	alpha := got.Upstreams[1]
	if alpha.Backends[0].State != "available" || alpha.Backends[0].InFlight != 4 {
		t.Errorf("runtime state was not joined: %+v", alpha.Backends[0])
	}
}

func TestV1UpstreamByName(t *testing.T) {
	s := resourceServer(t)

	got := decodeInto[adminapi.UpstreamResponse](t, getV1(t, s, "/api/v1/upstreams/alpha", ""))
	if got.Upstream.Name != "alpha" {
		t.Fatalf("upstream = %q", got.Upstream.Name)
	}

	rr := getV1(t, s, "/api/v1/upstreams/nosuchpool", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown pool = %d, want 404", rr.Code)
	}
	env := decodeEnvelope(t, rr)
	if env.Error.Details.Kind != "upstream_pool" || env.Error.Details.ID != "nosuchpool" {
		t.Fatalf("details = %+v", env.Error.Details)
	}
}

// TestV1CollectionsDoNotPaginate is §24a's other half: history is the only
// unbounded collection, and every other one returns in full because paginating
// a route list would make an operator page through their own configuration.
func TestV1CollectionsDoNotPaginate(t *testing.T) {
	s := resourceServer(t)
	for _, path := range []string{"/api/v1/routes", "/api/v1/upstreams"} {
		body := getV1(t, s, path, "").Body.String()
		for _, key := range []string{"next_cursor", "\"limit\""} {
			if strings.Contains(body, key) {
				t.Errorf("%s publishes %s; only /config/history paginates", path, key)
			}
		}
	}
}

// TestV1ResourceCollectionsReportStorageFailureHonestly: an unreadable
// configuration is not the caller's fault and not a validation error.
func TestV1ResourceCollectionsReportStorageFailureHonestly(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	for _, path := range []string{
		"/api/v1/routes", "/api/v1/routes/r-abcdefghijklmnopqrstuvwxyz",
		"/api/v1/upstreams", "/api/v1/upstreams/alpha",
	} {
		rr := getV1(t, s, path, "")
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503 with no config wired", path, rr.Code)
			continue
		}
		if env := decodeEnvelope(t, rr); env.Error.Code != adminapi.CodeStorageUnavailable {
			t.Errorf("%s: code = %q", path, env.Error.Code)
		}
	}
}

// TestV1RouteCollectionCarriesNoSecrets. The route projection reports whether a
// policy is attached, not its content, so an auth secret or a certificate path
// never reaches a status:read caller.
func TestV1RouteCollectionCarriesNoSecrets(t *testing.T) {
	cfg, err := config.Parse([]byte(`
[global]
log_level = "info"

[[servers]]
listen = "127.0.0.1:8443"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204
  [servers.locations.auth.basic]
  file = "/etc/jul/secret-htpasswd"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})

	body := getV1(t, s, "/api/v1/routes", "").Body.String()
	if strings.Contains(body, "secret-htpasswd") || strings.Contains(body, "/etc/jul") {
		t.Fatalf("the route collection leaked an auth file path: %s", body)
	}
	got := decodeInto[adminapi.RoutesResponse](t, getV1(t, s, "/api/v1/routes", ""))
	if !got.Routes[0].Auth {
		t.Error("the route does not report that auth is attached")
	}
}
