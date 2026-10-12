# Deployment journeys after v2.2.0

This is the execution ledger for issue [#516](https://github.com/victornife/jul/issues/516).
The maintainer selected all seven ranked journeys on 2026-10-12, in the order
below. All seven completed in the selected CI scope; final PR integration is
tracked separately from the execution identities below.

The maintainer subsequently selected **GitHub CI** as the execution environment.
The remaining journeys use controlled production-like conditions on disposable
native runners: a private root/intermediate/leaf chain, separate client network
namespace and routed packets/firewall rules, pre-existing Windows ACL fixtures,
actual service identity, registry lookup and durable Docker volumes. This
scope does not certify an enterprise/public CA deployment, a separate physical
client host, Internet ingress or a production cloud firewall. The CI sequence
requires each journey to pass before the next runs.

## Source and evidence identities

- Published package source: `b12e051139f652b400cff37d9f063f5637a7d550`, tag `v2.2.0`.
- Annotated tag object: `9115edc5961b92526b9ec76b2c752bc146ce3523`.
- Preparation baseline: main `6ca8762351036efd7bddd17d21869e5c145e58a1`.
- Harness changes are reviewed separately from the unchanged published package.
  Reports record both identities and the archive/binary/module hashes.
- No new long-duration soak, overall coverage target, feature promotion,
  platform expansion or systemd hardening change is selected.

## Sequential verdicts

| Order | Journey | Verdict | Evidence or remaining requirement |
| --- | --- | --- | --- |
| 1 | Linux systemd, trusted chain, firewall, independent client, WASM | Passed in approved CI lab | Published package, both hardened units, private three-certificate chain, routed client namespace and firewall denial/recovery |
| 2 | Windows service with pre-existing ACLs | Passed in approved CI lab | Secure/permissive pre-installation fixtures, effective ordinary-user denial/readability, preserved owners/ACEs and actual service recovery |
| 3 | Docker externally routed HTTPS and durable state | Passed in approved CI lab | Frozen-source full nonroot image, namespace/bridge routing, TLS/admin/healthcheck and container-recreation persistence |
| 4 | Windows service AF_UNIX HTTP upstream | Passed in approved CI lab | Actual virtual service account, normal response/Host, backend failure without path leakage and recovery |
| 5 | Windows registry/native MIME lookup | Passed in approved CI lab | All 28 native/explicit direct/gzip checks and original registry value/kind restoration |
| 6 | macOS Intel and Apple Silicon archive first run | Passed for the original archive/first-run scope | Native release verification on darwin/amd64 and darwin/arm64, both profiles |
| 7 | All OS/architecture/profile archive downloads | Passed for the original package scope | All six native hosts/twelve profiles, including checksums/attestations/startup/upgrade/rollback |

Rows 6 and 7 reuse actual completed evidence from the release; they do not
claim a new execution. Their later position in the maintainer's sequence is
documentation reconciliation after the remaining journeys.

## Completed native package evidence

The [official release build](https://github.com/victornife/jul/actions/runs/38184479322)
passed the immutable-tag preflight, unchanged build/test and short smoke gates,
and twelve builds. The [native actual-download run](https://github.com/victornife/jul/actions/runs/38185464698)
passed these six jobs, each checking lean and full:

| Native host | Job |
| --- | --- |
| Linux amd64 | [114606716221](https://github.com/victornife/jul/actions/runs/38185464698/job/114606716221) |
| Linux arm64 | [114606716366](https://github.com/victornife/jul/actions/runs/38185464698/job/114606716366) |
| Windows amd64 | [114606716323](https://github.com/victornife/jul/actions/runs/38185464698/job/114606716323) |
| Windows arm64 | [114606716346](https://github.com/victornife/jul/actions/runs/38185464698/job/114606716346) |
| macOS Intel | [114606716364](https://github.com/victornife/jul/actions/runs/38185464698/job/114606716364) |
| macOS Apple Silicon | [114606716336](https://github.com/victornife/jul/actions/runs/38185464698/job/114606716336) |

The logs explicitly report download/startup/upgrade/rollback verification for
both profiles. The [attached package-verification.json](https://github.com/victornife/jul/releases/download/v2.2.0/package-verification.json)
records archive hashes, package identity, provenance/SPDX and native results.
Workflow definition `9b73d7cb` checked out and verified package source
`b12e0511`; the first attempt's draft-token failure was corrected without
changing the tag or archives. See [published verification](../release.md#published-v220-verification).

These checks used bounded disposable native environments. They do not certify
real service identities, pre-existing Windows ACLs, external routing, a
production certificate deployment or long-duration feature stability.

## Historical journey 1 preparation and prefix runs

The previous systemd journey built with only `console` and therefore did not
execute a WASM guest under either shipped unit. The prepared extension builds
with `console,wasmplugins`, or accepts the verified published full archive,
and checks both units' actual `MemoryDenyWriteExecute=yes` property, running
service `engine=interpreter`, pinned guest digest, and a guest-generated
response header. Existing adoption/Apply/history/rollback/restart/read-only
checks remain. The bounded guest probe uses a five-second timeout to exercise
interpretation without turning this journey into a CPU benchmark.

The [hosted systemd run](https://github.com/victornife/jul/actions/runs/38306777753)
and [job](https://github.com/victornife/jul/actions/runs/38306777753/job/115002637828)
passed both source-build and actual v2.2.0 full-package execution on Linux
amd64 with systemd 255. Both units kept the hardening setting, reported the
interpreter, executed the pinned guest and passed recovery/read-only checks.
No product defect was established by this bounded run.

The reviewed preparation head was `7ea42206556f0f95c28b7eeea29ad236a588ac91`;
the workflow checked out CI merge `15091fd09c24efc5e16ff53602b2847a362d5229`.
That harness identity is distinct from unchanged published package source
`b12e051139f652b400cff37d9f063f5637a7d550`. The actual full archive SHA-256 was
`288ffa323d92d8abb586d364954eaaaa627026304471e33a3ec1174339287862`, and the
serving guest SHA-256 was
`1641d893c36149ef76bb6430f21a5b9dcb1efc77e539cea86a3fb651e03b4882`.

Artifact `11772628066` has SHA-256
`bc645a0b5f29f407c3afa5a11ec6b7c96dc49e81da9f93212a3d251d777918a4`;
the downloaded bytes matched it. Unmodified [source report](evidence/2026-10-12-systemd/systemd-source.json)
and [package report](evidence/2026-10-12-systemd/systemd-package.json) are
retained in the repository so artifact expiry does not remove the verdicts.
Both explicitly label this automation **disposable** and leave trusted-chain,
independent-client and firewall requirements **pending**. They do not certify
the later documentation commit or the unexecuted lab/network requirements.

The initial existing-host option had no connected SSH/runner/client, so no
private-host execution occurred. The later explicit GitHub-CI selection
supersedes that prerequisite. The controlled network extension is prepared;
its [first controlled CI run](https://github.com/victornife/jul/actions/runs/38309491556)
and [job](https://github.com/victornife/jul/actions/runs/38309491556/job/115011460608)
passed on reviewed head `9310bc41749f9c581330e44faef65b08778eb66f`.
The private chain, wrong-CA/hostname/missing-intermediate rejection, routed
client, scoped firewall denial/recovery and authenticated read-only admin
checks executed successfully against the unchanged published full package.
The raw report retains the workflow checkout and package identities. The
[two-journey run](https://github.com/victornife/jul/actions/runs/38311074090)
also passed [Windows ACL job115016900365](https://github.com/victornife/jul/actions/runs/38311074090/job/115016900365)
on reviewed head `d05be7f3406d849d430048054bd4690f14c4cb14`, against the
unchanged published Windows full package and its packaged installer. The
secure fixture refused ordinary-user config/history access while preserving
unrelated entries/data; the permissive fixture remained readable before and
after installation, as documented. Both used the actual virtual service
account and retained service availability/recovery. These prefix runs did not
promote later rows; the completed integrated run below supplies their evidence. See the
[execution runbook](../deployment-journeys.md).

Do not advance closure on preparatory checks. If a released-package defect
appears, retain that failing result and verify any correction under its own
source/package identity. Published tags and archives remain unchanged.

## Failed attempts and corrections

The [first integrated run](https://github.com/victornife/jul/actions/runs/38312478556)
on reviewed head `145fcd5d` passed journeys 1–3, including
[Docker job115022324941](https://github.com/victornife/jul/actions/runs/38312478556/job/115022324941).
[AF_UNIX job115023063549](https://github.com/victornife/jul/actions/runs/38312478556/job/115023063549)
stopped before proxy execution because the Python backend fixture could not
start on Windows. Rows 5–7 were correctly skipped by the dependency chain;
they are not recorded as passed. The fixture now uses Go's native `net.Listen`
Windows AF_UNIX implementation; the tested Jul binary stays unchanged. The
fixture's readiness identifies its PID/network and the report records its
binary hash. CPython's [socket header](https://github.com/python/cpython/blob/v3.13.0/Modules/socketmodule.h#L90-L94)
removes AF_UNIX without `sys/un.h`, and its socketserver Unix classes are
conditional. The agent workspace also denies AF_UNIX creation, so local
cross-build/vet are not relabeled as hosted functional evidence.

CodeQL reported two TLS-version alerts in the new trust-test fixture and one
response-splitting alert in the Python backend's raw Host echo. The test
contexts now explicitly require TLS 1.2+, and the replacement Go backend
escapes the echoed Host value. No query exclusion, suppression, gate reduction
or released-product vulnerability claim is introduced. Corrected hosted
execution and CodeQL must pass before closure.

The [second integrated run](https://github.com/victornife/jul/actions/runs/38314171746)
on reviewed head `b74e361a` passed journeys 1–4, including actual Windows
service AF_UNIX traffic, Host preservation and backend failure/recovery.
[MIME job115029015590](https://github.com/victornife/jul/actions/runs/38314171746/job/115029015590)
passed all 28 response checks, then failed restoring the pre-existing `.ts`
registry value through a read-only PowerShell registry handle. Rows 6–7 were
skipped. The fixture now opens an explicitly writable handle, preserves and
verifies the original value/kind, and disposes its handles. Successful traffic
alone does not complete the journey; its cleanup must pass too. This is a
verification-tooling correction, not a change to the published Jul package.

## Completed seven-job execution

[Run38315192960](https://github.com/victornife/jul/actions/runs/38315192960)
passed all seven jobs, in order, at reviewed harness head
`2c196095531890116bafaedf7794269234f9f24b`. Every report records the actual CI
checkout `6ab52a4ff14b84da47fbfa6c5cd064864cd5400a`; the published package and
frozen Docker source remain `b12e051139f652b400cff37d9f063f5637a7d550`.
The retained reports do not certify a later documentation commit or merge.

| Order | Successful job | Retained raw report |
| --- | --- | --- |
| 1 | [115030032193](https://github.com/victornife/jul/actions/runs/38315192960/job/115030032193) | [systemd network](evidence/2026-10-12-deployment/journey-1-systemd/systemd-network.json) |
| 2 | [115030281788](https://github.com/victornife/jul/actions/runs/38315192960/job/115030281788) | [Windows ACL](evidence/2026-10-12-deployment/journey-2-windows-acl/windows-acl.json) |
| 3 | [115030801294](https://github.com/victornife/jul/actions/runs/38315192960/job/115030801294) | [Docker network/recreation](evidence/2026-10-12-deployment/journey-3-docker/docker-network.json) |
| 4 | [115031344635](https://github.com/victornife/jul/actions/runs/38315192960/job/115031344635) | [Windows AF_UNIX](evidence/2026-10-12-deployment/journey-4-windows-unix/windows-unix.json) |
| 5 | [115031832752](https://github.com/victornife/jul/actions/runs/38315192960/job/115031832752) | [Windows MIME/restoration](evidence/2026-10-12-deployment/journey-5-windows-mime/windows-mime.json) |
| 6 | [115032227643](https://github.com/victornife/jul/actions/runs/38315192960/job/115032227643) | [Four macOS profiles](evidence/2026-10-12-deployment/journey-6-macos/macos-evidence.json) |
| 7 | [115032282899](https://github.com/victornife/jul/actions/runs/38315192960/job/115032282899) | [Twelve archive profiles](evidence/2026-10-12-deployment/journey-7-archives/archive-evidence.json) |

The [retention manifest](evidence/2026-10-12-deployment/manifest.json) records
all seven artifact ZIP digests, unmodified JSON file hashes, job identities and
start/completion times. Downloaded ZIP bytes matched GitHub's digests, every
report matched the checkout identity, and the next job started only after its
predecessor completed successfully. Package-verifier reports are retained beside
the systemd and Windows reports. Public evidence contains no tokens, private
keys or raw secret-bearing config readback.

The MIME result includes all 28 content/type/encoding/body checks and successful
registry restoration. Reconciliation retains the original native run38185464698
and report SHA-256, verifies the immutable tag/source and current asset digests,
and explicitly sets `new_native_execution: false`: four macOS profiles and
twelve total profiles are reconciled, not rerun on Linux. The private CA,
shared-kernel routing, synthetic ACLs and derived Docker image limits above
remain. No Jul runtime defect was established; all encountered fixture/CodeQL
findings were corrected and the sequence passed without gate suppression.

D12's selected GitHub-CI scope has executed evidence. #515/D11 semantic review,
#569's seven inherited HTTP/3 exceptions and post-GA soak are separate work.
[PR #575](https://github.com/victornife/jul/pull/575) owns final-head CI and
maintainer review; #516 closure follows integration. No broad repository audit
or private-production certification is implied.
