# D11 Console, API and CLI consumers

This area reconciles nine original rows: the eight Console/API rows plus the
original CLI/help wildcard, reassigned from examples to its actual command
owner. Every frozen CLI/admin wildcard path remains in the inventory. It also
adds one clearly supplemental current Console-guide consumer without changing
the original 112-row denominator. The companion
[ledger](2026-10-13-d11-certification.json) supplies individual claims, verdicts,
source/tests, evidence and limits. The human reviewer spot-check is pending.

## Findings and corrections

The Console build promise is Node-free at runtime, with committed assets built
by maintainers using Node/pnpm. The SPA and the supported minimal configuration
GUI have distinct CSP policies. Original substrate API/layout/phase tables are
dated June design, not the current mounted route inventory or maturity.

Local RBAC delivers named principals, predefined/custom roles and one
config-defined credential per principal, hashed in the policy. Source secret
storage still needs operator protection. There is no mandatory prefixed token
shape, API issuance, multi-token store or immediate API revocation. Public IDs
derive from 12 SHA-256 hex characters; request-generation policy publication is
distinct from revocation of an already captured request. The permission guide
now separates safe projections/raw readback, trust/adoption grants, logical-OR
route grants and additional object checks. Catalogs, not a stale duplicated
planning matrix, own actual authorization. #514's completed bounded review is
preserved; this is not a new security certification.

Quick editors emit typed drafts through POST patch preview/apply, never the
invented PATCH /api/config route. Global/Traffic forms use #81's delivered
sparse typed operations. History captures previous bytes during preparation,
but durable history finalizes after terminal outcome and can degrade; undo is
conditional on a retained successful snapshot. Pending mutations retain their
result and are not replayed by credential recovery. Accessibility describes
the actual keyboard widgets and enabled-cancellation behavior; it does not
claim a full WCAG/contrast audit.

Two actual audit gaps are repaired separately in [#603](https://github.com/victornife/jul/pull/603):
CSV credential identity and anonymous credential-rejection events (#602). The
stale known-grant confirmations are repaired in
[#597](https://github.com/victornife/jul/pull/597) (#595). Their failing/passing
reproductions are retained in those corrective sources. These are selected
repairs, not accepted limitations or claims that drafts have merged.

## Source and verification

Go runtime source stays main
`616f0ca0ec7ef3f7568ec8b11d0fc1c783df14bc` in this docs area. Fresh local
`console importer` race execution passes focused actual CLI binaries/exit0–9,
validation/importer/help and public adminapi/apicontract/rbac owners. The broad
cmd/jul attempt failed only its AF_UNIX corpus scenarios because this
environment rejects Unix sockets; that raw failure is retained, not a pass.
The focused CLI run excludes those corpus scenarios and states its boundary.
Hosted #592 full runtime tests certify the same unchanged runtime at their own
checkout. Lean/full actual CLI help is retained.

#517 proof is reused with original source identity: #592 runs 896 tests in
78 files and six report fixtures, with independent path-floor failure probes;
#597 runs 903 tests with the confirmation repair and fresh probes. Their
coverage is frontend path/global coverage, not Go patch or security coverage.
The final repair suite keeps config/client >=80% statements/>=70% branches,
history >=80% statements, exact ConfigPanel/reducer >=80% statements and global
58/45/40/50 statement/branch/function/line floors. No docs change lowers them.

Local docs/regression/semantic/generated/inventory checks and exact-head hosted
checks remain mandatory. No Go production statement changes are introduced by
this area. NGINX translation is unchanged; migration remains a CLI/API-native
surface under ADR0014. This packet supplements preparation only; maintainer
integration and the actual >=12-original-row review remain separate.
