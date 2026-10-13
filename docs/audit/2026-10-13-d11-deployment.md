# D11 deployment consumers — 2026-10-13

This area depends on configuration PR #596. It adds **three supplemental review
surfaces** to the companion ledger: the deployment guide, #516 runbook and
original execution ledger. The original TSV and its **230 rows / 112 originally
unreviewed rows** remain unchanged; configuration has dispositioned 31 of those
112 and the remaining original areas are still pending at this checkpoint.
Supplemental rows do not replace an original row or change its denominator.

## Claims and corrections

Read the shipped systemd units, Windows provisioning/installer/service fixture,
Dockerfile and frozen-source network journey, package verifier, trust fixtures
and reconciliation code against the guides and retained JSON reports. The
per-row ledger names the implementation, tests, evidence, verdict and limits.

- Editable operation requires managed authority, initial adoption, provisioned
  admin access and actual writable config/history paths. File-owned external
  restart-bound edits wait on disk for an operator restart; they do not become
  an API-managed staged candidate.
- Atomic writes create new secret-bearing files at `0600` but preserve existing
  POSIX modes. An Apply does not tighten an already permissive file. Windows
  confidentiality depends on effective ACLs; the installer preserves inherited
  and pre-existing entries. Fresh-root provisioning and existing-root review are
  separate operations.
- History finalization is distinct from config persistence and live publication.
  History failure can produce advisory degradation after a successful apply;
  it does not undo that live result. Existing readable history can remain
  available in a read-only deployment. Retention uses `history_keep`, not
  `history_dir`. File access-log rotation uses size and backup count, not age.
- The hardened systemd interpreter, nonroot Docker image, explicit trust and
  Windows service identities are substantiated by the original bounded journeys,
  not by a structural TOML check or a fresh timestamp.

## Reused execution and local checks

#516 completed through approved merged #575,
`39b93efbe43e7dc89fcb36350b9761eb218dee53`. Its original raw reports stay
unchanged. [Run38315192960](https://github.com/victornife/jul/actions/runs/38315192960)
executed seven jobs in order at harness `2c196095531890116bafaedf7794269234f9f24b`,
actual checkout `6ab52a4ff14b84da47fbfa6c5cd064864cd5400a`. Final review head
`6d2734264ca2339aee5f1903b3e0f3b891a368d9` has its separate
[approved final-head run](https://github.com/victornife/jul/actions/runs/38318455489).
These identities are not reassigned to this documentation PR.

The new local reconciliation verifies all **11 retained report hashes**, the
seven success conclusions and predecessor-completion ordering, immutable package
source and both `new_native_execution=false` flags. It does not download or run
the seven journeys again. The native archive evidence remains the original
six-host/twelve-profile release run38185464698; macOS accounts for four profiles.
Published source remains `b12e051139f652b400cff37d9f063f5637a7d550`.

Linux amd64 / Go1.27.2 focused `console wasmplugins stream importer` owner tests
pass for managed raw validation/restart/restoration and terminal history
degradation; atomic-file positive/failure/existing-mode cases pass. Four local
private-chain fixtures verify success, wrong CA, wrong name and missing
intermediate. Docs/regression/semantic/generated checks and the companion
inventory check run for this area. No Go production statement changed; its
incremental coverage requirement is not applicable. Existing Go/Codecov gates
remain. NGINX migration is not applicable: no translation/runtime contract changes.

[Local evidence](evidence/2026-10-13-d11/) and the
[original manifest](evidence/2026-10-12-deployment/manifest.json) retain the
actual sources and commands. Controlled disposable CI, private PKI, a client
namespace sharing the runner kernel and synthetic Windows ACL/registry fixtures
do not certify private production hosts, Internet/cloud firewalls, enterprise
permissions or public-CA issuance. No long-duration stability or maturity claim
is added. The original dated execution ledger is historical evidence, not current
semantic certification; #62/#516 and the active audit register own its completed
integration status. The maintainer/assigned-reviewer D11 spot-check is still pending.
