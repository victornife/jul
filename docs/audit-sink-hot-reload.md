# Durable audit sink hot reload (HR-07C)

Issue #160 makes only `admin.audit_log_file`, `admin.audit_log_rotate_max_mb`, and `admin.audit_log_rotate_keep` hot-reloadable. The in-memory audit ring, its bounded capacity/cursor, and the monotonic event-ID sequence remain process-lifetime state. `admin.history_dir`, `admin.history_keep`, `admin.enabled`, and `admin.listen` remain restart-bound.

## Ownership and event linearization

`auditLog` owns the process-lifetime ring and one event linearization lock. Recording an event under that lock assigns exactly one global ID, normalizes/redacts the event, appends it exactly once to the ring, selects the currently published durable generation, and acquires one ordered write ticket/lease for that generation. The lock is then released before JSON encoding and filesystem I/O.

This is intentionally different from request-policy snapshots. Console/upload/admission policy is captured at request entry. An audit event selects its durable destination when the event itself is recorded. An in-flight request that began before Publish but records its terminal audit event after Publish therefore writes that event to the new sink. System and managed-apply finalizer events use the same event-time rule.

Within one physical sink, write tickets preserve the global audit order even if goroutines reach the writer in a different scheduler order. Failed writes advance the ticket turn so later events cannot deadlock. There is no asynchronous audit-event queue, no queue-full drop policy, and no fallback write to a second generation. A durable write failure never removes the ring event or rolls back the audited configuration/business operation. Because durable persistence is synchronous, filesystem latency can contribute to request/finalizer latency.

## Prepared resource lifecycle

A changed sink is a prepared, immutable generation containing the resolved path and rotation policy. The reload transaction is:

```text
Prepare candidate writer
        ↓
Publish admin snapshot + sink-generation swap under the event barrier
        ↓
new audit events select the candidate generation
        ↓
old generation drains only writes selected before Publish
        ↓
bounded post-Publish Retire closes/releases the old owner
```

Prepare performs all fallible path/open/identity work. Publish performs no open, stat, chmod, rename, rotation, directory scan, pruning, close, or disk wait. Old-resource drain/close uses the existing `ReloadPlan` post-Publish retirement context. Retirement failure is advisory: it cannot roll back a committed new generation and does not make a healthy new active sink unhealthy.

An exact effective no-op does not reopen the file or increment the sink generation. Disabling the durable sink is a real generation transition to ring-only recording.

## Filesystem preparation and ownership

The configured destination is normalized to an absolute internal path while the user-configured value remains configuration data for the authenticated `config:read` settings projection.

For an existing destination, Jul `Lstat`s the final component, accepts only a regular non-symlink file, opens it for append without create/truncate, compares pre-open, handle, and post-open identities, and rejects a replacement race. Existing bytes and backups are not changed during Prepare and pre-existing permissions are not rewritten.

For a missing destination, Jul creates only an empty candidate-owned final placeholder with exclusive create and mode `0640`. Missing candidate parent directories are created with mode `0750` and identity-tracked. Abort removes the candidate file only if the path still names the exact candidate-owned file and it is still empty, then removes candidate-created directories bottom-up only while they retain their original identity and remain empty. External replacements or content are never deleted. A hard crash before Publish can leave empty non-destructive candidate artifacts because no Abort callback can run after process death; it cannot truncate or rotate existing audit history.

The implementation uses Go rooted filesystem operations for the prepared destination and rejects unsupported final file types. Multi-process locking is deliberately out of scope: one Jul process must own a durable audit path at a time.

## Rotation compatibility

The dynamic audit path uses a Jul-owned audit-specific rotating writer instead of creating a new `lumberjack.Logger` on each reload. This avoids lazy first-open failures after Publish and per-generation `millRun` maintenance goroutines.

The observable contract remains:

- one JSON object per line;
- empty `audit_log_file` disables only durable persistence;
- resolved `audit_log_rotate_max_mb = 0` means the canonical 100 MiB default;
- resolved `audit_log_rotate_keep = 0` means the canonical 14-backup default;
- negative rotation values are invalid;
- timestamped backups use the existing local-time naming shape;
- no compression;
- no age-based deletion;
- no per-event `fsync` guarantee.

The historical first-write max-size boundary is retained: when opening an already-existing active file, equality with the size limit rotates on the first durable write; subsequent writes rotate when the write would exceed the limit. Backup-name collisions do not overwrite an existing backup.

Retention cleanup occurs only for a committed active generation during rotation, never during preview/Prepare. Reducing `rotate_keep` therefore does not synchronously prune at Publish; it affects future active rotations. A path switch never applies the new path's retention policy to the old path.

A same-path rotation-policy update shares one physical file owner across the old and new logical generations. Physical owners remain weakly registered by normalized path until their final generation releases them, so a rapid A→B→A while the first A is still draining reuses that owner and cannot create a second rotator for A. This is the single-maintenance-owner handoff: there is never an old and new rotator concurrently operating on the same path. A path A→B transition leaves all A files/backups in place and B does not import them. B→A later appends to the still-valid A history.

## Health, readiness, and failure semantics

Active sink health is distinct from process-cumulative failure counters and retired-resource advisories. Machine-visible categories are bounded (`open`, `path_validation`, `encode`, `write`, `rotate`, `retention_cleanup`, `close`, `retirement_timeout`). Raw OS errors are for operator logs, not metric labels, public readiness, external error details, or audit event detail.

A configured active sink write/rotation failure degrades admin health and readiness while the ring continues. Physical-owner health is advanced in durable ticket order, so same-path handoff or rapid path reuse cannot hide a late old-generation failure. Repetitive persistence warnings are process-locally rate-limited to one emission per 30 seconds, with the next emitted warning reporting how many log lines were suppressed; failure counters and health transitions are never suppressed. A candidate Prepare failure does not poison the currently live generation's health. Publishing a healthy B after degraded A makes B the active health truth; reusing A restores A's physical-owner health until a successful ordered write proves recovery; disabling a degraded sink clears active durable-sink readiness degradation. Successful later writes recover transient active health while cumulative counters and last-failure history do not decrease. A retention-cleanup failure after a successful event write records retention degradation rather than claiming the event was lost.

Public `/readyz` returns only bounded readiness status/reason and never serializes filesystem paths, permissions, or raw OS errors.

## Configuration, API, and Console

The authenticated Admin Runtime Settings projection exposes the three audit configuration fields, their server-authoritative lifecycle metadata, and a bounded active-sink status. The narrow sparse operation is `admin_audit_sink_set` with optional `file`, `rotate_max_mb`, and `rotate_keep` fields. An explicitly supplied empty `file` is the canonical durable-disable operation.

The Console uses the existing Admin Runtime Settings drawer. A path change warns that old files/backups are not copied, moved, merged, or deleted. Disabling warns that durable persistence stops for new events after Publish while the ring continues and existing files remain. Rotation changes warn that preview does not prune backups and that committed retention applies during later active rotations.

Audit list/export remains ring-based. #160 does not add a durable-file browser, rotated-file API, filesystem explorer, or new `/api/v1` filesystem exposure.

## Security and observability boundaries

Redaction happens before the normalized event is stored in the ring and the same normalized representation is serialized durably. Audit-sink failures are logged through ordinary operator logging, not recursively as new audit events. Paths, actor IDs, source IPs, token IDs, event IDs, generation hashes, and raw errors are not introduced as Prometheus labels by #160. No new metric family is required; existing bounded runtime/admin-health surfaces remain the source of operational health.

## Shutdown and known limitations

Shutdown marks the active generation retired, waits only for its already-selected writes under a bounded context, and releases the writer once. No per-generation background cleanup worker exists.

Intentional limitations: durable audit is local-filesystem based; a configured path has one-process ownership; path changes do not migrate/merge files; there is no per-event fsync/crash-stable exactly-once claim; and the in-memory ring resets on an actual process restart. #160's exactly-one property is the live-process sink-generation selection invariant, not a distributed transaction guarantee.
