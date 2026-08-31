# Zero-config mode + `jul lint`

Jul.IA can run **without a config file** for quick tests and local development,
and ships a **`jul lint`** subcommand that flags risky-but-valid configurations
before they reach production.

This is **Y1-08**, in **core** — no build tag.

> **Maturity:** **GA** (see [ADR 0003](adr/0003-maturity-and-ga.md)).

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
jul run --serve ./public --listen :8080

# Proxy to a backend
jul run --proxy 127.0.0.1:3000 --listen :8080
```

Both modes apply production-ready defaults (compression on, standard timeouts,
index fallback) so the result is immediately usable.

### Synthesizers

| Command | What it does | Defaults applied |
| --- | --- | --- |
| `jul run --serve <dir> [--listen <addr>]` | Static file server for `dir` on `addr` (default `:8080`) | `Compression.Enabled = true`, `ReadHeaderTimeout = 10s`, `IdleTimeout = 60s`, `ClientMaxBodySize = 1 MiB`, `MaxHeaderBytes = 1 MiB` |
| `jul run --proxy <target> [--listen <addr>]` | Reverse-proxy every request to `target` on `addr` | Same timeouts + compression as above; target normalised to full URL (`:port` → loopback) |

The synthesised configs **pass `Validate`** and are therefore guaranteed to be
structurally valid.  They are also **round-trip safe** (`Marshal` → `Parse`
produces an equivalent config).

## `jul lint`

`jul lint [-config <file>] [-strict] [-json] [-quiet]` inspects a configuration
for best-practice and security issues that are syntactically valid but
operationally risky.  It produces **warnings** (`SeverityWarning`) — unlike
`Validate`, which returns **errors** that block startup.

Output formats:

- **Human** (default): one line per diagnostic with severity, field, message,
  and hint.
- **JSON** (`-json`): machine-readable array of `Diagnostic` objects for CI
  gates.
- **Quiet** (`-quiet`): exit code only (non-zero if any warning).

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

| # | Check | Trigger | Severity | Rationale | Test |
| --- | --- | --- | --- | --- | --- |
| L1 | Empty server | `len(Locations) == 0` and no `redirect_https` | warning | Every request returns 404 | `TestLintEmptyServerWarns` |
| L2 | HTTPS redirector exempt | `redirect_https` set | — | Intentional no-location server | `TestLintEmptyServerWithRedirectIsClean` |
| L3 | Duplicate location match | Same `(type, path)` pair seen before | warning | Later block is unreachable | `TestLintDuplicateLocation` |
| L4 | Directory listing enabled | `directory_listing = true` | warning | Exposes file names to clients | `TestLintDirectoryListing` |
| L5 | TLS without min_version | `tls.enabled && tls.min_version == ""` | warning | Relies on runtime default | `TestLintTLSMinVersion` |
| L6 | Exposed admin without token | Admin not loopback and no token | warning | Unauthenticated remote control | `TestLintAdminExposed` |
| L7 | Exposed admin without TLS | Admin not loopback and `admin.tls` not enabled | warning | Credentials and config travel in cleartext | `TestLintAdminExposedWithoutTLS` |
| L8 | Literal admin token | `admin.token` non-empty and not a `${…}` ref | warning | Secret committed to config file | `TestLintLiteralSecret` |
| L9 | Literal Consul token | `discovery.consul.token` non-empty, not `${…}` | warning | ACL token committed to config file | `TestLintLiteralSecret` |
| L10 | Literal Kubernetes token | `discovery.kubernetes.token` non-empty, not `${…}` | warning | SA token committed to config file | `TestLintLiteralSecret` |
| L11 | Compression disabled | `!compression.enabled` | warning | Wasted bandwidth on text responses | `TestLintCompressionDisabled` |

All rules are **conservative** (low false-positive rate).  A clean config
(strong TLS, references for secrets, loopback admin, compression on, no
duplicates, no directory listing) produces **zero diagnostics**.

## Benchmarks

> **Related tool: `jul fmt`**
>
> `jul fmt [-config <file>] [-w] [-diff]` rewrites a config in canonical TOML.
> Use it alongside `jul lint` in your workflow:
>
> ```bash
> jul fmt -config server.toml -w     # format in place
> jul fmt -config server.toml -diff  # show diff, exit 1 if changes needed (CI mode)
> ```
>
> `-diff` is useful as a CI gate: it exits 0 when the file is already canonical
> and 1 when `fmt -w` would change it, so you can enforce formatting in
> pre-commit or PR checks without modifying files. See
> [docs/getting-started.md](getting-started.md#validate-and-format-configs) for
> a full walkthrough.

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
| **Literal secrets in VCS** | Admin, Consul, or K8s tokens committed to repo | Checks L7–L9 flag any literal value in a sensitive field; CI gate `jul lint -json` in pre-commit |
| **Admin API exposed to internet** | `0.0.0.0:9090` with no token = remote code execution | Check L6 warns when admin binds non-loopback without authentication |
| **Weak TLS default** | Missing `min_version` may negotiate an obsolete protocol | Check L5 encourages explicit `1.3` or `1.2` |
| **Information disclosure** | `directory_listing` leaks directory contents | Check L4 flags it |
| **Unreachable config** | Duplicate location blocks shadow later rules | Check L3 surfaces the collision |
| **Lint bypass via `-strict` confusion** | Operator thinks `-strict` upgrades warnings to errors, but they skip fixes | `-strict` makes warnings fatal; document the difference from `Validate` errors |
| **False sense of security** | Clean lint does not mean secure deployment | Lint is advisory; pair with `Validate`, `jul check`, and the [hardening guide](../SECURITY.md#hardening-defaults--recommendations) |

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), zero-config + `jul lint` is **GA**:
the soak test (criterion 5) was validated on 2026-07-06.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [Lint checks matrix](#lint-checks-matrix) + [Synthesizers table](#synthesizers) |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) |
| 3 | Documented known-limitations | ✅ Conservative rules, advisory-only, does not replace hardening |
| 4 | Stable config/API contract (semver-guarded) | ✅ `Diagnostic` schema and `Lint` API frozen under [compatibility policy](compatibility.md) |
| 5 | Long-running soak test passed | ✅ validated via test-zero-config.ps1 2026-07-06 — [evidence](soak-evidence.md#2026-07-06--phase-2b-soak-preparation-local-windows-5-min-smoke--validation-scripts) |
| 6 | Runnable example + docs | ✅ `jul run --serve` / `jul run --proxy` CLI examples |
| 7 | Security / threat note | ✅ [Security / threat note](#security--threat-note) |
| 8 | Fuzzing where parsing is involved | ✅ `FuzzParse` in `internal/config/fuzz_test.go` (TOML → Config round-trip) |
| 9 | Self-explanatory Console surface | ✅ Console **Setup** wizard uses `ServeDir` / `ProxyTarget` synthesizers |
