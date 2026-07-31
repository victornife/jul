// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"net/http"

	"jul/internal/server"
)

// ErrConfigStorageUnavailable marks a failure to read or verify the persisted
// configuration. Mutation handlers map it to 503 and must not write anything.
var ErrConfigStorageUnavailable = errors.New("configuration storage unavailable")

func configApplyErrorStatus(result ConfigApplyResult, err error) int {
	if errors.Is(err, ErrConfigStorageUnavailable) {
		return http.StatusServiceUnavailable
	}
	if result.Reload != nil && result.Reload.FailedPhase == "enqueue" {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func isStructuredApplyError(result ConfigApplyResult) bool {
	return result.Reload != nil || result.ApplyID != "" || result.Mode != "" ||
		result.Message != "" || result.Version != "" || result.PersistedVersion != "" ||
		result.DesiredVersion != "" || result.Conflict || result.RestoreError != "" ||
		len(result.ValidationErrors) > 0
}

func configApplyResultStatus(result ConfigApplyResult) int {
	// AC-08: a reload_timeout breach BEFORE persistence surfaces as a
	// phase-specific 504 Gateway Timeout. Nothing was written to disk, so this
	// is a distinct outcome from a validation failure (400) or a
	// saved-not-live in-flight reload (202). Checked first because the result
	// carries neither ValidationErrors nor a Reload record.
	if result.TimedOutPhase != "" {
		return http.StatusGatewayTimeout
	}
	if len(result.ValidationErrors) > 0 {
		return http.StatusBadRequest
	}
	if result.Reload != nil && result.Reload.FailedPhase == "enqueue" {
		return http.StatusServiceUnavailable
	}
	if result.Reload != nil && result.Reload.Outcome == server.ReloadSavedNotLive {
		return http.StatusAccepted
	}
	if result.OK {
		return http.StatusOK
	}
	return http.StatusConflict
}

func isTerminalApplyResult(result ConfigApplyResult) bool {
	return result.Reload == nil || result.Reload.Outcome != server.ReloadSavedNotLive
}

// recordTimeoutAudit records a failure audit event for a managed configuration
// apply that exceeded reload_timeout BEFORE anything was persisted (AC-08 /
// defect 9). The operation is the per-handler managed operation so the trail
// distinguishes which mutation path timed out; the apply ID is appended only
// when the coordinator had allocated one before the timeout. The detail is
// non-sensitive transaction metadata only — the phase name and apply ID — and
// never carries configuration bytes, secrets, or token material.
func (s *Server) recordTimeoutAudit(r *http.Request, op ApplyOperation, result ConfigApplyResult) {
	detail := "timed out before persistence: phase=" + result.TimedOutPhase
	if result.ApplyID != "" {
		detail += " apply_id=" + result.ApplyID
	}
	s.recordAudit(r, string(op), "config", "failure", detail)
}

// managedApplyResolvePhase is the phase name reported when handler-side
// candidate resolution (secret/filesystem provider reads) exceeds the absolute
// managed-apply deadline before anything is persisted (AC-08).
const managedApplyResolvePhase = "resolve"

// candidatePreparationFailure classifies an error returned by handler-side
// candidate preparation (secret/config resolution under the absolute
// managed-apply deadline). A resolution deadline is a pre-persistence timeout
// (504 with a structured resolve-phase ConfigApplyResult), a cancellation is a
// client-abort (408), and any other error is an ordinary validation/rejection
// left to the caller (handled=false). No apply ID is set because the coordinator
// has not allocated one yet. It never reports persistence.
func candidatePreparationFailure(mode string, err error) (ConfigApplyResult, int, bool) {
	if mode == "" {
		mode = "hot"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ConfigApplyResult{
			OK:            false,
			Mode:          mode,
			TimedOutPhase: managedApplyResolvePhase,
			Message: "The configuration apply exceeded reload_timeout during the resolve phase; " +
				"nothing was changed. Investigate the slow secret or filesystem provider, " +
				"or raise global.reload_timeout.",
		}, http.StatusGatewayTimeout, true
	case errors.Is(err, context.Canceled):
		return ConfigApplyResult{
			OK:   false,
			Mode: mode,
			Message: "The configuration apply was canceled during the resolve phase " +
				"before anything was persisted.",
		}, http.StatusRequestTimeout, true
	default:
		return ConfigApplyResult{}, 0, false
	}
}

// writeCandidatePreparationFailure writes the structured timeout/cancel response
// for a candidate-preparation error and records the matching failure audit,
// returning true when it handled the error. A resolution deadline records the
// dedicated timeout audit (phase=resolve) and writes 504; a cancellation records
// a canceled-before-persistence audit and writes 408. Any other error is left to
// the caller's ordinary validation path (returns false). Nothing is persisted.
func (s *Server) writeCandidatePreparationFailure(w http.ResponseWriter, r *http.Request, reqCtx ApplyRequestContext, mode string, err error) bool {
	result, status, handled := candidatePreparationFailure(mode, err)
	if !handled {
		return false
	}
	if result.TimedOutPhase != "" {
		s.recordTimeoutAudit(r, reqCtx.Operation, result)
	} else {
		s.recordAudit(r, string(reqCtx.Operation), "config", "failure", "canceled before persistence: phase=resolve")
	}
	writeJSON(w, status, result)
	return true
}

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
	// TimedOutPhase names the transaction phase that exceeded reload_timeout
	// before the candidate was persisted (AC-08). When non-empty the handler
	// maps the result to 504 Gateway Timeout; the on-disk configuration is
	// unchanged. A timeout AFTER persistence is reported through Reload as
	// saved_not_live (202) instead, never here.
	TimedOutPhase string `json:"timed_out_phase,omitempty"`
	// FinalizationError carries a post-persistence transaction-tracking failure
	// (e.g. pending-record registration) from the coordinator to the trusted
	// composition-root completion callback. By the AC-05/AC-14 invariant this
	// provenance is threaded to the finalization surface, never serialized in
	// the apply result, so it is transport-only (json:"-").
	FinalizationError string `json:"-"`
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
