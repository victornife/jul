// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"sort"
	"strings"
	"testing"

	"jul/internal/config"
)

// TestRegistryCoversEverySchemaLeaf is the closed-world contract: adding a
// configurable field to the schema without giving it a lifecycle disposition
// fails here rather than silently defaulting to hot reload.
func TestRegistryCoversEverySchemaLeaf(t *testing.T) {
	registered := map[string]bool{}
	for _, e := range Registry {
		registered[e.Path] = true
	}
	var missing []string
	for _, leaf := range config.SchemaLeaves() {
		if registered[leaf.Path] {
			continue
		}
		if _, exempt := SchemaExemptions[leaf.Path]; exempt {
			continue
		}
		missing = append(missing, leaf.Path)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("schema leaves without a lifecycle disposition (add them to internal/lifecycle/registry.go and run `%s`):\n  %s",
			RegenerateCommand, strings.Join(missing, "\n  "))
	}
}

// TestRegistryPathsExistInSchema rejects an entry for a path the schema does not
// expose, which is how a typo or a removed field is caught.
func TestRegistryPathsExistInSchema(t *testing.T) {
	leaves := map[string]bool{}
	for _, leaf := range config.SchemaLeaves() {
		leaves[leaf.Path] = true
	}
	for _, e := range Registry {
		if !leaves[e.Path] {
			t.Errorf("registry path %q is not a leaf of the configuration schema", e.Path)
		}
	}
}

// TestRegistryHasNoDuplicatePaths proves each path is classified exactly once.
func TestRegistryHasNoDuplicatePaths(t *testing.T) {
	seen := map[string]int{}
	for _, e := range Registry {
		seen[e.Path]++
	}
	for path, n := range seen {
		if n > 1 {
			t.Errorf("path %q is classified %d times; every path must have exactly one disposition", path, n)
		}
	}
}

// TestRegistryHasNoWildcardAmbiguity proves that no two entries can both match
// the same concrete path, so Lookup's answer never depends on registry order.
func TestRegistryHasNoWildcardAmbiguity(t *testing.T) {
	for i := range Registry {
		for j := i + 1; j < len(Registry); j++ {
			if pathsOverlap(Registry[i].Path, Registry[j].Path) {
				t.Errorf("registry paths %q and %q can both match the same concrete path",
					Registry[i].Path, Registry[j].Path)
			}
		}
	}
}

// pathsOverlap reports whether two canonical paths can match a common concrete
// path: same length and every segment pair equal or wildcarded.
func pathsOverlap(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] == "*" || bs[i] == "*" || as[i] == bs[i] {
			continue
		}
		return false
	}
	return true
}

// TestRegistryIsSortedByPath keeps generated artifacts stable regardless of the
// order the per-subsystem groups are assembled in.
func TestRegistryIsSortedByPath(t *testing.T) {
	for i := 1; i < len(Registry); i++ {
		if Registry[i-1].Path >= Registry[i].Path {
			t.Fatalf("registry is not sorted: %q precedes %q", Registry[i-1].Path, Registry[i].Path)
		}
	}
}

// TestEveryEntryHasSubsystemAndReason keeps the generated reference informative.
func TestEveryEntryHasSubsystemAndReason(t *testing.T) {
	for _, e := range Registry {
		if e.Subsystem == "" {
			t.Errorf("%s: missing subsystem", e.Path)
		}
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("%s: missing reason", e.Path)
		}
	}
}

// TestSubsystemsAreDocumented keeps the subsystem set closed and bounded.
func TestSubsystemsAreDocumented(t *testing.T) {
	used := map[Subsystem]bool{}
	for _, e := range Registry {
		used[e.Subsystem] = true
		if _, ok := SubsystemDescription(e.Subsystem); !ok {
			t.Errorf("%s: subsystem %q has no description in subsystemDescriptions", e.Path, e.Subsystem)
		}
	}
	for sub := range subsystemDescriptions {
		if !used[sub] {
			t.Errorf("subsystem %q is described but no path uses it; remove it to keep the set bounded", sub)
		}
	}
}

// TestRestartRequiredEntriesAreStartupConsumed proves the restart gate actually
// compares every field it claims to protect: a restart-required entry that is
// not in the fingerprint would be silently accepted by a hot reload.
func TestRestartRequiredEntriesAreStartupConsumed(t *testing.T) {
	for _, e := range Registry {
		if e.Class == RestartRequiredClass && !e.StartupConsumed {
			t.Errorf("%s is restart_required but not StartupConsumed; RestartRequired would ignore it", e.Path)
		}
	}
}

// TestNonRestartEntriesAreNotStartupConsumed proves the inverse: a field that is
// in the fingerprint but classified hot would be rejected at reload despite its
// documented class.
func TestNonRestartEntriesAreNotStartupConsumed(t *testing.T) {
	for _, e := range Registry {
		if e.Class != RestartRequiredClass && e.StartupConsumed {
			t.Errorf("%s is %s but StartupConsumed; the fingerprint would reject a change its class says is allowed", e.Path, e.Class)
		}
	}
}

// TestIgnoredAndReservedEntriesAreNotStartupConsumed proves a field with no
// runtime consumer, or one validation rejects, can never create a pending
// restart.
func TestIgnoredAndReservedEntriesAreNotStartupConsumed(t *testing.T) {
	for _, e := range Registry {
		if (e.Ignored || e.Reserved) && e.StartupConsumed {
			t.Errorf("%s is ignored/reserved but startup-consumed; changing it must never create a pending restart", e.Path)
		}
	}
}

// TestClassMetadataIsConsistent keeps the boolean metadata aligned with the
// class it accompanies.
func TestClassMetadataIsConsistent(t *testing.T) {
	for _, e := range Registry {
		switch e.Class {
		case IgnoredDeprecatedClass:
			if !e.Ignored {
				t.Errorf("%s: class ignored_deprecated requires Ignored", e.Path)
			}
		case ValidationRejectedReservedClass:
			if !e.Reserved {
				t.Errorf("%s: class validation_rejected_reserved requires Reserved", e.Path)
			}
		}
		if e.AddressKeyed && !e.StartupConsumed {
			t.Errorf("%s: AddressKeyed only means anything for a startup-consumed path", e.Path)
		}
		if e.AddressKeyed && !e.Conditional {
			t.Errorf("%s: an address-keyed path is conditional on the live listener set", e.Path)
		}
	}
}

// TestFullyPopulatedFixtureReachesEveryEntry proves every registered path has a
// working extractor: a mistyped path or a missing special extractor yields no
// value from a configuration where every field is set.
func TestFullyPopulatedFixtureReachesEveryEntry(t *testing.T) {
	cfg := fullConfig()
	for _, e := range Registry {
		v, ok := EffectiveValue(cfg, e.Path)
		if !ok {
			t.Errorf("%s: no extractor", e.Path)
			continue
		}
		if isEmptyValue(v) {
			t.Errorf("%s: extractor returned an empty value from the fully populated fixture", e.Path)
		}
	}
}

func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case []string:
		return len(t) == 0
	case map[string]any:
		if len(t) == 0 {
			return true
		}
		for _, vv := range t {
			if !isEmptyValue(vv) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// TestLookupFailsClosedForUnknownPath is the anti-regression for the removed
// permissive default: an unregistered path must not be reported as hot.
func TestLookupFailsClosedForUnknownPath(t *testing.T) {
	if _, ok := Lookup("global.brand_new_field"); ok {
		t.Fatal("Lookup returned a disposition for an unregistered path")
	}
	if _, err := ClassOf("global.brand_new_field"); err == nil {
		t.Fatal("ClassOf must return an error for an unregistered path")
	} else if !strings.Contains(err.Error(), RegenerateCommand) {
		t.Errorf("ClassOf error must name the remediation command, got %q", err)
	}
	if _, err := ClassifyPath("servers.*.nope"); err == nil {
		t.Fatal("ClassifyPath must fail closed for an unregistered path")
	}
}

// TestLookupResolvesConcretePaths proves a concrete path from a diff resolves to
// its canonical entry.
func TestLookupResolvesConcretePaths(t *testing.T) {
	e, ok := Lookup("servers.0.tls.client_auth.ca_file")
	if !ok {
		t.Fatal("concrete path did not resolve")
	}
	if e.Path != "servers.*.tls.client_auth.ca_file" {
		t.Fatalf("resolved to %q", e.Path)
	}
	if e.Subsystem != SubMTLS {
		t.Fatalf("subsystem = %q, want %q", e.Subsystem, SubMTLS)
	}
}

// TestCoarseTLSGroupsAreSplit pins the split required by #89: the previous
// registry classified whole tls/http3 subtrees with one entry, which made the
// restart reason unusable and hid per-leaf differences.
func TestCoarseTLSGroupsAreSplit(t *testing.T) {
	for _, coarse := range []string{"servers.*.tls", "servers.*.http3"} {
		for _, e := range Registry {
			if e.Path == coarse {
				t.Errorf("%q is still registered as one coarse entry", coarse)
			}
		}
	}
	for _, path := range []string{
		"servers.*.tls.enabled",
		"servers.*.tls.cert",
		"servers.*.tls.key",
		"servers.*.tls.min_version",
		"servers.*.tls.client_auth.mode",
		"servers.*.tls.client_auth.ca_file",
		"servers.*.tls.client_auth.verify_san",
		"servers.*.tls.client_auth.crl_file",
		"servers.*.tls.acme.enabled",
		"servers.*.tls.acme.email",
		"servers.*.tls.acme.ca",
		"servers.*.tls.acme.domains",
		"servers.*.tls.acme.challenge",
		"servers.*.tls.acme.dns_provider",
		"servers.*.tls.acme.cache_dir",
		"servers.*.tls.acme.ocsp_stapling",
		"servers.*.http3.enabled",
		"servers.*.http3.alt_svc_max_age",
	} {
		if _, ok := Lookup(path); !ok {
			t.Errorf("%q has no exact disposition", path)
		}
	}
}

// TestReservedFieldsStayValidationRejected pins the truthful contract for the
// reserved ACME DNS-01 seam and the location-scoped connection cap: validation
// rejects both, so neither may be presented as a hot or restart-required
// setting.
func TestReservedFieldsStayValidationRejected(t *testing.T) {
	for _, path := range []string{
		"servers.*.tls.acme.dns_provider",
		"servers.*.locations.*.rate_limit.max_conns",
	} {
		e, ok := Lookup(path)
		if !ok {
			t.Fatalf("%s has no disposition", path)
		}
		if e.Class != ValidationRejectedReservedClass {
			t.Errorf("%s class = %s, want validation_rejected_reserved", path, e.Class)
		}
	}
	// http-01 and tls-alpn-01 are implemented, so the challenge selector itself
	// must not be reserved.
	e, ok := Lookup("servers.*.tls.acme.challenge")
	if !ok || e.Class == ValidationRejectedReservedClass {
		t.Error("acme.challenge must not be reserved: http-01 and tls-alpn-01 are supported")
	}
}

// TestDeprecatedLogFieldsAreIgnored pins that the legacy log destinations never
// gain a runtime effect or a pending restart.
func TestDeprecatedLogFieldsAreIgnored(t *testing.T) {
	for _, path := range []string{"global.access_log", "global.error_log", "servers.*.access_log", "servers.*.error_log"} {
		e, ok := Lookup(path)
		if !ok {
			t.Fatalf("%s has no disposition", path)
		}
		if e.Class != IgnoredDeprecatedClass || !e.Ignored || !e.Deprecated {
			t.Errorf("%s = %s (ignored=%v deprecated=%v), want ignored_deprecated", path, e.Class, e.Ignored, e.Deprecated)
		}
	}
}

// TestCacheStaysRestartRequired pins the #89 non-goal: the response cache is not
// pre-authorized for hot reload before #92/#93 land.
func TestCacheStaysRestartRequired(t *testing.T) {
	for _, e := range Registry {
		if strings.HasPrefix(e.Path, "cache.") && e.Class != RestartRequiredClass {
			t.Errorf("%s = %s, want restart_required until the prepared cache seam lands", e.Path, e.Class)
		}
	}
}

// TestStartupFieldsHaveExtractorsAndDigestSecrets proves the fingerprint can be
// computed for every startup-consumed path and never stores secret material.
func TestStartupFieldsHaveExtractorsAndDigestSecrets(t *testing.T) {
	cfg := fullConfig()
	fp := ComputeFingerprint(cfg)
	for _, e := range StartupFields() {
		if _, ok := fp.Values[e.Path]; !ok {
			t.Errorf("%s is startup-consumed but absent from the fingerprint", e.Path)
		}
	}
	for _, secret := range []string{"shared-token", "principal-token", "consul-token", "kubernetes-token", "BEGIN PRIVATE KEY"} {
		if strings.Contains(renderValues(fp.Values), secret) {
			t.Errorf("fingerprint contains secret material %q", secret)
		}
	}
}

// TestServerLevelListenerScopedPathsAreKnownToTheLinter is the other half of
// the cross-check in internal/config: if a new server-level path is classified
// as listener-bound here, the linter's duplicate list must be updated too, or a
// divergent value across blocks sharing a listen will again be silently
// discarded. The list below is the registry's own view; internal/config asserts
// its disposition for each entry.
func TestServerLevelListenerScopedPathsAreKnownToTheLinter(t *testing.T) {
	known := map[string]bool{
		"servers.*.read_header_timeout": true, "servers.*.read_timeout": true,
		"servers.*.write_timeout": true, "servers.*.idle_timeout": true,
		"servers.*.max_header_bytes": true, "servers.*.h2c": true,
		"servers.*.http3.enabled": true, "servers.*.http3.alt_svc_max_age": true,
		"servers.*.listen": true, "servers.*.tls.enabled": true,
		"servers.*.tls.min_version": true, "servers.*.tls.cert": true,
		"servers.*.tls.key": true, "servers.*.tls.client_auth.mode": true,
		"servers.*.tls.client_auth.ca_file": true, "servers.*.tls.client_auth.crl_file": true,
		"servers.*.tls.client_auth.verify_san": true, "servers.*.tls.acme.ca": true,
		"servers.*.tls.acme.cache_dir": true, "servers.*.tls.acme.challenge": true,
		"servers.*.tls.acme.domains": true, "servers.*.tls.acme.email": true,
		"servers.*.tls.acme.enabled": true, "servers.*.tls.acme.ocsp_stapling": true,
	}
	for _, e := range Registry {
		if !strings.HasPrefix(e.Path, "servers.*.") {
			continue
		}
		if e.Class != NewListenerOnlyClass && !e.AddressKeyed {
			continue
		}
		if !known[e.Path] {
			t.Errorf("%s is listener-bound but unknown to the divergence linter; add it to the list in internal/config/listener_scope_test.go and decide whether it is linted or exempt", e.Path)
		}
	}
}
