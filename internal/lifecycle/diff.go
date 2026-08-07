// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"sort"

	"jul/internal/config"
)

// DiffEntry describes one configuration path whose effective value changed
// between two resolved configurations, together with its disposition.
//
// It deliberately carries no before/after values. Preview, diff projections and
// audit records need the path, the class and the reason; carrying the values
// would put resolved secrets, certificate material and operator-defined
// identities into structured output that is rendered and logged.
type DiffEntry struct {
	Path      string
	Class     Class
	Subsystem Subsystem
	Reason    string
	// Ignored is true when no runtime consumer reads the path, so preview must
	// not claim the change was applied.
	Ignored bool
	// Reserved is true when configuration validation rejects the path.
	Reserved bool
}

// ChangeSet is the list of registered configuration paths that changed between
// two resolved configurations, in path order.
type ChangeSet = []DiffEntry

// DiffConfig compares the effective value of every registered path between two
// resolved configurations. It is the completeness layer: a registered field
// that changed is always reported here, whatever the higher-level comparators
// did or did not notice.
func DiffConfig(before, after *config.Config) ChangeSet {
	if before == nil || after == nil {
		return nil
	}
	out := make(ChangeSet, 0, 8)
	for _, e := range Registry {
		bv, ok1 := EffectiveValue(before, e.Path)
		av, ok2 := EffectiveValue(after, e.Path)
		if !ok1 || !ok2 {
			continue
		}
		if deepEqualValues(bv, av) {
			continue
		}
		out = append(out, DiffEntry{
			Path:      e.Path,
			Class:     e.Class,
			Subsystem: e.Subsystem,
			Reason:    e.Reason,
			Ignored:   e.Ignored,
			Reserved:  e.Reserved,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
