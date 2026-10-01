# Zero-config mode + `jul lint`

Jul.IA can run **without a config file** for quick tests and local development,
and ships a **`jul lint`** subcommand that flags risky-but-valid configurations
before they reach production.

This is **Y1-08**, in **core** — no build tag.

> **Maturity:** **GA / soaked**, released in v2.0.0. Its five-minute release validation remains a smoke; the [separate 2026-09-28 feature run](soak-evidence.md#2026-09-28--feature-specific-exact-head-one-hour-soaks-pr-482-head-9ab87c1--criterion-5-met-for-scoped-features) meets [ADR 0005](adr/0005-soak-post-ga-gate.md) for zero-config serve/proxy plus lint, with the stated workload limits.

## Contents

- [Zero-config mode](#zero-config-mode)
- [`jul lint`](#jul-lint)
- [Lint checks matrix](#lint-checks-matrix)
- [Benchmarks](#benchmarks)
- [Security / threat note](#security--threat-note)
- [GA status](#ga-status)

## Zero-config mode

Two CLI shortcuts synthesise a runnable config in-memory — no file is written:

```bash
# Serve a directory
mkdir -p public
printf 'Hello from Jul\n' > public/index.html
jul run --serve ./public --listen :8080

# Or proxy to an already-running backend on port 3000
jul run --proxy 127.0.0.1:3000 --listen :8080
```

Both modes enable compression and standard timeouts. Static mode looks for
`index.html`; proxy mode needs a reachable backend. The default listener binds
`:8080` without TLS or admin authentication, and `jul run` prints a one-line
stderr notice whenever the listener is not loopback. Treat these shortcuts as
local starting points and choose an appropriate listener, TLS and deployment
policy before exposing them to untrusted networks. The next major version is
intended to default to `127.0.0.1:8080`
([compatibility.md](compatibility.md#announced-default-changes-for-the-next-major)).

### Synthesizers

| Command | What it does | Defaults applied |
| --- | --- | --- |
| `jul run --serve <dir> [--listen <addr>]` | Static file server for `dir` on `addr` (default `:8080`) | `Compression.Enabled = true`, `ReadHeaderTimeout = 10s`, `IdleTimeout = 60s`, `ClientMaxBodySize = 1 MiB`, `MaxHeaderBytes = 1 MiB` |
| `jul run --proxy <target> [--listen <addr>]` | Reverse-proxy every request to `target` on `addr` | Same timeouts + compression as above; target normalised to full URL (`:port` → loopback) |

The synthesised configs **pass `Validate`** and are therefore guaranteed to be
structurally valid.  They are also **round-trip safe** (`Marshal` → `Parse`
produces an equivalent config).

## `jul lint`

`jul lint [-config <file>] [-strict] [-json] [-quiet]` parses and validates a
configuration, then reports operational and security findings. Most lint
findings are **warnings**; a few trust-boundary findings have `error` severity
and fail even without `-strict`. Managed-mode route IDs can produce `info`
suggestions. `Validate` errors also fail the command.

Output formats:

- **Human** (default): one line per diagnostic with severity, field, message,
  and hint.
- **JSON** (`-json`): one object with `source`, optional `errors`, and optional
  `warnings` (an array of diagnostic objects, each with its own severity).
- **Quiet** (`-quiet`): suppress advisory output; errors still appear. Use
  `-strict -quiet` for a silent nonzero exit on warnings. Without `-strict`,
  warnings alone exit 0. `-json -quiet` still emits the JSON object.

Exit codes are `0` for no errors (and warnings without `-strict`), `1` for
validation or error-severity lint findings, and `2` for warnings under
`-strict` or invalid flag usage. `info` suggestions never fail `-strict`.

### Diagnostic schema

```json
{
  "severity": "warning",
  "field": "servers[0].tls",
  "message": "tls.min_version is not set; the runtime default applies",
  "hint": "set min_version = \"1.3\" for the strongest protocol, or \"1.2\" for broader compatibility"
}
```

## Lint checks matrix

These are the material categories, not an exhaustive numbered rule inventory.
The implementation in `internal/config/lint*.go` and the filesystem checks in
`cmd/jul/cli.go` are authoritative when rules are added.

| Area | Examples of findings | Severity | Evidence |
| --- | --- | --- | --- |
| Route and response policy | Provably shadowed locations, risky header predicates, CORS/response-header interactions, directory listing, missing managed route ID | warning, error for forwarded-header routing, or info for route ID | `lint_match.go`, `lint_cors.go`, `lint_route_id_test.go` |
| Listener and admin | Conflicting listener-scoped settings, missing TLS minimum, off-loopback admin without TLS | warning (an off-loopback admin listener without a token or enabled RBAC is a validation **error**, not a lint finding) | `lint.go`, `listener_scope_test.go` |
| Client and backend trust | Broad trusted-proxy ranges, unverified backend or discovery TLS, union trust without peer identity | warning or error for disabled peer verification | `lint.go`, `backendtls_test.go`, `discovery_trust_test.go` |
| Secrets and operational defaults | Literal admin/RBAC/Consul/Kubernetes tokens, disabled compression, `enabled = true` with an explicit `encoders = []` (defaulted to gzip), ignored legacy log destinations | warning | `lint.go`, `lint_limits.go`, `lint_test.go`, `lint_limits_test.go` |
| Exposure bounds | A non-loopback listener with no `read_timeout`, `write_timeout`, `proxy_read_timeout` or `proxy_send_timeout`; a WAF route whose `request_body_limit` is below its `client_max_body_size` (bodies in between get 413) | warning | `lint_limits.go`, `lint.go` (`lintWAFFit`), `lint_limits_test.go`, `lint_test.go` |
| Upstream resilience and filesystem | Admission sizing or multiplexed connection bounds; managed config path and file-owned artifact checks | warning or error depending on path condition | `lint_resilience.go`, `cmd/jul/cli.go`, `internal/app` |

Lint is advisory except for its error-severity findings. A clean lint result
does not prove that a deployment is secure or that backends are reachable.

## Related tool: `jul fmt`

`jul fmt [-config <file>] [-w] [-diff]` rewrites a config in canonical TOML.
Use it alongside `jul lint` in your workflow:

```bash
jul fmt -config server.toml -w     # format in place
jul fmt -config server.toml -diff  # show diff, exit 1 if changes needed (CI mode)
```

`-diff` is useful as a CI gate: it exits 0 when the file is already canonical
and 1 when `fmt -w` would change it, so you can enforce formatting in
pre-commit or PR checks without modifying files. See
[docs/getting-started.md](getting-started.md#validate-and-format-configs) for
a full walkthrough.

## Benchmarks

From `go test ./internal/config/ -bench=. -benchmem` on a modest VM:

| Benchmark | ops/sec | time/op | allocs/op |
| --- | --- | --- | --- |
| `LintCleanConfig` (best case) | ~3 M | ~380 ns | 1 |
| `LintDirtyConfig` (worst case, all rules fire) | ~500 K | ~3.2 μs | 13 |
| `ParseAndValidate` (full `jul lint` path) | ~100 K | ~20 μs | 28 |
| `ServeDir` (zero-config synthesiser) | ~600 K | ~2.2 μs | 7 |
| `ProxyTarget` (zero-config synthesiser) | ~600 K | ~2.3 μs | 7 |

A typical config lints in **< 1 ms**, including parse + validate + lint.

## Security / threat note

| Threat | Risk | Mitigation |
| --- | --- | --- |
| **Literal secrets in VCS** | Admin, RBAC, Consul, or K8s tokens committed to repo | Lint flags literals in these fields without printing their values; run `jul lint -strict` in CI if warnings should fail the gate |
| **Admin API exposed to internet** | An off-loopback admin listener without a token grants unauthenticated control | Validation **rejects** an off-loopback admin listener without a token or enabled RBAC (`jul check` and reload fail); lint warns when an off-loopback admin listener has no TLS. Use loopback or authenticated TLS |
| **Unspecified TLS minimum** | Operators may assume a stronger protocol floor than the runtime default | The TLS minimum-version check suggests explicitly choosing `1.3` or `1.2` |
| **Information disclosure** | `directory_listing` exposes directory contents | The route check warns when it is enabled |
| **Unreachable route** | An earlier location provably subsumes a later one | The route matcher lint warns on provable shadowing, including predicates |
| **Lint bypass via `-strict` confusion** | Operator treats a zero exit without `-strict` as proof that no warnings exist | Use `-strict` in a warning-sensitive gate; error-severity trust findings fail either way |
| **False sense of security** | Clean lint does not mean secure deployment | Lint is advisory; pair with `Validate`, `jul check`, and the [hardening guide](../SECURITY.md#hardening-defaults--recommendations) |

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md) as amended by
[ADR 0005](adr/0005-soak-post-ga-gate.md), zero-config + `jul lint` is
**GA / soaked** for the released zero-config serve/proxy shortcuts and lint.
The [2026-09-28 feature-specific one-hour run](soak-evidence.md#2026-09-28--feature-specific-exact-head-one-hour-soaks-pr-482-head-9ab87c1--criterion-5-met-for-scoped-features)
exercised both shortcuts and lint. It does not cover public ingress, broad
concurrency or every lint configuration. The 2026-07-06 validation remains a
five-minute smoke.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [Lint checks matrix](#lint-checks-matrix) + [Synthesizers table](#synthesizers) |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) |
| 3 | Documented known-limitations | ✅ Conservative warnings plus error-severity trust findings; does not replace hardening |
| 4 | Stable config/API contract (semver-guarded) | ✅ `Diagnostic` schema and `Lint` API frozen under [compatibility policy](compatibility.md) |
| 5 | Long-running soak test (post-GA gate) | ✅ [Exact-head 3600.667s run](soak-evidence.md#2026-09-28--feature-specific-exact-head-one-hour-soaks-pr-482-head-9ab87c1--criterion-5-met-for-scoped-features); five-minute release validation remains smoke |
| 6 | Runnable example + docs | ✅ `jul run --serve` / `jul run --proxy` CLI examples |
| 7 | Security / threat note | ✅ [Security / threat note](#security--threat-note) |
| 8 | Fuzzing where parsing is involved | ✅ `FuzzParse` in `internal/config/fuzz_test.go` (TOML → Config round-trip) |
| 9 | Self-explanatory Console surface | ✅ Console **Setup** wizard uses `ServeDir` / `ProxyTarget` synthesizers |
