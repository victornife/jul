# ADR 0013 — Managed-apply terminal ledger, exactly-once finalization, and audit-closure product defaults

- **Status:** Accepted — audit-closure implementation baseline (2026-07-24)
- **Date:** 2026-07-24
- **Deciders:** Jul.IA maintainers
- **Applies to:** configuration mutation lifecycle, managed apply coordinator, terminal result retention, history snapshots, planned-restart promotion, admin Console correlation
- **Source:** Configuration audit closure plan (post-Console reconciliation commit `427e75d`)

## Context

The configuration mutation path produces one terminal result after restoration,
but the completion side effects (history snapshot, audit event, metrics, latest
pointer, health/degradation state) are recorded separately and can run after the
coordinator has already cleared its in-flight state and closed the finalization
gate. The composition-root callback also applies the high-water sequence guard
*before* metrics and audit, so a legitimate but older terminal result can be
discarded entirely rather than merely being prevented from replacing the latest
pointer.

Four product choices required to close the audit were not explicitly defined in
the repository. Rather than leave them implicit in code, they are recorded here
so they are deliberate and reviewable.

## Decision

### 1. Single authoritative terminal object

The same terminal object drives history, audit, metrics, health, latest-result
state, and the Console. No downstream system independently reconstructs the
outcome. A bounded managed-apply ledger retains terminal results per operation
identity so a browser can retrieve the exact terminal result of any recent
accepted apply regardless of later transactions.

### 2. Product defaults (conservative)

1. **Legacy configuration write endpoints** remain supported for one
   compatibility release. They must behave safely and truthfully until formally
   deprecated and removed. Uncorrelated legacy results must never claim
   definitive live success.

2. **Terminal apply-result retention:** results are retained in memory for at
   least 512 terminal transactions or one hour, whichever retains more useful
   recent results. **Pending transactions are never evicted.**

3. **`applied_degraded` is a committed configuration change.** It receives a
   history snapshot and a successful-but-degraded terminal audit result.

4. **A restoration failure creates an emergency recovery history snapshot**
   containing the exact pre-apply configuration, even though the attempted apply
   failed.

### 3. Exactly-once terminalization

For every unique terminal transaction, in this order:

1. Claim `pending → finalizing`.
2. Record history according to the reversibility rules.
3. Emit the history-outcome metric.
4. Emit the terminal managed-apply metric.
5. Emit the terminal audit event.
6. Complete the per-ID terminal ledger record.
7. Update health/finalization status and, **only then**, decide whether this
   record replaces the singular latest pointer.

The per-ID ledger is completed **after** history so a retrieval observes the
record only once its history/audit provenance is attached; the `finalizing`
state (step 1) is the externally observable window during which these steps run.
The high-water guard must not wrap these operations. A duplicate terminal
callback produces no duplicate side effects.

### 4. Coordinator ordering

The finalizer path uses this order:

```
receive reload result
restore previous file if required
build final ApplyResult
unlock coordinator file mutex
finalize history/audit/metrics/ledger
mark transaction no longer in flight
close ReloadRequest.Finalized
deliver terminal result to synchronous waiter
```

No external callback runs while holding the coordinator mutex. `inFlightState`
is not cleared and `Finalized` is not closed before finalization completes. No
second managed transaction begins while terminal history/audit is outstanding. A
callback panic is caught, converted into a finalization error, surfaced through
admin health, and must not wedge the coordinator. Finalization failure is an
observability/compliance degradation, not a runtime rollback.

### 5. Planned-restart linearization

A successful stage operation linearizes only after the marker is staged and the
on-disk digest is verified to still equal the staged candidate. Marker promotion
holds the coordinator mutex from final baseline verification through
post-promotion disk verification.

### 6. One absolute transaction deadline

A managed apply runs under a single absolute deadline, bound at HTTP admission
as `now + reload_timeout`, that governs candidate resolution, the full
pre-persistence preflight gate, persistence, and the live reload. There is no
per-phase budget that could let one slow phase run the overall apply unbounded.
Following R15-01 the budget is taken from the **currently serving** config's
`reload_timeout`, so a candidate that changes `reload_timeout` governs only the
next transaction, never the one that submits it. A deadline breach **before**
persistence aborts cleanly with the on-disk configuration untouched and is
surfaced as a phase-specific `504 Gateway Timeout` (`timed_out_phase`); after
Publish the deadline no longer cancels work and an overrun is reported as
`applied_degraded`, because the handler generation has already committed. The
absolute deadline is recorded on the pending ledger record so a browser can poll
it deadline-aware rather than by a fixed attempt count.

### 7. Finalization degradation is advisory

Runtime apply success and finalization success are orthogonal. A committed apply
(`applied_live` or `applied_degraded`) stays a success even if its history
snapshot or its ledger/audit finalization degrades; the degradation is surfaced
as a non-blocking advisory on the terminal record (`history_error`,
`finalization_error`) and rendered alongside — never replacing — the apply
outcome. A finalization-callback panic is caught, converted into a finalization
error, surfaced through admin health, and must not wedge the coordinator.
Finalization failure is an observability/compliance degradation, not a runtime
rollback.

A callback panic does **not** create a replacement zero-value record. The
registry transitions the existing record to terminal **in place**, preserving
the operation and complete apply result while attaching `finalization_error`; a
bare ID/error fallback that cannot be reconstructed into a valid terminal result
is rejected rather than published, so the public record stays parseable and
never loses its operation or result.

## Consequences

- **Positive:** every committed configuration transition is reversible; every
  terminal transaction is audited and metered regardless of completion order;
  the exact terminal result is retrievable by ID; planned-restart promotion is
  linearizable.
- **Negative / trade-off:** the coordinator holds its mutex across more of the
  promotion path; a bounded in-memory ledger adds retention state.
- **Invariant:** a singular latest pointer never substitutes for per-ID
  retention; no success response precedes coherent marker+disk verification; no
  high-water check suppresses a legitimate audit event.

## Implementation status

The following pieces of this decision have landed and are covered by tests:

- **Operation identity (AC-01):** `ApplyOperation` enum and `ApplyRequestContext.Operation`
  in `internal/admin/server.go`, assigned by each write handler.
- **Terminal ledger + exact-ID API (AC-02):** `internal/admin/managed_apply_registry.go`
  and `GET /api/config/applies/{id}`.
- **Coordinator finalizer ordering (AC-03):** `internal/app/config_apply.go` runs
  terminal finalization before clearing the in-flight guard, closing `Finalized`,
  or delivering the synchronous result.
- **Durable-recording-before-high-water + operation-specific audit and bounded
  metric labels (AC-04):** `internal/app/serve.go` callback ordering,
  `finalizedAuditOperation` in `internal/admin/server.go`, and the
  `operation`/`mode` labels on `jul_managed_apply_finalized_total`.
- **History metadata sidecar (AC-05 storage):** `HistoryMetadata`,
  `snapshotWithMeta`, and `getMeta` in `internal/admin/history.go`.
- **Preview immutability (AC-07):** clone-before-mutate in
  `internal/admin/patch_http.go` for the preview and candidate endpoints.
- **Planned-restart linearization (AC-06):** `PromoteToStagedVerified` in
  `internal/app/planned_restart.go`, driven under the coordinator mutex from
  `internal/app/config_apply.go`.
- **History creation in terminalization (AC-05 wiring):** the terminal
  `managedApplyFinalizer.Finalize` in `internal/app/managed_apply_finalizer.go`
  calls `admin.Server.RecordManagedHistory`, so the snapshot is produced by the
  single terminal path rather than out of band.
- **Single terminal helper (AC-03 completion):** every persisted mutation — hot
  apply, enqueue failure, stage create, and stage update — routes through the
  single `completeManagedApply` helper in `internal/app/config_apply.go`
  (covered by `config_apply_stage_finalize_test.go`).
- **One absolute transaction deadline (AC-08):** `preflightContext` and
  `servingReloadTimeout` in `internal/app/config_apply.go` bound resolution,
  every preflight gate, persistence, and reload under a single deadline taken
  from the serving `reload_timeout`; a pre-persistence breach surfaces as a
  phase-specific `504` (`timed_out_phase`).
- **Console correlation (AC-09–AC-13):** the exact-ID managed-apply poll
  (`useManagedApplyRecord`, `fetchManagedApply`, `GET /api/config/applies/{id}`)
  observes the terminal record by its exact id rather than the global
  runtime-overview pointer.
- **Health/finalization degradation surface (AC-14):** the finalization advisory
  (`internal/admin/ui/src/lib/finalizationAdvisory.ts`, `history_error` /
  `finalization_error` on the ledger record, and the bounded
  `jul_managed_apply_finalization_errors_total{component}` counter) renders a
  committed-but-degraded apply as advisory without downgrading apply success.

All of the audit-closure acceptance criteria referenced above landed through the
configuration-audit remediation workstreams (WS01–WS06) and are covered by
tests; the closure evidence is regenerated at the exact green SHA under
Workstream 7.

## Related

- `internal/admin/server.go` — `ApplyRequestContext`, `ApplyOperation`, `finalizedAuditOperation`
- `internal/admin/managed_apply_registry.go` — bounded terminal ledger
- `internal/admin/history.go` — `HistoryMetadata` sidecar
- `internal/app/planned_restart.go` — `PromoteToStagedVerified`
- `internal/app/config_apply.go` — managed apply coordinator and finalizer ordering
- `internal/config/candidate.go` — immutable candidate
- ADR 0011 — ReloadPlan reload transaction
- `docs/reload-semantics.md` — operator-facing reload semantics
