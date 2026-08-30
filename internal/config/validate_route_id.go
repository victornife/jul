// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import "fmt"

// routeIDMaxLen is the grammar's upper bound (ADR 0019 §4).
const routeIDMaxLen = 64

// validateRouteID checks the grammar of an optional route_id: absence is
// valid (a route without one is fully supported, never a degraded path);
// present-and-empty is rejected; length is 1-64 bytes; every byte is lowercase
// ASCII [a-z0-9_-]; the first byte must be alphanumeric. Global uniqueness
// across the whole configuration is checked by the caller (Validate), which
// alone has visibility across every server and location.
func validateRouteID(id *string, where string) []error {
	if id == nil {
		return nil
	}
	v := *id
	if v == "" {
		return []error{fmt.Errorf("%s: route_id is present and empty; omit the key to leave the route without a durable identity, or set a value", where)}
	}
	if len(v) > routeIDMaxLen {
		return []error{fmt.Errorf("%s: route_id %q is %d bytes, want at most %d", where, v, len(v), routeIDMaxLen)}
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !isRouteIDChar(c) {
			return []error{fmt.Errorf("%s: route_id %q contains %q; want lowercase ASCII [a-z0-9_-] only", where, v, string(c))}
		}
	}
	if first := v[0]; !isRouteIDAlnum(first) {
		return []error{fmt.Errorf("%s: route_id %q must start with a lowercase letter or digit, not %q", where, v, string(first))}
	}
	return nil
}

func isRouteIDChar(c byte) bool {
	return isRouteIDAlnum(c) || c == '_' || c == '-'
}

func isRouteIDAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}
