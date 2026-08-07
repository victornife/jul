// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"sort"
	"strings"
	"testing"
)

func indexPaths(t *testing.T) map[string]SchemaPath {
	t.Helper()
	out := map[string]SchemaPath{}
	for _, p := range SchemaPaths() {
		if _, dup := out[p.Path]; dup {
			t.Fatalf("path %q is inventoried more than once", p.Path)
		}
		out[p.Path] = p
	}
	return out
}

// TestSchemaPathsAreDeterministic proves repeated inventories are identical, so
// generated artifacts derived from them cannot churn.
func TestSchemaPathsAreDeterministic(t *testing.T) {
	first := SchemaPaths()
	for i := 0; i < 5; i++ {
		next := SchemaPaths()
		if len(first) != len(next) {
			t.Fatalf("inventory length changed: %d then %d", len(first), len(next))
		}
		for j := range first {
			if first[j] != next[j] {
				t.Fatalf("inventory entry %d changed: %+v then %+v", j, first[j], next[j])
			}
		}
	}
}

func TestSchemaPathsAreSorted(t *testing.T) {
	paths := SchemaPaths()
	sorted := make([]string, len(paths))
	for i, p := range paths {
		sorted[i] = p.Path
	}
	if !sort.StringsAreSorted(sorted) {
		t.Fatal("inventory is not sorted by path")
	}
}

// TestSchemaPathsCoverEveryShape pins the reflection rules that a lifecycle
// registry and generated configuration contracts depend on.
func TestSchemaPathsCoverEveryShape(t *testing.T) {
	idx := indexPaths(t)

	cases := []struct {
		path string
		kind PathKind
	}{
		// Plain struct field.
		{"global.log_format", KindScalar},
		// Nested struct.
		{"observability.tracing.endpoint", KindScalar},
		// Array table expanded through a wildcard.
		{"servers.*.listen", KindScalar},
		// Nested array table.
		{"servers.*.locations.*.proxy_pass", KindScalar},
		// Pointer-to-struct optional block.
		{"servers.*.tls.cert", KindScalar},
		// Doubly nested optional block.
		{"servers.*.tls.client_auth.ca_file", KindScalar},
		{"upstreams.*.discovery.consul.token", KindScalar},
		// Map with dynamic identifier keys.
		{"plugins.*.path", KindScalar},
		// Map of scalars: exactly one canonical leaf for operator-chosen keys.
		{"plugins.*.config.*", KindScalar},
		{"servers.*.error_pages.*", KindScalar},
		{"stream.*.sni_routes.*", KindScalar},
		// Slice of scalars.
		{"servers.*.server_names", KindList},
		{"upstreams.*.health_check.expect_status", KindList},
		// Custom scalar types.
		{"cache.default_ttl", KindScalar},
		{"cache.memory_max_size", KindScalar},
		// time.Time is a scalar, not a table to recurse into.
		{"admin.rbac.principals.*.expires_at", KindScalar},
		// Containers.
		{"servers", KindTable},
		{"servers.*.tls", KindTable},
		{"plugins.*.config", KindTable},
	}
	for _, tc := range cases {
		got, ok := idx[tc.path]
		if !ok {
			t.Errorf("%s is missing from the inventory", tc.path)
			continue
		}
		if got.Kind != tc.kind {
			t.Errorf("%s kind = %s, want %s", tc.path, got.Kind, tc.kind)
		}
	}
}

// TestSchemaPathsUseCanonicalWildcards proves dynamic keys are represented by
// the canonical wildcard segment. The inventory is derived from types, never
// from a document, so no plugin, principal, host or backend name can appear.
func TestSchemaPathsUseCanonicalWildcards(t *testing.T) {
	idx := indexPaths(t)
	for _, path := range []string{
		"servers.*.listen",
		"servers.*.locations.*.rate_limit.rate",
		"servers.*.locations.*.rewrites.*.pattern",
		"upstreams.*.servers.*.address",
		"upstreams.*.discovery.consul.token",
		"plugins.*.config.*",
		"admin.rbac.principals.*.token",
		"admin.rbac.roles.*.permissions",
		"stream.*.protocol",
		"stream.*.sni_routes.*",
	} {
		if _, ok := idx[path]; !ok {
			t.Errorf("canonical wildcard path %q is missing from the inventory", path)
		}
	}

	// Every segment is either a wildcard or a TOML key declared in the schema:
	// lowercase letters, digits and underscores only.
	for _, p := range SchemaPaths() {
		for _, seg := range strings.Split(p.Path, ".") {
			if seg == "*" {
				continue
			}
			if strings.Trim(seg, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
				t.Errorf("path %q has a segment %q that is not a declared TOML key", p.Path, seg)
			}
		}
	}
}

// TestSchemaLeavesExcludeContainers proves leaves are configurable values only.
func TestSchemaLeavesExcludeContainers(t *testing.T) {
	for _, p := range SchemaLeaves() {
		if !p.IsLeaf() {
			t.Errorf("%s is a container but was returned as a leaf", p.Path)
		}
	}
	if len(SchemaLeaves()) >= len(SchemaPaths()) {
		t.Fatal("SchemaLeaves must be a strict subset of SchemaPaths")
	}
}

// TestSchemaPathOptionality records which blocks are optional, which is what
// tells a reviewer whether an omitted table is distinguishable from an empty
// one.
func TestSchemaPathOptionality(t *testing.T) {
	idx := indexPaths(t)
	for _, path := range []string{"servers.*.tls", "servers.*.http3", "upstreams.*.discovery", "upstreams", "plugins", "stream"} {
		if p, ok := idx[path]; !ok || !p.Optional {
			t.Errorf("%s should be marked optional", path)
		}
	}
	if p, ok := idx["global.log_level"]; !ok || p.Optional {
		t.Error("global.log_level is a required scalar and must not be marked optional")
	}
}

// TestSchemaPathDynamicFlag proves the wildcard marker is set consistently.
func TestSchemaPathDynamicFlag(t *testing.T) {
	for _, p := range SchemaPaths() {
		wantDynamic := strings.Contains(p.Path, ".*") || strings.HasSuffix(p.Path, "*")
		if p.Dynamic != wantDynamic {
			t.Errorf("%s Dynamic = %v, want %v", p.Path, p.Dynamic, wantDynamic)
		}
	}
}

// TestSchemaPathTypesAreCheckoutIndependent proves the rendered Go type carries
// no import path, so generated artifacts do not depend on the module location.
func TestSchemaPathTypesAreCheckoutIndependent(t *testing.T) {
	for _, p := range SchemaPaths() {
		if strings.Contains(p.GoType, "/") {
			t.Errorf("%s GoType %q embeds an import path", p.Path, p.GoType)
		}
	}
}
