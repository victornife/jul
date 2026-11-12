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
- **Unsupported.** The raw writer, walked like `ResponseController`, lacks `SetReadDeadline`. In
  enforce mode with a timeout, the request fails with `500` and `unsupported`. In observe mode it is
  recorded and the request continues. When there is no owner in the context (outside the server, e.g.
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
- **Fetch request copy (R1).** At most one copy is live per invocation. The fixed grace is
  `fetchBodyCloseGrace` = 2 s. A copy still held by the transport after the grace stays charged
  and is refunded when the transport closes it, or at lease close if that comes first (a release
  after close is a no-op). The goroutine waiting for that late close ends with the request. A
  copy the transport still holds then stays charged until the lease closes. Tests:
  `TestFetchBodyCopyHeldPastGraceRefundsOnLateClose`, `TestFetchBodyHeldWaiterEndsWithRequest`
  and `TestFetchBodyRefusesReplayAfterSeal`.
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
| Host functions | `host_charge_test.go` (fetch sizes, repeated fetch, refusal, ignored refusal, grace path); `TestGeneratedResponseChargedAndBounded`, `TestRequestStateOverwriteKeepsOneCharge`, `TestResponseReplacementOverloadIsDelivered`, `TestHostOverloadWinsOverProvokedTrap` |
| Overload | `TestLeaseAdmissionOverloadDoesNoWork`, `TestBodyChargeOverloadMidRead`, `TestOverloadGRPCMapping`, `TestConcurrentChargesStayWithinBudget` |
| Pool and guest lifecycle | `TestInstanceWaitHoldsLeaseAndBody`, `TestRetirementAbortIsStickyAndAttributed`; existing `instance_limit_test.go`, `v2_lifecycle_test.go` |
| Deadline owner | `internal/reqdeadline/owner_test.go` (16 tests, including the barrier race `TestRacingAbortExpiryCompletionAndEnd`) |
| Protocols and siblings | `TestInspectionOutcomesOverH1H2`, `TestInspectionOutcomesOverH3`, `TestServeInspectionDeadlineThroughFullStack` |
| Reload | `TestPublishedLimitsGovernNewDecisionsOnly`; existing `TestReloadKeepsPreparingRequestOnItsGeneration`, `TestV2ReloadUnderLoadAndChurnQuiesces` |
| Observe | `TestObserveMatchesEnforceDecisions`, `TestBodyDeadlineOutcomes`, `TestObserveRecordsWouldTimeoutOnceWithoutAbort` |
| Error taxonomy and redaction | `internal/inspection/respond_test.go`, `internal/app/inspection_log_test.go`, `TestContainedTrapLogIsBoundedAndThrottled`, `TestTrapReasonUsesOnlyPublicExitCodes` |
| Streaming and #697/#698/#699 | `TestNonReaderStreamsWithoutPreparation`, `TestSlowUploadDoesNotConsumeGuestTimeout` and the existing suites, unchanged and passing |
