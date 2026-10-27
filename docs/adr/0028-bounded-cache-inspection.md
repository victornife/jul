# ADR 0028 — Bounded cache inspection and observed decision evidence

- Status: accepted for implementation; verification pending (#622).
- Date: 2026-10-27.
- Decision authority: maintainer approval of the implementation proposal and explicit-grant recommendation.
- Parent: #620; programme selection: #62; delivered foundation: #621 / ADR 0027.

Use the existing selector/catalog/variant ownership and safe projection, extended
with bounded detached metadata capture. API, CLI and Console consume one server
implementation. A separate `cache:inspect` grant preserves all existing built-in
role grants and normal wildcard/legacy-token authority; read access does not grant
purge, and exact purge access does not grant generic inspection. Each page
reauthenticates against the live credential/policy and binds the cursor to principal,
normalized path/scope, authorization generation and configuration identity.

Bound capture to 10,000 candidates, 256 logical resources, 32 representation
details per resource, two concurrent builders, at most 50 resources / 2 MiB encoded response per page, 16 retained
snapshots and 8 MiB aggregate encoded metadata. Snapshot retention is at most
60 seconds and never extends contained decision lifetimes. Cancellation and failed
post-capture authorization/configuration checks reclaim new retained state and
stop its cleanup timer. Capture consistency is explicitly weaker than a global
transaction across tiers/configuration/decisions. Cache sequence detects some
changes; unchanged sequence leaves eviction and independent churn unknown.

Compare bounded detached captures with per-tier iteration: captures provide stable
paging without readers/body retention; live map iteration has no stable bounded
seek and would repeatedly scan or miss/repeat under churn. Reject unbounded global
snapshots and a second cache index. Existing cache mutex waits are not instantaneously
cancelable; deadlines cannot promise to interrupt an existing purge lock hold.

Metadata capture is network-free with respect to applications and does not call
handlers, populate caches, update LRU order, sweep disk cleanup or open bodies.
Vary names are validated HTTP tokens; tags use the existing validator. Query/Vary
values, credentials and bodies are excluded, including from errors, audit and browser
storage. Detach projected strings so a redacted substring cannot retain a query's
backing buffer. Correlation uses a process-local keyed HMAC, never a plain query hash.

Opt-in recent decision evidence is memory-only, 256 records, at most a 60-second
lifetime, with timer-based reclamation and clearing on disable. Closed reason,
stage, explanation and next-step data come from actual reuse/admission/capture/
validation/fill/eviction owners. It is lossy evidence, not traffic recording or
proof of non-occurrence. Current tag membership does not establish historical tags.
No URL metrics, new telemetry exporter, live probe or destructive operation is added.

Publication provenance is immutable and compact, shared by entry/catalog/disk
residency, and charged to the memory-tier footprint estimate. Corrected origin age
is separate from insertion/validation time. Freshness provenance comes from the
actual freshness owner; current SIE is projected by the current stale-error owner.
Older disk entries explicitly lack this evidence. Normal reload can retain older
entry freshness; this does not implement #623 route policy or #624 cutover.

The [cache guide](../cache.md#bounded-cache-inspection) owns operational limits,
errors, examples and inherited #666/#667/#668 boundaries. NGINX migration impact
is not applicable while request semantics remain unchanged. Final maturity,
coverage, platform/browser and independent review evidence remain separate gates;
this ADR does not declare release, GA, soak or issue completion.

Review clarification: stale-deadline provenance distinguishes original SWR from
an SIE extension using the existing immutable provenance allocation; older
metadata lacking this distinction stays explicitly unknown. Inspection-only
Console loading uses a minimal view marker and manual configured-host input,
without widening existing configuration/status projections or grants. Unknown
evidence timestamps are omitted, not represented by zero dates. These implement
the accepted evidence/permission contract; they do not change reuse policy.
