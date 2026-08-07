// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"reflect"
	"sort"
	"strings"
	"time"
)

// PathKind classifies a node of the configuration schema inventory.
type PathKind string

const (
	// KindScalar is one configurable value: a string, number, boolean,
	// Duration, Size, or timestamp.
	KindScalar PathKind = "scalar"
	// KindList is an ordered list of scalars (e.g. server_names).
	KindList PathKind = "list"
	// KindTable is a container whose configurable values are inventoried as
	// descendant paths: a struct, an array table, or a map of tables.
	KindTable PathKind = "table"
)

// SchemaPath is one node of the public TOML configuration surface reachable
// from Config. Paths use dot notation and represent every dynamic collection
// key — array-table index, plugin name, RBAC principal name, map key — as the
// canonical wildcard segment "*", never as a key harvested from one document.
//
// SchemaPath carries no configured values, so an inventory is safe to render
// into generated documentation and machine metadata.
type SchemaPath struct {
	// Path is the canonical dotted TOML path, e.g.
	// "servers.*.tls.client_auth.ca_file".
	Path string
	// Kind distinguishes leaves (scalar, list) from containers (table).
	Kind PathKind
	// GoType is the declared Go type of the field, for reviewer context.
	GoType string
	// Optional is true when the field is a pointer (an omitted table is
	// distinguishable from an empty one) or carries the omitempty tag option.
	Optional bool
	// Dynamic is true when the path contains at least one wildcard segment,
	// meaning it expands to one instance per configured collection element.
	Dynamic bool
}

// IsLeaf reports whether the path is a configurable value rather than a
// container that only groups other paths.
func (p SchemaPath) IsLeaf() bool { return p.Kind != KindTable }

// SchemaPaths returns every public TOML path reachable from Config, containers
// included, sorted by path. The result is deterministic and independent of the
// build tags in effect: the schema is declared in one tag-free file, so a lean
// binary inventories exactly the same surface as a fully tagged one.
//
// It is the single schema-reflection implementation in the repository;
// lifecycle classification, generated configuration references, and contract
// tooling consume it rather than re-walking the struct tree.
func SchemaPaths() []SchemaPath {
	var out []SchemaPath
	walkSchema(reflect.TypeOf(Config{}), "", false, map[reflect.Type]bool{}, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// SchemaLeaves returns only the configurable values (scalars and scalar lists),
// sorted by path. Containers are omitted because they carry no value of their
// own; their children are inventoried individually.
func SchemaLeaves() []SchemaPath {
	all := SchemaPaths()
	out := make([]SchemaPath, 0, len(all))
	for _, p := range all {
		if p.IsLeaf() {
			out = append(out, p)
		}
	}
	return out
}

var timeType = reflect.TypeOf(time.Time{})

// walkSchema appends the inventory of t under prefix. visiting guards against a
// self-referential schema type; the current schema has none, but a future
// recursive block must not hang the generator.
func walkSchema(t reflect.Type, prefix string, dynamic bool, visiting map[reflect.Type]bool, out *[]SchemaPath) {
	t = derefType(t)
	if t.Kind() != reflect.Struct || visiting[t] {
		return
	}
	visiting[t] = true
	defer delete(visiting, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, ok := tomlName(f)
		if !ok {
			continue
		}
		path := joinSchemaPath(prefix, name)
		optional := opts["omitempty"] || f.Type.Kind() == reflect.Pointer
		ft := derefType(f.Type)

		switch {
		case isScalarType(ft):
			*out = append(*out, SchemaPath{Path: path, Kind: KindScalar, GoType: goTypeName(f.Type), Optional: optional, Dynamic: dynamic})

		case ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array:
			et := derefType(ft.Elem())
			if et.Kind() == reflect.Struct && !isScalarType(et) {
				*out = append(*out, SchemaPath{Path: path, Kind: KindTable, GoType: goTypeName(f.Type), Optional: optional, Dynamic: dynamic})
				walkSchema(et, path+".*", true, visiting, out)
				continue
			}
			*out = append(*out, SchemaPath{Path: path, Kind: KindList, GoType: goTypeName(f.Type), Optional: optional, Dynamic: dynamic})

		case ft.Kind() == reflect.Map:
			et := derefType(ft.Elem())
			*out = append(*out, SchemaPath{Path: path, Kind: KindTable, GoType: goTypeName(f.Type), Optional: optional, Dynamic: dynamic})
			if et.Kind() == reflect.Struct && !isScalarType(et) {
				walkSchema(et, path+".*", true, visiting, out)
				continue
			}
			// A map of scalars contributes exactly one canonical leaf for its
			// operator-chosen keys.
			*out = append(*out, SchemaPath{Path: path + ".*", Kind: KindScalar, GoType: goTypeName(ft.Elem()), Optional: false, Dynamic: true})

		case ft.Kind() == reflect.Struct:
			*out = append(*out, SchemaPath{Path: path, Kind: KindTable, GoType: goTypeName(f.Type), Optional: optional, Dynamic: dynamic})
			walkSchema(ft, path, dynamic, visiting, out)
		}
	}
}

// tomlName returns the TOML key and tag options for a struct field, reporting
// false for fields that are not part of the public configuration surface.
func tomlName(f reflect.StructField) (string, map[string]bool, bool) {
	if f.PkgPath != "" {
		return "", nil, false // unexported
	}
	tag := f.Tag.Get("toml")
	if tag == "" || tag == "-" {
		return "", nil, false
	}
	parts := strings.Split(tag, ",")
	name := strings.TrimSpace(parts[0])
	if name == "" || name == "-" {
		return "", nil, false
	}
	opts := make(map[string]bool, len(parts)-1)
	for _, o := range parts[1:] {
		opts[strings.TrimSpace(o)] = true
	}
	return name, opts, true
}

// isScalarType reports whether t is a single configurable value. Named scalar
// types (Duration, Size) and time.Time are scalars even though their kinds are
// integer or struct.
func isScalarType(t reflect.Type) bool {
	if t == timeType {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// goTypeName renders a type without its import path so generated artifacts stay
// independent of the checkout location.
func goTypeName(t reflect.Type) string {
	s := t.String()
	return strings.ReplaceAll(s, "config.", "")
}

func joinSchemaPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
