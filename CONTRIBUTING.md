# Contributing to Jul.IA

Thank you for your interest in Jul.IA. This document covers coding standards,
documentation conventions, and the contribution workflow.

## Code of conduct

Be respectful, constructive, and inclusive. All contributions are evaluated on
technical merit aligned with the project's vision.

## Getting started

1. **Fork** the repository.
2. **Clone** your fork and create a feature branch.
3. **Install** Go 1.26+ and ensure `$GOPATH/bin` is in your `$PATH`.
4. **Run tests** before opening a PR:
   ```bash
   make ci-fast          # lean build — fast, catches most issues
   ```
   If your change touches an opt-in feature (anything behind a build tag such
   as `grpc`, `wasmplugins`, `stream`, `http3`, `waf`, `consul`, `kubernetes`,
   `acme`, `console`, `otel`, `importer`, `brotli`, or `zstd`), also run:
   ```bash
   make ci-full          # full-tag Go build, lint, test, vulncheck, license
   ```
   For the closest local approximation of the full merge gate, run:
   ```bash
   make ci-pr            # ci-full + go vet + docs-check
   ```
   Changes to RBAC, WAF, WASM plugins, or their build-tag boundaries can run the
   focused gate directly:
   ```bash
   make security-gates   # lean/full negative tests + package coverage floors
   ```
   `make ci-pr` also runs this focused security gate.

   **What `ci-pr` does not cover** (still requires a CI push):
   - race detector (needs a CGO C toolchain)
   - repository-wide coverage floor enforcement
   - Windows / macOS platform lanes
   - frontend typecheck/lint/unit tests (run `make console-check` separately)
   - Playwright E2E
   - benchmark / fuzz / soak smoke

### Git hooks (optional local gate parity)

Repo-managed Git hooks mirror the CI **fast** gates so common failures surface
before you push. Install them in one command (safe to re-run):

```bash
make hooks                 # or: sh scripts/install-hooks.sh
# Windows (PowerShell):    pwsh scripts/install-hooks.ps1
# Any platform, directly:  git config core.hooksPath .githooks
```

What they check:

| Hook | Checks | Mirrors |
|------|--------|---------|
| `pre-commit` | `gofmt` on staged Go files | CI `gofmt` gate |
| `pre-push` | `gofmt`, `go vet`, `go build`, `go test` (lean) — plus `golangci-lint` and the console frontend checks **when installed** | `make ci-fast` |

The hooks are **non-destructive** (they only check, never rewrite files) and easy
to bypass on purpose: pass `--no-verify` to a single `git commit`/`git push`, or
set `JUL_SKIP_HOOKS=1` to disable them entirely. Uninstall with
`git config --unset core.hooksPath`.

**Parity and limits.** The hooks are a fast subset, not the whole pipeline. They
do **not** run the full build-tag profile, the race detector (needs a CGO
toolchain), the coverage floors, `govulncheck`, or the bench/fuzz/soak smoke
jobs — those still run in CI, and you should run `make ci-full` /
`make vulncheck-full` before a release-sensitive change. A green pre-push is a
good signal, not a guarantee the full CI will pass.

### Security-package gates

`make security-gates` enforces the dedicated fail-closed matrices and statement
coverage floors for `internal/rbac`, `internal/waf`, and `internal/plugins`.
The thresholds and exact baseline live in
`scripts/security-package-coverage.json`; the checker fails distinctly when a
required package is absent from the profile, so build-tag drift cannot turn a
missing package into a false pass.

Do not lower a security-package floor just to make a pull request green. Add a
linked rationale covering the changed trust boundary and replacement evidence.
See [docs/security-testing.md](docs/security-testing.md) for the negative matrix,
local commands, exit-code contract, and floor-change policy.

### Dependency vulnerability scanning

Before opening a PR, check for known vulnerabilities in the dependency tree
using `govulncheck`:

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...                                      # lean build tags
govulncheck -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" ./...
```

The CI `vulncheck` job will fail the build if any vulnerable dependencies are
detected. If a finding is known (e.g., a transitive dependency with a disclosed
but non-reachable vulnerability in your configuration), document the exception
in an issue comment and link the issue in your PR description.

## Coding standards

- Go style follows `gofmt` and `golangci-lint`.
- Build tags for optional features use `//go:build <tag>`.
- Every `_stub.go` file must provide a clear error when the tag is missing.
- Config changes require updates to `internal/config/schema.go` and
  `internal/config/validate.go`.
- New handlers and middleware must have table-driven tests.
- Panics are contained: `middleware/recover.go` catches them; re-panic only
  `http.ErrAbortHandler`.

### Error handling and sentinel errors

Preserve error context with wrapping instead of discarding it. When a lower-level
error is relevant, wrap it with `%w` so callers can still use `errors.Is` and
`errors.As` to inspect the failure. For expected, reusable failure modes, prefer
small package-level sentinel errors over ad hoc strings or boolean flags; export
those sentinels when other packages need to match them. The repository's current
sentinel set includes `ErrNoAvailableBackend`, `ErrRestartRequired`,
`errFetchBlocked`, `errBodyTooLarge`, and `egress.ErrBlocked`. For
lifecycle/cleanup code, prefer `io.Closer` and make teardown deterministic so
resources are released even when a later step fails.

## Documentation conventions

Every feature change must include docs. The rule is simple: **if a user could
configure it, they must be able to read about it.**

### Where docs live

| Audience | Location |
|----------|----------|
| New users evaluating the project | `README.md` (overview + quick-start only) |
| Operators configuring the server | `docs/configuration.md` (canonical reference) |
| Developers using a specific feature | `docs/<feature>.md` (deep-dive) |
| Contributors | `CONTRIBUTING.md` (this file) |

### Writing docs

1. **Use clear headings.** A reader should know what a section covers from its
   title alone.
2. **Provide concrete examples.** Every config key should have a copy-pasteable
   TOML snippet.
3. **State defaults explicitly.** Use tables with a "Default" column.
4. **Document build tags.** Every optional feature must state its build tag
   prominently.
5. **Document limitations.** If a feature does not support something (e.g.,
   WebSocket over HTTP/3), say so explicitly.
6. **Keep cross-links working.** Use relative paths (`docs/cache.md`, not
   absolute URLs).
7. **Match code and docs.** When a default or behavior changes, update the doc
   in the same PR.

### Adding a new feature

When adding a new feature, include **all** of these in your PR:

- [ ] Code implementation with tests (`*_test.go`).
- [ ] Config schema update (`internal/config/schema.go`).
- [ ] Config validation (`internal/config/validate.go`).
- [ ] Lifecycle disposition for every new configuration leaf
      (`internal/lifecycle/registry.go`), then `make lifecycle-generate`.
- [ ] Description for every new configuration leaf (a Go doc comment on the
      field, or an `internal/configcontract.DescriptionOverrides` entry when
      none applies), then `make config-contract-generate`.
- [ ] Admin diff support (`internal/admin/diff_helpers.go`) when applicable.
- [ ] Documentation in `docs/<feature>.md` or update to `docs/configuration.md`.
- [ ] README mention (brief, with a link to the deep-dive doc).
- [ ] Example config in `testdata/<feature>.toml` or `examples/<feature>/`.
- [ ] Security/threat note update in `SECURITY.md` or `docs/<feature>.md` when
      security-relevant.
- [ ] `CHANGELOG.md` entry under `[Unreleased]`.
- [ ] Metrics update in `internal/observability/metrics.go` when applicable.

### Adding a configuration field

The configuration lifecycle is closed-world: every public TOML leaf must have
exactly one disposition, and CI fails when one is missing. Follow this order:

1. Add the field to `internal/config/schema.go` with its `toml` tag.
2. Add validation to `internal/config/validate.go`.
3. Add its lifecycle entry to `internal/lifecycle/registry.go`. Classify what
   the code **already does** — never mark a field `hot_reload` in anticipation
   of work that has not landed. If the effective value needs different
   comparison semantics (a file-content digest, an order-insensitive list, a
   secret digest, a per-listener grouping), add a case to `specialExtractor`.
4. Run `make lifecycle-generate` and review the diff in
   `docs/config-lifecycle.yaml` and `docs/generated/config-lifecycle.{md,json}`.
   Never hand-edit those files: they are outputs, and the runtime does not read
   them.
5. Run `make config-contract-generate` and review the diff in
   `docs/generated/config.schema.json`, `config-metadata.json` and
   `config-reference.md` (ADR 0019 §21-23). These are also outputs, rendered
   from `config.SchemaPaths()`, the lifecycle registry, `docs/config-value-contract.json`
   and the small capability/resource/description tables in
   `internal/configcontract`. A new leaf needs a description (a Go doc comment
   on the field is usually enough); `make generated-check` fails otherwise.
6. Update conceptual prose in `docs/reload-semantics.md` only when the
   transition semantics changed — the per-field table is generated.
7. Verify with `make generated-check` (which `make ci-pr` runs) and
   `go test ./internal/lifecycle ./internal/config ./internal/configcontract`.

A stale artifact fails with the exact regeneration command, so a missing
regeneration is never a guess.

### Changelog entries

Add an entry to `CHANGELOG.md` under `[Unreleased]` for every user-visible
change:

- **Added** for new features.
- **Changed** for behavior changes.
- **Fixed** for bug fixes.
- **Security** for vulnerability fixes.

Move entries into a version section at release time.

## Commit message style

Use conventional commits when possible:

- `feat:` — new feature
- `fix:` — bug fix
- `docs:` — documentation only
- `test:` — adding or correcting tests
- `refactor:` — code change that neither fixes a bug nor adds a feature
- `chore:` — build process or auxiliary tool changes

Example:

```
feat(cache): add stale-if-error support

When a background revalidation encounters a 5xx upstream error,
extend the stale-serving window by the configured stale_if_error
duration. This protects clients from backend outages.

Closes #42.
```

## Pull request process

1. Open a **draft PR** early for feedback.
2. Ensure CI passes (`make ci-fast`; `make ci-full` if build-tagged features changed).
3. Request review from at least one maintainer.
4. Address review comments and mark conversations as resolved.
5. Maintainers will merge when approved and CI is green.

## Release process

Only maintainers cut releases. See `docs/release.md` for the full checklist.

## Questions?

- Open a **discussion** for questions about usage or architecture.
- Open an **issue** for bugs or feature requests.
- Join the community channels listed in the repository README.

## Licensing & DCO

Jul.IA is licensed under the **GNU Affero General Public License v3.0 or later**
(AGPL-3.0-or-later). By contributing to this project, you agree that your
contributions will be licensed under the same terms.

We use the **Developer Certificate of Origin (DCO)**. By submitting a pull
request, you certify that you have the right to submit the code and that you
agree to the DCO (https://developercertificate.org).

All commits must include a `Signed-off-by:` line indicating who authored the
work. Use `git commit -s` to add it automatically:

```bash
git commit -s -m "feat(cache): add stale-if-error support"
```

The repository also uses the **DCO GitHub App**, which blocks pull requests
that contain commits without a `Signed-off-by` line.
