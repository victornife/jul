// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

// refreshStateLocked calls the RefreshState hook if configured and then
// re-assesses managed-baseline drift. It must be called while applyMu is
// held. Errors are logged and returned so the caller can fail closed.
//
// ADR 0019 §11.2.3 layers the two state machines: planned restart reconciles
// first (via RefreshState), the baseline second, against what that leaves.
func (c *ConfigApplyCoordinator) refreshStateLocked() error {
	if c.RefreshState != nil {
		if err := c.RefreshState(); err != nil {
			return err
		}
	}
	c.assessManagedDrift()
	return nil
}

// assessManagedDrift re-reads the configuration file and updates the managed
// baseline's drift assessment. It implements the "before every managed
// write" trigger of ADR 0019 §12's four event-driven assessment points; the
// same helper backs the explicit AssessDriftNow entry used by the file
// watcher and SIGHUP. It is a no-op outside managed authority or when no
// baseline store is wired.
func (c *ConfigApplyCoordinator) assessManagedDrift() {
	if c.Authority != AuthorityManaged || c.ManagedBaseline == nil {
		return
	}
	raw, err := c.readConfigRaw()
	var version, parseErr string
	if err == nil {
		if cfg, perr := config.Parse(raw); perr == nil {
			version = server.CanonicalVersion(cfg)
		} else {
			parseErr = perr.Error()
		}
	}
	c.ManagedBaseline.AssessDrift(raw, err, version, parseErr)
}

// AssessDriftNow acquires applyMu and re-assesses managed-baseline drift. It
// is the entry point the file watcher and SIGHUP call in managed mode (ADR
// 0019 §11 point 4/5, §12): both become drift detectors and never enqueue a
// reload themselves.
//
// While a managed transaction still owns the config-path mutation gate
// (inFlightState == waiting), the file on disk may already be this
// transaction's own in-flight candidate compared against a baseline that
// has not been updated to match it yet — the watcher's own echo of Jul's
// write would misreport that as drift, and managed mode's driftOnlyFileConsumer
// deliberately does not suppress it by digest (a no-op transaction's echo
// is otherwise indistinguishable from a real external write). Skip the
// assessment in that window instead: the transaction's own mandatory
// terminal reassessment (completeWriteAndReassessDrift /
// rewindWriteAndReassessDrift) is the authoritative check once the baseline
// is current, so nothing is lost by deferring to it here.
func (c *ConfigApplyCoordinator) AssessDriftNow() {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.mu.Lock()
	inFlight := c.inFlightState == ApplyInFlightWaiting
	if inFlight {
		// Remember that an assessment was deferred, so drainPendingDriftAssessment
		// re-checks once the gate clears rather than losing this event entirely —
		// a real external write can still land after the transaction's own
		// terminal reassessment but before inFlightState clears.
		c.driftAssessmentPending = true
	}
	c.mu.Unlock()
	if inFlight {
		return
	}
	c.assessManagedDrift()
}

// drainPendingDriftAssessment re-runs the drift assessment once more if
// AssessDriftNow deferred one while this transaction owned inFlightState.
// Called immediately after every point that clears inFlightState, outside
// mu (assessManagedDrift needs only ManagedBaselineStore's own locking), so
// a watcher/SIGHUP event that arrived during the transaction's window is not
// silently dropped once the gate reopens.
func (c *ConfigApplyCoordinator) drainPendingDriftAssessment() {
	c.mu.Lock()
	pending := c.driftAssessmentPending
	c.driftAssessmentPending = false
	c.mu.Unlock()
	if pending {
		c.assessManagedDrift()
	}
}

// managedBaselineBlockMessage reports whether a managed write must be
// refused because ownership is not yet established, drift is unresolved, or
// the baseline is inconsistent (ADR 0019 §10/§11 point 7). It must be called
// after refreshStateLocked so the assessment is current.
func (c *ConfigApplyCoordinator) managedBaselineBlockMessage() (string, bool) {
	if c.Authority != AuthorityManaged || c.ManagedBaseline == nil {
		return "", false
	}
	switch st := c.ManagedBaseline.Status(); st.State {
	case ConfigStateManagedUnadopted:
		return "Managed configuration has not been adopted yet; adopt the current file before applying changes.", true
	case ConfigStateManagedDrift:
		return "Configuration on disk has drifted from the managed baseline; adopt the external file or restore it before applying changes.", true
	case ConfigStateManagedInconsistent:
		return fmt.Sprintf("Managed baseline state is inconsistent (%s); resolve it before applying changes.", st.Reason), true
	default:
		return "", false
	}
}

// currentConfigState computes the single authoritative ADR 0019 §16
// config_state enum for the coordinator's current authority. It is the one
// place this value is computed; every surface (status, apply/adopt results,
// the CLI --json object) reads it from here rather than re-deriving it —
// otherwise a file-owned process could leak a managed_* value from the
// ManagedBaselineStore that is constructed regardless of authority (purely
// so a file_owned startup can find and clean up artifacts a prior managed
// epoch left behind), and a managed process with a durable staged restart
// would report managed_clean, because the baseline itself already advanced
// to the staged candidate and is not drift (ADR 0019 §11.2.3).
//
// managed_drift and managed_inconsistent always win over a durable staged
// restart, never the reverse: both name a condition §12/§11.2.1 requires to
// be reported and alertable, and a planned-restart marker being present says
// nothing about whether the file has since drifted out from under it — an
// external writer does not consult PlannedRestart before editing the file.
// Masking that behind managed_pending_restart would hide exactly the
// condition an operator most needs to see. Only once neither applies does a
// durable staged restart take priority over the baseline's other states
// (managed_clean, managed_desired_ahead, managed_unadopted): §16's table
// defines managed_pending_restart for "a staged restart is durable"
// regardless of what the baseline separately shows in those cases.
func (c *ConfigApplyCoordinator) currentConfigState() (ConfigState, ManagedInconsistentReason) {
	if c.Authority == AuthorityFileOwned {
		return c.fileOwnedConfigState(), ""
	}
	if c.ManagedBaseline == nil {
		return "", ""
	}
	bst := c.ManagedBaseline.Status()
	if bst.State == ConfigStateManagedDrift || bst.State == ConfigStateManagedInconsistent {
		return bst.State, bst.Reason
	}
	if c.PlannedRestart != nil && c.PlannedRestart.IsPending() {
		return ConfigStateManagedPendingRestart, ""
	}
	return bst.State, bst.Reason
}

// fileOwnedConfigState computes the file_owned half of ADR 0019 §16's state
// model, entirely independent of any ManagedBaselineStore artifacts a prior
// managed epoch may have left behind. file_owned_desired_ahead reuses the
// same external-divergence signal PendingRestartCheck already maintains for
// a restart-required edit that is not yet live; file_owned_invalid is a
// current file that fails validation — parsing is necessary but not
// sufficient, since config.Parse performs no semantic validation of its own
// and a syntactically well-formed file can still fail config.Validate (e.g.
// an unresolvable upstream reference) — file_owned_clean is everything else,
// including a process with no configuration file at all (ADR 0019 §9.1.1).
func (c *ConfigApplyCoordinator) fileOwnedConfigState() ConfigState {
	if c.PlannedRestart != nil {
		if st := c.PlannedRestart.State(); st.State == PlannedRestartStateExternalDivergence {
			return ConfigStateFileOwnedDesiredAhead
		}
	}
	if c.Path == "" {
		return ConfigStateFileOwnedClean
	}
	raw, err := c.readConfigRaw()
	if err != nil {
		// A read failure says nothing about the content's validity; report
		// the least-alarming state rather than asserting invalidity we did
		// not observe.
		return ConfigStateFileOwnedClean
	}
	cfg, perr := config.Parse(raw)
	if perr != nil {
		return ConfigStateFileOwnedInvalid
	}
	if verr := config.Validate(cfg); verr != nil {
		return ConfigStateFileOwnedInvalid
	}
	return ConfigStateFileOwnedClean
}

// completeWriteAndReassessDrift wraps ManagedBaselineStore.CompleteWrite with
// an immediate re-read of the actual disk file. CompleteWrite assumes disk
// holds exactly committedRaw — the bytes this transaction wrote — but an
// external writer does not take applyMu and can land between the write and
// this call, or (on the async hot-apply path, where applyMu is released once
// the candidate is persisted and the reload is enqueued) any time during the
// whole reload wait. Re-assessing immediately afterward, from the CURRENT
// file content rather than the assumption CompleteWrite bakes in, is what
// lets such a race surface as managed_drift(committedRaw, disk) instead of
// being silently overwritten by CompleteWrite's own optimistic
// managed_clean — an external write racing the transaction is exactly what
// ADR 0019 §12's drift detection exists to catch, not something admission
// alone can prevent, since it never gated external writers in the first
// place.
func (c *ConfigApplyCoordinator) completeWriteAndReassessDrift(committedRaw []byte, canonicalVersion string) error {
	if err := c.ManagedBaseline.CompleteWrite(committedRaw, canonicalVersion); err != nil {
		return err
	}
	c.assessManagedDrift()
	return nil
}

// rewindOrMarkInconsistent resolves the managed-baseline transaction when a
// managed write's reload failed and restoration was attempted (T-write's
// restored arm, ADR 0019 §11.2). When restoration succeeded it rewinds the
// baseline to the prior bytes; when restoration failed the on-disk state is
// uncertain, so the baseline is marked inconsistent immediately rather than
// left clean on the strength of an intention (§11.2.1a). The caller must
// pass a verified restored outcome (a fresh disk read compared against the
// prior bytes), not merely "the restoration write itself did not error" —
// the latter is not evidence the file still holds those bytes right now.
func (c *ConfigApplyCoordinator) rewindOrMarkInconsistent(restored bool) *DegradedEntry {
	if restored {
		if err := c.rewindWriteAndReassessDrift(); err != nil {
			return &DegradedEntry{Kind: DegradedBaselineError, Message: "baseline could not be rewound after restoration"}
		}
		return nil
	}
	c.ManagedBaseline.MarkInconsistent(ReasonRestorationFailed)
	return &DegradedEntry{Kind: DegradedBaselineError, Message: "baseline left inconsistent after a failed restoration"}
}

// rewindWriteAndReassessDrift mirrors completeWriteAndReassessDrift for the
// restoration arm: RewindWrite's own bookkeeping assumes disk holds exactly
// the restored prior bytes, but neither applyMu nor c.mu holds off an
// external writer, which can land between the restoration write and this
// call. Re-reading immediately afterward, from the CURRENT file content
// rather than the assumption RewindWrite bakes in, lets such a race surface
// as drift/inconsistency instead of being silently overwritten by
// RewindWrite's own optimistic managed_clean.
func (c *ConfigApplyCoordinator) rewindWriteAndReassessDrift() error {
	if err := c.ManagedBaseline.RewindWrite(); err != nil {
		return err
	}
	c.assessManagedDrift()
	return nil
}

// retryBaselineWriteLocked performs ADR 0019 §11.2.1a's single required
// retry after a post-commit baseline write failure, for callers that already
// hold applyMu for their own transaction (T-mark's adoption commit and
// T-write's stage_restart commit both do — unlike the hot-apply finalizer,
// neither detaches into another goroutine before returning). Running the
// retry inline, under the same applyMu hold, keeps it inside the same
// admission-gate-held critical section §11.2.0.1 requires: no later
// transaction can be admitted, contend with this retry over the same digest,
// or observe a window where the retry's own eventual outcome is still
// pending.
//
// It re-reads the configuration and verifies it still matches the digest
// committedRaw intends to record — the exact check the ADR requires, because
// a retry that skipped it could record a digest a restoration had already
// superseded. A mismatch, a read failure, or a failed retry all resolve the
// same way: managed_inconsistent, reason baseline_unwritable. Recovery never
// resolves managed_clean on the strength of an intention — only the retry
// write's own success does that, via commit's normal path.
func (c *ConfigApplyCoordinator) retryBaselineWriteLocked(committedRaw []byte, commit func([]byte) error) {
	if c.ManagedBaseline == nil {
		return
	}
	current, err := c.readConfigRaw()
	if err != nil || sha256Hex(current) != sha256Hex(committedRaw) {
		c.ManagedBaseline.MarkInconsistent(ReasonBaselineUnwritable)
		return
	}
	if err := commit(committedRaw); err != nil {
		c.ManagedBaseline.MarkInconsistent(ReasonBaselineUnwritable)
	}
}

// resolveBaselineWriteRetry is retryBaselineWriteLocked's counterpart for
// the one call site that cannot run it inline: the hot-apply finalizer's
// initial CompleteWrite failure happens in its own goroutine, and by that
// point the ApplyRaw call that spawned it may already have returned on a
// timeout and released applyMu — so the retry cannot assume applyMu is
// already held, and re-entering it there would deadlock if it were.
//
// ADR 0019 §11.2.0.1 requires the admission gate to stay closed until this
// retry reaches its terminal state, exactly like the initial write. The
// caller enforces that: it must call this only after the finalizer's own
// terminal-result delivery (so a synchronously-waiting ApplyRaw call has
// already returned and dropped applyMu, rather than deadlocking against
// it), and it must not clear inFlightState until this call returns — that
// is what keeps a later hot apply from being admitted while this retry is
// still pending, closing the exact race an earlier version of this fix
// left open (a stale retry losing the race for applyMu to a later,
// fully-successful apply and wrongly regressing it to
// managed_inconsistent).
//
// It takes applyMu itself, re-reads the configuration, and verifies it
// still matches the digest committedRaw intends to record. A mismatch is
// not automatically this retry's failure to detect: while inFlightState
// blocks a later ordinary apply, it does not gate adoption or
// stage_restart, so the mismatch may be a later, independent transaction
// that already established its own valid baseline for whatever the file
// now holds — in which case the system is already consistent, and
// recording managed_inconsistent would wrongly regress that newer
// successful commit. Abandoning silently when the current baseline
// already names the current file's digest closes that hole. Any other
// mismatch, a read failure, or a failed retry all resolve the same way:
// managed_inconsistent, reason baseline_unwritable.
func (c *ConfigApplyCoordinator) resolveBaselineWriteRetry(committedRaw []byte, commit func([]byte) error) {
	if c.ManagedBaseline == nil {
		return
	}
	if c.beforeBaselineWriteRetry != nil {
		c.beforeBaselineWriteRetry()
	}
	intended := sha256Hex(committedRaw)
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	raw, err := c.readConfigRaw()
	var currentDigest string
	if err == nil {
		currentDigest = sha256Hex(raw)
	}
	switch {
	case err != nil:
		c.ManagedBaseline.MarkInconsistent(ReasonBaselineUnwritable)
	case currentDigest != intended:
		if bst := c.ManagedBaseline.Status(); bst.BaselineRawSHA256 != currentDigest {
			c.ManagedBaseline.MarkInconsistent(ReasonBaselineUnwritable)
		}
	default:
		if err := commit(committedRaw); err != nil {
			c.ManagedBaseline.MarkInconsistent(ReasonBaselineUnwritable)
		}
	}
	if c.afterBaselineWriteRetry != nil {
		c.afterBaselineWriteRetry()
	}
}

// loadMutationBaseline uses the HTTP handler's exact authorized snapshot when
// supplied. Context-free compatibility callers fall back to one coordinator
// read, but read errors other than absence always fail closed.
func (c *ConfigApplyCoordinator) loadMutationBaseline(hint *admin.MutationBaseline) (admin.MutationBaseline, error) {
	if hint != nil {
		baseline := *hint
		baseline.Raw = append([]byte(nil), hint.Raw...)
		return baseline, nil
	}
	if c.Path == "" {
		return admin.MutationBaseline{}, nil
	}
	raw, err := c.readConfigRaw()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return admin.MutationBaseline{}, nil
		}
		return admin.MutationBaseline{}, fmt.Errorf("%w: read persisted config: %v", admin.ErrConfigStorageUnavailable, err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return admin.MutationBaseline{}, fmt.Errorf("%w: parse persisted config: %v", admin.ErrConfigStorageUnavailable, err)
	}
	return admin.MutationBaseline{
		Raw:     raw,
		Digest:  sha256.Sum256(raw),
		Version: server.CanonicalVersion(cfg),
		Config:  cfg,
		Exists:  true,
	}, nil
}

// verifyBaselineLocked compares the current exact bytes with the snapshot used
// for concurrency, authorization, reachability, history, and diffing. It
// returns the current canonical raw version for a typed conflict response.
func (c *ConfigApplyCoordinator) verifyBaselineLocked(baseline admin.MutationBaseline) (changed bool, currentVersion string, err error) {
	if c.Path == "" {
		return false, "", nil
	}
	current, err := c.readConfigRaw()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return baseline.Exists, "", nil
		}
		return false, "", fmt.Errorf("%w: verify persisted config: %v", admin.ErrConfigStorageUnavailable, err)
	}
	currentVersion = canonicalVersionFromRaw(current)
	if !baseline.Exists {
		return true, currentVersion, nil
	}
	return sha256.Sum256(current) != baseline.Digest, currentVersion, nil
}

func (c *ConfigApplyCoordinator) readConfigRaw() ([]byte, error) {
	if c.ReadConfigRaw != nil {
		return c.ReadConfigRaw()
	}
	return os.ReadFile(c.Path)
}

func (c *ConfigApplyCoordinator) conflictResult(mode ApplyMode, persistedVersion, desiredVersion, currentVersion string) ApplyResult {
	return ApplyResult{
		OK:               false,
		Mode:             mode,
		Version:          persistedVersion,
		PersistedVersion: persistedVersion,
		DesiredVersion:   desiredVersion,
		Conflict:         true,
		CurrentVersion:   currentVersion,
		Message:          "The configuration file changed on disk since this edit was prepared; reload and try again.",
	}
}
