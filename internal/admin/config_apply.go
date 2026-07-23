// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"

	"jul/internal/server"
)

// ErrConfigStorageUnavailable marks a failure to read or verify the persisted
// configuration. Mutation handlers map it to 503 and must not write anything.
var ErrConfigStorageUnavailable = errors.New("configuration storage unavailable")

// ConfigApplyResult is the structured response for a managed configuration
// apply. It correlates the persisted candidate with its live reload outcome
// when a hot reload was performed.
type ConfigApplyResult struct {
	// ApplyID is the monotonic transaction ID, populated regardless of whether
	// a reload was submitted. This allows callbacks to record outcomes even
	// when Reload is nil (e.g., enqueue failure).
	ApplyID string `json:"apply_id,omitempty"`
	OK      bool   `json:"ok"`
	Mode    string `json:"mode"`
	// Version and PersistedVersion identify the canonical unresolved config on
	// disk. Version is retained as the optimistic-concurrency compatibility
	// field; new consumers should prefer PersistedVersion.
	Version          string `json:"version,omitempty"`
	PersistedVersion string `json:"persisted_version,omitempty"`
	// DesiredVersion identifies the resolved effective candidate submitted to
	// the runtime. ServingVersion identifies the resolved live runtime.
	DesiredVersion string                `json:"desired_version,omitempty"`
	ServingVersion string                `json:"serving_version,omitempty"`
	Conflict       bool                  `json:"conflict,omitempty"`
	CurrentVersion string                `json:"current_version,omitempty"`
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
	// Persisted is true when the candidate bytes were written to disk.
	Persisted bool `json:"persisted,omitempty"`
	// Restored is true when a rejected candidate was rolled back to the
	// previous configuration.
	Restored bool `json:"restored,omitempty"`
	// RestoreError is non-empty when restoration was attempted and failed.
	RestoreError string `json:"restore_error,omitempty"`
	// FinalDiskVersion is the canonical version of the on-disk file after the
	// apply completed (including any restoration).
	FinalDiskVersion string `json:"final_disk_version,omitempty"`
	// FinalServingVersion is the canonical version of the live serving config
	// at the time the result was produced.
	FinalServingVersion string `json:"final_serving_version,omitempty"`
	// StagedRestartIsUpdate is true when the stage_restart apply succeeded and
	// replaced an already-pending staged restart (update), false when it created
	// the first staged restart. The API handler uses this to emit the correct
	// audit event (config.stage_restart.updated vs config.stage_restart.created)
	// without re-reading disk state after the apply.
	StagedRestartIsUpdate bool `json:"staged_restart_is_update,omitempty"`
	// Summary and Diff are populated by the structured-patch apply path.
	Summary []string   `json:"summary,omitempty"`
	Diff    ConfigDiff `json:"diff,omitempty"`
}

// ConfigMutationResponse preserves compatibility metadata used by legacy raw,
// settings, and rollback clients while carrying the full managed result.
type ConfigMutationResponse struct {
	Status string `json:"status,omitempty"`
	ID     string `json:"id,omitempty"`
	ConfigApplyResult
}

// PendingRestartStatus exposes whether a process restart is required before
// the on-disk configuration becomes fully live.
type PendingRestartStatus struct {
	// State is the authoritative enum: "none", "managed_staged",
	// "external_divergence", or "inconsistent". The boolean fields below are
	// deprecated and retained for backward compatibility.
	State string `json:"state"`
	// Managed is true for managed staged restarts and inconsistent states.
	// Deprecated: use State.
	Managed bool `json:"managed"`
	// Staged is true only for managed staged restarts.
	// Deprecated: use State.
	Staged bool `json:"staged"`
	// External is true for external disk/runtime divergence.
	// Deprecated: use State.
	External         bool     `json:"external,omitempty"`
	StagedAt         string   `json:"staged_at,omitempty"`
	StagedVersion    string   `json:"staged_version,omitempty"`
	PersistedVersion string   `json:"persisted_version,omitempty"`
	ServingVersion   string   `json:"serving_version,omitempty"`
	Subsystems       []string `json:"subsystems,omitempty"`
	DiscardAvailable bool     `json:"discard_available"`
	Inconsistent     bool     `json:"inconsistent"`
}
