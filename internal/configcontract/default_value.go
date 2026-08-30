// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"fmt"
	"strconv"
	"strings"

	"jul/internal/config"
)

// convertDefaultValue converts a DefaultOverrides author string into the
// properly JSON-typed value its leaf's own Scalar/Kind calls for — a real
// JSON Schema `default` must be typed (a bool, a number, an array), not a
// string that merely looks like one. Duration/Size/time-of-day fields stay
// strings, matching their actual wire representation.
func convertDefaultValue(kind config.PathKind, scalar config.ScalarKind, raw string) (any, error) {
	if kind == config.KindList {
		items := parseBracketList(raw)
		if scalar == config.ScalarInteger {
			out := make([]int64, 0, len(items))
			for _, item := range items {
				n, err := strconv.ParseInt(item, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("default %q: list element %q is not an integer: %w", raw, item, err)
				}
				out = append(out, n)
			}
			return out, nil
		}
		return items, nil
	}

	switch scalar {
	case config.ScalarBool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("default %q is not a bool: %w", raw, err)
		}
		return v, nil
	case config.ScalarInteger:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("default %q is not an integer: %w", raw, err)
		}
		return v, nil
	case config.ScalarFloat:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("default %q is not a float: %w", raw, err)
		}
		return v, nil
	default:
		// ScalarString, ScalarDuration, ScalarSize, ScalarTime: the wire
		// representation is already a string.
		return raw, nil
	}
}

// parseBracketList parses the defaults table's "[a, b, c]" authoring
// shorthand into its elements. An empty "[]" yields an empty, non-nil slice
// rather than nil, so a rendered default is "[]", not absent.
func parseBracketList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return []string{}
	}
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}
