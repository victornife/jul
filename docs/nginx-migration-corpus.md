# NGINX migration compatibility corpus

> Programme issue: **#154** · Build tag: `importer` · Fixture schema: **1**

The migration corpus turns importer support claims into reviewable, executable
evidence. It does not produce a compatibility percentage and it never labels an
entire configuration equivalent. Every runtime verdict is limited to the
response dimensions named by one scenario.

## Completed evidence baseline

Issue #154 closes with a bounded evidence programme rather than an open-ended
parity mandate. The checked-in baseline contains:

- **11 repository-owned fixtures**: 9 in the core tier and 2 in the full tier;
- exact schema-v2 assessment goldens with provenance for 127 directive results;
- strict-valid candidate assertions for every non-blocking fixture;
- supported, approximated, ignored, informational, and blocking evidence;
- safe real-Jul replay plus a pinned, isolated official NGINX 1.28.3 reference
  lane for the reviewed HTTP scenarios;
- a machine-checked minimum-category matrix in
  `testdata/nginx-corpus/coverage.json`;
- a deterministic, non-scoring aggregate in
  `testdata/nginx-corpus/inventory.json`;
- explicit deferred dimensions and objective revisit triggers instead of a
  universal compatibility claim.

The pinned reference lane executes the multi-file return/header fixture and the
routing/CORS/`limit_except` fixture. Other fixtures provide assessment and
candidate evidence unless their manifest contains an approved runtime scenario. A
reviewed NGINX-side expectation is not represented as a captured process result, and a Jul observation must still
match its separately declared expectation exactly.

The final acceptance record is
[NGINX migration corpus closure](audit/2026-08-31-nginx-migration-corpus-closure.md).

## Corpus admission policy

Core fixtures are repository-authored or generated from repository-owned source.
A fixture derived from a public external example must identify its HTTPS source
and compatible license and receive an explicit privacy/licensing review before
merge.

The corpus must not contain:

- proprietary or user-supplied NGINX estates;
- production hostnames, addresses, paths, certificates, credentials, or request
  bodies;
- copied private configuration;
- private keys or unreviewed binary artifacts;
- an externally derived sample without documented origin and license.

Every fixture is self-contained below `testdata/nginx-corpus/<id>/`. Symlinks,
non-regular files, files larger than 1 MiB, and high-confidence private-key
markers fail validation.

## Safe replay policy

The core lane sends only synthetic loopback traffic. The manifest grammar
allows `GET`, `HEAD`, and `OPTIONS`. `POST` is accepted only when the scenario is
explicitly marked `side_effect_free`; other methods are rejected. Request paths
must use origin form and cannot carry a scheme or authority. Authorization,
cookie, token, secret, and API-key headers are forbidden.

`corpus.NewRequest` independently rejects a non-loopback base URL. The harness
therefore cannot be redirected to a production or external endpoint by fixture
data.

## Selected comparison dimensions

A scenario lists each asserted dimension:

| Dimension | Compared value |
| --- | --- |
| `status` | HTTP status code. |
| `headers` | Only the lowercase names listed in `assert_headers`; values are trimmed and sorted. |
| `body` | Exact response bytes interpreted as UTF-8 fixture text. |
| `body_sha256` | `sha256:<lowercase hex>` of a bounded body. |
| `redirect_target` | Normalized `Location` value. |

Unlisted headers and dimensions make no equivalence claim. `Date`, intentional
`Server` differences, request IDs, ephemeral ports, timing, semantically
irrelevant header order, and encoded compression bytes can be omitted or ignored
only by the scenario contract; they are not silently normalized into equality.

For `expected_difference`, the manifest must contain a separate Jul expectation
that differs from the NGINX-side reference on an asserted dimension and must
name a stable difference code. A real Jul observation that differs from its Jul
expectation is always `unexpected_difference`.

## Fixture contract

Each fixture contains:

```text
testdata/nginx-corpus/<id>/
├── README.md
├── manifest.json
└── nginx/
    ├── nginx.conf
    └── ... included fragments
```

`manifest.json` records:

- schema version, stable fixture ID, core/full tier, categories, and build tags;
- origin kind, license, and source;
- root file and whether bounded include traversal is enabled;
- exact assessment-result multiset and source-policy expectations;
- selected canonical candidate substrings, or an explicit statement that no
  safe candidate is required;
- zero or more safe runtime scenarios.

The assessment golden deliberately ignores human prose. Messages may improve
without changing the stable finding contract, while any added, removed, or
reclassified result fails the fixture.

## Pinned NGINX reference lane

The isolated reference lane uses the Docker Official Image
`nginx:1.28.3-alpine` pinned to the immutable multi-platform index digest
`sha256:a8b39bd9cf0f83869a2162827a0caf6137ddf759d50a171451b335cecc87d236`.
The image is pulled before the test; the running container has:

- an internal-only Docker network with no external route;
- a loopback-only randomly published host port;
- a read-only root filesystem and read-only fixture mount;
- dedicated bounded `tmpfs` mounts for NGINX runtime state;
- UID/GID 101, all Linux capabilities dropped, and `no-new-privileges`;
- CPU, memory, and PID limits;
- deterministic teardown plus bounded image/container/log evidence.

Run it locally with:

```bash
make nginx-migration-e2e
```

`scripts/nginx-migration-e2e.sh` reports `not_executed` and exits successfully
when Docker is absent unless `REQUIRE_NGINX_E2E=1` is set. The Make target and
CI workflow set that variable, so a missing runtime cannot silently pass the
required lane. The separate workflow also runs on relevant pull requests,
weekly, on demand, and after relevant changes reach `main`.

The Go reference test compares the real NGINX response only with the manifest's
`reference` side. The ordinary real-Jul lane then compares Jul with its declared
expectation and classifies the approved NGINX-to-Jul relationship. This keeps a
reference-runtime failure distinct from an importer or Jul-runtime regression.

## Machine-checked category inventory

`coverage.json` is the reviewed closure contract for the six minimum categories
from #154. `inventory.json` is generated from that matrix and every fixture
manifest. CI fails if a fixture, classification, risk, code, verdict, category
disposition, or deferred dimension changes without deliberate review.

| Minimum category | Represented evidence | Intentionally deferred dimensions |
| --- | --- | --- |
| Core HTTP routing | Multi-file servers, exact/prefix/regex locations, returns, redirects, alias approximation, method constraints, static response headers/CORS, and dynamic-proxy blocking. | Full location-precedence cross-product, broader `proxy_pass` URI edge matrix, and non-static rewrite control flow. |
| Upstreams and resiliency | Named weighted pools, least-connections, proxy routing, ignored pool tuning, and variable-derived destination blocking. | Active-health, backend-TLS/private-CA, retry/circuit, and WebSocket/gRPC upstream migration replay. |
| Security | IPv4/IPv6 trusted proxies, supported and blocking real-IP forms, TLS references/protocols, security headers, and blocking auth/ACL/body/rate/cache controls. | Generated-certificate mTLS, multi-proxy chain comparison, and WAF/module-specific replay. Product-level client-identity spoofing and H1/H2/H3 parity remain owned by #259. |
| Cache and compression | Direct gzip classification and explicit blocking NGINX cache-policy evidence. | Stateful cache/Vary/range replay, decoded compression-byte comparison, and shared/distributed cache directives. |
| Protocol/application gateways | Strict-valid FastCGI candidate plus explicit blocking stream and mail fixtures. | Migration-specific H2/H3, WebSocket, gRPC/uWSGI, and L4 runtime replay. |
| Operations | Include-tree provenance plus ignored/blocking process, event, log, resolver, and variable-map evidence. | Live log-sink, resolver/DNS, and worker/process tuning parity. |

“Represented” means the category has executable migration evidence. It does not
mean every directive or runtime mode in that category is supported. The full
rationale and revisit trigger for each deferred dimension live in
`coverage.json`; prose cannot silently broaden the claim.

## Protocol-lane decisions

- The PR lane runs every assessment/candidate golden and all approved real-Jul
  scenarios under the `importer` tag. Full-tag and race jobs exercise the same
  code alongside the repository's protocol packages.
- The dedicated Docker lane runs a digest-pinned NGINX process for the selected
  HTTP reference scenarios. Missing Docker is a failure when the lane is
  required.
- FastCGI is represented at import and strict-candidate level. A real FastCGI
  comparison is deferred until a migration-specific semantic can be asserted
  without duplicating the product runtime suite.
- H2/H3 client-identity parity and spoofing are already real-server obligations
  of #259. They are evidence for the runtime capability, not a reason to claim
  that every imported NGINX protocol configuration is equivalent.
- WebSocket, gRPC, uWSGI, and L4 migration replay require a separately selected
  fixture/backend lane. Stream and mail are currently blocking source evidence,
  so executing them as if Jul had imported them would be misleading.

## Deterministic aggregate report

Generate the current non-scoring inventory with:

```bash
go run -tags importer scripts/nginx-corpus-report.go
```

Check or deliberately refresh the committed artifact with:

```bash
go run -tags importer scripts/nginx-corpus-report.go -check
go run -tags importer scripts/nginx-corpus-report.go -write
```

`-write` is never an automatic golden update. Review fixture additions, class
and risk changes, code frequency, verdicts, coverage dispositions, and deferred
dimensions together. Counts describe only this repository-owned corpus; they are
not a compatibility score or market claim.

## Running the core lane

```bash
scripts/nginx-corpus-check.sh
# or
make nginx-corpus-check
```

Equivalent direct commands:

```bash
go test -tags importer ./internal/migrate/nginx/corpus
go test -tags importer ./cmd/jul \
  -run '^TestNGINXCorpusAssessmentCandidateAndRealJul$' \
  -count=1
```

The ordinary full-tag and race test jobs also execute these tests. A blocking
fixture proves assessment behavior only and never enters the runtime lane. A
required candidate is marshaled canonically, reparsed, and validated before the
real Jul process starts.

## Adding a fixture

1. Create a lowercase hyphenated directory and matching manifest `id`.
2. Keep the NGINX tree self-contained below the fixture's `nginx/` directory.
3. Document the use case, origin, sanitization, and important directives in the
   fixture README.
4. Declare every assessment result. Use `count` only for identical repeated
   projections.
5. Require a candidate only when the assessment is expected to be ready.
6. Add runtime scenarios only for a required candidate and assert the minimum
   dimensions needed to prove the behavior.
7. Update `coverage.json` when the fixture changes minimum-category evidence.
8. Regenerate `inventory.json` with the explicit `-write` command and review the
   aggregate diff.
9. Run the core lane, pinned reference lane, and repository full-tag/race gates.

A fixture update is a contract change: review the source, assessment projection,
candidate assertions, runtime verdict, coverage disposition, aggregate report,
and licensing/privacy metadata together.

### Corpus-discovered selected differences

#### Local redirect authority

NGINX expands a local `return 30x /path` target to an absolute `Location` by
default, using the request/server authority. Jul preserves `/path`. The importer
therefore reports `NGX_LOCATION_RETURN_ABSOLUTE_REDIRECT` as `approximated`,
and the corpus records `expected_difference`.

#### `limit_except` denial status

For the supported narrow form, NGINX denies excluded methods with 403. Jul
translates the allowed methods into a route predicate, so an excluded request
does not match and resolves to 404. The assessment remains `approximated` under
`NGX_LOCATION_LIMIT_EXCEPT`, and the real NGINX/Jul scenarios pin the
selected-dimension difference.

