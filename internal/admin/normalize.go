// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "strings"

// normalizeStringSlice returns a cleaned copy of a user-supplied string list.
// It trims whitespace from each entry, drops blank entries, removes duplicates
// while preserving the first-seen order, and returns nil when nothing remains.
// Use it for every backend API field that accepts a list of strings so the
// console and other clients cannot create configs polluted by leading/trailing
// whitespace, empty slots, or repeated entries.
func normalizeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, s := range in {
		t := strings.TrimSpace(s)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
