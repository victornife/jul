# #622 corrections after review of 4cc4e46e

Refs [PR #669](https://github.com/victornife/jul/pull/669) and
[#622](https://github.com/victornife/jul/issues/622).

The four reported findings are valid. The earlier readiness assessment of
`4cc4e46eb76db6393add85deb8b5a1cef84ad5d1` is superseded: green CI and its
91.50623% Codecov patch result did not prove instrumentation/Console completeness.
The PR is back in draft. This checkpoint records implemented corrections and
local evidence; new-head independent reviews, hosted gates and actual Codecov
PATCH strictly >90% must be verified before readiness. No merge/manual closure.
ADR0028's accepted design and built-in grants remain unchanged.

## Correction matrix

| Finding | Corrected semantic owner | Behavioral evidence | Contract/docs |
| --- | --- | --- | --- |
| P2 stale error reuse mislabeled SWR | Existing immutable publication evidence carries a known-basis discriminator and SIE-extension flag; serve owner selects the actual reason | `TestInspectionReviewSIEReuseBasis`, `TestInspectionReviewBackgroundSIEExtension`, `TestInspectionReviewStaleBasisRestartAndReplacement`: SWR-off real handler, background500, subsequent reuse, disk restart, older metadata,304 reset | Cache guide / ADR0028; `stale_if_error_reuse`, `stale_if_error_extended`, `stale_basis_unavailable`; no reuse-policy change |
| P2 missing range/background decisions | Common serve hook before range/full/conditional dispatch; common recorder observation wiring; capture publication observed at its shared owner | `TestInspectionReviewRangeReuseAndRestrictions`:206 HIT and private/no-store/Set-Cookie fill rejection; `TestInspectionReviewBackgroundOversize`; isolated background SIE test; `TestInspectionReviewRangeFillPublication` proves successful range publication rather than inferring it from MISS | Cache reason semantics / observability; existing closed capture/publication codes and metrics owner preserved |
| P2 inspect-only Console unreachable | Shared traffic projection grant list; catalog admits inspect; handler returns only `inspection_only: true` before reading editable state; lazy inspector reused as limited view | `TestInspectionOnlyConsoleProjectionIsScoped`:real routes, exactly one safe marker/no config read, inspection200, config/status/routes/health/purge403, all five old broader grants retain full projection. Full-App unit uses only inspect and makes only two allowed requests. Browser case covers both pure inspect and config-read identities | Console/API/SECURITY; manual known hostname, no host catalogue/config projection/edit/purge grant. Unauthorized overview/health polling disabled |
| P3 year0001 validation date | Optional metadata times use Go `omitzero`; UI handles absence and older serialized zero dates | `TestInspectionReviewUnknownAndKnownValidationTime`:real publication then304 and JSON serialization. UI tests cover omitted and legacy-zero dates, insertion unknown and no recorded validation | API/cache/Console; known RFC3339 dates unchanged, unknown fields omitted |

## Reproduction and local validation

The maintained new Go regressions were copied into an immutable archive of
`4cc4e46e` and failed on all reported categories. They also exposed missing
no-store/cookie range rejection and range-fill publication evidence.
[Red baseline output](622/review-4cc-reproductions.txt) is preserved as failure
evidence, not a passing result. The same regressions pass with the corrections. The three new full-App/unknown-date UI cases also fail on4cc4e46e (11 existing cases pass) and pass with the corrections.

Commands use Go1.27.3 and the existing full tag set from Makefile:

| Command | Result at this checkpoint |
| --- | --- |
| `go test ./internal/cache ./internal/admin -run '^TestInspection' -count=1` | PASS, including all new owner/access/time tests |
| `go test ./internal/cache ./internal/admin ./internal/adminapi ./internal/adminclient ./cmd/jul` | PASS |
| `go test -race ./internal/cache ./internal/admin ./internal/adminclient ./cmd/jul` | PASS; focused inspection race rerun also passes |
| `go test -tags "$FULL_TAGS" ./internal/cache ./internal/admin ./internal/adminapi ./internal/adminclient ./cmd/jul` | FAIL overall at inherited Unix-socket CLI fixtures in this environment; affected cache/admin/API/client packages PASS |
| `make console-check` | PASS:997 tests,85 test files; existing123-production-file/10-target contract. Inspector14/14 tests pass. Total statements80.16%(5675/7079), inspector92.40%(73/79); no floors/exclusions changed |
| `pnpm --dir internal/admin/ui run build` | PASS after correcting a missing synthetic token_id in the new test fixture; initial-route gzip255,830/256,000B |
| `make generated-check` | PASS; owning API/schema/lifecycle projections remain in sync; no generated files hand-edited |
| `make ci-pr` / `make test-race` | Results collected separately; environment Unix sockets/process probes remain unavailable. Missing/failing checks are not passing evidence |

Move-aware changed Go at this checkpoint is **95.1381215469%=861/905**
against78b458b8 ([raw result](622/review-correction-added-go.json)); the correction
alone is **94.3661971831%=67/71** against4cc4e46e
([raw delta](622/review-correction-delta-go.json)). These are local Go statement
reports, not the new-head Codecov PATCH report.44 statements remain
uncovered in the complete diff, listed in the raw record; no exclusions/floors
changed. Full-tag lint passes0 issues after removing the now-unused private
headerStorable wrapper; the first lint attempt correctly failed that unused code.
D11 inventory and22 tests pass; docs checker passes2,538 checks and its unit
runner passes. An initial new test fixture used a nonexistent overview path and
omitted its required synthetic token_id; corrected to the actual path/identity
contract before the passing checks. The new-head platform/browser/Codecov gates
remain unverified at this source checkpoint.

The original schema/body tests had missed the Console permission path. The new
full-App test exercises the actual entry projection consumer and Layout with no
status/config/purge grant. Server-route tests prove the marker does not expose
configuration. The browser smoke now exercises the same identity through the
real permission provider and normal navigation. A passing older mocked drawer
scenario is not substituted for this check.

Optional zero-time fields were already advertised as optional. `omitempty`
alone did not omit a zero time.Time; `omitzero` makes actual JSON obey that
contract. This uses the pinned Go toolchain and does not change known dates.

The stale discriminator uses existing provenance flag bits without adding an
Entry field/allocation or changing expiry/reuse decisions. New publication/304
metadata identifies its original SWR basis; error clones mark SIE. Previous
metadata without this discriminator stays unknown rather than being guessed.
The cloned evidence remains immutable after publication and survives existing
additive gob persistence. A successful304/replacement clears the old SIE basis.

## Performance and remaining gates

[Five-pair smoke observations](622/review-correction-bench.json), same declared
baseline78b458b8, Go1.27.3, GOMAXPROCS2, alternating order, CPUs4/5,500ms:
HIT medians1757→1759ns, Vary2557→2566ns. HIT1256B/16allocations and
Vary1504B/25allocations are unchanged. All samples, including a baseline outlier,
are retained. This is an additional smoke comparison, not a replacement for the
original30-pair study or a tail/SLA guarantee. No provenance/sample capacity or
retention limit grew.

The prior [implementation ledger](2026-10-27-622-implementation.md) preserves
baseline, broader acceptance mapping, previous review dispositions and inherited
#666/#667/#668 boundaries. Its old ready/coverage claims cannot certify this new
head. Updated PR/issue evidence will bind the new source tree to independent
review, exact validation commands/results and the actual final Codecov report.
NGINX migration remains not applicable; no request/cache policy semantics changed.
