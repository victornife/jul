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
