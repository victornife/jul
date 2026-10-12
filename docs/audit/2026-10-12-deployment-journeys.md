# Deployment journeys after v2.2.0

This is the execution ledger for issue [#516](https://github.com/victornife/jul/issues/516).
The maintainer selected all seven ranked journeys on 2026-10-12, in the order
below. Pending entries are requirements, not successful results.

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
| 1 | Linux systemd, trusted chain, firewall, independent client, WASM | Partially verified | Published package/unit/WASM checks passed; actual trusted-chain/firewall/independent-client execution required |
| 2 | Windows service with pre-existing ACLs | Pending | Reviewed secure and permissive existing roots; effective service/ordinary-user access and managed recovery |
| 3 | Docker externally routed HTTPS and durable state | Pending | Frozen-source image, routed client, TLS/admin/healthcheck and restart/persistence |
| 4 | Windows service AF_UNIX HTTP upstream | Pending | Actual service identity, socket access, successful traffic and failure/recovery |
| 5 | Windows registry/native MIME lookup | Pending | Deployed package, registry lookup, explicit-table precedence, direct/precompressed responses |
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

## Journey 1 preparation and remaining execution

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

The current agent workspace has no running systemd, Docker or Windows runtime.
The maintainer selected an existing Linux lab, but the workspace has no
preconfigured SSH alias/agent or known runner connection to that host/client.
Actual connection names, TLS hostname/trust locations and approved network
path are required to execute the first journey. See the
[execution runbook](../deployment-journeys.md).

Do not advance closure on preparatory checks. If a released-package defect
appears, retain that failing result and verify any correction under its own
source/package identity. Published tags and archives remain unchanged.
