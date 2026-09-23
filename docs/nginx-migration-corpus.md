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
[NGINX migration corpus closure](audit/old/2026-08-31-nginx-migration-corpus-closure.md).

## Bounded NGINX stream and HTTP PROXY-protocol identity evidence (#426)

Issue #426 adds a bounded first tranche of NGINX `stream` (L4) translation plus
HTTP inbound PROXY-protocol identity on top of the #154 baseline above, without
reopening its closed acceptance record. Six fixtures were added:

- `stream-tcp-basic` — direct TCP `proxy_pass` plus `proxy_timeout`/
  `proxy_connect_timeout`;
- `stream-named-upstream` — a stream server reusing a named `upstream` block,
  weighted members, and outbound PROXY protocol;
- `stream-udp` — a `udp` listener documenting Jul's bounded session model;
- `stream-sni-bounded` — `server_name` + `ssl_preread` merged into one
  listener's `sni_routes`;
- `stream-security-boundaries` — four still-blocking stream forms (inbound
  PROXY protocol, TLS termination, `udp` + `proxy_protocol`, an unrecognized
  listen option);
- `realip-proxy-protocol-supported` — the complete HTTP
  `listen ... proxy_protocol` + `real_ip_header proxy_protocol` +
  `set_real_ip_from` trio.

See [the `stream` block table](nginx-importer.md#stream-block) and
[HTTP PROXY-protocol identity](nginx-importer.md#http-proxy-protocol-identity-426)
for exactly which forms are supported, approximate, or blocking. Real
NGINX-vs-Jul runtime evidence for these translated forms is #366/#367's
responsibility, not this issue's.

## Expanded HTTP/upstream/WebSocket/compression migration E2E (#365)

Issue #365 expands the #154 real-Jul runtime evidence with eight more fixtures:

- `routing-precedence-runtime` — proves the exact / longest-non-root-prefix /
  regex / root location-precedence order end to end, plus a `limit_except`
  method predicate. This fixture runs against **both** a real Jul instance
  and the pinned real-NGINX reference container (see
  `scripts/nginx-migration-e2e.sh`'s `FIXTURE_SPECS`): every reference value
  in its manifest was captured by running the exact same `nginx.conf` against
  the pinned image, not guessed. One scenario (`POST /methods`, excluded by
  `limit_except`) is a confirmed, intentional difference — real NGINX returns
  403, Jul's route simply does not match and falls through to the next
  candidate (204) — recorded as `expected_difference` with the existing
  `NGX_LOCATION_LIMIT_EXCEPT` code, matching the importer's own documented
  approximation;
- `upstream-weighted-runtime` — a named upstream with a weighted and a
  default-weight member proxied to two real local backends; asserts the
  *exact* smooth-weighted-round-robin distribution (3:1 over 40 requests ⇒
  30:10) against a real Jul instance, since the algorithm is fully
  deterministic;
- `compression-runtime` — `gzip on` proxying to a real backend; the test
  sets `Accept-Encoding` itself (opting out of Go's transparent
  decompression) so it can assert `Content-Encoding: gzip` actually engaged
  against a real Jul instance, then decodes the body to confirm logical
  content equivalence — the corpus never compares raw compressed bytes;
- `websocket-runtime` — a real WebSocket upgrade, bidirectional text and
  binary message exchange, and clean close through a real Jul instance
  proxying to a real local WebSocket-echoing backend. This is H1 WebSocket
  migration evidence only; H2/H3 Extended CONNECT WebSocket remains #435's
  scope, not implemented here;
- `cache-runtime` — a single `proxy_cache_path` zone referenced consistently
  by `proxy_cache` translates onto Jul's single process-wide `[cache]`; the
  test asserts a real `X-Cache: MISS` → `X-Cache: HIT` transition through a
  real Jul instance and real backend, that the cached response's own
  `X-Corpus-Backend-Id` header is replayed verbatim on the hit (per the
  stored-headers contract in `docs/cache.md`), and that a client
  `Cache-Control: no-store` request gets `X-Cache: BYPASS` without disturbing
  the already-stored entry;
- `upstream-failover-runtime` — two backends declaring the same
  `max_fails`/`fail_timeout` translate onto Jul's upstream-wide
  `[upstreams.resilience]` circuit breaker; the test proves a backend that
  refuses every connection is excluded after tripping the breaker, with every
  client request still succeeding (Jul's default retry-every-distinct-backend
  behavior masks the failure) against a real Jul instance;
- `proxy-pass-uri-runtime` — precisely characterizes `proxy_pass` URI-rewriting
  semantics through a real Jul instance: a location `/api` with
  `proxy_pass http://pool/v2` turns a client request for `/api/foo` into
  `/v2/api/foo` at the backend, because Jul's proxy
  (`net/http/httputil.ProxyRequest.SetURL`) always *prepends* the
  `proxy_pass` path to the client's full incoming request path rather than
  stripping the matched location prefix and substituting it, as nginx does.
  This difference was already flagged (`NGX_LOCATION_PROXY_PASS_URI`,
  approximated) but had never been proven end to end before this fixture;
- `timeout-runtime` — proves `proxy_connect_timeout`/`proxy_read_timeout` →
  Jul's location-level timeouts actually take effect: a backend that stalls
  before writing any response byte is cut off at the configured 1s
  `proxy_read_timeout`, and Jul returns a real 504 well before the backend's
  full 3s delay elapses (`docs/core-http.md`'s `upstream_timeout` → 504
  mapping). The same commit adds the bounded `proxy_next_upstream_tries` →
  `retry_attempts` translation (an explicit bound of 2 or more only; nginx's
  `0`/`1` forms collide with Jul's `retry_attempts = 0` "inherit" sentinel
  and stay blocking), unit-tested but not separately E2E'd since
  `upstream-failover-runtime` already proves Jul's retry mechanism works end
  to end.

The weighted-upstream, compression, WebSocket, cache, upstream-failover,
proxy_pass-URI, and timeout scenarios live in `cmd/jul/corpus_runtime_test.go`
rather than a fixture's manifest `scenarios` array, because each needs
assertions the generic single-request/response Scenario/Dimension model does
not express (repeated-request distribution counts, raw-header/decoded-body
inspection, a persistent bidirectional connection, a two-request MISS/HIT
sequence, a deliberately-never-listening backend, inspecting the exact
backend-visible request path, and a wall-clock bound on a deliberately
stalling backend, respectively). `startCorpusTCPBackends` in
`cmd/jul/import_corpus_test.go` gives any fixture's named-upstream TCP
members a real local backend for the real-Jul path, the same way
`startCorpusUnixHTTPBackends` already does for `unix:` addresses.

**Why only `routing-precedence-runtime` runs against the pinned NGINX
container.** The reference lane's isolation model runs NGINX on an
`--internal` Docker network with no external connectivity; a fixture with a
real TCP backend needs that backend reachable from inside NGINX's own network
namespace, which the pinned lane only currently solves for `unix-http-upstream`
(a Unix-domain-socket path is just a bind-mounted file, so it needs no network
namespace sharing at all). Extending that to a TCP backend for the weighted/
compression/WebSocket fixtures would mean either introducing a second pinned
sidecar container image (a new supply-chain dependency to justify and pin) or
running repeated-request/persistent-connection comparisons the reference
lane's single-request harness (`TestNGINXCorpusReferenceRuntime`) does not
support today. Both nginx and Jul already document the same "smooth weighted
round-robin" algorithm, and the compression/WebSocket real-Jul evidence above
already proves engagement and fidelity directly; a real-NGINX comparison for
those three would be confirmatory rather than divergence-hunting. This is
recorded as a deliberate, reasoned scope boundary — not an oversight — and is a
natural candidate for the heavier, more elaborate reference infrastructure
#368 already governs for the full/scheduled lane, if a concrete need arises.

Stateful cache E2E (miss/hit/bypass) and passive-failover/circuit-breaker E2E
are covered by `cache-runtime` and `upstream-failover-runtime` above, once the
bounded `proxy_cache_path`/`proxy_cache` → `[cache]` and per-backend
`max_fails`/`fail_timeout` → upstream-wide `[upstreams.resilience]`
translations were added alongside them. Both translations are deliberately
bounded to the case that is actually lossless: a single cache zone used
consistently everywhere, and backends that agree on the same failure
threshold/open duration. A wider case (multiple cache zones, or disagreeing
per-backend thresholds) is a genuine architectural mismatch — Jul's cache and
circuit breaker are process-/pool-wide, not per-zone or per-backend — and is
left blocking (cache) or approximated with a note (resilience) rather than
guessed. `security-cache-boundaries` documents the multi/undeclared-zone
conflict case; see `coverage.json`'s `cache-compression` and
`upstreams-resiliency` categories for the recorded evidence and any residual
revisit triggers (e.g. circuit-breaker recovery/half-open-probe replay).

**Cache dimensions deliberately not yet exercised here: `Vary`, `Range`, and
stale-serving.** `cache-runtime` proves MISS, HIT, and the client-side
`no-store` bypass — the three dispositions a migrated `proxy_cache` config
most directly puts at risk of a silent behavior change. `Vary`-keyed
multi-representation caching, `Range`/`If-Range` bypass, and
`stale-while-revalidate`/`stale-if-error` serving are already covered by
`internal/handler`'s own dedicated, extensive cache test suite (see
`docs/cache.md`'s shared-cache contract table) and are runtime behavior
independent of anything the importer translates — proving them again through
the migration corpus would be confirmatory of already-tested runtime
behavior, not migration-specific evidence, so they are left to that existing
suite rather than duplicated here.

## TLS, mTLS, and imported PROXY-protocol identity migration corpus (#366)

Issue #366 extends the #154 baseline with two new capabilities and their
real-Jul evidence:

- **Bounded mTLS translation.** `ssl_verify_client on|optional;` plus a
  non-empty `ssl_client_certificate` translates to
  `servers[].tls.client_auth` (`mode = "require"`/`"request"`, `ca_file`,
  `crl_file`). Mirrors the #426 bounded-pairing pattern: either directive
  alone is not representable (nginx never actually enables verification
  without both either), so it stays blocking with a specific reason rather
  than an inert or guessed translation. `ssl_verify_client optional_no_ca`
  (accept a client certificate without validating it against any CA) has no
  Jul equivalent — Jul's `client_auth` always validates against `ca_file`
  once enabled — and stays blocking too, not approximated.
  `ssl_verify_depth`/`ssl_trusted_certificate` are ignored (no corresponding
  Jul knob). `mtls-runtime` proves it end to end: a real Jul instance and a
  freshly generated ephemeral CA/leaf certificate chain accept a client
  certificate signed by the configured CA and reject both no certificate at
  all and a certificate signed by a different CA, at the TLS handshake.
- **Imported HTTP PROXY-protocol identity evidence**, per #366's own
  2026-09-21 amendment. `proxy-protocol-runtime` reuses the already-merged
  #426 `listen ... proxy_protocol;` + `real_ip_header proxy_protocol;` +
  `set_real_ip_from` translation and proves its trust boundary through a
  real Jul instance: a real local relay whose own peer address matches the
  configured `trusted_proxies` (simulated locally via
  `net.Dialer.LocalAddr` aliasing two distinct loopback addresses, since a
  single test machine has no other way to present two different network
  positions) prepends a genuine PROXY v1 header, which Jul honors as the
  canonical client address — observable in the `X-Forwarded-For` it forwards
  upstream. The same bytes from a peer outside `trusted_proxies` are refused
  outright, a malformed PROXY header from an otherwise-trusted peer is also
  refused, and a client-supplied `X-Forwarded-For` attempting to override
  the PROXY-derived identity is discarded (Jul always rebuilds it from its
  own trusted view).
- **WAF boundary documented, not translated.** nginx has no first-party WAF
  directives, and no third-party WAF module directives (`modsecurity`,
  `naxsi`, etc.) appear anywhere in this corpus — there is no nginx-side
  construct to translate *from* onto Jul's own `[waf]` (Coraza) block, so
  this is a genuine, documented absence of source material rather than an
  unimplemented translation. Any such directive that did appear would fall
  through to the generic `NGX_DIRECTIVE_UNSUPPORTED` blocking finding today.

**Protocol-lane disposition, explicit per #366's own acceptance criteria.**
Both new real-Jul E2E tests exercise H1 only. Jul's own mTLS documentation
states client-certificate verification applies uniformly across H1/H2/H3
(the same TLS handshake underlies all three), so this is not a
protocol-specific runtime path the migration corpus needs to separately
prove — H2/H3 parity for the *runtime capability itself* is already a
product-level obligation of #259, not a migration-specific claim. HTTP/3 is
additionally out of scope here because PROXY protocol is a TCP-preamble
mechanism with no meaning over QUIC/UDP. Config-reload-under-live-traffic for
either capability is also not separately exercised: reload atomicity and
timing are already governed by the dedicated `docs/reload-semantics.md`
contract and its own test suite, independent of anything the importer
translates.

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
| Security | IPv4/IPv6 trusted proxies, supported and blocking real-IP forms, TLS references/protocols, security headers, blocking auth/ACL/body/rate/cache controls, bounded mTLS translation with a real-Jul certificate-accept/reject E2E, and imported PROXY-protocol identity trust-boundary evidence (#366). | Multi-proxy chain comparison and WAF/module-specific replay (no nginx-side WAF directive exists to translate from). Product-level client-identity spoofing and H1/H2/H3 parity remain owned by #259. |
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
- FastCGI is represented at import and strict-candidate level. #367 owns
  focused real-runtime comparison where a migration-specific semantic can be
  asserted without duplicating the product runtime suite.
- H2/H3 client-identity parity and spoofing are already real-server obligations
  of #259. They are evidence for the runtime capability, not a reason to claim
  that every imported NGINX protocol configuration is equivalent.
- H1 WebSocket migration replay is covered by #365. #367 owns focused
  gRPC, FastCGI, uWSGI and L4 real-runtime comparison, including the bounded
  `stream` subset translated by #426. Unsupported stream forms and `mail`
  remain blocking; importer support alone does not establish runtime parity.
- mTLS and imported PROXY-protocol identity migration replay (#366) is H1
  only and loopback-only, real-Jul, real-NGINX-comparison-free by design: no
  pinned NGINX reference lane runs TLS today (see the fixture READMEs), and
  H2/H3 parity for the underlying runtime capability is already #259's
  obligation, not a migration-specific claim.

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

### Unix HTTP upstream scenario (#407)

The migration corpus now treats a named NGINX upstream containing a Unix socket
and an HTTP location referencing that upstream as a supported #407 scenario.
Direct NGINX Unix `proxy_pass` remains a blocking/manual mapping because Jul's
public contract intentionally requires the socket to live in a named upstream.
