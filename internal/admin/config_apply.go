// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "jul/internal/server"

// ConfigApplyResult is the structured response for a managed configuration
// apply. It correlates the persisted candidate with its live reload outcome
// when a hot reload was performed.
type ConfigApplyResult struct {
	OK             bool                  `json:"ok"`
	Mode           string                `json:"mode"`
	Version        string                `json:"version,omitempty"`
	ServingVersion string                `json:"serving_version,omitempty"`
	Status         []FeatureStatus       `json:"status,omitempty"`
	Reload         *server.ReloadResult  `json:"reload,omitempty"`
	PendingRestart *PendingRestartStatus `json:"pending_restart,omitempty"`
	Message        string                `json:"message,omitempty"`
	// ValidationErrors is set when the candidate could not be parsed or failed
	// runtime preflight. The handler maps this field to a 400 Bad Request.
	ValidationErrors []string `json:"validation_errors,omitempty"`
	// RestartRequired is set when the candidate is valid but changes a
	// startup-bound setting. The handler maps this field to a 409 Conflict
	// carrying restart_required:true.
	RestartRequired bool `json:"restart_required,omitempty"`
	// CanStage is set together with RestartRequired when the candidate can be
	// staged for the next process restart instead of being rejected outright.
	CanStage bool `json:"can_stage,omitempty"`
	// Summary and Diff are populated by the structured-patch apply path.
	Summary []string   `json:"summary,omitempty"`
	Diff    ConfigDiff `json:"diff,omitempty"`
}

// PendingRestartStatus exposes whether a process restart is required before
// the on-disk configuration becomes fully live.
type PendingRestartStatus struct {
	Managed          bool     `json:"managed"`
	Staged           bool     `json:"staged"`
	StagedAt         string   `json:"staged_at,omitempty"`
	StagedVersion    string   `json:"staged_version,omitempty"`
	ServingVersion   string   `json:"serving_version,omitempty"`
	Subsystems       []string `json:"subsystems,omitempty"`
	DiscardAvailable bool     `json:"discard_available"`
	Inconsistent     bool     `json:"inconsistent"`
}
