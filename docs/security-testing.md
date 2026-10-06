# Security testing gates

Jul.IA uses focused, fail-closed test gates for security-sensitive packages in
addition to the repository-wide test, race, fuzz, vulnerability, E2E, and
coverage jobs. The dedicated gates are owned by issue
[#129](https://github.com/victornife/jul/issues/129).

## Scope

The dedicated gate covers packages whose incorrect fallback behavior can widen
a trust boundary or silently disable protection:

- `internal/rbac` — principal/token validation, role resolution, and
  deny-by-default policy construction;
- `internal/waf` — full/lean build capability enforcement, privacy-safe match
  handling, and firewall lifecycle behavior;
- `internal/plugins` — full/lean build capability enforcement, guest resource
  bounds, SSRF/DNS-rebinding protection, and global egress intersection.

The existing main CI coverage job retains its separate package floors for
`internal/config`, `internal/server`, `internal/auth`, and `internal/admin`.
Those general critical-package floors are not duplicated here.

## Admin route-policy drift guard

The dedicated lean/full negative-test lanes also run the admin `TestCatalog*`
guards and `TestRouteTransportPolicyInventory` (#514). The initialized route
catalog must declare exactly one authorization mode, known permissions and
complete per-method grants. Every route needs an explicit transport-policy
inventory entry; only the two probes are exempt. Each declared method is
checked through the real middleware for insecure transport, missing credentials
and a valid RBAC identity without grants. A source-level guard rejects
out-of-catalog `Handle`/`HandleFunc` registrations.

These tests do not change the dedicated package coverage floors or certify the
entire admin/Console codebase. Scope and remaining review are recorded in the
[dated security review installment](audit/2026-10-06-admin-security-review.md).

## Recorded full-tag baseline and floors

The initial baselines were measured from the exact full-tag coverage artifact for
`main@0de8541e0479bebd175e6ca7d3c47e2cd091ec74`, workflow run
`31044350343`, on 2026-08-05. The machine-readable authority is
[`scripts/security-package-coverage.json`](../scripts/security-package-coverage.json).

| Package | Recorded baseline | Enforced floor | Initial margin |
| --- | ---: | ---: | ---: |
| `internal/plugins` | 70.1% | 69.0% | 1.1 points |
| `internal/rbac` | 75.8% | 75.0% | 0.8 points |
| `internal/waf` | 73.0% | 72.0% | 1.0 point |

The floors are intentionally close to the measured baseline so deletion of a
meaningful test or an uncovered security branch fails CI. They are not set to
100%: generated glue, defensive impossible-state branches, and dependency
adapters are reviewed separately from the high-risk paths. A floor must never be
lowered merely to make a pull request pass.

## Negative-test matrix

The `Security package gates` workflow runs the same focused package set in both
lean and full-tag builds.

### RBAC

The matrix includes:

- enabled policy with no principal fails;
- empty principal token fails;
- unknown predefined/custom/default role fails;
- duplicate tokens, including legacy plus named principals, fail;
- disabled and expired principals do not authenticate;
- at least one admin-capable principal is required;
- predefined role permissions remain pinned and unknown permissions fail closed.

### WAF

The matrix includes:

- a lean build rejects enabled WAF configuration and direct construction;
- a disabled WAF remains acceptable in lean builds;
- global and per-location activation are both detected;
- full builds exercise rule compilation, block/detect behavior, reload churn,
  bounded logging, and query/macro privacy guarantees;
- filesystem normalization remains portable across slash conventions.

### WASM plugins

The matrix includes:

- a lean build rejects any configured plugin with actionable build-tag guidance;
- an empty lean plugin set remains a safe no-op;
- full builds exercise guest traps/panics, request/response body bounds, KV
  quotas, invalid status handling, and lifecycle cleanup;
- outbound guest fetch enforces plugin allow-lists, private-address rejection,
  DNS-rebinding defense, response caps, and the global egress-policy
  intersection.

## Local commands

Run the full focused gate with:

```bash
make security-gates
```

The equivalent commands are:

```bash
go test -count=1 ./internal/rbac ./internal/waf ./internal/plugins
go test -count=1 -run '^(TestRouteTransportPolicyInventory|TestCatalog)' ./internal/admin

go test -count=1 \
  -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" \
  ./internal/rbac ./internal/waf ./internal/plugins
go test -count=1 \
  -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" \
  -run '^(TestRouteTransportPolicyInventory|TestCatalog)' ./internal/admin

go test -count=1 -covermode=atomic -coverprofile=security-cover.out \
  -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" \
  ./internal/rbac ./internal/waf ./internal/plugins

python3 scripts/check-package-coverage.py \
  --profile scripts/security-package-coverage.json \
  --coverprofile security-cover.out

python3 scripts/test_check_package_coverage.py
```

The checker exits with:

- `0` when every package is present and at or above its floor;
- `1` when a measured package is below its floor;
- `2` when the manifest/profile is malformed or a required package is absent.

## Changing a floor

A floor increase should accompany new durable tests and should record the new
exact-SHA baseline in the manifest. A decrease requires a linked issue or pull
request explanation that identifies:

1. the removed or newly untestable statements;
2. why the reduction does not weaken a trust boundary;
3. the replacement validation, if any; and
4. the intended restoration trigger.

Do not add low-value assertions solely to move a percentage. Prefer tests that
prove a fail-closed outcome, a resource bound, a privacy invariant, or a
cross-build capability boundary.

## Relationship to other gates

Dedicated package floors do not replace:

- repository-wide default/full tests and the global coverage floor;
- the full-tag race detector;
- fuzz smoke targets for attacker-controlled parsers;
- `govulncheck`, dependency audit, CodeQL, and license checks;
- real-server protocol/browser E2E;
- release soak and long-running lifecycle evidence.

Security confidence comes from the combined evidence. Coverage is only one
regression signal, and a green percentage is never evidence that an untested
security contract is correct.

## Protocol conformance lane

The [`conformance`](../.github/workflows/conformance.yml) workflow (#513)
checks Jul against external protocol suites instead of hand-written
expectations. It runs weekly and on `workflow_dispatch`, and it is **not** a
pull-request gate. Each suite starts the full-profile binary with its own
configuration, uploads its report as an artifact, and fails only on a case
that is neither passing nor listed in
[`testdata/conformance/allowlist.yaml`](../testdata/conformance/allowlist.yaml)
with a rationale and an issue. An allow-listed case that starts passing is
reported as stale.

| Suite | Tool | Target |
| --- | --- | --- |
| `h2spec` | h2spec 2.6.0, built from its tag | HTTP/2 over TLS and h2c, static and proxied paths |
| `h3` | h3spec 0.1.13 | HTTP/3 and QUIC on a TLS listener with `http3.enabled` |
| `autobahn` | Autobahn\|Testsuite 0.8.2 (fuzzing client) | WebSocket through the reverse proxy to an echo server |
| `cache` | `http-tests/cache-tests` at a pinned commit | a cache-enabled proxy location; only `required` cases gate, `optimal` and `check` results are informational |
| `cache` (custom) | two checks | validators across content codings: a compressed response carries a weak `ETag`, and `If-Range` with it returns a full `200` (#504) |
| `framing` | [`testdata/conformance/framing.yaml`](../testdata/conformance/framing.yaml) | ambiguous HTTP/1.x framing: conflicting `Content-Length`, `Transfer-Encoding` variants, chunk syntax, obs-fold, bare LF, `Host` rules. Each case must be rejected (no complete request reaches the origin) or forwarded exactly as listed |

Run a suite locally with the tool it needs:

```sh
go build -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" -o /tmp/jul ./cmd/jul
python3 scripts/conformance/run.py framing --jul /tmp/jul --out /tmp/conformance
python3 scripts/conformance/run.py h2spec --jul /tmp/jul --out /tmp/conformance --h2spec /path/to/h2spec
```

The first local baselines (2026-09-30) found four Jul defects, all fixed:
`HEAD` through the proxy failed and marked the backend down (#534); a
malformed or oversized client body was counted as a backend failure;
a request with both `Transfer-Encoding` and `Content-Length`, or HTTP/1.0 with
`Transfer-Encoding`, kept the connection open so a trailing request was served
(RFC 9112 §6.3). The current residuals are scoped below; old diagnoses are not
the authority for their ownership.

### Cache and HTTP/2 remediation evidence (2026-10-06)

Starting source: `main@7d24bb5203fcc750bef0f1460a894964f93edc34`, Go 1.26.8,
Linux arm64. The [engineering report](reviews/2026-10-06-cache-h2-conformance.md)
records classifications, standalone comparisons, security boundaries, commands,
coverage, migration disposition, and closure conditions.

Cache-tests is pinned to `d644cf4bf487763646aac19d2c8b846daa0f604d`
(package version 0.4.5). There are **160 upstream required cases**, not 162:
the runner adds two Jul validator checks. Required cases improved from
**144/160 to 155/160**; including the custom checks, **146/162 to 157/162**.
The baseline and five post-change runs each reported one separate
`conditional-etag-vary-headers: [Setup, retry]` signal. Both final runs had
zero unexpected failures and zero stale entries after allow-list cleanup.

| Remaining required case | Owner | Reason |
| --- | --- | --- |
| `partial-use-headers` | #442 | Range/If-Range intentionally bypass lookup and storage |
| `partial-use-stored-headers` | #442 | No complete-object or fragment range serving |
| `headers-store-Set-Cookie` | #540 | Deliberately never shared; conservative security contract |
| `headers-store-Transfer-Encoding` | #540 | Invalid origin framing rejected with 502, never guessed/stored |
| `conditional-etag-vary-headers [setup-retry]` | #540 | Only exact `["Setup", "retry"]` is suppressed; any assertion failure uses the unsuffixed ID and fails the lane |

`test_cache_setup_retry_does_not_suppress_assertion_failures` proves the
reason-specific exception, including rejection of longer look-alike results.
Successful ordinary execution reports the retry entry as stale without adding
a synthetic case or changing the required totals. These reporter tests run
in the existing Python CI regression job.

H2spec source is tag v2.6.0, commit `70ac2294010887f48b18e2d64f5cccd48421fad1`,
with 146 cases per target. Building the tag without linker version injection
prints an old v1.5.0 CLI version; source tag/SHA is the tool provenance.

| Target | First baseline | Final |
| --- | ---: | ---: |
| TLS/static | 142/146 | 142/146 |
| TLS/proxied | 142/146 | 142/146 |
| h2c/static | 140/146 | 141/146 |
| h2c/proxied | 141/146 | 141/146 |
| Total | 565/584 | 566/584 |

Five historical named cases persist (18 target failures). The first baseline
also observed one content-length case in h2c/static; it did not recur in either
post-change Jul run, and a minimal Go frame-size experiment showed it on a
different target. This is a timing-sensitive observation, not a fixed Jul bug
or a newly allow-listed case. Stable distinct failing cases remain five;
the first baseline's observed distinct count was six, final five.

Go 1.26.8 stdlib, x/net/http2 v0.59.0, and newer Go 1.27.1 standalone servers
reproduce all five historical groups. The forbidden-header/TE cases are
**Go-generated HTTP 400 before Jul's handler**, not normally served requests;
see [golang/go#26321](https://github.com/golang/go/issues/26321).
Duplicate SETTINGS are rejected before mutation, affecting only that peer.
The oversized-HEADERS case uses five fixed 4000-byte header values, below
Go's advertised 1 MiB frame maximum, so it is an **external test assumption**,
not evidence of a frame-bound bypass. A real above-advertised frame and a
compressed above-configured header list are rejected by the Jul regression.

`TestH2CConformanceSecurityBoundaries` proves malformed prefacing, prohibited
headers and header bounds do not reach Jul's handler/backend, and repeated bad
SETTINGS peers cannot disrupt a fresh healthy H2 peer. Existing middleware,
backend trust, body limits, connection accounting and lifecycle are unchanged.
HTTP/1.x framing passes 27/27 with no suppressions. Recheck every Go upgrade;
wildcard entries now become stale when every matched target passes.

`FuzzCachePolicy` completed 228,181 executions in the local 30-second smoke
without panic or bounds violations. Changed production statement coverage is
100.00% (74/74) using `scripts/strip_moved_lines.py` and the full-tag coverage
profile; repository coverage is 87.8325% before and 87.8333% after. No coverage
floor, Codecov threshold or meaningful code exclusion was changed.
