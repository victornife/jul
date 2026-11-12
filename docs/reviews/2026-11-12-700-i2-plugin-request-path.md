# ADR 0032 slice I2 — plugin request admission, allocation accounting and upload deadlines

- Tracking: #700 (programme #62). Contract: [ADR 0032](../adr/0032-aggregate-inspection-resource-governor.md).
- Starting point: `main` = `d8d0520b1b57ef8c0df08e8902b407856f2d47cd` (I5 #708 merge). I1 `a57afa4d`
  and the ADR comparison source `858590dc` are ancestors.
- Status: implementation slice I2 only. I3 (Coraza charging, evaluation slots, calibration), I4
  (response-subscription buffering, response-header gate) and I6 (A1–A18 qualification) remain
  open. This document does not claim A1–A18 protection.

## 1. Design note (frozen before implementation)

### 1.1 Call-flow trace

Composition, outermost first (`internal/app/factory.go`): global chain → router admit → server
plugins → location plugins → client-cert → preflight → auth → rate limit → WAF → plugin
`responsePoint` → action (proxy, static, cache, or a plugin terminal handler).

| Path | Source | I2 behaviour |
| --- | --- | --- |
| Middleware plugin | `plugins.(*Set).Middleware` | Acquires or reuses the request lease before preparation or guest execution. The owner closes it after `next.ServeHTTP` or `inv.flush` returns. |
| Terminal plugin | `plugins.(*Set).Handler` | Same as above, around `invoke` + `flush`. |
| Body reader (imports `read_request_body`) | `plugin.invoke` → `prepare` → `prepareRequestBody` → `readBounded` | Upload arrival happens before `acquire`; it is charged before each allocation and bounded by the deadline owner. |
| Non-body plugin | `plugin.invoke` → `call` | Holds only the lease and host-allocation charges. The body still streams. |
| Response callback | `responsePoint` → `responseLayer` → `invokeResponse` | Runs synchronously inside `next`, so it is covered by the outer lease. Only the guest replacement copy is charged. The layer's `buf` is I4. |
| Nested chain | Several plugins on one request | `AcquireLease` returns the context lease with `owner=false`. An unread prepared body is reused across the chain. |
| Background revalidation | `background.Detach` | Propagates no lease or deadline owner. A plugin there acquires its own lease. |

### 1.2 Lease ownership

- A single process governor lives in `app.Runtime.Inspection` (I1). It is injected into
  `plugins.Options.Inspection`, and `NewManager` refuses a nil governor unless `MetadataOnly` is set.
  `MetadataOnly` is used only by the offline `jul check` budget path, which never instantiates
  guests. There is no silent production bypass.
- The outermost plugin wrapper acquires the lease. Every plugin wrapper calls `AcquireLease(r.Context())`
  and installs the derived context on the request, so inner plugins, the WAF (I3), response callbacks
  and host functions all see the same lease. Only `owner=true` closes it, deferred until the downstream
  handler has returned.
- `Lease.Close` refunds every live charge. Release before close happens only where a test shows
  the represented allocation is no longer referenced.
- Routes with no plugin (and, until I3, no WAF) never call the governor.

### 1.3 Allocation map

| Allocation | Owner and charge point | Capacity charged | Transfer and aliases | Release |
| --- | --- | --- | --- | --- |
| Prepared request body | `readBounded`. One `Charge` per preparation, `Resize`d before each `make`. | `cap(buf)` | Retained by `preparedBody.data`, reused by later chain plugins (no copy), and by `inv.body` | Lease close (the downstream exchange may still replay it). On failure: drop `buf` after the read has returned, then `Release`. |
| Growth copy | Same charge, resized to `old+new` before `make`, then to `new` after the old reference is dropped | Both arrays during the copy | None | The shrink to `new` after `copy` |
| `write_response_body` (v1 and v2) | Per-invocation charge, resized before growth. The guest memory view is copied straight into the destination (no temporary). | `cap(respBody)` | `inv.respBody` | After `flush`, or when the invocation is abandoned |
| Fetch request body copy | Charged before the copy | `n` | Referenced by the transport's request body until `Close` (`RoundTrip` contract) | When `Do` has returned and every body reader the transport opened has been closed |
| Fetch response | Charged before the buffer is made. Declared length ≤ max: `CL+1`. Otherwise (unknown or over max): `max+1`. | `cap` | `inv.lastFetch` aliases the buffer; `fetch_read` copies into guest memory | When the next fetch starts (the old reference is dropped first), or when the invocation ends |
| Request state (`set_request_state`, ≤ 4096) | Charged before the copy | `n` | Passed to `subscribe` and to later response callbacks | An overwrite in the same invocation releases the previous charge. Otherwise held until lease close. |
| Response replacement (`resp_body_replace`) | Charged before the copy | `n` | `view.body`, written by `responseLayer.finish` | After `finish` writes it, when replaced again, or at lease close |

Bounded exclusions (documented, not governed): short strings from `readStr` and `v2str` (header
names and values, URIs, methods, KV keys); `kv_set` values, which are persistent and owned by the
KV quota rather than by the request; the `responseLayer.buf` subscription buffer (I4); guest linear
memory, compiled modules and idle pools (static and headroom terms).

**Growth algorithm.** The capacities are unchanged: an initial `min(32 KiB, CL+1)` when the declared
length is within the limit, otherwise `min(512, limit+1)`; doubling, capped at the declared length + 1
and then at `limit+1`. The charge is acquired with checked arithmetic (`Resize`) before every `make`.
The surviving full capacity stays charged. Shortening a slice is never refunded. A declared
`Content-Length > limit` is rejected before any charge, keeping the existing #699 `too_large`
outcome and `500`. At most `limit+1` bytes are read.

**Budget model check.** The runtime peaks are `Σ(prepared cap ≤ L+1)` plus one in-flight growth
copy, `≤ max(L+1)`; state 4096; response body growth `≤ 3×maxResp` (old + new while copying); and fetch
`max+1` plus a request copy `≤ MemoryLimit`. All fit the I1 terms. One exception: several fetch request
copies in flight in one invocation are not in the static term. The runtime still governs them (a
`503` at a minimal budget). This is documented rather than modelled. The model is revisited if a
measured peak shows otherwise.

### 1.4 Deadline owner

Pinned transport facts:
- **Go 1.27.4 net/http (H1).** The server sets the connection read deadline (`ReadTimeout`)
  before the handler. `response.SetReadDeadline` sets the connection deadline directly.
  `abortPendingRead` runs only in `finishRequest` and hijack.
- **Go `internal/http2` (H2).** A per-stream `AfterFunc(ReadTimeout)` is armed at stream start.
  `SetReadDeadline(past)` calls `onReadTimeout` immediately, closing the body pipe with
  `ErrDeadlineExceeded` for good. A future deadline replaces the timer.
- **quic-go v0.65.0 (H3).** `SetReadDeadline` sets a per-stream deadline. `read_timeout` is ignored.

**Design (independent mechanism, not record-and-restore).** A new package `internal/reqdeadline`
provides `Owner`, created by `server.dynamicHandler` (H1, H2 and H3 all enter here) from the raw
transport writer, before any wrapper, and stored in the request context.

- **Inspection never sets a future deadline.** `BeginBodyRead(timeout, mode)` arms an owner
  `time.AfterFunc` for the absolute deadline `start+timeout`, fixed at the first begin for the
  request. It is never refreshed per chunk, retry, plugin or replay. On expiry, the callback
  (holding the owner mutex, never across a network read) records `expired` and applies a sticky
  abort: `SetReadDeadline(past)` on the raw writer.
- `EndBodyRead` stops the timer and reports `expired`. The transport deadline is never touched on
  success, so the server deadline (H1 connection deadline, H2 stream timer) remains exactly as
  installed. **Nothing needs restoring**, and a stricter server deadline can never be extended. The
  effective deadline is the earliest of server, body and abort by construction.
- `Abort()` is sticky. Once aborted, no inspection path ever calls `SetReadDeadline` with a later
  time, because none exists. Plugin retirement (`abortPreparations`) and
  `respwriter.ActiveExchange.triggerStreamAbort` (its read side) go through `Owner.Abort` when an
  owner is present.
- `End()` is called when the request handler returns. After it, the timer and `Abort` are no-ops,
  so no callback touches a completed writer. Callbacks run under the mutex that `End` takes.
- **Observe mode.** No abort. Expiry counts `would_timeout` once per request.
- **`body_timeout = 0s`.** No timer. Server deadlines and aborts still apply.
- **Unsupported.** The raw writer, walked like `ResponseController`, lacks `SetReadDeadline`, or a
  forwarding writer reports through `SupportsReadDeadline` that it does not reach one. In
  enforce mode with a timeout, the request fails with `500` and `unsupported`. In observe mode it is
  recorded and the request continues. A writer that has the method but refuses the abort when the
  deadline expires (for example with `http.ErrNotSupported`) is also `unsupported`, not `timeout`:
  the owner ends the blocked read through the transport, and `EndBodyRead` reports
  `BodyUnsupported`, so the request fails with `500`. On HTTP/1 the server registers each
  connection (`reqdeadline.ConnContext`), and the owner applies the same past read deadline to it
  under its lock inside the read window, closing it only if it refuses too. Closing a net/http
  HTTP/1 body cannot end the read, because the body holds its lock while a read blocks. On HTTP/2
  and HTTP/3 the owner closes the request body, which does end the read. A refused retirement
  abort is handled the same way. Without a registered connection (an embedder's own
  `http.Server`) nothing could end a blocked HTTP/1 read whose abort is refused, so the owner treats
  HTTP/1 deadlines as unsupported there whatever the writer: in enforce mode the request fails with
  `500` and `unsupported` before its body is read, and the response closes the connection instead
  of letting net/http drain the unread body. A retirement abort without an enforced deadline never
  closes an HTTP/1 body, so it cannot wait on the read; the request fails as retired when the read
  ends. No future or zero deadline is ever set to probe support. When there is no owner in the context (outside the server, e.g.
  in tests), the plugin builds a local owner from its own writer via the narrow `SetReadDeadline`
  forwarding the WAF interceptor already provides. No wrapper gains `Unwrap`.
- **Reload.** Limits (`Mode`, `BodyTimeout`) are snapshotted at `BeginBodyRead`. Later publishes
  never re-arm an active read.

The ADR (H6 §5) describes record-and-restore. This design meets every invariant that section
states (earliest deadline, sticky abort, no extension, per stream, narrow forwarding) without a
restore step. The design review below decides whether it is an implementation choice or an
amendment.

### 1.5 Error taxonomy

| Condition | Status | Notes |
| --- | --- | --- |
| Lease or charge `bytes` saturation (enforce) | `503`, `Retry-After: 1`, body `inspection_overloaded` | gRPC (`application/grpc`, not `-web`): trailers-only `grpc-status: 14`. No guest call, no origin call, no invoke or panic metric. |
| Host-call charge failure inside the guest | Same `503` | Retained by `inv.fail`. Wins over a later guest trap that the error caused. |
| Body arrival timeout (enforce) | `408`; H1 adds `Connection: close` | gRPC: `grpc-status: 4`. Metric `timeout`. Plugin `body_fail` reason `read_error` is not counted twice. |
| Unsupported deadline (enforce, timeout > 0) | `500` | Metric `unsupported`. |
| Oversize, read, cancel or retired preparation | Unchanged #699 (`500`; cancel is a dropped request) | |
| Instance pool exhaustion | `503` (unchanged) | |
| Trap, guest timeout | `500`; panic metric and invocation `error` (unchanged) | |
| Host error (not overload) | `500`; invocation `error`, no panic (unchanged) | |
| Response callback overload | `503` via `responseLayer.decide` | Like `errInstanceLimit` |

### 1.6 Observe semantics

The governor already over-admits in observe mode and fires `OnRejection(reason, observe)`. I2 adds
no governor-driven `503`, `408` or `500`. Deterministic fixed-state tests run the same sequence under
both modes and compare the recorded decisions.

### 1.7 Test matrix (requirement → test)

| Area | Test location |
| --- | --- |
| Lease entry and lifetime, owner close, zero on uninspected routes | `internal/plugins/inspection_lease_test.go` |
| Body boundaries and growth accounting (+ fuzz) | `internal/plugins/body_charge_test.go`, `FuzzReadBoundedCharged` |
| Host functions (fetch, state, write, replace; ignored errors; malicious lengths) | `internal/plugins/host_charge_test.go` (real guest fixtures) |
| Overload delivery on H1, H2 and H3 | `internal/plugins/body_prepare_http3_test.go`, `internal/server` integration |
| Pool and guest lifecycle | `internal/plugins/inspection_lease_test.go` |
| Deadline owner (unit and race) | `internal/reqdeadline/owner_test.go` |
| Protocols, wrappers, siblings | `internal/server` real H1, TLS/H2 and H3 tests |
| Cancellation and reload | `internal/plugins` and `internal/app` reload tests |
| Observe equivalence | `internal/plugins/inspection_observe_test.go` |
| Error taxonomy and redaction | `internal/inspection/respond_test.go`, `internal/app` hook log test |
| Streaming compatibility, #697/#698/#699 regressions | Existing tests kept green |

Measured claims map to the I5 harness under a reviewed plan v3 (section 3).

### 1.8 Exclusions and their evidence implications

- **I3.** WAF state is not charged. s9/F3 `both` may still OOM, which is recorded against I3/I6.
  `both` cells are wrapper-integration checks only.
- **I4.** Subscription buffers are not charged. Plugin-only stress fixtures avoid subscription body
  mode so that I4 exposure does not dominate.

### 1.9 Facts and assumptions

Demonstrated from source:
- the transport deadline behaviour above;
- the composition order;
- that `background.Detach` drops the context values;
- that H2 `onReadTimeout` only closes the request-body pipe (`internal/http2/server.go`
  `(*stream).onReadTimeout`), so the stream stays writable for the `408`.

Assumed until tested: H3 `408` delivery after a past stream read deadline, already observed in F4b
on the old harness. The real-protocol tests re-establish it.

### 1.10 Independent design review and resolutions

A read-only design review (rubber-duck agent, 2026-11-12) concentrated on lifetime, deadline
restoration and error classification. Its findings and their resolutions are below. They supersede
the corresponding text above where they differ.

| # | Finding | Severity | Resolution |
| --- | --- | --- | --- |
| R1 | Fetch request copies can outlive the lease. `RoundTrip` may close the request body asynchronously after it returns, and `Lease.Close` refunds unconditionally. | Blocking | The fetch host function does not return until the transport has closed every request-body reader it opened. After the response is read, the fetch context is cancelled, which makes the transport abort any write still in progress and close the body. The host then waits for the close signal before releasing the copy's charge. The copy therefore never outlives the host call, the invocation or the lease. At most one request copy (≤ the guest memory limit, the modelled `MemoryLimit` term) is live at a time. The wait is bounded by a fixed grace period after cancellation. If the transport has still not closed the body by then, this is logged and documented as a residual. Tests: delayed close, fetch failure, guest termination. |
| R2 | A timer callback that has already started can run after `EndBodyRead`, and a delayed callback could admit an over-deadline read. | Blocking | `fire` checks the phase state under the owner lock and does nothing once the phase is done. `EndBodyRead` judges expiry against the absolute deadline itself (`TestLateCallbackCannotAdmitOverDeadlineRead`, `TestCallbackBlockedBehindCompletionDoesNothing`). |
| R3 | Several fetch request copies, and a state overwrite (new copy charged before the old is dropped), exceed the static terms. | Blocking | Fetch: see R1, at most one copy at a time. State: validate the guest range, drop the old state and release its charge (it cannot be aliased before the invocation returns), then charge and copy the new state. The peak stays within the 4096-byte term. The fetch response buffer's old charge is released before the next buffer is charged. The static model is unchanged and stays sufficient. |
| R4 | Overload in a response callback would reuse the instance-limit path and lose its identity and the gRPC mapping. | Blocking | `responseLayer` keeps the typed inspection error. `writeFinal` resets the headers as today, then answers through `inspection.WriteOverloaded`. Instance exhaustion and guest failures keep their existing responses. |
| R5 | H1 claims were overstated. net/http clears the connection read deadline when it starts the post-EOF background read, on hijack and in `abortPendingRead`. | Non-blocking | The guarantee is scoped to Jul's own setters and to the upload-admission phase. The owner's logical abort is authoritative: preparation checks `Owner.Aborted` and expiry, whatever the transport deadline currently is. After a hijack, the upgraded session owns the connection (`respwriter/upgrade.go`). |
| R6 | Host errors were listed with traps as panic-metric events. | Non-blocking | Corrected. `callHostError` counts an invocation `error` but no panic. An overload stored by a host function wins over a guest trap it provoked, and is counted as neither a panic nor a trap. |

**Deadline verdict from the review.** The timer-only mechanism is viable and, with R2 and R5, sound.
H6 §5 explicitly prescribes record-and-restore, however, so the reviewer judged the change to be an
**ADR amendment** requiring a maintainer decision. Recording the server deadline at installation is
not possible without approximation. net/http arms the H2 stream timer inside the transport and
exposes no getter, and the prompt forbids approximating it. That is the justification for the
amendment.

## 2. As built

Section 1 is unchanged from the frozen design and the review. Where implementation differs from it,
this section governs.

### 2.1 Differences from the design note

- **Test locations.** The section 1.7 file names were provisional. The actual files are listed in
  section 2.2. Observe equivalence is in `inspection_lease_test.go`, not a separate file. Protocol
  delivery is in `inspection_protocol_test.go` (H1 and TLS/H2) and `inspection_http3_test.go`
  (H3). The full-stack server path is in `internal/app/inspection_plugin_serve_test.go`.
- **Stream abort.** `triggerStreamAbort` (#699 retirement) is unchanged. It applies the past
  per-stream read deadline directly rather than through `reqdeadline.Owner.Abort`. The owner
  records retirement as sticky, so a later `EndBodyRead` cannot admit the body.
- **Fetch request copy (R1).** At most one copy is live per invocation, and it never outlives the
  `fetch` call. After the response is read, the call waits up to `fetchBodyCloseGrace` = 2 s, or
  until the request ends if that is sooner, for the transport to close every reader it opened.
  Any reader still open is then revoked: the host drops the copy from the body and from each
  reader, under the reader's lock, so a later `Read` fails and cannot reach the array. Only then
  is the charge refunded. The copy is detached on every path, including when every reader closed
  in time: a closed reader drops its body, and the body drops the copy, so a transport that keeps
  the closed reader, the request or its `GetBody` closure cannot reach the refunded array. A replay
  (`GetBody`) after the seal or revocation is refused. Tests:
  `TestFetchBodyHeldPastGraceIsRevokedBeforeRefund`, `TestFetchBodyHeldAtCancelIsRevoked`,
  `TestFetchBodyClosedNormallyIsDetachedBeforeRefund` (weak pointer and GC) and
  `TestFetchBodyRefusesReplayAfterSeal`. (Before the external reviews in section 4, a held copy
  stayed reachable by the transport after its lease refunded it, and then a normally closed one.)
- **Generated-response growth.** `appendResponse` grows into a new charged array while both
  arrays stay charged, points `inv.respBody` at the new array, and only then refunds the old
  capacity (`TestAppendResponseDropsOldArrayBeforeRefund`).
- **Request-phase header undo.** `set_response_header` (v1 and v2) journals each header's value
  before the first plugin set it. When a request-phase call fails (overload `503`, `408`, `500`),
  the headers set by that plugin, and by outer plugins that continued (carried in the request
  context), are restored before the replacement response is written. Headers from other
  middleware keep their values, and compression sets `Content-Encoding` only when it compresses
  (`TestOverloadDiscardsPluginResponseHeaders`, both ABIs, real HTTP client negotiating gzip).
- **Fetch read.** A manual read loop replaces `io.ReadFull`. A response cut short of its declared
  length (`io.ErrUnexpectedEOF` from the transport) is therefore an error, not a short success.
  This was found and fixed during implementation (`TestFetchResponseChargeBySize/cut_short`).
- **Instance waiters.** Plugin retirement does not wake a request waiting for an instance. It ends
  at the existing finite wait (`timeout`) while still holding its lease and prepared body, which
  is bounded by admitted leases (`TestInstanceWaitHoldsLeaseAndBody`).
- **Trap reasons.** Only public wazero `sys.ExitError` codes are distinguished: `timeout`,
  `canceled` and `guest_exit`. A recovered host panic is `host_panic`. Anything else is
  `unknown`, including a Go `wasip1` guest panic, which surfaces as a wasm trap and not as a
  process exit.
- **Status projection.** `request_path_integration` is now `partial`, not `pending`. Generated
  configuration descriptions say that the plugin request path is governed and the WAF is not.

### 2.2 Test matrix as built

| Area | Tests |
| --- | --- |
| Lease entry and lifetime | `inspection_lease_test.go`: `TestRequestLeaseSpansChainAndDownstream`, `TestRequestLeaseClosesOnEveryOutcome`, `TestMissingLeaseIsAnInvariantFailure` |
| Body boundaries, growth, fuzz | `admission_test.go`: `TestChargedBufGrowHoldsBothArrays`, `TestChargedBufOverflowIsRefused`, `TestNextBodyCap`, `TestReadBoundedRefusedAtEveryStep`, `TestReadBoundedDeclaredLengths`, `FuzzReadBoundedCharged`; existing `body_prepare_test.go` (#699) |
| Host functions | `host_charge_test.go` (fetch sizes, repeated fetch, refusal, ignored refusal, held-copy revocation at the grace and at cancellation, normally closed copy detached from a retained request); `TestGeneratedResponseChargedAndBounded`, `TestAppendResponseDropsOldArrayBeforeRefund`, `TestRequestStateOverwriteKeepsOneCharge`, `TestResponseReplacementOverloadIsDelivered`, `TestHostOverloadWinsOverProvokedTrap`, `TestOverloadDiscardsPluginResponseHeaders` |
| Overload | `TestLeaseAdmissionOverloadDoesNoWork`, `TestBodyChargeOverloadMidRead`, `TestOverloadGRPCMapping`, `TestConcurrentChargesStayWithinBudget` |
| Pool and guest lifecycle | `TestInstanceWaitHoldsLeaseAndBody`, `TestRetirementAbortIsStickyAndAttributed`; existing `instance_limit_test.go`, `v2_lifecycle_test.go` |
| Deadline owner | `internal/reqdeadline/owner_test.go` (19 tests, including the barrier race `TestRacingAbortExpiryCompletionAndEnd`, the refused-abort cases `TestRefusedAbortFailsClosed` and `TestRefusedRetirementAbortInterruptsRead`, and `TestUnregisteredH1IsUnsupported`); real HTTP/1 servers stalling a declared 100-byte upload after one byte: `h1_refused_test.go` (expiry and retirement through the registered connection; without registration, fail-closed `500` before the read for native and refusing writers, and a refused abort that never waits on the read), `TestRefusedAbortEndsRealH1Upload` (plugin expiry and retirement), `TestUnregisteredH1UploadFailsClosed` (plugin chain on an embedder's unregistered server: `500` and `unsupported` with no guest call, and retirement that does not wait on the read) and `TestServerConnContextEndsRefusedH1Read` (server wiring); `TestWAFInterceptorReportsReadDeadlineSupport`; the `enforce refused abort` case of `TestBodyDeadlineOutcomes` |
| Protocols and siblings | `TestInspectionOutcomesOverH1H2`, `TestInspectionOutcomesOverH3` (both count accepted connections, so connection reuse after the timeout and overload cases is asserted, not assumed), `TestServeInspectionDeadlineThroughFullStack` |
| Reload | `TestPublishedLimitsGovernNewDecisionsOnly`; existing `TestReloadKeepsPreparingRequestOnItsGeneration`, `TestV2ReloadUnderLoadAndChurnQuiesces` |
| Observe | `TestObserveMatchesEnforceDecisions`, `TestBodyDeadlineOutcomes`, `TestObserveRecordsWouldTimeoutOnceWithoutAbort` |
| Error taxonomy and redaction | `internal/inspection/respond_test.go`, `internal/app/inspection_log_test.go`, `TestContainedTrapLogIsBoundedAndThrottled`, `TestTrapReasonUsesOnlyPublicExitCodes` |
| Streaming and #697/#698/#699 | `TestNonReaderStreamsWithoutPreparation`, `TestSlowUploadDoesNotConsumeGuestTimeout` and the existing suites, unchanged and passing |

## 3. Measured evidence

Raw runs, identities, scripts and regenerated summaries are in
[`evidence/2026-11-12-700-i2/`](evidence/2026-11-12-700-i2/README.md). These are I2 measurements
under harness plan v3. They are not A1–A18 qualification, which remains I6.

### 3.1 Plan, builds and environment

- **Plan.** v3, sha256 `5ce583d03555dddb1ecde57cd3e03c0edff0af95cae73a2f62efba7676314bd8`,
  frozen before the campaign. The rubber-duck review of the harness changes ran in two rounds and
  found no blocking issue. v3 adds:
  - the `inspect-nobody` guest;
  - the p1–p4 plugin cells and the plugin-only `f4-body-deadline-5s` variant;
  - the H3 `wasm` normal cell, calibrated as `pre-freeze-cal-h3-wasm`;
  - the `u1b` H1-none revision;
  - `reload_every`;
  - the acknowledged-reload and post-recovery drain validity rules.

  No v1 or v2 value is reused or merged.
- **Targets.**
  - `cand` is `b1a670fe` (I2).
  - `cand2` is `a8be1417`, I2 with the main #709 fix, measured only in the `p4` re-run.
  - `main` is `d8d0520b` (pre-I2 main).
  - `base` is the ADR source `858590dc`, as its historical go1.27.3 build.
  - `base4` is the I5 go1.27.4 rebuild of `858590dc`. It differs in toolchain and in linkage
    (`CGO_ENABLED=1`), is labelled separately and is used only in the supplement.

  `main`, `cand`, the harness and both guests were rebuilt from `git archive` exports and
  reproduced their sha256 values.
- **Head versus measured binary.** The head adds two runtime commits to the measured `b1a670fe`:
  - `e1c6357c` (section 4) changes only the `fetch` host function, which no campaign fixture
    calls.
  - `a8be1417` fixes #709 (section 3.2). It changes reload retention, so `p4` was re-measured as
    `cand2`. Otherwise it only clears fields of exchanges that have already closed.
  - `2a9ced55` completes #709 for 101-upgraded exchanges, which no campaign fixture uses.
- **Envelope.** Verified on every run: 2 vCPU, 768 MiB, no swap, `GOMAXPROCS=2`. The client and
  origin run outside the envelope.
- **Budgets.** Each fixture has an admissible budget that passes the real minimum-budget
  validation:
  - the default 256 MiB, or 512 MiB for WAF variants;
  - 128 MiB with sized pools for the p2 saturation cells.

  Global defaults are unchanged.

### 3.2 Protection results for the I2-governed allocations

| Claim | Cell | Result |
| --- | --- | --- |
| Fail-fast overload is delivered and leaves other traffic progressing | `p2-saturation-plugin-h1` | `cand`:<br>• 539 × `503 inspection_overloaded`, p50 1.8 ms and max 245 ms, against a p50 of 5.3 s for an admitted upload.<br>• Governed bytes peaked at 126.6 of 128 MiB with 135 leases; 35,748 probes succeeded.<br>• Peak 518 MiB; post-GC heap 55 MiB.<br>• Leases and bytes drained to 0.<br>`main` never rejects, so its p2 run is instrument-invalid (unexercised) by design. |
| Observe accounts and continues | `p2o-saturation-plugin-observe-h1` | `cand`:<br>• All 640 uploads succeeded with no 503.<br>• `rejections_total{mode="observe"}` = 84,965. Observe counts every refused charge (documented I1 meaning).<br>• Governed bytes reached 152 MiB, above the 128 MiB budget, as observe allows.<br>• Peak 525 MiB. |
| Body deadline: plugin-only, on real protocols | `f4-body-deadline-5s` (`body_timeout = "5s"`) | `cand` `wasm` and `both`: H1, H2 and H3 stalled uploads each received `408` at 5.0 s. The sibling on the same connection (H2/H3) or a parallel one (H1) answered within a few milliseconds.<br>`main` ran until the client's 20 s watchdog.<br>`none` (no inspection) reached 20 s on both binaries. |
| Valid slow uploads keep working and spend no guest time while arriving (#699) | `f4-stalled-body`, `r1-slow-upload-c16-h1` | Default 60 s: slow H1/H2/H3 uploads succeeded in 7.6–8.1 s with healthy siblings, and stalled uploads reached the client watchdog, unchanged from `main`.<br>r1: 3.2 rps on both, peak 234 vs 225 MiB. |
| Non-body plugins stream and charge only host state | `p3-nobody-plugin-h1`, `r2-fast-nobody-h1` | Governed bytes ≤ 648 KiB and ≤ 27 leases, drained to 0. Bodies are not prepared. See 3.3 for peaks. |
| Large bodies stay bounded | `p1-large-body-plugin-h1` (pool 4) | `cand`: peak 461 MiB (`main` 481); governed bytes up to 48 MiB; drained to 0. |
| Reload and budget churn drain | `p4-reload-budget-plugin` | `cand`:<br>• valid, including the v3 drain rule;<br>• 8 reloads: 7 applied live (budgets 128m/256m/512m, enforce↔observe) and a `1m` budget refused by minimum-budget validation;<br>• 63,760 loads, all correct; in-flight H2 streams reset on generation drain: 48 (`main` 80);<br>• leases and bytes 0 after recovery.<br>After the #709 fix, `cand2` (3 repetitions, `runs/i2-p4-709fix`): peak 500–501 MiB, below the 576 MiB objective (`main` 658–677); post-GC heap 69–103 MiB (`main` 504–525); no OOM; every reload acknowledged; leases and bytes 0. |

#### Reload retirement (#709): fixed in this PR

Under reload churn, retired handler generations stayed reachable, with their plugins, wazero
instances and guest memory. I2 must keep reload and retirement correct, so this is fixed here in
two separate commits, `a8be1417` and `2a9ced55`, even though the defect predates I2.

- **Root cause.** `respwriter.ExchangeRegistry.PublishGeneration` keeps the last 32 generation
  steps so later recomputes can report outcomes. Each step's tracker keeps its retired
  `*ActiveExchange`. A closed exchange still held its `cancel` closure, an unused `ctx` field and
  the raw H1 connection. The request context carries the generation lease (`background.WithLease`),
  which reaches the `handlerGen`. Every stream retired by a reload therefore pinned its generation
  for up to 32 further reloads. Governed charges were never involved; leases and bytes drained to
  0 in every run before and after the fix.
- **Fix.** `markClosed` now drops `cancel`, `rawH1Conn` and `controller` under the exchange lock
  before cancelling, and the unused `ctx` field is gone. `triggerStreamAbort` reads both under the
  lock and tolerates a closed exchange. `TestRetiredExchangeHistoryDropsRequestContext` fails
  without the fix.
- **Upgraded exchanges.** A closed 101 upgrade (WebSocket or opaque) also kept its session for
  outcome reporting. The session keeps the upstream leg, which is the proxy's attempt body and
  holds the request context. In `2a9ced55` the exchange keeps only the session's shared
  clean-handshake flag, and `markClosed` drops the session and upstream references.
  `TestRetiredUpgradedExchangeHistoryDropsBackend` fails without it. No campaign fixture
  upgrades, so this path has unit evidence only.
- **Outcome reporting** (graceful, forced, within bound) reads the same values as before.
- **Attribution.** Instrumented diagnostic builds (`runs/diag-709-instr-*`,
  `attachments/diag-709-instrumentation.patch`) counted objects created, closed and collected
  after two GCs at the end of `p4`:

  | Build | Generations made / closed / collected | Plugins made / closed / collected | Heap |
  | --- | --- | --- | --- |
  | Before the fix | 15 / 14 / 8 | 34 / 32 / 20 | 523 MiB |
  | With the fix | 15 / 14 / 14 | 34 / 32 / 32 | 99 MiB |

- **Faster churn.** A labelled re-check with the pre-freeze `ia-wip` harness (2 s unacknowledged
  reloads, default pools, no `GOMEMLIMIT`, `runs/diag-709-p4-2s-cand2`) completed both repetitions
  with 22 reloads, peaks of 587 and 542 MiB and post-GC heaps of 98 and 122 MiB. The pre-freeze
  2 s runs of `main` and `cand` were OOM-killed. The identities differ, so this is a diagnostic,
  not a comparison.
- **H1 and H2 errors during reload.** A plugin change makes the route `uncertain`, so the
  ADR 0026/#642 retirement contract aborts old-generation exchanges: at once if a header was
  already written, otherwise after the 1 s grace. An H1 abort closes the connection. Three
  instrumented `p4` runs logged 4, 0 and 1 H1 aborts after a written header and saw exactly 4, 0
  and 1 H1 client errors (`body-eof`, `reset`, `eof`). Each failure is reported to the client as
  an error, never as a complete response. These are designed retirement aborts, not truncation
  defects, and occur on `main` as well. The in-flight H2 `stream-reset` counts are the same
  contract.

### 3.3 Findings: not I2 regressions, and owned elsewhere

Each finding is preserved as a `correct=false`, invalid or OOM run.

| Finding | Evidence | Attribution and owner |
| --- | --- | --- |
| Default plugin pools exceed the envelope | Pre-freeze `p1`/`p2` default-pool runs were OOM-killed on both binaries. `p3` peaks track live instances, not governed bytes:<br>• frozen `cand` 704 MiB with 32 instances;<br>• the 5-repetition investigation: `main` 339–531 MiB (12–26 instances), `cand` 313–550 MiB (13–25 instances). | Static guest-pool term (`max_instances × memory_limit`), outside the dynamic governor by ADR 0032. I6 sizing; operators size pools. |
| Connection buffers under no-backoff clients | `p2h-saturation-hostile-h1` `cand`: OOM-killed with governed bytes at 126 MiB. In the pre-freeze `memory.stat`, socket memory reached 286 MiB beside 393 MiB anon. | ADR 0032 §10: kernel and connection buffers are not governed. The cell is a frozen record cell, never a protection claim. `main` does not reject, so it shows no flood. I6. |
| WAF state is uncharged | `s6`/`s7`/`s8` `both`: peaks 446–652 MiB on both binaries | I3 (and I6). `both` cells are wrapper-integration checks only. |
| Contained guest 500s under CPU saturation | `p1`, `p3`, `s6`, `s7`, `s8` `wasm`/`both`: 1–48 per run on both binaries. Logged reasons:<br>• `timeout` (guest invocation deadline under saturation);<br>• `unknown` in `p1`. | Pre-existing guest behaviour (#699 containment). I2 now logs a bounded reason. |

### 3.4 Unsaturated throughput (ADR 10% rule)

Cells: `u1`/`u2` × `none`/`waf`/`wasm`/`both`, `u10` × `none`/`wasm`/`both`, and `u1b` (H1 none,
c8).

- **Frozen campaign, `main` → `cand`.** No median regression above 10%. `u2` `none`/`both`/`waf`
  pass. The other cells are `inconclusive-noise`, so 3 more repetitions were added
  (`runs/i2-normal-supplement`, 5 for `u1b`).
- **Supplement alone.** Two `main` → `cand` cells crossed 10%: `u1b none` +13.7% and `u2 wasm`
  +19.4%.
- **Pooled (6–10 per cell).** Every cell is within 10%: the worst is `u10 wasm` at +9.3%, and
  `u1b none` −8.7% and `u2 wasm` −6.4% favour the candidate. The pooled verdict is
  `inconclusive-noise` except where it passes.
- **Noise investigation.** Spreads are 20–82%. Every target moves together in the same time window:
  for example, all four targets fell to 283–378 rps in the third supplement repetition of `u2 wasm`.
  Same-source comparisons show swings of the same size:
  - `base` vs `base4` crosses 10% in several cells;
  - `u1b none` exercises no I2 path except the per-request deadline owner (one small allocation)
    and still moved ±14%.

  `summary/paired-ratios.md` divides each `cand` result by the `main` result of the same
  repetition, measured about 40 s apart. The median within-block regression is at most +4.4%
  (`u10 wasm` and `u1 waf`) in every cell. This is a descriptive drift check; it does not replace
  the ADR rule.
- **Direct cost.** Interleaved `internal/plugins` benchmarks (`attachments/bench/`, n=8) show:
  - no significant time change on any plugin path (geomean −5%, all p > 0.1);
  - +5 allocations and about 0.5 KiB per plugin request for the lease and charges.
- **Verdict.** Under the ADR rule, the I2 throughput result is **not a fail but remains
  inconclusive through host noise**. The evidence attributes the noise to the host: an arm64 WSL2
  machine with time-correlated drift. A quieter qualification host is an I6 requirement.
- **`base` comparisons.**
  - `base` → `cand` pooled is within 10%.
  - `base` → `main` fails for `u1 none`, `u10 none` and `u2 wasm`. Those differences predate I2,
    and they combine the toolchain confound and the noise above.
  - No improvement percentage is claimed. The `none` cells are faster on `cand` within every block
    (16–26%), which is unexplained, so this is not claimed either.
- **Capacity cells (saturated, outside the 10% rule).**
  - `p3 wasm`: `cand` median 1206 vs `main` 1396 rps over 5 repetitions. The within-block ratios
    are 0.58–1.05, and the frozen repetition favoured `cand` (1695 vs 1647).
  - `p1 none` (no plugin): 1858 vs 2332, with block ratios of 0.68, 1.35 and 0.79.

  Both are recorded as noise-dominated. The benchmarks above bound the direct I2 cost.

### 3.5 Validation gaps

- The throughput criterion is inconclusive on this host, not passed. This is restated for I6.
- Single repetitions of the plugin and deadline record cells establish behaviour, not sizing
  distributions.
- s9/F3 `both` (WAF state) and response-subscription (I4) exposure were not re-measured as I2
  claims.

## 4. Reviews

| Review | Scope | Result |
| --- | --- | --- |
| Rubber-duck design review | Lifetime, deadline restoration, error classification (before coding) | R1–R6 (section 1.10); the deadline finding led to ADR §6 amendment 8 (maintainer decision) |
| Rubber-duck harness review | Plan v3 changes (two rounds) | Unexercised budget churn, drain gauges and reload cadence were fixed; the final round found no blocking issue |
| Code review agent | Full I2 diff at `b1a670fe` | No significant issue |
| Security review agent | Full I2 diff at `b1a670fe` | No vulnerability found. One robustness note was fixed in `e1c6357c`: the late fetch-body waiter goroutine could outlive a request whose transport never closes the body. It now also ends with the request context; the copy stays charged until lease close (`TestFetchBodyHeldWaiterEndsWithRequest`). Superseded by the external review below: the waiter is gone, and a held copy is revoked before its refund |
| Code review agent | #709 fixes `a8be1417` and `2a9ced55` (locking, outcome reporting, upgrade ordering, regression tests) | One data race fixed before push: `Hijack` re-read `upgradedBack` after unlocking while `markClosed` could clear it. It now reads it once under the lock. The regression tests passed 200 times under `-race -cpu 1,2,8` |
| External review | I2 head `49669720` (implementation, tests, ADR amendment, this report, 50 evidence checksums) | Five findings, all still present at `d83a7274` because the #709 commits touched none of the affected files, and all fixed in the next commit. (1) A fetch copy held by the transport past the grace was refunded at lease close while the transport could still read it; held readers are now revoked before the refund (R1 above). (2) A writer that has `SetReadDeadline` but refuses the abort left the read blocked and answered `408`; it is now `unsupported`, the read is interrupted and the answer is `500`. (3) Request-phase plugin headers such as `Content-Encoding: gzip` survived the overload `503`; they are now undone. (4) `appendResponse` refunded the old capacity before `inv.respBody` dropped it. (5) The H3 `sameConn` check always passed; it now counts QUIC connections. Each new test was confirmed to fail with its fix reverted. The reviewer's other conclusions stand: WAF and response accounting remain later slices, and the unsaturated performance criterion stays inconclusive |
| External re-review | I2 head `092ddbcc` | Fixes (3), (4) and (5) and the #709 changes accepted. Three items remained, all fixed in the next commit. (A) The refused-abort fallback closed the request body, which cannot end a real HTTP/1 read: net/http's body holds its lock while the read blocks, and `Close` needs it. The reviewer's stalled 100-byte upload stayed blocked until the client left. The server now registers each HTTP/1 connection, and a refused abort applies the past read deadline to it. Real HTTP/1 tests cover expiry and retirement. (B) A normally closed fetch reader kept the copy reachable through its body, as did the request's `GetBody` closure, after the refund. The copy is now detached on every path. (C) `SHA256SUMS` listed `attachments/pre-freeze-envsweep-main-console.log`, which the repository's `*.log` ignore rule had kept out of the commit. It is now committed unchanged, and `sha256sum -c` passes on a clean checkout. The new tests fail with their fix reverted: without the registration, the HTTP/1 tests stay blocked |
| Revision of `4f7b5534` (before re-review) | Embedder HTTP/1 servers that do not register connections | `4f7b5534` left one case documented as a limitation: on an HTTP/1 server without `reqdeadline.ConnContext`, a refused abort still could not end a blocked read. The owner now treats HTTP/1 deadlines on an unregistered connection as unsupported whatever the writer, so enforce mode fails with `500` and `unsupported` before reading, and the response closes the connection instead of letting net/http drain the unread body. Without a registered connection an HTTP/1 abort never closes the body, so retirement cannot wait on the read. Real HTTP/1 tests cover expiry and retirement for net/http's own writer and a refusing one. Each was confirmed to fail with its change reverted: treating the writer alone as enough (blocked until the client gives up), omitting `Connection: close` (net/http drains the stalled body first), and closing the HTTP/1 body on a refused abort (`Abort` blocks on the read). The same commit fixes `TestReloadPostPublishTimeoutReportsDegraded`, which failed the `4f7b5534` `test -race` CI job. That failure is a timing flake already on `main`: under CPU contention it fails 79/100 at `d8d0520b` and 89/100 on this branch, against 0/100 idle, because its 50 ms reload budget also has to cover the phases before Publish. With a 2 s budget and the post-reload hook waiting past the deadline, it passed 40/40 under the same load |
| External re-review | I2 head `e9db762a` | Earlier findings verified resolved; all 2,690 evidence checksums pass. One further HTTP/1 defect, present since the start of the PR: `WriteOverloaded` answered `503` without `Connection: close`, so net/http first drained an unread upload. A client that declared 100 bytes, sent one and stalled never received the `503` when `read_timeout = 0` (5/5 in the reviewer's reproduction). HTTP/1 overload responses now close the connection, as the `408` and unsupported-deadline responses already did. `TestOverloadReachesStalledH1Upload` covers admission and body-allocation refusals on a real HTTP/1 server; both cases time out with the fix reverted. HTTP/2 and HTTP/3 are unchanged, and `TestInspectionOutcomesOverH1H2` still asserts that the shared connection survives an overload. ADR 0032's admission row, which assumed the drain was bounded, is amended |
