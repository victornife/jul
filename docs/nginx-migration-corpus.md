# NGINX migration compatibility corpus

> Programme issue: **#154** · Build tag: `importer` · Fixture schema: **1**

The migration corpus turns importer support claims into reviewable, executable
evidence. It does not produce a compatibility percentage and it never labels an
entire configuration equivalent. Every runtime verdict is limited to the
response dimensions named by one scenario.

## Current tranche

The first tranche establishes the core contract and runs on every full-tag test
lane:

- strict, versioned fixture manifests;
- repository-authored sanitized NGINX source trees;
- exact semantic assessment goldens covering source, finding code, class, risk,
  context, directive, source catalogue, completeness, and readiness;
- selected canonical Jul TOML assertions plus authoritative parse/validation;
- a loopback-only real Jul server replay for supported fixtures;
- a pinned, isolated official NGINX 1.28.3 reference runtime for the first
  multi-file core fixture;
- explicit `equivalent_for_asserted_dimensions`, `expected_difference`,
  `unexpected_difference`, `not_executed`, and `blocking_source` verdicts;
- supported, approximated, and blocking fixtures in the core lane.

The pinned reference lane currently executes the multi-file return/header fixture.
Protocol-heavy lanes and broader corpus categories remain follow-up work within
#154. For fixtures not yet selected by that lane, `reference` remains a reviewed
NGINX-side expectation rather than a captured process response. The real Jul
observation must still match the separately recorded Jul expectation exactly.

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

## Category inventory and explicit deferrals

| Minimum category | Current evidence | Deferred boundary |
| --- | --- | --- |
| Core HTTP routing | Multi-file server/listen/name, exact locations, returns, redirects, static response header, alias approximation, dynamic proxy blocker. | Full precedence matrix, rewrite/proxy URI and request predicates expand in later fixtures. |
| Upstreams and resiliency | Dynamic proxy target blocking is represented. | Weighted pools, health, retries, WebSocket, gRPC and backend TLS need deterministic local backends. |
| Security | Secret/private-key fixture admission checks and sensitive-header replay rejection are enforced. | TLS, mTLS, auth, ACL, limits, WAF and real-IP behavior need generated certs or dedicated fixtures. |
| Cache and compression | Explicitly deferred. | Stateful cache/Vary/range behavior needs deterministic setup and reset semantics. |
| Protocol/application gateways | Explicitly deferred. | FastCGI, uWSGI, gRPC and L4 require protocol-specific local backends and build-tag lanes. |
| Operations | Include-tree provenance is represented. | Logs, maps, resolver, process settings, zones, Lua and conditional behavior expand as blocking/approximate fixtures. |

A deferral is not a compatibility claim. It identifies the fixture/runtime work
still required before #154 can close.

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
7. Run the core lane and the repository full-tag/race gates.

A fixture update is a contract change: review the source, assessment projection,
candidate assertions, runtime verdict, and licensing/privacy metadata together.

### Corpus-discovered local redirect boundary

NGINX expands a local `return 30x /path` target to an absolute
`Location` by default, using the request/server authority. Jul preserves
`/path`. The importer therefore reports
`NGX_LOCATION_RETURN_ABSOLUTE_REDIRECT` as `approximated`, and the corpus
records the selected-dimension runtime relationship as
`expected_difference` rather than claiming equivalence.

