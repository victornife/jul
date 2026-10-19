// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"sort"

	"jul/internal/config"
)

// routeSelectorVersion distinguishes corrected selectors from pre-#643
// projections. Their base_version can be identical, so CAS alone cannot
// distinguish an old per-block ordinal from a correct cross-block ordinal.
const routeSelectorVersion = "global-v1"

// routeOrdinals computes ADR 0018 §14 ordinals once per immutable snapshot.
// Raw strings and name multiplicities are preserved; nil and empty name lists
// compare equally, exactly as stringSetsEqual does. JSON frames arbitrary
// strings unambiguously (including delimiters and embedded NULs).
// Cost: O(locations + sum(names per block * log(names per block))).
func routeOrdinals(c *config.Config) [][]int {
	type coordinate struct{ server, matchType, path string }
	counts := make(map[coordinate]int)
	ordinals := make([][]int, len(c.Servers))
	for i, srv := range c.Servers {
		names := append([]string{}, srv.ServerNames...)
		sort.Strings(names)
		framed, _ := json.Marshal(struct {
			Listen string
			Names  []string
		}{srv.Listen, names})
		serverKey := string(framed)
		ordinals[i] = make([]int, len(srv.Locations))
		for j, loc := range srv.Locations {
			key := coordinate{serverKey, loc.Match.Type, loc.Match.Path}
			ordinals[i][j] = counts[key]
			counts[key]++
		}
	}
	return ordinals
}
