// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"encoding/json"
	"strings"
	"testing"

	"jul/internal/config"
)

func buildTestSchema(t *testing.T) map[string]any {
	t.Helper()
	c, err := Build(loadTestSources(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	raw, err := RenderSchema(c)
	if err != nil {
		t.Fatalf("RenderSchema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal generated schema: %v", err)
	}
	return schema
}

func minimalValidConfig() map[string]any {
	return map[string]any{
		"servers": []any{
			map[string]any{"listen": "127.0.0.1:8080"},
		},
	}
}

func TestSchemaDialectAndID(t *testing.T) {
	schema := buildTestSchema(t)
	if schema["$schema"] != SchemaDialect {
		t.Errorf("$schema = %v, want %v", schema["$schema"], SchemaDialect)
	}
	id, _ := schema["$id"].(string)
	if id == "" {
		t.Fatal("$id is empty")
	}
	for _, bad := range []string{"file://", `C:\`, "Users", "AppData", "/home/", "tmp"} {
		if strings.Contains(id, bad) {
			t.Errorf("$id %q contains local/environment fragment %q", id, bad)
		}
	}
}

// TestSchemaAcceptsMinimalValidDocument proves the smallest legal document —
// one server with only the unconditionally required "listen" — validates.
func TestSchemaAcceptsMinimalValidDocument(t *testing.T) {
	schema := buildTestSchema(t)
	if err := minivalidate(schema, minimalValidConfig()); err != nil {
		t.Errorf("minimal document should validate: %v", err)
	}
}

// TestSchemaRejectsMissingServers and TestSchemaRejectsEmptyServers pin the
// two verified unconditional requirements at the root.
func TestSchemaRejectsMissingServers(t *testing.T) {
	schema := buildTestSchema(t)
	if err := minivalidate(schema, map[string]any{}); err == nil {
		t.Error("a document with no servers key should fail validation")
	}
}

func TestSchemaRejectsEmptyServers(t *testing.T) {
	schema := buildTestSchema(t)
	doc := map[string]any{"servers": []any{}}
	if err := minivalidate(schema, doc); err == nil {
		t.Error("an empty servers array should fail validation (minItems: 1)")
	}
}

// TestSchemaRejectsUnknownTopLevelProperty proves typed objects are closed
// (additionalProperties: false), matching Jul's strict TOML decoder.
func TestSchemaRejectsUnknownTopLevelProperty(t *testing.T) {
	schema := buildTestSchema(t)
	doc := minimalValidConfig()
	doc["nonexistent_field"] = true
	if err := minivalidate(schema, doc); err == nil {
		t.Error("an unknown top-level property should be rejected")
	}
}

// TestSchemaUpstreamPoolRequiresName pins the verified unconditional
// "upstreams[].name" requirement.
func TestSchemaUpstreamPoolRequiresName(t *testing.T) {
	schema := buildTestSchema(t)
	doc := minimalValidConfig()
	doc["upstreams"] = []any{map[string]any{}}
	if err := minivalidate(schema, doc); err == nil {
		t.Error("an upstream with no name should fail validation")
	}
	doc["upstreams"] = []any{map[string]any{"name": "api"}}
	if err := minivalidate(schema, doc); err != nil {
		t.Errorf("an upstream with a name should validate: %v", err)
	}
}

// TestSchemaUpstreamServerAcceptsStringOrObject proves the one dual wire
// shape in the schema (encoding.TextUnmarshaler on UpstreamServer) is
// rendered as a union rather than a closed object that would reject the
// common shorthand form.
func TestSchemaUpstreamServerAcceptsStringOrObject(t *testing.T) {
	schema := buildTestSchema(t)
	base := func(server any) map[string]any {
		doc := minimalValidConfig()
		doc["upstreams"] = []any{map[string]any{
			"name":    "api",
			"servers": []any{server},
		}}
		return doc
	}

	if err := minivalidate(schema, base("127.0.0.1:3000 weight=5")); err != nil {
		t.Errorf("bare address string should validate: %v", err)
	}
	if err := minivalidate(schema, base(map[string]any{"address": "127.0.0.1:3000", "weight": float64(5)})); err != nil {
		t.Errorf("object form should validate: %v", err)
	}
	if err := minivalidate(schema, base(map[string]any{"weight": float64(5)})); err == nil {
		t.Error("object form missing address should fail validation")
	}
}

// TestSchemaDurationPattern and TestSchemaSizePattern prove duration/size
// leaves are pattern-constrained strings, never numbers.
func TestSchemaDurationPattern(t *testing.T) {
	schema := buildTestSchema(t)
	base := func(ttl any) map[string]any {
		doc := minimalValidConfig()
		doc["cache"] = map[string]any{"default_ttl": ttl}
		return doc
	}
	if err := minivalidate(schema, base("30s")); err != nil {
		t.Errorf("\"30s\" should validate as a duration: %v", err)
	}
	if err := minivalidate(schema, base("1h30m")); err != nil {
		t.Errorf("\"1h30m\" should validate as a duration: %v", err)
	}
	if err := minivalidate(schema, base("not-a-duration")); err == nil {
		t.Error("\"not-a-duration\" should fail the duration pattern")
	}
	if err := minivalidate(schema, base(float64(30))); err == nil {
		t.Error("a bare number should fail: Duration is a string, never a number")
	}
}

func TestSchemaSizePattern(t *testing.T) {
	schema := buildTestSchema(t)
	base := func(sz any) map[string]any {
		doc := minimalValidConfig()
		doc["cache"] = map[string]any{"memory_max_size": sz}
		return doc
	}
	if err := minivalidate(schema, base("64m")); err != nil {
		t.Errorf("\"64m\" should validate as a size: %v", err)
	}
	if err := minivalidate(schema, base("1024")); err != nil {
		t.Errorf("\"1024\" (bare byte count) should validate as a size: %v", err)
	}
	if err := minivalidate(schema, base("huge")); err == nil {
		t.Error("\"huge\" should fail the size pattern")
	}
}

// TestSchemaEnumRejectsUnknownValue proves an audited enum is a structural
// constraint.
func TestSchemaEnumRejectsUnknownValue(t *testing.T) {
	schema := buildTestSchema(t)
	base := func(format any) map[string]any {
		doc := minimalValidConfig()
		doc["observability"] = map[string]any{"access_log": map[string]any{"format": format}}
		return doc
	}
	if err := minivalidate(schema, base("json")); err != nil {
		t.Errorf("\"json\" should validate: %v", err)
	}
	if err := minivalidate(schema, base("xml")); err == nil {
		t.Error("\"xml\" is not an allowed access_log format and should fail")
	}
}

// TestSchemaHTTPStatusBounds proves the "100..599" convention becomes a
// structural minimum/maximum.
func TestSchemaHTTPStatusBounds(t *testing.T) {
	schema := buildTestSchema(t)
	base := func(status any) map[string]any {
		doc := minimalValidConfig()
		doc["servers"] = []any{
			map[string]any{
				"listen": "127.0.0.1:8080",
				"locations": []any{
					map[string]any{
						"match":  map[string]any{"type": "exact", "path": "/"},
						"return": status,
					},
				},
			},
		}
		return doc
	}
	if err := minivalidate(schema, base(float64(204))); err != nil {
		t.Errorf("204 should validate: %v", err)
	}
	if err := minivalidate(schema, base(float64(50))); err == nil {
		t.Error("50 is below the 100..599 bound and should fail")
	}
	if err := minivalidate(schema, base(float64(700))); err == nil {
		t.Error("700 is above the 100..599 bound and should fail")
	}
}

// TestSchemaWAFParanoiaAcceptsZero is the regression test for the numeric
// bound parser trap case: "0 or 1..4" must not become minimum=1.
func TestSchemaWAFParanoiaAcceptsZero(t *testing.T) {
	schema := buildTestSchema(t)
	doc := minimalValidConfig()
	doc["waf"] = map[string]any{"paranoia": float64(0)}
	if err := minivalidate(schema, doc); err != nil {
		t.Errorf("waf.paranoia = 0 should validate (no mechanical bound should have been synthesized): %v", err)
	}
}

// TestSchemaNullRejectedEverywhere proves null is never a valid value,
// across every basic scalar type the schema uses.
func TestSchemaNullRejectedEverywhere(t *testing.T) {
	schema := buildTestSchema(t)
	cases := []func(v any) map[string]any{
		func(v any) map[string]any {
			doc := minimalValidConfig()
			doc["cache"] = map[string]any{"enabled": v}
			return doc
		},
		func(v any) map[string]any {
			doc := minimalValidConfig()
			doc["cache"] = map[string]any{"default_ttl": v}
			return doc
		},
		func(v any) map[string]any {
			doc := minimalValidConfig()
			doc["global"] = map[string]any{"redact_min_secret_length": v}
			return doc
		},
	}
	for i, build := range cases {
		if err := minivalidate(schema, build(nil)); err == nil {
			t.Errorf("case %d: null should be rejected", i)
		}
	}
}

// TestSchemaForwardedHeadersOmittedVsEmptyBothValid proves the
// omitted-vs-explicit-empty distinction is preserved: neither representation
// is rejected by the schema (the distinction is a data concern, and the
// schema must not collapse it by, for example, requiring the key or forcing
// a minItems that would reject the security-relevant empty case).
func TestSchemaForwardedHeadersOmittedVsEmptyBothValid(t *testing.T) {
	schema := buildTestSchema(t)
	base := func(clientAddr map[string]any) map[string]any {
		doc := minimalValidConfig()
		doc["servers"] = []any{
			map[string]any{"listen": "127.0.0.1:8080", "client_address": clientAddr},
		}
		return doc
	}
	if err := minivalidate(schema, base(map[string]any{})); err != nil {
		t.Errorf("omitted forwarded_headers should validate: %v", err)
	}
	if err := minivalidate(schema, base(map[string]any{"forwarded_headers": []any{}})); err != nil {
		t.Errorf("explicit empty forwarded_headers should validate: %v", err)
	}
}

// TestRuntimeAsymmetrySchemaValidRuntimeInvalid is the required
// schema-valid/runtime-invalid asymmetry test (ADR 0019 §22.2): an explicitly
// empty match.methods list satisfies the generated JSON Schema (no
// mechanical minItems was added, since that rule is conditional/cross-field)
// but is rejected by Jul's own runtime config.Validate.
func TestRuntimeAsymmetrySchemaValidRuntimeInvalid(t *testing.T) {
	schema := buildTestSchema(t)
	doc := minimalValidConfig()
	doc["servers"] = []any{
		map[string]any{
			"listen": "127.0.0.1:8080",
			"locations": []any{
				map[string]any{
					"match": map[string]any{
						"type":    "exact",
						"path":    "/",
						"methods": []any{},
					},
				},
			},
		},
	}
	if err := minivalidate(schema, doc); err != nil {
		t.Fatalf("an explicitly empty match.methods should satisfy the generated schema: %v", err)
	}

	// The equivalent real configuration: Jul's runtime validator rejects an
	// explicitly empty methods list as "a route that can never match".
	cfg := config.Config{
		Servers: []config.ServerConfig{{
			Listen: "127.0.0.1:8080",
			Locations: []config.LocationConfig{{
				Match: config.MatchConfig{Type: "exact", Path: "/", Methods: []string{}},
			}},
		}},
	}
	if err := config.Validate(&cfg); err == nil {
		t.Fatal("an explicitly empty match.methods should be rejected by Jul's runtime validation")
	}
}
