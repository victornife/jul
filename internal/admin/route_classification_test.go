// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"
)

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
