// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package apicontract

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"jul/internal/adminapi"
)

// schemaFor reflects a Go DTO into a JSON Schema. It is the mechanism ADR 0019
// §29 requires instead of a hand-maintained parallel schema: the Go type is the
// source, so the two cannot disagree.
//
// A nested type that is itself a registered component becomes a $ref rather
// than being inlined, so the document has one definition of each shape and a
// generated client has one Go/TypeScript type per component.
func schemaFor(t reflect.Type, self string) (*Schema, error) {
	switch t.Kind() {
	case reflect.Pointer:
		// A pointer field is an optional field, not a nullable type: the DTOs
		// use pointers where "omitted" and "present and zero" must stay
		// distinct, and omitempty already expresses the optionality.
		return schemaFor(t.Elem(), self)

	case reflect.String:
		return &Schema{Type: "string"}, nil

	case reflect.Bool:
		return &Schema{Type: "boolean"}, nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer", Format: "int64"}, nil

	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}, nil

	case reflect.Slice, reflect.Array:
		items, err := refOrInline(t.Elem(), self)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "array", Items: items}, nil

	case reflect.Map:
		// A map renders as a free-form JSON object, which publishes an
		// unbounded shape — exactly what the closed Details struct exists to
		// avoid. Declaring a struct with named fields is the alternative, and
		// it is always available.
		return nil, fmt.Errorf("map-typed field of kind %s is not part of the external contract: "+
			"a free-form object publishes an unbounded shape; declare a struct with named fields instead", t)

	case reflect.Struct:
		return structSchema(t, self)

	default:
		return nil, fmt.Errorf("cannot render %s (%s) as a schema", t, t.Kind())
	}
}

func structSchema(t reflect.Type, self string) (*Schema, error) {
	s := &Schema{
		Type:       "object",
		Properties: map[string]*Schema{},
		// Unknown request fields are rejected (ADR 0019 §24a), and the schema
		// says so rather than leaving a client to discover it from a 400.
		AdditionalProperties: boolPtr(false),
	}
	var required []string

	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if name == "" {
			return nil, fmt.Errorf("%s.%s has no json tag; every external DTO field names its wire key explicitly", t, f.Name)
		}

		fs, err := refOrInline(f.Type, self)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t, f.Name, err)
		}
		s.Properties[name] = fs

		// A field is required when it is always emitted: neither omitempty nor
		// omitzero, and not a pointer.
		if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") && f.Type.Kind() != reflect.Pointer {
			required = append(required, name)
		}
	}

	sort.Strings(required)
	s.Required = required
	return s, nil
}

// refOrInline emits a $ref when t is a registered component and is not the
// component currently being rendered, which is what keeps a self-referential
// type from recursing forever while still giving nested components one
// definition each.
func refOrInline(t reflect.Type, self string) (*Schema, error) {
	base := t
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	if name, ok := adminapi.ComponentNameFor(base); ok && name != self {
		return &Schema{Ref: "#/components/schemas/" + name}, nil
	}
	return schemaFor(t, self)
}

func boolPtr(b bool) *bool { return &b }
