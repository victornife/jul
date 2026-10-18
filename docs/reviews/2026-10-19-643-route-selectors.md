# #643 — Correct route selectors across server blocks

Implementation base: `9f72938ce71392c6394dca79bae70704625ce758`.
Design context: #580 / proposed PR #640, design head
`ce929b4968b6369a748fc67c800664b45866a16f`. No sharing acceptance is implied.

## Decision and compatibility

Keep ADR 0018's resolver contract. Count across every location with identical
raw listener, raw name multiset, match type and path, in declaration order.
`routeOrdinals` builds one index per snapshot, preserving whitespace, case,
duplicates and nil/empty equivalence. Producers in Console, external v1 and the
real-router tester consume it. Returned coordinates come from raw declarations,
including omitted/default match types, rather than normalized router labels.

A corrected payload and a cached pre-fix payload can otherwise be byte-identical
under an unchanged raw revision. Corrected selectors carry
`selector_version = "global-v1"`; cross-block ordinal edits without it are
refused. Unknown formats are refused. Single-block legacy ordinals and unique
legacy targets remain supported. No raw configuration version changes merely
because the binary upgrades. Refresh cached Console clients or API projections;
retaining an old selector and just obtaining a new version is insufficient.

Console editors echo the full selector and pin previews to the projection's
`base_version`. Selection keys distinguish duplicate locations. V4 ID-less
navigation requires both the stored and actual projection revision to agree;
durable `route_id` navigation is unchanged. Older ambiguous navigation fails
closed. The historical V2 coordinate-only restore behavior remains for old
unique navigation records; it cannot resolve duplicate server identities.

An independent review found that an earlier structural operation in one batch
can shift an ordinal even when its original CAS passes. Such batches now refuse
a later ordinal operation atomically. Apply the declaration-changing operation,
then fetch new selectors. This conservative restriction includes unrelated
structural operations; it avoids adding another mutable identity mechanism.

## Acceptance evidence

| Requirement | Implementation / maintained evidence | Result |
| --- | --- | --- |
| Reported fixture accepted and wrong target reproduced | `selector_crossblock_test.go`; unmodified-base probe | Before: block 1 ordinal 0 resolves to block 0. After: ordinal 1 resolves to block 1. |
| Independent projection/v1 oracle | `TestSelectorGeneratedRoundTrips`, seed 643, 100 × 72 locations | Pass; exact/prefix/regex, interleaving, reordered/padded/case/duplicate/empty names |
| Real tester selector identity | Same test compares to `router.Explain` positional results; parsed-default fixture | Pass |
| Production HTTP preview/apply and unchanged live route | `TestPublishedSelectorEditsOnlyShadowedBlock`, production `Serve`, real coordinator/reload/disk | Managed changes only second block; first config unchanged and traffic stays 200. File-owned refuses persistence. |
| Cached payload through real binary upgrade | `scripts/probe-route-selector-upgrade.py` with separately built baseline/fixed binaries | Raw revision unchanged; old ordinal 0 refused; fresh ordinal 1 preview valid. |
| Console selects second duplicate and posts correct payload | `route-selector-crossblock.test.tsx` | Predicate, response-header and CORS edits pin revision and echo ordinal 1/format. |
| Stale/missing CAS and bad ordinals | Existing `patch_ordinal_test.go` plus legacy/unknown-format and batch-shift regressions | Pass |
| Changed Go statement gate | Complete zero-context diff, `strip_moved_lines.py`, `check-added-go-coverage.py`, lean + full profiles | 43/43, 100% |

Commands actually run (Go 1.27.2, Linux amd64):

- `go test ./internal/admin ./internal/adminapi ./internal/app -count=1 -coverprofile=...`
- Same command with full tags: `brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf`.
- `go test -race ./internal/admin -run 'TestSelectorGenerated|TestPublishedOrdinal|TestLegacyCrossBlock|TestPatchOrdinal' -count=1`.
- `go test -race ./internal/app -run '^TestPublishedSelectorEditsOnlyShadowedBlock$' -count=1`.
- `go vet ./internal/admin ./internal/adminapi ./internal/app`.
- `go generate ./internal/apicontract`; `make generated-check`.
- `python3 scripts/docs-check.py`: 2,380 passed, zero failed before this report.
- Console typecheck, lint, unit/coverage and build. Coverage contract passed;
  923 tests passed; statements 75.52%, branches 72.85%, existing floors retained.
- Baseline/fixed `go build ./cmd/jul` and the binary-upgrade probe above.

The local Playwright Chromium run could not start: both full-browser and
headless-shell downloads returned invalid ZIP data. This is an unavailable
local browser gate, not a passing test. Required remote CI and browser evidence
must be checked on the final PR head before merge. No soak or deployment ran.

## Documentation and scope dispositions

Updated admin API, Console, ADR 0018 compatibility note, generated OpenAPI and
Unreleased Fixed entry. README, `docs/roadmap/README.md` and
`docs/feature-status.yaml` need no feature/maturity change: this repairs an
existing administration contract. NGINX migration: not applicable; no directive,
import classification or request-dispatch behavior changes. #640 already states
the corrected raw selector semantics, so no proposed design text changes are
needed. #580/#641/#642 remain unaccepted/blocked as recorded; this fix neither
implements nor completes those capabilities. #644 is a separate security change.
