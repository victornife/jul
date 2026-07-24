// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"jul/internal/config"
	"jul/internal/server"
)

// RecordManagedHistory writes the configuration-history snapshot for a managed
// apply at terminalization (AC-05). It is the trusted in-process writer invoked
// by the coordinator's WriteManagedHistory hook: previousRaw is the exact prior
// on-disk configuration and is sensitive, so it is consumed here to produce a
// snapshot and its redacted metadata sidecar and is never logged or retained.
//
// The reason matrix is derived purely from the terminal result:
//   - a committed apply (applied_live / applied_degraded) or a committed stage
//     (create or update) or a committed rollback records a pre_apply snapshot
//     of the prior configuration;
//   - a failed apply whose restoration also FAILED records a recovery snapshot
//     so the prior configuration remains recoverable even though the candidate
//     is still on disk;
//   - a failed apply that was cleanly restored, and any pre-write rejection,
//     record nothing.
//
// It returns the snapshot id and a non-fatal degradation error: a metadata
// sidecar failure never discards the raw snapshot (the TOML alone is still
// roll-back-able), and a raw snapshot failure is surfaced as a degraded-history
// condition rather than failing the already-committed apply.
func (s *Server) RecordManagedHistory(reqCtx ApplyRequestContext, result ConfigApplyResult, previousRaw []byte) (string, error) {
	if s == nil || !s.hist.enabled() {
		return "", nil
	}
	reason, record := managedHistoryDecision(result)
	if !record || len(previousRaw) == 0 {
		return "", nil
	}
	meta := &HistoryMetadata{
		ApplyID:          result.ApplyID,
		Operation:        reqCtx.Operation,
		Mode:             result.Mode,
		Outcome:          managedOutcomeString(result),
		Actor:            reqCtx.Actor,
		Reason:           reason,
		PreviousVersion:  managedCanonicalVersion(previousRaw),
		CandidateVersion: result.PersistedVersion,
	}
	id, metaErr, err := s.hist.snapshotWithMeta(previousRaw, meta)
	if err != nil {
		// Raw snapshot failed. The apply already committed; surface the failure
		// as a degraded-history condition rather than a failed save (AC-14).
		if s.log != nil {
			s.log.Warn("managed history snapshot failed", "error", err)
		}
		return "", err
	}
	if metaErr != nil {
		// The raw snapshot is on disk and remains roll-back-able; only the
		// provenance sidecar is missing. Report it as degraded, not fatal.
		if s.log != nil {
			s.log.Warn("managed history metadata sidecar degraded", "error", metaErr)
		}
		return id, metaErr
	}
	return id, nil
}

// managedHistoryDecision maps a terminal managed-apply result to its history
// reason and whether a snapshot should be written. It never inspects raw config
// bytes — only the structured outcome flags — so the policy stays auditable.
func managedHistoryDecision(result ConfigApplyResult) (reason string, record bool) {
	if result.OK {
		// applied_live, applied_degraded, committed stage, committed rollback.
		return historyReasonPreApply, true
	}
	// A failed apply only snapshots when restoration was attempted and failed,
	// so the prior configuration stays recoverable while the candidate lingers.
	if result.RestoreError != "" && !result.Restored {
		return historyReasonRecovery, true
	}
	return "", false
}

// managedOutcomeString returns the low-cardinality reload outcome for the
// metadata sidecar, or "" for stage_restart applies that submit no reload.
func managedOutcomeString(result ConfigApplyResult) string {
	if result.Reload != nil {
		return string(result.Reload.Outcome)
	}
	return ""
}

// managedCanonicalVersion returns the canonical unresolved version of raw
// configuration bytes, or "" when they cannot be parsed.
func managedCanonicalVersion(raw []byte) string {
	cfg, err := config.Parse(raw)
	if err != nil {
		return ""
	}
	return server.CanonicalVersion(cfg)
}
