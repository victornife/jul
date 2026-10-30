# Independent Review Report — `#624` Route/Backend Cutover & `#620` Integrated Epic Verification

- **Date**: 2026-10-30
- **Branch**: `feat/624-620-route-backend-cutover` (stacked on `feat/666-667-668-cache-recovery-and-outcomes` at `86432bd9b34ee88f1f50d983b88d411fa63a2e61`)
- **Initial Candidate Commit SHA**: `d4f1ca919c19e7cee44594d6f464131406b0abf5`
- **Intermediate Fix Commit SHAs**: `a77ca8d28d261658ad476ab18d8ce2fe0a72fe51`, `c9b5fd3337115547fead8edf087cf739d5a3832e`, `46eb94d23c5a00127a76398e98dc4872d566e557`, `4e09802597b174f0c9399f0d4d3c401a6a1a5b9f`
- **Final Reviewed Production/Test Commit SHA**: `51b8839a0957bc8a9e7853cdf10a5448df83983c`
- **Reviewers**:
  - **Reviewer 1 (`Explore-Slice2-Cutover-Race-Reviewer`)**: Independent read-only subagent reviewing `#624` route/backend cutover semantics, `WithBackendCutover`, `DeriveRouteCutoverSelectorsForPolicy`, `ExecuteRouteCutover`, `internal/app/factory.go` (`Build` vs `Prepare` vs `commitFn` vs `abortFn`), in-flight cold fills / `304` / SWR / delayed old-handler fencing, config rollback `A -> B -> A` non-resurrection, and partial post-`Publish` failure handling.
  - **Reviewer 2 (`Explore-Slice2-Contract-UI-Epic620-Reviewer`)**: Independent read-only subagent reviewing config schema/validation/lifecycle (`[cache].reload_policy`, `[servers.locations.cache_policy].reload_policy`), admin projections/patches/diff (`CacheCutoverPlan`), Console UI (`LocationCachePolicyEditor.tsx`, `RouteDetail.tsx`, `cachePolicyDraft.ts`), documentation accuracy, and the `#620` integrated portal + two-application verification fixture (`internal/app/cache_cutover_epic620_test.go`).

---

## 1. Initial Review Findings (`d4f1ca919c19e7cee44594d6f464131406b0abf5`)

### Reviewer 1 (`Explore-Slice2-Cutover-Race-Reviewer`)
- **P0 / P1 Findings**: `0`
- **P2 Findings**: `3`
  1. **P2-R1-1 (`internal/cache/selector.go`, `internal/cache/scoped_purge.go`)**: Cutover selectors discarded the listener/route ownership boundary, so a cutover on `:8080` or a parent prefix `/api/` could evict unrelated retained routes sharing the same hostname on `:9090` or more-specific retained child routes (`/api/static/`).
  2. **P2-R1-2 (`internal/cache/selector.go`)**: `findMatchingLocationForCutover` and `routeChangedOrUpstreamChanged` compared `ServerNames` in raw order rather than normalized+sorted order (`routeScopeBoundary`), causing a false cutover when `server_names` were reordered or case-normalized.
  3. **P2-R1-3 (`internal/cache/route_policy.go`, `internal/cache/selector.go`)**: `canonicalLocationBackendIdentity` and `sameLocationCutoverTarget` did not clear metadata-only `loc.Sharing`, so editing `[servers.locations.sharing]` triggered a false cutover under `reload_policy = "invalidate"`.

### Reviewer 2 (`Explore-Slice2-Contract-UI-Epic620-Reviewer`)
- **P0 / P1 Findings**: `0`
- **P2 Findings**: `6`
  1. **P2-R2-1 (`internal/cache/route_policy.go`, `internal/admin/diff.go`)**: Folding `backendID` directly into `matcherProof` only when `reload_policy == "invalidate"` caused policy-only transitions (`"retain" <-> "invalidate"` without backend changes) to advance `RouteEpoch` at runtime while `diffConfigs` reported no invalidation.
  2. **P2-R2-2 (`internal/admin/diff.go`)**: `diffCacheCutoverPlan` emitted live publish-time invalidation warnings even when `Disposition == "stage_for_restart"` or when `[cache].enabled` was being disabled.
  3. **P2-R2-3 (`internal/admin/apiv1_resources.go`)**: `v1Routes` omitted `cache_policy` (and therefore `EffectiveReloadPolicy`) when `loc.CachePolicy == nil` on a cached route inheriting global `[cache]`.
  4. **P2-R2-4 (`internal/admin/apiv1_cache_inspect.go`)**: `resolveInspectionRoutePolicy` did not check `ReloadPolicy` equivalence across multiple candidate routes matching a host/prefix inspection selector.
  5. **P2-R2-5 (`internal/admin/diff.go`)**: `formatCutoverSelector` formatted `SelectorGlob` (used for `exact` routes) as `"glob"` instead of `"exact <host><path>"`.
  6. **P2-R2-6 (`internal/admin/diff.go`)**: `diffCacheCutoverPlan` used display label equality rather than selector/route-scope coverage when filtering `RetainedScopes`.

---

## 2. Remediations & Intermediate Re-Reviews (`a77ca8d2` -> `c9b5fd33` -> `46eb94d2`)

1. **Route-Scoped Cutover Selectors & Recovery (`P2-R1-1`)**:
   - Added `cutoverRouteScopes []string` to `NormalizedSelector` and `CutoverRouteScopes []string` to `diskPurgeIntent` (`internal/cache/selector.go`, `internal/cache/diskstore.go`, `internal/cache/scoped_purge.go`).
   - Updated `collectOwnedKeysIntoForCutoverLocked`, `matchesAnyFenceLocked`, `visitMatchingInflightLocked`, `InvalidateScope` (`isFastAll` and `completePurgeIntent` gated on `len(sel.cutoverRouteScopes) == 0`), and `reconcileRecoveredPurgeIntents` so route-scoped cutovers never evict or fence entries/variants or clear pending recovery intents belonging to unrelated retained routes (cross-listener `:9090`, retained child routes `/app-one/static/`, or sibling routes on unnamed/wildcard servers).
2. **Canonical Server Boundary & Metadata-Only Field Exclusion (`P2-R1-2`, `P2-R1-3`)**:
   - Used `sameCutoverServerBoundary` (`bytes.Equal(routeScopeBoundary(*a), routeScopeBoundary(*b))`) in `findMatchingLocationForCutover`, excluded `Sharing` and `CachePolicy` in `canonicalLocationBackendIdentity` and `sameLocationCutoverTarget`, and included `srv.Plugins` in `sameLocationCutoverTarget`.
3. **Separate Durable `backendProof` (`P2-R2-1`)**:
   - Separated `backendProof` (`cache-route-backend-v1`) and `reloadInvalidate` from `matcherProof` in `RouteCacheConfig` and persisted `Backends` in `.jul-cache-route-epochs` (`routeEpochManifest`), failing closed when `reload_policy == "invalidate"` and prior backend proof is absent or changed while keeping policy-only flips (`"retain" <-> "invalidate"` without backend changes) warm.
4. **Admin Diff, Inspection, and `v1Routes` Fixes (`P2-R2-2`..`P2-R2-6`)**:
   - Updated `diffCacheCutoverPlan` (`internal/admin/diff.go`) for `stage_for_restart` and `!after.Cache.Enabled`, formatted `SelectorGlob` as `exact <host><path>`, included `cutoverRouteScopes` in `computeSelectorDigest` and `cutoverSelectorCoveredByInvalidated`, exposed `EffectiveReloadPolicy` in `v1Routes` when `loc.Cache && loc.CacheEffective != nil`, and checked `ReloadPolicy` equivalence in `resolveInspectionRoutePolicy`.

---

## 3. Final Re-Review Verdict (`46eb94d23c5a00127a76398e98dc4872d566e557`, `4e09802597b174f0c9399f0d4d3c401a6a1a5b9f`, & `51b8839a0957bc8a9e7853cdf10a5448df83983c`)

- **Reviewer 1 (`Explore-Slice2-Cutover-Race-Reviewer`)**: **APPROVE (`0` P0, `0` P1, `0` P2)** at `46eb94d23c5a00127a76398e98dc4872d566e557`, `4e09802597b174f0c9399f0d4d3c401a6a1a5b9f`, and `51b8839a0957bc8a9e7853cdf10a5448df83983c`.
- **Reviewer 2 (`Explore-Slice2-Contract-UI-Epic620-Reviewer`)**: **APPROVE (`0` P0, `0` P1, `0` P2)** at `46eb94d23c5a00127a76398e98dc4872d566e557`, `4e09802597b174f0c9399f0d4d3c401a6a1a5b9f`, and `51b8839a0957bc8a9e7853cdf10a5448df83983c`.
