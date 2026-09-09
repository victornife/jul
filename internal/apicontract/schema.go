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

func schemaFor(t reflect.Type, self string) (*Schema, error) {
	switch t.Kind() {
	case reflect.Pointer:
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
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map %s has non-string keys; JSON object keys must be strings", t)
		}
		value, err := refOrInline(t.Elem(), self)
		if err != nil {
			return nil, fmt.Errorf("map %s value: %w", t, err)
		}
		return &Schema{Type: "object", AdditionalPropertiesSchema: value}, nil
	case reflect.Struct:
		return structSchema(t, self)
	default:
		return nil, fmt.Errorf("cannot render %s (%s) as a schema", t, t.Kind())
	}
}

func structSchema(t reflect.Type, self string) (*Schema, error) {
	s := &Schema{
		Type:                 "object",
		Properties:           map[string]*Schema{},
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
		if f.Anonymous && name == "" {
			embedded := f.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				sub, err := structSchema(embedded, self)
				if err != nil {
					return nil, fmt.Errorf("%s.%s: %w", t, f.Name, err)
				}
				for k, v := range sub.Properties {
					s.Properties[k] = v
				}
				required = append(required, sub.Required...)
				continue
			}
		}
		if name == "" {
			return nil, fmt.Errorf("%s.%s has no json tag; every external DTO field names its wire key explicitly", t, f.Name)
		}
		fs, err := refOrInline(f.Type, self)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t, f.Name, err)
		}
		s.Properties[name] = fs
		if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") && f.Type.Kind() != reflect.Pointer {
			required = append(required, name)
		}
	}

	sort.Strings(required)
	s.Required = required
	return s, nil
}

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
