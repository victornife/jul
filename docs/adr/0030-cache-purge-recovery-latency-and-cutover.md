# ADR 0030 — Durable incomplete-purge recovery, large-purge latency bounds, purge outcomes, and route/backend cutover

- **Status:** Accepted — Beta/unreleased (`#667`, `#666`, `#668`, `#624`, `#620`)
- **Date:** 2026-10-30
- **Deciders:** Victor Niharra (`@victornife`)
- **Applies to:** `internal/cache`, `internal/admin`, `internal/adminapi`, `internal/adminclient`, `internal/app`, `internal/config`, `internal/lifecycle`, `cmd/jul`, Console v2 (`internal/admin/ui`), and `[cache]` / `[servers.locations.cache_policy]`
- **Source:** [#667](https://github.com/victornife/jul/issues/667), [#666](https://github.com/victornife/jul/issues/666), [#668](https://github.com/victornife/jul/issues/668), [#624](https://github.com/victornife/jul/issues/624), [#620](https://github.com/victornife/jul/issues/620), [#62](https://github.com/victornife/jul/issues/62)
- **Related:** [ADR 0011](0011-reload-plan.md), [ADR 0013](0013-project-operating-model-and-completeness.md), [ADR 0014](0014-operability-surfaces.md), [ADR 0015](0015-managed-apply-terminal-ledger.md), [ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md), [ADR 0025](0025-effective-version-confidentiality.md), [ADR 0027](0027-scoped-cache-invalidation.md), [ADR 0028](0028-bounded-cache-inspection.md), [ADR 0029](0029-route-scoped-cache-policy-and-vary-guards.md)

## Context

Following the delivery of `#621` ([ADR 0027](0027-scoped-cache-invalidation.md)), `#622` ([ADR 0028](0028-bounded-cache-inspection.md)), and `#623`/`#672` ([ADR 0029](0029-route-scoped-cache-policy-and-vary-guards.md), merged at `fddc34562a18d4047c27a6e1bcbe9f78917bf304`), four coordinated cache-lifecycle obligations remain under `#620` (`CACHE-OPS`):

1. **Large-purge lock and persistence latency (`#667`)**: `InvalidateScope` sorted all candidate keys under `c.genMu` (including `200,000+` host keys), allocated intermediate slices and maps per candidate (`~2.12M` heap allocations / `~194 MB` for `200,000` entries), set a single global `incompleteFenceGen` atomic that forced warm memory hits on *unrelated* hosts to acquire `c.genMu`, and executed `64`-entry disk batches with two `fsync`s per batch (`32` `fsync`s per `1,000` disk entries).
2. **Incomplete-purge and retirement protection across restart (`#666`)**:
   - Selector fences lived only in memory, and `.jul-cache-purge-ledger` was owned by `internal/admin` with lazy loading and redacted `exact_url`/`key` query strings (`?<redacted>`). If a purge committed its cutoff `fenceGen` and was interrupted by a crash or canceled mid-batch before deleting every matching disk file, restarting Jul could rehydrate and serve surviving pre-cutoff disk entries (`gen <= fenceGen`) before an operator retried.
   - Similarly (`#666` Comment 1), when a `304 Not Modified` (or `5xx` / replacement response) introduced `Vary: Cookie`, `Vary: Authorization`, or a transformed `Vary` on an older cached entry whose own on-disk metadata lacked that sensitive `Vary`, `deleteAtMostGen` / `delOneIfGenAtMost` dropped the memory entry before unlinking the disk file without a pre-unlink durable tombstone batch, allowing an interrupted cleanup to rehydrate the older disk file on restart.
3. **Truthful legacy purge durability outcomes (`#668`)**: `Cache.Purge()` discarded disk high-water marker and deletion/tombstone errors and `POST /cache/purge` (`handlePurge`) unconditionally returned `200 OK` `{"status":"purged"}` with audit `"success"`. Conversely, `InvalidateScope` (`SelectorAll`) reflexively set `unrecoverableInvalidation = true` (disabling a healthy empty disk tier) even on a marker-only `.jul-cache-gen` write failure after every disk cache file had been removed.
4. **Explicit route and backend cutover (`#624`, `CACHE-OPS-04`)**: Ordinary reloads intentionally retain cached entries (`retain` default). Operators deploying a new backend version or changing a route target/action/upstream need an explicit, previewable `invalidate` option aligned with the configuration `Publish` boundary that advances route ownership (`RouteEpoch` / `boundGen`) and executes durable scoped invalidation without flushing unrelated applications or falsely claiming rollback when configuration already committed.

## Decision

### 1. Measured large-purge latency bounds and lock/allocation reduction (`#667`)

We retain the single `c.genMu` linearization owner (preserving atomic cutoff assignment, immutable tag snapshot capture, and publication fencing) while eliminating redundant work inside and across critical sections:

1. **Unsorted candidate collection on execution paths:** `candidateBaseKeysLocked` and `collectBoundedHostKeys` sort keys only for bounded `PreviewScope` / `InspectScope` (`maxScan <= 10,000`), never during `InvalidateScope` execution.
2. **Zero-allocation resource key slicing and flat key collection:**
   - Because base keys have the canonical form `METHOD + "\n" + host + "\n" + reqURI`, `resKey` is sliced directly as `bk[strings.IndexByte(bk, '\n')+1:]` with zero heap allocation.
   - `matchedResSet` (`map[string]struct{}`) is allocated for `sel.Kind == SelectorTag` and small candidate sets (`<= 2,048`) (where mutable tag membership requires an explicit resource snapshot). For `SelectorHost`, `SelectorPrefix`, `SelectorGlob`, `SelectorExactURL`, and `SelectorKey`, `sel.MatchesResource(host, reqURI, nil)` is stateless and `O(1)`; unique resources are counted in `O(1)` by checking whether a `HEAD` base key has a sibling `GET` base key in `c.byHost[host]`.
   - `collectOwnedKeysIntoLocked` appends owned memory, disk, and in-flight keys directly into a pre-sized flat batch buffer without per-resource map or slice allocations.
3. **Per-host lock-free fast path for `incompleteFenceGen`:**
   - When an active or incomplete fence targets a single hostname (`sel.Host != ""` and `!sel.cutoverMatchAnyPort` and `incompleteFloorGen == 0`), `c.incompleteFenceHost` records that hostname. Warm memory hits (`c.get`) on any other hostname bypass `c.genMu` lock acquisition completely when `mgen <= c.incompleteFenceGen.Load()`.
4. **Bounded disk batching (`defaultDiskPurgeBatchSize = 512`):**
   - When `opts.BatchSize == 0` (the default), disk deletions group up to `512` keys per `deleteBatchAtMostGen` prepare/commit tombstone cycle (reducing `1,000`-entry disk tombstone `fsync`s from `32` to `4`), while honoring explicit `opts.BatchSize > 0` for fine-grained batch cancellation tests.

### 2. Durable incomplete-operation protection and startup reconciliation (`#666`)

#### 2.1 Alternatives compared

| Alternative | Mechanism | Why selected or rejected |
| --- | --- | --- |
| **Option A — Reconcile before serving cache traffic** | Persist durable intent at cutoff commit; on startup, reconcile unfinished intents against rehydrated `it.gen <= cutoffGen` entries before `cache.New` returns. | **Selected as primary mechanism:** eliminates pre-cutoff entries at startup with zero ongoing request-path lookup cost while preserving post-cutoff fills (`it.gen > cutoffGen`) and unrelated scopes. |
| **Option B — Restore in-memory fence protection, clean up in background** | Reconstruct `selectorFence` entries at startup and delete files asynchronously. | **Selected as fail-closed fallback when startup unlink/tombstone fails:** if any file removal fails during Option A startup reconciliation, the reconstructed `selectorFence` (`f.remaining > 0`, `incompleteFenceGen >= cutoffGen`) remains active in memory so surviving pre-cutoff entries cannot be served as hits. |
| **Option C — Persist full resource ID lists for every selector** | Write all matched resource hashes for `host`/`all`/`prefix`. | **Rejected for broad/path selectors:** `host`, `all`, `prefix`, and `glob` selectors are already compact and self-contained (`< 1 KiB`), whereas persisting `200,000` resource hashes would write `> 13 MiB` under `genMu`. Resource hashes / target key hashes are persisted only for `tag` (`matched_resource_ids`) and `exact_url` / `key` (`target_hashes` + keyed HMAC). |
| **Option D — Whole-node cache flush on any incomplete purge** | Evict the entire disk cache whenever any purge was interrupted. | **Rejected as normal policy:** destroys unrelated applications' warm cache for a narrow interrupted purge. Reserved strictly for corrupt/unreadable/unverifiable recovery manifests. |

#### 2.2 Durable purge intent journal (`.jul-cache-purge-intents`)

- `diskStore` owns `.jul-cache-purge-intents` (`diskPurgeIntentsFile = ".jul-cache-purge-intents"`, version `1` JSON envelope, maximum size `1 MiB`, maximum `256` active/incomplete intents, strict `DisallowUnknownFields`).
- **Commit ordering:** In `InvalidateScope`, after `fenceGen` is allocated under `c.genMu`, `diskStore.commitPurgeIntent(...)` checks aggregate capacity and writes the intent record to `.jul-cache-purge-intents` (the persisted fence also supplies startup high-water recovery) **before** `opts.CommitFence` can publish the administrative cutoff. The callback atomically stores the same complete `PurgeRecoveryIntent` as `recovery_intent` with the ledger cutoff, so fallback recovery also remains executable after journal completion but before the terminal ledger write. Only then is the in-memory fence installed and physical cleanup begun. Journal I/O uses an independent intent mutex; it never takes the disk-item mutex under `genMu`. Actual I/O-failure cleanup runs after releasing `genMu`, avoiding callbacks that invert the generation/item lock order. Capacity rejection occurs before the callback, fence installation, or resident deletion.
  - Each `diskPurgeIntent` stores:
    - `fence_gen` (`1..math.MaxInt64-1`), `kind`, normalized `host`, `explicit_default_port`, `cutover_match_any_port`, `prefix`, `cutover_raw_prefix`, `pattern`, `cutover_exact_path`, `tags`, `tag_match`, and frozen post-`route_id` `route_path` / `route_type` (never a raw `route_id` that could resolve differently after a config change).
    - For `tag`: `matched_resource_ids` (the original `SHA-256(host + "\n" + reqURI)` resource set at `<= fence_gen`).
    - For `exact_url` and `key`: **never** the raw URI or query string. Instead, the intent stores:
      1. `target_hashes`: the sorted 64-hex-char `SHA-256(key)` disk file hashes of all owned keys (`GET`, `HEAD`, `sel.Key`, and resident/in-flight `Vary` variants) at `<= fence_gen`.
      2. `target_mac`: keyed HMAC-SHA-256 under the node's `versionid.Context` (`domain = "cache-purge-target-v1"`) over `host + "\n" + reqURI` (for `exact_url`) or `sel.Key` (for `key`).
      - Why this is both private and rotation-safe: `target_hashes` are identical to the on-disk filenames already present in `disk_path` and `.jul-cache-tombstones` and are deleted as soon as the operation completes; `target_mac` uses the protected confidential-version key (ADR 0025) so no unkeyed query digest is stored. Even if the version key is rotated across restart, `target_hashes` still matches and deletes every surviving pre-cutoff disk file (`it.gen <= fence_gen`).
- **Capacity policy:** both the encoded `1 MiB` aggregate limit and `256`-intent count limit are checked atomically before admission. The effective count depends on payload sizes; `256` maximum-size snapshots are not supported. Pressure returns `503 storage_unavailable` with retry/reconciliation guidance; it never enters corruption cleanup or broadens new tag intents to host/all. Retries retain their immutable original identity and can proceed at full capacity. Old coalesced manifests remain readable for migration.
- **Legacy ledger fallback:** valid historical tag `matched_resource_ids` snapshots recover exactly their original resource set, even when tag ingestion is now disabled. An explicit `[]` is a valid zero-target snapshot and preserves all entries; absent/null or invalid IDs are unavailable identity. Older committed exact-URL/key records without executable target identity, unavailable tag snapshots, or unknown selectors trigger an explicit logged sweep of all disk entries at that original cutoff, preserving newer generations. This is a migration fallback for unverifiable records; successful reconciliation requires actual retirement, never a target-less no-op. New records recover from their complete normalized identity.
- **Completion boundary:** Only when `InvalidateScope` finishes all deletion batches and tombstone reconciliation with `remainingGroups == 0 && failedEntries == 0 && !uncertain && !canceledMidway` does `c.completePurgeIntent(fenceGen)` remove `fenceGen` from `.jul-cache-purge-intents`. Canceled, partial, failed, or crash-interrupted operations retain their intent record across restarts until reconciliation succeeds.
- **Startup reconciliation (`recoverIncompletePurgeIntents`):**
  - Executed during `cache.New` after `rehydrate()` and `onDiskRehydrated` before `cache.New` returns.
  - Corrupt, truncated, oversized (`> 1 MiB`), unknown-version, unknown-field, or invalid-cutoff (`fence_gen == 0` or `>= math.MaxInt64`) intent files trigger fail-closed `purgeAllCacheFilesOnCorruptTombstoneLocked()` (preserving `highWaterGen` above all observed entries and setting `unrecoverableInvalidation = true` if any file unlink fails).
  - For each valid persisted intent, `recoverIncompletePurgeIntents` identifies all rehydrated entries with `st.gen <= intent.FenceGen` matching the intent (`exact_url` includes `GET`/`HEAD`/`Vary` siblings; `key` retains its selected method and owned representations), deletes them via `deleteBatchAtMostGen` + `mem.delIfGenAtMost`, and removes the intent from `.jul-cache-purge-intents` once `failedEntries == 0 && !uncertain`. If any deletion fails, the intent stays on disk and its `selectorFence` (`f.remaining > 0`, `incompleteFenceGen >= intent.FenceGen`) stays active in memory.
- **Crash-safe sensitive-`Vary` and validation retirement (`#666` Comment 1):**
  - `deleteAtMostGen` and `delOneIfGenAtMost` (called by `cleanupSensitiveVary`, `leadValidation`, and `revalidateWithPolicy` when a `304`, `5xx`, or replacement response introduces `Vary: Cookie`, `Vary: Authorization`, or a transformed `Vary` on an older entry whose own metadata lacked that `Vary`) now execute `c.disk.deleteBatchAtMostGen(allKeys, maxGen)` **before** dropping in-memory state. Because `deleteBatchAtMostGen` atomically commits target hashes to `.jul-cache-tombstones` (`JULTOMB1`) before unlinking files, an interrupted retirement is swept by `loadAndSweepTombstonesLocked()` on startup even when the old disk file's own `diskEntryMeta` had non-sensitive `Vary`.

### 3. Truthful legacy purge durability outcomes (`#668`)

1. **Shared engine behind `PurgeResult()` and `DeleteResult(key)`:**
   - `Cache.PurgeResult() InvalidateResult` executes whole-cache invalidation through the shared `InvalidateScope` (`SelectorAll`) engine; `Cache.Purge()` is a 1-line wrapper (`_, _ = c.PurgeResult()`).
   - `Cache.DeleteResult(key string) InvalidateResult` executes generation-conditional resource/key deletion with durable tombstone batching and reconciliation; `Cache.Delete(key)` wraps `_, _ = c.DeleteResult(key)`.
2. **Distinguishing marker-only failure from surviving-data / tombstone / unlink / sync failures:**
   - If `.jul-cache-gen` (`recordHighWaterGen`) fails during a purge, `diskStore` increments `writeFails`, sets `failing = true` in `DiskWriteHealth()`, and marks the operation durability outcome `uncertain = true` (`State = "partial"` or `"failed"`, `InvalidationEffective = "uncertain"`) so the structured API reports the terminal incomplete/uncertain outcome and recovery guidance instead of fabricating durable success.
   - However, if all targeted disk entries were removed (`diskFailed == 0 && !repairUncertain`) and no target cache files or dirty tombstones remain, no old entry can resurrect on restart. In that marker-only case, Jul does **not** set `unrecoverableInvalidation = true`, keeping the healthy disk tier operational while surfacing the durability failure in the purge result, HTTP status, audit log, and `DiskWriteHealth()`.
   - If `.jul-cache-gen` fails alongside any tombstone, unlink, truncate, or directory-sync error (`diskFailed > 0 || repairUncertain`), `unrecoverableInvalidation = true` is set, disabling disk reads until reconciliation succeeds.
3. **Retire the internal HTTP endpoint in #674:**
   - Remove `POST /cache/purge` (including `?key=`), its handler, the unused `ResultPurger` HTTP adapter, and route/classification inventory entries. All administrative cache invalidation uses `/api/v1/cache/purge*`; Console and remote CLI use that supported API.
   - ADR 0019's classification is explicit: the removed endpoint was `internal`, unsupported and not a stable external contract. The maintainer selected retirement in this PR after migrating the last product consumer. No public-API deprecation window or legacy alias is introduced. Old scripts must migrate to the versioned workflow.
   - Migrate OIDC RBAC/CSRF/audit tests and durability fault tests to the actual versioned route. A completed degraded operation returns HTTP 200 with terminal `state`, `invalidation_effective`, failure counts and recovery guidance; rejection before commit (including journal capacity) returns the versioned error envelope and 503. Clients must inspect structured outcomes.
   - Unknown mutations may not fall through to a successful Console SPA response. Retired POST/key/trailing-slash paths return 404 in lean/full and enabled/disabled Console configurations, without mutating the cache.
   - Keep internal `Cache.Delete()` / `DeleteResult()` and `Purge()` / `PurgeResult()`. These are Go cache operations, not HTTP endpoints. Unsafe-method GET/HEAD/Vary invalidation actively uses `Delete()`, already backed by the current durability machinery. HTTP retirement does not remove that required behavior.

### 4. Publication-aligned route and backend cutover (`#624`, `CACHE-OPS-04`)

#### 4.1 Alternatives compared

| Alternative | Mechanism | Why selected or rejected |
| --- | --- | --- |
| **Post-reload best-effort purge only** | Call `InvalidateScope` after handler swap without advancing `RouteEpoch` or binding `boundGen`. | **Rejected:** races pre-swap handlers that were selected before reload but start their origin fetch after the fence (`startGen > fenceGen`), allowing old backend responses to publish into the new generation. |
| **Route-epoch advancement only** | Advance `RouteEpoch` in `.jul-cache-route-epochs` on changed routes without selector invalidation. | **Insufficient alone:** immediately isolates route-scoped entries and fences old handlers via `boundGen`, but does not evict pre-existing unscoped single-route entries or reclaim physical disk/memory space for deleted routes. |
| **Unified Epoch + Commit-Time Selector Cutover (Selected)** | Advance `RouteEpoch` and `boundGen` in `CommitRoutePolicies` for all cutover-affected routes **before** handler `Publish`, AND execute `DeriveRouteCutoverSelectors` -> `InvalidateScope` across affected scopes at the `Publish` boundary. | **Selected:** `RouteEpoch` + `boundGen` provides immediate `O(1)` admission/lookup isolation and prevents delayed old-handler publication across normal, coalesced, 304, SWR, and Range paths, while `InvalidateScope` (with `#666` durable intent) reclaims physical entries and fences unscoped/shared-host representations. |

#### 4.2 Configuration and per-change cutover contract

1. **Persistent configuration (`reload_policy`, default `"retain"`):**
   - `[cache].reload_policy` (`"retain"` default when omitted | `"invalidate"`, lifecycle `hot_reload`): sets the default cache cutover behavior when routes or upstreams change on reload/apply across both `managed` and `file_owned` (`SIGHUP` / `/reload`) modes.
   - `[servers.locations.cache_policy].reload_policy` (`""` = inherit `[cache].reload_policy` | `"retain"` | `"invalidate"`, lifecycle `hot_reload`): overrides the cutover policy for a specific location.
2. **Per-change apply choice (`cache_cutover`):**
   - Managed apply/preview requests (`/api/config/preview`, `/api/config/patch/preview`, `/api/config/apply`, `/api/config/patch/apply`, `/api/config/rollback`, `/api/v1/config/plan`, `/api/v1/config/patch`, `/api/v1/config/apply`, `/api/v1/config/patch/apply`, `/api/v1/config/rollback`, `jul apply --cache-cutover=retain|invalidate`, and Console `ConfigPanel` / `HistoryPanel`) accept an optional `cache_cutover` (`""` = use configured `reload_policy`, `"retain"`, or `"invalidate"`).
   - `retain` never overrides `#623`/`#672` mandatory matcher compatibility, policy tightening (`max_ttl`, `default_ttl`, `stale_while_revalidate`, `stale_if_error`, `bypass_cookies`, `bypass_any_cookie`), or sensitive/transformed `Vary` rejection.
3. **What changes trigger cutover invalidation when effective policy is `"invalidate"`:**
   - Route target/action changes (`proxy_pass`, `fastcgi_pass`, `uwsgi_pass`, `plugin`, `root`, `return`, `redirect`, `deny`, `grpc`, `grpc_transcode`), route-level `headers`/`rewrites`/`response_headers`/`cors` changes, referenced `[upstreams.NAME]` server list/LB/TLS changes, route deletion, or route matcher changes on routes whose effective cutover mode (before or after) is `"invalidate"`.
   - Runtime health-check transitions and service-discovery address refreshes do **not** invoke config `Publish` cutover and never trigger invalidation storms.
4. **Transaction ordering and failure handling:**
   - **Validate / Preview / Prepare:** Read-only with respect to cache contents and epochs (`BeginRouteCandidate` stages pending handler bindings; `Abort` calls `DiscardPendingRouteHandlers` without touching live epochs or cached entries). Preview computes and returns `cache_cutover_preview` (effective mode, affected selectors, broadened host/all warnings, and hot vs `stage_restart` disposition).
   - **`stage_restart`:** Persists staged config without executing live cache cutover; reports `cache_cutover_state = "staged_for_restart"`.
   - **Hot `Publish`:**
     1. Managed candidate TOML is written to disk.
     2. Inside `commitFn` (at the handler `Publish` boundary): `CommitRoutePoliciesWithCutover` advances `RouteEpoch` and `routeMatcherGen` (persisting `.jul-cache-route-epochs` before updating `routeStateSnap`) for all cutover-affected route scopes, and commits durable purge intents (`.jul-cache-purge-intents`) + selector fences for derived cutover selectors before the new router generation serves requests.
     3. Subordinate physical cleanup (`InvalidateScope` batches) executes and reports `cache_cutover_result` (`succeeded`, `partial`, `failed`, `uncertain`) separately from configuration `Publish` (`applied = true`). If physical cleanup or disk persistence degrades after configuration `Publish`, the response reports the configuration as live/committed and the cache cutover as degraded/uncertain with retry guidance — never claiming configuration rollback when the new configuration is already serving.

## Consequences

- **Positive:**
  - Large `200,000`-entry host purges used approximately `17.4 MB` instead of `194.3 MB` in the original memory-only profile (not a `< 5 MB` guarantee), avoid sorting `200,000` keys under `genMu`, and no longer stall warm memory hits on unrelated hosts.
  - Interrupted, canceled, or partially failed purges—as well as interrupted `304` sensitive-`Vary` retirements—cannot resurrect pre-cutoff disk entries after a restart.
  - All admin callers use the versioned API and receive truthful structured outcomes. The retired internal HTTP route cannot mutate cache or return a misleading SPA success; marker-only failures do not reflexively disable a healthy empty disk tier.
  - Operators have an explicit, previewable `retain` (default) vs `invalidate` cutover choice across managed and file-owned workflows.
- **Console migration:** all Console invalidation now uses the versioned preview/confirm/execute/observe panel, including structured effectiveness and recovery guidance. The duplicate unversioned shortcut/client helper is removed. The internal HTTP endpoint is retired in this PR after consumer migration; internal `Delete()` remains for unsafe-method invalidation.
- **Measured acceptance:** [the updated review](../reviews/2026-10-31-pr674-confirmed-findings-and-validation.md) defines local admin/request/startup thresholds and exercises real durable ledger commits, 200,000-resource disk catalogs, and both journal limits. These are declared local acceptance budgets, not hardware-independent SLOs.
- **Limits:**
  - Startup reconciliation bounds `.jul-cache-purge-intents` to `1 MiB` / `256` operations; corrupt intent files fail closed by purging disk cache files.
  - Crash recovery is verified against reachable filesystem states and injected I/O errors, not physical hardware power-loss certification when the filesystem refuses all writes, renames, unlinks, and truncations.

## Related

- [#667](https://github.com/victornife/jul/issues/667) — Measure and bound large-purge lock and persistence latency
- [#666](https://github.com/victornife/jul/issues/666) — Preserve incomplete purge protection across restart
- [#668](https://github.com/victornife/jul/issues/668) — Make legacy purge durability outcomes observable
- [#624](https://github.com/victornife/jul/issues/624) — Make cache behavior explicit during route and backend changes (`CACHE-OPS-04`)
- [#620](https://github.com/victornife/jul/issues/620) — Cache operations epic (`CACHE-OPS`)
