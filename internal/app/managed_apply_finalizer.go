// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/admin"
	"jul/internal/observability"
	"jul/internal/server"
)

// managedApplyFinalizer is the single terminal-finalization orchestrator for a
// managed configuration apply (WS02 §3.7). Every persisted mutation's terminal
// callback is routed through Finalize so the transaction is CLAIMED before any
// history/audit/metrics/ledger side effect runs, and each side effect runs
// exactly once per apply ID. Centralizing the terminal path here replaces the
// composition root's former inline completion callback, fixing the ordering
// defects called out in WS02 §3.2: history no longer runs before the
// finalization claim (defect 2), the terminal-ledger Complete error is no
// longer ignored (defect 4), a claim error fails closed with an explicit
// FinalizationError instead of silently proceeding (defect 5), and the
// FinalizationError is reliably threaded back onto the terminal result
// (defect 6). The finalizer holds only concurrency-safe, repository-native
// surfaces (the terminal-ledger registry, the admin server, the private-registry
// metrics, and the process-lifetime latest-outcome pointer), so it introduces
// no new unsynchronized shared state.
type managedApplyFinalizer struct {
	registry *admin.ManagedApplyRegistry
	admin    *admin.Server
	metrics  *observability.Metrics
	log      *slog.Logger

	// latest, latestSeq, and latestMu implement the AC-04/C3 monotonic
	// high-water guard for the singular latest-outcome pointer surfaced on the
	// runtime overview. An older terminal result finishing later still records
	// its own per-ID ledger record and audit/metrics, but does not replace the
	// latest pointer.
	latest    *atomic.Pointer[admin.ManagedApplyOutcome]
	latestSeq *atomic.Uint64
	latestMu  *sync.Mutex

	// setAdvisory publishes the ADVISORY, non-readiness managed-apply
	// finalization-health state surfaced on the runtime overview as
	// managed_apply_finalization (WS02 §3.9). It is called on EVERY
	// terminalization: healthy after a clean finalize — which clears any prior
	// degradation — and unhealthy (carrying the apply ID and a bounded detail)
	// on a claim failure, a terminal-ledger completion failure, or a
	// configuration-history snapshot/metadata failure. It carries only bounded,
	// low-cardinality metadata (never raw TOML, secrets, or actor tokens) and
	// MUST NOT gate readiness. Nil in context-free/unit callers that do not
	// expose advisory health.
	setAdvisory func(admin.ManagedApplyAdvisory)
}

// Finalize performs the single terminal finalization for a managed apply. It
// claims the transaction, writes the trusted configuration-history snapshot,
// records the terminal audit/metrics/ledger side effects exactly once, and
// returns the ManagedApplyFinalization provenance threaded back onto the
// terminal result. A duplicate terminal callback for an already-finalized ID is
// deduplicated through the ledger claim and returns the recorded provenance
// without repeating any side effect. Finalize never fails an already-committed
// apply: a claim or ledger error is surfaced through the returned
// FinalizationError, the advisory health flag, and the finalization-error
// metric rather than rolling back the persisted configuration.
func (f *managedApplyFinalizer) Finalize(completion admin.ManagedApplyCompletion) (fin admin.ManagedApplyFinalization) {
	result := completion.Result
	applyID := result.ApplyID
	if applyID == "" && result.Reload != nil {
		applyID = result.Reload.ID
	}

	// Echo any post-persistence finalization error already carried on the
	// serialized result (e.g. a pending-registration failure threaded on by the
	// coordinator) so it is preserved through the terminal ledger/overview.
	fin.FinalizationError = result.FinalizationError

	baseRecord := admin.ManagedApplyRecord{
		ID:           applyID,
		Operation:    completion.Context.Operation,
		StartedAt:    completion.Context.StartedAt,
		Deadline:     completion.Context.Deadline,
		Result:       result,
		OwnerTokenID: completion.Context.TokenID,
	}

	// Defect 2/5: claim the single terminal finalization BEFORE any history or
	// audit side effect. A claim error fails closed — the finalization error is
	// made explicit and reported — instead of silently duplicating history.
	claimed := true
	if applyID != "" {
		ok, err := f.registry.ClaimFinalization(baseRecord)
		if err != nil {
			fin.FinalizationError = appendFinalizationError(
				fin.FinalizationError,
				"claim finalization: "+err.Error(),
			)
			f.reportFinalizationError(applyID, err)
			return fin
		}
		claimed = ok
	}
	if !claimed {
		// Exact duplicate terminal callback: return the provenance already
		// recorded for this ID without repeating any side effect. It does NOT
		// re-publish the advisory: the first (claiming) finalization already
		// published the authoritative finalization-health state for this ID.
		if existing, ok := f.registry.Get(applyID); ok {
			return admin.ManagedApplyFinalization{
				HistorySnapshotID: existing.HistorySnapshotID,
				HistoryError:      existing.HistoryError,
				FinalizationError: existing.FinalizationError,
			}
		}
		return fin
	}

	// AC-05: perform the trusted configuration-history write AFTER the claim so
	// the snapshot side effect happens exactly once per apply ID. previousRaw is
	// sensitive and is consumed only here — never logged or retained.
	snapshotID, historyErr := f.admin.RecordManagedHistory(
		completion.Context,
		result,
		completion.PreviousRaw,
	)
	fin.HistorySnapshotID = snapshotID
	operation := string(completion.Context.Operation)
	if historyErr != nil {
		fin.HistoryError = historyErr.Error()
		f.observeHistory(operation, "failed")
	} else if snapshotID != "" {
		f.observeHistory(operation, "recorded")
	} else {
		f.observeHistory(operation, "skipped")
	}

	// AC-04: emit the bounded terminal metric.
	if f.metrics != nil {
		f.metrics.ObserveManagedApplyFinalized(
			operation,
			result.Mode,
			managedReloadOutcome(result),
			managedRestoredLabel(result),
		)
	}

	// Audit every unique terminal transaction with only redacted,
	// low-cardinality metadata.
	managedOutcome := projectManagedApplyOutcome(completion.Context, result, fin)
	f.admin.RecordManagedApplyOutcome(completion.Context, managedOutcome)

	// AC-02: publish the durable per-ID terminal record. Defect 4: the
	// terminal-ledger Complete error is no longer ignored — it is threaded onto
	// the FinalizationError, reported, and retried once with the error included.
	if applyID != "" {
		record := baseRecord
		record.CompletedAt = managedOutcome.CompletedAt
		record.Result = result
		record.HistorySnapshotID = fin.HistorySnapshotID
		record.HistoryError = fin.HistoryError
		record.FinalizationError = fin.FinalizationError

		if err := f.registry.Complete(record); err != nil {
			fin.FinalizationError = appendFinalizationError(
				fin.FinalizationError,
				"complete terminal ledger: "+err.Error(),
			)
			f.reportFinalizationError(applyID, err)

			// Best effort: retry once with the finalization error included so a
			// browser retrieving the exact ID still sees the failure.
			record.FinalizationError = fin.FinalizationError
			_ = f.registry.Complete(record)
		}
	}

	f.updateLatestIfNewest(result, managedOutcome)
	// WS02 §3.9: publish the advisory, non-readiness finalization-health state
	// for this terminal ID. Every terminalization publishes exactly one advisory:
	// unhealthy when any finalization or configuration-history degradation was
	// recorded (a claim failure returns earlier via reportFinalizationError; a
	// terminal-ledger completion failure and a history snapshot/metadata failure
	// are captured here), otherwise healthy — a clean finalize CLEARS any prior
	// degraded advisory. This never gates readiness.
	f.publishAdvisory(applyID, fin)
	return fin
}

// publishAdvisory reports the ADVISORY, non-readiness finalization-health state
// for a completed terminal finalization (WS02 §3.9). A healthy advisory is
// published when neither a finalization-machinery error nor a
// configuration-history degradation was recorded — clearing any prior degraded
// state — and an unhealthy advisory carrying the apply ID and a bounded detail
// otherwise. It carries only bounded metadata and never gates readiness. It is a
// no-op for context-free callers that do not expose advisory health.
func (f *managedApplyFinalizer) publishAdvisory(applyID string, fin admin.ManagedApplyFinalization) {
	if f.setAdvisory == nil {
		return
	}
	advisory := admin.ManagedApplyAdvisory{
		Healthy: true,
		At:      time.Now().UTC(),
		ApplyID: applyID,
	}
	if detail := advisoryDetail(fin); detail != "" {
		advisory.Healthy = false
		advisory.Detail = detail
	}
	f.setAdvisory(advisory)
}

// advisoryDetail returns the bounded advisory detail for a completed
// finalization, or "" when the finalization was clean. A finalization-machinery
// error (claim/complete) takes precedence; a configuration-history
// snapshot/metadata failure is also a finalization degradation (WS02 §3.9) even
// when the reload itself committed, so it is surfaced when no
// FinalizationError is present.
func advisoryDetail(fin admin.ManagedApplyFinalization) string {
	if d := strings.TrimSpace(fin.FinalizationError); d != "" {
		return d
	}
	if d := strings.TrimSpace(fin.HistoryError); d != "" {
		return "configuration history: " + d
	}
	return ""
}

// reportFinalizationError makes a terminal-finalization machinery failure
// EXPLICIT (WS02 §3.2 defect 4/5): it writes a structured error log, increments
// the finalization-error metric, and publishes the advisory, non-readiness
// admin-health detail. It carries only the bounded error message — never raw
// TOML, secrets, or actor metadata. It deliberately does NOT write a ledger
// record itself; the caller owns the best-effort terminal Complete so the ledger
// side effect stays in one place.
func (f *managedApplyFinalizer) reportFinalizationError(applyID string, err error) {
	detail := "managed apply finalization failed"
	if err != nil {
		detail = err.Error()
	}
	if f.log != nil {
		f.log.Error("managed apply terminal finalization failed",
			"apply_id", applyID,
			"error", detail,
		)
	}
	if f.metrics != nil {
		f.metrics.ObserveManagedApplyFinalizationError()
	}
	if f.setAdvisory != nil {
		f.setAdvisory(admin.ManagedApplyAdvisory{
			Healthy: false,
			At:      time.Now().UTC(),
			ApplyID: applyID,
			Detail:  detail,
		})
	}
}

// observeHistory records the bounded history-snapshot outcome metric, guarding a
// nil metrics surface for context-free callers.
func (f *managedApplyFinalizer) observeHistory(operation, result string) {
	if f.metrics != nil {
		f.metrics.ObserveManagedApplyHistory(operation, result)
	}
}

// updateLatestIfNewest advances the singular latest-outcome pointer only when
// this terminal result is the newest by monotonic sequence (AC-04). An older
// result finishing later keeps its own per-ID record but does not replace the
// latest pointer. It is a no-op for callers that do not expose the latest
// pointer.
func (f *managedApplyFinalizer) updateLatestIfNewest(result admin.ConfigApplyResult, outcome admin.ManagedApplyOutcome) {
	if f.latest == nil || f.latestSeq == nil || f.latestMu == nil {
		return
	}
	f.latestMu.Lock()
	defer f.latestMu.Unlock()
	if managedApplySeqGuard(f.latestSeq, result) {
		o := outcome
		f.latest.Store(&o)
		if outcome.ID != "" {
			f.registry.SetLatest(outcome.ID)
		}
	}
}

// appendFinalizationError preserves multiple terminal-finalization failures on a
// single FinalizationError string so no failure is swallowed. It trims the next
// fragment, drops empties, and joins successive failures with "; ".
func appendFinalizationError(existing, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	return existing + "; " + next
}

// managedReloadOutcome returns the terminal reload classification string for a
// managed-apply result, or "" for a result that submitted no reload (a committed
// stage_restart).
func managedReloadOutcome(result admin.ConfigApplyResult) string {
	if result.Reload != nil {
		return string(result.Reload.Outcome)
	}
	return ""
}

// managedRestoredLabel returns the bounded restoration label ("true", "false",
// or "n/a") for the terminal metric. Restoration is only meaningful for a
// non-committed reload; an applied_live or saved_not_live outcome — and any
// result without a reload — labels "n/a". An enqueue_failure is treated as a
// non-persisted outcome for labeling, mirroring the composition-root policy.
func managedRestoredLabel(result admin.ConfigApplyResult) string {
	outcome := managedReloadOutcome(result)
	if outcome == "" ||
		outcome == string(server.ReloadAppliedLive) ||
		outcome == string(server.ReloadSavedNotLive) {
		return "n/a"
	}
	if result.Restored {
		return "true"
	}
	return "false"
}

// projectManagedApplyOutcome projects a terminal managed-apply result and its
// finalization provenance into the admin.ManagedApplyOutcome surfaced on the
// runtime overview and terminal audit. It carries only redacted, low-cardinality
// metadata (apply ID, mode, outcome, versions, restoration state, actor, source
// IP, and the history/finalization provenance) — never raw config or secrets.
func projectManagedApplyOutcome(reqCtx admin.ApplyRequestContext, result admin.ConfigApplyResult, fin admin.ManagedApplyFinalization) admin.ManagedApplyOutcome {
	applyID := result.ApplyID
	if applyID == "" && result.Reload != nil {
		applyID = result.Reload.ID
	}
	return admin.ManagedApplyOutcome{
		ID:                  applyID,
		Mode:                result.Mode,
		OK:                  result.OK,
		Outcome:             managedReloadOutcome(result),
		Restored:            result.Restored,
		RestoreError:        result.RestoreError,
		FinalDiskVersion:    result.FinalDiskVersion,
		FinalServingVersion: result.FinalServingVersion,
		CompletedAt:         time.Now().UTC(),
		Actor:               reqCtx.Actor,
		SourceIP:            reqCtx.SourceIP,
		// AC-14: surface the configuration-history finalization provenance
		// (threaded separately from the serialized ConfigApplyResult) so the
		// Console can render a degradation banner independent of the reload
		// success/failure.
		HistorySnapshotID: fin.HistorySnapshotID,
		HistoryError:      fin.HistoryError,
		FinalizationError: fin.FinalizationError,
	}
}
