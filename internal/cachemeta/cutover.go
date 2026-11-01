// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cachemeta

// CutoverResult is the bounded, secret-safe cleanup outcome correlated with a
// configuration publication. It carries no selectors, paths, keys or I/O errors.
// A degraded cleanup never means the committed configuration was rolled back.
type CutoverResult struct {
	State                 string `json:"state"`
	InvalidationEffective string `json:"invalidation_effective"`
	Operations            int    `json:"operations"`
	InvalidatedResources  int    `json:"invalidated_resources"`
	FailedEntries         int    `json:"failed_entries"`
	RemainingEntries      int    `json:"remaining_entries"`
	RecoveryGuidance      string `json:"recovery_guidance,omitempty"`
}
