# NGINX migration corpus and real E2E closure — 2026-08-31

> Programme: #112 · Work item: #154 · Evidence baseline: PR #352 merged at
> `ec0985020eefa07410b9dc12e1833a4c469a2c5a` plus the issue-closure tranche.

## Decision

Close #154 when the closure tranche merges and all commands below pass on its
exact head SHA.

The completed outcome is a **bounded migration-evidence programme**. It is not a
universal NGINX compatibility certificate, a performance benchmark, or a
commitment to implement every unsupported directive.

## Final inventory

The committed aggregate report is
[`testdata/nginx-corpus/inventory.json`](../../testdata/nginx-corpus/inventory.json).
It is generated from strict fixture manifests and the reviewed category matrix,
not edited by hand.

- 11 repository-owned, sanitized fixtures;
- 9 core-tier and 2 full-tier fixtures;
- 127 exact directive-result projections;
- 85 supported, 3 approximated, 4 ignored, 25 blocking, and 10 informational
  results;
- two selected-dimension equivalent scenarios and two approved expected
  differences;
- no compatibility percentage.

## Minimum-category disposition

The machine authority is
[`testdata/nginx-corpus/coverage.json`](../../testdata/nginx-corpus/coverage.json).
Every minimum category from #154 is represented by executable evidence:

| Category | Primary fixtures | Closure boundary |
| --- | --- | --- |
| Core HTTP routing | `core-multifile-return`, `routing-cors-policy`, `approximate-alias`, `blocking-dynamic-proxy` | Full precedence and dynamic control-flow matrices remain evidence-triggered. |
| Upstreams/resiliency | `upstream-weighted`, `blocking-dynamic-proxy` | Health, backend TLS, retries/circuits, and protocol-specific upstream replay remain separately selected work. |
| Security | `realip-supported`, `realip-blocking`, `security-cache-boundaries` | Generated-cert mTLS, multi-proxy differential replay, and WAF/module replay remain explicit deferred dimensions. |
| Cache/compression | `security-cache-boundaries` | The fixture proves classification boundaries; it is not a stateful cache conformance suite. |
| Protocol gateways | `fastcgi-gateway`, `protocol-boundaries` | FastCGI has strict-candidate evidence; stream/mail remain blocking; H2/H3/WebSocket/gRPC/uWSGI/L4 migration replay is not claimed. |
| Operations | `core-multifile-return`, `operations-boundaries` | Live log sinks, DNS/resolver behavior, and process tuning parity remain out of this bounded correctness harness. |

A future fixture is justified by a supported importer mapping change, a concrete
migration incident, or a selected use case with deterministic local dependencies.
A deferred dimension is not an implementation dependency for closing #154.

## Runtime evidence

The ordinary corpus lane:

1. loads and sanitizes every fixture;
2. checks the exact assessment multiset and source provenance;
3. refuses silent generation for blocking fixtures;
4. marshals, reparses, and validates every required Jul candidate;
5. starts a real Jul server for approved safe scenarios;
6. compares only explicitly named dimensions.

The separate reference lane runs Docker Official Image `nginx:1.28.3-alpine`
pinned to
`sha256:a8b39bd9cf0f83869a2162827a0caf6137ddf759d50a171451b335cecc87d236`.
It uses an internal-only network, loopback bridge, read-only root and fixture
mount, non-root UID/GID, dropped capabilities, `no-new-privileges`, bounded
`tmpfs`, CPU/memory/PID limits, bounded artifacts, and deterministic teardown.

The selected-dimension differences are retained intentionally:

- NGINX expands a local `return 30x /path` redirect to an absolute location
  while Jul preserves the relative target
  (`NGX_LOCATION_RETURN_ABSOLUTE_REDIRECT`);
- NGINX's narrow `limit_except` denial returns 403, while Jul's translated
  method predicate makes the route not match and returns 404
  (`NGX_LOCATION_LIMIT_EXCEPT`).

Both verdicts are `expected_difference`; any other observed value fails.

## Security, privacy, and licensing review

- All current fixtures are repository-authored and licensed
  `AGPL-3.0-only`.
- No proprietary/user configuration, production endpoint, user request body,
  credential, or private key is admitted.
- External fixtures require a separate explicit license/privacy review; none is
  required for closure.
- Fixture traversal rejects symlinks, non-regular files, files above 1 MiB, and
  high-confidence private-key markers.
- Scenario validation rejects unsafe methods, non-loopback targets, sensitive
  request headers, unbounded bodies, and unlisted comparison dimensions.
- Aggregate reports contain stable metadata and counts only; they do not copy
  NGINX source values.

## Acceptance matrix

- [x] Prerequisite assessment, provenance, include, and routing contracts are final.
- [x] Corpus structure, origin/license policy, sanitization, and scenario grammar are documented.
- [x] Every minimum category is represented and residual dimensions have explicit rationale and revisit triggers.
- [x] Every fixture has an exact assessment golden and a strict-valid candidate where generation is allowed.
- [x] Core assessment/generation and real-Jul scenarios run in PR CI.
- [x] A digest-pinned, isolated real-NGINX reference lane is reproducible and required in its workflow.
- [x] Scenario comparisons use explicit normalized dimensions and bounded verdicts.
- [x] Secret/privacy/license controls have executable negative tests.
- [x] Unexpected differences fail with fixture, scenario, dimension, expected, and actual values.
- [x] Deterministic aggregate evidence exists without a compatibility score.
- [x] Protocol-heavy and stateful residual work is explicitly deferred rather than silently claimed.
- [x] Documentation, changelog, contribution workflow, and rollback boundary are recorded.

## Verification

Run on the exact closure head:

```bash
make nginx-corpus-check
make nginx-migration-e2e

go test -count=1 -tags importer \
  ./internal/migrate/nginx ./internal/migrate/nginx/corpus ./cmd/jul

go test -count=1 -race -p 2 -tags "importer grpc stream waf" \
  ./internal/migrate/nginx ./internal/migrate/nginx/corpus ./cmd/jul

make ci-pr
python3 scripts/docs-check.py
python3 scripts/test_docs_check.py
```

The PR record must include the exact head SHA and GitHub workflow conclusions.
No result from a previous SHA is silently applied to the closure head.

## Maintenance and rollback

A corpus change must update the fixture, assessment/candidate golden, category
matrix, generated inventory, and documentation together. `-write` inventory
refreshes are deliberate review actions; CI uses `-check`.

The closure tranche changes evidence and tests, not live gateway defaults or
runtime configuration. Rollback is an ordinary revert of the fixture/reporting
commit. Existing importer assessment and the PR #352 reference lane continue to
operate independently.
