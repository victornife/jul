# Jul.IA — compatibility & versioning policy

> Version 1.0 · Updated 2026-06-21

This document defines what "stable" means for Jul.IA and what a **GA** label
guarantees about a feature's contract. It is the fleet-wide answer to GA
criterion 4 — *stable config/API contract, semver-guarded* — in
[ADR 0003](adr/0003-maturity-and-ga.md).

## Versioning

Jul.IA follows [Semantic Versioning 2.0.0](https://semver.org): **MAJOR.MINOR.PATCH**.

- **MAJOR** — incremented for a breaking change to a stable (GA) contract.
- **MINOR** — new features and non-breaking changes; new optional config keys.
- **PATCH** — bug and security fixes with no contract change.

The first stable line is **v1.0.0**. The version is reported by `jul -version`.

## What the GA contract covers

For a feature at **GA**, the following are **stable** and change only on a MAJOR
bump (with a deprecation period, below):

1. **Configuration surface** — the TOML keys, types, defaults, and accepted value
   sets documented in that feature's `docs/<feature>.md`. A valid GA config keeps
   loading across MINOR/PATCH upgrades.
2. **CLI** — documented subcommands and flags (`jul`, `-config`, `-check`,
   `-version`, `import`, `lint`, …).
3. **Admin/Console HTTP API** — documented `/api/*` request/response shapes the
   Console depends on.
4. **Prometheus metric names and labels** — the `jul_*` series documented for the
   feature. Renames/removals are MAJOR; **new** series/labels are MINOR.
5. **Observable wire behaviour** — documented headers, status codes, and proxy
   variables (e.g. `$ssl_client_*`).

Within a MAJOR line these are additive-only: new optional keys, new metrics, and
new endpoints may appear in MINOR releases, but existing ones keep their meaning.

## What it does not cover

- **Beta / Prototype / Alpha features** — still evolving; their config and APIs
  may change in a MINOR release. Each such key is marked in its doc.
- **The Go API** (`internal/…` packages) — Jul.IA ships as a binary; the Go
  packages are not a supported import surface and may change at any time.
- **Exact log lines, benchmark numbers, and internal file layouts** (e.g. the
  on-disk cache or history snapshot format), unless a doc explicitly commits to
  them.
- **Default values that are security or correctness fixes** — a default may change
  in a MINOR release when leaving it would be unsafe; such changes are called out
  in the changelog.

## Deprecation policy

A stable contract is removed only after a deprecation period:

1. The element is marked **Deprecated** (maturity ladder, [ADR 0003](adr/0003-maturity-and-ga.md))
   in its doc and, where practical, emits a startup or `-check` warning.
2. It keeps working for **at least one MINOR release** after the deprecation is
   announced.
3. It is removed no earlier than the next **MAJOR** release.

## Maturity and stability

| Maturity | Config/API stability |
| --- | --- |
| Planned / Prototype / Alpha | none — may change or disappear |
| **Beta** | usable; keys may still change in a MINOR with a changelog note |
| **GA** | covered by this policy (MAJOR-only breaking changes + deprecation) |
| Deprecated | scheduled for removal per the deprecation policy |

A feature reaching **GA** in the [GA push](ga-push.md) freezes its documented
config/API surface under this policy as of the v1.0.0 line; the v1.0.0 git tag is
cut at the first GA release.

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-21 | 1.0 | Created the compatibility & versioning policy (semver, the GA stable-contract surface, what is excluded, the deprecation policy, and the maturity/stability mapping) to satisfy GA criterion 4 fleet-wide for the GA push. | The maturity ladder and GA bar (ADR 0003) and all feature behaviour are unchanged; this only documents the contract. | [ADR 0003](adr/0003-maturity-and-ga.md), [ga-push.md](ga-push.md) |
