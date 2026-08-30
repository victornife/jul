// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"fmt"
	"regexp"
)

// minivalidate is a small, purpose-built JSON Schema validator covering only
// the keyword subset RenderSchema emits (type, properties,
// additionalProperties [bool or schema], items, required, enum, pattern,
// minimum, maximum, minItems, uniqueItems, oneOf). It exists so tests assert
// real validation *behavior* against generated schema documents rather than
// scanning generated JSON for substrings, without adding a third-party JSON
// Schema dependency to the module.
//
// It intentionally does not implement the full 2020-12 spec (no $ref
// resolution, no $defs, no allOf/anyOf/not, no format assertion beyond
// date-time-shaped strings) because RenderSchema never emits those
// constructs — see schema.go's comment on why unevaluatedProperties is
// unnecessary here.
func minivalidate(schema map[string]any, doc any) error {
	return validateNode(schema, doc, "$")
}

func validateNode(schema map[string]any, doc any, path string) error {
	if oneOf, ok := schema["oneOf"].([]any); ok {
		return validateOneOf(oneOf, doc, path)
	}

	if want, ok := schema["type"].(string); ok {
		if err := validateType(want, doc, path); err != nil {
			return err
		}
	}

	switch v := doc.(type) {
	case map[string]any:
		if err := validateObject(schema, v, path); err != nil {
			return err
		}
	case []any:
		if err := validateArray(schema, v, path); err != nil {
			return err
		}
	case string:
		if pat, ok := schema["pattern"].(string); ok {
			re := regexp.MustCompile(pat)
			if !re.MatchString(v) {
				return fmt.Errorf("%s: %q does not match pattern %q", path, v, pat)
			}
		}
	case float64:
		if err := validateNumber(schema, v, path); err != nil {
			return err
		}
	}

	if enum, ok := schema["enum"]; ok {
		if !enumContains(enum, doc) {
			return fmt.Errorf("%s: %v is not one of the enumerated values %v", path, doc, enum)
		}
	}
	return nil
}

func validateOneOf(branches []any, doc any, path string) error {
	var lastErr error
	for _, b := range branches {
		bs, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if err := validateNode(bs, doc, path); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no branch matched")
	}
	return fmt.Errorf("%s: matches no oneOf branch (%w)", path, lastErr)
}

func validateType(want string, doc any, path string) error {
	if doc == nil {
		return fmt.Errorf("%s: null is not a valid value", path)
	}
	got := jsonKind(doc)
	if want == "integer" && got == "number" {
		f := doc.(float64)
		if f != float64(int64(f)) {
			return fmt.Errorf("%s: %v is not an integer", path, doc)
		}
		return nil
	}
	if got != want {
		return fmt.Errorf("%s: type = %s, want %s", path, got, want)
	}
	return nil
}

func jsonKind(doc any) string {
	switch doc.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", doc)
	}
}

func validateObject(schema map[string]any, obj map[string]any, path string) error {
	props, _ := schema["properties"].(map[string]any)
	for _, reqAny := range asStringSlice(schema["required"]) {
		if _, ok := obj[reqAny]; !ok {
			return fmt.Errorf("%s: missing required property %q", path, reqAny)
		}
	}
	for key, val := range obj {
		propSchema, known := props[key]
		if !known {
			switch ap := schema["additionalProperties"].(type) {
			case bool:
				if !ap {
					return fmt.Errorf("%s: unknown property %q rejected", path, key)
				}
			case map[string]any:
				if err := validateNode(ap, val, path+"."+key); err != nil {
					return err
				}
			}
			continue
		}
		ps, ok := propSchema.(map[string]any)
		if !ok {
			continue
		}
		if err := validateNode(ps, val, path+"."+key); err != nil {
			return err
		}
	}
	return nil
}

func validateArray(schema map[string]any, arr []any, path string) error {
	if minItems, ok := asFloat(schema["minItems"]); ok && float64(len(arr)) < minItems {
		return fmt.Errorf("%s: has %d items, want at least %v", path, len(arr), minItems)
	}
	if unique, ok := schema["uniqueItems"].(bool); ok && unique {
		seen := map[any]bool{}
		for _, v := range arr {
			if seen[v] {
				return fmt.Errorf("%s: duplicate item %v where uniqueItems is required", path, v)
			}
			seen[v] = true
		}
	}
	items, ok := schema["items"].(map[string]any)
	if !ok {
		return nil
	}
	for i, v := range arr {
		if err := validateNode(items, v, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func validateNumber(schema map[string]any, v float64, path string) error {
	if min, ok := asFloat(schema["minimum"]); ok && v < min {
		return fmt.Errorf("%s: %v is below minimum %v", path, v, min)
	}
	if max, ok := asFloat(schema["maximum"]); ok && v > max {
		return fmt.Errorf("%s: %v is above maximum %v", path, v, max)
	}
	return nil
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func asStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}

func enumContains(enum any, doc any) bool {
	list, ok := enum.([]any)
	if !ok {
		// enum built directly as []string/[]int64 before a JSON round-trip.
		switch e := enum.(type) {
		case []string:
			s, ok := doc.(string)
			if !ok {
				return false
			}
			for _, v := range e {
				if v == s {
					return true
				}
			}
			return false
		case []int64:
			f, ok := doc.(float64)
			if !ok {
				return false
			}
			for _, v := range e {
				if float64(v) == f {
					return true
				}
			}
			return false
		}
		return false
	}
	for _, v := range list {
		if fmt.Sprint(v) == fmt.Sprint(doc) {
			return true
		}
	}
	return false
}
