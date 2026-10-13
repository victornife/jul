# D11 configuration review — 2026-10-13

Configuration is the first semantic area in #515's selected post-release pass.
This record accounts for **31 of the 112 original unreviewed rows**, preserving
all 230 historical ledger rows and their original reasons. The frozen inventory
is [original.json](2026-10-13-d11-original.json); one-to-one current dispositions
and tracked-file wildcard expansions are in [certification.json](2026-10-13-d11-certification.json).
Other areas remain pending here; this is not an umbrella completion claim.

## Source and review boundary

The review uses current main `616f0ca0ec7ef3f7568ec8b11d0fc1c783df14bc`, after
#591's bounded consumer cleanup. Published v2.2.0 remains immutable
`b12e051139f652b400cff37d9f063f5637a7d550`. All changes in this area correct
comments, guide claims and evidence; edited TOML parses to identical values.
Generated authorities and runtime code are unchanged.

The per-row boundary covers the documented defaults, build prerequisites,
configuration authority, validation, secret references, persistence/CAS,
reload/no-op/stage/discard/restoration, operation identity and example workload.
Implementation aggregate rows substantiate those claims; their explicit tracked
file inventories do not imply every-line code/security review. All original
owners/boundaries remain recorded even where the remaining area verdict is pending.

## Corrections and dispositions

- The resilience load command lacked `-fault`, so its default route mix did not
  exercise the advertised bounded pool. Add the existing flag and distinguish
  bounded HTTP/TCP from unexecuted gRPC/UDP/SSE and reload-spanning connections.
  A historical per-request 7.5KB estimate is not a guaranteed resource bound.
- The September `-current` configuration is a selected workload, not every
  subsequently merged capability. It needs the HTTPS admin URL and explicit
  RBAC/apply/L4/CORS/egress-denial phases for those claims.
- A five-minute Phase2A run is smoke, not all-feature or criterion-5 stability
  evidence. Worker count does not determine a fixed rate-limit success ratio;
  the standard backend's API contract matters more than an unsupported Python
  50-worker performance assertion.
- `server.everything.toml` is a broad synthetic demonstration requiring operator
  fixtures/services; merely loading it cannot certify every feature.
- Reload's opening connection-survival promise is bounded by the documented
  protocol/resource-retirement contract. Authority changes require staging in
  managed mode or an external edit/restart in file-owned mode. The #106 leaf
  counts remain explicitly historical; current counts belong to the generator.
- `examples/migrate/imported.toml` is an explicitly superseded saved legacy
  candidate, with the current assess/convert workflow as its replacement. It is
  accounted for but **not counted as current importer semantic certification**.
  Fresh assessment and conversion of the current nginx.conf both exit 3 for
  expected manual action; arbitrary Host headers are still blocking, the down
  server is omitted and regex case semantics remain approximate. #523's exact
  supported WebSocket trio is not broadened into arbitrary header support.
- [#594](https://github.com/victornife/jul/issues/594) separately owns the real
  TCP load-generator short-read defect. A deterministic net.Pipe reproducer
  obtains 5 then 14 valid bytes; the existing one-read harness labels the first
  fragment failed. This is filed, not repaired or accepted as correct.

The unchanged rows record their actual bounded contracts, named owner tests and
prerequisites rather than generic reviewed wording. Synthetic credentials,
fixture PKI, external issuers/backends and CWD-relative resources remain explicit
limits. Native service/package evidence is reconciled in the deployment area.

## Executed verification

Go 1.27.2 linux/amd64, full build tags
`brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf`:

- `make config-check`: every shipped root/Jul example config loads; existing
  skip-static-roots and placeholder-secret profile is retained. This proves
  load/preflight scope, not live external services or optional-path execution.
- Config/lifecycle/configcontract/adminapi/apicontract suites pass, including
  closed-world/classification, defaults/bounds, generated freshness and wire
  contract cases.
- Focused app/server/admin coordinator, lifecycle, stage/CAS/restoration and
  terminal-ledger suites pass.
- Focused real handler/observability HTTP/gRPC/uWSGI/admission/tracing/metric
  contract cases pass. The broader feature command passes auth/cache/middleware/
  upstream/stream/transcode/plugins/tracing/affinity/importer/RBAC/WAF but fails
  handler AF_UNIX creation and observability proc-derived resource tests on this
  host. Those environment failures are retained, not reported as passes; hosted
  owner lanes supply the required platform evidence.
- Lean `jul check` accepts the ACME example's structural/stateless contract;
  issuance/startup requires the acme build and real site prerequisites.
- Fresh example NGINX assess and convert retain exit 3 and exact reports.
- Edited TOML before/after comparisons prove values unchanged.

[Evidence directory](evidence/2026-10-13-d11/) retains successful focused outputs,
the broader failures, expected migration blockers and filed-defect reproducers.
The inventory checker rejects a changed historical denominator, missing/duplicate
rows, unexplained wildcard/path changes, missing owners/evidence and nonexistent
implementation/tests. Its `--complete` mode additionally rejects pending rows;
that mode will pass only after all remaining areas are reconciled.

Mandatory docs/checker/semantic/generated and config validation are recorded on
this area's PR. Final-head hosted checks and area PR identity are separate from
these local commands. The maintainer/assigned >=10% stratified reviewer spot-check
remains pending for final #515 closure. Agent self-checks cannot replace it.
No maturity, GA/soak decision, release package or tag changes; #590 remains separate.
