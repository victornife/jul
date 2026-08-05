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

go test -count=1 \
  -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" \
  ./internal/rbac ./internal/waf ./internal/plugins

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
