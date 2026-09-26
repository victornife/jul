// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"jul/internal/admin"
	"jul/internal/atomicfile"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

// DiscardPlannedRestart clears any staged planned restart. When the store is
// file-backed it performs the safety-verified discard from §17.5: it checks
// marker consistency, disk digest, and live serving version before restoring
// the backup. On success the watcher echo of the restoration is suppressed.
func (c *ConfigApplyCoordinator) DiscardPlannedRestart() (ApplyResult, error) {
	return c.DiscardPlannedRestartWithContext(admin.ApplyRequestContext{})
}

// DiscardPlannedRestartWithContext is the authenticated external form of the
// planned-restart discard. When the request carries idempotency metadata it
// reserves the key on the existing managed-apply ledger while applyMu/c.mu
// still guard the mutation, immediately before the first discard side effect.
// A successful discard is terminalized on that same ledger; a pre-commit
// failure removes the provisional reservation so a safe retry can proceed.
func (c *ConfigApplyCoordinator) DiscardPlannedRestartWithContext(reqCtx admin.ApplyRequestContext) (ApplyResult, error) {
	if c.Authority == AuthorityFileOwned {
		return ApplyResult{OK: false, AuthorityDenied: true, Message: "Configuration is file-owned; the running server does not write it."}, nil
	}

	// Serialize with applyMu so refresh, discard, and apply cannot interleave.
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	// H-02: refresh authoritative state before deciding whether a discard is
	// safe and what the marker contains.
	if err := c.refreshStateLocked(); err != nil {
		return ApplyResult{
			OK:             false,
			Mode:           ApplyHot,
			Message:        "Planned-restart state refresh failed: " + err.Error(),
			PendingRestart: c.plannedRestartStatus(),
		}, nil
	}

	c.mu.Lock()
	// Re-verify the HTTP handler's canonical baseline under the same mutation
	// lock that protects the discard. This closes the preview-to-discard race:
	// the public API has no force mode, so a stale base_version must fail before
	// the idempotency record or any staging/history state can change.
	if reqCtx.Baseline != nil {
		changed, current, err := c.verifyBaselineLocked(*reqCtx.Baseline)
		if err != nil {
			c.mu.Unlock()
			return ApplyResult{
				OK:      false,
				Mode:    ApplyHot,
				Message: "The persisted configuration could not be verified safely.",
			}, err
		}
		if changed {
			c.mu.Unlock()
			return c.conflictResult(ApplyHot, reqCtx.Baseline.Version, reqCtx.Baseline.Version, current), nil
		}
	}
	if c.PlannedRestart == nil || !c.PlannedRestart.IsPending() {
		c.mu.Unlock()
		return ApplyResult{
			OK:      true,
			Mode:    ApplyHot,
			Message: "No planned restart was pending.",
		}, nil
	}

	applyID := ""
	idempotencyReserved := false
	idempotencyCommitted := false
	if reqCtx.Idempotency != nil {
		applyID = c.nextID()
		if err := c.reserveManagedApplyIdempotency(reqCtx, applyID, ApplyHot); err != nil {
			c.mu.Unlock()
			return ApplyResult{
				ApplyID: applyID,
				OK:      false,
				Mode:    ApplyHot,
				Message: "The idempotency reservation could not be recorded; nothing was discarded.",
			}, fmt.Errorf("reserve discard idempotency: %w", err)
		}
		idempotencyReserved = true
	}
	defer func() {
		if idempotencyReserved && !idempotencyCommitted {
			c.abortManagedApplyIdempotency(reqCtx, applyID)
		}
	}()

	// File-backed safe discard.
	if c.PlannedRestart.ConfigPath != "" {
		var liveVersion string
		if c.LiveSnapshot != nil {
			liveVersion = server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig)
		}
		restoredBytes, err := c.PlannedRestart.DiscardSafe(liveVersion)
		if err != nil {
			c.mu.Unlock()
			return ApplyResult{
				ApplyID: applyID,
				OK:      false,
				Mode:    ApplyHot,
				Message: "Discard failed: " + err.Error(),
			}, err
		}
		idempotencyCommitted = true
		// Suppress the watcher echo of the restoration write.
		restoreDigest := sha256.Sum256(restoredBytes)
		c.suppressWatcher(restoreDigest)
		result := ApplyResult{
			ApplyID: applyID,
			OK:      true,
			Mode:    ApplyHot,
			Message: "Planned restart discarded and previous configuration restored.",
		}
		// ADR 0019 §11.2.3: a verified discard rewinds the baseline to the
		// restored bytes, the same as a T-write, so the discard is not
		// mistaken for drift.
		if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
			if err := c.ManagedBaseline.CommitMark(restoredBytes, canonicalVersionFromRaw(restoredBytes)); err != nil {
				result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedBaselineError, Message: "baseline could not be rewound after discard"})
			}
		}
		c.mu.Unlock()
		if idempotencyReserved {
			result = c.completeManagedApply(reqCtx, result, nil)
		}
		return result, nil
	}

	// In-memory discard (tests / no config path).
	if _, ok := c.PlannedRestart.Discard(); !ok {
		c.mu.Unlock()
		return ApplyResult{
			ApplyID: applyID,
			OK:      true,
			Mode:    ApplyHot,
			Message: "No planned restart was pending.",
		}, nil
	}
	idempotencyCommitted = true
	result := ApplyResult{
		ApplyID: applyID,
		OK:      true,
		Mode:    ApplyHot,
		Message: "Planned restart discarded.",
	}
	c.mu.Unlock()
	if idempotencyReserved {
		result = c.completeManagedApply(reqCtx, result, nil)
	}
	return result, nil
}

// PlannedRestartStatus returns the current managed planned-restart status as
// an admin.PendingRestartStatus, or nil when no staged restart is pending.
// It refreshes authoritative state under applyMu before exposing the status so
// status responses never return stale state (H-02).
func (c *ConfigApplyCoordinator) PlannedRestartStatus() *admin.PendingRestartStatus {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	_ = c.refreshStateLocked() // fail-closed: errors leave inconsistent flag set
	return c.plannedRestartStatus()
}

// applyStageRestart runs the stage_restart path: validates via stage preflight,
// writes the sidecar backup+marker, then writes the candidate atomically,
// and returns an ApplyResult without submitting a live reload.
//
// If a managed staged restart is already pending, this path replaces it with
// the new candidate while preserving the original serving config as the
// rollback base (H-03 staged update).
//
// Correct crash-consistent ordering (C-01 fix):
//  1. baseRaw is the original serving config (from disk for fresh stage, from
//     the existing marker/backup for a staged update).
//  2. StageManaged writes .bak (fresh stage only) and prepared marker.
//  3. atomicfile.Write writes the candidate.
//  4. PromoteToStaged promotes the marker to "staged" only after the candidate write succeeds.
func (c *ConfigApplyCoordinator) applyStageRestart(pctx context.Context, reqCtx admin.ApplyRequestContext, id string, cfg *config.Config, preparedCandidate *config.Candidate, prevCfg *config.Config, data []byte, baseline admin.MutationBaseline) (ApplyResult, error) {
	// H-03: determine whether this is a staged update. If a managed staged
	// restart is already pending, the new candidate replaces it but the
	// original serving config remains the rollback base and the diff base.
	isUpdate := c.PlannedRestart != nil && c.PlannedRestart.IsPending()

	var baseRaw []byte
	var diffBaseCfg *config.Config
	if isUpdate {
		baseRaw = c.PlannedRestart.BaseRaw()
		if len(baseRaw) > 0 {
			if raw, err := config.Parse(baseRaw); err == nil {
				if cand, err := config.NewCandidateContext(pctx, raw); err == nil {
					diffBaseCfg = cand.Effective
				} else {
					diffBaseCfg = raw
				}
			}
		}
	} else {
		baseRaw = baseline.Raw
		diffBaseCfg = prevCfg
	}

	// Run the shared preflight gates in stage-restart mode so restart-required
	// classification is retained rather than rejected. For updates the diff is
	// computed against the original serving config, not the previously staged
	// candidate. AC-08: pctx bounds resolution + every gate with the serving
	// reload_timeout; a pre-persistence breach surfaces as a phase-specific
	// 504 with disk untouched (nothing has been staged yet).
	pfResult, timedOutPhase, err := c.runPreflight(pctx, cfg, preparedCandidate, diffBaseCfg, PreflightStageRestart)
	if timedOutPhase != "" {
		return c.timedOutResult(ApplyStageRestart, timedOutPhase), nil
	}
	if err != nil {
		return ApplyResult{
			OK:               false,
			Mode:             ApplyStageRestart,
			Message:          "The configuration contains errors; no change was staged.",
			ValidationErrors: []string{err.Error()},
		}, nil
	}
	defer pfResult.PreparedAdmin.Abort()

	// E3 (M-02): use pfResult.Candidate directly — Preflight.Apply already
	// resolves secrets and validates the config, so a second NewCandidate call
	// is redundant and produces a stale resolved copy.
	persistedVersion := server.CanonicalVersion(pfResult.Candidate.Raw)
	desiredVersion := server.CanonicalVersion(pfResult.Candidate.Effective)
	liveVersion := ""
	if c.LiveSnapshot != nil {
		liveVersion = server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig)
	}

	// Collect subsystem names from the lifecycle diff.
	subsystems := subsystemNames(pfResult.Lifecycle)

	// A stage_restart should only be accepted when the candidate cannot be
	// fully hot-applied. If no restart-required changes exist, reject and tell
	// the operator to use hot apply instead.
	if len(subsystems) == 0 {
		return ApplyResult{
			OK:               false,
			Mode:             ApplyStageRestart,
			Version:          persistedVersion,
			PersistedVersion: persistedVersion,
			DesiredVersion:   desiredVersion,
			Message:          "No restart-required changes detected; use a hot apply instead.",
		}, nil
	}

	// Build the marker. BaseCanonicalVersion and BaseServingVersion describe the
	// originally-serving config, not the candidate. For updates StageManaged
	// preserves the existing base fields; for fresh stages it uses these values.
	marker := PlannedRestartMarker{
		BaseServingVersion:     liveVersion,
		BaseCanonicalVersion:   baseline.Version,
		StagedRawSHA256:        sha256Hex(data),
		StagedVersion:          desiredVersion,
		StagedPersistedVersion: persistedVersion,
		PendingSubsystems:      subsystems,
	}

	// Bind staging to the exact bytes authorized by the HTTP handler before any
	// recovery sidecar is written.
	c.mu.Lock()
	changed, currentVersion, verifyErr := c.verifyBaselineLocked(baseline)
	c.mu.Unlock()
	if verifyErr != nil {
		return ApplyResult{OK: false, Mode: ApplyStageRestart, Version: persistedVersion, PersistedVersion: persistedVersion, DesiredVersion: desiredVersion, Message: "The persisted configuration could not be verified safely."}, verifyErr
	}
	if changed {
		return c.conflictResult(ApplyStageRestart, persistedVersion, desiredVersion, currentVersion), nil
	}

	idempotencyReserved := false
	idempotencyCommitted := false
	defer func() {
		if idempotencyReserved && !idempotencyCommitted {
			c.abortManagedApplyIdempotency(reqCtx, id)
		}
	}()
	if err := c.reserveManagedApplyIdempotency(reqCtx, id, ApplyStageRestart); err != nil {
		return ApplyResult{
			ApplyID:          id,
			OK:               false,
			Mode:             ApplyStageRestart,
			Version:          persistedVersion,
			PersistedVersion: persistedVersion,
			DesiredVersion:   desiredVersion,
			Message:          "The idempotency reservation could not be recorded; nothing was staged.",
		}, fmt.Errorf("reserve managed stage idempotency: %w", err)
	}
	idempotencyReserved = reqCtx.Idempotency != nil

	// Step 1+2: Write backup (baseRaw, fresh stage only) and prepared marker
	// BEFORE writing the candidate to disk. StageManaged preserves the existing
	// backup and base metadata when this is an update.
	var previousMarker *PlannedRestartMarker
	if c.PlannedRestart != nil {
		previousMarker, _ = c.PlannedRestart.LoadMarker()
		if err := c.PlannedRestart.StageManaged(baseRaw, data, marker); err != nil {
			// Sidecar write failed; nothing has changed on disk yet. Return error.
			return ApplyResult{
				OK:               false,
				Mode:             ApplyStageRestart,
				Version:          persistedVersion,
				PersistedVersion: persistedVersion,
				DesiredVersion:   desiredVersion,
				Message:          "Failed to write planned-restart sidecar: " + err.Error(),
			}, err
		}
	}
	if c.beforePersist != nil {
		c.beforePersist(ApplyStageRestart)
	}

	// Step 3: Now that backup+marker are safely on disk, verify the same expected
	// base again immediately before writing the candidate.
	c.mu.Lock()
	changed, currentVersion, verifyErr = c.verifyBaselineLocked(baseline)
	if verifyErr != nil {
		c.mu.Unlock()
		if c.PlannedRestart != nil {
			if cleanupErr := c.PlannedRestart.AbortPrepared(previousMarker); cleanupErr != nil {
				return ApplyResult{OK: false, Mode: ApplyStageRestart, Version: persistedVersion, PersistedVersion: persistedVersion, DesiredVersion: desiredVersion, Message: "The staged recovery sidecar could not be rolled back safely."}, fmt.Errorf("%w: abort prepared stage after verification failure: %v", admin.ErrConfigStorageUnavailable, cleanupErr)
			}
		}
		return ApplyResult{OK: false, Mode: ApplyStageRestart, Version: persistedVersion, PersistedVersion: persistedVersion, DesiredVersion: desiredVersion, Message: "The persisted configuration could not be verified safely."}, verifyErr
	}
	if changed {
		c.mu.Unlock()
		if c.PlannedRestart != nil {
			if cleanupErr := c.PlannedRestart.AbortPrepared(previousMarker); cleanupErr != nil {
				return ApplyResult{OK: false, Mode: ApplyStageRestart, Version: persistedVersion, PersistedVersion: persistedVersion, DesiredVersion: desiredVersion, Message: "The staged recovery sidecar could not be rolled back safely."}, fmt.Errorf("%w: abort prepared stage after baseline conflict: %v", admin.ErrConfigStorageUnavailable, cleanupErr)
			}
		}
		return c.conflictResult(ApplyStageRestart, persistedVersion, desiredVersion, currentVersion), nil
	}
	// T-write step 3 (ADR 0019 §11.2.3): the baseline "preparing" marker
	// follows the planned-restart .bak+prepared marker and precedes the
	// configuration rename, which is the commit point for both machines. The
	// prior digest/version come from the managed baseline's own current
	// record, not from baseRaw — a staged UPDATE's baseRaw is the original
	// pre-stage bytes, but the baseline already advanced to the first staged
	// candidate when that stage committed.
	if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
		bst := c.ManagedBaseline.Status()
		if err := c.ManagedBaseline.BeginWrite(bst.BaselineRawSHA256, bst.BaselineCanonicalVersion, sha256Hex(data), persistedVersion); err != nil {
			c.mu.Unlock()
			if c.PlannedRestart != nil {
				_ = c.PlannedRestart.AbortPrepared(previousMarker)
			}
			return ApplyResult{OK: false, Mode: ApplyStageRestart, Version: persistedVersion, PersistedVersion: persistedVersion, DesiredVersion: desiredVersion, Message: "Failed to record managed-baseline provenance; nothing was staged."}, fmt.Errorf("%w: begin managed baseline write: %v", admin.ErrConfigStorageUnavailable, err)
		}
	}
	rawDigest := sha256.Sum256(data)
	if err := atomicfile.Write(c.Path, data, 0o600); err != nil {
		c.mu.Unlock()
		// Candidate write failed. The prepared marker is on disk but the config
		// file is unchanged. Reconcile on the next startup will detect
		// prepared+disk==base and clean up automatically.
		return ApplyResult{
			OK:               false,
			Mode:             ApplyStageRestart,
			Version:          persistedVersion,
			PersistedVersion: persistedVersion,
			DesiredVersion:   desiredVersion,
			Message:          "Failed to persist staged configuration; sidecar marker preserved for reconciliation on restart.",
		}, err
	}
	idempotencyCommitted = true
	c.suppressWatcher(rawDigest)

	// AC-06: promote the marker to "staged" WHILE STILL HOLDING c.mu, using the
	// verified promotion that re-reads the active config and confirms it still
	// equals the candidate both immediately before and after the marker is
	// staged. Keeping c.mu held from the candidate write through the final disk
	// verification makes the write→verify→promote→verify sequence linearizable
	// with respect to other managed applies, and the disk checks detect an
	// external writer replacing the candidate in the promotion window. A crash
	// before promotion still leaves marker="prepared" with disk==candidate,
	// which Reconcile promotes; a crash before the candidate write leaves
	// marker="prepared" with disk==base, which Reconcile cleans up.
	var stagePromotionDegraded *DegradedEntry
	if c.PlannedRestart != nil {
		if err := c.PlannedRestart.PromoteToStagedVerified(data); err != nil {
			// An external write in the promotion window (or a state mismatch)
			// must not be reported as a successful stage. Map the disk-change
			// races to a conflict so the HTTP layer returns 409; genuine
			// state/programming errors surface as a storage error.
			if errors.Is(err, ErrStagedCandidateChanged) {
				c.mu.Unlock()
				conflict := c.conflictResult(ApplyStageRestart, persistedVersion, desiredVersion, currentVersion)
				conflict.ApplyID = id
				conflict.Persisted = true
				conflict = c.completeManagedApply(reqCtx, conflict, baseline.Raw)
				return conflict, nil
			}
			// ADR 0019 §11.2.3 row 5: the configuration rename already
			// committed (step 4) — only the "prepared"->"staged" transition
			// write itself failed. The stage still succeeds: Reconcile
			// promotes the still-"prepared" marker at the next start because
			// the file already matches the candidate, so run it now rather
			// than reporting a failure a restart would silently complete
			// anyway. Steps 6-7 (baseline) still run below.
			if rerr := c.PlannedRestart.Reconcile(); rerr != nil {
				c.mu.Unlock()
				failure := ApplyResult{
					ApplyID:          id,
					OK:               false,
					Mode:             ApplyStageRestart,
					Version:          persistedVersion,
					PersistedVersion: persistedVersion,
					DesiredVersion:   desiredVersion,
					Persisted:        true,
					Message:          "Failed to promote or reconcile the staged marker after candidate write: " + err.Error(),
				}
				failure = c.completeManagedApply(reqCtx, failure, baseline.Raw)
				return failure, err
			}
			stagePromotionDegraded = &DegradedEntry{Kind: DegradedStagingError, Message: "planned-restart promotion did not complete synchronously; the stage still converges at the next restart"}
		}
	}
	// Steps 6-7 (ADR 0019 §11.2.3): the baseline snapshot and promotion follow
	// the planned-restart "staged" promotion, so a failed baseline write can
	// never be lost to — or block — the outer machine's own guarantee.
	var stageDegraded *DegradedEntry
	if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
		if err := c.completeWriteAndReassessDrift(data, persistedVersion); err != nil {
			stageDegraded = &DegradedEntry{Kind: DegradedBaselineError, Message: "baseline snapshot could not be written"}
			// ApplyRaw still holds applyMu for this whole call, so the retry
			// runs inline rather than racing a later apply for it.
			c.retryBaselineWriteLocked(data, func(b []byte) error { return c.completeWriteAndReassessDrift(b, persistedVersion) })
		}
	}
	c.mu.Unlock()

	msg := "Configuration staged for the next process restart; the live runtime is unchanged."
	if isUpdate {
		msg = "Staged configuration updated for the next process restart; the live runtime is unchanged."
	}
	// §3.2 defect 8 / §3.8: a committed stage_restart is a persisted mutation,
	// so its terminal result carries the same first-class persistence truth as
	// the hot path before it is routed through completeManagedApply: Persisted
	// is true (the candidate bytes are on disk), FinalDiskVersion is the staged
	// candidate now on disk, and FinalServingVersion is the still-serving live
	// version (unchanged, since a stage does not touch the running runtime).
	result := ApplyResult{
		ApplyID:               id,
		OK:                    true,
		Mode:                  ApplyStageRestart,
		Version:               persistedVersion,
		PersistedVersion:      persistedVersion,
		DesiredVersion:        desiredVersion,
		ServingVersion:        liveVersion,
		Persisted:             true,
		FinalDiskVersion:      persistedVersion,
		FinalServingVersion:   liveVersion,
		PendingRestart:        c.plannedRestartStatus(),
		Message:               msg,
		StagedRestartIsUpdate: isUpdate,
	}
	if stageDegraded != nil {
		result.Degraded = append(result.Degraded, *stageDegraded)
	}
	if stagePromotionDegraded != nil {
		result.Degraded = append(result.Degraded, *stagePromotionDegraded)
	}
	// AC-05: a committed stage (create or update) snapshots the prior on-disk
	// configuration — the previous serving config for a fresh stage, or the
	// prior staged candidate for an update — because baseline.Raw is whatever
	// was persisted before this stage overwrote it. Recorded once, here at the
	// stage's terminal success, so no snapshot is written at a provisional 202.
	result = c.completeManagedApply(reqCtx, result, baseline.Raw)
	return result, nil
}

// subsystemNames extracts unique subsystem names from a lifecycle ChangeSet.
func subsystemNames(cs lifecycle.ChangeSet) []string {
	seen := make(map[string]struct{}, len(cs))
	var out []string
	for _, e := range cs {
		name := string(e.Subsystem)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func (c *ConfigApplyCoordinator) plannedRestartStatus() *admin.PendingRestartStatus {
	if c.PlannedRestart == nil {
		return nil
	}
	st := c.PlannedRestart.Status()
	if st.State == "" && !st.Managed && !st.Inconsistent && !st.External {
		return nil
	}
	res := &admin.PendingRestartStatus{
		State:            st.State,
		Managed:          st.Managed,
		Staged:           st.Staged,
		External:         st.External,
		DiscardAvailable: st.DiscardAvailable,
		Inconsistent:     st.Inconsistent,
		Subsystems:       st.Subsystems,
		StagedVersion:    st.StagedVersion,
		PersistedVersion: st.PersistedVersion,
		ServingVersion:   st.ServingVersion,
	}
	if !st.StagedAt.IsZero() {
		res.StagedAt = st.StagedAt.UTC().Format(time.RFC3339)
	}
	return res
}
