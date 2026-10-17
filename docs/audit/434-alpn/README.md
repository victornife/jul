# #434 TCP ALPN feasibility and delivery evidence

## Merged delivery — 2026-10-18 Europe/Madrid

[PR #635](https://github.com/victornife/jul/pull/635) merged with explicit
maintainer authorization at 2026-10-17T23:13:59Z as
[`1b462ce46659275db38964a25436a6eac9c022cf`](https://github.com/victornife/jul/commit/1b462ce46659275db38964a25436a6eac9c022cf).
Reviewed head: `fbaa69a8594edc3dd99d82472989aeb23e4c6b37`; base:
`d0f30218c46743f274a9f7280f1ebf2c82a78c5f`. The merge and reviewed head share
tree `0949db9d332dbb5338a2d69c7b2fd94ac04b674d`.

Reviewed-head checks completed with 65 successes and two expected website
deployment skips; Codecov and Qlty passed. Native profiles, race, browser,
static/generated/docs and the existing short smoke passed. Hosted fuzz smoke
passed all 35 targets; ALPN completed 71,818 executions in 21.013s without
failure. All nine pinned NGINX ALPN cases and the real-Jul corpus passed.
Changed production statement coverage is Go 165/166 (99.39759036144578%) and
frontend 27/29 (93.10344827586206%), each strictly greater than 90%.

Sources: [reviewed-head certification](https://github.com/victornife/jul/pull/635#issuecomment-6242787219),
[CI](https://github.com/victornife/jul/actions/runs/39076149316),
[changed-code coverage](https://github.com/victornife/jul/actions/runs/39076149292),
[pinned NGINX](https://github.com/victornife/jul/actions/runs/39076149283).
Separate [post-merge CI](https://github.com/victornife/jul/actions/runs/39078217836)
and its final disposition are tracked in the [issue closure](https://github.com/victornife/jul/issues/434).
Reviewed-head results do not assert a separate completed execution of main CI.
#434 is closed/completed. Maturity remains Alpha; this feature is unreleased.

Codecov's separate line-patch metric is 98.24561% against its 90% target.
Its red bot-comment marker flags three uncovered lines; the actual reviewed-head
check and merge-commit status are successful. The merge-commit upload completed
successfully. This line metric is distinct from the Go/frontend statement
denominators above.

## Phase 0 — GO (2026-10-17)

Base: `d0f30218c46743f274a9f7280f1ebf2c82a78c5f`.
Parser spike: [`b0b10a3bf31438419dede1044aa4081ae0567230`](https://github.com/victornife/jul/commit/b0b10a3bf31438419dede1044aa4081ae0567230) (no configuration/API changes).
This is a bounded implementation decision, not a release/GA certification.

The concrete demonstrator is two TLS services sharing `shared.example` and one
TCP listener: an application offering `jul-demo/1` routes to an application TLS
backend; `http/1.1` routes to an independent web TLS backend. Each backend owns
its TLS key/certificate and negotiation. SNI cannot distinguish these clients.
This is an engineering demonstrator, not evidence of external customer demand.
ALPN is an offer list, never proof of negotiation or gRPC traffic.

### Predeclared budgets and measurements

See [the budget](phase0-budget.txt), [baseline](phase0-base-bench.txt) and
[spike results](phase0-head-bench.txt). Go 1.27.2, linux/amd64, shared Xeon 8573C,
GOMAXPROCS=2, six 300ms samples per benchmark, sequential base then spike.
The identical harness differs only in calling legacy `peekSNI` versus
`peekClientHello(..., true)`; its padded ClientHello offers h2 and http/1.1.
The harness is committed in `internal/stream/alpn_phase0_bench_test.go`.
For baseline reproduction copy it to the base checkout and substitute
`_ = peekSNI(br)` in `phase0Inspect`. No production source changes at base.

| Measurement | Base median (range) | Spike median (range) | Result |
| --- | --- | --- | --- |
| Buffered preread | 264.5 ns (220.1–392.3) | 326.15 ns (315.6–410.6) | +61.65 ns, below +2 µs |
| Preread allocations | 16 B / 1 allocation | 64 B / 4 allocations | +48 B / +3, within budget |
| Fresh TCP + preread + backend dial + echo | 157.155 µs (155.173–246.258) | 165.3605 µs (160.828–172.628) | +8.2055 µs (+5.22%), below max(10%,25 µs) |

The connection benchmark includes a fresh connection and complete preread
forward/replay, not bulk throughput. It excludes TLS cryptography and is not a
production latency guarantee. Shared-host variation is visible, especially the
baseline outlier; median comparison is the predeclared method.

### Safety evidence

- [Parser tests](phase0-tests.log) and [race results](phase0-race.log): pass.
- [Fuzz run](phase0-fuzz.log): 30-second budget, 54,958 executions, no failure;
  positive seeds assert ALPN extraction before fuzzing.
- Every two-record cut and one-byte reads preserve the exact input and payload.
- Fixed limits remain 16,384 handshake bytes, 16,384 record payload bytes,
  64 records and 16,704 preread bytes. ALPN: at most 64 offers of 1–255 bytes.
- Invalid lengths, empty identifiers, duplicate extensions, truncation, timeout,
  non-TLS and cap violations yield no partial ALPN routing metadata.
- Configurations without ALPN inspection retain the previous SNI-only handling
  of malformed/duplicate ALPN bodies. The existing SNI fuzz fixture was malformed;
  its length fields were fixed and a positive seed assertion added.
- No raw offered values are logged or exposed as metric labels.

### Commands

```sh
go test -tags stream ./internal/stream -run 'TestALPN|TestSNI|TestParseClientHello|FuzzPeek' -count=1
go test -race -tags stream ./internal/stream -run 'TestALPN|TestSNI|TestParseClientHello|FuzzPeek' -count=1
go test -tags stream ./internal/stream -run '^$' -fuzz '^FuzzPeekALPN$' -fuzztime=30s -parallel=2
go test -tags stream ./internal/stream -run '^$' -bench '^BenchmarkALPNPhase0' -benchtime=300ms -count=6
```

## Baseline limitations

Repository-wide instrumentation (`-coverpkg=./...`) was attempted separately on
base lean/full profiles. Both runs fail pre-existing tests: Unix-domain sockets
are denied (`operation not permitted`), and process resource fields are unavailable.
The observed profiles aggregate unique statement spans, not package averages:
lean 28,067/31,859 = 88.09755485106249%; full 34,889/39,269 =
88.84616364053070%. These failing constrained runs are not successful baseline
certification. On 2026-10-18 the maintainer clarified the coverage contract:
strictly >90% applies to added/modified executable production code. Global and
whole-package percentages are retained as informational measurements; existing
repository CI gates remain in force.
The pinned real-NGINX harness also requires Docker, which is unavailable locally.

## Routing contract selected after the spike

Use `[[stream.alpn_routes]]` entries with `server_name`, `protocols` and
`proxy_pass`. Exact SNI+ALPN entries precede wildcard (`server_name = "*"`)
ALPN entries; within either tier the first configured matching rule wins,
regardless of client offer order. Existing exact SNI-only, SNI catch-all and
default `proxy_pass` follow. Inspect once per connection. Existing connections
keep their original immutable route snapshot across reloads.

Native rules compare complete, case-sensitive identifiers. Wire identifiers
are opaque; configuration identifiers are UTF-8 TOML strings encoded verbatim
(1–255 bytes). Unrepresentable opaque identifiers remain unmatched. No joining,
case folding, substring matching or silent truncation is allowed.

HTTP/3/QUIC passthrough inspection, stream TLS termination, payload inspection
and general map/expression support are excluded. Existing HTTP/3 termination
is unaffected.

## Delivery acceptance matrix

| Contract | Implementation and verification | Disposition |
| --- | --- | --- |
| Fragmented/bounded ALPN inspection | `sni.go`, ALPN/SNI envelope, cap, replay and fuzz tests | Phase-0 GO; fixed byte/record limits preserved |
| Exact identifiers and deterministic priority | `alpn.go`, `TestALPNRuleSelection` | Exact-SNI then wildcard ALPN tiers; source order; client order ignored |
| Legacy compatibility | `TestALPNMalformedAndLegacyCompatibility` plus existing SNI/TCP/UDP tests | Configs without ALPN retain ignored-extension behavior |
| Backend-owned TLS and payload | `TestALPNRealTLSNegotiationReloadAndAdmission`, `TestALPNTCPReplayIncludesSubsequentPayload` | Backend certificates and negotiation proved independently from selected route |
| Snapshot/reload/admission | Real TLS reload/admission test; invalid-pool atomicity test | Established sessions retain pool and admission ownership; invalid candidates roll back |
| Config/API/lifecycle/Console | `validate_alpn*`, generated schema/value/lifecycle/OpenAPI, projection/patch/diff tests and UI tests | Guided edits preserve rules; omission preserves for older clients, explicit empty clears |
| NGINX migration decision | `NGX_STREAM_ALPN_MAP_AMBIGUOUS`, manual guidance, provenance/corpus fixture | Automatic maps remain blocking; no source form falsely claimed equivalent |
| Real NGINX comparison | Required pinned `nginx-alpn-e2e.sh` lane and `TestALPNNGINXReference` | All nine reviewed-head cases and the real-Jul corpus pass in hosted CI; Docker unavailable locally |
| Documentation and status | Root/index/config/stream/reload/migration guides, generated contracts, roadmap/status, example and unreleased changelog | Alpha/merged/unreleased, separate from released L4 GA |
| Coverage | Complete Go diff and separate lean/full main-module instrumentation; changed frontend statement spans | Strict >90% complete changed-code gates for Go and frontend; global/package measurements informational |
| Final reviewed-head CI | PR #635, exact-head checks and source-bound artifacts | 65 success, two expected skips; Codecov and Qlty pass; merged with maintainer authorization |

### Additional observed CI finding

The initial spike head `b0b10a3` passed ordinary tests, race, platform, lint,
fuzz, benchmarks and UI checks, but the short reproduction smoke failed with
exit 141 after successful HTTP/TCP load and retained collection: [job evidence](https://github.com/victornife/jul/actions/runs/39071875586/job/117510996338).
The final summary pipe used `grep -q`, closing its input after a match under
`pipefail`. The adjacent one-line fix drains the full summary using ordinary
`grep` redirected to `/dev/null`. A 1 MiB producer regression completes, and a
producer that fails after a match still fails the pipeline. No duration gate
is reduced; no hour-long run is added. Initial changed-code coverage also
failed; the complete-envelope adversarial tests cover its reachable critical
rejection cases rather than excluding their statements.

The hosted docs lane also identified four stale D11 current asset paths after
the Console rebuild. D11-104 now records the exact old-to-new path mapping and
PR635 rebuild provenance while preserving its historical review and verdict.

### Closeout CI finding

The documentation closeout in [PR #636](https://github.com/victornife/jul/pull/636)
exposed an overstrict check introduced by #635: the ALPN delivery workflow treated
a documentation-only diff with no changed executable statements as missing
coverage ([failed frontend job](https://github.com/victornife/jul/actions/runs/39078586502/job/117530743913)).
Both language gates now record a zero denominator as not applicable, with no
invented percentage. Missing/empty coverage artifacts and missing changed-file
instrumentation still fail; any nonempty changed-statement scope must remain
strictly above 90%. Ten regression tests execute the actual workflow gate scripts,
covering empty scopes, exact/below/above-floor boundaries and missing evidence;
the existing seven Go scorer regressions also pass. This CI defect does not
change the implementation's measured denominators or runtime behavior.

### Reviewed surfaces without broader changes

- `SECURITY.md`: the feature-specific threat model is in `stream-alpn.md`; no
  global trust model, authorization guarantee or unbounded label is introduced.
- Historical release/audit/soak records: preserved as dated evidence. The
  historical 2026-09-29 no-go is retained and cross-referenced to this later run.
- Generic NGINX map support and QUIC: deliberately not activated. The correct
  migration disposition is blocking with specific evidence, not guessed output.
- Console general config editing: already accepts the typed TOML contract;
  this tranche adds route projection, count/guidance and safe guided-edit
  preservation rather than a second expression/config editor.

## Integrated implementation and evidence

The complete native integration merged in PR #635.
The maintainer explicitly authorized publication and merge on 2026-10-18,
resolving the previous generated-contract publication pause. Final reviewed-head
CI and the pinned NGINX reference lane passed, as recorded in the merged-delivery
checkpoint above. Earlier parser-spike CI remains historical evidence.
The generated metadata was checked against the
already-public base: all 348 existing fields, including security/credential
descriptions, are unchanged. Only three ALPN fields and schema counts differ;
see the [exact proposed diff](config-metadata-proposed.diff) and
[machine-readable field review](config-metadata-review.json).

The first integrated pinned-NGINX run exposed a harness startup assumption:
the official image has no `/usr/lib/nginx/modules/ngx_stream_module.so` file.
The reference fixture no longer attempts that dynamic load. The harness records
`nginx -V` and a required `nginx -t` before running, propagates endpoint-discovery
failures, and prints retained container diagnostics on failure. The image build
flags confirm static stream/preread support. The isolated internal network uses
bounded transparent Go TCP bridges on loopback, matching the existing corpus
network model instead of relying on unavailable Docker port publication. The current PR
checkpoint records the resulting pinned-runtime evidence.

The [complete changed Go statement report](changed-go-coverage.json) measures
**165/166 = 99.39759036144578%**, using the complete PR diff without a moved-line
filter. The single remaining unexecuted defensive return follows the fully
validated extension walk (`sni.go:180`); structural malformed/truncated envelope
cases are covered directly and through fuzzing.

The [local main-module profiles](local-coverage.json) include all production
packages via `-coverpkg=./...`, aggregate each statement span once, and keep
lean/full instrumentation separate:

| Scope | Base covered/total | Head covered/total | Head percentage | Whole-scope measurement |
| --- | --- | --- | --- | --- |
| Whole module, lean | 28,067/31,859 | 28,120/31,912 | 88.11732263725244% | informational |
| Whole module, full | 34,889/39,269 | 35,002/39,382 | 88.87816769082322% | informational |
| Stream, full | 562/630 | 630/685 | 91.97080291970803% | informational |
| Admin, full | 8,106/9,391 | 8,123/9,409 | 86.33223509405887% | informational |
| Config, full | 2,538/2,671 | 2,573/2,706 | 95.0849963045085% | informational |
| Lifecycle, full | 751/832 | 751/832 | 90.26442307692308% | informational |
| Config contract, full | 455/492 | 455/492 | 92.47967479674797% | informational |
| Importer, full | 2,700/2,886 | 2,705/2,891 | 93.56624005534417% | informational |
| Admin API, full | 57/57 | 57/57 | 100% | informational |

Neither global profile regresses versus its comparable baseline. These are
**constrained failing local runs**, not passing CI certification: baseline and
head both fail the Unix-socket and resource-stat tests described above. Per the
2026-10-18 maintainer clarification, global/admin percentages are informational,
rather than completion blockers. The CI workflow enforces exact integer >90%
for complete changed Go and frontend statements, rejects missing artifacts and
retains existing repository coverage gates. It measures the final head in both
lean/full profiles without repeating baseline test runs in this extra lane.

The [integrated runtime benchmark](runtime-bench.txt) measures the actual stream
accept/preread/routing/admission/backend-dial/relay path. Six sequential 300ms
samples on the same environment compare SNI routing with ALPN routing: medians
187.728 µs versus 192.249 µs (+4.521 µs, +2.41%), within the predeclared budget;
ALPN adds approximately 47 bytes and three allocations. The
[final fuzz run](final-fuzz.txt) used a 30-second budget, completed in 31.037s
and executed 331,049 cases without failure. No production/soak guarantee is
inferred from either measurement.

Locally passing checks include full-tag lint (zero issues), vet, dependency
vulnerability scan (zero reachable vulnerabilities), license, lean/full builds,
generated contract checks, all shipped example loads, 2,375 docs checks, focused
importer/corpus ALPN cases, and race tests for stream/config/lifecycle/admin/
importer. Console typecheck/lint/tests/coverage/build pass (79 files, 919 tests; final
coverage suite 38.75s), and the embedded
production bundle is regenerated. Native-platform and pinned-NGINX evidence
passed in hosted CI on the integrated published head; Docker is unavailable here.

The [changed frontend statement report](changed-frontend-coverage.json) measures
**27/29 = 93.10344827586206%** over complete diff statement spans. The overall
frontend statement metric is separately 4,651/6,178 = 75.28%; existing file,
contract and branch gates pass. The changed Go and frontend percentages are separate delivery gates.
Informational package measurements use exact package directories; unchanged
nested generator packages remain in the global denominator.

The two uncovered changed frontend spans are the existing failed-response return
in `client.ts` and the PROXY-protocol selector callback in `StreamsPanel.tsx`;
both remain counted. ALPN rule preservation, ALPN-only targets, projection,
priority and UDP rejection are asserted by the affected tests.

### Reproduction and source identity

Both base/head coverage profiles use Go 1.27.2, the same package selection and
`-coverpkg=./...`. No package is omitted because it has no tests. Exact scope
counts are in the linked JSON; fractional percentages are not rounded to pass.
The current integrated source and final CI identities are recorded in the PR
checkpoint; the Phase-0 source above remains historical evidence.

```sh
go test -p 2 -coverpkg=./... -coverprofile=lean.cover ./...
go test -p 2 -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' -coverpkg=./... -coverprofile=full.cover ./...
git diff --unified=0 d0f30218c46743f274a9f7280f1ebf2c82a78c5f -- '*.go' > complete.diff
python3 scripts/check-added-go-coverage.py --diff complete.diff --profile lean.cover --profile full.cover
pnpm --dir internal/admin/ui run test:coverage
make generated-check
python3 scripts/docs-check.py
```

### GitHub reconciliation

- [#434](https://github.com/victornife/jul/issues/434): closed/completed after
  the authorized merge; all implementation and migration acceptance criteria
  checked, with clarified changed-code coverage and source-bound evidence.
- [#62 checkpoint](https://github.com/victornife/jul/issues/62#issuecomment-6242114735):
  records the later selection and current verification status without rewriting unrelated programme
  details or historical release decisions.
- [#368](https://github.com/victornife/jul/issues/368#issuecomment-6242116168),
  [#426](https://github.com/victornife/jul/issues/426#issuecomment-6242117434), and
  [#367](https://github.com/victornife/jul/issues/367#issuecomment-6242118727): factual
  migration/reference cross-references only; all remain closed.
- [PR #635](https://github.com/victornife/jul/pull/635): merged, with exact source
  and reviewed-head CI while preserving historical spike and coverage measurements.

No release or maturity promotion occurred. HTTP/3/QUIC
passthrough inspection remains excluded. Mandatory acceptance work was not
moved into a follow-up issue to declare completion.
