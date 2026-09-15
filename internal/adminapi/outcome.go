// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

// ReloadOutcome is the bounded outcome vocabulary shared by v1 configuration
// mutation, status, ledger, and history responses. The app layer adds staged
// and owned_not_serving to the server's reload outcomes.
type ReloadOutcome string

const (
	OutcomeAppliedLive     ReloadOutcome = "applied_live"
	OutcomeAppliedDegraded ReloadOutcome = "applied_degraded"
	OutcomeNoChange        ReloadOutcome = "no_change"
	OutcomeNotApplied      ReloadOutcome = "not_applied"
	OutcomeSavedNotLive    ReloadOutcome = "saved_not_live"
	OutcomeStaged          ReloadOutcome = "staged"
	OutcomeOwnedNotServing ReloadOutcome = "owned_not_serving"
)

// ReloadOutcomes returns the public enum in stable contract order.
func ReloadOutcomes() []ReloadOutcome {
	return []ReloadOutcome{
		OutcomeAppliedLive,
		OutcomeAppliedDegraded,
		OutcomeNoChange,
		OutcomeNotApplied,
		OutcomeSavedNotLive,
		OutcomeStaged,
		OutcomeOwnedNotServing,
	}
}
