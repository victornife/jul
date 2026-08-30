// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package admin exposes a separate operational HTTP listener bound to loopback
// by default. It serves health checks, Prometheus metrics, cache purge, and a
// configuration reload trigger. It must never be attached to the main traffic
// listeners.
package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/config"
	"jul/internal/observability"
	"jul/internal/rbac"
	"jul/internal/server"
)

// Purger clears cached responses. A nil Purger means caching is disabled.
type Purger interface {
	Purge()
	Delete(key string)
}

// ApplyOperation is a bounded enum identifying the initiating write operation
// for a managed configuration mutation. It is assigned by the HTTP handler
// before the coordinator runs so the terminal audit event, history entry, and
// managed-result record can be traced to the operation without parsing free
// text or inferring from URL strings, mode, or response shape (AC-01).
type ApplyOperation string

const (
	ApplyOperationConfigApply   ApplyOperation = "config.apply"
	ApplyOperationPatchApply    ApplyOperation = "config.patch"
	ApplyOperationLegacyRaw     ApplyOperation = "config.raw"
	ApplyOperationSettings      ApplyOperation = "config.settings"
	ApplyOperationRollback      ApplyOperation = "config.rollback"
	ApplyOperationAdoptExternal ApplyOperation = "config.adopt_external"
)

// ApplyRequestContext carries the authenticated caller from an admin HTTP
// request through to the managed apply coordinator so async finalizers can
// emit an audit event attributed to the original actor (H-05).
type ApplyRequestContext struct {
	// Operation is the immutable identity of the initiating write operation.
	// It MUST be assigned by the handler before calling the coordinator.
	Operation ApplyOperation
	// Resource is the audited resource label (e.g. "config").
	Resource string

	Actor   string
	TokenID string
	// TokenDigest is retained only for internal confirmation binding. Never log
	// or serialize it; TokenID remains the public audit identifier.
	TokenDigest string
	SourceIP    string

	// Baseline is the exact persisted configuration snapshot against which the
	// HTTP handler performed concurrency, authorization, and reachability
	// checks. The coordinator must compare this digest immediately before any
	// write instead of adopting a later filesystem read as its own baseline.
	Baseline *MutationBaseline
	// Candidate is the already-resolved immutable candidate used for effective
	// authorization and passed unchanged to the coordinator.
	Candidate *config.Candidate
	// LiveGeneration and AuthGeneration bind authorization to the exact live
	// runtime and authentication snapshots observed by the handler.
	LiveGeneration uint64
	AuthGeneration string

	// StartedAt is the wall-clock time the handler admitted the operation. It
	// is recorded on the terminal ledger record so a browser can observe when
	// the transaction began (AC-01/AC-02). Deadline is the absolute transaction
	// deadline derived from the currently serving reload_timeout (AC-08); it is
	// zero when no bounded deadline applies.
	StartedAt time.Time
	Deadline  time.Time

	// RequestContext is the initiating admin HTTP request's context. It bounds
	// candidate resolution and other pre-persistence work to the caller's
	// absolute managed-apply deadline (AC-08) and propagates client cancellation
	// before anything is persisted. It is never serialized and is cleared before
	// the coordinator retains any async audit/ledger copy.
	RequestContext context.Context `json:"-"`
}

// MutationBaseline is the authoritative raw-first snapshot for a configuration
// mutation. Config is parsed from Raw, Version is the canonical unresolved
// configuration version, and Digest identifies the exact persisted bytes.
type MutationBaseline struct {
	Raw     []byte
	Digest  [32]byte
	Version string
	Config  *config.Config
	Exists  bool
}

// prepareMutationCandidateContext resolves raw into an immutable candidate under
// operationCtx so a slow or blocked secret provider (e.g. a ${file:...} read on
// a stalled mount) is bounded by the caller's absolute managed-apply deadline
// (AC-08). The resolved candidate is stored on reqCtx for the coordinator to
// reuse without re-resolving.
func prepareMutationCandidateContext(operationCtx context.Context, reqCtx *ApplyRequestContext, raw *config.Config) (*config.Candidate, error) {
	if raw == nil {
		return nil, errors.New("candidate is nil")
	}
	candidate, err := config.NewCandidateContext(operationCtx, raw)
	if err != nil {
		return nil, err
	}
	reqCtx.Candidate = candidate
	return candidate, nil
}

// bindManagedApplyDeadline derives the single absolute transaction deadline for
// a managed mutation from the currently serving reload_timeout at admission
// (AC-08). The deadline is taken from the live effective config so a candidate
// that changes reload_timeout governs only the next apply, never the one that
// submits it (R15-01). It is a no-op when a deadline is already bound, no live
// snapshot is available, or the serving reload_timeout is not positive.
func (s *Server) bindManagedApplyDeadline(ctx *ApplyRequestContext) {
	if ctx == nil || !ctx.Deadline.IsZero() {
		return
	}
	if ctx.StartedAt.IsZero() {
		ctx.StartedAt = time.Now().UTC()
	}
	if s.deps.LiveSnapshot == nil {
		return
	}
	live := s.deps.LiveSnapshot()
	if live.EffectiveConfig == nil {
		return
	}
	timeout := live.EffectiveConfig.Global.ReloadTimeout.Std()
	if timeout <= 0 {
		return
	}
	ctx.Deadline = ctx.StartedAt.Add(timeout)
}

// managedApplyPrePersistenceContext derives the bounded context that caps all
// handler-side pre-persistence work (candidate secret resolution and effective
// authorization preparation) with the single absolute deadline bound at
// admission (AC-08) and propagates client cancellation. The returned cancel
// MUST be called by the caller. A zero deadline yields a cancel-only context so
// behaviour is unchanged for callers without a bounded deadline.
func managedApplyPrePersistenceContext(reqCtx ApplyRequestContext, fallback context.Context) (context.Context, context.CancelFunc) {
	base := reqCtx.RequestContext
	if base == nil {
		base = fallback
	}
	if base == nil {
		base = context.Background()
	}
	if !reqCtx.Deadline.IsZero() {
		return context.WithDeadline(base, reqCtx.Deadline)
	}
	return context.WithCancel(base)
}

func (s *Server) bindMutationRuntime(ctx *ApplyRequestContext) *config.Config {
	if s.deps.LiveSnapshot != nil {
		snapshot := s.deps.LiveSnapshot()
		ctx.LiveGeneration = snapshot.Generation
		ctx.AuthGeneration = s.AuthGeneration()
		return snapshot.EffectiveConfig
	}
	ctx.AuthGeneration = s.AuthGeneration()
	return nil
}

// AuthorizationError represents an authorization failure with typed details.
// It is returned by authorization helpers instead of writing HTTP responses
// directly, so callers can handle response writing consistently.
type AuthorizationError struct {
	Status   int
	Message  string
	Reason   string // user-facing reason code
	Required rbac.Permission
}

func (e *AuthorizationError) Error() string { return e.Message }

// ManagedApplyOutcome is the terminal result of a managed configuration apply,
// including any async restoration. It is exposed in RuntimeOverview so the
// console can show the final state of a previously timed-out apply.
type ManagedApplyOutcome struct {
	ID                  string    `json:"id"`
	Mode                string    `json:"mode"`
	OK                  bool      `json:"ok"`
	Outcome             string    `json:"outcome"` // server.ReloadOutcome string
	Restored            bool      `json:"restored,omitempty"`
	RestoreError        string    `json:"restore_error,omitempty"`
	FinalDiskVersion    string    `json:"final_disk_version,omitempty"`
	FinalServingVersion string    `json:"final_serving_version,omitempty"`
	CompletedAt         time.Time `json:"completed_at"`
	Actor               string    `json:"actor,omitempty"`
	SourceIP            string    `json:"source_ip,omitempty"`

	// HistorySnapshotID, HistoryError, and FinalizationError surface the
	// configuration-history finalization provenance (AC-14) separately from the
	// reload success/failure carried by Outcome. A committed apply whose raw
	// history snapshot was written but whose metadata sidecar failed is a
	// degraded-but-usable state: HistorySnapshotID is set AND HistoryError is
	// non-empty. These never fail readiness — the raw config stays
	// roll-back-able — so the Console shows them as an advisory finalization
	// banner distinct from the reload outcome.
	HistorySnapshotID string `json:"history_snapshot_id,omitempty"`
	HistoryError      string `json:"history_error,omitempty"`
	FinalizationError string `json:"finalization_error,omitempty"`
}

// ManagedApplyAdvisory is the ADVISORY, non-readiness finalization-health state
// of the most recent managed apply (WS02 §3.9). It is healthy after a terminal
// finalization that ran without finalization or configuration-history
// degradation, and unhealthy — carrying the apply ID and a bounded detail — when
// terminal finalization degraded (a completion-callback panic, a terminal-ledger
// completion failure, or a configuration-history snapshot/metadata failure). It
// is surfaced in the runtime overview as managed_apply_finalization and is
// deliberately independent of readiness: a finalization degradation NEVER fails
// /readyz and NEVER turns an already-committed apply into a failed apply. It
// carries only bounded, low-cardinality metadata — never raw TOML, secrets, or
// actor tokens.
type ManagedApplyAdvisory struct {
	Healthy bool      `json:"healthy"`
	At      time.Time `json:"at,omitempty"`
	ApplyID string    `json:"apply_id,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// ManagedApplyFinalization carries the terminal finalization provenance that is
// deliberately NOT part of the serialized ConfigApplyResult (AC-05 invariant):
// the configuration-history snapshot id, its non-fatal degradation, and any
// finalization-machinery error. The composition root threads it from the
// coordinator into the durable ledger record and the runtime-overview outcome
// so the Console can render a finalization/degradation surface (AC-14) that is
// independent of reload success/failure.
type ManagedApplyFinalization struct {
	HistorySnapshotID string
	HistoryError      string
	FinalizationError string
}

// ManagedApplyStart is the provisional pending-registration signal for a
// managed configuration apply (AC-02). The composition root turns it into a
// pending terminal-ledger record the moment the candidate is persisted and the
// live reload is enqueued, but before the HTTP caller can observe a 202
// saved_not_live. Registering the pending record first closes the window where
// a real 202 saved_not_live could be immediately followed by a spurious 404,
// which stalls the ConfigPanel on its first failed poll. The Result carries the
// provisional pending projection (ApplyID + saved_not_live reload) and the
// Context carries the authenticated caller, operation, start time, deadline,
// and owner token used to enrich and authorize the pending record.
type ManagedApplyStart struct {
	Context ApplyRequestContext
	Result  ConfigApplyResult
}

// ManagedApplyCompletion is the single terminal-completion object handed to the
// unified OnManagedApplyComplete callback the moment a managed configuration
// apply reaches a terminal state (including any restoration). It replaces the
// former split hooks — the void completion notification plus the separate
// WriteManagedHistory writer — so history-writing and terminal finalization are
// driven from one claim inside the composition root. The Context attributes the
// terminal audit/ledger record to the original caller (H-05); the Result is the
// serialized terminal projection (which by the AC-05 invariant never carries
// HistorySnapshotID/HistoryError); the callback returns a ManagedApplyFinalization
// carrying the history/finalization provenance threaded back to the coordinator.
type ManagedApplyCompletion struct {
	Context ApplyRequestContext
	Result  ConfigApplyResult

	// PreviousRaw is the exact prior on-disk configuration. It is sensitive and
	// is used only by the trusted in-process history writer. Never log,
	// serialize, or retain it in the ledger.
	PreviousRaw []byte
}

// Deps wires the admin server to runtime components owned by the composition
// root. All fields are optional; endpoints degrade gracefully when a
// dependency is nil.
type Deps struct {
	// Product is the commercial product name shown in the GUI (e.g. "Jul.IA").
	Product string
	// Version is the running build version shown in the GUI.
	Version string
	// ConfigPath is the path to the active configuration file, shown in the GUI.
	ConfigPath string
	// Metrics serves the Prometheus exposition format.
	Metrics http.Handler
	// Stats returns a point-in-time snapshot of runtime metrics for the console
	// dashboard, polled by the /api/stats endpoint. Nil yields an
	// "unavailable" snapshot so the console can render an empty state.
	Stats func() observability.StatsSnapshot
	// TrafficSources returns the bounded top-N projection of request hosts,
	// origins, and referers for the Console Overview Traffic Sources panel
	// (Milestone 1.4). Nil omits the panel.
	TrafficSources func() observability.TrafficSources
	// Cache, when non-nil, backs the /cache/purge endpoint.
	Cache Purger
	// Reload triggers a configuration reload. It returns an error if the
	// reload could not be enqueued within the implementation's timeout.
	Reload func() error
	// Ready reports whether the server is ready to receive traffic.
	Ready func() bool
	// LiveSnapshot returns the current bound listener state from the runtime
	// server. Used by PendingRestartCheck to evaluate listener rebind against
	// actually-bound addresses rather than the on-disk baseline.
	LiveSnapshot func() server.LiveSnapshot

	// StreamStatus reports the most recent L4 stream-proxy reload outcome for the
	// Console Overview. Because stream listeners reload asynchronously after the
	// HTTP swap, their success/failure cannot be returned in the synchronous
	// apply response; the console polls this instead. It returns "" when no
	// stream is configured (panel omitted), "ok" when the running stream set
	// matches the applied config, and "failed: <reason>" when the last reload was
	// rejected and the prior listeners are still serving. Nil omits the field.
	StreamStatus func() string
	// ReadConfigRaw returns the raw configuration file contents for the GUI
	// editor. Nil disables config viewing/editing.
	ReadConfigRaw func() ([]byte, error)
	// WriteConfigRaw validates and persists raw configuration text, then
	// triggers a reload. It returns a non-nil error (without writing) when the
	// content is invalid. Nil disables raw editing.
	WriteConfigRaw func([]byte) error
	// LoadConfig returns the current parsed configuration for the simple
	// settings form. Nil disables the settings form.
	LoadConfig func() (*config.Config, error)
	// SaveConfig persists a modified configuration (re-serialized to TOML),
	// validating and reloading. Nil disables the settings form.
	SaveConfig func(*config.Config) error

	// Upstreams returns a snapshot of the live upstream pools and per-backend
	// health for the console upstream panel. Nil yields an empty list.
	Upstreams func() []UpstreamStatus
	// UpstreamResilience returns live resilience state for every pool serving
	// under the given name, one entry per scheme. An unknown name returns nil,
	// which the handler renders as a 404. Nil disables the endpoint.
	UpstreamResilience func(name string) []PoolResilience
	// Certs returns configured-certificate metadata (subject, expiry; never key
	// material) for the console certificate panel. Nil yields an empty list.
	Certs func() []CertStatus

	// RequestSamples returns the bounded ring buffer of recent requests for the
	// Console v2 Request Samples panel (Milestone 5.1). Nil omits the panel.
	RequestSamples func() []observability.RequestSample
	// FailingRoutes returns the top-n paths ranked by recent failures for the
	// Console v2 Top Failing Routes panel (Milestone 5.2). Nil omits the panel.
	FailingRoutes func(n int) []observability.RouteFailure
	// UpstreamHealthHistory returns per-backend up/down history for the Console
	// v2 Upstream Health History panel (Milestone 5.5). Nil omits the panel.
	UpstreamHealthHistory func() []observability.BackendHealthHistory
	// CertRenewalHistory returns per-domain certificate renewal history for the
	// Console v2 Certificate Renewal History panel (Milestone 5.6). Nil omits
	// the panel.
	CertRenewalHistory func() []observability.CertRenewalHistory

	// RecentLogs returns up to limit recent access-log entries (newest first)
	// from the bounded ring buffer for the Console v2 Operations Log tab
	// (Phase 4g). Nil omits the tab.
	RecentLogs func(limit int) []observability.LogEntry
	// SubscribeLogs registers a live follower for the Operations Log stream. It
	// returns a receive channel of new entries and an unsubscribe function the
	// caller must invoke when the stream closes. Nil disables log streaming.
	SubscribeLogs func() (<-chan observability.LogEntry, func())

	// PluginsCompiled reports whether this binary includes the WASM plugin
	// runtime (the wasmplugins build tag). The Console v2 Plugins panel (Phase
	// 4h) surfaces it so the guided editor can warn that declarations validate
	// but the apply preflight rejects them on a lean build.
	PluginsCompiled bool

	// StreamCompiled reports whether this binary includes the L4 stream proxy
	// (the stream build tag). The Console v2 Streams panel (Phase 4i) surfaces it
	// so the guided editor can warn that declarations validate but a lean binary
	// refuses to start with them.
	StreamCompiled bool

	// WAFCompiled reports whether this binary includes the web application
	// firewall (the waf build tag). The Console Security panel surfaces it so
	// the guided WAF editor can warn that a policy validates here but the apply
	// preflight rejects an enabled WAF on a non-waf build.
	WAFCompiled bool

	// EgressBlocked returns the bounded per-subsystem/reason tally of egress
	// allow-list blocks for the Console Security panel (P4-01). Nil omits the
	// recent-blocked breakdown (e.g. on a build/test without a live metrics
	// source). It exposes no destination host or IP.
	EgressBlocked func() []observability.EgressBlockedCount

	// LastReload reports the structured outcome of the most recent runtime
	// reload. Nil when no reload has been attempted yet. Used by the apply
	// handler to include the previous reload snapshot in the apply response.
	LastReload func() *server.ReloadResult
	// PendingRestartCheck reports which startup-bound subsystems have changed
	// on disk relative to the values the running process was built from,
	// meaning they require a process restart to take effect. The live snapshot
	// lets the check compare against actually-bound listeners instead of the
	// on-disk baseline. Returns an empty slice (or nil) when no restart is
	// pending. Used by the Overview to show a persistent indicator when saved
	// changes are not yet live.
	PendingRestartCheck func(server.LiveSnapshot) []string

	// ApplyConfigRaw validates, persists, and reloads raw configuration bytes.
	// The mode string is "hot" or "stage_restart". It returns a structured
	// result correlated with the live reload outcome.
	ApplyConfigRaw func(ApplyRequestContext, []byte, string) (ConfigApplyResult, error)
	// ApplyConfig validates, persists, and reloads a structured config object.
	// The mode string is "hot" or "stage_restart". It returns a structured
	// result correlated with the live reload outcome.
	ApplyConfig func(ApplyRequestContext, *config.Config, string) (ConfigApplyResult, error)
	// DiscardPendingRestart discards the managed staged restart and atomically
	// restores the previous configuration. A verification failure (inconsistent
	// state, disk digest mismatch, or changed serving version) returns a
	// non-nil error and leaves all files untouched.
	DiscardPendingRestart func() (ConfigApplyResult, error)
	// PendingRestart returns the current managed planned-restart status, or nil
	// when a staged restart is pending. This is the structured source of truth
	// for the overview banner and the /api/config/pending-restart endpoint.
	PendingRestart func() *PendingRestartStatus
	// LastManagedApply returns the terminal outcome of the most recent managed
	// apply, including async restoration state (H-05). Nil when no apply has
	// finalized since startup.
	LastManagedApply func() *ManagedApplyOutcome
	// ManagedApplies is the bounded terminal-result ledger (AC-02). When set,
	// GET /api/config/applies/{id} serves the exact terminal (or pending)
	// record for a managed apply transaction, so the console can retrieve the
	// precise outcome of a recent accepted apply regardless of later
	// transactions. Nil yields 404 for all IDs.
	ManagedApplies *ManagedApplyRegistry
	// ObserveManagedApplyLookup counts one exact-ID managed-apply lookup by
	// bounded result ("pending", "terminal", "missing", or "invalid") for the
	// GET /api/config/applies/{id} endpoint (WS06 §7.5). It carries no apply ID,
	// actor, or source IP. Nil disables lookup metrics so context-free callers
	// and tests are unaffected.
	ObserveManagedApplyLookup func(result string)
	// AdminHealth reports the health of admin-subsystem concerns that are owned
	// by the composition root (e.g., the most recent reload's admin subsystem
	// result). It returns nil when healthy. When it returns an error, /readyz
	// reports not ready and the runtime overview surfaces the degradation.
	AdminHealth func() error

	// ManagedApplyFinalizationHealth returns the ADVISORY, non-readiness
	// finalization-health state of the most recent managed apply (WS02 §3.9):
	// healthy after a clean terminal finalization, unhealthy (with the apply ID
	// and a bounded detail) after a finalization panic, a terminal-ledger
	// completion failure, or a configuration-history snapshot/metadata failure.
	// It is surfaced in the runtime overview as managed_apply_finalization and
	// MUST NOT gate /readyz — an already-committed apply stays roll-back-able.
	// Nil when no managed apply has finalized (the overview omits the field).
	ManagedApplyFinalizationHealth func() *ManagedApplyAdvisory

	// ManagedHistoryActive reports that the managed apply coordinator records
	// configuration-history snapshots at terminalization (AC-05). When true the
	// HTTP handlers MUST NOT record history eagerly for managed apply paths —
	// doing so would double-snapshot and, worse, snapshot at a provisional 202
	// before the terminal outcome is known. When false (unit tests that wire
	// WriteConfigRaw/ApplyConfig fakes without a terminal callback) the handlers
	// keep recording history eagerly so behavior is unchanged.
	ManagedHistoryActive bool

	// ObserveAuthorityDenied counts one mutating request refused by
	// denyIfFileOwned (ADR 0019 §15), labeled by the bounded operation name.
	// Nil disables the metric so context-free and unit-test callers are
	// unaffected.
	ObserveAuthorityDenied func(reason string)

	// Authority reports the process's configuration-authority mode, its
	// source, and its current managed/file-owned state (ADR 0019 §9/§10/§16).
	// Nil is treated as managed with no drift/inconsistency, so existing tests
	// and embedding callers that do not wire it are unaffected. Every mutating
	// admin endpoint consults it before any side effect (§15); the Console
	// banner and the runtime overview surface it read-only.
	Authority func() ConfigAuthorityStatus

	// RefreshAuthorityDrift re-assesses managed-baseline drift on demand and
	// returns the resulting status. It is the "explicit drift/status refresh"
	// trigger ADR 0019 §12 lists as one of exactly four event-driven
	// assessment points (watcher event, SIGHUP, before every managed write,
	// and this one) — deliberately distinct from Authority, which only reads
	// the last-assessed, possibly-stale status; polling Authority itself must
	// never substitute for this, or drift detection becomes the polling loop
	// §12 says does not exist. Nil disables the refresh endpoint (returns
	// 501); a no-op outside managed authority, matching AssessDriftNow.
	RefreshAuthorityDrift func() ConfigAuthorityStatus

	// AdoptExternalPreview assesses the current external file against the
	// managed baseline without any side effect: strict decode, resolve,
	// validate, lint and lifecycle-classify, and a diff against the baseline
	// snapshot when one exists (ADR 0019 §14). Nil disables the adopt-preview
	// endpoint (returns 501).
	AdoptExternalPreview func() (AdoptPreviewResult, error)
	// AdoptExternal performs the authenticated adopt-external operation (ADR
	// 0019 §14): it re-verifies the external file matches observedDigest,
	// establishes or adopts the managed baseline through T-mark, and — when
	// the adopted bytes are not already live — submits them through the
	// dedicated AdoptExternal coordinator entry (§14.3), never through the
	// ordinary apply path. Nil disables the adopt endpoint (returns 501).
	AdoptExternal func(ctx ApplyRequestContext, req AdoptExternalRequest) (ConfigApplyResult, error)
}

// ConfigAuthorityStatus is the wire-safe projection of the process's
// configuration-authority state (ADR 0019 §9/§10/§16), exposed through
// Deps.Authority so admin handlers and status responses can reason about it
// without importing internal/app (which would create an import cycle: app
// already imports admin for ApplyRequestContext, ConfigApplyResult, etc.).
type ConfigAuthorityStatus struct {
	// Mode is "managed" or "file_owned".
	Mode string `json:"config_authority"`
	// Source is "explicit", "default", or "no_config_file".
	Source string `json:"config_authority_source"`
	// ConfigState is the closed §16 state enum (e.g. "managed_clean",
	// "managed_drift", "file_owned_desired_ahead").
	ConfigState string `json:"config_state,omitempty"`
	// InconsistentReason is set only when ConfigState is
	// "managed_inconsistent"; the state alone is not actionable.
	InconsistentReason string `json:"config_inconsistent_reason,omitempty"`
	// Drift and DriftDetectedAt describe an unresolved managed-mode external
	// edit. DriftDetectedAt is the zero time when Drift is false.
	Drift           bool      `json:"drift"`
	DriftDetectedAt time.Time `json:"drift_detected_at,omitempty"`
	// BaselineVersion/DiskVersion are canonical versions (never raw bytes or
	// digests beyond what §13 already bounds); DiskParseError is set only when
	// the on-disk content does not parse.
	BaselineVersion string `json:"baseline_version,omitempty"`
	DiskVersion     string `json:"disk_version,omitempty"`
	DiskParseError  string `json:"disk_parse_error,omitempty"`
	// DiskRawDigest is the on-disk raw sha256 digest, truncated to the same
	// 16 hex characters CanonicalVersion uses (ADR 0019 §13) — bounded
	// evidence that the file is (or is not) a byte-for-byte match for the
	// baseline, never the bytes themselves. Set only when the disk could be
	// read; empty on a disk read error.
	DiskRawDigest string `json:"disk_raw_digest,omitempty"`
}

// IsFileOwned reports whether mutating admin endpoints must be refused. A
// zero-value ConfigAuthorityStatus (Mode == "") is treated as managed, so a
// caller that never wired Deps.Authority sees unchanged behavior.
func (a ConfigAuthorityStatus) IsFileOwned() bool {
	return a.Mode == "file_owned"
}

// AdoptExternalRequest is the wire body of both the adopt-external preview and
// apply operations (ADR 0019 §14).
type AdoptExternalRequest struct {
	// ObservedDigest is the raw sha256 digest of the external file observed at
	// preview time. The apply step re-verifies it under the write lock and
	// conflicts (409 drift_detected) if the file changed since.
	ObservedDigest string `json:"observed_digest"`
	// BaseVersion is the managed baseline's canonical version observed at
	// preview time.
	BaseVersion string `json:"base_version,omitempty"`
	// Mode is "hot" or "stage_restart".
	Mode string `json:"mode,omitempty"`
	// Confirm must be true on the apply step; adoption without an explicit
	// confirmation is rejected, because it accepts bytes Jul did not produce.
	Confirm bool `json:"confirm,omitempty"`
}

// AdoptPreviewResult is the side-effect-free assessment returned by adopt
// preview (ADR 0019 §14/§14.1).
type AdoptPreviewResult struct {
	OK     bool   `json:"ok"`
	Origin string `json:"origin"` // drift | no_baseline | inconsistent
	// InconsistentReason is the bounded ADR 0019 §11.2.1 reason accompanying
	// Origin == "inconsistent" (e.g. "marker_missing", "snapshot_unreadable").
	// Empty for every other origin.
	InconsistentReason    string            `json:"inconsistent_reason,omitempty"`
	ObservedDigest        string            `json:"observed_digest"`
	BaseVersion           string            `json:"base_version,omitempty"`
	CandidateVersion      string            `json:"candidate_version,omitempty"`
	RestartRequired       bool              `json:"restart_required"`
	ValidationErrors      []string          `json:"validation_errors,omitempty"`
	Lint                  []validationError `json:"lint,omitempty"`
	Diff                  *ConfigDiff       `json:"diff,omitempty"`
	DiffUnavailableReason string            `json:"diff_unavailable_reason,omitempty"`
}

// ReloadSnapshot is the legacy admin-package view of the most recent reload
// outcome. It is kept for one compatibility release; new code should use
// server.ReloadResult.
type ReloadSnapshot struct {
	OK       bool          `json:"ok"`
	TimedOut bool          `json:"timed_out"`
	Duration time.Duration `json:"duration"`
	At       time.Time     `json:"at"`
	Error    string        `json:"error,omitempty"`
}

// Server is the admin HTTP listener.
type Server struct {
	cfg      config.AdminConfig
	log      *slog.Logger
	deps     Deps
	hist     *history
	hub      *Hub
	limiter  *adminLimiter
	timeline *eventHistory
	audit    *auditLog
	health   *consoleHealth
	quit     chan struct{}
	httpd    *http.Server
	// auth holds the immutable, atomically-installed authentication snapshot
	// (H-01). It pairs the effective admin config, the built RBAC policy, and
	// the derived authoritative mode so middleware observes a single,
	// internally-consistent view via one atomic pointer load. Replaces the
	// previous three independent stores (policy, liveCfg, rbacEnabled) that
	// could interleave and expose a transient anonymous/legacy window during an
	// RBAC transition.
	authState atomic.Pointer[authSnapshot]
	// authGen is a monotonic generation counter stamped into each installed
	// snapshot so transitions can be correlated in logs and tests.
	authGen atomic.Uint64
	// applyMu serializes config writes (raw apply, structured patch apply, and
	// history rollback) so optimistic-concurrency checks and the write they guard
	// are atomic, closing the read-modify-write race between concurrent edits
	// (P2-12).
	applyMu sync.Mutex
}

// RecordManagedApplyOutcome records the terminal async outcome of a managed
// apply in the audit log (H-05). The actor is the original request identity
// captured by the caller; when no identity is available it falls back to
// "system".
func (s *Server) RecordManagedApplyOutcome(ctx ApplyRequestContext, o ManagedApplyOutcome) {
	if s.audit == nil {
		return
	}
	result := "success"
	if !o.OK {
		// C4 (N-08): always record "failure" when the apply did not succeed,
		// regardless of whether the previous configuration was restored.
		// Restoration is a recovery action, not an indication that the apply
		// succeeded. The restored and restore_error fields in the detail
		// string provide the full picture.
		result = "failure"
	}
	// AC-04: the terminal audit detail carries only redacted, low-cardinality
	// metadata — apply ID, mode, outcome, versions and restoration state. It
	// never includes raw config, secret values or the token digest.
	detail := fmt.Sprintf("outcome=%s restored=%t", o.Outcome, o.Restored)
	if o.ID != "" {
		detail = "apply_id=" + o.ID + " " + detail
	}
	if o.Mode != "" {
		detail += " mode=" + o.Mode
	}
	if o.RestoreError != "" {
		detail += " restore_error=" + o.RestoreError
	}
	if o.FinalDiskVersion != "" {
		detail += " final_disk_version=" + o.FinalDiskVersion
	}
	if o.FinalServingVersion != "" {
		detail += " final_serving_version=" + o.FinalServingVersion
	}
	actor := ctx.Actor
	if actor == "" {
		actor = "system"
	}
	s.audit.record(AuditEvent{
		Time:      time.Now().UTC(),
		Actor:     actor,
		TokenID:   ctx.TokenID,
		Operation: finalizedAuditOperation(ctx.Operation),
		Resource:  "config",
		Result:    result,
		Detail:    detail,
		SourceIP:  ctx.SourceIP,
	})
}

// finalizedAuditOperation maps a managed ApplyOperation to its terminal audit
// operation name (AC-04). Terminal audit operations are operation-specific so
// a reviewer can distinguish a finalized patch from a finalized rollback
// without parsing free-text detail. An empty or unknown operation falls back
// to the generic config.apply.finalized name.
func finalizedAuditOperation(op ApplyOperation) string {
	switch op {
	case ApplyOperationConfigApply:
		return "config.apply.finalized"
	case ApplyOperationPatchApply:
		return "config.patch.finalized"
	case ApplyOperationLegacyRaw:
		return "config.raw.finalized"
	case ApplyOperationSettings:
		return "config.settings.finalized"
	case ApplyOperationRollback:
		return "config.rollback.finalized"
	default:
		return "config.apply.finalized"
	}
}

// New builds an admin Server from config. It returns nil when admin is
// disabled so callers can skip starting it.
func New(cfg config.AdminConfig, log *slog.Logger, deps Deps) *Server {
	if !cfg.Enabled {
		return nil
	}
	s := &Server{
		cfg:      cfg,
		log:      log,
		deps:     deps,
		hist:     newHistory(cfg.HistoryDir, cfg.HistoryKeep),
		hub:      newHub(),
		limiter:  newAdminLimiter(log, cfg.RateLimitReadPerMin, cfg.RateLimitWritePerMin, cfg.RateLimitApplyPerMin, cfg.MaxEventConns),
		timeline: newEventHistory(timelineCap),
		audit:    newAuditLogWithSink(auditCap, cfg.AuditLogFile, cfg.AuditLogRotateMaxMB, cfg.AuditLogRotateKeep, log),
		health:   newConsoleHealth(),
		quit:     make(chan struct{}),
	}
	s.httpd = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.installAuth(cfg, nil)
	return s
}

// Handler returns the fully-wired admin HTTP handler: the route mux with the
// single authentication/authorization chokepoint applied. It is the exact
// handler installed on the listener by New (and served by Run), exposed so the
// composition root and cross-package integration tests can exercise the real
// route stack — including route authorization — without reconstructing it or
// bypassing the auth chokepoint.
func (s *Server) Handler() http.Handler {
	return s.routes()
}

// Run starts the admin listener and shuts it down when ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.log.Info("admin listener started", "addr", s.cfg.Listen, "auth", s.currentAdminConfig().Token != "")
	// The admin API grants full read/write control of the running server. It is
	// designed for single-operator, loopback-bound use. Binding to a routable
	// address without an external firewall, VPN, or mTLS layer is unsafe.
	if !adminIsLoopback(s.cfg.Listen) {
		security := "single shared bearer token; full read/write access"
		if pol := s.currentPolicy(); pol != nil && pol.Enabled() {
			security = "RBAC enabled; full read/write access"
		}
		s.log.Warn("admin listener bound to a non-loopback address — restrict access with firewall rules or a private network",
			"addr", s.cfg.Listen,
			"security", security)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpd.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		close(s.quit)
		s.hub.Close()
		if s.audit != nil {
			_ = s.audit.Close()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpd.Shutdown(shutdownCtx)
	case err := <-errCh:
		close(s.quit)
		s.hub.Close()
		if s.audit != nil {
			_ = s.audit.Close()
		}
		return err
	}
}

// auth enforces authentication. When RBAC is enabled (a policy is installed
// with Enabled() == true) it delegates to authWithRBAC which handles named
// principals and stores the Identity in the request context. Otherwise it
// falls back to legacy single-token auth. Routes always call s.auth(...) so
// this is the single chokepoint — there is no way to bypass the check.
func (s *Server) auth(next http.Handler) http.Handler {
	return s.authWithRBAC(next)
}

// handleHealthz reports process liveness.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMetrics serves Prometheus metrics, or 404 when metrics are disabled.
func (s *Server) handleMetrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Metrics == nil {
			http.NotFound(w, r)
			return
		}
		s.deps.Metrics.ServeHTTP(w, r)
	})
}

// handlePurge clears the response cache. POST only. An optional ?key=<key>
// removes a single entry; otherwise the whole cache is purged.
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.Cache == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "cache disabled"})
		return
	}
	if key := r.URL.Query().Get("key"); key != "" {
		s.deps.Cache.Delete(key)
		writeJSON(w, http.StatusOK, map[string]string{"status": "purged", "key": key})
		return
	}
	s.deps.Cache.Purge()
	writeJSON(w, http.StatusOK, map[string]string{"status": "purged"})
}

// handleReload triggers a configuration reload. POST only. It returns 202
// when the reload is successfully enqueued and 503 when the enqueue times out
// or is otherwise rejected by the runtime coordinator.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.Reload == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	if err := s.deps.Reload(); err != nil {
		s.recordAudit(r, "config.reload", "config", "failure", err.Error())
		http.Error(w, "503 Service Unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.recordAudit(r, "config.reload", "config", "success", "reload triggered via admin API")
	// Notify SSE subscribers and the timeline so the Console updates live.
	s.emit("config", "reload", "info", "Configuration reload triggered.")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "reload triggered"})
}

// handleRoot serves the dependency-free configuration GUI at the admin root.
// It is registered only when the Console v2 SPA is not the active UI (lean
// build or an explicitly disabled console); the SPA owns the root otherwise.
// Any non-root path that reaches this catch-all handler is a 404.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.serveConfigPage(w)
}

// handleConfigPage always serves the configuration GUI page, independent of the
// console setting, so the editor stays reachable at /config and /ui.
func (s *Server) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	s.serveConfigPage(w)
}

// serveConfigPage writes the static configuration GUI page. The page fetches
// data from the API endpoints, sending the admin token when one is required.
func (s *Server) serveConfigPage(w http.ResponseWriter) {
	s.writeSecurityHeadersLegacy(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, configUIPage)
}

// handleStats serves the runtime dashboard metrics as JSON for the console. It
// is always registered (and cheap) so it is useful for scripts even when the
// console UI is compiled out. When no Stats hook is wired it returns an
// "unavailable" snapshot rather than an error.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.deps.Stats == nil {
		writeJSON(w, http.StatusOK, observability.StatsSnapshot{})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Stats())
}

// writeSecurityHeaders applies defensive HTTP headers to the admin pages.
// The Console v2 SPA is served from same-origin embedded assets only — no CDN,
// no external fonts, no inline scripts — so script-src 'self' is safe.
// The legacy config page uses inline scripts/styles and is covered by
// writeSecurityHeadersLegacy below.
func (s *Server) writeSecurityHeaders(w http.ResponseWriter) {
	s.writeSecurityHeadersNonce(w, "")
}

// writeSecurityHeadersNonce is writeSecurityHeaders with an optional per-response
// style nonce. The SPA's only inline <style> source is the CodeMirror editor,
// which stamps the advertised nonce onto the theme elements it injects, so the
// policy stays free of 'unsafe-inline'. An empty nonce yields the strict
// style-src 'self' policy used for the hashed static assets.
func (s *Server) writeSecurityHeadersNonce(w http.ResponseWriter, styleNonce string) {
	h := w.Header()
	h.Set("Content-Security-Policy", securityCSP(styleNonce))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
}

// securityCSP builds the Console v2 Content-Security-Policy. When styleNonce is
// non-empty the style-src gains a 'nonce-…' source for editor-injected styles.
func securityCSP(styleNonce string) string {
	style := "style-src 'self'"
	if styleNonce != "" {
		style = "style-src 'self' 'nonce-" + styleNonce + "'"
	}
	return "default-src 'self'; " + style + "; " +
		"script-src 'self'; img-src 'self' data:; " +
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; " +
		"report-uri /csp-violation-report"
}

// writeSecurityHeadersLegacy applies the CSP for the legacy config/console v1
// page which contains first-party embedded inline scripts and styles.
func (s *Server) writeSecurityHeadersLegacy(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy",
		"default-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"script-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
			"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
}

// settings is the curated subset of configuration exposed by the simple GUI.
// All fields are strings/bools so they round-trip cleanly through the form.
type settings struct {
	LogLevel           string `json:"log_level"`
	LogFormat          string `json:"log_format"`
	ShutdownTimeout    string `json:"shutdown_timeout"`
	CacheEnabled       bool   `json:"cache_enabled"`
	CacheDefaultTTL    string `json:"cache_default_ttl"`
	CacheMemoryMaxSize string `json:"cache_memory_max_size"`
	AdminListen        string `json:"admin_listen"`
}

// extractSettings reads the curated subset from a parsed configuration.
func extractSettings(c *config.Config) settings {
	return settings{
		LogLevel:           c.Global.LogLevel,
		LogFormat:          c.Global.LogFormat,
		ShutdownTimeout:    c.Global.ShutdownTimeout.Std().String(),
		CacheEnabled:       c.Cache.Enabled,
		CacheDefaultTTL:    c.Cache.DefaultTTL.Std().String(),
		CacheMemoryMaxSize: string(must(c.Cache.MemoryMaxSize.MarshalText())),
		AdminListen:        c.Admin.Listen,
	}
}

// applySettings writes the curated subset onto a parsed configuration, parsing
// the string-typed durations and sizes. It returns an error on malformed input.
func applySettings(c *config.Config, in settings) error {
	c.Global.LogLevel = in.LogLevel
	c.Global.LogFormat = in.LogFormat
	if err := c.Global.ShutdownTimeout.UnmarshalText([]byte(in.ShutdownTimeout)); err != nil {
		return fmt.Errorf("shutdown_timeout: %w", err)
	}
	c.Cache.Enabled = in.CacheEnabled
	if err := c.Cache.DefaultTTL.UnmarshalText([]byte(in.CacheDefaultTTL)); err != nil {
		return fmt.Errorf("cache_default_ttl: %w", err)
	}
	if err := c.Cache.MemoryMaxSize.UnmarshalText([]byte(in.CacheMemoryMaxSize)); err != nil {
		return fmt.Errorf("cache_memory_max_size: %w", err)
	}
	c.Admin.Listen = in.AdminListen
	return nil
}

func must(b []byte, _ error) []byte { return b }

// handleConfigGet returns metadata, the raw config text, and the curated
// settings subset for the GUI.
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	adminCfg := s.currentAdminConfig()
	resp := map[string]any{
		"product":        s.deps.Product,
		"version":        s.deps.Version,
		"path":           s.deps.ConfigPath,
		"authRequired":   adminCfg.Token != "",
		"rawEditable":    s.deps.ApplyConfigRaw != nil || s.deps.WriteConfigRaw != nil,
		"formEditable":   s.deps.LoadConfig != nil && (s.deps.ApplyConfig != nil || s.deps.SaveConfig != nil),
		"consoleEnabled": consoleV2Compiled && adminCfg.ConsoleEnabled(),
	}
	if s.deps.ReadConfigRaw != nil || s.deps.LoadConfig != nil {
		state, err := s.currentWriteState(false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if s.deps.ReadConfigRaw != nil {
			resp["raw"] = string(state.Raw)
		}
		resp["settings"] = extractSettings(state.Config)
		// base_version always identifies the canonical unresolved configuration
		// parsed from the exact raw bytes returned above.
		resp["base_version"] = state.Version
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleConfigRaw persists raw configuration text (advanced editor). It
// validates before writing so an invalid edit never causes downtime.
// NOTE: This is a legacy endpoint; prefer /api/config/apply. This handler now
// uses the same applyMu lock and optimistic-version contract as v2 apply to
// close the read-modify-write race with concurrent writes (P2-12).
func (s *Server) handleConfigRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.Header().Set("Allow", "POST, PUT")
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.denyIfFileOwned(w, r, string(ApplyOperationLegacyRaw)) {
		return
	}
	if s.deps.ApplyConfigRaw == nil && s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Pre-parse the candidate once for validation and authorization. The
	// actual authorization against the current config happens inside applyMu so
	// a concurrent admin change cannot invalidate the decision (N-02).
	next, parseErr := config.Parse(body)
	if parseErr != nil {
		// Invalid candidate; fall through to the normal validation path below
		// which reports the parse error without persisting anything.
		next = nil
	}

	// Serialize with the apply path so concurrent writes cannot interleave (P2-12).
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	// M-06: Fail-closed prerequisite check for mutation endpoints. The raw
	// endpoint only needs the baseline config (for the admin:manage guard,
	// optimistic-concurrency version, and reachability confirmation); the raw
	// bytes are used solely for best-effort history and must not hard-require a
	// wired ReadConfigRaw. Finding 11's mandatory-raw contract is scoped to the
	// structured-patch history/diff caller, which is handled separately.
	state, err := s.currentWriteState(false)
	if err != nil {
		s.recordAudit(r, "config.raw", "config", "failure", "rejected: "+err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cannot load current configuration: " + err.Error()})
		return
	}
	reqCtx := applyRequestContext(r, ApplyOperationLegacyRaw)
	s.bindManagedApplyDeadline(&reqCtx)
	reqCtx.Baseline = &state
	currentEffective := bindEffectiveBaseline(s, &reqCtx, state.Config)
	var effectiveNext *config.Candidate
	if next != nil {
		opCtx, cancel := managedApplyPrePersistenceContext(reqCtx, r.Context())
		defer cancel()
		effectiveNext, _ = prepareMutationCandidateContext(opCtx, &reqCtx, next)
	}

	// Object-level guard: a caller with config:apply still must hold admin:manage
	// to change anything under [admin]. This check runs inside the write lock so
	// the current config cannot change between authorization and write (N-02).
	if effectiveNext != nil && !s.authorizeConfigTransition(w, r, "config.raw", currentEffective, effectiveNext.Effective) {
		return
	}

	// Optimistic concurrency: reject stale writes. An empty base_version skips
	// the check (explicit force-apply). The version basis matches /api/config/apply
	// so both editors can interoperate.
	if baseVersion := r.URL.Query().Get("base_version"); baseVersion != "" {
		if baseVersion != state.Version {
			s.recordAudit(r, "config.raw", "config", "failure", "rejected: base version stale (concurrent change)")
			writeJSON(w, http.StatusConflict, conflictResponse{
				OK:             false,
				Conflict:       true,
				Message:        "The configuration changed since this edit was prepared; reload and try again.",
				CurrentVersion: state.Version,
			})
			return
		}
	}

	// Self-lockout guard (finding 9): a raw edit through the legacy endpoint can
	// also change how the current operator reaches the console. Require the same
	// explicit confirmation used by /api/config/apply unless confirm_admin=true.
	if effectiveNext != nil && r.URL.Query().Get("confirm_admin") != "true" {
		id, _ := rbacIdentityFromRequest(r)
		if changes := s.reachabilityChanges(currentEffective, effectiveNext.Effective, id); len(changes) > 0 {
			s.recordAudit(r, "config.raw", "config", "failure", "rejected: admin-reachability change needs confirmation")
			writeJSON(w, http.StatusConflict, adminGuardResponse{
				OK:          false,
				AdminChange: true,
				Message:     "This change affects how you reach the admin console; re-apply with confirmation to proceed.",
				Changes:     changes,
			})
			return
		}
	}

	// Capture the pre-write raw for the history snapshot BEFORE the write; after
	// WriteConfigRaw the file already holds the new configuration, so reading it
	// then would snapshot the new config instead of the prior one.
	prevRaw := state.Raw
	if s.deps.ApplyConfigRaw != nil {
		result, applyErr := s.deps.ApplyConfigRaw(reqCtx, body, "hot")
		if applyErr != nil {
			code := configApplyErrorStatus(result, applyErr)
			s.recordAudit(r, "config.raw", "config", "failure", "coordinator error: "+applyErr.Error())
			writeJSON(w, code, result)
			return
		}
		status := configApplyResultStatus(result)
		if status != http.StatusOK {
			if result.TimedOutPhase != "" {
				// AC-08 / defect 9: name the timed-out preflight phase in the audit
				// trail before writing the 504; nothing was persisted.
				s.recordTimeoutAudit(r, reqCtx.Operation, result)
			}
			writeJSON(w, status, result)
			return
		}
		// AC-05: when the managed coordinator records history at
		// terminalization, the handler must NOT snapshot here — doing so would
		// double-record and could snapshot at a provisional 202 before the
		// terminal outcome is known. Handlers keep recording eagerly only when
		// no managed terminal writer is wired (unit-test fakes).
		if !s.deps.ManagedHistoryActive {
			s.recordHistory(prevRaw)
		}
		s.recordAudit(r, "config.raw", "config", "success", "configuration validated and saved")
		writeJSON(w, http.StatusOK, ConfigMutationResponse{Status: "saved", ConfigApplyResult: result})
		return
	}
	if err := s.deps.WriteConfigRaw(body); err != nil {
		s.recordAudit(r, "config.raw", "config", "failure", "rejected: invalid configuration")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.recordHistory(prevRaw)
	s.recordAudit(r, "config.raw", "config", "success", "configuration validated and saved; live runtime reloading")
	// Finding 10: return the version of what was just persisted, not the pre-write
	// version, so a client can reuse it as the next optimistic-concurrency token
	// without a spurious 409. Reload the authoritative post-write state.
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "version": s.postWriteVersion(state.Version)})
}

// handleConfigSettings applies the curated settings subset (simple form),
// re-serializing the whole configuration to TOML.
// NOTE: This is a legacy endpoint; prefer /api/config/patch + /api/config/patch/apply.
// This handler now uses the same applyMu lock and optimistic-version contract as v2 apply
// to close the read-modify-write race with concurrent writes (P2-12).
func (s *Server) handleConfigSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.Header().Set("Allow", "POST, PUT")
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.denyIfFileOwned(w, r, string(ApplyOperationSettings)) {
		return
	}
	if s.deps.LoadConfig == nil || (s.deps.ApplyConfig == nil && s.deps.SaveConfig == nil) {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var in settings
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Serialize with the apply path so the read-modify-write is atomic.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	// M-06: Fail-closed prerequisite check for mutation endpoints. As with the
	// raw endpoint, the settings form only needs the baseline config; the raw
	// bytes are best-effort history, so requireRaw stays false here.
	state, err := s.currentWriteState(false)
	if err != nil {
		s.recordAudit(r, "config.settings", "config", "failure", "rejected: "+err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cannot load current configuration: " + err.Error()})
		return
	}
	reqCtx := applyRequestContext(r, ApplyOperationSettings)
	s.bindManagedApplyDeadline(&reqCtx)
	reqCtx.Baseline = &state
	currentEffective := bindEffectiveBaseline(s, &reqCtx, state.Config)
	cfg := state.Config

	// Optimistic concurrency: reject stale writes. An empty base_version skips
	// the check (explicit force-apply). The version basis matches /api/config/patch
	// so both editors can interoperate.
	if baseVersion := r.URL.Query().Get("base_version"); baseVersion != "" {
		if baseVersion != state.Version {
			s.recordAudit(r, "config.settings", "config", "failure", "rejected: base version stale (concurrent change)")
			writeJSON(w, http.StatusConflict, conflictResponse{
				OK:             false,
				Conflict:       true,
				Message:        "The configuration changed since this edit was prepared; reload and try again.",
				CurrentVersion: state.Version,
			})
			return
		}
	}

	// C-01: never mutate the loaded baseline in place. LoadConfig may return a
	// shared/cached pointer; if we mutated it and then reloaded for
	// authorization, "current" and "candidate" would alias the same object and
	// compare equal, silently skipping the admin:manage guard. Build an
	// independent candidate from a deep clone, mutate the clone, and authorize
	// the clone against the pristine baseline WITHOUT reloading.
	candidate, cerr := cfg.Clone()
	if cerr != nil {
		s.recordAudit(r, "config.settings", "config", "failure", "rejected: cannot clone current config: "+cerr.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cannot prepare configuration candidate: " + cerr.Error()})
		return
	}
	if err := applySettings(candidate, in); err != nil {
		s.recordAudit(r, "config.settings", "config", "failure", "rejected: invalid settings")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	opCtx, cancel := managedApplyPrePersistenceContext(reqCtx, r.Context())
	defer cancel()
	effectiveCandidate, candidateErr := prepareMutationCandidateContext(opCtx, &reqCtx, candidate)
	if candidateErr != nil {
		// A resolution deadline is a pre-persistence timeout (504); a
		// cancellation is a client abort (408). Only a genuine invalid value
		// falls through to the 400 response. Settings is always hot mode.
		if s.writeCandidatePreparationFailure(w, r, reqCtx, "hot", candidateErr) {
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": candidateErr.Error()})
		return
	}
	// Object-level guard: the settings form can mutate [admin].listen, so any
	// change in the [admin] subtree requires admin:manage. Authorize the
	// candidate against the immutable baseline (no reload) to defeat aliasing.
	if !s.authorizeConfigTransition(w, r, "config.settings", currentEffective, effectiveCandidate.Effective) {
		return
	}
	// Self-lockout guard (finding 9): the settings form can move the admin
	// listener or change credentials. Require the same explicit confirmation
	// used by /api/config/apply unless confirm_admin=true.
	if r.URL.Query().Get("confirm_admin") != "true" {
		id, _ := rbacIdentityFromRequest(r)
		if changes := s.reachabilityChanges(currentEffective, effectiveCandidate.Effective, id); len(changes) > 0 {
			s.recordAudit(r, "config.settings", "config", "failure", "rejected: admin-reachability change needs confirmation")
			writeJSON(w, http.StatusConflict, adminGuardResponse{
				OK:          false,
				AdminChange: true,
				Message:     "This change affects how you reach the admin console; re-apply with confirmation to proceed.",
				Changes:     changes,
			})
			return
		}
	}
	// Capture the pre-write raw for the history snapshot BEFORE SaveConfig.
	prevRaw := state.Raw
	if s.deps.ApplyConfig != nil {
		result, applyErr := s.deps.ApplyConfig(reqCtx, candidate, "hot")
		if applyErr != nil {
			code := configApplyErrorStatus(result, applyErr)
			s.recordAudit(r, "config.settings", "config", "failure", "coordinator error: "+applyErr.Error())
			writeJSON(w, code, result)
			return
		}
		status := configApplyResultStatus(result)
		if status != http.StatusOK {
			if result.TimedOutPhase != "" {
				// AC-08 / defect 9: name the timed-out preflight phase in the audit
				// trail before writing the 504; nothing was persisted.
				s.recordTimeoutAudit(r, reqCtx.Operation, result)
			}
			writeJSON(w, status, result)
			return
		}
		// AC-05: skip eager handler-side history when the managed coordinator
		// records it at terminalization (see handleConfigRaw for rationale).
		if !s.deps.ManagedHistoryActive {
			s.recordHistory(prevRaw)
		}
		s.recordAudit(r, "config.settings", "config", "success", "settings applied and saved")
		writeJSON(w, http.StatusOK, ConfigMutationResponse{Status: "saved", ConfigApplyResult: result})
		return
	}
	if err := s.deps.SaveConfig(candidate); err != nil {
		s.recordAudit(r, "config.settings", "config", "failure", "rejected: cannot save config")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.recordHistory(prevRaw)
	s.recordAudit(r, "config.settings", "config", "success", "settings applied and saved; live runtime reloading")
	// Finding 10: return the post-write version so it is a valid next base token.
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "version": s.postWriteVersion(state.Version)})
}

// postWriteVersion returns the canonical version of the configuration currently
// on disk after a successful write, so callers receive a token that identifies
// what was just saved rather than the pre-write version (finding 10). If the
// authoritative post-write state cannot be re-read it falls back to the
// supplied pre-write version rather than failing an already-committed write.
func (s *Server) postWriteVersion(fallback string) string {
	fresh, err := s.currentWriteState(false)
	if err != nil {
		return fallback
	}
	return fresh.Version
}

// CurrentWriteState is retained as an alias for the raw-first mutation
// baseline used by mutation handlers.
type CurrentWriteState = MutationBaseline

// currentWriteState loads the current configuration state with fail-closed
// semantics: it returns an error if LoadConfig is unavailable, fails, returns
// nil config, or if marshaling fails. The raw snapshot is only required when
// the caller needs it (e.g., for history recording).
func (s *Server) currentWriteState(requireRaw bool) (CurrentWriteState, error) {
	// File-backed production paths must read the bytes once and parse those
	// exact bytes. Calling LoadConfig and ReadConfigRaw independently can pair a
	// parsed configuration from one filesystem generation with raw bytes from
	// another.
	if s.deps.ReadConfigRaw != nil {
		raw, err := s.deps.ReadConfigRaw()
		if err != nil {
			return CurrentWriteState{}, fmt.Errorf("cannot read config raw: %w", err)
		}
		cur, err := config.Parse(raw)
		if err != nil {
			return CurrentWriteState{}, fmt.Errorf("cannot parse config raw: %w", err)
		}
		return CurrentWriteState{
			Raw:     raw,
			Digest:  sha256.Sum256(raw),
			Version: server.CanonicalVersion(cur),
			Config:  cur,
			Exists:  true,
		}, nil
	}

	if requireRaw {
		return CurrentWriteState{}, errors.New("system unavailable: raw config reader not wired")
	}
	if s.deps.LoadConfig == nil {
		return CurrentWriteState{}, errors.New("system unavailable: config loader not wired")
	}
	cur, err := s.deps.LoadConfig()
	if err != nil {
		return CurrentWriteState{}, fmt.Errorf("cannot load config: %w", err)
	}
	if cur == nil {
		return CurrentWriteState{}, errors.New("config loader returned nil")
	}
	raw, err := config.Marshal(cur)
	if err != nil {
		return CurrentWriteState{}, fmt.Errorf("cannot marshal config: %w", err)
	}
	return CurrentWriteState{
		Raw:     raw,
		Digest:  sha256.Sum256(raw),
		Version: server.CanonicalVersion(cur),
		Config:  cur,
		Exists:  true,
	}, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Readiness is a small helper the composition root can use to expose a boolean
// readiness flag flipped once listeners are bound.
type Readiness struct{ ready atomic.Bool }

// Set marks readiness state.
func (r *Readiness) Set(v bool) { r.ready.Store(v) }

// Ready reports the current readiness state.
func (r *Readiness) Ready() bool { return r.ready.Load() }

// handleConsoleV2 serves the embedded Console v2 SPA bundle with the
// appropriate security headers and index.html fallback for client-side routing.
func (s *Server) handleConsoleV2() http.Handler {
	fsys, err := fs.Sub(consoleV2Assets(), "assets/dist")
	if err != nil {
		s.log.Error("console v2: failed to open embedded filesystem", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Console v2 unavailable", http.StatusInternalServerError)
		})
	}
	indexHTML, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		s.log.Error("console v2: missing index.html in embedded bundle", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Console v2 unavailable", http.StatusInternalServerError)
		})
	}
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hashed, content-addressed assets (JS/CSS/images) are served verbatim
		// under the strict static policy. Everything else — the bare root and
		// any unknown path — is a client-side route, so we serve the SPA shell
		// with a fresh per-response nonce and let react-router resolve it.
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" && name != "index.html" {
			if f, ferr := fsys.Open(name); ferr == nil {
				_ = f.Close()
				s.writeSecurityHeaders(w)
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		s.serveConsoleV2Index(w, indexHTML)
	})
}

// serveConsoleV2Index writes the SPA shell with a fresh per-response style
// nonce. CodeMirror injects its theme as inline <style> elements, so the
// document CSP advertises a nonce that the editor stamps onto them; the SPA
// reads it from the injected <meta name="csp-nonce"> tag. The shell is marked
// no-store so each load gets a distinct nonce.
func (s *Server) serveConsoleV2Index(w http.ResponseWriter, indexHTML []byte) {
	nonce := newStyleNonce()
	body := bytes.Replace(indexHTML,
		[]byte("<head>"),
		[]byte("<head>\n<meta name=\"csp-nonce\" content=\""+nonce+"\" />"),
		1)
	s.writeSecurityHeadersNonce(w, nonce)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// newStyleNonce returns a base64-encoded 128-bit random CSP nonce.
func newStyleNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail; if it ever does, returning an empty
		// nonce degrades to the strict style-src 'self' policy (editor styles
		// blocked) rather than panicking the request.
		return ""
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

// adminIsLoopback reports whether addr (host:port or bare host) resolves to a
// loopback address. It is used to warn when the admin listener is bound to a
// routable address, since the admin API uses a single shared bearer token with
// no RBAC and is designed for local single-operator use.
func adminIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
