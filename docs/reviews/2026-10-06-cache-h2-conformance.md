# Cache and HTTP/2 conformance remediation

Date: 2026-10-06. Owners: #540 (cache), #539 (HTTP/2 residuals).
Source baseline: `7d24bb5203fcc750bef0f1460a894964f93edc34`.
Branch: `fix/539-540-cache-h2-conformance`. This is engineering evidence,
not a product-maturity promotion or a programme execution authority.

## 1. Executive summary

Fixed RFC 9111 Expires/Age defects and shared s-maxage stale-revalidation
semantics; implemented bounded RFC 9213 CDN-Cache-Control using Structured
Fields and the existing policy engine. No custom Jul header or
Surrogate-Control alias. Range caching remains deferred to #442.
HTTP/2 runtime wiring is unchanged: four named Go behavior mismatches and one
h2spec frame-size assumption remain precisely owned by #539. All required
local gates pass. Merge still requires green remote PR checks and review;
#539 must remain open. The PR description carries the live CI verdict.

## 2. Repository baseline

- Current main was fetched/fast-forwarded before creating the dedicated branch.
- Starting SHA: `7d24bb5203fcc750bef0f1460a894964f93edc34`.
- Go directive and executed baseline toolchain: 1.26.8, Linux arm64.
- Cache-tests: `d644cf4bf487763646aac19d2c8b846daa0f604d`, package 0.4.5.
- H2spec: v2.6.0 source `70ac2294010887f48b18e2d64f5cccd48421fad1`.
  Upstream's uninjected CLI version prints v1.5.0; tag/SHA identifies the suite.
- Cache baseline: 144/160 upstream required, 146/162 including two custom
  checks; 16 required failures, all listed; one setup retry separately counted.
- Initial H2 baseline: TLS/static 142/146; TLS/proxied 142/146;
  h2c/static 140/146; h2c/proxied 141/146; total 565/584.
- Unchanged targeted packages passed: cache 94.2%, handler 90.9%, server 87.5%
  (lean statement coverage; full-tag numbers are in section 14).

## 3. Issue #540 root-cause matrix

| Area | Cases | Classification/root cause | Resolution | Owner after PR |
| --- | --- | --- | --- | --- |
| Expires | invalid-1-digit-hour, invalid-multiple-spaces, invalid-multiple-lines | Jul defect: permissive http.ParseTime and accepting repeated dates | Strict format round trip, singleton expiry, UTC | Fixed |
| Age | age-parse-prefix-twoline, age-parse-suffix | Jul defect: maximum across lines and ignoring comma-form input | First field/member, strict digits, saturating arithmetic | Fixed |
| Targeted policy | Six cdn cases listed in section 16 | Missing Jul feature selected for implementation | RFC 9213 Structured Fields selection and precedence | Implemented |
| Range | partial-use-headers, partial-use-stored-headers | Deliberate Jul behavior | Preserve complete bypass and bytes/validators | #442 |
| Cookies | headers-store-Set-Cookie | Deliberate Jul behavior | Keep never-store rule | #540 documented contract |
| Framing | headers-store-Transfer-Encoding | Deliberate Jul behavior | Keep invalid origin framing rejection | #540 documented contract |
| Conditional setup | conditional-etag-vary-headers | External test/setup issue: [Setup, retry] | Preserve precise entry after four reproductions | #540 harness tracker |
| Tool setup | npm ci at pinned cache source | External setup issue: no lockfile | No-lock, scripts-disabled npm install | Fixed lane setup |

The Age "suffix" case actually sends `7200, 0`, not `123junk`; the old issue
diagnosis was corrected from the current source. Independent tests also reject
numeric suffix garbage, signs, negatives and decimals.

## 4. Expires

The old path let permissive Go parsing admit malformed hour/whitespace and
selected the earliest repeated date. The new parser accepts exact IMF-fixdate,
RFC850 and asctime representations, compares spelling case-insensitively,
uses UTC and corrects RFC850 years more than 50 years in the future. Input is
bounded to 33 bytes. Invalid/empty/zero/impossible/non-GMT/repeated values
cannot produce freshness. max-age/s-maxage precedence and Date-based clock
skew remain intact. `TestStrictExpires`, `TestFreshnessPrecedence` and
`TestAgeFreshBoundary` prove formats, error cases and exact transitions.

## 5. Age

RFC 9111 section 5.1 recommends the first member of a list-based singleton
and ignoring an invalid selected value. Jul now selects before the first comma
in the first field line, trims SP/HTAB only, validates every digit, and ignores
all later values. Saturation occurs during accumulation, but invalid trailing
bytes still invalidate the value; overflow cannot wrap. Date-based apparent
age survives ignored Age. The same logic restarts age on 304 validation.
Tests cover first-valid/later-invalid and inverse combinations, lists,
whitespace, signs, decimals, huge integers, Date interaction and boundaries.

## 6. RFC 9213 design

`github.com/dunglas/httpsfv` v1.1.2 is a BSD-3-Clause, no-runtime-dependency
Structured Fields implementation with official-suite and fuzz tests. There was
no suitable existing Jul parser. It materially reduces grammar/security risk
without introducing another caching engine. Context7 was unavailable; upstream
documentation and installed source were reviewed.

The internal ordered target list contains only CDN-Cache-Control. Combined
input is capped at 16 KiB/128 field lines before parser allocation. A valid
non-empty dictionary replaces both generic Cache-Control and Expires;
absent/empty/syntax-invalid input falls back. Unknown extensions are ignored
semantically but never turn a selected dictionary into "absent". Parameters
are ignored; duplicate dictionary members follow last-wins semantics. Wrong
directive value types are not coerced. Valid integer durations saturate;
invalid Structured Fields numeric overflow invokes generic fallback.

Supported directives: max-age, s-maxage, must-revalidate, proxy-revalidate,
no-store, no-cache, private, public, stale-while-revalidate and stale-if-error.
Field-qualified no-cache validates the whole representation; private is never
shared. The same policy representation controls publication, authentication,
SWR/SIE, validation and 304 changes. s-maxage includes proxy revalidation in
both generic and targeted policy. Normal downstream forwarding is unchanged,
including the selected header, Surrogate-Control and end-to-end age metadata.

CDN-Cache-Control: implemented. Jul-CDN-Cache-Control: not introduced.
Surrogate-Control: not interpreted or translated; normal sanitation applies.
No extra public setting or speculative target is introduced.

## 7. Cache conformance result

```text
required before: 144 / 160
required after:  155 / 160
runner before:   146 / 162 (includes 2 custom validator checks)
runner after:    157 / 162 (includes 2 custom validator checks)
```

Both final full runs: five listed failures, zero unexpected, zero stale.
One setup retry is separately reported in each of four runs (baseline plus
three post-change runs). Required residuals:

| Case | Owner | Reason |
| --- | --- | --- |
| partial-use-headers | #442 | Deliberate range bypass |
| partial-use-stored-headers | #442 | No stored complete-object range serving |
| headers-store-Set-Cookie | #540 | Never share per-client cookie state |
| headers-store-Transfer-Encoding | #540 | Reject invalid origin framing with 502 |
| conditional-etag-vary-headers | #540 | [Setup, retry], before actual assertion |

Optimal/check cases are informational and not included in the required count.

## 8. Range caching

No range serving, fragments, synthesis, slice parity, fill locks, large-object
disk capture or range eviction was implemented. Range/If-Range still bypass
lookup/storage, preserve origin 200/206/416, Content-Range, validators and
bytes, report BYPASS and leave an existing complete entry intact. #442 remains
the future evidence-gated owner; #521/#525 remain separate work.

## 9. Issue #539 matrix

T/P below means both static and proxy paths. Minimal-server "proxy" labels
exercise the same trivial handler at /proxy/: intentionally no Jul proxy.

| Named h2spec case | Jul before | Minimal stdlib | Jul final | Classification | Fix/residual rationale | Upstream reference |
| --- | --- | --- | --- | --- | --- | --- |
| 3.5 invalid preface | h2c T/P fail, TLS pass | Same | Same | Upstream Go behavior outside safe Jul control | Invalid h2c sniffing closes without GOAWAY; handler not reached | Go 1.26.8 server negotiation; standalone source |
| 4.2 oversized HEADERS | All fail | Same | Same | External test/harness issue | Fixed 5x4000 dummy values do not exceed advertised 1 MiB; genuinely larger frames fail safely | h2spec FrameSize/DummyHeaders; Go maxReadFrameSize |
| 6.5.3 duplicate window SETTINGS | All fail | Same | Same | Upstream Go behavior outside safe Jul control | processSettings rejects duplicates before any mutation; peer-only availability cost | h2_bundle.go processSettings/settings_big_or_dups |
| 8.1.2.2 connection-specific field | All fail | Same | Same | Upstream Go behavior outside safe Jul control | Go-generated HTTP 400 before application, not a stream protocol error | [golang/go#26321](https://github.com/golang/go/issues/26321), checkValidHTTP2RequestHeaders/new400Handler |
| 8.1.2.2 invalid TE | All fail | Same | Same | Upstream Go behavior outside safe Jul control | Same fail-closed HTTP 400 response before application | [golang/go#26321](https://github.com/golang/go/issues/26321) |

Final: TLS/static 142/146, TLS/proxied 142/146, h2c/static 141/146,
h2c/proxied 141/146; total 566/584, 18 listed failures, no unexpected or stale.
Distinct observed failures: first baseline six, final five. The extra baseline
content-length case appeared once in h2c/static and once on a different target
in the standalone 16 KiB experiment, but not either post-change Jul run. It
remains an unsuppressed timing-sensitive observation, not an asserted fix.

Minimal stdlib, x/net/http2 v0.59.0 and newer Go 1.27.1 all reproduce the five
historical groups. A supported MaxReadFrameSize=16384 experiment makes 4.2
pass by making its frame illegal, not by fixing a demonstrated server bug;
Jul's configuration is preserved. No supported switch for the other four
was found, and switching server architecture gains no conformance benefit.

Reproducer: `go run ./testdata/conformance/h2stdlib -h2spec /path/to/h2spec
-out /tmp/h2stdlib`; add `-implementation xnet` or `-max-frame 16384` for
the comparisons, or prefix `GOTOOLCHAIN=go1.27.1` for the newer release.
No protocol fork, monkey-patching or raw production frame parser is introduced.

## 10. HTTP/2 security impact

The raw-frame test uses Jul's actual listener setup. It proves compressed
headers beyond the configured bound cannot reach the application, an above-
advertised frame is rejected, connection-specific/illegal TE fields never reach
the backend, malformed prefaces do not enter Jul's handler, and eight bad
SETTINGS peers leave a new healthy stdlib h2c peer operational. Tests pass
three repeats and the full-tag race detector.

No authentication, authorization, WAF, rate-limit, body-limit, backend TLS/trust,
header sanitation, connection-accounting, timeout, reload or graceful-drain
wiring changes. Invalid header requests are rejected before that stack rather
than bypassing it to reach a backend; valid admitted requests retain the same
stack. Existing full-tag/security/race tests remain green. No new resource-
bound bypass was found. Peer-only SETTINGS interoperability remains explicit.

## 11. NGINX migration impact

Reviewed: yes. Disposition: **approximated**. Standard NGINX proxy caching
does not consume CDN-Cache-Control; Jul now does. NGINX consumes X-Accel-Expires,
including zero disabling storage; Jul does not. Generic Expires behavior is
proved on both runtimes. NGINX `expires` header generation remains #523 work,
not targeted-policy selection.

Assessment guidance changed: NGX_LOCATION_CACHE_VALID (still approximated)
and NGX_LOCATION_CACHE_IGNORE_HEADERS (still blocking). Single-zone enablement,
candidate mapping and manifest/goldens remain unchanged and pass their checks.
The existing cache-runtime fixture README/category evidence was updated.
The supported generator verifies inventory; no generated file was hand-edited.
Six real paired stateful cases now run in make nginx-migration-e2e against the
existing pinned NGINX 1.28.3 digest, with no new image or network dependency.
Docs explain origin-header review before cutover and privacy policy for each
cache class. No general #523 work is absorbed.

## 12. Tests

| File | Purpose |
| --- | --- |
| internal/cache/policy_test.go | Strict dates/Age, saturation, boundaries, SF grammar/types/duplicates, target selection/precedence, stale/auth/304 policy, fuzz target |
| internal/cache/merge_test.go | Correct first-member Age during validation |
| internal/handler/cache_conformance_test.go | Real-origin targeted HIT/MISS/validation/forwarding, auth and Vary isolation, invalid TE rejection |
| internal/server/h2c_test.go | Real Jul raw-frame safety/peer isolation |
| cmd/jul/corpus_runtime_test.go | Six real NGINX/Jul stateful cache-policy comparisons |
| cmd/jul/import_corpus_test.go | Existing synthetic origin extended for those cases |
| scripts/conformance/test_run.py | Exact/wildcard stale reporting, unexpected failures and unexercised entries |
| testdata/conformance/h2stdlib/main.go | Standalone stdlib/xnet/upgraded-Go ownership reproducer, not runtime code |

Existing range, memory/disk, SWR/SIE, authenticated reuse, middleware, security,
reload, revalidation and generation tests were preserved and run in full-tag,
coverage and race gates.

## 13. Fuzzing

```sh
go test ./internal/cache -run '^$' -fuzz '^FuzzCachePolicy$' -fuzztime=30s -parallel=2
```

PASS, 228,181 executions. Seeds include malformed/repeated dictionaries,
delimiters, quoted/escaped values, duplicate members, huge numbers, long input,
invalid octets and integer boundaries. Invariants: no panic/hang, bounded
durations, malformed target cannot override generic no-store, and bounded
selection before SF parsing. The same fuzz path exercises strict Age and dates.

## 14. Coverage

**Changed/added production statement coverage: 100.00% (70/70).**

Measurement uses `git diff --unified=0 <starting SHA> -- '*.go'` filtered by
`python3 scripts/strip_moved_lines.py`, intersecting added/modified production
lines with full-tag Go coverage statement blocks, using the same moved-line
and statement-block method as the existing issue408-quality gate. Test files
and the testdata-only standalone reproducer are not production. No meaningful
runtime code is excluded, no threshold lowered, no nocover escapes or moves.

```sh
go test -count=1 -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" -coverprofile=/tmp/jul-conformance-after-full.cover ./...
git diff --unified=0 7d24bb5203fcc750bef0f1460a894964f93edc34 -- '*.go' | python3 scripts/strip_moved_lines.py
```

Before profile came from an isolated detached worktree at the starting SHA,
with the same full-tag command/toolchain. Counts are package-local statements,
not cross-package execution inflated by coverpkg.

| Full-tag scope | Before | After |
| --- | ---: | ---: |
| Repository | 31,141/35,455 = 87.8325% | 31,191/35,511 = 87.8348% |
| Cache | 999/1,061 = 94.1565% | 1,044/1,104 = 94.5652% |
| Handler | 909/1,005 = 90.4478% | 909/1,005 = 90.4478% |
| Server | 1,678/1,898 = 88.4089% | 1,679/1,898 = 88.4615% |
| NGINX importer | 2,259/2,480 = 91.0887% | 2,259/2,480 = 91.0887% |

Security-package floors passed unchanged: plugins 90.2%, RBAC 80.9%, WAF 82.8%.

## 15. Documentation

- docs/cache.md: strict metadata, RFC 9213 policy/types/extensions/bounds,
  examples, forwarding/security decisions and executable matrix.
- docs/security-testing.md: exact counts, tool provenance, residuals,
  standalone/security/fuzz/coverage evidence.
- docs/known-limitations.md: truthful H2, Surrogate-Control, range and
  migration boundaries.
- docs/nginx-importer.md: approximated origin policy and cutover guidance.
- docs/nginx-migration-corpus.md and cache-runtime/README.md: real paired
  evidence and preserved fixture scope/provenance.
- CHANGELOG.md: Added/Fixed entries without claiming H2 runtime conformance.
- This report: durable engineering evidence. README and feature-status did
  not need changes; no maturity/config/API authority changes or new Console
  feature row. Existing cache status/config examples remain applicable.

## 16. Conformance allow-list

### Removed

- freshness-expires-invalid-1-digit-hour
- freshness-expires-invalid-multiple-spaces
- freshness-expires-invalid-multiple-lines
- age-parse-prefix-twoline
- age-parse-suffix
- cdn-* (covers cdn-fresh-cc-nostore, cdn-max-age-0-expires,
  cdn-max-age-long-cc-max-age, cdn-no-cache, cdn-no-store-cc-fresh, cdn-private)

### Remaining

The five required cache entries in section 7 are exact. Range entries can be
removed only after an activated #442 design meets its evidence/security gate;
cookie/framing entries only after a deliberate security-reviewed contract or
suite correction; conditional setup only after consistent successful setup.

The five exact named H2 entries in section 9 use only their target suffix
wildcards; none was widened. #539 owns rechecks: four require corrected safe Go
behavior, 4.2 requires a corrected suite assumption or genuine changed behavior.
The reporter now detects wildcard staleness when all matched targets pass.

## 17. CI / verification

Passed local commands (full tag set as above):

- Unchanged `go test -count=1 -coverprofile=... ./internal/cache ./internal/handler ./internal/server`.
- Focused freshness, target policy, real-origin, invalid framing and H2 tests;
  real-origin/H2 matrices repeated three times.
- `python3 scripts/conformance/test_run.py` (four tests).
- Full external cache: one baseline, three post-change; final two both 155/160.
- Full external h2spec: baseline and two post-change Jul runs; standalone
  stdlib, 16 KiB experiment, xnet and newer Go 1.27.1.
- `python3 scripts/conformance/run.py framing ...` (27/27).
- `make ci-fast ci-full ci-pr` (shared prerequisites), including full-tag
  tests/lint/vet, vulnerability/license/security floors and shipped configs.
- `make test-race` (entire full-tag repository, passed; plugins took 482.939s).
- `make generated-check`, `python3 scripts/docs-check.py`,
  `python3 scripts/test_docs_check.py`, `python3 scripts/test_semantic_drift.py`.
- `go run -tags importer scripts/nginx-corpus-report.go -check`.
- `make nginx-corpus-check`, `make nginx-migration-e2e` (real NGINX available).
- Full-tag baseline/final repository coverage, local fuzz smoke and diff check.

Development failures were repaired and rerun: test fixture compile/contract
errors, Python indentation, NGINX scratch setup, and deprecated test transport
lint. Initial npm ci failure was a reproduced upstream setup issue and fixed in
the lane. No gate was bypassed. Remote PR checks are separate; consult the PR's
exact head status, not this local record, before merge.

## 18. Files changed

Production: cache policy/date/Age and freshness selection only; NGINX assessment
guidance strings; small SF dependency. Tests/reproducer: section 12. Harness:
explicit cache/H2 totals, wildcard staleness, npm setup, paired migration gate.
Docs/evidence: section 15, allow-list cleanup and corpus category evidence.
No listener runtime, auth, lifecycle, config, metrics, frontend or generated
authority changes. Temporary task/worktree helpers are not deliverables.

## 19. Backlog impact

| Issue | Update disposition |
| --- | --- |
| #540 | Completion evidence and conditional closure after green PR checks |
| #539 | Detailed current evidence; leave open for upstream/test rechecks |
| #442 | No scope/decision change; range deferral accurately retained |
| #521 | No metric work or new prerequisite; unchanged |
| #523 | Inform material CDN origin-policy difference only; no expires implementation |
| #525 | No fill-lock/large-object work; unchanged |
| #62 | Execution update only, not a roadmap rewrite |

No speculative Surrogate-Control or unrelated implementation issue created.

## 20. Residual risks

- Medium: origin policy that previously ignored CDN-Cache-Control in NGINX
  can now store or not store differently in Jul; documented and proved on real
  runtimes. Operators must review personalized/cookie-authenticated origin
  policy before cutover.
- Low: Go H2 error-shape/duplicate-settings interoperability remains; no
  demonstrated security/resource-bound bypass, exact ownership and upgrade rule.
- Low: timing-sensitive external content-length observation and conditional
  setup retry can recur; neither is claimed fixed or broadly hidden.

No new critical/high runtime risk was demonstrated.

## 21. Final verdict

```text
#540 ready to close: YES after green required PR checks/review; not before
#539 ready to close: NO (four upstream groups and one exact test assumption remain)
Changed-production coverage >=90%: YES (100.00%)
Cache conformance state acceptable: YES (five precise, owned residuals)
HTTP/2 residual state acceptable: YES (bounded, reproduced, explicitly owned)
NGINX migration truth updated: YES (approximated, real paired E2E passed)
Documentation complete: YES
CI ready: local gates YES; remote PR checks must be verified on exact head
PR ready to merge: only after required remote checks and maintainer review
```

This work does not authorize merging the PR or prematurely closing #539.