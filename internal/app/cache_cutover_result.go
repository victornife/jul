// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import "jul/internal/cachemeta"

const cacheCutoverRecoveryGuidance = "Configuration is live. Inspect cache health, repair storage, then use the versioned cache purge flow for the previewed scopes; startup reconciles retained durable intents. Do not reapply or roll back solely to retry cache cleanup."

// CacheCutoverResult projects only the matching generation's outcome. Rejected,
// aborted and no-op reloads must never observe an earlier publish's degradation.
func (f *HandlerFactory) CacheCutoverResult(genID uint64) *cachemeta.CutoverResult {
	f.moduleMu.Lock()
	defer f.moduleMu.Unlock()
	if genID != f.lastCutoverGen || f.Cache == nil {
		return nil
	}
	out := &cachemeta.CutoverResult{State: "not_required", InvalidationEffective: "effective", Operations: len(f.lastCutover)}
	if len(f.lastCutover) > 0 {
		out.State = "succeeded"
	}
	for _, r := range f.lastCutover {
		out.InvalidatedResources += r.InvalidatedResources
		out.FailedEntries += r.FailedEntries
		out.RemainingEntries += r.RemainingEntries
		switch r.InvalidationEffective {
		case "uncertain":
			out.InvalidationEffective = "uncertain"
		case "incomplete":
			if out.InvalidationEffective != "uncertain" {
				out.InvalidationEffective = "incomplete"
			}
		case "effective":
		default:
			out.InvalidationEffective = "uncertain"
		}
	}
	if f.lastCutoverErr != nil && out.InvalidationEffective == "effective" {
		out.InvalidationEffective = "uncertain"
	}
	if out.InvalidationEffective != "effective" {
		out.State = "partial"
		if out.InvalidationEffective == "uncertain" {
			out.State = "uncertain"
		}
		out.RecoveryGuidance = cacheCutoverRecoveryGuidance
	}
	return out
}
