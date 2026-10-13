# Post-release corrective work — #594 / #595

The maintainer explicitly selected both repairs after the D11 review reproduced
them. This corrective PR depends on the coverage/test-only PR #592; it changes
Console behavior and the standalone TCP load generator separately from #515's
documentation certification. No release package, maturity decision or soak
obligation changes.

## Behavior and regression evidence

- **#594:** one echo round uses `io.ReadFull` and compares the complete payload.
  Fragmented valid replies succeed; premature EOF, expired read deadlines,
  failed/short writes and wrong contents remain failures. Deterministic
  `net.Pipe` writes guarantee separate reads without sleeps. Successful echo
  rounds and failed connection attempts have separate labels; the connection
  success rate uses `(connections - failed_connections) / connections`, rather
  than dividing rounds by connections. A connection attempt ending at the run
  deadline without an error is counted as error-free; echo rounds remain the
  actual completed-message counter.
- **#595:** already-open rollback, raw/structured live apply and raw/structured
  restart-staging dialogs disable confirmation after the required known grant
  is revoked, display the grant and check it again in the confirmation handler.
  Grant recovery requires another explicit click. Cancellation remains available
  unless an operation is pending; permission changes retain submitted outcomes.
  Unknown identity keeps the established fail-open UI policy. Server
  authorization remains authoritative and 403 outcomes stay failures.
- The new UI regressions fail against the original production components:
  five denied-confirmation cases fail, two control cases pass, exit 1. With the
  repair the complete suite passes **903 tests in 78 files**, plus the six
  coverage-report fixtures. Network requests use the real API client/state.
- A two-second loopback smoke uses the actual `-tags stream` Jul binary, four
  workers and a fragment-writing TCP backend. The retained output records
  completed echo rounds and zero failures. This bounded smoke is not #590's
  long-running campaign or proof of other L4 protocols.

## Coverage

The complete changed non-test Go diff contains only
`scripts/burn-in-stream-load.go`: **22/22 changed statements, 100%**. The scorer
uses the existing HR-07C block-overlap/duplicate-profile algorithm and
`strip_moved_lines.py`, with no package allowlist. Missing changed production
files fail closed. Standalone ignored Go files need an explicit-file test
command; CI now executes it and includes the profile in the usual Codecov
upload. Normal repository/global/owner gates remain unchanged. The whole
standalone file is **86.7%**, distinct from the changed-statement score.

| Console target | Statements | Branches | Functions | Lines |
| --- | ---: | ---: | ---: | ---: |
| Global | 73.70% | 70.79% | 63.01% | 75.73% |
| Configuration aggregate | 86.86% | 79.18% | 84.48% | 90.07% |
| ConfigPanel | 84.06% | 78.43% | 78.08% | 87.34% |
| configMutationMachine | 89.47% | 95.00% | 75.00% | 88.88% |
| HistoryPanel / history | 90.00% | 89.17% | 90.32% | 93.61% |
| API client | 82.97% | 76.66% | 77.77% | 85.25% |

All #517 floors remain unchanged. Seven independent raised-floor probes pass
the ordinary tests and fail only the intended path/metric, exit 1; the restored
full gate passes. The original #592 evidence is preserved; these fresh probe
logs are stored separately. Defensive confirmation-handler early returns remain
uncovered because a disabled native button cannot dispatch its click handler;
the observable no-request invariant is directly tested. Coverage does not
establish security certification.

## Source, commands and limits

Measured source: `6eaebc8bd0478cc6565e74e169183950a861adb0`, tree
`0d3a12658d263941dbc5e5ecffe5df1a3bc34ca4`, based on main
`616f0ca0ec7ef3f7568ec8b11d0fc1c783df14bc` plus #592's equivalent local tree.
Linux amd64, Go 1.27.2, Node 24.19.0, frozen pnpm 11.8.0. The evidence-only
commit retains the measured source rather than relabeling earlier executions.

Local passes: frozen install; `make console-check`; normal Console production
build and regenerated embedded assets; seven coverage probes and restored gate;
explicit TCP-tool `go test -race -coverprofile`; tool `go vet`; stream-owner
race suite; five scorer fixtures; generated-check; docs-check (2088/0), docs
regressions and semantic-drift regressions. Shipped TOML values are unchanged.
Hosted corrective-head/platform/browser checks are pending at preparation and
must be recorded on the PR before integration. Earlier #592 CI certifies its
own source, not this repair. NGINX translation is not applicable: no gateway
routing, configuration or importer semantics change.

[Evidence and source/digest manifest](evidence/2026-10-13-repairs/) retain raw
failure/pass logs, per-file Console counts, full changed Go diff/profile,
deterministic test output and the bounded real-proxy smoke reproducer.
