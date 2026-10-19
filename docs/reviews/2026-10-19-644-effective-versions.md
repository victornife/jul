# #644 implementation and acceptance evidence

Original reproduction base: `9f72938ce71392c6394dca79bae70704625ce758`.
Integration base: `509dacab64b85273d7e33a8af45fb25cf989a4de` (merged #646).
ADR 0025 is accepted for this fix; implementation merge remains gated on green
CI. This is unreleased and does not accept the proposed sharing design.

## Decision and data-flow review

ADR 0025 compares durable keyed canonical identifiers with process-only IDs.
The implementation covers the complete immutable resolved configuration using
standard HMAC-SHA-256 with a 256-bit random private key, length framing and
separate domains. Context identity is separately keyed. Raw canonical CAS,
history and baseline remain raw; sharing postcondition configuration inputs
remain raw under their separate process-only keyed scheme. Effective versions
are cached at publication, not computed by re-reading secret providers. The
same key preserves restart and rollback equality; independent keys and unknown
formats are explicitly incomparable.

Every `CanonicalVersion` production caller was inspected. Remaining calls take
raw parsed candidates, disk/baseline/history or nil. Effective calls in server
startup/reload/adoption and app apply/stage/restoration/adoption use the context.
Live/status/API/Console/apply/ledger output receives the resulting opaque ID.
Legacy planned-restart effective fields remain private and are suppressed on
public export. Incomparable metadata cannot authorize discard. Raw-digest
startup reconciliation can still converge genuine staged startup.

The companion-digest review found and fixed auth/posture generations (separate
boot-only HMAC context), private route scope fingerprints in four preparation
errors (now declaration indexes), and public token/OIDC fingerprint IDs. Public
identity, JSON/CSV audit and durable audit receive domain-separated keyed IDs;
private operation ownership keys are unchanged. Derived OIDC group principals
are keyed. A separate reviewer traced these paths and reproduced the public-ID
race regression. No raw private scope metric/log sink was found. External rule
and WASM digests describe file bytes, not configuration resolution. Support
bundles have a closed local collector registry and never collect this key file
or runtime effective configuration. Key values are excluded from Context
pointer/value formatting and all CLI/status output.

These source-level facts, plus an independent standard HMAC construction test,
provide the offline-oracle argument. Merely showing unequal outputs would not.
Plaintext inline raw secrets and intentionally exposed configuration/application
behavior remain outside the confidentiality claim. Old distributed hashes and
append-only historical files cannot be revoked.

## Acceptance table

| Requirement | Implementation and executable evidence | Current result |
| --- | --- | --- |
| Original failure | Disposable baseline `TestEffectiveVersionMustNotConfirmOfflineGuess` failed: desired version equals guessed effective canonical hash | Reproduced on unmodified base |
| Keyed derivation/framing/scopes | `internal/versionid/context_test.go`, independent standard-library HMAC oracle | Pass Linux |
| Ordinary/embedded/multiple/reused and short/empty references | Same package: accepted env/file values, full configuration, raw digest unchanged, rollback/rotation/non-secret changes | Pass Linux |
| Public apply and serving state | `internal/app/effective_version_test.go`, production HTTP `effective_version_http_test.go`, separately built binary probe | Pass Linux targeted/race |
| Durable lifecycle | `file_test.go`: concurrent initializers, backup, corruption/loss/read-only/permissions/symlinks, interrupted phase fault injection, write/sync/close failures | Pass Linux; native Windows pending |
| Companion fields and sinks | `public_identifier_test.go`, OIDC/audit suites; auth/WAF positional preparation-error tests | Pass Linux; full profile revalidation recorded below |
| Legacy/unknown/different-context pending state | `TestLegacyAndIndependentPendingVersionsNeverAuthorizeDiscard`; refused discard unchanged bytes, genuine startup reconciliation | Pass Linux |
| Restart/upgrade/downgrade | `scripts/probe-effective-version-upgrade.py`, separately built old/fixed binaries | Pass Linux |
| Raw CAS concurrency | Probe stale apply returns 409, raw revision unchanged across rotations; existing transaction tests | Pass Linux |
| Console comparison explanation | ConfigPanel and `config-history-hardening.test.tsx` for unverifiable/different-context with no discard | 17 focused tests pass |
| Full changed-statement coverage | Complete move-aware diff, lean/full + native Windows profiles, same 90% gate | Pending native platform profile; no platform exclusion/waiver |
| Platform/deployment/browser/required CI | Native CI lanes and deployment E2E | Pending final PR head |

## Transition matrix

The operator matrix and exact recovery procedures are authoritative in
[effective-versions.md](../effective-versions.md). The binary probe actually runs
old → fixed → repeated fixed startup → env/file rotation → managed hot/no-change
and raw rollback → staged cache change → fixed restart → old binary → fixed.
The raw persisted revision is unchanged when only providers rotate; desired and
serving are equal after publication, distinct while staged. The old binary runs
against converged new baseline/history/key state; the key is retained and the
fixed binary's ID returns on re-upgrade. Unresolved post-fix pending markers are
an unsupported downgrade boundary: converge with the fixed binary first. Old
binaries never retain this confidentiality guarantee.

## Verification and remaining gates

Go 1.27.2 Linux amd64. Actual local commands include affected lean/full suites,
targeted race, key fault/permission/race tests, Windows cross-compilation,
`go vet ./...` lean/full, `make generated-check`, `make config-check` and
`python3 scripts/docs-check.py` (2,391 checks before this report). Console
unit/coverage: 921 tests passed, statements 75.28%, branches 72.39%; existing
contract floors passed. Typecheck/lint/build and regenerated embedded assets.

Local full-tag CLI corpus tests cannot bind their Unix sockets in this sandbox
(`operation not permitted`); this is an unavailable local gate, not green.
An initial full run overlapped asset rebuilding and failed missing embed files;
that result is invalidated and the final batch runs after build completion.
Chromium download is unavailable locally, as recorded for #643. Native Windows
ACL/runtime, virtual-account provisioning, cross-platform changed coverage and
all required CI are merge blockers until their actual final-head results exist.
The key package currently measures 90.4% Linux package statement coverage;
that is informational and is not substituted for the complete changed gate.

## Documentation and scope dispositions

Updated secrets, API/Console, configuration/reload, deployment, README, proposed
ADR/index/0019 amendment, Docker/systemd examples and Unreleased Security entry;
OpenAPI is regenerated from DTO/catalog authorities. Roadmap and feature-status
maturity need no change: existing capability security hardening, no GA claim.
NGINX migration is not applicable; private version contexts have no imported
NGINX directive. The configuration-value/lifecycle contract is unchanged.

#640 remains Proposed. It needs a rebase companion edit: its proposed sharing
ADR currently uses main's next number 0025; rename that proposed record to 0026
and update spec/review/index references before landing. Append the distinction
between durable effective IDs and AD-09's separate process-only context, which
keys every postcondition digest, including those over raw configuration inputs.
The proposed design was refreshed at `68aed05d4e947262de4437cf97705dcb579aa811`;
precise companion edits are recorded on #640. No #641/#642 runtime work is complete.

## CI-driven deployment correction

Initial native CI reproduced missing key provisioning in package/service/smoke
starts, and overstrict Windows ancestor ACL validation. Current corrections add
private disposable contexts outside uploaded reports, explicit source Windows
installer opt-in and SCM key-path argument, offline guide packaging, and preserve
pinned older-package harnesses through CLI command discovery. Windows ancestors
permit only read/traverse/control/synchronize and sibling creation; strict leaves
still reject every untrusted grant. Native rerun is required. D11 current asset
paths/mapping are reconciled without altering frozen identities or historical
review evidence. The fault namespace harness now requires dedicated-host root
without user remapping; its trust boundary and invocation change are explicit.

Native Windows follow-up: standard ProgramData grants include untrusted ancestor
metadata writes, so the service key moves to a flat protected system-volume leaf
rather than relaxing those rights. Standard TrustedInstaller-owned ancestors
are recognized as trusted OS servicing owners, with the same owner DACL authority;
private key/leaf grants are unchanged. Installer conversion inspects raw ACE
forms and refuses null/empty or unsupported/exposed established ACLs. The native
fixture checks refusal without changing dummy bytes or ACL state. Native rerun
and coverage remain required; this note does not claim those results yet.

The next native run passed all version-key lifecycle preflight tests and the
virtual-account service recovery journey. Its lean suite exposed a PowerShell
argument-tokenization error in the new coverage command and a test expecting
an unescaped Windows path inside quoted CLI output; both are corrected. The
new reload-fence regressions exercise initial auth rejection and auth/runtime
changes during resource preparation, asserting keyed desired/serving versions,
resource abort, unchanged snapshot and the original live HTTP response. They
and the legacy flag-entry key precedence regression pass under the race
detector. Rebuilt source maps use a separate dependency installation so their
paths match clean CI checkouts. The complete final-head gates remain required.

## Integration after #646 and Codecov follow-up

The integration keeps #646's snapshot-wide selector index and raw projection
revision together with #644's private posture-generation context. Generated
OpenAPI and Console assets are rebuilt from the combined sources. D11 preserves
both parents' historical records and appends a separate integration path mapping.
An independent reviewer found no lost selector behavior, version data-flow
regression or historical audit rewrite; focused race checks and 26 UI tests pass.

Combined affected Go packages pass lean/full tests with coverage; focused CLI
full-tag lifecycle tests, targeted race, lean/full vet and the full-tag Windows
cross-build pass. Both actual pre-fix/combined-binary probes pass, including the
unchanged-raw legacy-selector refusal, restart/rotation/rollback/downgrade matrix
and persisted credential absence check. D11 has zero errors and 22 unit tests
pass. Console typecheck/lint and all 931 tests pass; production coverage is
76.37% statements and 73.98% branches, preserving all existing frontend floors.

The #646 Codecov bot comment marked uncovered/partial lines despite reporting
95.57522% patch coverage. Its actual merged-commit Codecov status passes the
unchanged 90% target. Six additional regressions exercise both rate-limit states,
descriptor/reflection transcode previews, legacy coordinate omission and malformed,
missing or ambiguous retained selectors. The previously uncovered editor selector
statements execute twice each; the server-name fallback and retained-selector
refusal branches are exercised. Codecov now also receives the native Windows
profiles already required by the complete changed-Go gate; no file or threshold
exemption is introduced. Fresh integrated-head CI and direct Codecov/CodeQL
results remain the merge gate; the final PR and issue closure records bind the
actual head, coverage result and merge commit.
