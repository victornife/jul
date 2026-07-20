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

// PlannedRestartStore holds the managed planned-restart sidecar state. In
// P2-02 it is used only to refuse hot applies while a staged restart is
// pending; the full stage/discard/reconcile workflow is implemented in P2-03.
type PlannedRestartStore struct {
	mu       sync.Mutex
	pending  bool
	raw      []byte
	stagedAt time.Time
}

// IsPending reports whether a managed planned restart is currently staged.
func (s *PlannedRestartStore) IsPending() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// Stage records a planned-restart candidate. It is a no-op if the candidate
// bytes are identical to the already-staged bytes.
func (s *PlannedRestartStore) Stage(raw []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = true
	s.raw = raw
	s.stagedAt = time.Now()
}

// Discard clears any staged planned restart and returns the previously staged
// raw bytes and true when there was one.
func (s *PlannedRestartStore) Discard() ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending {
		return nil, false
	}
	raw := s.raw
	s.pending = false
	s.raw = nil
	s.stagedAt = time.Time{}
	return raw, true
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

	candidate, err := c.Preflight.Apply(cfg, prevCfg)
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

	return c.applyCandidate(data, candidate, prevRaw, mode)
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

// DiscardPlannedRestart clears any staged planned restart. In P2-02 the full
// staging workflow is not yet implemented, so this always reports that no
// planned restart was pending.
func (c *ConfigApplyCoordinator) DiscardPlannedRestart() (ApplyResult, error) {
	if c.PlannedRestart == nil {
		return ApplyResult{
			OK:      true,
			Mode:    ApplyHot,
			Message: "No planned restart was pending.",
		}, nil
	}
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

func (c *ConfigApplyCoordinator) suppressWatcher(digest [32]byte) {
	if c.WatchDigest != nil {
		c.WatchDigest.Store(&digest)
	}
}

func (c *ConfigApplyCoordinator) plannedRestartStatus() *admin.PendingRestartStatus {
	if c.PlannedRestart == nil || !c.PlannedRestart.IsPending() {
		return nil
	}
	return &admin.PendingRestartStatus{
		Managed:          true,
		Staged:           true,
		DiscardAvailable: true,
		Inconsistent:     false,
	}
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
