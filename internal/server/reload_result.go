// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"jul/internal/config"
)

// ReloadOutcome is the terminal classification of a reload transaction.
type ReloadOutcome string

const (
	// ReloadAppliedLive means the new generation was published and all
	// post-commit side effects completed without degradation.
	ReloadAppliedLive ReloadOutcome = "applied_live"
	// ReloadAppliedDegraded means the new generation was published but at
	// least one post-commit side effect (certificate refresh, stream reload,
	// etc.) failed or the reload exceeded its deadline after Publish.
	ReloadAppliedDegraded ReloadOutcome = "applied_degraded"
	// ReloadNotApplied means the reload did not reach Publish and the
	// previous runtime state remains serving.
	ReloadNotApplied ReloadOutcome = "not_applied"
	// ReloadSavedNotLive is used by the app-layer coordinator when the
	// configuration was persisted but the live reload outcome is not yet
	// known (for example, the coordinator timed out waiting for the result).
	// The server itself never produces this outcome.
	ReloadSavedNotLive ReloadOutcome = "saved_not_live"
)

// ReloadSubsystemStatus reports the outcome for one subsystem.
type ReloadSubsystemStatus string

const (
	ReloadSubsystemOK       ReloadSubsystemStatus = "ok"
	ReloadSubsystemFailed   ReloadSubsystemStatus = "failed"
	ReloadSubsystemTimedOut ReloadSubsystemStatus = "timed_out"
	ReloadSubsystemSkipped  ReloadSubsystemStatus = "skipped"
	ReloadSubsystemNotRun   ReloadSubsystemStatus = "not_run"
)

// ReloadSubsystemResult carries per-subsystem timing and error details.
type ReloadSubsystemResult struct {
	Status     ReloadSubsystemStatus `json:"status"`
	DurationMS int64                 `json:"duration_ms,omitempty"`
	Error      string                `json:"error,omitempty"`
}

// ReloadResult is the structured, correlated outcome of a single reload
// transaction. It is produced by the server after every reload attempt and
// returned to callers that supplied a result channel on ReloadRequest.
type ReloadResult struct {
	ID             string                `json:"id"`
	Source         ReloadSource          `json:"source"`
	DesiredVersion string                `json:"desired_version,omitempty"`
	ServingVersion string                `json:"serving_version,omitempty"`
	StartedAt      time.Time             `json:"started_at"`
	CompletedAt    time.Time             `json:"completed_at"`
	DurationMS     int64                 `json:"duration_ms"`
	Outcome        ReloadOutcome         `json:"outcome"`
	Persisted      bool                  `json:"persisted"`
	Published      bool                  `json:"published"`
	TimedOut       bool                  `json:"timed_out"`
	TimedOutPhase  string                `json:"timed_out_phase,omitempty"`
	FailedPhase    string                `json:"failed_phase,omitempty"`
	HTTP           ReloadSubsystemResult `json:"http"`
	Stream         ReloadSubsystemResult `json:"stream"`
	Error          string                `json:"error,omitempty"`
}

// CanonicalVersion returns a short, stable fingerprint of a configuration
// used for optimistic concurrency and desired/serving version correlation. It
// is computed over the canonical marshaled form so it is insensitive to
// comments and whitespace in the on-disk file.
func CanonicalVersion(c *config.Config) string {
	if c == nil {
		return ""
	}
	data, err := config.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
