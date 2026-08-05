from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    target = Path(path)
    text = target.read_text(encoding="utf-8")
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected one replacement anchor, found {count}")
    target.write_text(text.replace(old, new, 1), encoding="utf-8")


replace_once(
    "CHANGELOG.md",
    '''> **Release state:** these changes are **merged to `main`** but **not yet tagged
> or released**. Per the delivery-state vocabulary in [docs/status.md](docs/status.md),
> "merged" is not "released" or "soaked"; the Phase 4 egress hardening becomes
> **GA** only after its first tagged release passes the post-GA soak gate.
''',
    '''> **Release state:** these changes are **merged to `main`** and selected for the
> unpublished `v1.32.1-rc.1` release-candidate checkpoint. An immutable RC tag
> and draft GitHub Release provide verification evidence; they are not a stable
> publication. Per the delivery-state vocabulary in [docs/status.md](docs/status.md),
> "merged", "candidate", "released", and "soaked" remain distinct. This section
> stays `[Unreleased]` until a stable-release decision is made; Phase 4 egress
> remains stable-release pending even when included in the RC.
''',
)

replace_once(
    "README.md",
    '''> **Delivery state ≠ maturity.** "GA" describes the released maturity
> contract; changes merged to `main` but not yet tagged remain under
> [`CHANGELOG.md`](CHANGELOG.md) `[Unreleased]`. The current correction tranche
> has merged strict unknown-field rejection, HTTP/3 mTLS parity, compression
> `no-transform`, exclusive ACME challenge selection, dependency and CI fixes.
> The response cache remains under a documented correctness recertification; see
> [`docs/cache.md`](docs/cache.md), [`docs/status.md`](docs/status.md), and the
> [combined audit](docs/audit/combined-audit-2026-08-03.md).
''',
    '''> **Delivery state ≠ maturity.** "GA" describes the stable released maturity
> contract. Changes merged to `main` remain under
> [`CHANGELOG.md`](CHANGELOG.md) `[Unreleased]`; an immutable `vX.Y.Z-rc.N` tag
> with draft artifacts is a **candidate**, not stable publication. The selected
> `v1.32.1-rc.1` checkpoint covers strict unknown- and known-value validation,
> HTTP/3 mTLS parity, compression `no-transform`, exclusive ACME challenge
> selection, the frozen Prometheus contract, bounded/redacted WAF request
> logging, explicit access-log enablement, and dependency/CI fixes. The response
> cache remains under a documented correctness recertification; see
> [`docs/cache.md`](docs/cache.md), [`docs/status.md`](docs/status.md), and the
> [combined audit](docs/audit/combined-audit-2026-08-03.md).
''',
)

replace_once(
    "docs/status.md",
    "> Version 2.0 · Updated 2026-08-04",
    "> Version 2.0 · Updated 2026-08-05",
)
replace_once(
    "docs/status.md",
    '''| **implemented** | Code exists and its tests pass on a working branch. |
| **merged** | Landed on `main`; listed under `[Unreleased]` in [CHANGELOG.md](../CHANGELOG.md), not yet tagged. |
| **released** | Shipped in a tagged `vX.Y` build with cross-compiled artifacts. |
| **soaked** | The post-GA soak gate ([ADR 0005](adr/0005-soak-post-ga-gate.md)) has passed for the released build. |
| **audit-closed** | Any reopened audit finding covering it is formally Closed (exact-SHA CI + two human sign-offs). |
''',
    '''| **implemented** | Code exists and its tests pass on a working branch. |
| **merged** | Landed on `main` and listed under `[Unreleased]` in [CHANGELOG.md](../CHANGELOG.md); it is not stable publication. |
| **candidate** | Frozen in an immutable `vX.Y.Z-rc.N` tag with draft release artifacts for review; it is not a published stable release. |
| **released** | Published in a stable tagged `vX.Y.Z` build with cross-compiled artifacts. |
| **soaked** | The post-GA soak gate ([ADR 0005](adr/0005-soak-post-ga-gate.md)) has passed for the released build. |
| **audit-closed** | Any reopened audit finding covering it is formally Closed (exact-SHA CI + two human sign-offs). |
''',
)
replace_once(
    "docs/status.md",
    '''- **Egress allow-list (Phase 4)** — *merged* to `main` (see [CHANGELOG.md](../CHANGELOG.md)
  `[Unreleased]`); its first tagged **release** is pending. The implementation is
  complete and tested: treat it as *merged, release pending*.
''',
    '''- **Egress allow-list (Phase 4)** — *merged* to `main` and selected for the
  unpublished `v1.32.1-rc.1` candidate checkpoint. Until stable publication,
  treat it as *candidate, stable release pending*; RC soak is release-path
  evidence rather than GA publication.
''',
)
replace_once(
    "docs/status.md",
    "- **Configuration lifecycle:** strict unknown-field decoding, fail-closed known-value validation, and explicit access-log enablement are implemented in the current tree and release pending; closed-world lifecycle generation (#89) remains open. Access-log changes are restart-required until #98.",
    "- **Configuration lifecycle:** strict unknown-field decoding, fail-closed known-value validation, and explicit access-log enablement are selected for the `v1.32.1-rc.1` candidate; stable publication remains pending. Closed-world lifecycle generation (#89) remains open, and access-log changes are restart-required until #98.",
)
replace_once(
    "docs/status.md",
    "- **Prometheus and WAF logging:** the exact `v1.32.0` metric surface is preserved and CI-frozen, additive families are release-pending, and WAF matched-request warnings are now path-only and bounded with query/request-derived message data omitted.",
    "- **Prometheus and WAF logging:** the exact `v1.32.0` metric surface is preserved and CI-frozen; additive families and the bounded, path-only WAF warning contract are selected for the `v1.32.1-rc.1` candidate and remain stable-release pending.",
)
replace_once(
    "docs/status.md",
    "- **Recently corrected:** HTTP/3 mTLS parity, exclusive ACME challenge selection and compression `no-transform` are merged to `main` and await the next tagged release.",
    "- **Recently corrected:** HTTP/3 mTLS parity, exclusive ACME challenge selection and compression `no-transform` are merged to `main` and selected for the `v1.32.1-rc.1` candidate; stable publication remains a later decision.",
)

replace_once(
    "docs/release.md",
    '''> cut by [`.github/workflows/release.yml`](../.github/workflows/release.yml) when
> a `v*` tag is pushed, and only after the build/test gate **and** the ADR-0005
> soak gate are green — a regression cannot ship under a tag.
''',
    '''> cut by [`.github/workflows/release.yml`](../.github/workflows/release.yml) when
> a `v*` tag is pushed or the workflow is explicitly dispatched at an existing
> immutable `v*` tag. The build/test gate **and** ADR-0005 soak gate must both be
> green — a regression cannot ship under a tag.
''',
)
replace_once(
    "docs/release.md",
    '''flowchart LR
  tag([push tag v*]) --> gate[gate: vet + build + test]
  tag --> soak[soak gate ADR-0005]
  gate --> build[build matrix: os × arch × profile]
  soak --> build
  build --> publish[publish: draft GitHub Release]
''',
    '''flowchart LR
  tag([tag push or tag-ref dispatch]) --> preflight[require refs/tags/v*]
  preflight --> gate[gate: vet + build + test]
  preflight --> soak[soak gate ADR-0005]
  gate --> build[build matrix: os × arch × profile]
  soak --> build
  build --> publish[publish: draft GitHub Release]
''',
)
replace_once(
    "docs/release.md",
    '''1. **`gate`** — `go vet`/`go build`/`go test` with the full tag set.
2. **`soak`** — the ADR-0005 long-running soak; a red run blocks the release.
3. **`build`** (matrix) — cross-compiles a static (`CGO_ENABLED=0`),
   `-trimpath` binary per cell, stamps `main.version` from the tag, generates
   the SBOM, archives, checksums, and attests provenance + SBOM.
4. **`publish`** — collects every archive, writes `SHA256SUMS`, and opens a
   **draft** GitHub Release. A maintainer reviews the assets and publishes it.
''',
    '''1. **`preflight`** — fails closed unless the run is bound to an existing
   immutable `refs/tags/v*` ref.
2. **`gate`** — `go vet`/`go build`/`go test` with the full tag set.
3. **`soak`** — the ADR-0005 long-running soak; a red run blocks the release.
4. **`build`** (matrix) — cross-compiles a static (`CGO_ENABLED=0`),
   `-trimpath` binary per cell, stamps `main.version` from the tag, generates
   the SBOM, archives, checksums, and attests provenance + SBOM.
5. **`publish`** — collects every archive, writes `SHA256SUMS`, and opens a
   **draft** GitHub Release. A maintainer reviews the assets and publishes it.
''',
)
replace_once(
    "docs/release.md",
    '''Because the release is created as a draft, publishing is a deliberate human
step, not an automatic side effect of pushing a tag. GitHub-generated release
notes are a starting point: before publication they must be reconciled with
`CHANGELOG.md`, the current security/status/known-limitations documents, and the
actual artifacts produced for the tagged SHA.
''',
    '''Because the release is created as a draft, publishing is a deliberate human
step, not an automatic side effect of creating a tag. A controlled
`workflow_dispatch` at the **existing tag ref** is supported for recovery when a
tag push did not start the workflow; dispatch from a branch fails preflight and
the workflow never creates or moves tags. GitHub-generated release notes are a
starting point: before publication they must be reconciled with `CHANGELOG.md`,
the current security/status/known-limitations documents, and the actual
artifacts produced for the tagged SHA.
''',
)
replace_once(
    "docs/release.md",
    '''The [current roadmap checkpoint](roadmap/README.md#release-candidate-checkpoint)
is `v1.32.1-rc.1`, to be tagged from the exact integrated `main` SHA only after
PRs #165 and #166 merge and the complete CI pipeline is green. A later stable
`v1.32.1` tag requires a separate publication decision and a fresh release run;
the RC tag is never renamed or reused.
''',
    '''The [current roadmap checkpoint](roadmap/README.md#release-candidate-checkpoint)
is `v1.32.1-rc.1`, to be tagged from the exact integrated `main` SHA only after
#165/#166 and the selected #123/#124/#126/#127 correction tranche are merged,
the documentation is reconciled, and the complete CI pipeline is green for that
exact SHA. Issue #194 is the evidence ledger. A later stable `v1.32.1` tag
requires a separate publication decision and a fresh release run; the RC tag is
never renamed or reused.
''',
)

replace_once(
    "docs/roadmap/README.md",
    "> Version 2.0 · Updated 2026-08-04",
    "> Version 2.0 · Updated 2026-08-05",
)
replace_once(
    "docs/roadmap/README.md",
    '''#165 — canonical current product truth
    ↓
#166 — operating model, roadmap v2.0 and completeness boundary
    ↓
v1.32.1-rc.1 — unpublished draft release-candidate checkpoint
    ↓
#124 — explicit access-log enablement (complete)
    ↓
#131 → #133 / #132 → #134 — cache correctness and recertification
''',
    '''#165/#166 — product truth, operating model and completeness boundary (complete)
    ↓
#123/#124/#126/#127 — selected correction tranche (complete)
    ↓
v1.32.1-rc.1 — unpublished draft release-candidate checkpoint
    ↓
#131 → #133 / #132 → #134 — cache correctness and recertification
''',
)
replace_once(
    "docs/roadmap/README.md",
    '''#124 is the remaining shared configuration-schema correction before lifecycle
generation and Phase 5. Shared edits to the configuration schema, lifecycle,
composition root, reload transaction, or Console patch contracts remain serial.
''',
    '''The selected correction tranche is complete. Cache correctness and
recertification are the next immediate correctness gate, followed by closed-world
lifecycle authority. Shared edits to the configuration schema, lifecycle,
composition root, reload transaction, or Console patch contracts remain serial.
''',
)
replace_once(
    "docs/roadmap/README.md",
    '''After #165 and #166 merge and exact-head CI is green, the next automatic patch
release candidate is **`v1.32.1-rc.1`**. Pushing that tag runs the existing
release gate, soak, lean/full cross-platform build, checksums, SBOM and
attestation workflow, and creates an unpublished draft GitHub Release for human
review. Stable publication remains a later explicit decision while selected
correctness work remains open.
''',
    '''After #165/#166 and the selected #123/#124/#126/#127 correction tranche
merge, documentation is reconciled, and exact-`main` CI is green, the next patch
release candidate is **`v1.32.1-rc.1`**. The immutable tag runs the release
preflight, gate, soak, lean/full cross-platform build, checksums, SBOM and
attestation workflow, and creates an unpublished draft GitHub Release for human
review. Stable publication remains a later explicit decision while cache
recertification and broader lifecycle-authority work remain open.
''',
)
