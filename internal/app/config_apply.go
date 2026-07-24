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
	// ApplyID is the monotonic transaction ID, populated regardless of whether
	// a reload was submitted. This allows callbacks to record outcomes even
	// when Reload is nil (e.g., enqueue failure).
	ApplyID string
	OK      bool
	Mode    ApplyMode
	// Version and PersistedVersion identify the canonical unresolved candidate
	// persisted on disk. Version is retained for API compatibility.
	Version          string
	PersistedVersion string
	// DesiredVersion identifies the resolved effective candidate; ServingVersion
	// identifies the resolved effective live runtime.
	DesiredVersion   string
	ServingVersion   string
	Conflict         bool
	CurrentVersion   string
	Reload           *server.ReloadResult
	PendingRestart   *admin.PendingRestartStatus
	Message          string
	ValidationErrors []string
	RestartRequired  bool
	CanStage         bool
	// Restoration fields (F-03): first-class truth about whether a rejected
	// candidate was rolled back to the previous configuration.
	Persisted           bool   // true if the candidate bytes were written to disk
	Restored            bool   // true if the previous configuration was restored
	RestoreError        string // non-empty if restoration was attempted and failed
	FinalDiskVersion    string // canonical version of the on-disk file after apply
	FinalServingVersion string // canonical version of the live serving config (may lag)

	// StagedRestartIsUpdate is true when a stage_restart apply replaced an
	// already-pending staged candidate. It is computed inside ApplyRaw (after
	// applyMu is acquired) so concurrent stage applies cannot misclassify the
	// first stage as an update.
	StagedRestartIsUpdate bool

	// TimedOutPhase names the transaction phase that exceeded reload_timeout
	// before the candidate was persisted (AC-08). It is empty unless the
	// bounded pre-persistence work (resolve, preflight_*) was aborted by the
	// deadline. The admin API maps a non-empty value to 504 Gateway Timeout
	// with timed_out_phase. A timeout AFTER persistence surfaces instead as a
	// saved_not_live (202) result, never here, so disk truth is never lost.
	TimedOutPhase string

	// HistorySnapshotID and HistoryError capture the configuration-history
	// snapshot written at terminalization (AC-05). They are internal
	// provenance for the composition root and tests; they are NOT part of the
	// serialized apply result. HistoryError is non-empty when the raw snapshot
	// was written but its metadata sidecar failed — a degraded-but-usable
	// state that never fails an already-committed apply.
	HistorySnapshotID string
	HistoryError      string
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
	AuthGeneration func() string
	// ReadConfigRaw reads the persisted config for baseline/CAS verification.
	// Nil uses os.ReadFile(Path); tests may inject deterministic failures.
	ReadConfigRaw func() ([]byte, error)

	// RefreshState is called while applyMu is held before any state-dependent
	// decision. It must reconcile the planned-restart marker with disk and
	// update any runtime/disk divergence flags. Failures are treated as
	// inconsistent (fail-closed).
	RefreshState func() error

	// OnManagedApplyComplete is called by the async finalizer after the
	// managed apply has reached a terminal state (including any restoration).
	// It receives the original request context so the terminal audit event can
	// be attributed to the caller (H-05).
	OnManagedApplyComplete func(admin.ApplyRequestContext, admin.ConfigApplyResult)

	// WriteManagedHistory records a configuration-history snapshot at
	// terminalization (AC-05). It receives the request context, the terminal
	// result, and the exact previous on-disk configuration. The previous raw
	// bytes are sensitive: they are passed only to this trusted in-process
	// writer and are never logged, retained, or serialized by the coordinator.
	// It returns the snapshot id and a non-fatal history-degradation error.
	// Nil disables coordinator-side history so handlers keep recording eagerly.
	WriteManagedHistory func(admin.ApplyRequestContext, admin.ConfigApplyResult, []byte) (string, error)

	// beforePersist is a deterministic test barrier invoked after preflight and,
	// for staging, after the prepared marker is written but before the final
	// expected-baseline comparison. Production leaves it nil.
	beforePersist func(ApplyMode)
	// beforeRestore and waitMargin are deterministic test seams for exercising
	// slow terminal restoration. Production leaves them unset.
	beforeRestore func()
	waitMargin    time.Duration

	mu      sync.Mutex
	applyMu sync.Mutex
	seq     atomic.Uint64

	// inFlightState tracks whether a managed apply transaction is still
	// outstanding. It is protected by applyMu for state transitions.
	inFlightState ApplyInFlightState
}

// ApplyRaw applies a raw configuration bytes slice. It is the hot-apply entry
// point for the admin /api/config/apply path.
func (c *ConfigApplyCoordinator) ApplyRaw(ctx admin.ApplyRequestContext, data []byte, mode ApplyMode) (ApplyResult, error) {
	// applyMu serializes applies so only one candidate is in flight at a time.
	// c.mu protects coordinator state and is not held across the reload wait so
	// the async finalizer can safely restore without deadlocking.
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	if mode == "" {
		mode = ApplyHot
	}

	// H-02: refresh authoritative planned-restart state from disk/runtime
	// before any state-dependent decision. Failures mark the store inconsistent
	// and block the operation.
	if err := c.refreshStateLocked(); err != nil {
		return ApplyResult{
			OK:             false,
			Mode:           mode,
			Message:        "Planned-restart state refresh failed: " + err.Error(),
			PendingRestart: c.plannedRestartStatus(),
		}, nil
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
	if ctx.LiveGeneration != 0 && c.LiveSnapshot != nil && c.LiveSnapshot().Generation != ctx.LiveGeneration {
		return ApplyResult{OK: false, Mode: mode, Conflict: true, Message: "The live runtime changed since this edit was authorized; reload and try again."}, nil
	}
	if ctx.AuthGeneration != "" && c.AuthGeneration != nil && c.AuthGeneration() != ctx.AuthGeneration {
		return ApplyResult{OK: false, Mode: mode, Conflict: true, Message: "Admin authentication changed since this edit was authorized; reload and try again."}, nil
	}

	baselineHint := ctx.Baseline
	preparedCandidate := ctx.Candidate
	ctx.Baseline = nil // do not retain raw configuration in the audit callback context
	ctx.Candidate = nil
	baseline, err := c.loadMutationBaseline(baselineHint)
	if err != nil {
		return ApplyResult{
			OK:      false,
			Mode:    mode,
			Message: "The persisted configuration could not be read safely.",
		}, err
	}
	prevRaw := baseline.Raw

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

	// Block a new stage_restart when the planned-restart state would make it
	// unsafe: external unmanaged divergence or post-reconciliation
	// inconsistency. A managed_staged state is allowed and treated as a staged
	// update (H-03).
	if mode == ApplyStageRestart && c.PlannedRestart != nil {
		st := c.PlannedRestart.State()
		if st.State == PlannedRestartStateExternalDivergence || st.State == PlannedRestartStateInconsistent {
			msg := "Cannot stage a restart while external disk/runtime divergence is present."
			if st.State == PlannedRestartStateInconsistent {
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

	// AC-03: allocate the transaction ApplyID BEFORE the hot vs stage_restart
	// branch so every persisted mutation — hot apply, enqueue failure, stage
	// create, and stage update — carries a stable ID and is routed through the
	// single completeManagedApply helper at terminalization.
	id := c.nextID()

	// AC-08: bound candidate resolution and every preflight gate with the
	// currently serving reload_timeout so a stalled ${file:...} provider or a
	// wedged handler/bind probe cannot hang the managed apply past the
	// transaction deadline. The deadline is derived from the SERVING config's
	// reload_timeout, never the candidate's (R15-01). A pre-persistence
	// deadline breach aborts cleanly (disk unchanged) and is surfaced as a
	// phase-specific 504 by the admin API.
	pctx, cancel := c.preflightContext(cfg)
	defer cancel()

	if mode == ApplyStageRestart {
		return c.applyStageRestart(pctx, ctx, id, cfg, preparedCandidate, prevCfg, data, baseline)
	}

	pfResult, timedOutPhase, err := c.runPreflight(pctx, cfg, preparedCandidate, prevCfg, PreflightHot)
	if timedOutPhase != "" {
		return c.timedOutResult(mode, timedOutPhase), nil
	}
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

	return c.applyCandidate(ctx, id, data, pfResult.Candidate, pfResult.PreparedAdmin, baseline, mode)
}

// servingReloadTimeout returns the transaction deadline budget for AC-08. It is
// taken from the currently SERVING configuration's reload_timeout so a candidate
// that changes reload_timeout governs only the next apply, never the one that
// submits it (R15-01). When no live snapshot is available (unit tests without a
// runtime) it falls back to the candidate's own reload_timeout so preflight is
// still bounded; a zero value disables bounding.
func (c *ConfigApplyCoordinator) servingReloadTimeout(candidate *config.Config) time.Duration {
	if c.LiveSnapshot != nil {
		if snap := c.LiveSnapshot(); snap.EffectiveConfig != nil && snap.EffectiveConfig.Global.ReloadTimeout > 0 {
			return snap.EffectiveConfig.Global.ReloadTimeout.Std()
		}
	}
	if candidate != nil && candidate.Global.ReloadTimeout > 0 {
		return candidate.Global.ReloadTimeout.Std()
	}
	return 0
}

// preflightContext derives the bounded context that caps all pre-persistence
// work (secret resolution + every preflight gate) with the serving
// reload_timeout (AC-08). The returned cancel MUST be called by the caller. A
// zero budget yields a cancel-only context so behaviour is unchanged for
// callers/tests without a configured timeout.
func (c *ConfigApplyCoordinator) preflightContext(candidate *config.Config) (context.Context, context.CancelFunc) {
	base := c.BaseCtx
	if base == nil {
		base = context.Background()
	}
	if timeout := c.servingReloadTimeout(candidate); timeout > 0 {
		return context.WithTimeout(base, timeout)
	}
	return context.WithCancel(base)
}

// runPreflight runs the preflight gates under the bounded, phase-instrumented
// context pctx and attributes a reload_timeout breach to the phase that was
// executing when the deadline fired (AC-08). It returns:
//   - (result, "", nil) on success;
//   - (nil, phase, nil)  when the bounded context expired before persistence;
//   - (nil, "", err)     for an ordinary validation failure.
//
// The phase observer records the most recently entered gate so a deadline that
// trips inside a gate is attributed to that gate rather than a coarse bucket.
func (c *ConfigApplyCoordinator) runPreflight(pctx context.Context, cfg *config.Config, preparedCandidate *config.Candidate, prevCfg *config.Config, mode PreflightMode) (*PreflightResult, string, error) {
	var (
		mu        sync.Mutex
		lastPhase string
	)
	obsCtx := withPhaseObserver(pctx, func(phase string) {
		mu.Lock()
		lastPhase = phase
		mu.Unlock()
	})

	var (
		pfResult *PreflightResult
		err      error
	)
	if preparedCandidate != nil && server.CanonicalVersion(preparedCandidate.Raw) == server.CanonicalVersion(cfg) {
		pfResult, err = c.Preflight.ApplyCandidate(obsCtx, preparedCandidate, prevCfg, mode)
	} else {
		pfResult, err = c.Preflight.Apply(obsCtx, cfg, prevCfg, mode)
	}
	if err != nil && pctx.Err() != nil {
		mu.Lock()
		phase := lastPhase
		mu.Unlock()
		if phase == "" {
			phase = PreflightPhaseResolve
		}
		return nil, phase, nil
	}
	return pfResult, "", err
}

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

// ApplyConfig applies a parsed configuration. It marshals the config and
// delegates to ApplyRaw so the same preflight, persistence, and restoration
// path is used for structured edits.
func (c *ConfigApplyCoordinator) ApplyConfig(ctx admin.ApplyRequestContext, cfg *config.Config, mode ApplyMode) (ApplyResult, error) {
	data, err := config.Marshal(cfg)
	if err != nil {
		return ApplyResult{
			OK:      false,
			Mode:    mode,
			Message: "Failed to marshal configuration.",
		}, err
	}
	return c.ApplyRaw(ctx, data, mode)
}

// DiscardPlannedRestart clears any staged planned restart. When the store is
// file-backed it performs the safety-verified discard from §17.5: it checks
// marker consistency, disk digest, and live serving version before restoring
// the backup. On success the watcher echo of the restoration is suppressed.
func (c *ConfigApplyCoordinator) DiscardPlannedRestart() (ApplyResult, error) {
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

// refreshStateLocked calls the RefreshState hook if configured. It must be
// called while applyMu is held. Errors are logged and returned so the caller
// can fail closed.
func (c *ConfigApplyCoordinator) refreshStateLocked() error {
	if c.RefreshState == nil {
		return nil
	}
	return c.RefreshState()
}

func (c *ConfigApplyCoordinator) nextID() string {
	return fmt.Sprintf("rl_%d", c.seq.Add(1))
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

func (c *ConfigApplyCoordinator) suppressWatcher(digest [32]byte) {
	if c.WatchDigest != nil {
		c.WatchDigest.Store(&digest)
	}
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
				if cand, err := config.NewCandidate(raw); err == nil {
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
	if c.PlannedRestart != nil {
		if err := c.PlannedRestart.PromoteToStagedVerified(data); err != nil {
			c.mu.Unlock()
			// An external write in the promotion window (or a state mismatch)
			// must not be reported as a successful stage. Map the disk-change
			// races to a conflict so the HTTP layer returns 409; genuine
			// state/programming errors surface as a storage error.
			if errors.Is(err, ErrStagedCandidateChanged) {
				return c.conflictResult(ApplyStageRestart, persistedVersion, desiredVersion, currentVersion), nil
			}
			return ApplyResult{
				OK:               false,
				Mode:             ApplyStageRestart,
				Version:          persistedVersion,
				PersistedVersion: persistedVersion,
				DesiredVersion:   desiredVersion,
				Message:          "Failed to promote staged marker after candidate write: " + err.Error(),
			}, err
		}
	}
	c.mu.Unlock()

	msg := "Configuration staged for the next process restart; the live runtime is unchanged."
	if isUpdate {
		msg = "Staged configuration updated for the next process restart; the live runtime is unchanged."
	}
	result := ApplyResult{
		ApplyID:               id,
		OK:                    true,
		Mode:                  ApplyStageRestart,
		Version:               persistedVersion,
		PersistedVersion:      persistedVersion,
		DesiredVersion:        desiredVersion,
		ServingVersion:        liveVersion,
		PendingRestart:        c.plannedRestartStatus(),
		Message:               msg,
		StagedRestartIsUpdate: isUpdate,
	}
	// AC-05: a committed stage (create or update) snapshots the prior on-disk
	// configuration — the previous serving config for a fresh stage, or the
	// prior staged candidate for an update — because baseline.Raw is whatever
	// was persisted before this stage overwrote it. Recorded once, here at the
	// stage's terminal success, so no snapshot is written at a provisional 202.
	result = c.recordManagedHistory(reqCtx, result, baseline.Raw)
	return result, nil
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
		PersistedVersion: st.PersistedVersion,
		ServingVersion:   st.ServingVersion,
	}
	if !st.StagedAt.IsZero() {
		res.StagedAt = st.StagedAt.UTC().Format(time.RFC3339)
	}
	return res
}

func (c *ConfigApplyCoordinator) applyCandidate(reqCtx admin.ApplyRequestContext, id string, data []byte, candidate *config.Candidate, preparedAdmin *server.PreparedCommit, baseline admin.MutationBaseline, mode ApplyMode) (ApplyResult, error) {
	preparedOwned := preparedAdmin != nil
	defer func() {
		if preparedOwned {
			preparedAdmin.Abort()
		}
	}()
	persistedVersion := server.CanonicalVersion(candidate.Raw)
	desiredVersion := server.CanonicalVersion(candidate.Effective)
	rawDigest := sha256.Sum256(data)
	transactionStarted := time.Now()

	// AC-03: id is allocated once in ApplyRaw before the hot/stage branch so
	// every persisted mutation shares one monotonic transaction ID.
	// resultCh has capacity 1 because the server sends exactly one terminal
	// result and the finalizer goroutine below is the sole receiver.
	resultCh := make(chan server.ReloadResult, 1)
	// terminalCh carries the one final ApplyResult after any required
	// restoration. The HTTP response and completion callback consume the same
	// value instead of independently reconstructing state from disk.
	terminalCh := make(chan ApplyResult, 1)
	finalizedCh := make(chan struct{})

	// Use the currently serving config's reload_timeout for this transaction,
	// not the candidate's. A candidate that changes reload_timeout affects the
	// next apply, never this one (R15-01).
	reloadTimeout := candidate.Effective.Global.ReloadTimeout
	if snap := c.LiveSnapshot(); snap.EffectiveConfig != nil && snap.EffectiveConfig.Global.ReloadTimeout > 0 {
		reloadTimeout = snap.EffectiveConfig.Global.ReloadTimeout
	}
	deadline := transactionStarted.Add(reloadTimeout.Std())

	// Serialize file writes and staged state with the coordinator mutex. It is
	// released before the reload wait so the async finalizer cannot deadlock
	// with the HTTP goroutine; applyMu still prevents concurrent applies.
	if c.beforePersist != nil {
		c.beforePersist(mode)
	}
	c.mu.Lock()
	// Finding 12: coordinator-level optimistic-concurrency CAS. prevRaw was read
	// at the top of ApplyRaw WITHOUT c.mu held, and the candidate was
	// preflighted and diffed against it. Between that read and this write an
	// external writer (file watcher, operator editing the file directly) that
	// does not hold applyMu could have changed the file on disk. Writing the
	// candidate now would silently clobber that external change — the exact
	// time-of-check/time-of-use window the audit calls out. Re-read the base
	// under c.mu, immediately before the write, and reject with a conflict when
	// the on-disk base no longer matches what this apply prepared against. The
	// HTTP layer maps a non-OK managed result to 409.
	changed, currentVersion, verifyErr := c.verifyBaselineLocked(baseline)
	if verifyErr != nil {
		c.mu.Unlock()
		return ApplyResult{
			OK:               false,
			Mode:             mode,
			Version:          persistedVersion,
			PersistedVersion: persistedVersion,
			DesiredVersion:   desiredVersion,
			Message:          "The persisted configuration could not be verified safely.",
		}, verifyErr
	}
	if changed {
		c.mu.Unlock()
		return c.conflictResult(mode, persistedVersion, desiredVersion, currentVersion), nil
	}
	if err := atomicfile.Write(c.Path, data, 0o600); err != nil {
		c.mu.Unlock()
		return ApplyResult{
			OK:               false,
			Mode:             mode,
			Version:          persistedVersion,
			PersistedVersion: persistedVersion,
			DesiredVersion:   desiredVersion,
			Message:          "Failed to persist configuration.",
		}, err
	}

	// Mark a managed transaction as in-flight before releasing applyMu.
	// The finalizer clears this only after any restoration is complete.
	c.inFlightState = ApplyInFlightWaiting

	// Suppress the echo of our own write on the file watcher.
	c.suppressWatcher(rawDigest)

	req := server.ReloadRequest{
		ID:                 id,
		Source:             server.ReloadSourceAdmin,
		Candidate:          candidate,
		PreparedAdmin:      preparedAdmin,
		ExpectedGeneration: reqCtx.LiveGeneration,
		AuthGeneration:     reqCtx.AuthGeneration,
		ValidateAuthGeneration: func(expected string) bool {
			return c.AuthGeneration == nil || c.AuthGeneration() == expected
		},
		RawDigest: rawDigest,
		Deadline:  deadline,
		Result:    resultCh,
		Finalized: finalizedCh,
	}

	if err := c.SubmitReload(req); err != nil {
		// Enqueue failed: the candidate file is on disk but the runtime will
		// not reload. Restore the exact previous bytes and suppress the
		// restoration echo so the watcher does not loop.
		restoreErr := c.restorePreviousLocked(baseline.Raw, baseline.Exists, rawDigest)
		c.inFlightState = ApplyInFlightNone
		// Build structured truth with Persisted/Restored/FinalDiskVersion
		// while still holding the lock so the disk read in withRestorationOutcome
		// is atomic with the restoration. ApplyID is set so the callback can
		// record the outcome even when Reload is nil.
		msg := "Reload was not enqueued; the previous configuration was restored."
		if restoreErr != nil {
			msg = "Reload enqueue failed; the candidate may remain on disk: " + restoreErr.Error()
		}
		terminal := ApplyResult{
			ApplyID:          id,
			OK:               false,
			Mode:             mode,
			Version:          persistedVersion,
			PersistedVersion: persistedVersion,
			DesiredVersion:   desiredVersion,
			Message:          msg,
			Persisted:        true,
			Reload: &server.ReloadResult{
				ID:             id,
				Source:         server.ReloadSourceAdmin,
				DesiredVersion: desiredVersion,
				ServingVersion: server.CanonicalVersion(c.LiveSnapshot().EffectiveConfig),
				Outcome:        server.ReloadNotApplied,
				Persisted:      true,
				Published:      false,
				FailedPhase:    "enqueue",
				Error:          err.Error(),
			},
		}
		terminal = c.withRestorationOutcome(terminal, baseline.Raw, baseline.Exists, rawDigest)
		// M-05: Move callback after unlock to prevent mutex wedge on panic.
		c.mu.Unlock()
		// AC-05: an enqueue failure whose restoration also failed records a
		// recovery snapshot; a clean restoration records nothing. Recorded
		// before the completion callback and outside c.mu.
		terminal = c.recordManagedHistory(reqCtx, terminal, baseline.Raw)
		c.notifyManagedApplyComplete(reqCtx, terminal)
		return terminal, err
	}
	preparedOwned = false // the server reload plan now owns commit/abort
	c.mu.Unlock()

	// Finalizer goroutine: sole owner of the reload result and restoration. It
	// creates exactly one terminal ApplyResult after disk state is final, then
	// sends that value to both the callback and the synchronous waiter.
	go func() {
		rr := <-resultCh
		restoreNeeded := !rr.Published && rr.Outcome != server.ReloadAppliedLive && rr.Outcome != server.ReloadAppliedDegraded
		c.mu.Lock()
		if restoreNeeded {
			if c.beforeRestore != nil {
				c.beforeRestore()
			}
			if err := c.restorePreviousLocked(baseline.Raw, baseline.Exists, rawDigest); err != nil {
				c.logRestorationFailure(id, err)
			}
		}
		terminal := c.buildTerminalResult(mode, persistedVersion, desiredVersion, rr, baseline.Raw, baseline.Exists, rawDigest)
		// AC-03: terminal finalization ordering. Unlock the coordinator file
		// mutex, then run terminal finalization (history/audit/metrics/ledger)
		// BEFORE clearing the in-flight guard, closing Finalized, or delivering
		// the result to the synchronous waiter. This guarantees that:
		//   - no subsequent managed transaction can begin while terminal
		//     history/audit is still outstanding (inFlightState stays set), and
		//   - the HTTP caller never observes completion until the terminal
		//     record is durably finalized in the ledger/audit/metrics.
		// notifyManagedApplyComplete contains its own panic recovery so a
		// callback panic cannot wedge the coordinator here.
		c.mu.Unlock()
		// AC-05: record the terminal configuration-history snapshot before the
		// completion callback so the prior config is durably captured
		// (pre_apply on a committed apply, recovery when a failed apply's
		// restoration also failed) and enriched onto the terminal result. The
		// filesystem write runs outside c.mu.
		terminal = c.recordManagedHistory(reqCtx, terminal, baseline.Raw)
		c.notifyManagedApplyComplete(reqCtx, terminal)
		c.mu.Lock()
		c.inFlightState = ApplyInFlightNone
		c.mu.Unlock()
		close(finalizedCh)
		terminalCh <- terminal
	}()

	waitMargin := c.waitMargin
	if waitMargin <= 0 {
		waitMargin = time.Second
	}
	waitTimeout := time.Until(deadline) + waitMargin
	if waitTimeout <= 0 {
		waitTimeout = time.Second
	}

	select {
	case terminal := <-terminalCh:
		return terminal, nil
	case <-c.BaseCtx.Done():
		return c.provisionalResult(id, mode, persistedVersion, desiredVersion, transactionStarted, false, "Configuration saved; the process is shutting down and the reload outcome is unknown."), nil
	case <-time.After(waitTimeout):
		// Finalizer goroutine now owns the restoration obligation. The result
		// returned here marks Persisted because the candidate is on disk, but
		// the final restoration state will only be known after restoreDone.
		// ApplyID is populated so the callback's monotonic sequence guard can
		// correlate the async finalizer's later terminal result (M-05).
		return c.provisionalResult(id, mode, persistedVersion, desiredVersion, transactionStarted, true, "Configuration saved; the live reload is still in flight. Check the runtime overview for the final outcome."), nil
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

func (c *ConfigApplyCoordinator) notifyManagedApplyComplete(reqCtx admin.ApplyRequestContext, result ApplyResult) {
	if c.OnManagedApplyComplete == nil {
		return
	}
	defer func() { _ = recover() }()
	c.OnManagedApplyComplete(reqCtx, toAdminConfigApplyResult(result))
}

// recordManagedHistory writes the terminal configuration-history snapshot for a
// managed apply through the trusted WriteManagedHistory hook and records the
// resulting snapshot id / degradation onto the result. previousRaw is the exact
// prior on-disk configuration; it is forwarded only to the hook and is never
// logged or retained here. When no hook is wired it is a no-op. The hook itself
// decides — from the terminal outcome — whether to snapshot and with which
// reason, so this method never encodes history policy.
func (c *ConfigApplyCoordinator) recordManagedHistory(reqCtx admin.ApplyRequestContext, result ApplyResult, previousRaw []byte) ApplyResult {
	if c.WriteManagedHistory == nil {
		return result
	}
	id, err := c.WriteManagedHistory(reqCtx, toAdminConfigApplyResult(result), previousRaw)
	result.HistorySnapshotID = id
	if err != nil {
		result.HistoryError = err.Error()
	}
	return result
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
		OK:               rr.Outcome == server.ReloadAppliedLive || rr.Outcome == server.ReloadAppliedDegraded,
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
