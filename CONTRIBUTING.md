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
   make ci-full          # full feature set — matches CI exactly
   ```

## Coding standards

- Go style follows `gofmt` and `golangci-lint`.
- Build tags for optional features use `//go:build <tag>`.
- Every `_stub.go` file must provide a clear error when the tag is missing.
- Config changes require updates to `internal/config/schema.go` and
  `internal/config/validate.go`.
- New handlers and middleware must have table-driven tests.
- Panics are contained: `middleware/recover.go` catches them; re-panic only
  `http.ErrAbortHandler`.

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
- [ ] Admin diff support (`internal/admin/diff_helpers.go`) when applicable.
- [ ] Documentation in `docs/<feature>.md` or update to `docs/configuration.md`.
- [ ] README mention (brief, with a link to the deep-dive doc).
- [ ] Example config in `testdata/<feature>.toml` or `examples/<feature>/`.
- [ ] Security/threat note update in `SECURITY.md` or `docs/<feature>.md` when
      security-relevant.
- [ ] `CHANGELOG.md` entry under `[Unreleased]`.
- [ ] Metrics update in `internal/observability/metrics.go` when applicable.

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
