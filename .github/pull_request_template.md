<!-- Keep this short. Delete sections that do not apply. -->

## What & why

<!-- One or two sentences. Link the roadmap/spec item (e.g. Y2-07) if there is one. -->

## Definition of Done

A user-facing feature is **not done** until an operator can see it from the
Console. The minimum Beta DoD is criteria 1, 6, and **9** below; the full list is
the GA bar (see [ADR 0003](../docs/adr/0003-maturity-and-ga.md) and
[ADR 0004](../docs/adr/0004-console-ui-invariants.md)).

If this PR adds or changes a **user-facing capability**, all three minimum items
must be checked before merge:

- [ ] **(1) Behaviour documented** — supported behaviour enumerated in `docs/<feature>.md`.
- [ ] **(6) Runnable example** — `testdata/<feature>.toml` (and/or `examples/`) that `jul -check` accepts.
- [ ] **(9) Console surface** — the capability appears in the Console **Status**
      overview as a `FeatureStatus` row in [`runtimeStatus`](../internal/admin/api.go)
      and is asserted in `TestStatusAPI`. *No feature ships without this.*

If this PR targets **GA**, also confirm: benchmark numbers, known-limitations
list, semver-guarded contract, soak test, threat note, and fuzzing where parsing
is involved.

## Checks

- [ ] `gofmt` clean, `go vet ./...` clean (relevant build tags)
- [ ] `go test ./...` passes
- [ ] Docs / changelog updated where applicable

## Not a user-facing feature

<!-- Check if this is a refactor, test-only, docs-only, or internal change. -->

- [ ] This PR adds no user-facing capability, so the per-feature DoD does not apply.
