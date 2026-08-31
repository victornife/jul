// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/rbac"
)

func TestRouteStabilityString(t *testing.T) {
	cases := map[RouteStability]string{
		StabilityInternal:   "internal",
		StabilityExternal:   "external",
		StabilityPublic:     "public",
		StabilityDeprecated: "deprecated",
		RouteStability(200): "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("RouteStability(%d).String() = %q, want %q", s, got, want)
		}
	}
	// The value the generator writes into x-jul-stability is part of the
	// published contract, so an unclassified route must never render as one of
	// the external words.
	if s := RouteStability(200).String(); s == "external" || s == "public" || s == "deprecated" {
		t.Fatalf("an out-of-range stability rendered as %q", s)
	}
}

func TestRouteStabilityExternal(t *testing.T) {
	for s, want := range map[RouteStability]bool{
		StabilityInternal:   false,
		StabilityExternal:   true,
		StabilityPublic:     true,
		StabilityDeprecated: true,
		RouteStability(200): false,
	} {
		if got := s.External(); got != want {
			t.Errorf("RouteStability(%d).External() = %v, want %v", s, got, want)
		}
	}
}

// TestPermissionsForCoversEveryAuthorizationMode. The permissions the generator
// publishes come from here, and a published permission that does not match the
// one the server enforces tells an operator to issue a token that will not
// work — or one wider than necessary.
func TestPermissionsForCoversEveryAuthorizationMode(t *testing.T) {
	cases := []struct {
		name   string
		spec   RouteSpec
		method string
		want   []string
	}{
		{
			name:   "public routes need none",
			spec:   RouteSpec{Public: true, Permission: rbac.StatusRead},
			method: http.MethodGet,
		},
		{
			name:   "authenticated-only routes need none",
			spec:   RouteSpec{Authenticated: true},
			method: http.MethodGet,
		},
		{
			name:   "a single permission applies to every method",
			spec:   RouteSpec{Permission: rbac.MetricsRead},
			method: http.MethodGet,
			want:   []string{"metrics:read"},
		},
		{
			name:   "any-of returns every alternative in declaration order",
			spec:   RouteSpec{AnyPermissions: []rbac.Permission{rbac.StatusRead, rbac.ConfigApply, rbac.HistoryRollback}},
			method: http.MethodGet,
			want:   []string{"status:read", "config:apply", "history:rollback"},
		},
		{
			name: "per-method selects the method's own permission",
			spec: RouteSpec{Permissions: map[string]rbac.Permission{
				http.MethodGet:   rbac.ConfigRead,
				http.MethodPatch: rbac.ConfigTrust,
			}},
			method: http.MethodPatch,
			want:   []string{"config:trust"},
		},
		{
			name: "per-method returns nothing for a method it does not accept",
			spec: RouteSpec{Permissions: map[string]rbac.Permission{
				http.MethodGet: rbac.ConfigRead,
			}},
			method: http.MethodDelete,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.spec.permissionsFor(tc.method)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("permissionsFor(%s) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

// TestInternalRouteReasonsIsACopy: the inventory is a guard-tested record, so
// handing a caller the live map would let one silently excuse a route.
func TestInternalRouteReasonsIsACopy(t *testing.T) {
	got := InternalRouteReasons()
	if len(got) != len(internalRouteReasons) {
		t.Fatalf("returned %d reasons, the inventory has %d", len(got), len(internalRouteReasons))
	}
	for pattern, reason := range internalRouteReasons {
		if got[pattern] != reason {
			t.Errorf("reason for %q differs", pattern)
		}
	}

	got["/api/injected"] = "not a real route"
	delete(got, "/api/stats")
	if _, ok := internalRouteReasons["/api/injected"]; ok {
		t.Fatal("mutating the returned map changed the inventory")
	}
	if _, ok := internalRouteReasons["/api/stats"]; !ok {
		t.Fatal("deleting from the returned map removed an inventory entry")
	}
}

// TestAddrIsLoopback covers the gate's verdict directly, including the forms
// that are easy to get wrong: a wildcard bind is not loopback, and a
// non-loopback name that does not parse as an IP must not be treated as one.
func TestAddrIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:9090":   true,
		"127.0.0.1":        true,
		"localhost:9090":   true,
		"localhost":        true,
		"[::1]:9090":       true,
		"::1":              true,
		"127.5.6.7:9090":   true, // the whole 127/8 block is loopback
		"0.0.0.0:9090":     false,
		"0.0.0.0":          false,
		"[::]:9090":        false,
		"::":               false,
		":9090":            false,
		"":                 false,
		"203.0.113.7:9090": false,
		"10.0.0.1":         false,
		"example.com:9090": false,
		"not-an-address":   false,
	}
	for addr, want := range cases {
		if got := addrIsLoopback(addr); got != want {
			t.Errorf("addrIsLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// TestTransportRefusalIsLoggedWithoutTheCredential. The refusal is the one
// event an operator debugging a broken deployment will look for, so it has to
// reach the log — and it must not put the token or the listen address there.
func TestTransportRefusalIsLoggedWithoutTheCredential(t *testing.T) {
	var buf bytes.Buffer
	s := newTestServer(t, config.AdminConfig{Listen: "203.0.113.7:9090", Token: "secret-token"}, Deps{})
	s.log = slog.New(slog.NewTextHandler(&buf, nil))

	rr := httptest.NewRecorder()
	req := withLocalAddr(httptest.NewRequest(http.MethodGet, "/api/status", nil), "203.0.113.7:9090")
	req.Header.Set("Authorization", "Bearer secret-token")
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	logged := buf.String()
	if !strings.Contains(logged, "insecure transport") {
		t.Fatalf("the refusal was not logged: %q", logged)
	}
	if !strings.Contains(logged, "tls_or_loopback") {
		t.Errorf("the log does not name the condition the caller must satisfy: %q", logged)
	}
	for _, leak := range []string{"secret-token", "203.0.113.7"} {
		if strings.Contains(logged, leak) {
			t.Errorf("the log disclosed %q: %s", leak, logged)
		}
	}
}

// TestTransportRefusalSetsCacheHeaders: a 403 that a proxy or a browser caches
// would survive the fix an operator then applies.
func TestTransportRefusalSetsCacheHeaders(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Listen: "0.0.0.0:9090"}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, withLocalAddr(httptest.NewRequest(http.MethodGet, "/api/status", nil), "203.0.113.7:9090"))

	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}
}

// TestExternalRoutesFlattensPerMethod pins the shape the generator consumes.
func TestExternalRoutesFlattensPerMethod(t *testing.T) {
	routes := ExternalRoutes()
	if len(routes) == 0 {
		t.Fatal("no external routes")
	}
	byKey := make(map[string]ExternalRoute, len(routes))
	for _, r := range routes {
		key := r.Method + " " + r.Pattern
		if _, dup := byKey[key]; dup {
			t.Errorf("%s appears twice", key)
		}
		byKey[key] = r
		if r.Operation.ID == "" {
			t.Errorf("%s carries no operation id", key)
		}
		if !r.Stability.External() {
			t.Errorf("%s is in ExternalRoutes with stability %s", key, r.Stability)
		}
	}
	for _, want := range []string{"GET /healthz", "GET /readyz", "GET /metrics"} {
		if _, ok := byKey[want]; !ok {
			t.Errorf("%s is missing from the external surface", want)
		}
	}
	if byKey["GET /metrics"].Public {
		t.Error("/metrics is reported public; it requires metrics:read")
	}
	if !byKey["GET /healthz"].Public {
		t.Error("/healthz is not reported public")
	}
	if !slices.Equal(byKey["GET /metrics"].Permissions, []string{"metrics:read"}) {
		t.Errorf("/metrics permissions = %v", byKey["GET /metrics"].Permissions)
	}
}
