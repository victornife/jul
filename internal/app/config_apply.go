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

	// OnManagedApplyAdmitted reserves an idempotent mutation on the existing
	// managed-apply ledger after validation/CAS succeeds but before the first
	// write-side effect. It is invoked only when the request carries ADR 0019
	// idempotency metadata. A non-nil error fails the mutation closed before any
	// side effect; nil is valid only for callers without idempotency metadata.
	OnManagedApplyAdmitted func(admin.ManagedApplyAdmission) error
	// OnManagedApplyAdmissionAborted removes a pre-side-effect reservation when
	// the mutation exits before its durable commit point. It is best-effort:
	// production wires the bounded ledger directly and context-free callers may
	// leave it nil.
	OnManagedApplyAdmissionAborted func(applyID string)

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
	// afterRestore is a deterministic test barrier invoked immediately after
	// restorePreviousLocked returns, on both the synchronous enqueue-failure
	// path and the async failed-reload path, before the caller decides
	// clean-vs-inconsistent or reassesses drift. Production leaves it nil;
	// tests use it to write an external change between the restoration write
	// and the baseline's own resolution of that restoration.
	afterRestore func()
	// beforeGateClears is a deterministic test barrier invoked immediately
	// before inFlightState clears, on both the synchronous enqueue-failure
	// path and the async finalizer, after any baseline resolution/retry has
	// already run. Production leaves it nil; tests use it to simulate a
	// watcher event landing in the narrow window between the transaction's
	// own terminal drift reassessment and the gate actually reopening.
	beforeGateClears func()
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
	// driftAssessmentPending is set by AssessDriftNow when a watcher/SIGHUP
	// event arrives while inFlightState is waiting and is deferred rather than
	// evaluated (see AssessDriftNow). It is protected by mu and drained by
	// drainPendingDriftAssessment once the gate clears, so a real external
	// write landing after the transaction's own terminal reassessment but
	// before the gate clears is not lost until an unrelated later trigger.
	driftAssessmentPending bool
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

// BootID returns the boot-scoped instance identity embedded in every apply id.
//
// It is published as `boot_id` by GET /api/v1/status and /api/v1/capabilities:
// the terminal ledger is process-local, so a changed boot_id tells a client its
// replay window and every idempotency binding were discarded (ADR 0019 §27.2).
// Reading it from here rather than minting a second identity is what keeps
// `boot_id` and the `rl_<instance>_<seq>` ids a client correlates it with from
// disagreeing.
func (c *ConfigApplyCoordinator) BootID() string { return c.managedApplyInstanceID() }

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

func (c *ConfigApplyCoordinator) suppressWatcher(digest [32]byte) {
	if c.WatchDigest != nil {
		c.WatchDigest.Store(&digest)
	}
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

	idempotencyReserved := false
	idempotencyCommitted := false
	defer func() {
		if idempotencyReserved && !idempotencyCommitted {
			c.abortManagedApplyIdempotency(reqCtx, id)
		}
	}()
	if err := c.reserveManagedApplyIdempotency(reqCtx, id, mode); err != nil {
		c.mu.Unlock()
		return ApplyResult{
			ApplyID:          id,
			OK:               false,
			Mode:             mode,
			Version:          persistedVersion,
			PersistedVersion: persistedVersion,
			DesiredVersion:   desiredVersion,
			Message:          "The idempotency reservation could not be recorded; nothing was changed.",
		}, fmt.Errorf("reserve managed apply idempotency: %w", err)
	}
	idempotencyReserved = reqCtx.Idempotency != nil

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
	idempotencyCommitted = true

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
		if c.afterRestore != nil {
			c.afterRestore()
		}
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
		// Verify restoration by re-reading disk BEFORE the baseline decides
		// clean vs inconsistent: restoreErr == nil only means the restoration
		// write itself did not error, not that disk still holds those bytes
		// at this instant — an external writer can race the restoration
		// write itself, same as it can race a successful apply's commit.
		terminal = c.withRestorationOutcome(terminal, baseline.Raw, baseline.Exists, rawDigest)
		var baselineDegraded *DegradedEntry
		if c.Authority == AuthorityManaged && c.ManagedBaseline != nil {
			baselineDegraded = c.rewindOrMarkInconsistent(terminal.Restored)
		}
		if baselineDegraded != nil {
			terminal.Degraded = append(terminal.Degraded, *baselineDegraded)
		}
		// M-05: Move callback after unlock to prevent mutex wedge on panic.
		c.mu.Unlock()
		// beforeGateClears fires with mu released: a test barrier that blocks
		// here must not hold mu, or a concurrent AssessDriftNow (which also
		// takes mu) would deadlock against it.
		if c.beforeGateClears != nil {
			c.beforeGateClears()
		}
		c.mu.Lock()
		c.inFlightState = ApplyInFlightNone
		c.mu.Unlock()
		c.drainPendingDriftAssessment()
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
		restoreNeeded := !rr.Published && !reloadOutcomeSucceeded(rr.Outcome)
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
			if c.afterRestore != nil {
				c.afterRestore()
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
			} else if err := c.completeWriteAndReassessDrift(data, persistedVersion); err != nil {
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
			c.resolveBaselineWriteRetry(data, func(b []byte) error { return c.completeWriteAndReassessDrift(b, persistedVersion) })
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
		//
		// beforeGateClears fires with mu released: a test barrier that blocks
		// here must not hold mu, or a concurrent AssessDriftNow (which also
		// takes mu) would deadlock against it.
		if c.beforeGateClears != nil {
			c.beforeGateClears()
		}
		c.mu.Lock()
		c.inFlightState = ApplyInFlightNone
		c.mu.Unlock()
		c.drainPendingDriftAssessment()
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

// reserveManagedApplyIdempotency records the client binding at the write
// linearization point. Callers invoke it only after all side-effect-free
// validation/CAS gates have succeeded and while holding the same coordinator
// mutation lock that guards the first write. A request without a key is a no-op.
func (c *ConfigApplyCoordinator) reserveManagedApplyIdempotency(reqCtx admin.ApplyRequestContext, applyID string, mode ApplyMode) error {
	if reqCtx.Idempotency == nil {
		return nil
	}
	if c.OnManagedApplyAdmitted == nil {
		return errors.New("managed apply idempotency admission is unavailable")
	}
	return c.OnManagedApplyAdmitted(admin.ManagedApplyAdmission{
		Context: reqCtx,
		ApplyID: applyID,
		Mode:    string(mode),
	})
}

func (c *ConfigApplyCoordinator) abortManagedApplyIdempotency(reqCtx admin.ApplyRequestContext, applyID string) {
	if reqCtx.Idempotency == nil || applyID == "" || c.OnManagedApplyAdmissionAborted == nil {
		return
	}
	c.OnManagedApplyAdmissionAborted(applyID)
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
