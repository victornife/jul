// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

	// FinalizationError carries a post-persistence transaction-tracking failure
	// (e.g. pending-record registration) discovered by the coordinator finalizer
	// and threaded to the trusted composition-root completion callback. It is
	// internal finalization provenance, NOT serialized in the apply result.
	FinalizationError string

	// AuthorityDenied is true when the operation was refused before any side
	// effect because the process is file_owned (ADR 0019 §15). This is a
	// defense-in-depth check: the admin HTTP layer normally denies first with
	// the exact config_authority_read_only shape, but every mutating coordinator
	// entry enforces it independently so no caller can bypass it.
	AuthorityDenied bool
	// Degraded carries bounded, non-content-bearing degradations that do not
	// change this result's own OK/outcome, following the ordering rule that a
	// degradation never upgrades or downgrades a terminal outcome (ADR 0019
	// §33.2).
	Degraded []DegradedEntry
	// ConfigState is the closed §16 state enum computed for this operation
	// (e.g. "managed_clean", "managed_drift"), when authority tracking is wired.
	ConfigState ConfigState
	// Origin is set only by AdoptExternal: "drift", "no_baseline", or
	// "inconsistent" (ADR 0019 §11.2.1/§14.1) — which condition the adoption
	// resolved.
	Origin string
	// AppOutcome carries an app-layer terminal outcome not expressible by
	// server.ReloadOutcome, currently only "owned_not_serving" (ADR 0019
	// §33.1): the configuration is owned and persisted but neither serving
	// nor staged. Empty for every ordinary apply.
	AppOutcome string
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

	// Authority is the process's immutable configuration-authority mode,
	// established once at startup (ADR 0019 §9/§10). The zero value is
	// AuthorityManaged, so context-free/unit-test callers that never set it see
	// today's behavior.
	Authority ConfigAuthority
	// ManagedBaseline owns the persisted managed-baseline marker+snapshot and
	// drift assessment (ADR 0019 §11.2/§12). Nil disables baseline persistence
	// and drift refusal, so unit tests that do not wire it are unaffected.
	ManagedBaseline *ManagedBaselineStore

	// RefreshState is called while applyMu is held before any state-dependent
	// decision. It must reconcile the planned-restart marker with disk and
	// update any runtime/disk divergence flags. Failures are treated as
	// inconsistent (fail-closed).
	RefreshState func() error

	// OnManagedApplyStarted is called once the candidate has been persisted and
	// the correlated live reload has been enqueued, but BEFORE the synchronous
	// HTTP path can return a 202 saved_not_live to the caller. The composition
	// root registers an exact-ID pending record in the terminal ledger so a real
	// 202 is never immediately followed by a 404 (AC-02). A non-nil error is a
	// transaction-tracking failure after persistence: the apply itself is not
	// rolled back and the already-accepted reload is not aborted; instead the
	// error is carried into terminal finalization and surfaced through
	// logs/health/ledger. Nil disables pending registration so context-free and
	// unit-test callers behave exactly as before.
	OnManagedApplyStarted func(admin.ManagedApplyStart) error

	// OnManagedApplyComplete is called by the async finalizer after the managed
	// apply has reached a terminal state (including any restoration). It receives
	// a single ManagedApplyCompletion object — the original request context, the
	// serialized terminal result, and the exact previous on-disk configuration —
	// and returns a ManagedApplyFinalization carrying the history/finalization
	// provenance threaded back onto the terminal result. The composition-root
	// callback performs the trusted history write itself and produces the fin, so
	// history-writing and terminal finalization are driven from one claim (H-05).
	// Nil disables completion notification for context-free and unit-test callers.
	OnManagedApplyComplete func(admin.ManagedApplyCompletion) admin.ManagedApplyFinalization

	// OnManagedApplyFinalizationError is invoked when the unified completion
	// callback panics during terminal finalization (WS02 §3.6). The coordinator
	// recovers the panic, threads a FinalizationError onto the terminal result,
	// and calls this hook with the full ManagedApplyCompletion (request context,
	// terminal result, prior raw) and the reconstructed panic error so the
	// composition root can make the degradation explicit — a structured error
	// log, a finalization-error metric, an advisory health state, and a
	// best-effort terminal ledger record that preserves the operation and
	// complete apply result — instead of silently swallowing the panic. A
	// finalization panic never fails an already-committed apply: the raw
	// configuration stays roll-back-able. The completion's PreviousRaw is
	// sensitive and must never be logged, serialized, or retained by the hook.
	// Nil leaves the recovered panic recorded on the returned finalization only,
	// so context-free and unit-test callers are unaffected.
	OnManagedApplyFinalizationError func(completion admin.ManagedApplyCompletion, err error)

	// ReportManagedApplyError makes a managed-apply machinery failure that
	// happens OUTSIDE the unified completion callback explicit (WS06 §7.6). The
	// coordinator calls it with the apply ID, a bounded phase ("restoration" for
	// a terminal restoration write failure, "pending" for a pending-registration
	// write failure), and the underlying error so the composition root can emit a
	// structured error log and a bounded finalization-error metric instead of
	// silently swallowing the failure. It carries only the bounded error message
	// — never raw TOML, secrets, or actor metadata. Nil leaves the failure
	// recorded on the returned result only, so context-free and unit-test callers
	// are unaffected.
	ReportManagedApplyError func(applyID string, phase string, err error)

	// beforePersist is a deterministic test barrier invoked after preflight and,
	// for staging, after the prepared marker is written but before the final
	// expected-baseline comparison. Production leaves it nil.
	beforePersist func(ApplyMode)
	// beforeRestore and waitMargin are deterministic test seams for exercising
	// slow terminal restoration. Production leaves them unset.
	beforeRestore func()
	waitMargin    time.Duration
	// beforeBaselineWriteRetry is a deterministic test barrier invoked at the
	// start of resolveBaselineWriteRetry, before it does any work. Production
	// leaves it nil; tests use it to hold the retry open and observe that the
	// admission gate (inFlightState) still refuses a concurrent hot apply for
	// as long as the retry is unresolved (ADR 0019 §11.2.0.1).
	beforeBaselineWriteRetry func()
	// afterBaselineWriteRetry is a deterministic test barrier invoked once
	// resolveBaselineWriteRetry has resolved (committed, abandoned, or marked
	// inconsistent). Production leaves it nil; tests use it to await the
	// retry instead of polling.
	afterBaselineWriteRetry func()

	// clock is an internal deterministic test seam for time. nil selects the
	// real wall clock; tests may inject a fakeClock to advance deadlines and
	// timers explicitly. It is not part of the public coordinator contract.
	clock Clock

	mu      sync.Mutex
	applyMu sync.Mutex
	// finalizeMu serializes managed terminal history/audit/metrics/ledger work
	// after the config mutation gate is released. This permits the next apply to
	// start without allowing two managed finalizers to write history at once.
	finalizeMu sync.Mutex

	// applyIDOnce guards the one-time generation of applyInstanceID, the
	// boot-scoped correlation prefix used by nextID. seq is the monotonically
	// increasing per-process apply sequence.
	applyIDOnce     sync.Once
	applyInstanceID string
	seq             atomic.Uint64

	// inFlightState tracks whether a managed apply transaction still owns the
	// config-path mutation/restoration gate. It is protected by mu.
	inFlightState ApplyInFlightState
}

// ApplyRaw applies a raw configuration bytes slice. It is the hot-apply entry
// point for the admin /api/config/apply path.
func (c *ConfigApplyCoordinator) ApplyRaw(ctx admin.ApplyRequestContext, data []byte, mode ApplyMode) (ApplyResult, error) {
	// ADR 0019 §15: file-owned denial precedes every lock, every read, and every
	// side effect. This is the defense-in-depth enforcement point; the admin
	// HTTP layer normally denies first with the exact wire shape.
	if c.Authority == AuthorityFileOwned {
		return ApplyResult{OK: false, Mode: mode, AuthorityDenied: true, Message: "Configuration is file-owned; the running server does not write it."}, nil
	}

	// applyMu serializes applies so only one candidate is in flight at a time.
	// c.mu protects coordinator state and is not held across the reload wait so
	// the async finalizer can safely restore without deadlocking. For the
	// hot-apply path specifically, applyMu itself is released once the
	// candidate is persisted and the reload is enqueued (see applyMuHeld
	// below) — inFlightState, not applyMu, is what continues to admit-refuse
	// a later apply for the rest of this transaction's lifetime, including
	// its baseline retry. Releasing applyMu there rather than only at
	// function return is what lets the finalizer resolve that retry under
	// applyMu itself without deadlocking against this call's own wait below
	// (ADR 0015 §4 / #226: the mutation gate and terminal publication must
	// not depend on each other for release).
	c.applyMu.Lock()
	applyMuHeld := true
	defer func() {
		if applyMuHeld {
			c.applyMu.Unlock()
		}
	}()

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

	// ADR 0019 §11 point 7: managed writes are refused while ownership is not
	// yet established, while drift exists, or while the baseline is
	// inconsistent — the same shape as the existing refusal of a hot apply
	// while a planned restart is pending, and for the same reason: writing
	// would silently discard something the operator did (or paper over
	// storage damage this process cannot explain).
	if msg, blocked := c.managedBaselineBlockMessage(); blocked {
		return ApplyResult{
			OK:             false,
			Mode:           mode,
			Message:        msg,
			PendingRestart: c.plannedRestartStatus(),
		}, nil
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

	// AC-03: allocate the transaction ApplyID BEFORE the hot vs stage_restart
	// branch so every persisted mutation — hot apply, enqueue failure, stage
	// create, and stage update — carries a stable ID and is routed through the
	// single completeManagedApply helper at terminalization.
	id := c.nextID()

	// AC-08: bound candidate resolution and every preflight gate with the ONE
	// absolute transaction deadline. When the admitting handler bound a
	// Deadline/RequestContext (R15-01), preflight derives from those so the
	// same deadline that started at HTTP admission governs preflight,
	// persistence, and reload — the candidate's own reload_timeout never
	// affects the apply that submits it. Absent a bound deadline, preflight
	// falls back to the currently serving reload_timeout. A pre-persistence
	// deadline breach aborts cleanly (disk unchanged) and is surfaced as a
	// phase-specific 504 by the admin API.
	pctx, cancel := c.preflightContext(ctx, cfg)
	defer cancel()
	ctx.RequestContext = nil // derived into pctx; never retained past this point

	// Parse and resolve the previous config so lifecycle.DiffConfig compares
	// effective values on both sides. Without resolution, secret references
	// produce false differences when the resolved value happens to match but
	// the reference string differs (M-02 fix). Resolution runs under pctx so a
	// stalled previous-config secret provider cannot hang the apply past the
	// transaction deadline (AC-08).
	var prevCfg *config.Config
	if len(prevRaw) > 0 {
		if raw, err := config.Parse(prevRaw); err == nil {
			if cand, err := config.NewCandidateContext(pctx, raw); err == nil {
				prevCfg = cand.Effective
			} else {
				prevCfg = raw // fallback to unresolved on resolution error
			}
		}
	}

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

	return c.applyCandidate(ctx, id, data, pfResult.Candidate, pfResult.PreparedAdmin, baseline, mode, &applyMuHeld)
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
// work (secret resolution + every preflight gate) under the ONE absolute
// transaction deadline (AC-08). When the admitting handler bound a Deadline it
// is used verbatim so preflight shares the exact deadline that started at HTTP
// admission (R15-01); the request context, when present, is the parent so
// client cancellation aborts pre-persistence work. Absent a bound deadline the
// serving reload_timeout is applied as a relative fallback. The returned cancel
// MUST be called by the caller. A zero budget yields a cancel-only context so
// behaviour is unchanged for callers/tests without a configured timeout.
func (c *ConfigApplyCoordinator) preflightContext(reqCtx admin.ApplyRequestContext, candidate *config.Config) (context.Context, context.CancelFunc) {
	base := reqCtx.RequestContext
	if base == nil {
		base = c.BaseCtx
	}
	if base == nil {
		base = context.Background()
	}
	if !reqCtx.Deadline.IsZero() {
		return c.withDeadline(base, reqCtx.Deadline)
	}
	if timeout := c.servingReloadTimeout(candidate); timeout > 0 {
		return c.withTimeout(base, timeout)
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
	// AC-08: attribute a deadline breach even when the gate returned nil. A gate
	// that finished a hair after the deadline fired (or ignored ctx) must not be
	// allowed to persist; the expired transaction context aborts before any
	// disk write, regardless of the gate's own error value.
	if ctxErr := pctx.Err(); ctxErr != nil {
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
		result := ApplyResult{
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
		return result, nil
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
func (c *ConfigApplyCoordinator) AssessDriftNow() {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.assessManagedDrift()
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

// rewindOrMarkInconsistent resolves the managed-baseline transaction when a
// managed write's reload failed and restoration was attempted (T-write's
// restored arm, ADR 0019 §11.2). When restoration succeeded it rewinds the
// baseline to the prior bytes; when restoration failed the on-disk state is
// uncertain, so the baseline is marked inconsistent immediately rather than
// left clean on the strength of an intention (§11.2.1a).
func (c *ConfigApplyCoordinator) rewindOrMarkInconsistent(restored bool) *DegradedEntry {
	if restored {
		if err := c.ManagedBaseline.RewindWrite(); err != nil {
			return &DegradedEntry{Kind: DegradedBaselineError, Message: "baseline could not be rewound after restoration"}
		}
		return nil
	}
	c.ManagedBaseline.MarkInconsistent(ReasonRestorationFailed)
	return &DegradedEntry{Kind: DegradedBaselineError, Message: "baseline left inconsistent after a failed restoration"}
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

// newManagedApplyInstanceID returns a 12-hex-character boot-scoped identifier
// generated once per process. It is correlation metadata, not a cryptographic
// secret; the fallback only runs if the OS CSPRNG is unavailable.
func newManagedApplyInstanceID() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}

	// Extremely defensive fallback. This identifier is correlation metadata,
	// not a cryptographic secret.
	fallback := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UTC().UnixNano())
	sum := sha256.Sum256([]byte(fallback))
	return hex.EncodeToString(sum[:6])
}

func (c *ConfigApplyCoordinator) managedApplyInstanceID() string {
	c.applyIDOnce.Do(func() {
		c.applyInstanceID = newManagedApplyInstanceID()
	})
	return c.applyInstanceID
}

// nextID allocates the next boot-scoped managed apply ID in the form
// rl_<boot-id>_<sequence>. The boot-id prevents apply-ID reuse across process
// restarts; the sequence is monotonically increasing within the process.
func (c *ConfigApplyCoordinator) nextID() string {
	return fmt.Sprintf(
		"rl_%s_%d",
		c.managedApplyInstanceID(),
		c.seq.Add(1),
	)
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
				return c.conflictResult(ApplyStageRestart, persistedVersion, desiredVersion, currentVersion), nil
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
				return ApplyResult{
					OK:               false,
					Mode:             ApplyStageRestart,
					Version:          persistedVersion,
					PersistedVersion: persistedVersion,
					DesiredVersion:   desiredVersion,
					Message:          "Failed to promote or reconcile the staged marker after candidate write: " + err.Error(),
				}, err
			}
			stagePromotionDegraded = &DegradedEntry{Kind: DegradedStagingError, Message: "planned-restart promotion did not complete synchronously; the stage still converges at the next restart"}
		}
	}
	// Steps 6-7 (ADR 0019 §11.2.3): the baseline snapshot and promotion follow
	// the planned-restart "staged" promotion, so a failed baseline write can
	// never be lost to — or block — the outer machine's own guarantee.
	var stageDegraded *DegradedEntry
	if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
		if err := c.ManagedBaseline.CompleteWrite(data, persistedVersion); err != nil {
			stageDegraded = &DegradedEntry{Kind: DegradedBaselineError, Message: "baseline snapshot could not be written"}
			// ApplyRaw still holds applyMu for this whole call, so the retry
			// runs inline rather than racing a later apply for it.
			c.retryBaselineWriteLocked(data, func(b []byte) error { return c.ManagedBaseline.CompleteWrite(b, persistedVersion) })
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

func (c *ConfigApplyCoordinator) applyCandidate(reqCtx admin.ApplyRequestContext, id string, data []byte, candidate *config.Candidate, preparedAdmin *server.PreparedCommit, baseline admin.MutationBaseline, mode ApplyMode, applyMuHeld *bool) (ApplyResult, error) {
	preparedOwned := preparedAdmin != nil
	defer func() {
		if preparedOwned {
			preparedAdmin.Abort()
		}
	}()
	persistedVersion := server.CanonicalVersion(candidate.Raw)
	desiredVersion := server.CanonicalVersion(candidate.Effective)
	rawDigest := sha256.Sum256(data)
	// AC-08/R15-01: reuse the ONE absolute deadline bound at HTTP admission.
	// transactionStarted is the admission time so provisional results and the
	// reload deadline share a single origin; only when no admission time was
	// bound (older callers/tests) do we fall back to the coordinator clock.
	transactionStarted := reqCtx.StartedAt
	if transactionStarted.IsZero() {
		transactionStarted = c.coordinatorClock().Now().UTC()
	}

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

	// Do NOT restart the transaction clock or grant a fresh full timeout after
	// preflight. Carry the single admission deadline through to reload so the
	// whole transaction (preflight + persistence + reload wait) shares one
	// budget (R15-01). When no deadline was bound, derive one from the serving
	// reload_timeout relative to the admission time — never a full timeout
	// starting now. A zero result means "no bound"; it is handled explicitly at
	// the wait below rather than collapsing into an accidental one-second wait.
	deadline := reqCtx.Deadline
	if deadline.IsZero() {
		if reloadTimeout := c.servingReloadTimeout(candidate.Effective); reloadTimeout > 0 {
			deadline = transactionStarted.Add(reloadTimeout)
		}
	}

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
	// T-write step 1 (ADR 0019 §11.2): record the baseline transition before
	// the configuration file changes. A failure here means nothing is
	// attempted — the config write below never runs.
	if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
		if err := c.ManagedBaseline.BeginWrite(sha256Hex(baseline.Raw), baseline.Version, sha256Hex(data), persistedVersion); err != nil {
			c.mu.Unlock()
			return ApplyResult{
				OK:               false,
				Mode:             mode,
				Version:          persistedVersion,
				PersistedVersion: persistedVersion,
				DesiredVersion:   desiredVersion,
				Message:          "Failed to record managed-baseline provenance; nothing was changed.",
			}, fmt.Errorf("%w: begin managed baseline write: %v", admin.ErrConfigStorageUnavailable, err)
		}
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
		if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
			c.ManagedBaseline.MarkFailedApply()
		}
		restoreErr := c.restorePreviousLocked(baseline.Raw, baseline.Exists, rawDigest)
		var baselineDegraded *DegradedEntry
		if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
			baselineDegraded = c.rewindOrMarkInconsistent(restoreErr == nil)
		}
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
		if baselineDegraded != nil {
			terminal.Degraded = append(terminal.Degraded, *baselineDegraded)
		}
		// M-05: Move callback after unlock to prevent mutex wedge on panic.
		c.mu.Unlock()
		// AC-05: an enqueue failure whose restoration also failed records a
		// recovery snapshot; a clean restoration records nothing. Recorded
		// through the single terminal completion helper outside c.mu.
		terminal = c.completeManagedApply(reqCtx, terminal, baseline.Raw)
		return terminal, err
	}
	preparedOwned = false // the server reload plan now owns commit/abort
	c.mu.Unlock()
	// The candidate is persisted and the reload is enqueued: release applyMu
	// here rather than at function return (see the comment where it was
	// acquired, in ApplyRaw). inFlightState — already ApplyInFlightWaiting —
	// is what continues to refuse a later apply for the rest of this
	// transaction, including the async finalizer's baseline retry below.
	*applyMuHeld = false
	c.applyMu.Unlock()

	// AC-02: register the exact-ID pending ledger record now that the candidate
	// is persisted and the reload is enqueued, but BEFORE the synchronous path
	// below can hand a 202 saved_not_live back to the HTTP caller. Registering
	// the pending record first closes the window where a real 202 could be
	// immediately followed by a spurious 404 that stalls the ConfigPanel poll.
	pending := c.provisionalResult(
		id, mode, persistedVersion, desiredVersion, transactionStarted, false,
		"Configuration saved; live reload is in flight.",
	)
	var trackingErr error
	if err := c.notifyManagedApplyStarted(reqCtx, pending); err != nil {
		// This is a transaction-tracking failure after persistence. Do not
		// pretend the apply itself failed or roll back a reload already accepted
		// by the runtime. Carry the error into terminal finalization and make it
		// visible through logs/health/ledger.
		trackingErr = fmt.Errorf("register managed apply pending record: %w", err)
		// WS06 §7.6: also make the pending-registration failure explicit at the
		// failure point through the composition root's structured log and bounded
		// finalization-error metric, not only on the terminal result.
		c.logPendingRegistrationFailure(id, trackingErr)
	}

	// Finalizer goroutine: sole owner of the reload result and restoration. It
	// creates exactly one terminal ApplyResult after disk state is final, then
	// sends that value to both the callback and the synchronous waiter.
	go func() {
		rr := <-resultCh
		restoreNeeded := !rr.Published && rr.Outcome != server.ReloadAppliedLive && rr.Outcome != server.ReloadAppliedDegraded
		c.mu.Lock()
		if restoreNeeded {
			if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
				c.ManagedBaseline.MarkFailedApply()
			}
			if c.beforeRestore != nil {
				c.beforeRestore()
			}
			if err := c.restorePreviousLocked(baseline.Raw, baseline.Exists, rawDigest); err != nil {
				c.logRestorationFailure(id, err)
			}
		}
		terminal := c.buildTerminalResult(mode, persistedVersion, desiredVersion, rr, baseline.Raw, baseline.Exists, rawDigest)
		// ADR 0019 §11.2.0.1: the baseline transaction terminalizes here,
		// before the admission gate (inFlightState) is cleared, so a later
		// apply is never admitted while this one's baseline is still
		// pending. A post-commit write failure needs a retry (§11.2.1a); it
		// cannot run inline here because it takes applyMu itself (freed
		// above once the candidate was persisted and the reload enqueued —
		// see ApplyRaw), and this goroutine still holds c.mu, which would
		// invert the lock order every other applyMu-then-c.mu caller uses.
		// c.mu is released below, before the retry runs, precisely to avoid
		// that.
		needsBaselineRetry := false
		if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
			if restoreNeeded {
				if d := c.rewindOrMarkInconsistent(terminal.Restored); d != nil {
					terminal.Degraded = append(terminal.Degraded, *d)
				}
			} else if err := c.ManagedBaseline.CompleteWrite(data, persistedVersion); err != nil {
				terminal.Degraded = append(terminal.Degraded, DegradedEntry{Kind: DegradedBaselineError, Message: "baseline snapshot could not be written"})
				needsBaselineRetry = true
			}
		}
		c.mu.Unlock()

		// ADR 0015 §4 / #226 requires the mutation gate to clear and the
		// terminal result to publish only after the config-path mutation is
		// fully resolved — a baseline retry is part of that mutation, not a
		// side effect of reporting it, so it must resolve here, before
		// either. inFlightState is still ApplyInFlightWaiting throughout
		// this call, so a later ordinary apply is refused as in flight for
		// the whole of it; AdoptExternal checks the same flag for the same
		// reason, since applyMu alone no longer serializes this window.
		if needsBaselineRetry {
			c.resolveBaselineWriteRetry(data, func(b []byte) error { return c.ManagedBaseline.CompleteWrite(b, persistedVersion) })
		}

		// #226 / AC-03: every config-path mutation — including any baseline
		// retry — is complete at this point. Clear the admission gate, then
		// release the server's reload-serialization gate, before any
		// completion callback can publish a terminal ledger record.
		//
		// The non-config terminal side effects remain exactly-once and ordered:
		// completeManagedApply serializes history/audit/metrics/ledger work with
		// finalizeMu. A later apply may start while that work finishes, but its own
		// terminal publication queues behind this finalizer.
		c.mu.Lock()
		c.inFlightState = ApplyInFlightNone
		c.mu.Unlock()
		close(finalizedCh)
		// Carry any post-persistence pending-registration failure into the
		// terminal finalization provenance so it is surfaced through the
		// ledger/overview rather than silently dropped.
		if trackingErr != nil {
			terminal.FinalizationError = trackingErr.Error()
		}
		// AC-05: drive the single terminal completion — the composition root
		// writes the configuration-history snapshot from the ManagedApplyCompletion
		// and returns the finalization provenance threaded back onto the terminal
		// result. The trusted history write runs outside c.mu.
		terminal = c.completeManagedApply(reqCtx, terminal, baseline.Raw)
		terminalCh <- terminal
	}()

	waitMargin := c.waitMargin
	if waitMargin <= 0 {
		waitMargin = time.Second
	}
	// Wait against the single transaction deadline. A zero deadline means no
	// bound was configured, so the synchronous waiter blocks until the finalizer
	// delivers (or the process shuts down) rather than collapsing into an
	// accidental one-second wait.
	var wait <-chan time.Time
	if !deadline.IsZero() {
		remaining := c.coordinatorClock().Until(deadline) + waitMargin
		if remaining < 0 {
			remaining = 0
		}
		timer := c.coordinatorClock().NewTimer(remaining)
		defer timer.Stop()
		wait = timer.C()
	}

	select {
	case terminal := <-terminalCh:
		return terminal, nil
	case <-c.BaseCtx.Done():
		return c.provisionalResult(id, mode, persistedVersion, desiredVersion, transactionStarted, false, "Configuration saved; the process is shutting down and the reload outcome is unknown."), nil
	case <-wait:
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

// notifyManagedApplyStarted registers the provisional pending record for a
// managed apply the moment the candidate is persisted and the reload is
// enqueued (AC-02). It projects the provisional saved_not_live result into the
// admin shape so the composition root can insert an exact-ID pending ledger
// record before the synchronous HTTP path can return a 202. A nil hook is a
// no-op so context-free and unit-test callers are unaffected. A non-nil error
// is a transaction-tracking failure after persistence; the caller carries it
// into terminal finalization rather than rolling back an accepted reload.
func (c *ConfigApplyCoordinator) notifyManagedApplyStarted(reqCtx admin.ApplyRequestContext, result ApplyResult) error {
	if c.OnManagedApplyStarted == nil {
		return nil
	}
	return c.OnManagedApplyStarted(admin.ManagedApplyStart{
		Context: reqCtx,
		Result:  toAdminConfigApplyResult(result),
	})
}

// notifyManagedApplyComplete invokes the single composition-root completion
// callback with the ManagedApplyCompletion object and returns the resulting
// ManagedApplyFinalization. A nil callback yields a zero finalization so
// context-free and unit-test callers are unaffected.
//
// WS02 §3.6: a callback panic is made EXPLICIT rather than silently discarded.
// The recovered panic is reconstructed into a FinalizationError on the returned
// finalization (so it is threaded onto the terminal result and surfaced through
// the ledger/overview) and, when wired, reported to OnManagedApplyFinalizationError
// so the composition root can emit a structured error log, increment the
// finalization-error metric, set an advisory health state, and best-effort
// write a terminal ledger record carrying the FinalizationError. A finalization
// panic never fails an already-committed apply: the raw configuration stays
// roll-back-able and the coordinator finalizer is not wedged.
func (c *ConfigApplyCoordinator) notifyManagedApplyComplete(comp admin.ManagedApplyCompletion) (fin admin.ManagedApplyFinalization) {
	if c.OnManagedApplyComplete == nil {
		return fin
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			fin.FinalizationError = fmt.Sprintf(
				"managed apply finalization panic: %v",
				recovered,
			)
			if c.OnManagedApplyFinalizationError != nil {
				c.OnManagedApplyFinalizationError(
					comp,
					errors.New(fin.FinalizationError),
				)
			}
		}
	}()

	return c.OnManagedApplyComplete(comp)
}

// completeManagedApply drives the single terminal completion for a managed
// apply: it hands the trusted composition root a ManagedApplyCompletion
// (request context, serialized terminal result, and the exact prior on-disk
// configuration) and threads the returned ManagedApplyFinalization provenance
// (history snapshot id, history degradation, and any post-persistence
// finalization error) back onto the terminal result. previousRaw is sensitive
// and is forwarded only to the callback, never logged or retained here. A nil
// callback yields a zero finalization so context-free and unit-test callers are
// unaffected.
func (c *ConfigApplyCoordinator) completeManagedApply(reqCtx admin.ApplyRequestContext, result ApplyResult, previousRaw []byte) ApplyResult {
	// Managed finalization writes history and publishes audit/metric/ledger
	// truth. Keep those side effects ordered even though #226 deliberately
	// releases the config mutation gate before terminal publication.
	c.finalizeMu.Lock()
	defer c.finalizeMu.Unlock()

	// ADR 0019 §16: config_state is computed once, here — the single point
	// every managed apply/adopt path funnels through at terminalization —
	// rather than re-derived independently by each surface.
	result.ConfigState, _ = c.currentConfigState()

	fin := c.notifyManagedApplyComplete(admin.ManagedApplyCompletion{
		Context:     reqCtx,
		Result:      toAdminConfigApplyResult(result),
		PreviousRaw: append([]byte(nil), previousRaw...),
	})
	result.HistorySnapshotID = fin.HistorySnapshotID
	result.HistoryError = fin.HistoryError
	result.FinalizationError = fin.FinalizationError
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

// logPendingRegistrationFailure makes a post-persistence pending-registration
// write failure explicit (WS06 §7.6). The error is still carried onto the
// terminal result for the ledger/overview; this routes the same failure to the
// composition root's structured log and finalization-error metric at the point
// it happens.
func (c *ConfigApplyCoordinator) logPendingRegistrationFailure(id string, err error) {
	if c.ReportManagedApplyError != nil {
		c.ReportManagedApplyError(id, "pending", err)
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
