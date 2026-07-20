// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/admin"
	"jul/internal/atomicfile"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

// ApplyMode selects how a managed configuration apply should behave.
type ApplyMode string

const (
	// ApplyHot applies the configuration live through a hot reload.
	ApplyHot ApplyMode = "hot"
	// ApplyStageRestart validates and persists the candidate for the next
	// process restart without changing the live runtime. The full staging,
	// discard, and reconciliation workflow is implemented in later issues.
	ApplyStageRestart ApplyMode = "stage_restart"
)

// ApplyResult is the structured outcome of a managed configuration apply
// produced by ConfigApplyCoordinator. It carries the persisted and serving
// truth without any UI, audit, or history rendering policy.
type ApplyResult struct {
	OK               bool
	Mode             ApplyMode
	Version          string
	ServingVersion   string
	Reload           *server.ReloadResult
	PendingRestart   *admin.PendingRestartStatus
	Message          string
	ValidationErrors []string
	RestartRequired  bool
	CanStage         bool

	// Restoration fields (F-03): first-class truth about whether a rejected
	// candidate was rolled back to the previous configuration.
	Persisted         bool   // true if the candidate bytes were written to disk
	Restored          bool   // true if the previous configuration was restored
	RestoreError      string // non-empty if restoration was attempted and failed
	FinalDiskVersion  string // canonical version of the on-disk file after apply
	FinalServingVersion string // canonical version of the live serving config (may lag)
}

// ApplyInFlightState tracks the current managed apply transaction. It is used
// to prevent a second managed apply from starting while a previous apply's
// async finalizer still owns disk restoration, and to make the finalizer's
// digest check + restore atomic with respect to new writes.
type ApplyInFlightState string

const (
	// ApplyInFlightNone means no managed apply transaction is currently active.
	ApplyInFlightNone ApplyInFlightState = ""
	// ApplyInFlightWaiting means a candidate has been persisted and the
	// coordinator is either waiting synchronously for the reload result or the
	// finalizer owns the asynchronous completion/restoration.
	ApplyInFlightWaiting ApplyInFlightState = "waiting"
)

// ConfigApplyCoordinator owns every managed configuration write: it serializes
// applies, keeps the exact previous raw bytes, runs preflight, persists
// atomically, suppresses watcher echoes, submits correlated reloads, waits for
// results, and restores the previous bytes when a pre-Publish failure occurs.
type ConfigApplyCoordinator struct {
	BaseCtx        context.Context
	Path           string
	Preflight      *Preflight
	SubmitReload   func(server.ReloadRequest) error
	LiveSnapshot   func() server.LiveSnapshot
	WatchDigest    *atomic.Pointer[[32]byte]
	PlannedRestart *PlannedRestartStore

	mu      sync.Mutex
	applyMu sync.Mutex
	seq     atomic.Uint64

	// inFlightState tracks whether a managed apply transaction is still
	// outstanding. It is protected by applyMu for state transitions.
	inFlightState ApplyInFlightState
}

// ApplyRaw applies a raw configuration bytes slice. It is the hot-apply entry
// point for the admin /api/config/apply path.
func (c *ConfigApplyCoordinator) ApplyRaw(data []byte, mode ApplyMode) (ApplyResult, error) {
	// applyMu serializes applies so only one candidate is in flight at a time.
	// c.mu protects coordinator state and is not held across the reload wait so
	// the async finalizer can safely restore without deadlocking.
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	if mode == "" {
		mode = ApplyHot
	}

	// Refuse to start a new managed transaction while the previous one's
	// finalizer still owns restoration. This keeps the disk serialization
	// contract even after the synchronous HTTP path returns saved_not_live.
	c.mu.Lock()
	inFlight := c.inFlightState == ApplyInFlightWaiting
	c.mu.Unlock()
	if inFlight {
		return ApplyResult{
			OK:      false,
			Mode:    mode,
			Version: server.CanonicalVersion(nil),
			Message: "A previous apply is still in flight; wait for it to complete or check the runtime overview for status.",
		}, nil
	}

	prevRaw, prevErr := os.ReadFile(c.Path)
	previouslyExisted := !errors.Is(prevErr, os.ErrNotExist)

	// Block hot apply on any blocking planned-restart state: managed pending,
	// external unmanaged divergence, or post-reconciliation inconsistency.
	if mode == ApplyHot && c.PlannedRestart != nil {
		st := c.PlannedRestart.State()
		if st.State != PlannedRestartStateNone {
			msg := "A planned restart is pending; discard or complete it before applying hot changes."
			switch st.State {
			case PlannedRestartStateExternalDivergence:
				msg = "Configuration on disk differs from the running runtime; resolve the external divergence before applying hot changes."
			case PlannedRestartStateInconsistent:
				msg = "Planned-restart state is inconsistent; resolve the inconsistency before applying hot changes."
			}
			return ApplyResult{
				OK:             false,
				Mode:           mode,
				Message:        msg,
				PendingRestart: c.plannedRestartStatus(),
			}, nil
		}
	}

	// Block a new stage_restart while any blocking planned-restart state is
	// present: an already-pending managed staged restart, external unmanaged
	// divergence, or a post-reconciliation inconsistency. This enforces the
	// single-candidate invariant (F-08) and prevents staging from silently
	// adopting an externally-owned divergence (F-04).
	if mode == ApplyStageRestart && c.PlannedRestart != nil {
		st := c.PlannedRestart.State()
		if st.State != PlannedRestartStateNone {
			msg := "A staged restart is already pending; discard or complete it before staging a new candidate."
			switch st.State {
			case PlannedRestartStateExternalDivergence:
				msg = "Cannot stage a restart while external disk/runtime divergence is present."
			case PlannedRestartStateInconsistent:
				msg = "Cannot stage a restart while planned-restart state is inconsistent."
			}
			return ApplyResult{
				OK:             false,
				Mode:           mode,
				Message:        msg,
				PendingRestart: c.plannedRestartStatus(),
			}, nil
		}
	}

	cfg, err := config.Parse(data)
	if err != nil {
		return ApplyResult{
			OK:               false,
			Mode:             mode,
			Message:          "The configuration could not be parsed.",
			ValidationErrors: []string{err.Error()},
		}, nil
	}

	// Parse and resolve the previous config so lifecycle.DiffConfig compares
	// effective values on both sides. Without resolution, secret references
	// produce false differences when the resolved value happens to match but
	// the reference string differs (M-02 fix).
	var prevCfg *config.Config
	if len(prevRaw) > 0 {
		if raw, err := config.Parse(prevRaw); err == nil {
			if cand, err := config.NewCandidate(raw); err == nil {
				prevCfg = cand.Effective
			} else {
				prevCfg = raw // fallback to unresolved on resolution error
			}
		}
	}

	if mode == ApplyStageRestart {
		return c.applyStageRestart(cfg, prevCfg, data, prevRaw)
	}

	pfResult, err := c.Preflight.Apply(c.BaseCtx, cfg, prevCfg, PreflightHot)
	if err != nil {
		result := ApplyResult{
			OK:      false,
			Mode:    mode,
			Message: "The configuration contains errors; no change was applied.",
		}
		if errors.Is(err, admin.ErrRestartRequired) {
			result.RestartRequired = true
			result.CanStage = true
			result.Message = err.Error()
			return result, nil
		}
		result.ValidationErrors = []string{err.Error()}
		return result, nil
	}

	return c.applyCandidate(data, pfResult.Candidate, prevRaw, previouslyExisted, mode)
}

// ApplyConfig applies a parsed configuration. It marshals the config and
// delegates to ApplyRaw so the same preflight, persistence, and restoration
// path is used for structured edits.
func (c *ConfigApplyCoordinator) ApplyConfig(cfg *config.Config, mode ApplyMode) (ApplyResult, error) {
	data, err := config.Marshal(cfg)
	if err != nil {
		return ApplyResult{
			OK:      false,
			Mode:    mode,
			Message: "Failed to marshal configuration.",
		}, err
	}
	return c.ApplyRaw(data, mode)
}

// DiscardPlannedRestart clears any staged planned restart. When the store is
// file-backed it performs the safety-verified discard from §17.5: it checks
// marker consistency, disk digest, and live serving version before restoring
// the backup. On success the watcher echo of the restoration is suppressed.
func (c *ConfigApplyCoordinator) DiscardPlannedRestart() (ApplyResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.PlannedRestart == nil || !c.PlannedRestart.IsPending() {
		return ApplyResult{
			OK:      true,
			Mode:    ApplyHot,
			Message: "No planned restart was pending.",
		}, nil
	}

	// File-backed safe discard.
	if c.PlannedRestart.ConfigPath != "" {
		var liveVersion string
		if c.LiveSnapshot != nil {
			liveVersion = server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig)
		}
		restoredBytes, err := c.PlannedRestart.DiscardSafe(liveVersion)
		if err != nil {
			return ApplyResult{
				OK:      false,
				Mode:    ApplyHot,
				Message: "Discard failed: " + err.Error(),
			}, err
		}
		// Suppress the watcher echo of the restoration write.
		restoreDigest := sha256.Sum256(restoredBytes)
		c.suppressWatcher(restoreDigest)
		return ApplyResult{
			OK:      true,
			Mode:    ApplyHot,
			Message: "Planned restart discarded and previous configuration restored.",
		}, nil
	}

	// In-memory discard (tests / no config path).
	if _, ok := c.PlannedRestart.Discard(); !ok {
		return ApplyResult{
			OK:      true,
			Mode:    ApplyHot,
			Message: "No planned restart was pending.",
		}, nil
	}
	return ApplyResult{
		OK:      true,
		Mode:    ApplyHot,
		Message: "Planned restart discarded.",
	}, nil
}

func (c *ConfigApplyCoordinator) nextID() string {
	return fmt.Sprintf("rl_%d", c.seq.Add(1))
}

// PlannedRestartStatus returns the current managed planned-restart status as
// an admin.PendingRestartStatus, or nil when no staged restart is pending.
// It is safe to call without holding the coordinator mutex because it delegates
// to the PlannedRestartStore which guards its own state.
func (c *ConfigApplyCoordinator) PlannedRestartStatus() *admin.PendingRestartStatus {
	return c.plannedRestartStatus()
}

func (c *ConfigApplyCoordinator) suppressWatcher(digest [32]byte) {
	if c.WatchDigest != nil {
		c.WatchDigest.Store(&digest)
	}
}

// applyStageRestart runs the stage_restart path: validates via stage preflight,
// writes the sidecar backup+marker, then writes the candidate atomically,
// and returns an ApplyResult without submitting a live reload.
//
// Only one staged candidate may be in flight at a time (F-08); ApplyRaw blocks
// subsequent stage_restart requests until the pending candidate is discarded
// or the process restarts.
//
// Correct crash-consistent ordering (C-01 fix):
//  1. prevRaw captured in ApplyRaw before any write (passed in).
//  2. StageManaged writes .bak (prevRaw) and prepared marker — no candidate on disk yet.
//  3. atomicfile.Write writes the candidate.
//  4. PromoteToStaged promotes the marker to "staged" only after the candidate write succeeds.
func (c *ConfigApplyCoordinator) applyStageRestart(cfg, prevCfg *config.Config, data, prevRaw []byte) (ApplyResult, error) {
	candidate, err := config.NewCandidate(cfg)
	if err != nil {
		return ApplyResult{
			OK:               false,
			Mode:             ApplyStageRestart,
			Message:          "The configuration contains errors; no change was staged.",
			ValidationErrors: []string{err.Error()},
		}, nil
	}
	if err := ValidateRuntimeConfig(candidate.Effective); err != nil {
		return ApplyResult{
			OK:               false,
			Mode:             ApplyStageRestart,
			Message:          "The configuration contains errors; no change was staged.",
			ValidationErrors: []string{err.Error()},
		}, nil
	}

	desiredVersion := server.CanonicalVersion(candidate.Effective)
	liveVersion := ""
	if c.LiveSnapshot != nil {
		liveVersion = server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig)
	}

	// The single-candidate invariant is enforced in ApplyRaw before this path is
	// reached, so there is never an update to an already-pending stage here.
	// The diff is computed against the live/pre-stage config (prevCfg).

	// Run the shared preflight gates in stage-restart mode so restart-required
	// classification is retained rather than rejected.
	pfResult, err := c.Preflight.Apply(c.BaseCtx, cfg, prevCfg, PreflightStageRestart)
	if err != nil {
		return ApplyResult{
			OK:               false,
			Mode:             ApplyStageRestart,
			Message:          "The configuration contains errors; no change was staged.",
			ValidationErrors: []string{err.Error()},
		}, nil
	}

	// Collect subsystem names from the lifecycle diff.
	subsystems := subsystemNames(pfResult.Lifecycle)

	// A stage_restart should only be accepted when the candidate cannot be
	// fully hot-applied. If no restart-required changes exist, reject and tell
	// the operator to use hot apply instead.
	if len(subsystems) == 0 {
		return ApplyResult{
			OK:      false,
			Mode:    ApplyStageRestart,
			Version: desiredVersion,
			Message: "No restart-required changes detected; use a hot apply instead.",
		}, nil
	}

	// Build the marker. BaseCanonicalVersion and BaseServingVersion describe the
	// originally-serving config, not the candidate.
	marker := PlannedRestartMarker{
		BaseServingVersion:   liveVersion,
		BaseCanonicalVersion: liveVersion, // version of the live config, not the candidate
		StagedRawSHA256:      sha256Hex(data),
		StagedVersion:        desiredVersion,
		PendingSubsystems:    subsystems,
	}

	// Step 1+2: Write backup (prevRaw) and prepared marker BEFORE writing the
	// candidate to disk. StageManaged uses prevRaw for the .bak file so it
	// always contains the pre-stage original configuration, not the candidate.
	if c.PlannedRestart != nil {
		if err := c.PlannedRestart.StageManaged(prevRaw, data, marker); err != nil {
			// Sidecar write failed; nothing has changed on disk yet. Return error.
			return ApplyResult{
				OK:      false,
				Mode:    ApplyStageRestart,
				Version: desiredVersion,
				Message: "Failed to write planned-restart sidecar: " + err.Error(),
			}, err
		}
	}

	// Step 3: Now that backup+marker are safely on disk, write the candidate.
	rawDigest := sha256.Sum256(data)
	if err := atomicfile.Write(c.Path, data, 0o600); err != nil {
		// Candidate write failed. The prepared marker is on disk but the config
		// file is unchanged. Reconcile on the next startup will detect
		// prepared+disk==base and clean up automatically.
		return ApplyResult{
			OK:      false,
			Mode:    ApplyStageRestart,
			Version: desiredVersion,
			Message: "Failed to persist staged configuration; sidecar marker preserved for reconciliation on restart.",
		}, err
	}
	c.suppressWatcher(rawDigest)

	// Step 4: promote the marker to "staged" only after the candidate write
	// succeeds. A crash before this leaves marker="prepared" and disk==base,
	// which Reconcile cleans up automatically.
	if c.PlannedRestart != nil {
		if err := c.PlannedRestart.PromoteToStaged(data); err != nil {
			return ApplyResult{
				OK:      false,
				Mode:    ApplyStageRestart,
				Version: desiredVersion,
				Message: "Failed to promote staged marker after candidate write: " + err.Error(),
			}, err
		}
	}

	msg := "Configuration staged for the next process restart; the live runtime is unchanged."
	return ApplyResult{
		OK:             true,
		Mode:           ApplyStageRestart,
		Version:        desiredVersion,
		ServingVersion: liveVersion,
		PendingRestart: c.plannedRestartStatus(),
		Message:        msg,
	}, nil
}

// errIsStageRestartAlreadyPending reports whether err is the sentinel returned
// when a stage_restart is attempted while one is already pending.
func errIsStageRestartAlreadyPending(err error) bool {
	return errors.Is(err, ErrStageRestartAlreadyPending)
}

// subsystemNames extracts unique subsystem names from a lifecycle ChangeSet.
func subsystemNames(cs lifecycle.ChangeSet) []string {
	seen := make(map[string]struct{}, len(cs))
	var out []string
	for _, e := range cs {
		if e.Subsystem == "" {
			continue
		}
		if _, ok := seen[e.Subsystem]; !ok {
			seen[e.Subsystem] = struct{}{}
			out = append(out, e.Subsystem)
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
		ServingVersion:   st.ServingVersion,
	}
	if !st.StagedAt.IsZero() {
		res.StagedAt = st.StagedAt.UTC().Format(time.RFC3339)
	}
	return res
}

func (c *ConfigApplyCoordinator) applyCandidate(data []byte, candidate *config.Candidate, prevRaw []byte, previouslyExisted bool, mode ApplyMode) (ApplyResult, error) {
	desiredVersion := server.CanonicalVersion(candidate.Effective)
	rawDigest := sha256.Sum256(data)

	id := c.nextID()
	// resultCh has capacity 1 because the server sends exactly one terminal
	// result and the finalizer goroutine below is the sole receiver.
	resultCh := make(chan server.ReloadResult, 1)
	// waiterCh carries the result from the finalizer goroutine to the
	// synchronous HTTP path. It has capacity 1 so the finalizer never blocks
	// when forwarding the result.
	waiterCh := make(chan server.ReloadResult, 1)
	// restoreDone is closed by the finalizer after it has finished any
	// restoration, so applyCandidate can return the file to a known state.
	restoreDone := make(chan struct{})

	// Use the currently serving config's reload_timeout for this transaction,
	// not the candidate's. A candidate that changes reload_timeout affects the
	// next apply, never this one (R15-01).
	reloadTimeout := candidate.Effective.Global.ReloadTimeout
	if snap := c.LiveSnapshot(); snap.EffectiveConfig != nil && snap.EffectiveConfig.Global.ReloadTimeout > 0 {
		reloadTimeout = snap.EffectiveConfig.Global.ReloadTimeout
	}
	deadline := time.Now().Add(reloadTimeout.Std())

	// Serialize file writes and staged state with the coordinator mutex. It is
	// released before the reload wait so the async finalizer cannot deadlock
	// with the HTTP goroutine; applyMu still prevents concurrent applies.
	c.mu.Lock()
	if err := atomicfile.Write(c.Path, data, 0o600); err != nil {
		c.mu.Unlock()
		return ApplyResult{
			OK:      false,
			Mode:    mode,
			Version: desiredVersion,
			Message: "Failed to persist configuration.",
		}, err
	}

	// Mark a managed transaction as in-flight before releasing applyMu.
	// The finalizer clears this only after any restoration is complete.
	c.inFlightState = ApplyInFlightWaiting

	// Suppress the echo of our own write on the file watcher.
	c.suppressWatcher(rawDigest)

	req := server.ReloadRequest{
		ID:        id,
		Source:    server.ReloadSourceAdmin,
		Candidate: candidate,
		RawDigest: rawDigest,
		Deadline:  deadline,
		Result:    resultCh,
	}

	if err := c.SubmitReload(req); err != nil {
		// Enqueue failed: the candidate file is on disk but the runtime will
		// not reload. Restore the exact previous bytes and suppress the
		// restoration echo so the watcher does not loop.
		restoreErr := c.restorePreviousLocked(prevRaw, previouslyExisted, rawDigest)
		c.inFlightState = ApplyInFlightNone
		c.mu.Unlock()
		msg := "Reload enqueue failed; the configuration was saved but may not be applied."
		if restoreErr != nil {
			msg += " Restoration failed: " + restoreErr.Error()
		}
		return ApplyResult{
			OK:      false,
			Mode:    mode,
			Version: desiredVersion,
			Message: msg,
		}, err
	}
	c.mu.Unlock()

	// Finalizer goroutine: sole owner of the reload result. It forwards the
	// result to waiterCh for the synchronous HTTP path, then performs any
	// required restoration while holding c.mu so the digest check and restore
	// are atomic with respect to a subsequent apply's write. The state is
	// cleared under the same lock so the next apply observes the completed
	// transaction only after the disk is in its final state.
	go func() {
		defer close(restoreDone)
		select {
		case rr := <-resultCh:
			select {
			case waiterCh <- rr:
			default:
			}
			c.mu.Lock()
			restoreNeeded := !rr.Published && rr.Outcome != server.ReloadAppliedLive && rr.Outcome != server.ReloadAppliedDegraded
			if restoreNeeded {
				if err := c.restorePreviousLocked(prevRaw, previouslyExisted, rawDigest); err != nil {
					// Log the restoration failure; F-03 surfaces it through the
					// result/audit path below when the synchronous path is still
					// waiting. When it has already returned (saved_not_live), the
					// failure is discoverable via overview/metrics.
					c.logRestorationFailure(id, err)
				}
			}
			c.inFlightState = ApplyInFlightNone
			c.mu.Unlock()
		case <-c.BaseCtx.Done():
			// Process shutting down; no restoration attempt — startup will
			// determine the correct state from disk and marker files.
			c.mu.Lock()
			c.inFlightState = ApplyInFlightNone
			c.mu.Unlock()
		}
	}()

	waitTimeout := time.Until(deadline) + time.Second
	if waitTimeout <= 0 {
		waitTimeout = time.Second
	}

	select {
	case rr := <-waiterCh:
		res := c.decorateResultNoRestore(mode, desiredVersion, rr)
		if !res.OK {
			// Wait for the finalizer's restoration so callers observe a known
			// on-disk state before we return, then record the outcome.
			select {
			case <-restoreDone:
			case <-time.After(5 * time.Second):
			}
			res = c.withRestorationOutcome(res, prevRaw, previouslyExisted, rawDigest)
		} else {
			// Successful apply: record the final persisted/serving versions.
			res.Persisted = true
			res.FinalDiskVersion = desiredVersion
			res.FinalServingVersion = rr.ServingVersion
		}
		return res, nil
	case <-c.BaseCtx.Done():
		// The process is shutting down; the finalizer will clear inFlightState
		// once it observes BaseCtx cancellation.
		return ApplyResult{
			OK:             true,
			Mode:           mode,
			Version:        desiredVersion,
			ServingVersion: server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig),
			Message:        "Configuration saved; the process is shutting down and the reload outcome is unknown.",
		}, nil
	case <-time.After(waitTimeout):
		// Finalizer goroutine now owns the restoration obligation. The result
		// returned here marks Persisted because the candidate is on disk, but
		// the final restoration state will only be known after restoreDone.
		return ApplyResult{
			OK:             true,
			Mode:           mode,
			Version:        desiredVersion,
			ServingVersion: server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig),
			Persisted:      true,
			Reload: &server.ReloadResult{
				ID:        id,
				Source:    server.ReloadSourceAdmin,
				Outcome:   server.ReloadSavedNotLive,
				Persisted: true,
				TimedOut:  true,
				StartedAt: deadline.Add(-candidate.Effective.Global.ReloadTimeout.Std()),
			},
			Message: "Configuration saved; the live reload is still in flight. Check the runtime overview for the final outcome.",
		}, nil
	}
}

// logRestorationFailure records a restoration failure in a best-effort way.
// The synchronous path uses withRestorationOutcome to surface the same error
// when it is still waiting; this method covers the saved_not_live path.
func (c *ConfigApplyCoordinator) logRestorationFailure(id string, err error) {
	// Placeholder for future metric/structured-log emission; kept minimal to
	// avoid importing logging packages into the coordinator.
	_ = id
	_ = err
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

// decorateResultNoRestore builds the ApplyResult from a ReloadResult without
// calling restorePrevious — restoration is handled by the restore closure in
// applyCandidate to ensure exactly-once semantics.
func (c *ConfigApplyCoordinator) decorateResultNoRestore(mode ApplyMode, desiredVersion string, rr server.ReloadResult) ApplyResult {
	res := ApplyResult{
		OK:             rr.Outcome == server.ReloadAppliedLive || rr.Outcome == server.ReloadAppliedDegraded,
		Mode:           mode,
		Version:        desiredVersion,
		ServingVersion: rr.ServingVersion,
		Reload:         &rr,
	}
	switch rr.Outcome {
	case server.ReloadAppliedLive:
		res.Message = "Configuration validated, saved, and applied live."
	case server.ReloadAppliedDegraded:
		res.Message = "Configuration applied live with degradation: " + rr.Error
	case server.ReloadSavedNotLive:
		res.Message = "Configuration saved; the live reload is still in flight. Check the runtime overview for the final outcome."
	default:
		res.Message = "Configuration was saved but the live reload did not apply: " + rr.Error
	}
	return res
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
