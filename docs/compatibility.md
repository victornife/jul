# Jul.IA — compatibility & versioning policy

> Version 1.1 · Updated 2026-08-04

This document defines what "stable" means for Jul.IA and what a **GA** label
guarantees about a feature's contract. It is the fleet-wide answer to GA
criterion 4 — *stable config/API contract, semver-guarded* — in
[ADR 0003](adr/0003-maturity-and-ga.md).

## Versioning

Jul.IA follows [Semantic Versioning 2.0.0](https://semver.org): **MAJOR.MINOR.PATCH**.

- **MAJOR** — incremented for a breaking change to a stable (GA) contract.
- **MINOR** — new features and non-breaking changes; new optional config keys.
- **PATCH** — bug and security fixes with no contract change.

The first stable line is **v1.0.0**. The version is reported by `jul -version`.
The core configuration format and documented standalone admin API covered by
this policy remain open-source per [ADR 0012](adr/0012-oss-open-core-boundary.md).

## What the GA contract covers

For a feature at **GA**, the following are **stable** and change only on a MAJOR
bump (with a deprecation period, below):

1. **Configuration surface** — the TOML keys, types, defaults, and accepted value
   sets documented in that feature's `docs/<feature>.md`. A valid GA config keeps
   loading across MINOR/PATCH upgrades.
2. **CLI** — documented subcommands and flags (`jul`, `-config`, `-check`,
   `-version`, `import`, `lint`, …).
3. **Admin/Console HTTP API** — documented `/api/*` request/response shapes the
   Console depends on.
4. **Prometheus metric names and labels** — the `jul_*` series documented for the
   feature. Renames/removals are MAJOR; **new** series/labels are MINOR.
5. **Observable wire behaviour** — documented headers, status codes, and proxy
   variables (e.g. `$ssl_client_*`).

Within a MAJOR line these are additive-only: new optional keys, new metrics, and
new endpoints may appear in MINOR releases, but existing ones keep their meaning.

### Invalid values are not a compatibility contract

The stable configuration contract covers the documented keys, types, defaults,
accepted value sets, and explicit zero/disabled meanings. It does **not** make an
implementation bug that silently accepted an undocumented invalid value part of
the v1 contract. A PATCH release may therefore reject a value that previously
failed open to a default, provided that:

- the value was never documented as valid;
- omission and every documented explicit-zero meaning remain valid;
- the rejection is consistent across startup, CLI, managed apply/stage, rollback,
  importer output, and Console validation; and
- the stricter behavior and any required correction are recorded in the
  changelog.

Jul.IA never clamps or auto-corrects an explicit invalid value. Operators should
run `jul check` against the target binary before deployment.

### Access-log enablement compatibility

`[observability.access_log].enabled` is an additive optional Boolean. Omission
preserves the v1 default-on behavior; explicit `false` is the only supported
disable contract. The legacy global/per-server log-destination strings remain
parseable but deprecated and ignored for the rest of the v1 line.

### Response-cache behavior changes (#132)

No configuration key changed. The following **observable HTTP behavior** changed
because the previous behavior contradicted the shared-cache contract Jul.IA
claims. Each is a correctness fix, not a feature toggle, and none is
configurable:

- `Cache-Control: no-cache` on a request or a response now forces successful
  validation before reuse. It previously did nothing.
- `must-revalidate` and `proxy-revalidate` now prevent stale reuse. They were
  previously ignored, so `[cache] stale_while_revalidate` silently overrode them.
- A request carrying `Authorization` can no longer be answered from a stored
  response unless that response explicitly permits shared reuse.
- A successful `POST`/`PUT`/`PATCH`/`DELETE` now invalidates the cached
  representations of its target. Nothing was invalidated before.
- A request carrying `Range` or `If-Range` now bypasses the cache. It could
  previously be answered with a cached complete representation.
- Freshness is measured from the origin's `Date`/`Age`, so a response that
  already aged upstream is served for less time than before.
- A new `X-Cache: REVALIDATED` value can appear where `HIT` previously did.
  Clients or dashboards that enumerate `X-Cache` values, or that compute a hit
  ratio, must account for it.

The on-disk entry format gained fields. It remains readable in both directions:
gob decodes an absent field as its zero value, every added field's zero value is
the conservative answer, and an older binary ignores fields it does not know. No
migration is required, and no cache needs to be cleared on upgrade or rollback.

### Configuration authority default (ADR 0019)

`[global].config_authority` is a new, additive, optional field, so no existing
configuration file becomes invalid. **One deployment shape requires a required
migration step to keep its control plane writable**, and it is called out here
in the same terms as the response-cache changes above: the previous behavior
(every deployment implicitly able to write its configuration file through the
Console/API) contradicted the ownership contract this record establishes, so
the new fixed default is a correctness fix, not a feature toggle, and is not
configurable to anything else.

- **A file-operated deployment** — edits `server.toml` directly and relies on
  SIGHUP or the file watcher, with or without the admin API enabled — resolves
  to `file_owned` and is **unaffected**. This is the majority shape and the
  reason the default is what it is.
- **A Console- or API-operated deployment that has never declared
  `config_authority` becomes read-only at its next restart.** Every mutating
  admin endpoint returns `409 config_authority_read_only`, naming the exact
  field to set. Setting `config_authority = "managed"` restores exactly
  today's write behavior, with one deliberate difference: a direct file edit
  is no longer adopted by SIGHUP or the watcher in `managed` mode — it becomes
  drift, resolved through `POST /api/config/adopt-external`. See
  [reload-semantics.md](reload-semantics.md#configuration-authority-managed-vs-file-owned)
  and [deployment.md](deployment.md#configuration-authority).

No file gains a `config_authority` value on upgrade; it must be declared
explicitly to opt into `managed`.

### Generated configuration contract artifacts (ADR 0019 §21-23)

`docs/generated/config.schema.json`, `config-metadata.json` and
`config-reference.md` are deterministic renderings of the same configuration
surface item 1 above already covers — they describe existing keys, types,
defaults and value constraints, and do not themselves widen or narrow the
configuration contract. Their own **rendered shape** (JSON Schema dialect,
`config-metadata.json`'s field layout, generated-reference structure) is
tracked separately by `configcontract.ContractVersion`, which changes only
when that shape changes, independent of ordinary additive configuration
growth. **JSON Schema validity is necessary and not sufficient**: a document
satisfying the generated schema can still fail `jul check`, whose cross-field
and cross-object rules remain authoritative and are not themselves part of
the schema. Likewise, a configuration may satisfy both the schema and
`jul check` while `jul lint` reports an error-severity finding — lint policy
is never encoded as schema-level invalidity.

## What it does not cover

- **Beta / Prototype / Alpha features** — still evolving; their config and APIs
  may change in a MINOR release. Each such key is marked in its doc.
- **The Go API** (`internal/…` packages) — Jul.IA ships as a binary; the Go
  packages are not a supported import surface and may change at any time.
- **Exact log lines, benchmark numbers, and internal file layouts** (e.g. the
  on-disk cache or history snapshot format), unless a doc explicitly commits to
  them.
- **Default values that are security or correctness fixes** — a default may change
  in a MINOR release when leaving it would be unsafe; such changes are called out
  in the changelog.

## Deprecation policy

A stable contract is removed only after a deprecation period:

1. The element is marked **Deprecated** (maturity ladder, [ADR 0003](adr/0003-maturity-and-ga.md))
   in its doc and, where practical, emits a startup or `-check` warning.
2. It keeps working for **at least one MINOR release** after the deprecation is
   announced.
3. It is removed no earlier than the next **MAJOR** release.

## Maturity and stability

| Maturity | Config/API stability |
| --- | --- |
| Planned / Prototype / Alpha | none — may change or disappear |
| **Beta** | usable; keys may still change in a MINOR with a changelog note |
| **GA** | covered by this policy (MAJOR-only breaking changes + deprecation) |
| Deprecated | scheduled for removal per the deprecation policy |

A feature reaching **GA** in the [GA push](ga-push.md) freezes its documented
config/API surface under this policy as of the v1.0.0 line; the v1.0.0 git tag is
cut at the first GA release.

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-08-19 | 1.6 | Replaced the boolean `healthy` field on backends with a five-state `state` enum (`available`, `circuit_open`, `circuit_half_open`, `health_unhealthy`, `at_capacity`) on `GET /api/apps` and `GET /api/upstreams`, and added a server-computed pool `verdict` (`healthy`/`degraded`/`down`/`unknown`) to `GET /api/apps`. The boolean was fed only by the health checker, so a backend taken out of rotation by the circuit breaker or by a per-backend concurrency cap still reported `healthy` while receiving no traffic — the field said the opposite of what an operator needed during an incident. | The field is still absent when no live status has been observed, and absent still means *unknown*, not *down*. Every other field on both projections keeps its name and type. | #144; [console.md](console.md#upstreams) |
| 2026-08-19 | 1.5 | The access log gained an `upstream_reason` field, carrying the bounded reason an upstream call failed. It is emitted only when one did, following the same omit-what-adds-nothing rule as `trace_id` and `peer_ip`. | Every existing field keeps its name, type and meaning. The value set is closed, so a backend address, route path or raw error text will never appear in it. | #144; [failure taxonomy](core-http.md#upstream-failure-taxonomy) |
| 2026-08-19 | 1.4 | `jul_upstream_probes_total` and `jul_upstream_probe_duration_seconds` gained a bounded `source` label (`http`/`stream`). The stream proxy owns its own upstream registry, so an upstream used by both surfaces is probed twice and the two series were previously indistinguishable — an operator saw an unexplained 2×. | Existing queries keep working; only a recording rule that aggregates without `by (source)` will start splitting. Metric names, types and existing labels are unchanged. | #144; `amendedReleasedMetrics` in `internal/observability/metric_contract_test.go` |
| 2026-08-19 | 1.3 | Client cancellation is recorded as **499** (nginx's non-IANA "client closed request") instead of 504. The client has already disconnected, so nothing is transmitted either way and this is purely the recorded status — but recording 504 inflated "gateway timeout" with requests where nothing timed out. | The 502, 503 and 504 mappings for every other failure are unchanged, including a genuine upstream timeout and Jul's own retry deadline, which both remain 504. | #144; [failure taxonomy](core-http.md#upstream-failure-taxonomy) |
| 2026-08-19 | 1.2 | Retired the `jul_upstream_healthy{pool,backend}` gauge, replaced by `jul_upstream_backends_healthy{pool}`. The `backend` label was a raw address and grew without bound under Kubernetes pod churn, contradicting the project's own bounded-label rule; per-backend health moves to the Admin API, which is queried on demand rather than retained as a time series. Removed and renamed pool identities are now also deleted from every `pool`-labelled vector on reload. | Every other v1.32.0 metric family keeps its name, type, help and label set, enforced by the frozen released baseline. | #144; `retiredReleasedMetrics` in `internal/observability/metric_contract_test.go` |
| 2026-08-04 | 1.1 | Clarified that undocumented invalid values accepted through silent fallback are defects, not a stable v1 contract; PATCH releases may reject them while preserving omission and documented zero semantics. | Documented valid keys, value sets, defaults, lifecycle and deprecation guarantees remain unchanged. | #123; [configuration value contract](config-value-contract.json) |
| 2026-06-21 | 1.0 | Created the compatibility & versioning policy (semver, the GA stable-contract surface, what is excluded, the deprecation policy, and the maturity/stability mapping) to satisfy GA criterion 4 fleet-wide for the GA push. | The maturity ladder and GA bar (ADR 0003) and all feature behaviour are unchanged; this only documents the contract. | [ADR 0003](adr/0003-maturity-and-ga.md), [ga-push.md](ga-push.md) |
