# Console coverage hardening — 2026-10-13 (#517)

The selected implementation is in [PR #592](https://github.com/victornife/jul/pull/592).
Initial main was `b2ae397bf43f14eb8e5517b45973c6dd74eafd51`; the branch was
rebased onto #591's merge `616f0ca0ec7ef3f7568ec8b11d0fc1c783df14bc`.
Local checks and independent probes passed on the recorded implementation
source below. Hosted final-head results, approval and integration are separate;
this record does not claim merged completion or security certification.

## Contract and baseline

Frozen pnpm 11.8.0 install uses the existing lockfile, Vitest 4.1.11 and V8
provider. Node 24.19.0 meets the repository's >=22 requirement. The first
bootstrap install used the available pnpm 11.25.0; installation and coverage
were repeated with the selected 11.8.0 version without changing dependencies.
The full baseline suite passed 842 tests in 75 files. The committed
[baseline summary](evidence/2026-10-13-console/baseline-summary.json) includes
all production files in Vitest's existing include set, including unimported
production sources.

| Measurement | Statements | Branches | Functions | Lines |
| --- | ---: | ---: | ---: | ---: |
| Global | 71.10 | 68.63 | 61.16 | 73.24 |
| Config area | 74.74 | 69.90 | 65.51 | 77.44 |
| ConfigPanel.tsx | 75.29 | 71.58 | 68.49 | 77.35 |
| configMutationMachine.ts | 89.47 | 95.00 | 75.00 | 88.88 |
| HistoryPanel.tsx / history area | 81.81 | 82.66 | 70.96 | 83.82 |
| API client.ts | 72.29 | 63.03 | 73.50 | 76.37 |

Selected policy: aggregate config/client >=80% statements and >=70% branches;
history >=80% statements. Additional exact-file >=80% statement floors for
ConfigPanel.tsx and configMutationMachine.ts are this tranche's stronger
hardening policy, not an original #517 requirement. Vitest 4.1.11's glob
thresholds aggregate matching files; exact-file entries prevent helper coverage
from masking the core apply flow. Global statements now enforce the existing
CI 58% floor locally too; branches/functions/lines remain 45/40/50. No broader
global ratchet is selected.

Coverage policy is consumed by Vitest. The report companion requires nonempty
production matches and both summary/raw entries for every target file; it
retains counts and all four dimensions by file, area and global. All production
includes and legitimate exclusions are preserved. No ignore directives, path
moves or excluded hard files are introduced. `make console-check` replaces its
plain test invocation with `test:coverage`; the fast `test` command remains.
CI retains its prior 58% check and preserves failures through `pipefail`.

## Review and remaining evidence

The existing reducer/hook, Console v2 writes, managed terminal-record and
permission tests are reused. New tests exercise real API/client state and mock
only the network; the editor-only rendering seam is retained for UI fixtures.
The complete suite passes **896 tests in 78 files**, plus six report-contract
fixtures. The final measurements and every measured production file are in
[final-summary.json](evidence/2026-10-13-console/final-summary.json).

| Measurement | Statements | Branches | Functions | Lines |
| --- | ---: | ---: | ---: | ---: |
| Global | 73.51 | 70.15 | 62.90 | 75.47 |
| Config area | 85.05 | 74.59 | 82.75 | 87.78 |
| ConfigPanel.tsx | 81.58 | 73.05 | 75.34 | 84.22 |
| configMutationMachine.ts | 89.47 | 95.00 | 75.00 | 88.88 |
| HistoryPanel.tsx / history area | 90.90 | 90.00 | 90.32 | 93.38 |
| API client.ts | 82.81 | 76.36 | 77.77 | 85.25 |

All seven independent path/metric probes passed at local implementation head
`035706153a7ec2cd8aec6d080699466fb558acf9`, tree
`73b09a33193bb71bb1c50d59699903f4892f45e3`; pnpm 11.8.0, Node 24.19.0 and
policy digest are retained in [source.json](evidence/2026-10-13-console/probes/source.json).
Each probe raises only its intended metric above the measured value. All 896
ordinary tests pass, exactly that named Vitest gate fails, and the command
returns 1. All other configured floors are unchanged and pass; complete-suite
global counts are unchanged in every probe. The original policy is restored
and the full unmodified gate returns 0. See [results.json](evidence/2026-10-13-console/probes/results.json)
and its diagnostic logs; before/restored logs retain full successful runs.

`make console-check` passes typecheck/lint, the six report fixtures and complete
coverage suite. The production build passes and tracked embedded assets are
unchanged. `make generated-check`, documentation checks and semantic-drift
regressions pass. No Go production statements change. Initial lint/type fixture
issues were corrected, not bypassed. Final-head hosted evidence is recorded on
the PR when available rather than projected from an earlier head.

New high-risk regressions cover credential replace/clear, 401/403 broadcasts,
malformed/empty responses, transport failure without mutation replay, raw
preview byte/base identity, unsafe discard, exact-ID lookup, protected candidate
errors, raw conflict/reload, repeated explicit admin confirmation, dirty reset
and canceled confirmation, staged discard failure/retry, known grant revocation,
server 403 rollback, selected snapshot viewing, double-submit prevention and
successful state refresh. GlobalSettingsEditor tests cover sparse changes,
server validation details, permission denial and fresh preview after failure.
Existing reducer/hook/managed-ledger tests retain generation, stale response,
terminal provenance, timeout, stage, recovery and rollback contracts.

Residual uncovered production branches are visible in the raw/HTML CI reports:
config raw-handoff assessment failures, some stage/admin retry combinations and
legacy poll fallbacks; client auxiliary read/export/upload and SSE parse/retry
variants; history supplemental provenance expiry and some metadata fallbacks.
Neither these numeric floors nor the test matrix proves every interaction.

The #514 risk-focused review and published advisories remain completed, with
their original limits. Browser UI grants are hints; the backend authorizes
requests. Existing unknown-identity fail-open UI behavior is an explicit
product boundary, not a promise that the browser replaces server enforcement.
No runtime/Console behavior changes are selected. Go changed-production
statement coverage is not applicable; unchanged Go/Codecov gates still apply.
NGINX migration impact: not applicable to test and coverage tooling.

#515's Console ledger review can reuse this bounded packet. Approval, merge
and issue closure remain separate.
