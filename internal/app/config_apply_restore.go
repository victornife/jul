// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"time"

	"jul/internal/atomicfile"
	"jul/internal/config"
	"jul/internal/server"
)

// timedOutResult builds the pre-persistence reload_timeout outcome for AC-08.
// Nothing was written to disk, so the result carries no persistence or
// restoration state; the admin API maps a non-empty TimedOutPhase to 504 with
// timed_out_phase. The message names the phase so the operator knows which slow
// path (secret resolution, handler build, bind probe, startup-resource
// validation) to investigate or whether to raise reload_timeout.
func (c *ConfigApplyCoordinator) timedOutResult(mode ApplyMode, phase string) ApplyResult {
	return ApplyResult{
		OK:            false,
		Mode:          mode,
		TimedOutPhase: phase,
		Message:       "The configuration apply exceeded reload_timeout during the " + phase + " phase; nothing was changed. Investigate the slow path or raise reload_timeout.",
	}
}

func (c *ConfigApplyCoordinator) provisionalResult(id string, mode ApplyMode, persistedVersion, desiredVersion string, startedAt time.Time, timedOut bool, message string) ApplyResult {
	servingVersion := server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig)
	return ApplyResult{
		ApplyID:          id,
		OK:               true,
		Mode:             mode,
		Version:          persistedVersion,
		PersistedVersion: persistedVersion,
		DesiredVersion:   desiredVersion,
		ServingVersion:   servingVersion,
		Persisted:        true,
		Reload: &server.ReloadResult{
			ID:             id,
			Source:         server.ReloadSourceAdmin,
			Outcome:        server.ReloadSavedNotLive,
			Persisted:      true,
			TimedOut:       timedOut,
			DesiredVersion: desiredVersion,
			ServingVersion: servingVersion,
			StartedAt:      startedAt,
		},
		Message: message,
	}
}

// logRestorationFailure makes a saved_not_live restoration write failure
// explicit (WS06 §7.6). The synchronous path uses withRestorationOutcome to
// surface the same error when it is still waiting; this method covers the
// saved_not_live path where no synchronous waiter remains, routing the failure
// to the composition root's structured log and finalization-error metric.
func (c *ConfigApplyCoordinator) logRestorationFailure(id string, err error) {
	if c.ReportManagedApplyError != nil {
		c.ReportManagedApplyError(id, "restoration", err)
	}
}

// withRestorationOutcome populates the restoration fields of an ApplyResult
// after the finalizer has completed. It reads the on-disk file and compares
// it to the expected candidate digest to determine whether restoration
// succeeded. When prevRaw is nil and the candidate file did not exist before,
// success means the file is now absent.
func (c *ConfigApplyCoordinator) withRestorationOutcome(res ApplyResult, prevRaw []byte, previouslyExisted bool, expectedCandidateDigest [32]byte) ApplyResult {
	res.Persisted = true
	if c.Path == "" {
		res.Restored = false
		return res
	}

	current, err := os.ReadFile(c.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// File absent. Restoration succeeded only if the candidate did not
			// exist before either.
			res.Restored = !previouslyExisted
			if !res.Restored {
				res.RestoreError = "candidate file missing after restoration window"
			}
			return res
		}
		res.Restored = false
		res.RestoreError = "cannot read disk after restoration: " + err.Error()
		return res
	}

	res.FinalDiskVersion = canonicalVersionFromRaw(current)
	currentDigest := sha256.Sum256(current)

	if currentDigest == expectedCandidateDigest {
		// Candidate is still on disk: restoration either failed or was skipped.
		res.Restored = false
		res.RestoreError = res.restoreOutcomeError(previouslyExisted)
		return res
	}

	// Disk no longer contains the candidate. If a previous file existed, verify
	// it matches prevRaw before declaring restoration successful.
	if previouslyExisted {
		if sha256.Sum256(current) == sha256.Sum256(prevRaw) {
			res.Restored = true
			return res
		}
		res.Restored = false
		res.RestoreError = "disk contents do not match previous configuration after restoration"
		return res
	}

	// No previous file existed and candidate is gone: restoration succeeded.
	res.Restored = true
	return res
}

// restoreOutcomeError returns a stable message when the candidate is still on
// disk after a failed pre-Publish reload. It differentiates the common cases
// so the operator knows whether the previous configuration was recoverable.
func (res ApplyResult) restoreOutcomeError(previouslyExisted bool) string {
	if res.RestoreError != "" {
		return res.RestoreError
	}
	if previouslyExisted {
		return "configuration was not restored to the previous version"
	}
	return "candidate file was not removed after failed apply"
}

// canonicalVersionFromRaw returns a short canonical version for raw config
// bytes, or "" when the bytes cannot be parsed/marshaled.
func canonicalVersionFromRaw(raw []byte) string {
	cfg, err := config.Parse(raw)
	if err != nil {
		return ""
	}
	return server.CanonicalVersion(cfg)
}

// buildTerminalResult constructs the final ApplyResult after the finalizer has
// finished any restoration. It is used both for the synchronous success path
// and for the async terminal outcome callback (H-05).
func (c *ConfigApplyCoordinator) buildTerminalResult(mode ApplyMode, persistedVersion, desiredVersion string, rr server.ReloadResult, prevRaw []byte, previouslyExisted bool, expectedCandidateDigest [32]byte) ApplyResult {
	res := c.decorateResultNoRestore(mode, persistedVersion, desiredVersion, rr)
	res.FinalServingVersion = rr.ServingVersion
	if res.FinalServingVersion == "" && c.LiveSnapshot != nil {
		res.FinalServingVersion = server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig)
	}
	if res.OK {
		res.Persisted = true
		res.FinalDiskVersion = persistedVersion
		if current, err := os.ReadFile(c.Path); err == nil {
			res.FinalDiskVersion = canonicalVersionFromRaw(current)
		}
	} else {
		res = c.withRestorationOutcome(res, prevRaw, previouslyExisted, expectedCandidateDigest)
	}
	return res
}

// decorateResultNoRestore builds the ApplyResult from a ReloadResult without
// calling restorePrevious — restoration is handled by the restore closure in
// applyCandidate to ensure exactly-once semantics.
func (c *ConfigApplyCoordinator) decorateResultNoRestore(mode ApplyMode, persistedVersion, desiredVersion string, rr server.ReloadResult) ApplyResult {
	res := ApplyResult{
		// M-05: ApplyID must be populated on every managed terminal result so
		// the OnManagedApplyComplete monotonic sequence guard records normal
		// applies (live, degraded, not-applied/restored, restoration-failed)
		// instead of dropping them as sequence-0. The server echoes the
		// request ID back into ReloadResult.ID.
		ApplyID:          rr.ID,
		OK:               reloadOutcomeSucceeded(rr.Outcome),
		Mode:             mode,
		Version:          persistedVersion,
		PersistedVersion: persistedVersion,
		DesiredVersion:   desiredVersion,
		ServingVersion:   rr.ServingVersion,
		Reload:           &rr,
	}
	switch rr.Outcome {
	case server.ReloadAppliedLive:
		res.Message = "Configuration validated, saved, and applied live."
	case server.ReloadAppliedDegraded:
		res.Message = "Configuration applied live with degradation: " + rr.Error
	case server.ReloadNoChange:
		res.Message = "Configuration validated and saved; it is already effective, so no runtime generation change was required."
	case server.ReloadSavedNotLive:
		res.Message = "Configuration saved; the live reload is still in flight. Check the runtime overview for the final outcome."
	default:
		res.Message = "Configuration was saved but the live reload did not apply: " + rr.Error
	}
	return res
}

func reloadOutcomeSucceeded(outcome server.ReloadOutcome) bool {
	return outcome == server.ReloadAppliedLive || outcome == server.ReloadAppliedDegraded || outcome == server.ReloadNoChange
}

// restorePrevious is the safe restoration entry point. It verifies the disk
// still contains the expected candidate digest and either restores the
// previous bytes or removes the candidate file when no previous file existed.
// It does not acquire the coordinator mutex; callers must ensure apply
// serialization (applyMu) and the digest check protects against overwriting a
// subsequent apply's candidate. It returns every filesystem error so callers
// can report truthful state.
func (c *ConfigApplyCoordinator) restorePrevious(prevRaw []byte, previouslyExisted bool, expectedCandidateDigest [32]byte) error {
	return c.restorePreviousLocked(prevRaw, previouslyExisted, expectedCandidateDigest)
}

// restorePreviousLocked performs the actual restore. The caller must ensure
// serialization so no other apply overwrites the candidate concurrently; the
// digest check provides an additional safety guard.
func (c *ConfigApplyCoordinator) restorePreviousLocked(prevRaw []byte, previouslyExisted bool, expectedCandidateDigest [32]byte) error {
	if c.Path == "" {
		return nil
	}

	current, err := os.ReadFile(c.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Candidate file is already gone; nothing to restore.
			return nil
		}
		return fmt.Errorf("read current config for restore: %w", err)
	}
	if sha256.Sum256(current) != expectedCandidateDigest {
		// The disk no longer contains the candidate we were rolling back.
		// A later apply has already superseded it; do not overwrite.
		return fmt.Errorf("current disk digest does not match expected candidate; skipping restore")
	}

	if previouslyExisted {
		if err := atomicfile.Write(c.Path, prevRaw, 0o600); err != nil {
			return fmt.Errorf("restore previous config: %w", err)
		}
		prevDigest := sha256.Sum256(prevRaw)
		if prevDigest != expectedCandidateDigest {
			c.suppressWatcher(prevDigest)
		}
	} else {
		if err := os.Remove(c.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove candidate config: %w", err)
		}
	}
	return nil
}
