// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"testing"

	"jul/internal/config"
)

// TestDefaultOverridesResolveAgainstSchema proves every documented default
// names a real schema leaf (also enforced by Build()'s invariant check, but
// pinned here directly against the table).
func TestDefaultOverridesResolveAgainstSchema(t *testing.T) {
	leaves := map[string]bool{}
	for _, p := range config.SchemaLeaves() {
		leaves[p.Path] = true
	}
	for path := range DefaultOverrides {
		if !leaves[path] {
			t.Errorf("DefaultOverrides entry %q does not resolve against config.SchemaLeaves()", path)
		}
	}
}

// TestDefaultForNamedRepresentativeLeaves pins the documented defaults for a
// handful of leaves the corrective task calls out by name.
func TestDefaultForNamedRepresentativeLeaves(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"global.log_level", "info"},
		{"global.log_format", "text"},
		{"global.config_authority", "file_owned"},
		{"global.reload_timeout", "10s"},
		{"global.shutdown_timeout", "30s"},
		{"servers.*.max_header_bytes", "1m"},
		{"admin.history_keep", "50"},
		{"admin.console", "true (when admin.enabled)"},
	}
	for _, tc := range cases {
		got, ok := DefaultFor(tc.path)
		if !ok {
			t.Errorf("DefaultFor(%q) = not found, want %q", tc.path, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("DefaultFor(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}

	if _, ok := DefaultFor("cache.default_ttl"); ok {
		t.Error(`DefaultFor("cache.default_ttl") should be absent: zero means "no fallback freshness", already captured by ZeroSemantics, not a documented positive default`)
	}
}

// TestSchemaRendersDefaultAnnotation proves the generated schema exposes the
// documented default as an x-jul-default annotation, distinct from
// x-jul-constraint/zero-semantics prose.
func TestSchemaRendersDefaultAnnotation(t *testing.T) {
	schema := buildTestSchema(t)
	node := navigateSchema(t, schema, "global", "log_level")
	if got, _ := node["x-jul-default"].(string); got != "info" {
		t.Errorf(`global.log_level x-jul-default = %q, want "info"`, got)
	}
}

// navigateSchema walks "properties" from the schema root for each successive
// key, matching the exact document shape RenderSchema produces.
func navigateSchema(t *testing.T, doc map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := doc
	for _, k := range keys {
		props, ok := cur["properties"].(map[string]any)
		if !ok {
			t.Fatalf("no properties at %q", k)
		}
		next, ok := props[k].(map[string]any)
		if !ok {
			t.Fatalf("no property %q", k)
		}
		cur = next
	}
	return cur
}
