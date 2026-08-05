#!/usr/bin/env python3
"""Apply the one-time Stage 0/1 programme-truth closure edits.

This script is intentionally temporary. The companion workflow runs it on the
closure branch, validates the documentation, and removes both temporary files
before committing the actual repository changes.
"""

from __future__ import annotations

from pathlib import Path
from textwrap import dedent

ROOT = Path(__file__).resolve().parents[2]


def read(path: str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


def write(path: str, text: str) -> None:
    target = ROOT / path
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(text, encoding="utf-8")


def replace_once(path: str, old: str, new: str) -> None:
    text = read(path)
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected one exact match, found {count}: {old[:120]!r}")
    write(path, text.replace(old, new, 1))


def insert_before_once(path: str, marker: str, addition: str, unique_marker: str) -> None:
    text = read(path)
    if unique_marker in text:
        return
    count = text.count(marker)
    if count != 1:
        raise SystemExit(f"{path}: expected one insertion marker, found {count}: {marker!r}")
    write(path, text.replace(marker, addition + marker, 1))


# ---------------------------------------------------------------------------
# Roadmap: close programme truth and the non-cache correction tranche, while
# making #131 the explicit next issue without starting it.
# ---------------------------------------------------------------------------
replace_once(
    "docs/roadmap/README.md",
    "> Version 2.0 · Updated 2026-08-05",
    "> Version 2.1 · Updated 2026-08-05",
)
replace_once(
    "docs/roadmap/README.md",
    "| **0 — Programme and truth** | Combined audit, current product truth, operating model | One audit, one tracker, canonical docs synchronized | ✅ #165/#166 merged |",
    "| **0 — Programme and truth** | Combined audit, current product truth, operating model and historical-audit disposition | One audit, one tracker, canonical docs synchronized | ✅ reconciled; #114/#119/#130 closed |",
)
replace_once(
    "docs/roadmap/README.md",
    "| **1 — Immediate correctness** | Access-log semantics and cache correctness | No known P0; selected P1 behavior documented and tested | 🚧 selected tranche verified in `v1.32.1-rc.1`; cache remains |",
    "| **1 — Immediate non-cache correctness** | Strict configuration, HTTP/3 mTLS, ACME, compression, access logs, metrics and WAF contracts | No known non-cache P0; selected P1 corrections documented and tested | ✅ verified in `v1.32.1-rc.1`; #129 remains a non-blocking quality track |",
)
replace_once(
    "docs/roadmap/README.md",
    "| **2 — Cache correctness** | Generation-owned revalidation, immutable entries, HTTP semantics, upgrade transparency, recertification | Race-clean, protocol-safe, truthful conformance matrix | ⬜ planned |",
    "| **2 — Cache correctness** | Generation-owned revalidation, immutable entries, HTTP semantics, upgrade transparency, recertification | Race-clean, protocol-safe, truthful conformance matrix | ▶ next: #131; implementation not started |",
)
insert_before_once(
    "docs/roadmap/README.md",
    "## Immediate critical path\n",
    dedent(
        """
        ### Tracker-numbering crosswalk

        The roadmap deliberately consolidates the more granular numbering in #62:

        - roadmap Stage 0 = #62 Stage 0 programme reconciliation plus Stage 1 audit/documentation truth;
        - roadmap Stage 1 = the completed #62 Stage 2 non-cache correctness tranche;
        - roadmap Stage 2 = #62 Stage 3 cache correctness and recertification.

        This avoids two competing execution models. #62 owns issue-level status;
        this roadmap owns the durable portfolio sequence.

        """
    ),
    "### Tracker-numbering crosswalk",
)
replace_once(
    "docs/roadmap/README.md",
    "- Close the current audit with exact-SHA evidence.",
    "- Preserve #130's exact-SHA maintainer certification and historical-supersession record; no independent two-human certification is claimed.",
)

# ---------------------------------------------------------------------------
# Audit register: record the final historical disposition and current gates.
# ---------------------------------------------------------------------------
replace_once(
    "docs/audit-register.md",
    "| [2026-08-03 combined repository re-audit](audit/combined-audit-2026-08-03.md) | `66c71b2d48f578a770d5c6e5d86a0e5a9dcada9a` | Current implementation and planning source of truth | Active; implementation underway through staged PRs | #62, #107-#162 |",
    "| [2026-08-03 combined repository re-audit](audit/combined-audit-2026-08-03.md) | `66c71b2d48f578a770d5c6e5d86a0e5a9dcada9a` | Current implementation and planning source of truth | Active; programme truth closed, cache programme next | #62, #107-#162 |",
)
replace_once(
    "docs/audit-register.md",
    "| [2026-07-31 full repository audit](audit/2026-07-31-full-repository-audit.md) | `e8865615` plus recorded remediation commits | Historical audit and remediation evidence | Historical; formal closure evidence tracked by #130 | #130 |",
    "| [2026-07-31 full repository audit](audit/2026-07-31-full-repository-audit.md) | `e8865615` plus recorded remediation commits | Historical audit and remediation evidence | Historical; exact-SHA maintainer-certified and superseded under #130, not independently two-human certified | #130 |",
)
replace_once(
    "docs/audit-register.md",
    "The current combined audit does not rewrite the historical record. It supersedes the July audit only for current prioritisation, sequencing, capability truth and implementation planning.",
    "The current combined audit does not rewrite the historical record. It supersedes the July audit for current prioritisation, sequencing, capability truth and implementation planning. The [Stage 0/1 programme closure](audit/2026-08-05-stage-0-1-programme-closure.md) records the exact disposition, residual transfers and branch-cleanup gate.",
)
replace_once(
    "docs/audit-register.md",
    dedent(
        """
        - Documentation truth: #119 and #130.
        - Immediate correctness/security: #120-#127 and #129.
        - Cache correctness: #107 and #131-#134.
        - Lifecycle authority: #89 and #128.
        - Core Gateway Completeness decisions: #114-#118.
        - Core implementation: #135-#151.
        - Selected runtime dynamics: #88-#106 and #157-#161.
        - Migration/diagnostics: #112 and #152-#156.
        - Bounded experiment: #113 and #162.
        """
    ),
    dedent(
        """
        - Completed programme truth and correction tranche: #114, #119, #120-#127 and #130.
        - Non-blocking quality foundation: #129.
        - Active cache correctness: #107 and #131-#134.
        - Next lifecycle authority: #89 and #128.
        - Core Gateway Completeness decisions: #115-#118.
        - Core implementation: #135-#151.
        - Selected runtime dynamics: #88-#106 and #157-#161.
        - Migration/diagnostics: #112 and #152-#156.
        - Bounded experiment: #113 and #162.
        """
    ),
)
replace_once(
    "docs/audit-register.md",
    "| R9-14.4 | Never-draining shutdown test | — | *(deferred)* | — | ⏸ Deferred | — |",
    "| R9-14.4 | Never-draining shutdown test | final lifecycle/soak closure | Existing bounded-shutdown coverage; final integrated evidence remains in #106 | transferred, not silently closed | ↪ Superseded/non-blocking | 2026-08-05 |",
)
replace_once(
    "docs/audit-register.md",
    "| R9-14.5 | Hot-added TLS rotation test | — | *(deferred)* | — | ⏸ Deferred | — |",
    "| R9-14.5 | Hot-added TLS rotation test | #100 static certificate generation | Real TCP/QUIC rotation evidence required by #100 | transferred to selected runtime-dynamics work | ↪ Superseded/non-blocking | 2026-08-05 |",
)
replace_once(
    "docs/audit-register.md",
    dedent(
        """
        - **R9-14.4 (never-draining shutdown test)** — timing-sensitive and better handled through the current lifecycle/soak closure programme. The underlying shutdown behavior has focused unit coverage, but exact final evidence remains tracked by #130 and later closure work.
        - **R9-14.5 (hot-added TLS rotation)** — now related to the selected static-certificate generation work in #100 and the current HTTP/3 correctness work in #121. Do not close it based only on the old audit wording.
        """
    ),
    dedent(
        """
        - **R9-14.4 (never-draining shutdown test)** — transferred to the final lifecycle/soak closure in #106. Existing bounded-shutdown tests remain evidence; no new pre-cache blocker was inferred.
        - **R9-14.5 (hot-added TLS rotation)** — transferred to selected issue #100. HTTP/3 mTLS parity is already corrected by #121; certificate rotation remains later runtime-dynamics work and is not a prerequisite for #131.
        """
    ),
)

# ---------------------------------------------------------------------------
# Status: distinguish independent audit closure from maintainer certification.
# ---------------------------------------------------------------------------
replace_once(
    "docs/status.md",
    "> Version 2.0 · Updated 2026-08-05",
    "> Version 2.1 · Updated 2026-08-05",
)
replace_once(
    "docs/status.md",
    "| **audit-closed** | Any reopened audit finding covering it is formally Closed (exact-SHA CI + two human sign-offs). |",
    "| **audit-closed** | Any reopened audit finding covering it is formally Closed under its recorded rule, including exact-SHA CI and any required independent sign-offs. |\n| **maintainer-certified** | Exact-SHA evidence is recorded by the maintainer and the historical audit is explicitly superseded; this does not claim independent two-human certification. |",
)
replace_once(
    "docs/status.md",
    dedent(
        """
        - **Configuration write/apply/reload subsystem** — *remediated* across workstreams
          WS01–WS07 with tests, but the reopened
          [configuration-audit closure](audit/old/2026-07-25-configuration-audit-closure.md) is
          **not formally Closed** (exact-SHA CI + two sign-offs outstanding). Treat it as
          *remediated, closure pending*. Context: the
          [Full Repository Audit (2026-07-31)](audit/2026-07-31-full-repository-audit.md).
        """
    ),
    dedent(
        """
        - **Configuration write/apply/reload subsystem** — implementation remains
          remediated across workstreams WS01–WS07. Under #130 the historical closure is
          exact-SHA **maintainer-certified** and historically superseded by the current
          combined audit. It is intentionally not described as independently
          `audit-closed`, because the historical two-human sign-off rule was not met. See
          the [Stage 0/1 programme closure](audit/2026-08-05-stage-0-1-programme-closure.md)
          and the preserved
          [configuration-audit closure](audit/old/2026-07-25-configuration-audit-closure.md).
        """
    ),
)

# ---------------------------------------------------------------------------
# Documentation navigation and current audit banner.
# ---------------------------------------------------------------------------
insert_before_once(
    "docs/index.md",
    "- **[Master implementation tracker](https://github.com/victornife/jul/issues/62)**",
    "- **[Stage 0/1 programme closure](audit/2026-08-05-stage-0-1-programme-closure.md)** — Exact-SHA maintainer certification, historical-audit disposition, residual transfers and the gate before #131.\n",
    "Stage 0/1 programme closure",
)
replace_once(
    "docs/index.md",
    "- **[Feature status & GA matrix](status.md)** — What is GA, what is Beta, and what the maturity bar means. Read it together with the current audit while correction issues remain open.",
    "- **[Feature status & GA matrix](status.md)** — What is GA, what is Beta, and what the maturity bar means. Read it together with the current audit; cache recertification is the active correctness programme.",
)
replace_once(
    "docs/audit/combined-audit-2026-08-03.md",
    "**Status:** current authoritative audit and implementation-planning baseline  \n**Supersedes for current planning:**",
    "**Status:** current authoritative audit and implementation-planning baseline  \n**Execution status (2026-08-05):** programme/audit truth and the non-cache correction tranche are closed; #131 is the next issue and has not started  \n**Supersedes for current planning:**",
)

# ---------------------------------------------------------------------------
# Preserve the historical audit rule while recording the chosen disposition.
# ---------------------------------------------------------------------------
old_audit_path = "docs/audit/old/2026-07-25-configuration-audit-closure.md"
old_audit = read(old_audit_path)
append_marker = "## 2026-08-05 maintainer certification and historical supersession"
if append_marker not in old_audit:
    old_audit += dedent(
        """

        ---

        ## 2026-08-05 maintainer certification and historical supersession

        Issue #130 closes the remaining programme obligation using a deliberately
        narrower disposition than the historical `Closed` rule above:

        - the current repository state is certified against an exact `main` SHA and
          complete GitHub Actions CI;
        - the maintainer records the evidence and residual disposition;
        - the 2026-08-03 combined audit is the current authority;
        - this document remains historical evidence and is not rewritten to claim the
          two independent human sign-offs that were never obtained.

        The resulting status is **maintainer-certified and historically superseded**,
        not independently `audit-closed`. The exact final SHA and CI run are recorded
        in the #130 completion comment; that issue record is authoritative because a
        commit cannot embed its own final SHA without creating a new SHA.

        Final residual disposition:

        - R9-14.4 is transferred to the final lifecycle/soak closure in #106 and is
          non-blocking before cache correctness;
        - R9-14.5 is transferred to #100 static certificate rotation and is
          non-blocking before cache correctness;
        - #129 remains a non-blocking security-quality workstream before final
          integrated release closure;
        - cache correctness #107/#131-#134 is the next active programme gate.

        See the
        [Stage 0/1 programme closure](../2026-08-05-stage-0-1-programme-closure.md)
        and [audit register](../../audit-register.md) for the current relationship.
        """
    )
    write(old_audit_path, old_audit)

# ---------------------------------------------------------------------------
# New durable closure record. Exact final SHA/CI are written to #130 after merge.
# ---------------------------------------------------------------------------
closure_doc = dedent(
    """
    # Stage 0/1 programme and audit-truth closure

    **Date:** 2026-08-05  
    **Repository:** `victornife/jul`  
    **Pre-closure baseline:** `main@1f9850d1bc33100df07944174e56f8de7e3572e1`  
    **Pre-closure CI:** workflow run `31031732788` — success  
    **Disposition:** exact-SHA maintainer-certified; historical audit superseded  
    **Next issue:** #131 — ready after this closure; implementation not started

    ## Purpose

    Close the programme-reconciliation and documentation-truth prerequisites before
    response-cache correctness work begins. This record reconciles #62, the roadmap,
    the audit register, historical audit evidence, completed correction issues and
    repository branches without changing runtime behavior.

    The exact final merge/main SHA and complete CI run are recorded in issue #130's
    completion comment. The issue record is authoritative for those values because a
    commit cannot embed its own final SHA without producing a different SHA.

    ## Outcome

    | Area | Final disposition |
    | --- | --- |
    | Stage 0 programme reconciliation | Complete: one tracker, reconciled child contracts and final dependency map |
    | Stage 1 audit/documentation truth | Complete under the maintainer-certified historical-supersession model |
    | Non-cache correction tranche | Complete: #120-#127 delivered; #129 remains a non-blocking quality track |
    | Cache correctness | Next active programme, beginning with #131 after this closure |
    | Lifecycle authority | #89 follows cache recertification in the serial critical path |
    | Phase 5 structured configuration | #77-#82 remain sequential after #89 |
    | Branch structure | One-time cleanup deletes every non-`main` branch after merge and green CI |

    ## Issue reconciliation

    The following completed contracts receive completion evidence and close as part
    of this operation:

    - #114 — operating model and operability surfaces, delivered by #166;
    - #119 — current product truth, delivered by #165;
    - #120 — strict TOML fields, delivered by #167;
    - #121 — HTTP/3 mTLS parity, delivered by #169;
    - #122 — exclusive ACME challenge selection, delivered by #170;
    - #125 — compression `no-transform`, delivered by #168;
    - #130 — historical audit evidence closure under this disposition.

    Current programme comments supersede stale sections in #78-#82, #88, #91,
    #98 and #101-#103. In particular:

    - #82 no longer opens an automatic AI implementation phase;
    - #91 does not require #90 unless implementation proves a concrete resource seam;
    - #98 includes `observability.access_log.enabled` from #124;
    - #101 treats #121 as complete and retains only optional dynamic TLS/mTLS work;
    - #102 excludes `alt_svc_max_age`, which belongs to #161;
    - #103 treats #122 as complete and retains only optional dynamic ACME work.

    ## Historical audit disposition

    The preserved configuration-audit document required exact-SHA CI plus two
    independent human sign-offs before using its formal `Closed` label. This
    repository is solo-maintained and those two sign-offs were not obtained.

    The record therefore does not rewrite history or claim that rule was met. #130
    closes the current obligation with a precise alternative:

    1. complete CI is green on an exact current SHA;
    2. the maintainer records the evidence and residuals;
    3. the combined 2026-08-03 audit remains the current source of truth;
    4. the July audit is retained as historical evidence;
    5. status is **maintainer-certified and historically superseded**, not
       independently two-human certified.

    ## Residuals and transfers

    | Residual | Disposition | Blocks #131? |
    | --- | --- | --- |
    | R9-14.4 never-draining shutdown evidence | Transferred to final lifecycle/soak closure #106; existing bounded-shutdown tests retained | No |
    | R9-14.5 hot-added TLS rotation | Transferred to selected runtime-dynamics issue #100 | No |
    | #129 security-package coverage floors | Remains a parallel/non-blocking quality foundation before final integrated release closure | No |
    | #89 closed-world lifecycle authority | Required after cache recertification and before #77 | No, sequenced after cache |

    ## Final dependency map

    ```text
    programme/audit truth closure
       ↓
    #131 — generation ownership, cancellation and immutable entries
       ↓
    #133 — ResponseWriter transparency, upgrade and streaming behavior
       ↓
    #132 — shared-cache HTTP conformance, invalidation, 304 and Range bypass
       ↓
    #134 — fresh audit, protocol evidence, race/leak/soak, benchmarks and maturity decision
       ↓
    close #107 and explicitly re-gate #92/#93
       ↓
    #89 — closed-world lifecycle authority
       ↓
    #77 → #78 → #79 → #80 → #81 → #82
    ```

    No #131 implementation is included in this closure.

    ## Branch cleanup

    The pre-closure repository contained 30 non-`main` branches and no open pull
    requests. The temporary cleanup workflow included with this change runs only
    when merged to `main`, after the PR has passed required checks. It deletes every
    remaining non-`main` branch and verifies that only `main` remains. Tags,
    releases, attestations and commit history are unaffected.

    The one-time workflow is removed from `main` after successful cleanup, followed
    by a final CI run on the cleanup commit.

    ## Validation and evidence

    Required evidence for completion:

    - documentation, link and diff checks on the closure branch;
    - full pull-request CI on the exact PR head;
    - merge to `main`;
    - complete push CI on the exact merge/main SHA;
    - branch cleanup result showing only `main`;
    - final CI after removal of the one-time cleanup workflow;
    - exact SHA and run IDs recorded in #130 and #62.

    ## Non-goals

    - no cache implementation;
    - no #131 code or test changes;
    - no stable release publication;
    - no claim of independent human audit sign-off;
    - no implementation of #100, #129 or #89;
    - no deletion of tags, releases or historical commits.

    ## Residual risk

    The repository is ready to begin #131 after the exact-SHA and branch-cleanup
    gates above are complete. Cache conformance remains intentionally open and no
    GA recertification claim is made before #134.
    """
).lstrip()
write("docs/audit/2026-08-05-stage-0-1-programme-closure.md", closure_doc)

# ---------------------------------------------------------------------------
# One-time post-merge branch cleanup. It is removed from main after it succeeds.
# ---------------------------------------------------------------------------
cleanup_workflow = dedent(
    """
    name: One-time programme branch cleanup

    on:
      push:
        branches: [main]
        paths:
          - ".github/workflows/cleanup-programme-branches.yml"

    permissions:
      contents: write

    jobs:
      cleanup:
        if: github.repository == 'victornife/jul'
        runs-on: ubuntu-latest
        steps:
          - name: Delete every branch except main
            env:
              GH_TOKEN: ${{ github.token }}
              REPO: ${{ github.repository }}
            shell: bash
            run: |
              set -euo pipefail

              mapfile -t branches < <(
                gh api --paginate "repos/${REPO}/branches?per_page=100" --jq '.[].name'
              )

              for branch in "${branches[@]}"; do
                if [[ "${branch}" == "main" ]]; then
                  continue
                fi
                encoded="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "${branch}")"
                echo "Deleting ${branch}"
                gh api --method DELETE "repos/${REPO}/git/refs/heads/${encoded}"
              done

              gh api --paginate "repos/${REPO}/branches?per_page=100" --jq '.[].name' > /tmp/branches-after.txt
              unexpected="$(grep -vxF main /tmp/branches-after.txt || true)"
              if [[ -n "${unexpected}" ]]; then
                echo "Non-main branches remain:" >&2
                printf '%s\n' "${unexpected}" >&2
                exit 1
              fi

              echo "Branch cleanup complete: main is the only branch."
    """
).lstrip()
write(".github/workflows/cleanup-programme-branches.yml", cleanup_workflow)

# Remove this one-time applicator and its workflow from the resulting commit.
for temporary in (
    ".github/scripts/apply_stage0_stage1_closure.py",
    ".github/workflows/apply-stage0-stage1-closure.yml",
):
    target = ROOT / temporary
    if target.exists():
        target.unlink()
