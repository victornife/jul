// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

// AdoptExternalAssessment is the side-effect-free result of assessing the
// current external file against the managed baseline (ADR 0019 §14/§14.1).
// PreviousRaw is nil when Origin is "no_baseline", so a caller never renders
// an empty diff where none is knowable.
type AdoptExternalAssessment struct {
	OK               bool
	Origin           string // drift | no_baseline | inconsistent
	ObservedDigest   string
	BaselineVersion  string
	CandidateRaw     []byte
	CandidateVersion string
	PreviousRaw      []byte
	RestartRequired  bool
	ValidationErrors []string
}

// originForBaselineState maps a managed-baseline state to the §11.2.1 origin
// discriminator adoption reports.
func originForBaselineState(state ConfigState) string {
	switch state {
	case ConfigStateManagedUnadopted:
		return "no_baseline"
	case ConfigStateManagedInconsistent:
		return "inconsistent"
	default:
		// managed_clean, managed_drift, managed_desired_ahead: there is a
		// prior managed configuration to diff against and to preserve in
		// history. Adopting already-clean bytes is an ownership no-op
		// establishment shaped exactly like resolving drift.
		return "drift"
	}
}

// AssessAdoptExternal reads the current external file and assesses it
// against the managed baseline without any side effect: strict decode,
// lifecycle classification (to report RestartRequired without rejecting),
// and the origin the operation would resolve. It never writes the marker,
// the snapshot, or the configuration file.
func (c *ConfigApplyCoordinator) AssessAdoptExternal() (AdoptExternalAssessment, error) {
	if c.Authority != AuthorityManaged || c.ManagedBaseline == nil {
		return AdoptExternalAssessment{}, errors.New("adoption is only available in managed mode")
	}
	raw, err := c.readConfigRaw()
	if err != nil {
		return AdoptExternalAssessment{}, fmt.Errorf("%w: read external file: %v", admin.ErrConfigStorageUnavailable, err)
	}
	digest := sha256Hex(raw)
	bst := c.ManagedBaseline.Status()
	origin := originForBaselineState(bst.State)

	cfg, perr := config.Parse(raw)
	if perr != nil {
		return AdoptExternalAssessment{
			Origin:           origin,
			ObservedDigest:   digest,
			BaselineVersion:  bst.BaselineCanonicalVersion,
			ValidationErrors: []string{perr.Error()},
		}, nil
	}

	var prevRaw []byte
	var prevCfg *config.Config
	if origin != "no_baseline" {
		snap, serr := c.ManagedBaseline.Snapshot()
		if serr != nil {
			// ADR 0019 §14 step 5/§11.2.1b: a baseline the marker claims exists
			// but whose snapshot bytes cannot be read is not "nothing prior" —
			// it is damage, and must be reported as such rather than silently
			// degrading into a no_baseline-shaped preview.
			c.ManagedBaseline.MarkInconsistent(ReasonSnapshotUnreadable)
			return AdoptExternalAssessment{
				Origin:           "inconsistent",
				ObservedDigest:   digest,
				BaselineVersion:  bst.BaselineCanonicalVersion,
				ValidationErrors: []string{fmt.Sprintf("managed baseline snapshot could not be read: %v", serr)},
			}, nil
		}
		prevRaw = snap
		prevCfg, _ = config.Parse(snap)
	}

	// PreflightStageRestart classifies restart-required changes instead of
	// rejecting them, which is what a preview needs: it never returns
	// admin.ErrRestartRequired (that error is only raised by the hot-apply
	// gates in another mode), so restart-required-ness has to be read back
	// from the classified lifecycle changeset, not from the error.
	pfResult, err := c.Preflight.Apply(context.Background(), cfg, prevCfg, PreflightStageRestart)
	if err != nil {
		return AdoptExternalAssessment{
			Origin:           origin,
			ObservedDigest:   digest,
			BaselineVersion:  bst.BaselineCanonicalVersion,
			ValidationErrors: []string{err.Error()},
		}, nil
	}
	restartRequired := false
	for _, e := range pfResult.Lifecycle {
		if e.Class == lifecycle.RestartRequiredClass {
			restartRequired = true
			break
		}
	}
	if pfResult.PreparedAdmin != nil {
		pfResult.PreparedAdmin.Abort()
	}

	return AdoptExternalAssessment{
		OK:               true,
		Origin:           origin,
		ObservedDigest:   digest,
		BaselineVersion:  bst.BaselineCanonicalVersion,
		CandidateRaw:     raw,
		CandidateVersion: server.CanonicalVersion(cfg),
		PreviousRaw:      prevRaw,
		RestartRequired:  restartRequired,
	}, nil
}

// AdoptExternal is the dedicated coordinator entry ADR 0019 §14.3 requires:
// every existing apply entry takes applyMu itself and its shared candidate
// path persists the candidate and restores on failure, both of which would be
// wrong here — adoption writes no configuration on any path and never
// restores, because there is nothing to restore. It acquires applyMu exactly
// once for the whole operation, including the digest fence.
func (c *ConfigApplyCoordinator) AdoptExternal(reqCtx admin.ApplyRequestContext, req admin.AdoptExternalRequest) (ApplyResult, error) {
	mode := ApplyMode(req.Mode)
	if mode == "" {
		mode = ApplyHot
	}
	if c.Authority == AuthorityFileOwned {
		return ApplyResult{OK: false, Mode: mode, AuthorityDenied: true, Message: "Configuration is file-owned; there is nothing to adopt."}, nil
	}
	if c.Authority != AuthorityManaged || c.ManagedBaseline == nil {
		return ApplyResult{OK: false, Mode: mode, Message: "Adoption is only available in managed mode."}, nil
	}
	if !req.Confirm {
		return ApplyResult{OK: false, Mode: mode, Message: "Adoption requires explicit confirmation that managed ownership will resume over these bytes."}, nil
	}

	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	// ADR 0019 §14.1: reject while a planned restart is pending rather than
	// silently discarding a staged, previewed candidate.
	if c.PlannedRestart != nil && c.PlannedRestart.IsPending() {
		return ApplyResult{
			OK:             false,
			Mode:           mode,
			Message:        "A planned restart is pending; discard or complete it before adopting an external file.",
			PendingRestart: c.plannedRestartStatus(),
		}, nil
	}

	bst := c.ManagedBaseline.Status()
	origin := originForBaselineState(bst.State)
	// ADR 0019 §14 step 6: adoption binds itself to BOTH the observed external
	// digest and the managed baseline version observed at preview time — an
	// omitted field must never silently disable the CAS it exists to enforce.
	// base_version has nothing to bind to when no baseline exists yet, so it is
	// required only when one does (origin != "no_baseline"); observed_digest is
	// always required, because a preview — including a no_baseline one — always
	// returns one.
	if origin != "no_baseline" {
		if req.BaseVersion == "" {
			return ApplyResult{OK: false, Mode: mode, Message: "A managed baseline exists; base_version from the preview is required to adopt."}, nil
		}
		if req.BaseVersion != bst.BaselineCanonicalVersion {
			return ApplyResult{OK: false, Mode: mode, Conflict: true, Message: "The managed baseline changed since this adoption was previewed; re-preview and try again."}, nil
		}
	}

	// §14 step 8 / §14.2: the digest fence. This read produces the buffer the
	// whole transaction uses; nothing re-reads the path after this point.
	raw, err := c.readConfigRaw()
	if err != nil {
		return ApplyResult{OK: false, Mode: mode, Message: "Failed to read the external configuration file."}, fmt.Errorf("%w: read external file for adoption: %v", admin.ErrConfigStorageUnavailable, err)
	}
	observed := sha256Hex(raw)
	if req.ObservedDigest == "" {
		return ApplyResult{OK: false, Mode: mode, Message: "observed_digest from the preview is required to adopt."}, nil
	}
	if observed != req.ObservedDigest {
		return ApplyResult{OK: false, Mode: mode, Conflict: true, Message: "The external file changed since it was previewed; re-preview and try again."}, nil
	}

	cfg, perr := config.Parse(raw)
	if perr != nil {
		return ApplyResult{OK: false, Mode: mode, Message: "The external configuration could not be parsed.", ValidationErrors: []string{perr.Error()}}, nil
	}

	var prevRaw []byte
	if origin != "no_baseline" {
		snap, serr := c.ManagedBaseline.Snapshot()
		if serr != nil {
			// See AssessAdoptExternal: a claimed baseline whose snapshot cannot
			// be read is damage, never a silent no_baseline degrade.
			c.ManagedBaseline.MarkInconsistent(ReasonSnapshotUnreadable)
			return ApplyResult{OK: false, Mode: mode, Message: "The managed baseline snapshot could not be read; nothing was adopted."}, fmt.Errorf("%w: read managed baseline snapshot: %v", admin.ErrConfigStorageUnavailable, serr)
		}
		prevRaw = snap
	}
	var prevCfg *config.Config
	if len(prevRaw) > 0 {
		prevCfg, _ = config.Parse(prevRaw)
	}

	pfMode := PreflightHot
	if mode == ApplyStageRestart {
		pfMode = PreflightStageRestart
	}
	pfResult, err := c.Preflight.Apply(context.Background(), cfg, prevCfg, pfMode)
	if err != nil {
		if errors.Is(err, admin.ErrRestartRequired) {
			return ApplyResult{OK: false, Mode: mode, RestartRequired: true, CanStage: true, Message: err.Error()}, nil
		}
		return ApplyResult{OK: false, Mode: mode, Message: "The external configuration contains errors; nothing was adopted.", ValidationErrors: []string{err.Error()}}, nil
	}
	preparedOwned := pfResult.PreparedAdmin != nil
	defer func() {
		if preparedOwned && pfResult.PreparedAdmin != nil {
			pfResult.PreparedAdmin.Abort()
		}
	}()

	persistedVersion := server.CanonicalVersion(cfg)
	desiredVersion := server.CanonicalVersion(pfResult.Candidate.Effective)
	id := c.nextID()

	result := ApplyResult{
		ApplyID:          id,
		OK:               true,
		Mode:             mode,
		Version:          persistedVersion,
		PersistedVersion: persistedVersion,
		DesiredVersion:   desiredVersion,
		Persisted:        true,
		FinalDiskVersion: persistedVersion,
		Origin:           origin,
	}

	if mode == ApplyStageRestart {
		if err := c.adoptAndStageLocked(origin, bst.BaselineRawSHA256, prevRaw, raw, persistedVersion, desiredVersion, pfResult, &result); err != nil {
			return result, err
		}
		result.PendingRestart = c.plannedRestartStatus()
		if result.Message == "" {
			result.Message = "External configuration adopted and staged for the next process restart."
		}
		result = c.completeManagedApply(reqCtx, result, prevRaw)
		return result, nil
	}

	// Hot adoption: T-mark commits the baseline first — no configuration is
	// written, so nothing can require rolling back. The marker write is the
	// commit point (ADR 0019 §11.2.1a): its failure means the operation never
	// happened and the prior baseline is intact. A snapshot-only failure is
	// different — the marker already committed, so the adoption succeeded and
	// only its provenance degraded (§33.2's baseline_error), which is why the
	// two writes are split rather than made through the combined CommitMark.
	if err := c.ManagedBaseline.CommitMarkerOnly(raw, persistedVersion); err != nil {
		return ApplyResult{OK: false, Mode: mode, Message: "Failed to record the adopted baseline; nothing was changed."}, fmt.Errorf("%w: commit managed baseline marker: %v", admin.ErrConfigStorageUnavailable, err)
	}
	if err := c.ManagedBaseline.CommitSnapshotOnly(raw); err != nil {
		result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedBaselineError, Message: "baseline snapshot could not be written after adoption"})
	}

	// ADR 0019 §14.2/§14.3: the one post-commit read, scoped exclusively to
	// drift assessment — it is never transaction input. It distinguishes an
	// external write ordered after the fence (adoption still succeeded; the
	// file has already drifted again) from a read failure (adoption still
	// succeeded; drift assessment is deferred to the next §12 trigger). It
	// never feeds the snapshot, the marker, the history snapshot, or the
	// reload below, all of which use the buffers already retained.
	postCommitRaw, postCommitErr := c.readConfigRaw()
	driftDetectedPostCommit := false
	switch {
	case postCommitErr != nil:
		result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedDriftUnknown, Message: "the configuration could not be re-read after adoption; drift assessment deferred"})
	case sha256Hex(postCommitRaw) != observed:
		var postVersion, postParseErr string
		if pcfg, perr := config.Parse(postCommitRaw); perr == nil {
			postVersion = server.CanonicalVersion(pcfg)
		} else {
			postParseErr = perr.Error()
		}
		c.ManagedBaseline.AssessDrift(postCommitRaw, nil, postVersion, postParseErr)
		driftDetectedPostCommit = true
		result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedDriftAfterAdopt, Message: "the file no longer matches the adopted candidate"})
	}

	liveVersion := ""
	if c.LiveSnapshot != nil {
		liveVersion = server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig)
	}
	result.ServingVersion = liveVersion
	if liveVersion == desiredVersion {
		// ADR 0019 §11.2.2: adoption after a restart is cheap — the bytes are
		// already live, so no reload is needed or performed.
		result.FinalServingVersion = liveVersion
		result.Message = "External configuration adopted; it was already live."
		result = c.completeManagedApply(reqCtx, result, prevRaw)
		return result, nil
	}

	rawDigest := sha256.Sum256(raw)
	resultCh := make(chan server.ReloadResult, 1)
	req2 := server.ReloadRequest{
		ID:            id,
		Source:        server.ReloadSourceAdmin,
		Candidate:     pfResult.Candidate,
		PreparedAdmin: pfResult.PreparedAdmin,
		RawDigest:     rawDigest,
		Result:        resultCh,
	}
	if err := c.SubmitReload(req2); err != nil {
		// ADR 0019 §14 step 10: a reload that cannot even be enqueued does not
		// fail the adoption — the bytes are owned, and nothing was written
		// that could be restored. The process enters managed_desired_ahead,
		// unless the post-commit fence already found the file drifted again —
		// that is strictly more urgent than desired_ahead and must not be
		// silently overwritten by an unrelated reload-enqueue failure.
		if !driftDetectedPostCommit {
			c.ManagedBaseline.MarkDesiredAhead()
		}
		result.AppOutcome = "owned_not_serving"
		result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedStagingIncomplete, Message: "reload could not be enqueued after adoption"})
		result.Message = "External configuration adopted, but the live reload could not be enqueued; a restart will converge it."
		result = c.completeManagedApply(reqCtx, result, prevRaw)
		return result, nil
	}
	preparedOwned = false
	c.suppressWatcher(rawDigest)
	rr := <-resultCh
	result.Reload = &rr
	result.ServingVersion = rr.ServingVersion
	result.FinalServingVersion = rr.ServingVersion
	if rr.Published {
		result.Message = "External configuration adopted and applied live."
	} else {
		// ADR 0019 §14 step 10: a reload that does not take does not fail the
		// adoption either, and is specifically NOT not_applied — nothing was
		// written that needs restoring.
		if !driftDetectedPostCommit {
			c.ManagedBaseline.MarkDesiredAhead()
		}
		result.AppOutcome = "owned_not_serving"
		result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedStagingIncomplete, Message: "adoption committed but the live reload did not take"})
		result.Message = "External configuration adopted, but the live reload did not take; a restart will converge it."
	}
	result = c.completeManagedApply(reqCtx, result, prevRaw)
	return result, nil
}

// adoptAndStageLocked implements ADR 0019 §11.2.4's adopt-and-stage
// composition, in the exact order and with the exact per-failure outcomes its
// table specifies. The caller holds applyMu.
//
//  1. (verified above) the retained baseline snapshot P must already match
//     origin: no_baseline is exempt because there is no prior baseline to
//     verify.
//  2. write .bak from P (never from the file — the file already holds the
//     candidate being adopted).
//  3. commit the managed-baseline marker to current(I) — the T-mark commit
//     point. Nothing after this point may be abandoned without being
//     reported as at least a degraded success, because a later Reconcile can
//     complete anything that follows it.
//  4. write the planned-restart marker "prepared".
//  5. promote it to "staged", verified (never the unverified variant —
//     §11.2.4.1).
//  6. overwrite the managed-baseline snapshot with I.
//
// origin and baselineDigest are the values AdoptExternal already computed
// from the managed-baseline status before the digest fence.
func (c *ConfigApplyCoordinator) adoptAndStageLocked(origin, baselineDigest string, prevRaw, candidateRaw []byte, persistedVersion, desiredVersion string, pfResult *PreflightResult, result *ApplyResult) error {
	if c.PlannedRestart == nil {
		result.Message = "Planned-restart staging is not available."
		return nil
	}

	// Step 1: the retained snapshot must actually be the bytes the baseline
	// marker names before it is trusted as a .bak source (§11.2.4 row 1).
	if origin != "no_baseline" {
		if baselineDigest == "" || sha256Hex(prevRaw) != baselineDigest {
			c.ManagedBaseline.MarkInconsistent(ReasonSnapshotDigestMismatch)
			result.OK = false
			result.Message = "The managed baseline snapshot could not be verified; nothing was staged."
			return fmt.Errorf("%w: baseline snapshot digest mismatch before adopt-and-stage", admin.ErrConfigStorageUnavailable)
		}
	}

	// Step 2.
	if err := c.PlannedRestart.WriteAdoptBackup(prevRaw); err != nil {
		result.OK = false
		result.Message = "Failed to back up the previous configuration; nothing was staged."
		return fmt.Errorf("%w: write adopt backup: %v", admin.ErrConfigStorageUnavailable, err)
	}

	// Step 3: the commit point.
	if err := c.ManagedBaseline.CommitMarkerOnly(candidateRaw, persistedVersion); err != nil {
		// Row 3: nothing can complete this — no planned marker exists yet, so
		// Reconcile would never collect the orphaned backup either.
		if clearErr := c.PlannedRestart.ClearStagingArtifacts(); clearErr != nil {
			c.ManagedBaseline.MarkInconsistent(ReasonCleanupIncomplete)
			result.OK = false
			result.Message = "Failed to commit the baseline, and cleanup of the orphaned backup also failed."
			return fmt.Errorf("%w: clear orphaned backup after baseline commit failure: %v", admin.ErrConfigStorageUnavailable, clearErr)
		}
		result.OK = false
		result.Message = "Failed to commit the adopted baseline; nothing was staged."
		return fmt.Errorf("%w: commit managed baseline marker: %v", admin.ErrConfigStorageUnavailable, err)
	}

	liveVersion := ""
	if c.LiveSnapshot != nil {
		liveVersion = server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig)
	}
	marker := PlannedRestartMarker{
		BaseServingVersion:     liveVersion,
		StagedRawSHA256:        sha256Hex(candidateRaw),
		StagedVersion:          desiredVersion,
		StagedPersistedVersion: persistedVersion,
		PendingSubsystems:      subsystemNames(pfResult.Lifecycle),
	}

	// Step 4. Everything from here on is reachable by Reconcile, and the
	// baseline has already committed: every remaining failure is reported as
	// a (possibly degraded) success, never as a failure a later restart could
	// then silently complete anyway.
	if err := c.PlannedRestart.WritePreparedAfterAdoptBackup(marker); err != nil {
		// Row 4: no planned marker exists, so Reconcile would never collect
		// the backup either — remove it. The adoption still succeeded.
		if clearErr := c.PlannedRestart.ClearStagingArtifacts(); clearErr != nil {
			c.ManagedBaseline.MarkInconsistent(ReasonCleanupIncomplete)
			result.OK = false
			result.Message = "The baseline committed, but cleanup of the abandoned stage failed; managed writes are refused until resolved."
			return fmt.Errorf("%w: clear abandoned backup after prepared-marker failure: %v", admin.ErrConfigStorageUnavailable, clearErr)
		}
		if snapErr := c.ManagedBaseline.CommitSnapshotOnly(candidateRaw); snapErr != nil {
			result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedBaselineError, Message: "baseline snapshot could not be written after adoption"})
		}
		c.ManagedBaseline.MarkDesiredAhead()
		result.AppOutcome = "owned_not_serving"
		result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedStagingIncomplete, Message: "planned-restart marker could not be written after adoption"})
		result.Message = "External configuration adopted, but no restart could be staged; a restart is not scheduled."
		return nil
	}

	// Step 5.
	if err := c.PlannedRestart.PromoteToStagedVerified(candidateRaw); err != nil {
		if c.PlannedRestart.State().Inconsistent || errors.Is(err, ErrStagedCandidateChanged) {
			// §11.2.4.1: a pre- or post-promotion mismatch. The marker may
			// still be "prepared" or may already be "staged" — clean up
			// either way; artifacts naming bytes the file may no longer hold
			// are never trusted, only removed.
			cleanupErr := c.PlannedRestart.AbortPrepared(nil)
			if errors.Is(cleanupErr, ErrNoManagedPreparedMarker) {
				cleanupErr = c.PlannedRestart.ClearStagingArtifacts()
			}
			if cleanupErr != nil {
				c.ManagedBaseline.MarkInconsistent(ReasonCleanupIncomplete)
				result.OK = false
				result.Message = "The external file changed while staging the adoption, and cleanup of the abandoned stage failed; managed writes are refused until resolved."
				return fmt.Errorf("%w: clear staged artifacts after adoption mismatch: %v", admin.ErrConfigStorageUnavailable, cleanupErr)
			}
			if snapErr := c.ManagedBaseline.CommitSnapshotOnly(candidateRaw); snapErr != nil {
				result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedBaselineError, Message: "baseline snapshot could not be written after adoption"})
			}
			result.AppOutcome = "owned_not_serving"
			result.Degraded = append(result.Degraded,
				DegradedEntry{Kind: DegradedStagingIncomplete, Message: "the external file changed while staging the adoption"},
				DegradedEntry{Kind: DegradedDriftAfterAdopt, Message: "the file no longer matches the adopted candidate"},
			)
			result.Message = "External configuration adopted, but the file changed while staging; no restart is scheduled."
			return nil
		}
		// Row 5: an ordinary I/O failure transitioning "prepared" -> "staged"
		// itself (not a verified mismatch). The marker is still durably
		// "prepared" on disk, naming a candidate the file already matches —
		// exactly the condition Reconcile resolves at every startup. Run it
		// now, so the pending-restart status this call reports back is
		// accurate, rather than waiting for a restart to notice.
		if rerr := c.PlannedRestart.Reconcile(); rerr != nil {
			c.ManagedBaseline.MarkInconsistent(ReasonCleanupIncomplete)
			result.OK = false
			result.Message = "The baseline committed, but the staged restart could not be verified or repaired; managed writes are refused until resolved."
			return fmt.Errorf("%w: reconcile planned restart after promotion failure: %v", admin.ErrConfigStorageUnavailable, rerr)
		}
		result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedStagingError, Message: "planned-restart promotion did not complete synchronously; the stage still converges at the next restart"})
	}

	// Step 6. A failure here degrades provenance without unmaking the real,
	// durable stage — the marker already commits to the adopted bytes and the
	// configuration file can repair it (§11.2.1b).
	if err := c.ManagedBaseline.CommitSnapshotOnly(candidateRaw); err != nil {
		result.Degraded = append(result.Degraded, DegradedEntry{Kind: DegradedBaselineError, Message: "baseline snapshot could not be written after staging"})
	}
	return nil
}
