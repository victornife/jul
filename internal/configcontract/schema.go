// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"jul/internal/config"
)

// SchemaDialect is the JSON Schema dialect every generated schema declares
// (ADR 0019 §23.1).
const SchemaDialect = "https://json-schema.org/draft/2020-12/schema"

// SchemaID identifies the generated schema. It is tied to the real,
// version-controlled location of the artifact in Jul's own repository —
// never a local checkout path, developer username, or invented domain.
const SchemaID = "https://github.com/victornife/jul/blob/main/docs/generated/config.schema.json"

// requiredChildren is the small, explicit, evidence-backed table of
// unconditionally required properties. ADR 0019 §22.1 warns against
// mechanically assuming every non-pointer Go field is required in the source
// TOML; every entry here was confirmed by reading the corresponding
// unconditional check in internal/config/validate*.go, not inferred from the
// Go type. Every other object renders with an empty "required" list.
var requiredChildren = map[string][]string{
	"":                            {"servers"},      // "at least one [[servers]] block is required"
	"upstreams.*":                 {"name"},         // "upstream 'name' is required"
	"upstreams.*.servers.*":       {"address"},      // "address is required" (object form only)
	"servers.*":                   {"listen"},       // "'listen' is required"
	"stream.*":                    {"listen"},       // "'listen' is required"
	"servers.*.locations.*.match": {"type", "path"}, // "match.type/match.path is required"
}

// RenderSchema builds the complete JSON Schema 2020-12 document for Jul's
// configuration surface (ADR 0019 §5/§21-23), rendered entirely from c and
// config.SchemaPaths() — the only schema walker. It never reads GoType
// strings to recover structure; container shape comes from
// config.SchemaPath.Structure.
func RenderSchema(c Contract) ([]byte, error) {
	b := &schemaBuilder{
		all:    config.SchemaPaths(),
		leaves: make(map[string]Field, len(c.Leaves)),
	}
	for _, f := range c.Leaves {
		b.leaves[f.Path] = f
	}

	doc := map[string]any{
		"$schema":         SchemaDialect,
		"$id":             SchemaID,
		"x-jul-generated": strings.ReplaceAll(generatedBanner, "\n", " "),
		"$comment": "Jul configuration contract v" + strconv.Itoa(ContractVersion) + ". " +
			"Schema validity is necessary and not sufficient; Jul's runtime " +
			"configuration validation (`jul check`) remains authoritative. A " +
			"document may satisfy this schema and still fail `jul check` (a " +
			"cross-field rule), and a document may pass `jul check` while " +
			"`jul lint` reports an error-severity finding — lint policy is " +
			"never converted into structural invalidity here.",
		"title":                "Jul configuration",
		"type":                 "object",
		"additionalProperties": false,
		"properties":           b.propertiesFor(""),
	}
	if req := requiredChildren[""]; len(req) > 0 {
		doc["required"] = req
	}

	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

type schemaBuilder struct {
	all    []config.SchemaPath
	leaves map[string]Field
}

// directChildren returns the schema paths that are direct children of
// elementPrefix: for "" (root) every path with no ".", otherwise every path
// under elementPrefix+"." with no further ".".
func (b *schemaBuilder) directChildren(elementPrefix string) []config.SchemaPath {
	var out []config.SchemaPath
	for _, p := range b.all {
		rel := p.Path
		if elementPrefix != "" {
			trimmed := strings.TrimPrefix(p.Path, elementPrefix+".")
			if trimmed == p.Path {
				continue // not under elementPrefix at all
			}
			rel = trimmed
		}
		if strings.Contains(rel, ".") {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// propertiesFor builds the JSON Schema "properties" object for every direct
// child of elementPrefix, keyed by its own last path segment.
func (b *schemaBuilder) propertiesFor(elementPrefix string) map[string]any {
	props := map[string]any{}
	for _, p := range b.directChildren(elementPrefix) {
		key := lastSegment(p.Path)
		props[key] = b.nodeFor(p)
	}
	return props
}

// nodeFor renders one schema node (leaf or container).
func (b *schemaBuilder) nodeFor(p config.SchemaPath) map[string]any {
	if p.Kind != config.KindTable {
		return b.leafSchema(p.Path)
	}

	switch p.Structure {
	case config.StructObject:
		node := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           b.propertiesFor(p.Path),
		}
		if req := requiredChildren[p.Path]; len(req) > 0 {
			node["required"] = req
		}
		return node

	case config.StructArrayTable:
		elementPrefix := p.Path + ".*"
		itemSchema := b.objectSchemaAt(elementPrefix)
		var items any = itemSchema
		if p.TextScalar {
			items = map[string]any{"oneOf": []any{
				map[string]any{"type": "string"},
				itemSchema,
			}}
		}
		node := map[string]any{"type": "array", "items": items}
		if p.Path == "servers" {
			node["minItems"] = 1
		}
		return node

	case config.StructMapTable:
		elementPrefix := p.Path + ".*"
		return map[string]any{
			"type":                 "object",
			"additionalProperties": b.objectSchemaAt(elementPrefix),
		}

	case config.StructMapScalar:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": b.leafSchema(p.Path + ".*"),
		}

	default:
		// StructOpen has no live example in the current schema (see
		// config.StructOpen's doc comment); render it as intentionally open
		// rather than silently closing it if one is ever added.
		return map[string]any{}
	}
}

// objectSchemaAt renders the closed-object schema for one element of a
// dynamic collection (an array-of-tables item or a map-of-tables value).
func (b *schemaBuilder) objectSchemaAt(elementPrefix string) map[string]any {
	node := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           b.propertiesFor(elementPrefix),
	}
	if req := requiredChildren[elementPrefix]; len(req) > 0 {
		node["required"] = req
	}
	return node
}

// leafSchema renders a KindScalar or KindList leaf, including every
// mechanically sound constraint from the joined value contract, capability
// registry and lifecycle metadata.
func (b *schemaBuilder) leafSchema(path string) map[string]any {
	f, ok := b.leaves[path]
	if !ok {
		// Unreachable when c was produced by Build (every leaf is present);
		// render an empty schema rather than panicking on a malformed input.
		return map[string]any{}
	}

	var node map[string]any
	if f.Kind == config.KindList {
		node = map[string]any{"type": "array", "items": scalarTypeSchema(f.Scalar)}
		applyListValueContract(node, f)
	} else {
		node = scalarTypeSchema(f.Scalar)
		applyScalarValueContract(node, f)
	}

	if f.Description != "" {
		node["description"] = f.Description
	}
	if f.Deprecated {
		node["deprecated"] = true
	}
	if f.Ignored {
		node["x-jul-ignored"] = true
	}
	if f.Reserved {
		node["x-jul-reserved"] = true
	}
	if f.Secret {
		node["x-jul-secret"] = true
	}
	if len(f.Capabilities) > 0 {
		node["x-jul-capability"] = f.Capabilities
	}
	if f.HasValueContract && f.Constraint != "" {
		node["x-jul-constraint"] = f.Constraint
	}
	return node
}

// scalarTypeSchema maps a config.ScalarKind to its JSON Schema projection
// (ADR 0019 §23.1). Duration and Size are constrained strings, never numbers.
func scalarTypeSchema(k config.ScalarKind) map[string]any {
	switch k {
	case config.ScalarString:
		return map[string]any{"type": "string"}
	case config.ScalarBool:
		return map[string]any{"type": "boolean"}
	case config.ScalarInteger:
		return map[string]any{"type": "integer"}
	case config.ScalarFloat:
		return map[string]any{"type": "number"}
	case config.ScalarDuration:
		return map[string]any{"type": "string", "pattern": DurationPattern}
	case config.ScalarSize:
		return map[string]any{"type": "string", "pattern": SizePattern}
	case config.ScalarTime:
		return map[string]any{"type": "string", "format": "date-time"}
	default:
		return map[string]any{}
	}
}

// applyScalarValueContract adds mechanically sound constraints for a scalar
// leaf. Prose that has no sound translation (most "grammar"/"grammar_list"
// constraints) is deliberately left as the x-jul-constraint annotation
// leafSchema already attaches, never guessed into a pattern.
func applyScalarValueContract(node map[string]any, f Field) {
	if !f.HasValueContract {
		return
	}
	switch f.ValueKind {
	case "enum", "enum_or_url":
		if len(f.Allowed) > 0 {
			node["enum"] = f.Allowed
		}
	case "integer_enum":
		if len(f.IntegerEnum) > 0 {
			node["enum"] = f.IntegerEnum
		}
	case "integer", "ratio", "http_status":
		applyNumericBound(node, f.NumericBound)
	}
}

// applyListValueContract adds constraints to a KindList leaf's items (and the
// array itself), for the "enum_list" and "grammar_list" kinds.
func applyListValueContract(node map[string]any, f Field) {
	if !f.HasValueContract {
		return
	}
	items, _ := node["items"].(map[string]any)
	switch f.ValueKind {
	case "enum_list":
		if len(f.Allowed) > 0 && items != nil {
			items["enum"] = f.Allowed
		}
		node["uniqueItems"] = true
	}
}

func applyNumericBound(node map[string]any, b NumericBound) {
	if !b.HasBound {
		return
	}
	if b.Min != nil {
		node["minimum"] = *b.Min
	}
	if b.Max != nil {
		node["maximum"] = *b.Max
	}
}

func lastSegment(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[i+1:]
	}
	return path
}
