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
}

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

	mu  sync.Mutex
	seq atomic.Uint64
}

// ApplyRaw applies a raw configuration bytes slice. It is the hot-apply entry
// point for the admin /api/config/apply path.
func (c *ConfigApplyCoordinator) ApplyRaw(data []byte, mode ApplyMode) (ApplyResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if mode == "" {
		mode = ApplyHot
	}

	prevRaw, _ := os.ReadFile(c.Path)

	if mode == ApplyHot && c.PlannedRestart != nil && c.PlannedRestart.IsPending() {
		return ApplyResult{
			OK:             false,
			Mode:           mode,
			Message:        "A planned restart is pending; discard or complete it before applying hot changes.",
			PendingRestart: c.plannedRestartStatus(),
		}, nil
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

	var prevCfg *config.Config
	if len(prevRaw) > 0 {
		prevCfg, _ = config.Parse(prevRaw)
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

	return c.applyCandidate(data, pfResult.Candidate, prevRaw, mode)
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
// Correct crash-consistent ordering (C-01 fix):
//  1. prevRaw captured in ApplyRaw before any write (passed in).
//  2. StageManaged writes .bak (prevRaw) and prepared marker — no candidate on disk yet.
//  3. atomicfile.Write writes the candidate.
//  4. StageManaged already promoted marker to "staged" in step 2.
func (c *ConfigApplyCoordinator) applyStageRestart(cfg, prevCfg *config.Config, data, prevRaw []byte) (ApplyResult, error) {
	pfResult, err := c.Preflight.Apply(c.BaseCtx, cfg, prevCfg, PreflightStageRestart)
	if err != nil {
		return ApplyResult{
			OK:               false,
			Mode:             ApplyStageRestart,
			Message:          "The configuration contains errors; no change was staged.",
			ValidationErrors: []string{err.Error()},
		}, nil
	}

	candidate := pfResult.Candidate
	desiredVersion := server.CanonicalVersion(candidate.Effective)
	liveVersion := ""
	if c.LiveSnapshot != nil {
		liveVersion = server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig)
	}

	// Collect subsystem names from the lifecycle diff.
	subsystems := subsystemNames(pfResult.Lifecycle)

	// Whether this is a first stage (base version = live serving config) or an
	// update to an already-pending stage.
	isUpdate := c.PlannedRestart != nil && c.PlannedRestart.IsPending()

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

	msg := "Configuration staged for the next process restart; the live runtime is unchanged."
	if !isUpdate && len(subsystems) == 0 {
		msg += " (Note: no restart-required changes detected; a hot apply may also be possible.)"
	}
	return ApplyResult{
		OK:             true,
		Mode:           ApplyStageRestart,
		Version:        desiredVersion,
		ServingVersion: liveVersion,
		PendingRestart: c.plannedRestartStatus(),
		Message:        msg,
	}, nil
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
	if c.PlannedRestart == nil || !c.PlannedRestart.IsPending() {
		return nil
	}
	st := c.PlannedRestart.Status()
	if !st.Managed {
		return nil
	}
	res := &admin.PendingRestartStatus{
		Managed:          true,
		Staged:           st.Staged,
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

func (c *ConfigApplyCoordinator) applyCandidate(data []byte, candidate *config.Candidate, prevRaw []byte, mode ApplyMode) (ApplyResult, error) {
	desiredVersion := server.CanonicalVersion(candidate.Effective)
	rawDigest := sha256.Sum256(data)

	id := c.nextID()
	resultCh := make(chan server.ReloadResult, 1)
	deadline := time.Now().Add(candidate.Effective.Global.ReloadTimeout.Std())

	if err := atomicfile.Write(c.Path, data, 0o600); err != nil {
		return ApplyResult{
			OK:      false,
			Mode:    mode,
			Version: desiredVersion,
			Message: "Failed to persist configuration.",
		}, err
	}

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
		c.restorePrevious(prevRaw, rawDigest)
		return ApplyResult{
			OK:      false,
			Mode:    mode,
			Version: desiredVersion,
			Message: "Reload enqueue timed out after 5s; the configuration was saved but may not be applied.",
		}, err
	}

	waitTimeout := time.Until(deadline) + time.Second
	if waitTimeout <= 0 {
		waitTimeout = time.Second
	}

	select {
	case rr := <-resultCh:
		return c.decorateResult(mode, desiredVersion, data, prevRaw, &rr), nil
	case <-c.BaseCtx.Done():
		// Process is shutting down. We do not restore because we cannot know
		// whether the reload reached Publish; the operator can inspect the
		// runtime overview on restart.
		return ApplyResult{
			OK:             true,
			Mode:           mode,
			Version:        desiredVersion,
			ServingVersion: server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig),
			Message:        "Configuration saved; the process is shutting down and the reload outcome is unknown.",
		}, nil
	case <-time.After(waitTimeout):
		return ApplyResult{
			OK:             true,
			Mode:           mode,
			Version:        desiredVersion,
			ServingVersion: server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig),
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

func (c *ConfigApplyCoordinator) decorateResult(mode ApplyMode, desiredVersion string, data, prevRaw []byte, rr *server.ReloadResult) ApplyResult {
	if rr == nil {
		return ApplyResult{
			OK:      false,
			Mode:    mode,
			Version: desiredVersion,
			Message: "Reload result is unavailable.",
		}
	}

	// Pre-Publish failure or timeout: the candidate file must not remain
	// authoritative because the runtime is still serving the previous
	// generation. Restore the exact previous bytes and suppress the
	// restoration echo.
	if !rr.Published && rr.Outcome != server.ReloadAppliedLive && rr.Outcome != server.ReloadAppliedDegraded {
		c.restorePrevious(prevRaw, sha256.Sum256(data))
	}

	res := ApplyResult{
		OK:             rr.Outcome == server.ReloadAppliedLive || rr.Outcome == server.ReloadAppliedDegraded,
		Mode:           mode,
		Version:        desiredVersion,
		ServingVersion: rr.ServingVersion,
		Reload:         rr,
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

func (c *ConfigApplyCoordinator) restorePrevious(prevRaw []byte, currentDigest [32]byte) {
	if len(prevRaw) == 0 {
		return
	}
	_ = atomicfile.Write(c.Path, prevRaw, 0o600)
	prevDigest := sha256.Sum256(prevRaw)
	if prevDigest != currentDigest {
		c.suppressWatcher(prevDigest)
	}
}
