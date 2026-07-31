// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"jul/internal/config"
)

// DiffEntry describes one configuration field whose effective value changed
// between two resolved configs, according to the lifecycle registry.
type DiffEntry struct {
	Path      string
	Class     Class
	Subsystem string
	Reason    string
	Before    any
	After     any
}

// ChangeSet is the list of registered configuration fields that changed
// between two resolved configs, together with their lifecycle class and
// subsystem. It is the structured output of a stage-restart preflight
// classification so the coordinator can populate the pending-restart marker
// without re-running the diff.
type ChangeSet = []DiffEntry

// DiffConfig compares the effective values of every registered path between
// two configs and returns the paths that differ. It is the source of truth for
// completeness checks: any registered field that changed is reported here.
func DiffConfig(before, after *config.Config) []DiffEntry {
	var out []DiffEntry
	for _, e := range Registry {
		bv := extractRegisteredValue(before, e.Path)
		av := extractRegisteredValue(after, e.Path)
		if !deepEqualValues(bv, av) {
			out = append(out, DiffEntry{
				Path:      e.Path,
				Class:     e.Class,
				Subsystem: e.Subsystem,
				Reason:    e.Reason,
				Before:    bv,
				After:     av,
			})
		}
	}
	return out
}

// extractRegisteredValue returns the effective value for any registered path.
// Startup-consumed paths reuse the fingerprint extractors (which understand
// digests, file content, and effective values); the remaining paths are served
// by schema-derived extractors built in init().
func extractRegisteredValue(cfg *config.Config, path string) any {
	if v := extractValue(cfg, path); v != nil {
		return v
	}
	if fn, ok := registeredExtractors[path]; ok {
		return fn(cfg)
	}
	return nil
}
