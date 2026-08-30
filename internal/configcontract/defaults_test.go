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

// TestDefaultForNamedRepresentativeLeaves pins the documented AUTHOR-TEXT
// defaults (before scalar-type conversion) for a handful of leaves the
// corrective task calls out by name.
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

// TestConditionalDefaultOverridesAreDisjointFromDefaultOverrides pins the
// mutual-exclusion invariant Build() also enforces: admin.console's default
// depends on admin.enabled, so it lives ONLY in ConditionalDefaultOverrides.
func TestConditionalDefaultOverridesAreDisjointFromDefaultOverrides(t *testing.T) {
	got, ok := ConditionalDefaultFor("admin.console")
	if !ok || got != "true (when admin.enabled)" {
		t.Errorf(`ConditionalDefaultFor("admin.console") = (%q, %v), want ("true (when admin.enabled)", true)`, got, ok)
	}
	if _, ok := DefaultFor("admin.console"); ok {
		t.Error(`DefaultFor("admin.console") should be absent: it is conditional, not unconditional`)
	}
	for path := range DefaultOverrides {
		if _, ok := ConditionalDefaultOverrides[path]; ok {
			t.Errorf("%q is in both DefaultOverrides and ConditionalDefaultOverrides", path)
		}
	}
}

// TestBuildProducesTypedDefaults proves Build() converts every documented
// default to the JSON type its leaf's own scalar/kind calls for — never a
// string standing in for a bool, a number, or an array.
func TestBuildProducesTypedDefaults(t *testing.T) {
	c, err := Build(loadTestSources(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	byPath := make(map[string]Field, len(c.Leaves))
	for _, f := range c.Leaves {
		byPath[f.Path] = f
	}

	cases := []struct {
		path string
		want any
	}{
		{"global.log_level", "info"},
		{"global.redact_min_secret_length", int64(4)},
		{"servers.*.max_header_bytes", "1m"}, // Size stays a string on the wire
		{"servers.*.locations.*.grpc_transcode.preserve_proto_field_names", false},
		{"observability.tracing.sample_ratio", 1.0},
		{"compression.encoders", []string{"gzip"}},
		{"upstreams.*.health_check.expect_status", []int64{200}},
	}
	for _, tc := range cases {
		f, ok := byPath[tc.path]
		if !ok {
			t.Fatalf("no leaf %q", tc.path)
		}
		if !f.HasDefault {
			t.Fatalf("%q: HasDefault = false, want true", tc.path)
		}
		if diff := cmpDefault(f.Default, tc.want); diff != "" {
			t.Errorf("%q: Default = %#v (%T), want %#v (%T)", tc.path, f.Default, f.Default, tc.want, tc.want)
		}
	}

	// admin.console never gets a schema-level Default: it is conditional.
	adminConsole, ok := byPath["admin.console"]
	if !ok {
		t.Fatal("no leaf admin.console")
	}
	if adminConsole.HasDefault {
		t.Errorf("admin.console.HasDefault = true, want false (conditional, not unconditional)")
	}
	if adminConsole.ConditionalDefault != "true (when admin.enabled)" {
		t.Errorf("admin.console.ConditionalDefault = %q, want %q", adminConsole.ConditionalDefault, "true (when admin.enabled)")
	}
}

func cmpDefault(got, want any) string {
	gs, ok1 := got.([]string)
	ws, ok2 := want.([]string)
	if ok1 && ok2 {
		if len(gs) != len(ws) {
			return "length mismatch"
		}
		for i := range gs {
			if gs[i] != ws[i] {
				return "element mismatch"
			}
		}
		return ""
	}
	gi, ok1 := got.([]int64)
	wi, ok2 := want.([]int64)
	if ok1 && ok2 {
		if len(gi) != len(wi) {
			return "length mismatch"
		}
		for i := range gi {
			if gi[i] != wi[i] {
				return "element mismatch"
			}
		}
		return ""
	}
	if got != want {
		return "mismatch"
	}
	return ""
}

// TestSchemaRendersDefaultAnnotation proves the generated schema exposes the
// documented default as the standard JSON Schema "default" keyword with a
// properly typed value, distinct from x-jul-constraint/zero-semantics prose
// and from a conditional default (which never becomes "default").
func TestSchemaRendersDefaultAnnotation(t *testing.T) {
	schema := buildTestSchema(t)

	logLevel := navigateSchema(t, schema, "global", "log_level")
	if got, _ := logLevel["default"].(string); got != "info" {
		t.Errorf(`global.log_level default = %#v, want "info"`, logLevel["default"])
	}

	secretLen := navigateSchema(t, schema, "global", "redact_min_secret_length")
	if got, ok := secretLen["default"].(float64); !ok || got != 4 {
		t.Errorf("global.redact_min_secret_length default = %#v, want 4 (a JSON number)", secretLen["default"])
	}

	preserveNames := navigateSchema(t, schema, "servers", "items", "locations", "items", "grpc_transcode", "preserve_proto_field_names")
	if got, ok := preserveNames["default"].(bool); !ok || got != false {
		t.Errorf("preserve_proto_field_names default = %#v, want false (a JSON bool)", preserveNames["default"])
	}

	adminConsole := navigateSchema(t, schema, "admin", "console")
	if _, present := adminConsole["default"]; present {
		t.Errorf("admin.console should have no schema default (conditional), got %#v", adminConsole["default"])
	}
	if got, _ := adminConsole["x-jul-conditional-default"].(string); got != "true (when admin.enabled)" {
		t.Errorf("admin.console x-jul-conditional-default = %#v, want %q", adminConsole["x-jul-conditional-default"], "true (when admin.enabled)")
	}
}

// navigateSchema walks "properties" from the schema root for each successive
// key, matching the exact document shape RenderSchema produces. A literal
// "items" segment walks into a list's item schema instead of "properties".
func navigateSchema(t *testing.T, doc map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := doc
	for _, k := range keys {
		if k == "items" {
			next, ok := cur["items"].(map[string]any)
			if !ok {
				t.Fatalf("no items at current node")
			}
			cur = next
			continue
		}
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
