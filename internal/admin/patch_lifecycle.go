// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"sort"

	"jul/internal/lifecycle"
)

// patchLifecycleChange is the value-free public projection of one canonical
// lifecycle change. Class names and subsystems are closed strings; no configured
// before/after value is ever serialized.
type patchLifecycleChange struct {
	Path      string `json:"path"`
	Declared  string `json:"declared"`
	Effective string `json:"effective"`
	Subsystem string `json:"subsystem"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail,omitempty"`
	Ignored   bool   `json:"ignored"`
	Reserved  bool   `json:"reserved"`
}

// patchLifecycleSummary is the stable wire contract for canonical lifecycle
// classification in structured patch assessment responses.
type patchLifecycleSummary struct {
	Changes                 []patchLifecycleChange `json:"changes"`
	CanApplyHot             bool                   `json:"can_apply_hot"`
	CanStageRestart         bool                   `json:"can_stage_restart"`
	HotPaths                []string               `json:"hot_paths"`
	RestartRequiredPaths    []string               `json:"restart_required_paths"`
	NewListenerOnlyPaths    []string               `json:"new_listener_only_paths"`
	IgnoredDeprecatedPaths  []string               `json:"ignored_deprecated_paths"`
	ValidationRejectedPaths []string               `json:"validation_rejected_paths"`
	PendingSubsystems       []string               `json:"pending_subsystems"`
}

func (s *Server) patchLifecycleProjection(result lifecycle.Result, valid bool) patchLifecycleSummary {
	changes := make([]patchLifecycleChange, 0, len(result.Changes))
	hotPaths := make([]string, 0, len(result.Changes))
	pending := make(map[string]struct{})
	for _, change := range result.Changes {
		changes = append(changes, patchLifecycleChange{
			Path:      change.Path,
			Declared:  change.Declared.String(),
			Effective: change.Effective.String(),
			Subsystem: string(change.Subsystem),
			Reason:    change.Reason,
			Detail:    change.Detail,
			Ignored:   change.Ignored,
			Reserved:  change.Reserved,
		})
		if change.Effective == lifecycle.HotReloadClass {
			hotPaths = append(hotPaths, change.Path)
		}
		if change.Effective == lifecycle.RestartRequiredClass {
			pending[string(change.Subsystem)] = struct{}{}
		}
	}
	pendingSubsystems := make([]string, 0, len(pending))
	for subsystem := range pending {
		pendingSubsystems = append(pendingSubsystems, subsystem)
	}
	sort.Strings(hotPaths)
	sort.Strings(pendingSubsystems)

	canStage := valid && result.CanStageRestart
	if canStage && s.deps.PendingRestart != nil {
		if status := s.deps.PendingRestart(); status != nil && status.Inconsistent {
			canStage = false
		}
	}
	return patchLifecycleSummary{
		Changes:                 changes,
		CanApplyHot:             valid && result.CanApplyHot,
		CanStageRestart:         canStage,
		HotPaths:                hotPaths,
		RestartRequiredPaths:    copyStrings(result.RestartRequired),
		NewListenerOnlyPaths:    copyStrings(result.NewListenerOnly),
		IgnoredDeprecatedPaths:  copyStrings(result.IgnoredDeprecated),
		ValidationRejectedPaths: copyStrings(result.ValidationRejected),
		PendingSubsystems:       pendingSubsystems,
	}
}

func copyStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return append([]string(nil), in...)
}
