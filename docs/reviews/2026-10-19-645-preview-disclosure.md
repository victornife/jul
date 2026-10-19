# #645 — configuration comparison disclosure

Base: `9f72938ce71392c6394dca79bae70704625ce758` (current main verified via GitHub).
Branch: `fix/645-preview-confidentiality`. Date: 2026-10-19.
This record accompanies a security correction for review; no release, merge,
sharing-design acceptance or GA claim is made. Final PR head and CI are recorded
in the PR acceptance comment so this document does not need a self-referential SHA.

## Reproduction and response boundary

`TestPreviewDisclosureOriginalOracle` was run on unmodified main through the
actual HTTP mux and RBAC authentication, with a separate named synthetic writer
credential holding only `config:write`. A correct and an incorrect guess used
one unchanged baseline. Both tests failed the confidentiality policy:

| Target | Correct guess (HTTP 200) | Incorrect guess (HTTP 200) |
| --- | --- | --- |
| Literal admin token | `No structural changes detected…` | 1 modification, `Rotate admin bearer token`, restart warning |
| Opaque plugin credential map | `No structural changes detected…` | 2 modifications: `Change plugin config` plus `lifecycle plugins.*.config.*` |

After the correction both restricted responses are identical HTTP 403 bodies
requiring `config:raw`. Neither parses the candidate nor reads the baseline.
Privileged negative controls still return the original distinct 200 responses,
without adding plaintext secrets. Returning an empty diff to everyone would fail
the controls. The unmodified-main regression failed with the above outputs; this
is an executed negative control, not a claim inferred from source.

The guarantee is **response-level non-disclosure on configuration comparison
surfaces to a caller without `config:raw`**. No hidden-field comparison, version,
lifecycle flag, validation error, count or plan artifact is produced for that
caller. This deliberately uses the issue's allowed stronger-permission option,
not an opaque partial comparison or a label claiming unexamined values changed.
General timing hardening is outside this correction.

## Decision and alternatives

1. Candidate/operation-driven opaque rows would preserve useful writer-only
   previews, but current envelopes carry complete effective lifecycle assessments
   and unkeyed canonical raw-config `base_version` values. Even public-only typed
   operations expose that fingerprint (`patch_http.go`, `server.go`). Removing
   rows leaves equality in flags/fallbacks/hashes. A safe partial mode needs a
   separate disclosure/CAS contract, intent model, compatible client behavior and
   version migration; it is not a local masking change.
2. **Selected:** retain detailed previews and require raw-read authority in
   addition to the existing action permission. This is a small server-owned gate
   before all candidate/baseline work. Public edits remain accurately diffed for
   authorized operators; restricted users receive an explicit permission failure.
   The compatibility cost is loss of writer-only/rollback-only/adoption-only
   preview workflows. Request administrator review or an explicit custom-role
   raw grant; predefined roles are not widened.
3. Dropping hidden changes from internal lifecycle/no-op comparison is rejected:
   it could suppress rotation, permit unsafe apply or falsify restart behavior.

`RouteSpec.RequiresConfigRaw` is the authority. `routes.go` places the shared
check *inside* ordinary action authentication and authorization. It uses the
immutable request `rbac.Identity.Has`, including global/resource wildcards,
without role-name inference, client flags, provider rereads or global mode.
New capability/OpenAPI boolean metadata explicitly means an additional required
grant, preserving existing any-of permission arrays. Successful DTOs are unchanged.
ADRs 0013/0014 retain one server implementation and proportional security work;
ADR 0019 receives a comparison-disclosure amendment.

## Surface and permission inventory

| Surface (all mounted method variants tested) | Before | After |
| --- | --- | --- |
| Legacy POST `/api/config/diff` | write | write AND raw |
| Legacy POST `/api/config/patch` | write | write AND raw |
| Legacy POST `/api/config/patch/preview` | write | write AND raw |
| v1 POST `/api/v1/config/plan` | write | write AND raw |
| v1 POST `/api/v1/config/patch` | write | write AND raw |
| Legacy POST `/api/config/preview` | raw | raw (unchanged) |
| Legacy POST `/api/config/patch/candidate` | raw | raw (unchanged) |
| Legacy/v1 GET history `{id}/diff` | history:rollback | history:rollback AND raw |
| Legacy GET/POST and v1 POST adoption preview | config:adopt | config:adopt AND raw |
| Plugin editor | shared plugin_set preview | same gates as typed preview |
| Remote CLI plan/diff, rollback, adoption | v1 shared previews | now requires raw plus action; direct mutation API permissions unchanged |
| Wizard/generate, route test, standalone validation | existing action | unchanged: no server-held config comparison; wizard patch generation uses public listener identity |
| Local `jul fmt --diff` | local file access | unaffected: compares caller-accessible files |

`DiffConfigs` production consumers were searched repository-wide. Adoption's
composition root (`internal/app/serve.go`) returns the same trusted internal diff;
its mounted legacy/v1 read routes are gated. No server-side preview cache exists.
Apply-ledger records are mutation outcomes, not saved preview artifacts.

| Caller | Comparison result |
| --- | --- |
| write without raw, read-only, custom writer regardless of role label | 403 |
| raw without write | 403 on write-scoped previews; existing raw-only preview/candidate permission preserved |
| write plus raw | original detailed response |
| custom `config:*` or `*` | original detailed response via existing evaluator |
| unauthenticated/invalid credential | ordinary authentication denial |
| legacy shared token | existing wildcard authority preserved |
| request authenticated before a concurrent policy update | original pinned identity governs; later grants/revocations are not mixed into that request |

## Field and producer audit

For all rows below, the comparison gate denies before reading either raw or
effective configuration, so omission/preserve/clear/invalid combinations and
reference text cannot change restricted comparison output. Direct write behavior
is deliberately unchanged. `TestPreviewDisclosureFieldAndIntentInventory` parses
the actual supported field shapes and tests fixed requests against two baselines,
equal/unequal input, omission, empty, literal, single and embedded/multiple
references. Providers are never consulted by denied comparisons.

| Field/subtree | Actual read boundary / public metadata | Comparison producers and disposition |
| --- | --- | --- |
| `[admin].token` | literal raw TOML requires raw; auth identity is separate | `diffGlobalAdmin`, lifecycle, summary/restart warnings: gated; original HTTP reproduction |
| Named RBAC principal token | principal identity/role metadata may be visible; credential value is raw | `diffGlobalRBAC`, lifecycle token digest: gated; field regression |
| Entire plugin config map | `plugins.go` hides complete map, including keys/cardinality, without raw | map comparator plus `plugins.*.config.*` lifecycle fallback: gated; original reproduction; no keys/counts returned on denial |
| Health-check headers | upstream projection reports header count, not values (`projections.go`) | `diffHealthCheckFields`, lifecycle secret digest: gated; field regression |
| Request/forwarded header values | omitted from curated route projection; not every header name is secret | high-level/generic lifecycle comparison: gated; field regression; no blanket new schema redaction |
| Response-header values | curated route projection reports presence, not configured values | ordered response policy and lifecycle path: gated; field regression |
| Forward-auth | schema has URL, response-header *names*, timeout; no static credential-header map | URL may carry references; gated field regression; no invented header authority |
| OIDC client_secret | no plaintext secret in OIDC status; raw config authority | generic lifecycle fallback (`admin.oidc.client_secret`): gated; field regression |
| Consul/Kubernetes tokens | discovery projection publishes HasToken booleans only | specialised helper omits tokens but lifecycle fallback still compares them: gated; both fields tested; misleading helper comment corrected |
| Generic referenced string fields | references are legal in every schema string; projection authorities differ | entire comparison envelope gated; generic-string and embedded-reference regression; no effective value is newly exported |
| TLS/key file paths and asset-dependent transitions | path visibility varies by curated projection; loaded private-key bytes are not raw TOML fields | whole lifecycle assessment gated; no change to file resolution or key loading; no new key-byte read endpoint |
| Durable audit sink | schema has local audit file/rotation controls; no sink credential fields | absent credential feature, no importer work; all actual config comparisons gated |
| ACME EAB keys | no EAB configuration fields in current ACME schema | unsupported/absent; no fabricated fix. Removed the secrets introduction's stale EAB example |

Raw candidates remain full replacements with parser defaults. Typed operations
retain their existing omission/clear semantics. In particular `plugin_set`
explicit `preserve_config=true` retains the map, a replacement map replaces it,
empty/omitted non-preserving config clears it, and preserve-plus-map is invalid.
`TestPluginMetadataEditPreservesHiddenParameters` and plugin-set tests retain
execution evidence; this fix does not replace hidden values with placeholders.

## Console and stored results

`configDraftHandoff.ts` previously stored previewDiff, lifecycle, versions and
operations under a session key without binding them to the credential. A
privileged handoff could survive reauthentication as a restricted writer.
All comparison handoffs are now same-credential, memory-only. Historical storage
keys are purged rather than migrated. Plugin candidate input is not confused with
server-held value disclosure: neither supplied sensitive operations nor comparison
artifacts are newly persisted.

The PermissionProvider waits for a verified identity, clears non-identity query
caches on credential/grant transitions, and unmounts old component state before
mounting panels under new grants. ConfigPanel denies restricted preview access;
shared typed hooks and rollback confirmation explain the raw requirement.
Tests cover old stored payloads, cached raw data, same-token live permission loss,
unknown initial identity, supplied plugin secrets and credential switching.
Existing privileged source/edit/apply and accessible confirmation tests remain.

## #644 / #580 coordination and scope limits

#644 remains open; its current implementation is draft PR #647, not merged into
this base. It changes *effective* versions and explicitly leaves raw persisted/CAS
versions unchanged. #645 does not depend on that unmerged work and does not import
its branch. Raw fingerprints in a preview are protected here by denying the full
response without raw authority.

Other independently authorized reads, notably structured config/export/status,
already publish raw/effective versions. Those can still allow a caller who knows
all other canonical inputs to verify a low-entropy literal guess offline. This
is an existing exposure outside the comparison-output guarantee, and cannot be
called harmless opaque metadata. Global version confidentiality needs coordinated
follow-up beyond #644's resolved-value-only acceptance. Authorized real mutations
may also reveal outcomes through runtime effects. This fix does not claim otherwise.

#640 is still a draft proposed contract, #580 is not accepted, and #641 remains
blocked. This correction neither implements sharing successors nor grants their
acceptance. NGINX migration: not applicable; imported directives and traffic
behavior are unchanged by Jul admin-preview authorization.

## Acceptance and verification ledger

| Requirement | Implementation / evidence | Result |
| --- | --- | --- |
| Original admin/plugin oracle | failing unmodified-main HTTP regressions; privileged negative control | reproduced; fixed through denial |
| All comparison surfaces and indirect fields | independent endpoint inventory, whole DTO comparison, deny-before-read callbacks | covered |
| Field/subtree and intent inventory | table above; parsed field fixtures and bounded combinations | covered by pre-read denial; unsupported fields recorded |
| Permissions and wildcard/snapshot semantics | permission matrix and pinned-identity tests | covered |
| Privileged useful public/private changes | original diff implementation unchanged; negative control, existing full suites | retained |
| Internal apply, CAS, validation, lifecycle, preserve/clear | unchanged executor; affected admin suites, plugin/batch/apply tests | verification recorded below |
| Privileged cache reuse / sensitive storage | dedicated Console differential/state/storage tests | covered locally |
| Schema/client compatibility | additive boolean metadata; generated OpenAPI; existing success DTOs | covered locally |
| Documentation/security/compatibility | authoritative guides, ADR 0019 amendment, changelog, source-comment correction | updated |
| README, roadmap, maturity authorities | no claims of writer-only preview or fixed release in these authorities | reviewed; no change |
| Final-head CI/browser and change coverage | exact commands and final PR evidence below | pending final verification |

Full response comparison normalizes only the independently sampled v1 request ID,
and verifies its header/body correlation. It never normalizes versions, hashes,
lifecycle outputs, errors, flags, counts or any defect-bearing field.

### Commands and measured results

Toolchain: Go 1.27.2 linux/amd64; repository full tags are
`brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf`.
Local commands use the existing installed matching Go/Node tools and repository
scripts. Results are updated below after final validation; no presumed CI green.

- `go test ./internal/admin ./internal/apicontract ./internal/adminapi ./internal/rbac -coverprofile=…` — passed (lean).
- Same affected packages under the full tag set above — passed. Admin package
  coverage: 85.7% lean / 85.9% full; existing security-package floors retained.
- `go test -race ./internal/admin -run 'TestPreviewDisclosure|TestAuthSnapshot|TestTokenRotationAtomicOverAdminHTTP' -count=1` — passed.
- `scripts/security-gates.sh` — passed lean/full negative matrices and full
  package floors: plugins 90.9%, RBAC 89.2%, WAF 83.7%.
- `git diff --unified=0 BASE -- '*.go' | python3 scripts/strip_moved_lines.py`,
  then `scripts/check-added-go-coverage.py` with lean/full profiles — **24/24
  changed statements covered (100%)**, no exclusions/threshold changes.
- `go vet ./...` and `go vet -tags '<full tags>' ./...` — passed.
- Console `tsc --noEmit`, `eslint . --cache`, `vitest run --coverage`, coverage
  contract script and `vite build` — passed. **938 tests, 80 files**; statements
  76.41%, branches 73.50%, functions 65.60%, lines 78.55%; all seven existing
  coverage-contract targets and global/area/exact-file floors passed.
- Browser smoke regressions were added for stored-result denial, live grant loss
  and initial-401 recovery. Local execution was attempted, but Chromium was
  absent. `playwright install chromium` failed with truncated/non-ZIP downloads;
  browser assertions have **not** been executed locally. CI must run them.
- `make generated-check` — passed; OpenAPI was generated from sources, not edited.
- `scripts/docs-check.py` — 2,385 passed, 0 failed. Documentation and semantic-drift
  test scripts passed (including 28 semantic-drift tests).
- `go test ./...` was attempted. It failed only in unchanged handler Unix-socket
  tests (`socket: operation not permitted`) and observability process metrics
  (`RSSBytes`, `OpenFDs`, `CPUCores` unavailable). Both failure kinds were separately
  reproduced on unchanged runtime source (docs-only head `ce929b49`; no Go diff
  against the recorded main base). These are environment limits, not a passing
  aggregate suite; final CI remains necessary. No tests were skipped or weakened.
- No local golangci-lint result is claimed; final required CI owns that gate.
- Console initial-route gzip was 240,340 bytes (below the existing 250 KiB
  budget); assets were rebuilt and included to preserve the source/embedded drift
  guard. Cosmetic sourcemap differences are generated, not manually edited.

### Independent review and corrections

A separate AI reviewer independently inventoried routes/fields, then reviewed
this implementation. It found no server-side comparison bypass. It identified
three material frontend/documentation findings, all corrected before publication:

1. A fail-closed PermissionProvider initially also hid AuthGate on 401. The actual
   root now uses AdminSession with credential recovery outside the protected
   panel subtree. Initial and later 401 recovery tests use that real composition.
2. Late async previews/raw-source reads could repopulate cleared handoffs after
   an identity change. Responses and every direct editor handoff now check a
   captured identity generation; the shared hook also guards unmount/origin.
   Deferred typed/raw regressions prove old data is discarded after credential
   replacement and same-token permission-generation changes.
3. Existing route-authoring documentation still described writer-only diff review
   and stored assessments. Those paragraphs and misleading helper comments were
   corrected rather than merely appending an amendment.

The final separate re-review found no remaining concrete source blocker and
independently ran all 9 disclosure tests successfully. Reviewer independence
is a separate AI context, not a human maintainer approval.
Final-head required CI and browser outcomes must be posted with exact URLs before
claiming the complete acceptance scope. The maintainer subsequently directed
`Closes #645` on PR #648 while CI runs; the issue closes on merge, not before.

### Initial hosted CI correction

The first hosted `semantic-noop-console` job passed all 928 tests and coverage
floors, then detected two generated sourcemaps containing paths through the
shared local dependency directory. A local materialized dependency tree now
produces the same portable relative paths as a clean CI checkout. The maps were
regenerated with Vite; no generated content was hand-edited and no drift guard
was weakened. The served JavaScript hashes and behavior did not change. A second
build is checked for zero generated-asset diff. Original job evidence:
https://github.com/victornife/jul/actions/runs/39178364274/job/117825654547

### Hosted coverage and inventory follow-up

The initial Codecov patch result was 80.95%, with missing or partial paths in
Console identity transitions, preview handoffs, and editor producers. Ten added
regressions exercise denial before requests, permission-check failure/retry,
replacement credentials, same-identity TLS/WAF/cache success, delayed revoked
responses, editor unmount, and unavailable storage. The complete Console suite
passes 938 tests and all existing coverage floors. Applying the repository's
changed-frontend-statement selector to the final local coverage profile measures
126/128 changed statements (98.44%); the hosted Codecov result remains a separate
required gate. No coverage configuration or production source was changed for
this follow-up. The separate reviewer independently passed all 19 disclosure
tests and found no material false-positive or blocker.

Hosted docs-check also found four old content-hashed paths in D11-104 after the
embedded Console rebuild. Its current paths and original-to-current mapping now
follow PR648, with an appended rebuild record. The historical frozen inventory,
prior mappings, denominator and review verdict are retained. Both the inventory
check (zero errors) and its 22 regression tests pass; docs-check again passes
2,385 checks with zero failures. Initial failed-job evidence:
https://github.com/victornife/jul/actions/runs/39178903879/job/117827376748
