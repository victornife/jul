// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/tls"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/rbac"
)

func TestCatalogOwnsMuxRegistrations(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	registrations := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") {
				return true
			}
			registrations++
			if name != "routes.go" || selector.Sel.Name != "Handle" || len(call.Args) != 2 {
				t.Errorf("%s: register admin routes only through Catalog in routes.go", name)
				return true
			}
			receiver, receiverOK := selector.X.(*ast.Ident)
			pattern, patternOK := call.Args[0].(*ast.SelectorExpr)
			handler, handlerOK := call.Args[1].(*ast.Ident)
			if !receiverOK || receiver.Name != "mux" || !patternOK || pattern.Sel.Name != "Pattern" || !handlerOK || handler.Name != "h" {
				t.Errorf("%s: registration must remain mux.Handle(spec.Pattern, h)", name)
				return true
			}
			spec, specOK := pattern.X.(*ast.Ident)
			if !specOK || spec.Name != "spec" {
				t.Errorf("%s: route pattern must come from the Catalog entry", name)
			}
			return true
		})
	}
	if registrations != 1 {
		t.Errorf("got %d mux registrations, want the single Catalog registration", registrations)
	}
}

func TestRouteTransportPolicyInventory(t *testing.T) {
	policies := map[string]string{
		"/healthz":                                "probe_exempt",
		"/readyz":                                 "probe_exempt",
		"/":                                       "tls_or_loopback",
		"/api/v1/status":                          "tls_or_loopback",
		"/api/v1/capabilities":                    "tls_or_loopback",
		"/api/v1/config":                          "tls_or_loopback",
		"/api/v1/config/pending-restart":          "tls_or_loopback",
		"/api/v1/config/applies/{apply_id}":       "tls_or_loopback",
		"/api/v1/config/history":                  "tls_or_loopback",
		"/api/v1/routes":                          "tls_or_loopback",
		"/api/v1/routes/{route_id}":               "tls_or_loopback",
		"/api/v1/upstreams":                       "tls_or_loopback",
		"/api/v1/upstreams/{name}":                "tls_or_loopback",
		"/api/v1/listeners":                       "tls_or_loopback",
		"/api/v1/listeners/{addr}/client_address": "tls_or_loopback",
		"/api/v1/streams":                         "tls_or_loopback",
		"/api/v1/config/export":                   "tls_or_loopback",
		"/api/v1/config/history/{id}/diff":        "tls_or_loopback",
		"/api/v1/config/validate":                 "tls_or_loopback",
		"/api/v1/config/plan":                     "tls_or_loopback",
		"/api/v1/routes/test":                     "tls_or_loopback",
		"/api/v1/config/patch":                    "tls_or_loopback",
		"/api/v1/config/apply":                    "tls_or_loopback",
		"/api/v1/config/patch/apply":              "tls_or_loopback",
		"/api/v1/config/rollback":                 "tls_or_loopback",
		"/api/v1/config/adopt-external/preview":   "tls_or_loopback",
		"/api/v1/config/adopt-external":           "tls_or_loopback",
		"/api/v1/config/pending-restart/discard":  "tls_or_loopback",
		"/api/admin/me":                           "tls_or_loopback",
		"/metrics":                                "tls_or_loopback",
		"/api/stats":                              "tls_or_loopback",
		"/api/status":                             "tls_or_loopback",
		"/api/runtime/overview":                   "tls_or_loopback",
		"/api/routes":                             "tls_or_loopback",
		"/api/apps":                               "tls_or_loopback",
		"/api/upstreams":                          "tls_or_loopback",
		"/api/upstreams/{name}/resilience":        "tls_or_loopback",
		"/api/certs":                              "tls_or_loopback",
		"/api/tls":                                "tls_or_loopback",
		"/api/security":                           "tls_or_loopback",
		"/api/traffic-controls":                   "tls_or_loopback",
		"/api/plugins":                            "tls_or_loopback",
		"/api/streams":                            "tls_or_loopback",
		"/api/mtls":                               "tls_or_loopback",
		"/api/search":                             "tls_or_loopback",
		"/api/events":                             "tls_or_loopback",
		"/api/config":                             "tls_or_loopback",
		"/api/config/raw":                         "tls_or_loopback",
		"/api/config/settings":                    "tls_or_loopback",
		"/api/config/pending-restart":             "tls_or_loopback",
		"/api/config/authority/refresh":           "tls_or_loopback",
		"/api/config/applies/{id}":                "tls_or_loopback",
		"/api/config/history":                     "tls_or_loopback",
		"/api/config/history/{id}":                "tls_or_loopback",
		"/api/config/history/{id}/diff":           "tls_or_loopback",
		"/api/config/validate":                    "tls_or_loopback",
		"/api/config/preview":                     "tls_or_loopback",
		"/api/config/diff":                        "tls_or_loopback",
		"/api/config/patch":                       "tls_or_loopback",
		"/api/config/patch/preview":               "tls_or_loopback",
		"/api/config/patch/candidate":             "tls_or_loopback",
		"/api/routes/test":                        "tls_or_loopback",
		"/api/wizard":                             "tls_or_loopback",
		"/api/wizard/generate":                    "tls_or_loopback",
		"/api/transcode/descriptor-upload":        "tls_or_loopback",
		"/api/config/apply":                       "tls_or_loopback",
		"/api/config/patch/apply":                 "tls_or_loopback",
		"/api/listeners":                          "tls_or_loopback",
		"/api/listeners/{addr}/client_address":    "tls_or_loopback",
		"/api/config/pending-restart/discard":     "tls_or_loopback",
		"/api/config/adopt-external/preview":      "tls_or_loopback",
		"/api/config/adopt-external":              "tls_or_loopback",
		"/api/history":                            "tls_or_loopback",
		"/api/history/get":                        "tls_or_loopback",
		"/api/history/rollback":                   "tls_or_loopback",
		"/api/config/rollback":                    "tls_or_loopback",
		"/api/plugins/upload":                     "tls_or_loopback",
		"/api/observability/requests":             "tls_or_loopback",
		"/api/observability/failing-routes":       "tls_or_loopback",
		"/api/observability/timeline":             "tls_or_loopback",
		"/api/observability/upstream-history":     "tls_or_loopback",
		"/api/observability/cert-history":         "tls_or_loopback",
		"/api/observability/logs":                 "tls_or_loopback",
		"/api/observability/logs/stream":          "tls_or_loopback",
		"/api/admin/health":                       "tls_or_loopback",
		"/api/admin/client-errors":                "tls_or_loopback",
		"/api/audit":                              "tls_or_loopback",
		"/api/audit/export":                       "tls_or_loopback",
		"/cache/purge":                            "tls_or_loopback",
		"/reload":                                 "tls_or_loopback",
		"/debug/pprof/":                           "tls_or_loopback",
	}
	server := newTestServer(t, config.AdminConfig{Listen: "0.0.0.0:9090", Token: "secret-token"}, Deps{Ready: func() bool { return true }})
	policy, err := rbac.Build(true, "admin", map[string][]string{"none": {}}, []rbac.PrincipalDef{
		{Name: "admin", Role: rbac.RoleAdmin, Token: "admin-token-unused-by-this-test"},
		{Name: "no-permissions", Role: "none", Token: "no-permissions-token"},
	}, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server.UpdatePolicy(policy)
	handler := server.routes()
	for _, spec := range Catalog {
		policy, declared := policies[spec.Pattern]
		if !declared {
			t.Errorf("route %q has no explicit transport policy", spec.Pattern)
			continue
		}
		delete(policies, spec.Pattern)
		if policy != "tls_or_loopback" && policy != "probe_exempt" {
			t.Errorf("route %q has invalid transport policy %q", spec.Pattern, policy)
			continue
		}
		if transportExemptPaths[spec.Pattern] != (policy == "probe_exempt") {
			t.Errorf("route %q: transport exemption disagrees with policy %q", spec.Pattern, policy)
		}
		for _, method := range spec.Methods {
			t.Run(method+" "+spec.Pattern, func(t *testing.T) {
				request := withLocalAddr(httptest.NewRequest(method, spec.Pattern, nil), "203.0.113.7:9090")
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				if policy == "probe_exempt" {
					if recorder.Code != http.StatusOK {
						t.Fatalf("public probe status = %d, want 200", recorder.Code)
					}
					return
				}
				if recorder.Code != http.StatusForbidden || decodeEnvelope(t, recorder).Error.Code != adminapi.CodeInsecureTransport {
					t.Fatalf("insecure request escaped transport gate: status = %d", recorder.Code)
				}
				if spec.Public {
					return
				}
				request.TLS = &tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13}
				recorder = httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusUnauthorized {
					t.Fatalf("secure unauthenticated request status = %d, want 401", recorder.Code)
				}
				request.Header.Set("Authorization", "Bearer no-permissions-token")
				recorder = httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				want := http.StatusForbidden
				if spec.Authenticated {
					want = http.StatusOK
				}
				if recorder.Code != want {
					t.Fatalf("identity without permissions: status = %d, want %d", recorder.Code, want)
				}
			})
		}
	}
	for pattern := range policies {
		t.Errorf("transport policy names unregistered route %q", pattern)
	}
}

// TestClassificationInventoryIsExactlyTheInternalRoutes is the fail-closed
// guard ADR 0019 §24 asks for. It holds the classification inventory and the
// catalog to exact-set equality in both directions:
//
//   - a route added without a Stability defaults to internal and therefore
//     needs a recorded reason, so forgetting to classify it fails here rather
//     than shipping;
//   - a route promoted to external without deleting its reason also fails, so
//     the inventory cannot describe a route it no longer covers.
//
// This is the test that makes "no route becomes external merely because the
// Console calls it today" enforceable rather than aspirational.
func TestClassificationInventoryIsExactlyTheInternalRoutes(t *testing.T) {
	internal := make(map[string]bool)
	for _, spec := range Catalog {
		if spec.Stability == StabilityInternal {
			internal[spec.Pattern] = true
		}
	}

	for pattern := range internal {
		if _, ok := internalRouteReasons[pattern]; !ok {
			t.Errorf("route %q is internal but records no reason.\n"+
				"Add an entry to internalRouteReasons in route_classification.go saying why it is not part of the\n"+
				"external contract, or classify it StabilityExternal and give it Operations metadata.", pattern)
		}
	}

	catalogPatterns := make(map[string]RouteSpec, len(Catalog))
	for _, spec := range Catalog {
		catalogPatterns[spec.Pattern] = spec
	}
	for pattern := range internalRouteReasons {
		spec, ok := catalogPatterns[pattern]
		if !ok {
			t.Errorf("internalRouteReasons names %q, which is not a route in the catalog; delete the stale entry", pattern)
			continue
		}
		if spec.Stability != StabilityInternal {
			t.Errorf("route %q is classified %s but still records an internal reason; delete the entry from internalRouteReasons",
				pattern, spec.Stability)
		}
	}
}

// TestClassificationReasonsAreReasons rejects a placeholder. A reason a
// reviewer cannot disagree with is not a reason, and the whole value of the
// inventory is that "why is this not external?" has a real answer per route.
func TestClassificationReasonsAreReasons(t *testing.T) {
	const minReason = 40
	placeholders := []string{"internal", "n/a", "tbd", "todo", "not external"}
	for pattern, reason := range internalRouteReasons {
		if len(reason) < minReason {
			t.Errorf("route %q: reason %q is too short to be a reason (want >= %d characters explaining the decision)",
				pattern, reason, minReason)
		}
		for _, p := range placeholders {
			if strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(reason, ".")), p) {
				t.Errorf("route %q: %q is a label, not a reason", pattern, reason)
			}
		}
	}
}

// TestExternalRoutesCarryOperationMetadata asserts that an external route is
// fully described. The OpenAPI generator cannot invent an operation id, and a
// generated client cannot be built from a missing one, so an external route
// without complete per-method metadata is a build failure rather than a
// silently thin document.
func TestExternalRoutesCarryOperationMetadata(t *testing.T) {
	seenIDs := make(map[string]string)
	for _, spec := range Catalog {
		if !spec.Stability.External() {
			if len(spec.Operations) > 0 {
				t.Errorf("route %q is internal but declares Operations; internal shapes must not reach the external contract", spec.Pattern)
			}
			if spec.Sunset != "" {
				t.Errorf("route %q is internal but declares a Sunset date", spec.Pattern)
			}
			continue
		}
		for _, m := range spec.Methods {
			op, ok := spec.Operations[m]
			if !ok {
				t.Errorf("external route %q accepts %s but declares no ExternalOperation for it", spec.Pattern, m)
				continue
			}
			if op.ID == "" {
				t.Errorf("external route %q %s has no operation id", spec.Pattern, m)
			}
			if op.Summary == "" {
				t.Errorf("external route %q %s has no summary", spec.Pattern, m)
			}
			if op.Response == "" {
				t.Errorf("external route %q %s names no response schema", spec.Pattern, m)
			}
			if prev, dup := seenIDs[op.ID]; dup {
				t.Errorf("operation id %q is used by both %s and %s %s; operation ids are part of the versioned contract and must be unique",
					op.ID, prev, spec.Pattern, m)
			}
			seenIDs[op.ID] = spec.Pattern + " " + m
		}
		for m := range spec.Operations {
			if !containsMethod(spec.Methods, m) {
				t.Errorf("external route %q declares an operation for %s, which it does not accept", spec.Pattern, m)
			}
		}
		if spec.Stability == StabilityDeprecated && spec.Sunset == "" {
			t.Errorf("deprecated route %q declares no Sunset date; a deprecated endpoint must announce when it may be removed", spec.Pattern)
		}
		if spec.Stability != StabilityDeprecated && spec.Sunset != "" {
			t.Errorf("route %q declares a Sunset date but is not deprecated", spec.Pattern)
		}
	}
}

// TestOnlyProbesArePublic pins ADR 0019 §24a's correction: StabilityPublic
// means "requires no authentication", and only the two probes qualify.
// /metrics requires metrics:read, so it is external authenticated — an earlier
// draft classified it public, which would have published a contract the server
// does not implement.
func TestOnlyProbesArePublic(t *testing.T) {
	want := map[string]bool{"/healthz": true, "/readyz": true}
	for _, spec := range Catalog {
		if spec.Stability == StabilityPublic && !want[spec.Pattern] {
			t.Errorf("route %q is StabilityPublic; only %v may be", spec.Pattern, keysOf(want))
		}
		if spec.Stability == StabilityPublic && !spec.Public {
			t.Errorf("route %q is StabilityPublic but is not Public: the classification claims no authentication is required while the route requires it", spec.Pattern)
		}
		if want[spec.Pattern] && spec.Stability != StabilityPublic {
			t.Errorf("route %q must be StabilityPublic, got %s", spec.Pattern, spec.Stability)
		}
	}
}

// TestMetricsIsExternalAuthenticated pins the other half of the same
// correction, because it is the one most likely to be re-broken: /metrics is in
// the external contract and it requires a credential.
func TestMetricsIsExternalAuthenticated(t *testing.T) {
	for _, spec := range Catalog {
		if spec.Pattern != "/metrics" {
			continue
		}
		if spec.Stability != StabilityExternal {
			t.Fatalf("/metrics must be StabilityExternal, got %s", spec.Stability)
		}
		if spec.Public {
			t.Fatal("/metrics must not be Public: it requires metrics:read")
		}
		if spec.Permission != "metrics:read" {
			t.Fatalf("/metrics permission changed to %q; §28.1's transport gate and the external contract both depend on it requiring a credential", spec.Permission)
		}
		return
	}
	t.Fatal("/metrics is missing from the catalog")
}

// TestNoV1RouteReturnsRawConfigurationBytes is ADR 0019 §24's required test.
// Both raw-readback paths — the configuration file and a history snapshot body,
// which is the same data class — are withdrawn from v1 together, and §36
// records a single re-entry trigger for both. This test fails if either is
// promoted without that decision being made.
func TestNoV1RouteReturnsRawConfigurationBytes(t *testing.T) {
	withdrawn := []string{
		"/api/v1/config/raw",
		"/api/v1/config/history/{id}",
		"/api/v1/config/preview",
		"/api/v1/config/patch/candidate",
		"/api/v1/history/get",
	}
	for _, spec := range Catalog {
		for _, w := range withdrawn {
			if spec.Pattern == w {
				t.Errorf("route %q exists: no /api/v1 route returns raw configuration or history-snapshot bytes (ADR 0019 §24, §36).\n"+
					"Raw bodies remain on the internal routes under config:raw and history:raw.", w)
			}
		}
		// The permissions that gate raw bytes must never appear on a v1 route,
		// which catches a raw readback introduced under a different path.
		if !strings.HasPrefix(spec.Pattern, "/api/v1/") {
			continue
		}
		for _, p := range spec.permissionsFor(firstMethod(spec)) {
			if p == "config:raw" || p == "history:raw" {
				t.Errorf("v1 route %q requires %q; raw configuration and raw history bodies are not part of the external contract", spec.Pattern, p)
			}
		}
	}

	// The internal counterparts must still exist: the withdrawal moved the
	// capability, it did not delete it.
	for _, pattern := range []string{"/api/config", "/api/config/history/{id}"} {
		found := false
		for _, spec := range Catalog {
			if spec.Pattern == pattern {
				found = true
			}
		}
		if !found {
			t.Errorf("internal route %q is missing; raw bytes remain available there for the Console and local operators", pattern)
		}
	}
}

func containsMethod(methods []string, m string) bool {
	for _, x := range methods {
		if x == m {
			return true
		}
	}
	return false
}

func firstMethod(spec RouteSpec) string {
	if len(spec.Methods) == 0 {
		return ""
	}
	return spec.Methods[0]
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
