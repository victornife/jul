# Slice 1 Review & Evidence Report — `#667`, `#666`, `#668`

> Historical evidence only. The review below missed runtime defects at
> `86432bd9`. Its completeness, capacity-coalescing and durable-ledger performance
> claims are superseded by the [2026-10-31 corrective review](2026-10-31-pr674-confirmed-findings-and-validation.md).


- **Date:** 2026-10-30
- **Base (`main`):** `fddc34562a18d4047c27a6e1bcbe9f78917bf304` (includes merged `#621`/`#665`, `#622`/`#669`/`#670`, and `#623`/`#672`/`#673`)
- **Reviewed Production Tree / Commit:** `de2a6160c6c3d29437b3a23931679c8b3bb005ab` (initial candidate `daf9b57e3a9e617c8fe1386f4d2da34f0b14efba`, post-review fix `2ea6d4826628057ea792caee9416f08a63ec840f`, lint cleanup `de2a6160c6c3d29437b3a23931679c8b3bb005ab`)
- **Owning ADR:** [ADR 0030](../adr/0030-cache-purge-recovery-latency-and-cutover.md)
- **Issues Addressed in Slice 1:**
  - [#667](https://github.com/victornife/jul/issues/667) — Measure and bound large-purge lock and persistence latency
  - [#666](https://github.com/victornife/jul/issues/666) — Preserve incomplete purge protection across restart (including Comment 1 interrupted sensitive-`Vary` retirement)
  - [#668](https://github.com/victornife/jul/issues/668) — Make legacy purge durability outcomes observable

---

## 1. `#667` — Measured Large-Purge Latency, Allocations, and Concurrent Traffic Bounds

Environment: macOS `darwin/arm64` (Apple M5), Go `1.27.3`, `GOMAXPROCS=2`.
Raw logs: [baseline (`fddc3456`)](../audit/2026-10-30-667-purge-latency-baseline.txt), [optimized (`2ea6d482`)](../audit/2026-10-30-667-purge-latency-after.txt).

| Scenario | Entries / Reps | Baseline `crit_ms` (median) | Optimized `crit_ms` (median) | Baseline Total `ms` | Optimized Total `ms` | Baseline Allocations (`B` / `objs`) | Optimized Allocations (`B` / `objs`) | Unrelated Warm HIT `p95` (`us`) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `mem_1k_prefix` | 1,000 / 1,050 | 0.53 ms | **0.26 ms** (−50.9%) | 1.38 ms | **0.81 ms** (−41.3%) | 798 KB / 8,560 | **149 KB / 12** (−99.9% objs) | — |
| `mem_1k_tag` | 100 / 150 (in 1k) | 0.12 ms | **0.09 ms** (−25.0%) | 0.28 ms | **0.22 ms** (−21.4%) | 130 KB / 1,863 | **81 KB / 1,078** (−42.1% objs) | — |
| `mem_10k_host` | 10,000 / 10,500 | 4.24 ms | **1.59 ms** (−62.5%) | 11.80 ms | **8.72 ms** (−26.1%) | 9.32 MB / 106,187 | **2.42 MB / 34,006** (−68.0% objs) | — |
| `mem_10k_all` | 10,000 / 10,500 | 1.15 ms | **0.34 ms** (−70.4%) | 1.16 ms | **0.34 ms** (−70.7%) | 1.53 MB / 10,585 | **24.5 KB / 504** (−95.2% objs) | — |
| `mem_200k_host` | 200,000 / 210,000 | 106.85 ms | **42.06 ms** (−60.6%) | 302.99 ms | **177.49 ms** (−41.4%) | 194.3 MB / 2,124,169 | **17.4 MB / 10,603** (−99.5% objs) | **1,087.0 us -> 0.5 us** (−99.95%) |
| `mem_200k_all` | 200,000 / 210,000 | 33.44 ms | **7.95 ms** (−76.2%) | 33.44 ms | **7.95 ms** (−76.2%) | 27.4 MB / 211,091 | **483 KB / 10,007** (−95.3% objs) | 0.2 us -> 0.2 us |
| `disk_1k_host_ledger` | 1,000 / 1,050 (disk) | 21.01 ms | **18.92 ms** (−10.0%) | 534.99 ms | **251.09 ms** (−53.1%) | 1.96 MB / 17,005 | **1.47 MB / 9,919** (−41.7% objs) | — |

Key engineering improvements:
1. Eliminated `O(N log N)` candidate sorting on `InvalidateScope` execution paths (`candidateBaseKeysForExecLocked`).
2. Replaced per-resource map and slice allocations with flat key buffer collection (`collectOwnedKeysIntoLocked`) and direct `bk[nl+1:]` string slicing (`2,124,169` -> `10,603` allocations on `200,000`-entry host purge).
3. Added `incompleteFenceHost` fast path so warm memory hits on unrelated hosts do not acquire `c.genMu` during a single-host purge (`unrelated_hit_p95` dropped from `1,087.0 us` to `0.5 us`).
4. Increased default disk batch size from `64` to `512` (`534.99 ms` -> `251.09 ms` for `1,000` disk entries).

---

## 2. `#666` — Durable Incomplete-Purge and Sensitive-Vary Retirement Protection Across Restart

- **Baseline reproductions (`fddc3456`):**
  - `REPRO_666_INTERRUPTED_PURGE`: snapshotting `disk_path` after fence commit and before physical deletion served `X-Cache: HIT` (`v1-stale-article`) upon reopening the cache on restart.
  - `REPRO_666_SENSITIVE_VARY_304`: snapshotting `disk_path` during a `304 Not Modified` that introduced `Vary: Cookie` on an older disk entry (whose own metadata had `Vary: Accept-Encoding`) allowed the older disk entry to survive `rehydrate()` on restart.
- **Implemented solution (`2ea6d482`):**
  - `diskStore` persists `.jul-cache-purge-intents` (`diskPurgeIntentsManifest`, version `1`, bounded at `1 MiB` / `256` active intents, with capacity coalescing marked `Coalesced: true` and >4,096-resource tag purges coalesced to `SelectorHost`).
  - `exact_url` and `key` intents never persist raw URLs or query strings; they store pre-commit disk file hashes (`target_hashes`) and keyed `versionid.Context` HMAC digests (`target_resource_mac` / `target_key_mac`, domain `cache-purge-target-v1`).
  - `reconcileRecoveredPurgeIntents` runs during `cache.New` before returning, deleting pre-cutoff (`gen <= FenceGen`) matching entries across all selector kinds (`prefix`, `glob`, `tag`, `host`, `exact_url`, `key`, `all`) and `GET`/`HEAD`/`Vary` groups while preserving post-cutoff fills (`gen > FenceGen`) and unrelated scopes, and retaining an in-memory `selectorFence` if any disk removal fails.
  - `deleteAtMostGenWithResult` and `delOneIfGenAtMostWithResult` execute `c.disk.deleteBatchAtMostGen` (committing `.jul-cache-tombstones` before unlinking) before dropping in-memory state, preventing interrupted `304`/sensitive-`Vary` retirements from rehydrating on restart.

---

## 3. `#668` — Truthful Legacy Purge Durability Outcomes

- `Cache.PurgeResult()` and `Cache.DeleteResult(key)` execute through the shared invalidation and tombstone engine; `Cache.Purge()` and `Cache.Delete(key)` remain thin compatibility wrappers.
- Unversioned `POST /cache/purge` (`handlePurge` in `internal/admin/server.go`) inspects `ResultPurger`:
  - Returns `200 OK` `{"status":"purged"}` and audits `"success"` only when `InvalidationEffective == "effective"`.
  - Returns `503 Service Unavailable` with structured `status`, `outcome`, `invalidation_effective`, `failed_entries`, and `recovery_guidance` and audits `"failure"` when durability is `incomplete` or `uncertain`.
  - On a marker-only `.jul-cache-gen` write failure where all disk entries were removed (`diskFailed == 0 && !repairUncertain && !hasSurvivingCacheState()`), Jul reports `uncertain` (`503`) and records `DiskWriteHealth()` failure without setting `unrecoverableInvalidation = true` on the healthy empty disk tier.

---

## 4. Independent Adversarial Review Record (Slice 1)

| Reviewer Identifier | Reviewed SHA | Scope | Initial Findings (`daf9b57e`) | Fix & Re-Review Disposition (`2ea6d482`) |
| --- | --- | --- | --- | --- |
| `Explore-Slice1-Security-Durability-Reviewer` | `daf9b57e3a9e617c8fe1386f4d2da34f0b14efba` -> `2ea6d4826628057ea792caee9416f08a63ec840f` -> `de2a6160c6c3d29437b3a23931679c8b3bb005ab` | `#666` security/privacy/durability & `#668` legacy outcomes | 1. >4,096 tag resource IDs could exceed 1 MiB intent limit or have empty host.<br>2. `recordPurgeIntent` write failure needed fail-closed cleanup (`failClosedOnIntentWriteErrorLocked`) and `Coalesced` flag.<br>3. Noted non-`ResultPurger` test-double fallback in `handlePurge`. | Fixed in `2ea6d482` and `de2a6160` (`failClosedOnIntentWriteErrorLocked`, >4,096 tag coalescing to `SelectorHost`/`SelectorAll`, `Coalesced: true`, `batchUncertain` preserving intent-write `uncertain = true`). **Re-reviewed at `2ea6d482` & `de2a6160`: PASS (0 blocking defects).** |
| `Explore-Slice1-Concurrency-Performance-Reviewer` | `daf9b57e3a9e617c8fe1386f4d2da34f0b14efba` -> `2ea6d4826628057ea792caee9416f08a63ec840f` -> `de2a6160c6c3d29437b3a23931679c8b3bb005ab` | `#667` concurrency/latency/bounds & `#666` intent capacity | 1. `recordPurgeIntent` at 256 cap needed coalescing with `Coalesced: true` so individual `completePurgeIntent` cannot retire a coalesced intent early.<br>2. `InvalidatedResources` could undercount when `GET` was in-flight-only (`reps == 0`) and `HEAD` was resident (`reps > 0`). | Fixed in `daf9b57e`, `2ea6d482`, and `de2a6160` (`countedResSet`, `hasResidentBaseLocked`, `Coalesced: true` in `completePurgeIntent`, `golangci-lint` cleanup). **Re-reviewed at `2ea6d482` & `de2a6160`: PASS (0 blocking defects).** |
