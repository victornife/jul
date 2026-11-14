# ADR 0032 — Aggregate inspection resource governor

- **Status:** Accepted — I1 merged (#706, `a57afa4d`); I5 tooling merged (#708, `d8d0520b`); I2
  merged (#710, `c8bba7f2`); I3a merged (#715, `def419f5`); I4 is in progress on
  `feat/700-inspection-waf-i4` (PR #716, hosted checks pending); I3b and I6 remain pending. The
  2026-11-13 amendment below was accepted when #714 merged as `16d98258` from head `faf3ead4`;
  implementation remains incomplete
  until the scheduled checkpoints and qualification gates pass.
- **Date:** 2026-11-10
- **Revised:** 2026-11-10, after the maintainer's review of #704 (changes requested). The
  maintainer's decisions are recorded in §Maintainer decisions.
- **Accepted:** 2026-11-10 by the maintainer. PR #704 merged as `8c483be2` from head
  `81f07e75`. The PR proposed the ADR; this acceptance is recorded separately.
- **Deciders:** Victor Niharra (`@victornife`)
- **Applies to:**
  - the WAF (Coraza) request and response inspection;
  - WASM plugin request-body preparation, guest invocation and response subscriptions;
  - process-wide resource ownership;
  - per-stream body-arrival deadlines on HTTP/1.1, HTTP/2 and HTTP/3;
  - configuration reload, metrics, the runtime status API and the Console.
- **Source:**
  - owner: #700;
  - assessment: #695 ([report](../reviews/2026-11-09-695-waf-wasm-resource-assessment.md));
  - revalidations: [#697/#698](../reviews/2026-11-10-697-698-waf-fix-revalidation.md) and
    [#699](../reviews/2026-11-10-699-plugin-body-preparation-revalidation.md);
  - design feasibility: [evidence](../reviews/evidence/2026-11-10-700-adr/README.md);
  - programme: #62.
- **Related:**
  - [ADR 0017](0017-upstream-resilience-and-overload-control.md): overload taxonomy, 503 rather than
    429, and admission as counters plus limits;
  - [ADR 0011](0011-reload-plan.md): the reload transaction;
  - [ADR 0014](0014-operability-surfaces.md): operability surfaces;
  - [ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md): configuration authority;
  - [ADR 0020](0020-wasm-abi-v2-response-phase.md): the WASM response phase;
  - [ADR 0031](0031-waf-operations-posture-isolation-and-alerting.md): WAF transaction outcomes;
  - [resource ownership](../resource-ownership.md).

> **Accepted design record.** This ADR is the implementation contract for #700. Acceptance adds no
> configuration fields, enforcement or metrics. It does not establish protection, release readiness,
> a maturity change or soak. Those require implementation slices I1–I6 and the acceptance plan
> (A1–A18; if the 2026-11-13 proposed amendment is accepted, also A19–A22 for the controls it
> adds). Until then, current operator guidance in `docs/waf.md`, `docs/plugins.md` and
> `docs/known-limitations.md` still applies.

## Context

### Corrected baseline

The design is pinned to `main` at `858590dc7c430d24cd33b096a81ca6051591b427`. That commit contains:

- #702, merged as `e1f37cf7bffa5417b249a3ee877dd0a7e8ac2ae2`: the WAF argument-limit guard and the
  response-body ownership fix (#697 and #698);
- #703, merged as `858590dc` with reviewed head `68dd588edf2156993a346f73e41be78c102accd6`: request
  bodies are prepared before plugin instance acquisition (#699).

These fixes exist in merged source only. No published package contains them. The #695 measurements
were taken on runtime `453e62375bdce0302756ff386637832d5fa35bad`. They remain historical evidence and
are not re-attributed to the corrected runtime. Every new comparison in this ADR, and the
implementation acceptance below, uses `858590dc`.

### Problem on the corrected runtime

The facts below were re-verified on `858590dc`. Each is labelled:

- **S** — source-proven;
- **M** — measured in this design's [feasibility evidence](../reviews/evidence/2026-11-10-700-adr/README.md);
- **I** — inferred.

1. **Plugin bodies (S).** Body-reading plugins prepare the request body before acquiring an instance
   (`internal/plugins/runtime.go:719-723`, `body.go:144-175`).
   - The prepared buffer, up to `max_request_body` (default 1 MiB; `host.go:24`), stays referenced
     by the request until it completes. That is after the instance has been released
     (`runtime.go:709`).
   - Chained plugins reuse the same prepared buffer.
   - Nothing bounds the *sum* of prepared bodies across requests.
2. **Plugin instance waiters (S).** The number of instance waiters is unbounded.
   - `acquire` waits up to one call timeout (default 100 ms) per waiter
     (`runtime.go:554-586`).
   - Each waiter holds its prepared body while it waits.
3. **WAF state and amplification (M).** WAF inspection state is large and argument-driven.
   - **Retained state.** An open Coraza v3.8.1 transaction with CRS retains about 37 KiB with no
     arguments and about 37–52 KiB per argument. 991 arguments retain 41.5 MiB.
   - **JSON.** Wide JSON retains about 39× its body size.
   - **Lifetime.** The state lives until `tx.Close` (F1b), and Jul holds the transaction across
     `next.ServeHTTP` (`internal/waf/outcomes.go:206-444`). Long-lived exchanges therefore retain it
     for their whole duration.
   - **Allocation churn.** Per request, 25 MiB for 991 arguments and about 64× the body for wide
     JSON (F1).
4. **Envelope exhaustion (M).** s11 still exhausts the #700 envelope. With the WAF and 742-argument
   bodies:

   | Concurrency | Peak | Outcome |
   | --- | --- | --- |
   | c4 | 502 MiB | completes |
   | c8 | 762 MiB | killed (F2 `slots0`) |

   #702 bounds the *count* of arguments (at most 1000 per transaction). It does not bound the
   aggregate across concurrent transactions.
5. **Cancellation (S).** Coraza ignores client cancellation.
   - Its transaction methods take no context, and rule evaluation never checks one.
   - A `Transaction` is not safe for concurrent `Close`.
   - Jul's `serveHTTP` does not consult `r.Context()` between phases.
   - Guest calls *are* interruptible: wazero `WithCloseOnContextDone` with the per-call timeout.
6. **Read timeouts (S/M).** H3 ignores the server `read_timeout` (`internal/server/http3.go:154-168`;
   F4).
   - `read_timeout` defaults to `0`, which is unbounded, so SSE, WebSocket and large transfers are
     not cut.
   - When set, it is an absolute deadline for the whole request on H1 and H2. A legitimate slow
     upload is cut at the same instant as a stall (F4).
7. **H2 admission goodput (M).** ADR 0017's `max_pending_requests` queue starves H2 goodput when
   requests carry bodies.
   - Queued requests hold unread bodies whose DATA consumes the *shared connection's* flow-control
     credit, so admitted streams on that connection stall.
   - Goodput is 2.0–2.9/s with a 16-deep queue and 15.6/s with no queue (F3), against a maximum of
     16/s.
   - This establishes the cause of the original s5 observation. Retry traffic was not it: honouring
     `Retry-After` changed nothing while the queue existed.

These are merged-source facts. No published package contains any part of the remediation.

## Hypothesis evaluation

The maintainer proposed seven hypotheses for #700. Each is evaluated against source and against
bounded experiments. A finite load test shows behaviour inside one envelope. It cannot prove an
unconditional bound on total process memory, and no verdict below claims one.

### 1. H1 — one process-wide governor, optionally with listener sub-quotas

1. **Proposal and benefit.** One aggregate authority shares the single process and container memory
   envelope. Independent per-listener budgets cannot multiply it.
2. **Evidence.**
   - (S) Every listener and every generation runs in one Go heap. Generations overlap during reload
     and retirement (ADR 0011).
   - (S) Jul already owns process-wide state through the lifecycle registry, which constructs the
     metrics registry exactly once (`internal/lifecycle/registry.go:436`). A governor can follow the
     same pattern.
   - (M) The F2 prototype test `TestSharedCeilingAcrossListenersAndGenerations` holds two listeners
     × two generations to exactly one 1 MiB ceiling under `-race`.
3. **Works when** every charge-generating layer resolves the *same* governor instance, and reload
   updates its limits rather than constructing a new one.
4. **Counterexamples investigated.**
   - *Quota multiplication.* With three listeners and a per-listener budget of 128 MiB, the combined
     admitted work is 384 MiB. That exceeds a 128 MiB aggregate intent. A per-generation budget
     doubles again during a reload overlap.
   - *"A central governor guarantees the cgroup limit."* This is false. The governor sees only the
     work it charges. It cannot see:
     - compiled CRS rule sets (about 90 MiB per generation, #695);
     - plugin guest pools (`max_instances × memory_limit` per plugin per generation);
     - the Go runtime, GC headroom and `sync.Pool` victim retention (F1's invalid column showed that a
       pooled transaction survives one GC);
     - a reload preparing a second compiled generation;
     - Coraza temporary-file spill;
     - net/http and quic-go protocol buffers.
   - The cgroup limit holds only if the operator's sizing covers that static baseline plus the
     governed budget.
5. **Experiment or invariant.** The ownership invariant is *one governor per process, owned by the
   lifecycle registry and never by a generation.* The F2 prototype tests listener and generation
   sharing, and confirms that changing a limit never resets usage.
6. **Verdict: ACCEPT WITH CHANGES.** One process-owned governor, with no listener sub-quotas in
   tranche 1.
   - Sub-quotas would add configuration, status and validation surface without a concrete need:
     there is no tenant isolation requirement in #700, and one hostile listener can already be
     isolated by per-listener rate limits or connection limits.
   - Sub-quotas remain additive later, as a fraction of the one ceiling, never as independent
     budgets.
7. **Consequence.**
   - The governor lives in a new `internal/inspection` package and is owned by the lifecycle
     registry.
   - Generations receive a pointer to it.
   - The documented guarantee is "governed inspection memory", not "process memory".
   - Static baselines are reported so operators can size the envelope.

### 2. H2 — byte budgeting with a secondary request-slot limit

1. **Proposal and benefit.** Bytes address memory exhaustion directly. A secondary slot limit bounds
   per-request overhead and CPU work.
2. **Evidence.**
   - (M) Input bytes do **not** predict inspection memory. A 21.9 KB body with 991 arguments
     retains 41.5 MiB, about 1,940× its size (F1b). A 16 KiB wide JSON body retains 0.6 MiB.
   - (M) Per-request CPU is 289 ms for 991 arguments and 6.5 s for 1 MB of wide JSON (F1).
   - (M) Bounding concurrent WAF evaluations alone keeps s11 well inside the envelope (F2):

     | Evaluation slots | s11 outcome |
     | --- | --- |
     | None | Killed at c8 |
     | 4 | 624–648 MiB |
     | 2 (= `GOMAXPROCS`) | 435–444 MiB at c4–c16; CPU stays saturated at about 19.8 of 20 s |

     With 2 slots, goodput was 5.6–7.9/s against 4.6–9/s without slots, so it falls within the
     noise.
3. **Comparison.**
   - **Byte-only.** This fails on amplification: 16 KiB of input can charge 16 KiB while
     retaining 41 MiB. Charging a multiple of the body does not help either, because the argument
     count, not the body length, drives retention (query arguments have no body at all).
   - **Slot-only.** This bounds concurrent *evaluation*. It does not bound retained state *after*
     evaluation, which lives until `tx.Close` across `next.ServeHTTP`, potentially for a long-lived
     exchange. Nor does it bound prepared plugin bodies or response buffers.
   - **Hybrid.**
     - Exact bytes for buffers we allocate.
     - A *calibrated estimate* for WAF retained state.
     - A per-lease base charge.
     - Evaluation slots for the synchronous, CPU-bound, allocation-heavy Coraza phases.
4. **Counterexample: "small bodies make a large request count harmless".** Each lease also holds a
   request context, wrappers, timers, an H2 or H3 stream and possibly a waiter goroutine. 10,000 tiny
   requests each holding a lease cost real memory and scheduler time even when no body bytes are
   charged.
   - A **base charge per lease** bounds the lease count to `max_buffered_bytes / base`.
   - Slots bound the CPU-heavy part.
   - Protocol stream limits bound the rest: Go h2 defaults to 250 concurrent streams per connection
     and quic-go to 100.
5. **Derivation.**
   - **Slots.** `auto` equals `GOMAXPROCS`. Coraza evaluation is CPU-bound and non-yielding, so
     more slots than processors add memory and no throughput. F2 showed slots = 4 on 2 CPUs gained
     nothing and cost about 200 MiB of peak.
   - **Retained estimate.** The constants below are calibrated against Coraza v3.8.1 with the
     shipped CRS (F1b, K = 1/8/32). Each is the smallest round value at or above every measured row:
     - base: 64 KiB;
     - per argument: 56 KiB;
     - per body byte: 48.
   - **Check against measured rows:**

     | Request | Estimate | Measured |
     | --- | --- | --- |
     | 991 arguments | 55.3 MiB | 41.5 MiB |
     | 601 arguments | 33.5 MiB | 30.9 MiB (this row rules out 48 KiB per argument) |
     | 300 query arguments | 16.5 MiB | 13.6 MiB |
     | 1 MB wide JSON | 47 MiB | 39.3 MiB |

   - **Base charge.** 16 KiB per lease is **inferred**. It must be measured in slice 1.
   - **Scope of the constants.** They cover only the measured shapes, so the WAF term is a
     *calibrated estimate* of retained memory, not a proven bound. The 1 MB JSON row ran with CRS,
     whose `@coraza.conf-recommended` sets a 128 KiB in-memory limit, so most of that body was
     spilled to a temporary file. Its retention still scaled with the whole body. The per-byte term
     therefore applies to all body bytes processed, not to the buffered bytes (Decision §4).
6. **Verdict: ACCEPT WITH CHANGES.** The model is bytes, plus a calibrated WAF retained-state
   estimate, plus a base charge per lease, with evaluation slots as a co-equal limit rather than a
   secondary one. Input bytes alone are never treated as inspection memory.
7. **Consequence.** The governor has four counter classes under one owner. `waf_state` is
   *reserved* at its worst case before each phase and trued down afterwards (Decision §4), because
   amplification happens inside the phase. The classes are:
   - `buffer` — exact bytes;
   - `waf_state` — estimated;
   - `base`;
   - evaluation `slots`.

   A CI calibration test fails when measured retention exceeds the estimate. Upgrading Coraza or CRS
   then requires recalibration (see §Uncertainties).

### 3. H3 — one resource authority covers both the WAF and plugins

1. **Proposal and benefit.** Budgeting only plugin buffers leaves WAF work unprotected. Separate
   independent pools can oversubscribe the same process.
2. **Evidence.**
   - (S) A request through both layers passes four stages:
     1. plugin preparation copies the body (B);
     2. Coraza's request buffer copies it again (B, in memory up to 128 KiB);
     3. the JSON processor copies it a third time (`strings.Builder`);
     4. transaction state retains the argument-driven working set until `tx.Close`.
   - (S) On the response path:
     - a plugin response subscription buffers up to `max_response_body` (default 8 MiB;
       `host.go:27`, `phase.go:171-190`);
     - guest replacement bytes are copied into host memory;
     - WAF response inspection buffers through `WriteResponseBody` (`outcomes.go:635`), up to
       Coraza's response limit of 512 KiB, which Jul does not override.
3. **Single weighted pool versus separate counters under one owner.**
   - A single scalar pool mixes exact bytes with an estimate and cannot express CPU slots.
   - Separate counters under *one* owner can express:
     - one admission decision, with one lease per request;
     - one byte ceiling that both exact and estimated bytes draw from, reported per class;
     - an orthogonal slot ceiling.
4. **Counterexamples investigated.**
   - **Double charging.** The plugin layer and the WAF layer each "admit" the same request. This is
     resolved by attaching one lease through the request context: the outermost inspection layer
     creates it and inner layers reuse it (F2 test `TestNestedLayersShareLeaseNoDeadlock`).
   - **Nested-reservation deadlock.** A request holding bytes waits for a slot while the slot holder
     waits for bytes. This is prevented by the acquisition order below: byte acquisition never waits
     (fail-fast), and slot holders never wait for anything.
   - **Uncharged retention.** A prepared body outlives instance release, which is source-proven
     above. It stays charged to the lease until the request completes.
5. **Trace of a combined request:**

   | Step | Charge |
   | --- | --- |
   | Plugin layer is the first inspection layer | Lease: base charge (fail-fast) |
   | Prepare body | `buffer` += live capacity (both arrays during growth), charged before each allocation |
   | Guest call | Plugin instance (existing bounded wait; guest memory is a static baseline); host fetch, state and copies charged as `buffer` before allocation |
   | Instance released | Bytes stay charged |
   | WAF finds the lease | No new admission |
   | `ProcessURI` and phase 1 | Slot (bounded wait here, because the plugin has already consumed the body; otherwise try-acquire), then reserve R1 for the counted query pairs (Decision §4); release the slot and true down |
   | Body written to Coraza | `buffer` += 2 × `B_buf`, before each write |
   | Phase 2 | Slot (bounded wait), then reserve R2 in addition to R1 (fail-fast); release the slot and true down to the held `ARGS_POST` and `B_total` |
   | `next.ServeHTTP` | No new charge |
   | Plugin response layer buffers | `buffer` += response bytes, before append |
   | WAF response buffer | `buffer` += 2 × `B_resp`, before each write |
   | Phases 3 and 4 | Slot; reserve R3, then R4, then true down R4 |
   | Phase 5 | Slot (bounded wait; if none, runs unslotted and is counted); no new reservation |
   | `tx.Close` | `waf_state` and the WAF buffer released |
   | Request completion | Lease closed; everything remaining released |

   No category in that trace is uncharged except the documented static baselines. No category is
   charged twice: each copy that really exists is charged once.
6. **Verdict: ACCEPT WITH CHANGES.** One authority with separate counter classes, not one weighted
   pool. It does **not** cover guest linear memory dynamically, compiled engines or pools; those are
   reported static baselines.
7. **Consequence.**
   - One `Lease` type is attached to the request context.
   - Charge classes are reported separately.
   - The status surface reports the static baselines next to the governed usage.

### 4. H4 — fail fast on initial resource saturation

1. **Proposal and benefit.** Reject over-budget requests promptly with `503` or `429` plus
   `Retry-After`, instead of queueing them.
2. **Evidence.**
   - (M) F3, on the s5 shape:

     | Configuration | Goodput (maximum 16/s) |
     | --- | --- |
     | 16-deep queue, one shared H2 connection | 2.0/s with immediate retries; 2.9 and 2.5/s honouring `Retry-After` |
     | Same queue spread over 16 connections | 15.2/s |
     | No queue, one shared connection | 15.6/s, with or without retry pacing, even at 5,062 rejections/s |

     The legitimate probe on a separate connection stayed at about 1 ms throughout.
   - (S) Go's h2 server returns *connection-level* flow-control credit for DATA on a closed stream,
     or after the handler has closed the body, but not stream-level credit (bundled `http2` server
     around lines 1725–1780).
3. **Queue versus fail-fast.**
   - **A queue *before* the body has arrived** retains unread bodies and lets them consume shared
     credit. That is the s5 starvation.
   - **A queue *after* the body has been fully received and charged** does not hold network credit,
     and its memory is already accounted for.
   - Therefore:
     - **initial admission** (base plus first byte reservation) is fail-fast;
     - the only wait is a short, bounded, FIFO **evaluation-slot wait**, entered only when no
       unread request-body data remains on the network: there is no body, the body has reached
       EOF, or Jul has closed it. Any evaluation while unread data remains try-acquires its slot
       and fails fast, whatever its phase. That includes phase 1 before the body, phase 2 under
       partial processing, and response phases for a response produced before the upload completed
       (Decision §3).
4. **Counterexamples investigated.**
   - **Retry storms.** At 5,062 rejections per second, goodput did not fall (F3), because
     rejection before reading the body is cheap.
   - **Unread rejected bodies.**
     - H1: Go discards up to 256 KiB and then closes the connection. This is accepted; a
       saturation 503 on H1 may cost the connection.
     - H2: the stream is reset. Connection credit is returned for discarded DATA, so siblings
       proceed (M, F3).
     - H3: quic-go stops reading the stream. Sibling isolation is **inferred** and must be verified.
   - **Starvation and fairness.** Fail-fast has no ordering; a retrier is not favoured over a new
     arrival. The bounded slot wait is FIFO (F2 test `TestFIFOBoundedWait`). Abandoned waiters are
     skipped without stranding capacity, a bug that the prototype found and fixed.
5. **Status.** `503` with `Retry-After: 1`, consistent with ADR 0017's `proxy_overloaded`. **`429`
   is rejected.** Jul has no caller-rate or caller-quota contract here: saturation is node-wide and
   is not the caller's fault.
6. **Verdict: ACCEPT WITH CHANGES.**
   - Fail-fast at initial admission.
   - A bounded wait only for evaluation slots, and only when no unread request-body data remains.
   - `503`, never `429`.
   - The s5 cause is established, and it is not retries.
7. **Consequence.**
   - A new overload reason, `inspection_overloaded`.
   - An operator caution in the ADR 0017 guide: `max_pending_requests > 0` with H2 request bodies
     can starve admitted streams on a shared connection. Its default of `0` (no queue) is already
     correct.

### 5. H5 — bound plugin-instance waiters with a fixed cap (example: 1,000)

1. **Proposal and benefit.** A finite waiter cap stops unbounded waiter accumulation.
2. **Evidence.**
   - (S) Each instance waiter already holds its prepared body: up to 1 MiB by default, and more if
     `max_request_body` is raised.
   - (S) Each waiter waits at most one call timeout.
3. **Calculation.** 1,000 waiters × 1 MiB = 1,000 MiB of retained bodies, which exceeds the 768 MiB
   envelope on its own. A count cap without a byte relationship does not bound memory.
4. **Alternatives evaluated.**
   - **Zero-wait.** Rejects transient bursts in which an instance returns microseconds later. That
     harms goodput for no memory gain, because the body is already prepared and charged.
   - **A small fixed queue (for example 16).** Bounded, but it adds a second knob that must agree
     with the byte budget.
   - **Transitive bound.** Every waiter holds a lease, so the waiter count is at most the number of
     leases, which is at most `max_buffered_bytes / (base + B)`. Waiting time stays at most one call
     timeout (existing behaviour). Retained memory is bounded by the byte ceiling because it is
     charged.
5. **Distinctions.**
   - **Initial inspection admission** is fail-fast and holds base plus bytes.
   - **Later instance acquisition** uses the existing bounded wait. The waiter holds bytes but no
     slot.
   - **Waiting while holding other reservations** is safe because no capacity cycle exists:
     instance holders never wait for bytes or slots, since `call` releases or discards the instance
     before returning (`runtime.go:669-710`).
6. **Verdict: REJECT the fixed 1,000 cap.** ACCEPT a finite bound derived transitively from the byte
   budget and the base charge, with the existing finite wait.
7. **Consequence.** No new waiter field. `jul_plugin_instance_waits_total` keeps its meaning. A new
   gauge reports active leases.

### 6. H6 — finite upload-arrival deadlines, including H3 (30 s suggested)

1. **Proposal and benefit.** Clients cannot pin preparation buffers indefinitely.
2. **Evidence.**
   - (M, F4) The current behaviour by `read_timeout` setting:

     | `read_timeout` | Stalled body | Legitimate 8 s slow upload | H3 |
     | --- | --- | --- | --- |
     | 0 (default) | Held until the client gave up (about 20 s) on H1, H2 and H3 | Succeeds | Same as left |
     | 5 s | Ended at 5 s on H1 and H2 | Also ended at 5 s | Ignored |

     Siblings on the same H2 or H3 connection were unaffected (about 2 ms).
   - (M, F4b) quic-go's `http3` writer accepts a per-stream `SetReadDeadline` through
     `http.ResponseController`. A stalled stream received 408 at 2 s while its sibling took 1 ms.
   - (S) Go's h2 supports per-stream read deadlines.
3. **Options.**
   - **A whole-exchange deadline (30 s suggested).** This breaks downloads, SSE, WebSockets and
     long-lived gRPC that pass through inspection. Rejected.
   - **Reusing `read_timeout`.** It is absolute for the whole request, configured per address,
     defaults to 0, and H3 ignores it. Not reused.
   - **An inactivity deadline alone.** A trickle of one byte just under the idle limit holds a
     1 MiB buffer for days. It does not bound holding time. Rejected as the sole control.
   - **An absolute deadline on the inspection body-arrival phase only.** Set per request or stream
     through `ResponseController` when the inspection layer starts reading the body, and restored
     when the body is complete. This bounds holding time and leaves later streaming untouched.
4. **Compatibility.** A 60 s default admits at least:

   | Body | Minimum sustained rate |
   | --- | --- |
   | WAF default 128 KiB | 2.2 KiB/s |
   | Plugin default 1 MiB | 17.5 KiB/s |

   Uploads larger or slower than that on inspected routes need a raised `body_timeout`. Uninspected
   routes, meaning those with no WAF body access and no body-reading plugin, are never affected.
5. **Applying and restoring the deadline.** `ResponseController` can set a deadline but cannot read
   the current one. Two owners can also set it concurrently: the inspection layer, and plugin
   retirement, which aborts preparations by setting a past deadline (`body.go:197-206`).
   - **One deadline owner per request.** The server entry point records the absolute read deadline
     it applied in a per-request deadline owner in the request context (`internal/server`). Every
     setter goes through that owner, including the retirement abort (I2). The deadline applied is
     the earliest of the server deadline, the active `body_timeout` deadline and any abort.
   - **An abort cannot be undone.** An abort is sticky. Restoring after the body phase, or setting
     any later deadline, never replaces it. Restore re-applies exactly the recorded server deadline,
     and only when no abort is recorded. A stricter server deadline is never extended.
   - **Narrow forwarding only.** Every response-writer wrapper between the server and the
     inspection layer forwards `SetReadDeadline` and nothing more, as the WAF interceptor already
     does (`outcomes.go:676-683`). Wrappers do not gain `Unwrap` or any other general path to the
     underlying writer, because that would let callers write or flush around response inspection.
   - If a writer returns `http.ErrNotSupported` while `body_timeout > 0` in `enforce` mode, the
     request fails closed with `500` and the metric
     `jul_inspection_body_timeouts_total{protocol, result="unsupported"}`. Observe mode records the
     metric and continues (Decision §7).
   - Tests (A10) must show that no shipped protocol path reaches that branch. Proving every H1, H2
     and H3 wrapper path remains implementation acceptance work.
6. **Verdict: ACCEPT WITH CHANGES.**
   - An absolute **body-arrival** deadline, scoped to inspection, applied per stream on H1, H2 and
     H3.
   - A default of **60 s**, not 30 s, for the margin it gives slow uploads of the default sizes.
   - An explicit `0s` disables it.
   - Not a whole-exchange deadline.
   - The guarantee states that with `body_timeout = 0s`, holding *time* is unbounded while held
     *bytes* remain bounded.
7. **Consequence.**
   - A new outcome, `body_timeout`, returning `408`. On H1 the response also carries
     `Connection: close`.
   - It is distinct from overload.
   - Whether every wrapper in Jul's chain forwards `SetReadDeadline` to the quic-go writer is
     **unverified**. It is acceptance item A10.

8. **Amendment, 2026-11-12: no record and restore (maintainer decision, I2).** Item 5's
   record-and-restore mechanism is replaced. Go's HTTP/2 server arms each stream's `read_timeout`
   timer inside the transport and provides no getter, so the server deadline cannot be recorded
   without approximating it. Restoring it would also need a zero or later deadline, and a zero
   deadline removes the deadline. The replacement keeps the intended policy:
   - **Jul never sets a future read deadline, and never clears or restores one.** During a normal
     body arrival, transport deadlines stay as the transport installed them. The `body_timeout` is an
     owner timer (`internal/reqdeadline`). When it expires, the owner applies a sticky abort, which
     is a past read deadline on the request's own stream (H2, H3) or connection (H1). A successful
     arrival only stops the timer. The effective deadline is still the earliest of the server
     deadline, `body_timeout` and any abort, and a stricter server deadline is never extended.
   - **Completion and expiry are synchronised.** Stopping the timer is not enough. An
     already-running callback that finds the phase complete or the handler returned does nothing,
     and never touches a writer after the handler returns. Completion checks the absolute expiry
     itself, so a delayed callback cannot let a late upload succeed.
   - **Failure attribution is preserved.** A body timeout, an existing server timeout, a
     retirement abort and client cancellation stay distinguishable. Only an expired inspection
     deadline answers `408`. Other deadline-related read errors keep their existing outcomes.
   - **Unsupported aborts terminate safely.** Capability is decided when the body phase starts,
     before any read and without setting a deadline. Enforce mode fails closed with `500` before
     reading. No read is left blocked on a deadline that cannot be applied. A writer that has the
     method but refuses the abort at expiry is also `unsupported`, and the request fails closed with
     `500`, not `408`. The read is still ended: on H1 by the same past read deadline applied to
     the request's connection, which the server registers, and closed only if it refuses too. On
     H2 and H3 the request body is closed. Closing an H1 body cannot end a blocked read, so an H1
     request whose connection is not registered (an embedder's own `http.Server` without
     `reqdeadline.ConnContext`) is `unsupported` before any read: `500` in enforce mode.
   - **Isolation and accounting are unchanged.** H2 and H3 abort only the affected stream. Charges
     are held until the blocked read and represented work actually end.
   - The guarantee covers Jul's own setters and the upload-admission phase. net/http itself clears
     the H1 connection deadline when it starts its post-EOF background read and on hijack. The
     owner's logical abort and expiry state are authoritative whatever the transport deadline then
     is, and an upgraded session owns its hijacked connection. When R5 is reserved, its release at
     the upgrade or stream-commit boundary is defined by the 2026-11-13 proposed amendment
     ("Long-lived exchanges"), not by the hijacked connection's lifetime.
   - Public configuration and protocol behaviour are unchanged. Wrappers still forward only
     `SetReadDeadline` (item 5), and the integration tests in A6, A7 and A10 still apply.
     Decision 8 and the unit-test list read "a retirement abort cannot be undone by completion,
     expiry or any later setter" under this amendment.

### 7. H7 — re-run the complete affected matrix

1. **Proposal and benefit.** s8, s11 and the r-series detect regressions and establish protection.
2. **Evidence (S).** Scenario definitions were resolved from the archived harness (`scenarios()`):

   | Scenario | Definition |
   | --- | --- |
   | s1/s2 | H1/H2 POST of a 16 KiB wide-JSON body with a 64 KiB response, C=16 |
   | s3 | H2, 256 KiB body, 1 MiB chunked response, C=32 |
   | s4 | s3 ramped at C=16 to 192 |
   | s5 | H2 `/admitted`, 8 active and 16 pending, 500 ms delay, C=128 |
   | s6 | H2 static 256 KiB, C=128 |
   | s8 | H1: 96 slow uploaders, 32 that cancel, 4 probes |
   | s10 | H3 normal traffic |
   | s11 | 16 KiB `items` JSON (about 742 arguments) at C=1 to 16 |
   | r1 | 16 slow uploads plus a probe |
   | r2 | Fast no-body GET |
   | r3 | Fast-body s1 |

3. **Counterexamples investigated.**
   - The names do not imply coverage. No existing scenario combines the WAF and plugins *with*
     argument amplification. None covers multiple listeners or reload overlap. s10 is the only H3
     case, and it carries normal traffic.
   - #700's T1 omitted s8 and the r-series.
4. **Verdict: ACCEPT WITH CHANGES.** The matrix is extended (§Acceptance plan), and the harness is
   promoted into maintained tooling with tests.
5. **Consequence.** The full matrix runs on the implementation. Nothing in this ADR claims it has
   run.

## Decision

### 1. One process-owned governor in `internal/inspection`

- **Ownership.** `inspection.Governor` is created once by the lifecycle registry, alongside the
  metrics registry. It outlives every generation.
- **Distribution.** Generations receive the pointer at build time
  (`internal/app/factory.go`, middleware chain).
- **Limits.** They are an immutable `Limits` value, swapped atomically at Publish.
- **Counters.** They live in the governor and are never reset by reload.

### 2. One lease per request, created by the first inspection layer that runs

- **Creation.** `governor.Admit(ctx, class)` returns a `*Lease` and attaches it to the request
  context. It runs at:
  - **every** plugin invocation, request or response phase, whether or not the plugin reads the
    body: before `p.prepare` (`runtime.go:722`) for body readers, and before `p.call` otherwise.
    Non-body plugins still allocate host memory: fetch responses up to `maxFetchResp`
    (`fetch.go:81`), request state, generated responses and replacement bodies (`abi_v2.go`); or
  - WAF `serveHTTP`, before `newTransaction` (`outcomes.go:206`), when no plugin ran first.
- **Reuse.** Inner layers call `inspection.LeaseFrom(ctx)` and reuse the lease.
- **Lifetime.** The creating layer closes the lease in a `defer` when the handler returns. By then
  every buffer and transaction it represents has been released or closed.
- **Cost-free paths.** Only requests that reach no inspection layer pay nothing. Those are requests
  on routes with neither the WAF nor a plugin, including static and cached routes.
  - The WAF and plugins wrap the route handler, including the static handler and the response cache
    (`factory.go` ~907–925). On an inspected route, static responses and cache hits therefore still
    run the WAF and plugins, and are governed like any other inspected request (A5b).
  - These paths were part of #695's exposure because upstream admission (ADR 0017) does not
    protect them.
  - The chain order is unchanged.

### 3. Counter classes and acquisition order

> **Proposed correction, 2026-11-13:** the historical phase-5 row and “No reservation”
> paragraph below do not account for audit-record construction. The proposed R5 contract in the
> dated amendment supersedes them if accepted; the original assertions are retained for history.

| Order | Class | Unit | Kind | Wait |
| --- | --- | --- | --- | --- |
| 1 | `base` | bytes (16 KiB per lease, to be calibrated in slice 1) | conservative estimate | **none** — fail-fast |
| 2 | `buffer` | bytes | exact capacity charged before allocation | **none** — fail-fast |
| 3 | `slots` | count | exact | bounded FIFO wait (`evaluation_queue_timeout`) **only when no unread request-body data remains on the network**; otherwise try-acquire, fail-fast. Phase 5 has its own rule (below) |
| 4 | `waf_state` | bytes | worst-case **reservation** taken while holding the slot, trued down after the phase | **none** — fail-fast; on failure the slot is released |

`base`, `buffer` and `waf_state` draw from the single ceiling `max_buffered_bytes`. `slots` have
their own ceiling, `max_concurrent_evaluations`.

- **No wait-for cycle.** Byte acquisition never waits. A slot holder never waits: it takes its
  `waf_state` reservation fail-fast. A waiter for a slot holds bytes but no slot. Plugin-instance
  holders never wait on the governor, because `call` returns or discards the instance before
  returning. Partial-reservation deadlock is therefore impossible.
- **Why the slot is taken before the reservation.** Reserving worst-case state first and then
  waiting for a slot would let every waiter hold about 55 MiB. Taking the slot first means at most
  `slots` worst-case reservations exist at once.
- **Queue eligibility follows unread network data, not the phase number.** A request may wait for
  a slot only when no unread request-body data remains on the network: there is no body, the body
  has reached EOF, or Jul has closed it. Otherwise it try-acquires and receives a fail-fast 503 when
  no slot is free. Waiting while a stream holds unread data would recreate the H2 flow-control
  starvation of F3. Eligibility is evaluated at each acquisition. Later phases can still have unread
  data:
  - phase 2 under `SecRequestBodyLimitAction ProcessPartial`, where the body beyond the limit stays
    on the network and is forwarded afterwards (`outcomes.go` forwards
    `io.MultiReader(rbr, origBody)`);
  - phases 3 and 4 for a response produced before the upload completed, such as an early error or
    a handler that responds without reading the whole body;
  - a request whose body a plugin replaced or rejected before EOF.

**Every engine call that can evaluate rules is covered.** Slots placed only around Jul's explicit
phase calls would leave implicit evaluations outside the limit. In Coraza v3.8.1 these entry points
can evaluate rules. Jul's call sites are in `internal/waf/outcomes.go`.

| Entry point (Jul call site) | Rules it can evaluate | Slot | `waf_state` reservation (§4) |
| --- | --- | --- | --- |
| `ProcessConnection`, `ProcessURI` (346–347) | None, but `ProcessURI` parses `ARGS_GET` | Acquired before `ProcessURI`; held through `ProcessRequestHeaders` | R1, before `ProcessURI` |
| `ProcessRequestHeaders` (361) | Phase 1 | The same slot | R1 |
| `ReadRequestBodyFrom` (373) | Phase 2, implicitly, under `SecRequestBodyLimitAction ProcessPartial` once the limit is reached, *after* reading the network inside the same call | Not used after I3 (see below) | — |
| `ReadResponseBodyFrom` (not called by Jul) | Phase 4, implicitly, under response `ProcessPartial` | Any future call site is treated like `WriteResponseBody` | R4 |
| `WriteRequestBody` | Phase 2, implicitly, under `ProcessPartial`, on the write that reaches the limit | Acquired for any write that can reach the request body limit | R2, for that write |
| `ProcessRequestBody` (416) | Phase 2; returns early if phase 2 already ran implicitly | Acquired unless phase 2 already ran | R2, unless already held |
| `ProcessResponseHeaders` (602) | Phase 3 | Acquired | R3 |
| `WriteResponseBody` (635) | Phase 4, implicitly, under `SecResponseBodyLimitAction ProcessPartial` (the `@coraza.conf-recommended` default), on the write that reaches the response limit | Acquired for any write that can reach the response body limit | R4, for that write |
| `ProcessResponseBody` (712) | Phase 4; returns early if phase 4 already ran implicitly | Acquired unless phase 4 already ran | R4, unless already held |
| `ProcessLogging` (247, deferred) | Phase 5 | Phase-5 rule below | None beyond state already held (superseded by R5 in the 2026-11-13 proposed amendment) |
| `Close` (208, 335) | None; returns the transaction to the pool | — | Releases all `waf_state` |

- **Rule.** Jul holds a slot and the phase's reservation around *every* call that may evaluate
  rules, including body writes that can trigger implicit evaluation. A body write that cannot reach
  the limit evaluates no rules: it takes no slot and is charged as `buffer` only.
- **No network read inside a slotted call.** `ReadRequestBodyFrom` reads the network and may then
  evaluate phase 2 within one call, so no slot boundary around it is correct. I3 replaces it with a
  Jul loop that has the same semantics: read a chunk from the network without a slot, under
  `body_timeout`, then pass it to `WriteRequestBody`, taking a slot only for a write that can reach
  the limit. Response writes come from the handler, not the network, but follow the same
  eligibility rule, because the request body may still be unread.
- **Phase 5 runs rules and is slotted deliberately.** `ProcessLogging` evaluates phase-5 rules and
  then writes the audit log in the same call (`transaction.go:1431-1480`). It acquires a slot under
  the same eligibility rule as phases 1–4: a bounded FIFO wait, inside the waiter cap, only when no
  unread request-body data remains, and otherwise a try-acquire. It differs in one respect: it is
  never refused or skipped. The response is already committed, and logging rules must still run.
  - **Overflow.** If no slot is obtained, phase 5 runs without one and is counted in
    `jul_inspection_unslotted_logging_total`. Unslotted phase-5 evaluations are bounded by the number
    of leases, and the counter makes them visible for sizing.
  - **Audit output.** The slot also covers the audit-log write inside `ProcessLogging`. An audit
    sink that blocks therefore holds slots. Phases 1–4 then try-acquire, or wait and time out, per the
    rule above, ending in `evaluation` rejections, and phase 5 overflows. There is no governor wait
    cycle, because no slot holder waits on the governor (A18 includes a blocked audit writer). This
    is not a liveness guarantee: a writer that never returns holds its slot and charges
    indefinitely; the 2026-11-13 proposed amendment adds a writer deadline and abandonment rule.
  - **No reservation.** Phase 5 parses no new collection and takes no new reservation. Its transient
    working set is bounded through the slot, or by leases when it overflows. (Superseded by R5 in
    the 2026-11-13 proposed amendment; the probe there shows phase-5 audit allocation is not zero.)

### 4. Byte accounting rules

- **Charge live capacity, including growth copies.** A buffer is charged for every backing array the
  request can still reach, as in the archived prototype's `ReadBounded`:
  - Before allocating a larger array, Jul charges its full capacity. While the copy runs, the old
    and new arrays are both charged.
  - The old array's capacity is refunded only after the copy, once no reference to it remains.
  - Growth therefore stays inside the budget. There is no overshoot allowance, and concurrent growth
    in many requests cannot exceed `max_buffered_bytes`. Only allocator size-class rounding falls
    outside the charge (§10).
  - #703's initial allocation is kept (`body.go:67-80`). It is `min(32 KiB, Content-Length + 1)`
    when the declared length is within the layer limit, and `min(512 B, limit + 1)` otherwise. A
    stalled upload therefore holds only a small charged buffer, and later doublings are charged one
    step at a time.
- **Capacity stays charged until the array is released.** Shortening a slice does not free its
  backing array, so a body shorter than its capacity keeps the full capacity charged. A refund
  happens only when the array becomes unreachable, or when a separately charged compact copy
  replaces it: the copy is charged first, and the original is refunded after it is dropped.
- **Dishonest or unknown lengths.**
  - A plugin body whose `Content-Length` exceeds the plugin's `max_request_body` is refused before
    any charge. A body that grows beyond the limit is refused when it crosses it. Both keep #703's
    existing plugin outcome: a `500` with preparation-failure reason `too_large`. This ADR does not
    change that status.
  - The WAF request body limit keeps its existing `413` (`OutcomeRejectedBodyLimit`).
  - A body shorter than its declared length simply ends, and its capacity stays charged as above.
- **Arithmetic.** It is saturating and overflow-checked in `int64`: a charge whose addition would
  overflow fails as over-budget. Refunds never take a class below zero, and an attempt to do so is
  a bug that panics in tests.
- **Buffered bytes (`B_buf`).** Every byte Jul passes to `WriteRequestBody` is charged as `buffer`
  at 2×, to allow for the buffer's own growth, up to the compiled in-memory limit. With CRS,
  `@coraza.conf-recommended` sets that limit to 128 KiB. Without CRS, Coraza defaults it to the
  request body limit, which Jul lowers only for limits below 128 KiB (`firewall.go:91-95`). If Jul
  cannot read the compiled value, every written byte is charged as buffered. Bytes spilled to a
  temporary file are disk and are not charged as `buffer`.
- **Total processed bytes (`B_total`).** Spill does not remove parsing cost. `B_total` is every
  request body byte written to Coraza, including spill, and is at most the request body limit.
  - The urlencoded, JSON and raw body processors copy the *whole* body, spill included, into a
    `strings.Builder` (`bodyprocessors/json.go:27-28`). Extracted values may reference that copy
    until `tx.Close`.
  - Multipart reads non-file parts fully and streams file parts to temporary files. XML is decoded
    as a stream.
  - F1b's 1 MB JSON row mostly spilled and still retained about 39× its whole size (H2).

  Every per-byte WAF term therefore uses `B_total`, not `B_buf`.
- **WAF state is a calibrated estimate, not a proven bound.** The estimate is fitted to the F1b
  shapes and checked against them. It is a conservative engineering estimate of retained memory,
  not a derived upper bound on what Coraza can retain. The I3 calibration test makes it a
  maintained contract for the pinned Coraza and CRS. §10 states its guarantee for governed
  (charged) bytes, with WAF state counted at the estimate.

  The per-term estimate is `64 KiB + 56 KiB × A + 48 × B + 48 × H`, where `A` is the arguments
  held, `B` the body bytes processed and `H` the request header and cookie bytes. The header factor
  of 48 is **inferred** and must be calibrated in I3.
- **Cumulative reservations.** Amplification happens *during* a phase, so a post-phase check would
  come too late. Jul reserves the worst case before each rule-evaluating call (§3), while holding
  the slot, and trues it down after the call. Coraza limits each argument collection separately
  (`checkArgumentLimit`, `transaction.go:802-845`): `ARGS_GET`, `ARGS_POST` and `ARGS_PATH` can each
  reach `ArgumentLimit`, and `ResponseArgs` has its own limit. Reservations therefore accumulate per
  transaction. A later phase adds to what earlier phases hold, and nothing is released before
  `tx.Close` except by the true-down of that phase's own reservation.

  | Reservation | Taken before | Worst case reserved | Trued down to |
  | --- | --- | --- | --- |
  | R1 | `ProcessURI` (phase 1) | `64 KiB + 56 KiB × min(ArgumentLimit, URL query pairs) + 48 × H` | `64 KiB + 56 KiB × A_get + 48 × H` |
  | R2 | The first call that can evaluate phase 2 | `56 KiB × ArgumentLimit` for `ARGS_POST`, plus `48 × B_total` | `56 KiB × A_post + 48 × B_total` |
  | R3 | `ProcessResponseHeaders` (phase 3) | `48 × response header bytes` | Unchanged: the headers are known before the phase |
  | R4 | The first call that can evaluate phase 4 | `56 KiB × ArgumentLimit` for `ResponseArgs`, plus `48 × B_resp`, where `B_resp` is the response bytes written to Coraza (at most the response body limit) | `56 KiB × A_resp + 48 × B_resp` |

  - **`ARGS_PATH` is an asserted invariant.** Only `AddPathRequestArgument` populates `ARGS_PATH`,
    and Jul never calls it, so R2 reserves no `ARGS_PATH` term. The calibration test asserts that
    `ARGS_PATH` stays empty, so a change that populates it fails CI until R2 gains its own
    `56 KiB × ArgumentLimit` term and the minimum-budget rule includes it.
  - **Counts.** The held counts are `Variables().ArgsGet()`, `ArgsPost()` and `ResponseArgs()`
    `.FindAll()`, read through Coraza's experimental `plugintypes.TransactionState`. If the type
    assertion fails, every reservation stays at its worst case.
  - **Size.** At the default `ArgumentLimit` of 1000, a transaction with both query and body
    arguments can reserve about 112 MiB at phase 2 (R1 + R2), plus its body and header terms. The
    minimum-budget rule (§7) uses this figure.
  - **No later lowering.** F1b shows retention falls after response headers, because Coraza clears
    its transformation cache at each phase. R2 is still not lowered after phase 3: the estimate is
    calibrated at the phase-2 peak, and a lower post-phase-3 factor would need its own calibration
    and review.
- **Calibration (I3).** The test pins the API and every constant, and fails when measured
  retention exceeds the estimate. It covers:
  - query, urlencoded, JSON, XML, multipart and raw bodies;
  - bodies below, at and above the in-memory limit, including spill;
  - mixed query and body arguments, with `ARGS_GET` and `ARGS_POST` each at `ArgumentLimit`, and
    `ARGS_PATH` asserted empty;
  - partial processing on request and response;
  - large headers and cookies;
  - JSON and XML responses.
- **Plugin host allocations.**
  - Fetch responses are charged before reading. When the length is unknown, the full
    `maxFetchResp` is pre-charged.
  - Request state, generated responses and replacement bodies are charged in the host function
    before allocation.
  - Temporary guest-to-host copies are charged as well. One example is `write_response_body`, which
    copies guest memory and then appends to the host buffer.
- **Response buffers.**
  - Each subscribed plugin's nested response layer charges `buffer` before `make` or `append`
    (`phase.go:171-190`). The charges add up across nested subscriptions.
  - WAF response buffering charges 2× the bytes before `WriteResponseBody`, for `bytes.Buffer`
    growth.
- **Logical, not heap, retention.** A charge represents memory that the request can still reach.
  `tx.Close` returns the transaction to a `sync.Pool` with its maps and buffers reset but their
  backing capacity kept. That pooled capacity is process headroom (§10 exclusions), not governed
  usage. The acceptance plan checks it with a post-burst heap test (A15).

### 5. Overload, failure and cancellation contracts

| Event | Client | gRPC | Reason or outcome | Body handling |
| --- | --- | --- | --- | --- |
| Admission (`base` or first `buffer` charge) fails | `503`, `Retry-After: 1` | `UNAVAILABLE` | `inspection_overloaded` (metric reason `bytes`) | Not read. H1: the response carries `Connection: close`, so net/http does not first drain the unread body, which a stalled upload would delay indefinitely (amended in I2 review; the original text assumed the drain was bounded). H2/H3: stream reset; siblings unaffected |
| A growth charge fails mid-body | `503`, `Retry-After: 1` | `UNAVAILABLE` | `inspection_overloaded` (`bytes`) | Partial body dropped; charge released. H1: `Connection: close`, as above |
| Worst-case `waf_state` reservation fails before a phase | `503`, `Retry-After: 1` | `UNAVAILABLE` | `inspection_overloaded` (`waf_state`) | Slot released first. The phase does not run, so its amplification never happens. The transaction is closed on its owning goroutine. The request is not forwarded, so no verdict is skipped |
| Slot wait times out, the queue is full, or no slot is free while unread request-body data remains (phases 1–4) | `503`, `Retry-After: 1` | `UNAVAILABLE` | `inspection_overloaded` (`evaluation`) | Any charged body is released. An unread body is handled as for admission failure |
| Plugin response buffer charge fails | Existing subscription policy for `bodyTooLarge` (ADR 0020) | — | `jul_plugin_response_body_unavailable_total{reason="budget"}` | Passthrough or fail-closed per subscription |
| WAF response buffer or slot unavailable (phases 3–4) | `503` (headers not yet sent while buffering) | `UNAVAILABLE` | `inspection_overloaded` | Fails closed: overload never skips response inspection. Phase 5 is never refused (§3) |
| Body-arrival deadline | `408` (H1 adds `Connection: close`) | `DEADLINE_EXCEEDED` | `body_timeout` | Read stopped per stream |

**Kept distinct from these events:**

- WAF security interruption (`403` or the configured block status);
- WAF body limit (`413`);
- plugin preparation failure (existing `jul_plugin_request_body_failures_total` reasons);
- guest trap (`500`);
- backend failures (ADR 0017).

Overload never weakens a security decision.

**Cancellation:**

- **A blocked upload read** ends on `body_timeout`, on client reset, or on the plugin retirement
  abort (`body.go:176-205`, which already uses per-stream read deadlines). Charges are released when
  the read returns.
- **Preparation** is cancelled by the same mechanisms. Its bytes are released when
  `prepareRequestBody` returns.
- **Coraza evaluation cannot be interrupted** (source-proven). The slot and `waf_state` stay charged
  until the call returns. Jul then checks `r.Context()` between phases and closes the transaction on
  the owning goroutine. Concurrent `tx.Close` is never used, and no worker is ever detached.
  - **Residual risk:** an abandoned evaluation runs to completion. The bound is
    `max_concurrent_evaluations` concurrent evaluations × the per-evaluation duration. That duration
    was measured at about 0.3 s for 1000 arguments, about 0.6 s for 128 KiB of wide JSON (the default
    limit) and 6.5 s at a raised 1 MB limit.
  - Abandoned work therefore cannot accumulate beyond the slot count.
- **Guest calls** are interruptible: wazero closes the module when the context is done. The instance
  is discarded and its slot freed, which is existing behaviour.
- **Critical invariant:** no cancellation, timeout or handler return releases a charge while the
  buffer, transaction or evaluation it represents still exists. Leases close in a `defer`, after
  `tx.Close` and after every buffer reference has been dropped.

### 6. Long-lived exchanges

- **A WAF-inspected long-lived response** keeps `waf_state` charged until `tx.Close`. This applies to
  SSE, streaming gRPC and hijacked WebSockets. The state really is retained, so this is correct.
  Operators see it as `waf_state` in status.
  - F1b shows retained state falls after response headers (wide JSON drops from 39× to 22×), because
    Coraza clears its transformation cache at the start of the next phase.
  - The charge does not follow that reduction: R2 is not lowered after phase 3 (§4).
- **A prepared plugin body** stays charged until the lease closes. The request may still replay it,
  so an early refund would be unsafe.

### 7. Configuration

```toml
[global.inspection]
mode = "enforce"                 # "enforce" (default) | "observe"
max_buffered_bytes = "256m"      # Size, > 0; default 256 MiB (suffixes b, k, m, g)
max_concurrent_evaluations = 0   # int, 0 = auto (GOMAXPROCS); 1..1024 otherwise
evaluation_queue_timeout = "1s"  # Duration, 0s..10s; 0s = no wait (pure fail-fast)
body_timeout = "60s"             # Duration; omitted = 60s; explicit "0s" disables
max_response_header_bytes = "64k" # Size; positive; accounted header ceiling for R3
```

Example for the 2 vCPU / 768 MiB envelope (inferred sizing; it must be validated by A1–A3):

```toml
[global.inspection]
max_buffered_bytes = "192m"
max_concurrent_evaluations = 2
```

**Field semantics:**

| Field | Semantics |
| --- | --- |
| `mode` | `enforce` (default) or `observe`. Observe charges and reports so that operators can size before enforcing; its behaviour for each control is defined below. There is no `off`: accounting is cheap, and an unmeasured process cannot be sized. |
| `max_buffered_bytes` | Required to fit the largest single-request worst-case charge of any inspected route: `base`, plugin request/response buffers and replacement copies, WAF request/response bodies, plugin state, and R1 + R2 + R3 + R4 for that route's chain (§Minimum-budget rule). A configuration with no inspected route has no minimum. |
| `max_response_header_bytes` | Positive, default `64k` (64 KiB), configurable upward and not disableable. This is the complete R3 accounting measure for one final response, not wire bytes or a mathematical memory bound: for every decoded header field-value occurrence, `H` adds `len(name) + len(value) + 64` bytes. Repeated fields are counted separately. The `64` bytes is a fixed per-occurrence accounting allowance; the WAF multiplier remains a calibrated estimate. Startup's known R3 term is `48 × max_response_header_bytes` (3 MiB at the default). Runtime reserves `48 × actual H` before adding any response header to Coraza, then retains that R3 reservation for the transaction lifetime; it does not reserve the maximum for every lease. Raising this ceiling does not raise `max_buffered_bytes`; an unaffordable actual reservation still produces the separate governor `503`. |
| `max_concurrent_evaluations` | `0` resolves to `GOMAXPROCS` at Prepare. The effective value is reported. |
| `evaluation_queue_timeout` | Waiters are also capped internally at `16 × slots`. That count is a constant, not a field. |
| `body_timeout` | Applies only while an inspection layer reads a request body. Downloads, streaming responses and post-inspection reads are never affected. |
| All | Process-wide. Not overridable per listener or route. |

### Minimum-budget rule

The R3 bound is `max_response_header_bytes`, default `64k`, and is measured at
the shared Coraza response-header ingestion point immediately before
`ProcessResponseHeaders`. It applies to final response sources that pass this
boundary, regardless of whether they originate upstream, in a plugin, or in
Jul. The request-side `servers[].max_header_bytes` remains unrelated.

The R3 measure counts decoded name/value bytes and 64 bytes for each field
occurrence, including repeated values. It is not compressed HTTP/2 or HTTP/3
wire size. Informational responses other than 101 bypass this existing phase-3
boundary and are outside R3. Trailer declarations visible at the boundary are
counted as ordinary fields; trailer values added after phase 3 are outside R3
and are not inspected by this phase. Headers already allocated by a transport
or producer are not retroactively bounded; this governor charge represents
Coraza's retained transaction state, not total process memory.

In enforce mode, an over-ceiling response fails closed with an empty 500 before
any response header is added to Coraza or committed to the client. It is not
truncated and inspection is not bypassed. A governor reservation failure for
an in-ceiling response remains a 503 with `Retry-After`. In observe mode, the
ceiling and byte budget produce would-reject observations, but inspection
continues without a new governor-driven 500 or 503. Actual R3 reservations use
the measured response charge, never the configured maximum.

The 64 KiB default is an initial compatibility-sensitive policy, not an
experimentally established optimum. The unmerged I4 branch now implements the
R3 runtime check and reservation, but before shipping this response-path
enforcement, qualify representative responses, especially repeated
`Set-Cookie` fields and authentication flows, and complete the remaining I4
protocol and ownership tests. This is not a release or qualification claim.

**Route minimum.** Each route's static worst-case single-request charge is the
sum of the terms below, and `max_buffered_bytes` must be at least the largest
one over all inspected routes. A route with neither a WAF nor a plugin acquires
no lease and needs nothing, so an uninspected configuration has no minimum.

| Term | Counted when | Bytes |
| --- | --- | --- |
| `base` | The route is inspected | 16 KiB |
| Plugin request bodies | Each server, location or handler plugin importing `read_request_body` | `Σ(L + 1) + max(L + 1)` over their `max_request_body` values: every prepared body stays reachable, and the largest holds both arrays during its final growth copy |
| Plugin request state | `set_request_state` imported | 4 KiB per plugin |
| Plugin generated response | `write_response_body` imported | `3 × max_response_body` |
| Plugin fetch | `fetch = true` and `fetch` imported | `max_fetch_response + 1` plus the guest memory limit |
| Plugin response subscription | A jul-abi/v2 middleware exporting `handle_response` | `2 × max_response_body`, or `3 ×` when it imports `resp_body_replace` |
| WAF request buffer | A WAF guards the route | `2 × min(in-memory limit, request body limit)`, or `2 ×` the request body limit when the in-memory limit is unknown |
| WAF request read buffer | A WAF guards the route | `min(32 KiB, request body limit)`: the scratch buffer of Jul's request-body read loop (I3a) |
| R1 | A WAF guards the route | `64 KiB + 56 KiB × ArgumentLimit + 48 × (H + 4 KiB)`, where `H` is the listener's `max_header_bytes` (default 1 MiB) and 4 KiB is Go's header slack |
| R2 | A WAF guards the route | `56 KiB × ArgumentLimit + 48 ×` request body limit |
| R3 | A WAF guards the route | `48 × max_response_header_bytes` |
| R4 | A WAF guards the route | `56 KiB × ArgumentLimit + 48 ×` response body limit |
| WAF response buffer | A WAF guards the route | `2 ×` response body limit |

- **Inputs.** WAF limits are read from the compiled engine, after directives and Jul's
  `request_body_limit`, so `SecRequestBodyLimit`, `SecRequestBodyInMemoryLimit`,
  `SecResponseBodyLimit` and `SecArgumentsLimit` are honoured. Plugin terms come from the module's
  imports and configured limits. R4 and the response buffer are counted for every WAF route because
  `ctl:responseBodyAccess` can enable response inspection per transaction.
- **Per-transaction raises.** `ctl:requestBodyLimit` and `ctl:responseBodyLimit` raise only the
  transaction's limit check. Coraza creates each transaction's body buffers with the compiled
  limits and refuses bytes beyond them, so the compiled limits used here also bound buffered and
  parsed bytes under a raise. A raised body larger than the compiled limit fails closed (request
  `400` `request_body_read_error`; response `response_body_buffer_error`) rather than growing.
  `TestCtlBodyLimitRaiseCannotExceedCompiledBuffers` pins this for every request and response
  write entry point, so a Coraza change that breaks it fails CI. No `ctl` action changes
  `ArgumentLimit`.
- **Where it runs.** The handler build shared by startup, reload and admin apply computes the
  minimum. The server attaches it to the limits it publishes, and the governor refuses
  enforce-mode limits below it. `jul check` and `jul --check` compile the effective WAF policies
  offline and apply the same calculation. They compile each plugin module and negotiate its ABI
  for the same facts, but never instantiate it, so no guest start function or `_initialize` runs
  during a check. In `enforce` a shortfall is a configuration
  error naming the route, the required bytes and the largest terms; in `observe` it is a warning
  (a log line, stderr, or `warnings` in `jul check -json`).
- **Default fit.** Jul's default CRS route (128 KiB request body limit, 128 KiB in-memory limit,
  512 KiB response body limit, `ArgumentLimit` 1000, 1 MiB request headers, 64 KiB response-header
  ceiling) needs 258,654,208 B (about 246.7 MiB, including the 64 KiB fixed R5 term and the
  32 KiB read buffer added in I3a), which fits the 256 MiB default with about 9.3 MiB of headroom.
  With otherwise default limits, a WAF `request_body_limit` above 334,848 B (327 KiB) exceeds the
  default budget: 1 MiB needs 302,694,400 B. Adding a plugin that imports `write_response_body`
  (default 8 MiB response body, 24 MiB charged) to a default WAF route needs 283,820,032 B. Several shipped WAF configurations therefore set `max_buffered_bytes = "512m"`
  explicitly; whether the default itself should change remains an I6 acceptance question.
- **Scope.** The rule guarantees that one worst-case request on any route can be admitted. It does
  not size concurrency; that is the runtime budget's job.

**`mode = "observe"` for each control.** Observe introduces no governor-driven `503`, `408` or
`500`. It preserves every existing security decision, WAF and plugin limit, and server timeout.

| Control | `enforce` | `observe` |
| --- | --- | --- |
| Byte admission and growth (`base`, `buffer`) | Fail-fast `503` | Always charged, never refused. A charge that would exceed the limit counts `jul_inspection_rejections_total{reason="bytes", mode="observe"}` |
| `waf_state` reservation | Fail-fast `503` before the phase | Always charged, never refused; counted with `reason="waf_state"` |
| Evaluation slots, phases 1–4 | Bounded wait or try-acquire, then `503` | Not enforced: no wait and no limit. Active evaluations are still counted. An acquisition that enforce would have refused, or made wait, counts `reason="evaluation"` |
| Phase-5 slot | Bounded wait or try-acquire; on failure runs unslotted and is counted, never refused | Not enforced: no wait and no limit |
| `body_timeout` | Deadline applied; `408` on expiry | Not applied. A body read that outlasts `body_timeout` counts `jul_inspection_body_timeouts_total{result="would_timeout"}` once per request |
| Unsupported read deadline | Fails closed with `500` | Counted as `result="unsupported"`; the request continues |
| Minimum-budget validation | Configuration error | Warning |
| Existing outcomes: WAF interruptions, WAF `413`, plugin `500`s, `read_timeout`, ADR 0017 admission | Unchanged | Unchanged |

**Enforcement is the default.** Without it, #700's protection is opt-in and the shipped default stays
exposed. The default applies once implementation acceptance (A1–A18, plus A19–A22 for any control
added by the 2026-11-13 proposed amendment) passes, and the 256 MiB value
itself must be validated by A1–A4.

**Compatibility:**

- **New 503s.** Upgrading can add overload 503s on saturation that previously risked out-of-memory
  kills.
- **New 408s.** Upgrading can add 408s for inspected uploads slower than 60 s.
- **New configuration validation failures.** In `enforce` mode, the minimum-budget rule can reject
  an existing configuration on upgrade, for example when a large plugin `max_request_body` or WAF
  limit makes the worst-case chain exceed the 256 MiB default. The error names both fields.
  Operators raise `max_buffered_bytes`, lower the per-route limits, or select `observe` while
  sizing.
- **Migration path.** Run `mode = "observe"` and watch peak usage and the would-reject counters, then
  enforce.
- **Release notes** must say all four.

The maintainer recorded the default, `body_timeout` and placement decisions on 2026-11-10
(§Maintainer decisions).

### 8. Lifecycle under reload (ADR 0011)

- **Prepare.** Validate the new `[global.inspection]` and resolve `auto` against the current
  `GOMAXPROCS`. Construct no state.
- **Publish.** Atomically swap the `Limits` pointer.
  - Existing leases, charges and slot holders are untouched.
  - **Increase:** new admissions see more capacity immediately, and waiters are woken.
  - **Decrease below current usage:** existing work is never revoked. Charges and slots already
    held may drain above the new limit. No acquisition that would exceed the new limit succeeds:
    byte charges, including growth of an in-flight buffer, fail fast, and slot acquisitions wait or
    fail per §3. Status reports `draining = true`.
  - **Changing `mode`:** takes effect for new decisions only.
- **Abort.** No effect, because Prepare constructed no state.
- **Rollback.** An ordinary Publish of the previous limits.
- **Retire.**
  - In-flight requests of a retiring generation keep their leases on the same governor until they
    complete.
  - Retirement neither refunds nor resets anything.
  - Retirement's existing plugin preparation abort (`body.go:197`) releases its bytes when the read
    returns.
- **Listener removal** behaves like Retire for that listener's in-flight requests.
- **A semantic no-op reload** publishes equal limits, which is a no-op.
- **Shutdown** drains leases as requests finish. The governor is collected with the process.
- **Invariant:** no reload creates a governor, resets a counter or adds capacity beyond the
  published limit, so reload cannot multiply capacity. This is tested under overlapping generations
  (A9).

### 9. Observability and operability (ADR 0014)

**Metrics.** All are new. Labels are bounded closed enums. No existing metric changes meaning.

| Metric | Type | Labels |
| --- | --- | --- |
| `jul_inspection_bytes` | gauge | `class` = `base`, `buffer`, `waf_state` |
| `jul_inspection_bytes_limit` | gauge | — |
| `jul_inspection_leases` | gauge | — |
| `jul_inspection_evaluations` | gauge | `state` = `active`, `waiting` |
| `jul_inspection_evaluations_limit` | gauge | — |
| `jul_inspection_rejections_total` | counter | `reason` = `bytes`, `waf_state`, `evaluation`; `mode` = `enforce` (rejected), `observe` (would reject) |
| `jul_inspection_body_timeouts_total` | counter | `protocol` = `h1`, `h2`, `h3`; `result` = `timeout`, `would_timeout`, `unsupported` |
| `jul_inspection_unslotted_logging_total` | counter | — (phase-5 evaluations that ran without a slot, §3) |
| `jul_inspection_abandoned_evaluations` | gauge | — (counts evaluations still running whose request context is done) |

Existing metrics gain at most new closed values:

- `jul_plugin_response_body_unavailable_total` gains `reason="budget"`;
- the WAF transaction outcome gains `rejected_overload` (ADR 0031 taxonomy).

**Runtime API.** `GET /api/v1/status` gains an `inspection` object containing:

- `mode`;
- configured limits and effective limits (`max_concurrent_evaluations` resolved from `auto`);
- usage per class, leases, active and waiting evaluations, and `draining`;
- static baselines:
  - `plugin_guest_max_bytes` = Σ `max_instances × memory_limit` over loaded plugins and generations;
    this is permitted capacity, not actual guest-memory allocation; live instance counts are
    reported separately, while actual allocation remains explicitly unknown unless measured;
  - `evaluation_headroom_estimate_bytes` = slots × the worst-case evaluation working set.
    Until that working set is calibrated, the API reports this estimate as unavailable rather than
    as a zero-valued measurement.

It is read-only and uses the existing status read permission.

**CLI.** The existing status command renders the same object. No new command.

**Console.**
- The existing status or overview surface gains a read-only "Inspection resources" card showing
  configured and effective limits, usage, draining and baselines.
- Editing goes through the existing configuration editor tier.
- In `config_authority = "file_owned"` mode, mutation is refused exactly as for every other field
  (ADR 0019).
- No Console redesign.

**Logs.** A rate-limited warning on the first rejection per interval, carrying class and limit. It
never contains paths, hosts or bodies.

### 10. Guarantee and exclusions

**Guaranteed in `mode = "enforce"`**, once implemented and verified by A1–A18 (and, for R5, spool
and guest capacity under the 2026-11-13 proposed amendment, by A19–A22):

1. Governed (logical) bytes stay at or below `max_buffered_bytes`. The only exception is work
   already charged when the limit is lowered, which may drain above the new limit (§8). Growth
   copies are charged, so there is no growth overshoot. Only allocator size-class rounding is
   outside the charge. Governed bytes are:
   - every prepared plugin body, at its live capacity;
   - every Coraza in-memory request body (`B_buf`), charged at 2×;
   - every plugin and WAF response buffer;
   - guest replacement bytes;
   - per-lease base charges;
   - plugin fetch responses, request state, generated bodies and temporary host copies;
   - the WAF transaction-state estimate, reserved cumulatively before each rule-evaluating call and
     trued down after it (§4). It is counted at its calibrated estimate; actual retained memory can
     differ (see below).
2. Concurrent synchronous Coraza evaluations of phases 1–4, and slotted phase-5 evaluations, stay at
   or below `max_concurrent_evaluations`. This includes implicit evaluations inside body writes (§3).
   Phase-5 evaluations that find no slot run unslotted; they are bounded by leases and counted in
   `jul_inspection_unslotted_logging_total`.
3. Evaluation waiters stay at or below `16 × slots`, each waiting at most
   `evaluation_queue_timeout`. Plugin-instance waiters are bounded by leases and each waits at most
   one call timeout.
4. With `body_timeout > 0`, every inspection body-arrival phase ends within `body_timeout`, per
   stream, on H1, H2 and H3. Today this is applied on plugin routes only; WAF-only routes are
   covered once I3a ships the Jul-owned request-body read loop, and I3a must prove the H3 path
   (including deadline forwarding through Jul's HTTP/3 wrappers) before this item is claimed.
5. A charge is released only when the request can no longer reach the represented memory and
   the work is finished. Reload never multiplies capacity.

**Not guaranteed:**

- **Total process memory and the cgroup limit.** Operators must size for these. Static baselines
  are reported:
  - compiled CRS and rules, per live generation;
  - plugin guest pools, up to `max_instances × memory_limit`;
  - Go runtime, GC headroom, and pool retention, including reset Coraza transactions whose backing
    capacity stays in `sync.Pool` (recommend `GOMEMLIMIT`);
  - net/http and quic-go connection and stream buffers (bounded by protocol settings and
    connection limits);
  - reload preparation, which briefly holds a second compiled generation;
  - Coraza temporary-file spill, which is disk (its parsing cost is charged through `B_total`);
  - transient evaluation working set beyond retained state, which is bounded only through slots.
- **WAF state is an estimate.** It is calibrated per pinned Coraza and CRS. A future engine, a rule
  set or a request shape outside the calibration set can exceed it until recalibrated.
- **Holding time is unbounded with `body_timeout = 0s`.** Held bytes remain bounded.
- **Uninterruptible evaluation** continues after cancellation, bounded by slot count × the
  per-evaluation duration.
- **Observe mode** provides no protection.

### 11. Ownership and lifecycle matrix

| Phase/resource | Scope and owner | What is charged | Acquire point | Release condition | Cancellation/retirement |
|---|---|---|---|---|---|
| Governor | Process; lifecycle registry | — (holds limits and counters) | Process start | Process exit | Survives every reload; limits swapped at Publish |
| Lease base | Request; first inspection layer | `base`, 16 KiB (to be calibrated) | Before any plugin invocation (`p.prepare` or `p.call`), or before `newTransaction` | `defer` in the creating layer after the inner handler returns | Released only on handler return; cancellation does not shorten it |
| Plugin prepared body | Request; plugin layer | `buffer`: live capacity, with old and new arrays both charged during growth | Before each growth in `readBounded` | Lease close: request completion, since the body survives instance release | Read aborted by deadline, reset or retirement; charge released when the read returns |
| Plugin instance wait | Plugin, per generation | Nothing new (holds lease and bytes) | `acquire` | Instance obtained or call timeout | Context done ends the wait; bytes stay with the lease |
| Guest call and linear memory | Plugin pool, per generation | Static baseline (reported) | — | Pool retirement | Interruptible; instance discarded |
| Guest replacement bytes, fetch responses, request state, temporary copies | Request; invocation | `buffer` | Host function, before allocation or copy | Temporary copies when they are dropped; the rest at lease close | Released with the lease |
| WAF transaction state | Request; WAF layer | `waf_state`: cumulative R1–R4 reservations, each trued down to the held-argument estimate (§4) | Before each rule-evaluating call, while holding the slot | `tx.Close` | `tx.Close` only on the owning goroutine after the synchronous call returns |
| WAF request buffer | Request; WAF layer | `buffer`: 2× `B_buf`, up to the in-memory limit | Before each `WriteRequestBody` chunk in Jul's read loop | `tx.Close` | Read stopped by deadline; charge held until `tx.Close` |
| WAF evaluation (phases 1–5) | Process; slots | `slots` plus the phase's `waf_state` reservation | Every rule-evaluating call (§3). Try-acquire while unread request-body data remains; otherwise a bounded FIFO wait. Phase 5 is never refused: without a slot it runs unslotted and is counted | The synchronous call returns; the reservation is then trued down | Not interruptible; stays charged until return |
| Plugin response buffer | Request; response layer | `buffer` | Before `make` or `append` | Decision made and buffer dropped, or lease close | Released at lease close |
| WAF response buffer | Request; WAF interceptor | `buffer` | Before `WriteResponseBody` | `tx.Close` | Held until `tx.Close` |
| Long-lived exchange after inspection | Request | Retained `waf_state` and prepared body | — | `tx.Close` and lease close | Charged for the exchange lifetime |
| Compiled CRS and rules | Generation | Static baseline | Generation build | Generation retire | Two generations during overlap |
| Coraza tx pool and GC retention | Process (runtime) | Excluded headroom | — | GC | — |

## Failure and recovery examples

1. **s11 burst of 991-argument requests at C=16**, with an illustrative 128 MiB budget and 2 slots.
   - Two phase-2 evaluations run, each holding a worst-case reservation of about 56 MiB. Others
     wait up to 1 s, FIFO. Their bodies are already received and charged.
   - After a phase, the reservation is trued down to about 55 MiB for 991 arguments. That stays
     charged until the echo returns.
   - About two such requests fit in 128 MiB. The next reservation fails before its phase runs, so
     no amplification happens, and the request receives a 503.
   - Clients honouring `Retry-After` recover when `waf_state` drains.
   - Without the governor, the same burst killed the process at c8.
2. **Slow-upload attack, 96 × 64 KiB at 16 KiB/s** (s8 shape).
   - Each upload holds 64 KiB plus base until it completes, or for at most 60 s.
   - New uploads receive a 503 once the budget is full.
   - Bodiless and uninspected requests are unaffected.
3. **Operator lowers `max_buffered_bytes` from 256 to 96 MiB while 200 MiB is in use.**
   - Status shows `draining = true`.
   - New admissions receive 503 until usage falls below 96 MiB. In-flight requests keep what they
     hold, but a growth charge that would exceed 96 MiB fails like any new acquisition.
   - Rolling back restores 256 MiB at once.
4. **Client cancels during a 6 s phase-2 evaluation of 1 MB JSON** (raised limit).
   - The slot stays held for the remaining evaluation.
   - `jul_inspection_abandoned_evaluations` is 1.
   - On return, Jul sees the cancelled context, closes the transaction and releases everything.
5. **Reload overlap.**
   - Old-generation SSE streams keep `waf_state` charged.
   - The new generation shares the same ceiling, so the total is still at most the published
     `max_buffered_bytes`.

## Consequences

- Inspection gains one admission point, one context lookup per inner layer and a few atomic or
  mutex operations per charge. Uninspected paths pay nothing.
- Overload becomes fast 503s instead of out-of-memory kills.
- Throughput on unsaturated traffic must stay within 10% of `858590dc` (A13).
- Every inspected request also acquires a phase-5 slot, and a blocking audit sink now holds slots.
- A new maintenance obligation: recalibrate the WAF state constants on every Coraza or CRS bump. The
  CI calibration test enforces it.
- The ADR 0017 guide gains the `max_pending_requests` H2 body caution.

## Alternatives considered

| Alternative | Outcome |
| --- | --- |
| Per-listener or per-generation budgets | Rejected: they multiply the ceiling (H1) |
| A single weighted pool | Rejected: it mixes exact bytes, estimates and CPU (H3) |
| Slot-only protection | Rejected: it leaves retained state, prepared bodies and response buffers unbounded (H2) |
| A fixed 1,000 plugin waiter cap | Rejected: 1,000 × 1 MiB exceeds the envelope (H5) |
| A pre-body admission queue | Rejected: it causes s5 starvation (H4) |
| `429` for overload | Rejected: there is no caller-quota contract (H4) |
| A 30 s whole-exchange deadline, or reusing `read_timeout` | Rejected: it breaks streaming, is per address, and H3 ignores it (H6) |
| A detached evaluation worker, cancelled through concurrent `tx.Close` | Rejected: unsafe (Coraza `Transaction` is not concurrency-safe), and it escapes accounting |
| Automatic cgroup detection or adaptive tuning | Deferred: it adds platform coupling, and fixed limits with observe mode are sufficient and testable |
| Engine-side mitigation (Coraza dropping caches after phase 2) | Deferred: requires an upstream change; the governor remains necessary for retained arguments |
| Charging a multiple of the body for the WAF | Rejected: the argument count dominates and query arguments carry no body |

## One-way doors

- The public names and placement of `[global.inspection]` fields, and the `mode` values.
- The new reason, outcome and metric names: `inspection_overloaded`, `body_timeout`,
  `rejected_overload`, `jul_inspection_*`.
- `503` (not `429`) for inspection overload, and `408` for body-arrival timeout.
- Default-on enforcement with the stated defaults.
- Process-wide scope. Listener sub-quotas can be added later only as fractions of the one ceiling.

## Uncertainties

- **WAF state constants.** They come from one arm64 host on Coraza v3.8.1 with the shipped CRS.
  amd64, other rule sets and `SecArgumentsLimit` above 1000 need recalibration.
- **Experimental Coraza API.** The argument count depends on the experimental
  `plugintypes.TransactionState`. The fallback is the worst case.
- **H3 deadline reach.** Whether every wrapper in Jul's writer chain forwards `SetReadDeadline` to
  quic-go's writer is unverified (A10). A wrapper that does not must gain narrow `SetReadDeadline`
  forwarding, never `Unwrap` (H6 item 5).
- **H3 sibling isolation after a rejection without reading the body** is inferred.
- **Base charge.** 16 KiB per lease is inferred.
- **Header factor.** The header and cookie factor (48 × H) is inferred.
- **Cost of the reservation.** At the default `ArgumentLimit` of 1000, one phase-2 reservation costs
  about 56 MiB, and a transaction holding both query and body arguments about 112 MiB. Up to
  `slots` such reservations can be transiently held. Validated sizing for
  small budgets (A1–A4) may require a larger budget or a lower `SecArgumentsLimit`.
- **Phase-1 fail-fast** for requests with unread bodies may reduce goodput under saturation compared
  with queueing (A17). The deferred alternative is to receive the body into the charged buffer
  before phase 1.
- **Evaluation working set beyond retained state** (F1 shows 25 MiB of allocation churn for 991
  arguments) is bounded only through slots. Its live peak per slot in the envelope is unmeasured
  (A1).
- **Combined plugin and WAF behaviour in the envelope** is unmeasured (A4).
- **Calibrated request shapes.** I3a calibrates spill, mixed query and body arguments at
  `ArgumentLimit`, partial processing and large headers (see I3a progress). Response shapes are
  calibrated in I4.
- **Phase-5 slotting cost.** Its contention under unsaturated load, and how often phase 5 overflows
  under saturation, are unmeasured (A13, A18).
- **Feasibility measurements** are single or double repetitions, with c1 noise of 2.3–4.6/s.

## Accepted amendment — 2026-11-13: inspection-owned storage, guest capacity and audit pressure

This amendment was proposed in checkpoint 1 and **accepted** when #714 merged (`16d98258`, head
`faf3ead4`). Acceptance alone does not change current configuration, runtime behavior, metrics or
qualification; the implementation checkpoints do. Its specific corrections supersede
conflicting historical statements in §§3, 4, 7–11 and the original slice table; all other accepted
decisions and A1–A18 remain in force, and A19–A22 are added. In particular, keep the process owner,
the existing `enforce` default, `256m` byte ceiling, auto evaluation slots, `1s` queue timeout,
absolute `60s` body timeout (`0s` disables it) and `64k` response-header ceiling. The `enforce`
guarantee and default gate cover a new control (R5, spool, guest capacity) only once its own
criterion passes: R5 → A21, spool → A19, guest capacity → A20, operability → A22. Until defaults
are chosen, spool and guest enforcement require explicit limits. No final size is chosen and no
A1–A22 criterion is claimed as passed. The original §5 statement that abandoned work cannot accumulate
beyond the slot count applies only to slotted calls, not unslotted phase 5.

### Source boundaries and evidence

| Boundary | Pinned-source finding | Design consequence |
| --- | --- | --- |
| Coraza v3.8.1 temp files | `internal/corazawaf/body_buffer.go` calls `os.CreateTemp` and copies the buffered prefix on spill; `internal/bodyprocessors/multipart.go` separately creates and writes an upload file per file part. `SecTmpDir` is accepted but ignored by `internal/seclang/directives.go`. The body processor sets `REQBODY_ERROR`, evaluates phase 2 and returns a nil error on a parser I/O error. | The pinned public API does **not** provide the required file-creation/write interception or preserve a typed parser I/O result for Jul. A reader wrapper, `SecTmpDir`, `REQBODY_ERROR` rules, free-space polling or post-write counting cannot establish fail-closed quota/error handling. |
| Static roots | Jul's static handler opens an operator-configured `os.Root` and serves regular files under it; Coraza temp names are ordinary non-hidden names. | The owned spool root and all upload destinations must be provably disjoint from every static-served tree in every serving/candidate generation. Canonical and symlink aliases must be checked; ambiguous identity fails validation. |
| Audit phase | `Transaction.ProcessLogging` evaluates phase 5 and then invokes `AuditLog()` and a writer. Part C performs `io.ReadAll` and converts the body to a string before the writer sees it; the audit transaction exposes mutable collections, and `tx.Close()` resets them before pooling. A custom writer can retain the record after `Write` returns. | R1–R4 and a slot alone do not prove phase-5 coverage. Reserve R5 before a transaction can require irrevocable phase-5 work, and pass only an immutable detached snapshot to writers; no writer may retain a pooled transaction graph. |
| wazero v1.12.0 | `WithMemoryLimitPages` sets a maximum per memory; no pre-growth admission callback is established. The pinned decoder permits at most one memory per module. Jul eagerly instantiates one module per plugin in Prepare and retains idle modules. | Reserve the effective page-rounded ceiling for each starting/live instance **before** instantiation. Do not poll `Memory.Size` as admission. Revalidate the one-memory assumption on an engine upgrade. |

[Checkpoint-1 probes](../reviews/evidence/2026-11-13-700-checkpoint-1/README.md) show a
262,308-byte multipart request simultaneously occupying a 262,308-byte body spill and a
262,144-byte file, 32 empty files from a 4,371-byte request, and approximately 2.25 MB of
cumulative `ProcessLogging` allocations for one 256 KiB part-C audit. A synthetic multipart
file-create failure sets `REQBODY_ERROR` but returns neither a parser error nor a phase-2
interruption; a rule on a later multipart field therefore does not block. A retained audit record
also observes changed arguments after `tx.Close()` and transaction reuse. Four 16 MiB guest
ceilings could grow to their limits. These are small, unconstrained macOS probes, not a Linux
envelope or a universal bound. The historical I2 p1/p2 sized-pool cells each have only one
repetition; p1 contains guest-500 findings. They do not establish production defaults.

### Normative ownership and admission inventory

All counters below belong to the same process-owned authority but retain their own units; bytes,
files and slots are never added into one “total resource” number. “Reserve” means a logical
admission charge, not a measurement of RSS. The lifecycle, mode, status and test rules following
the table apply to every applicable row.

| Resource and owner | Unit; reserve before consumption | Release / remaining boundary | Owner checkpoint |
| --- | --- | --- | --- |
| Lease and dynamic plugin/WAF host buffers | `base` and `buffer` bytes under `max_buffered_bytes`; the first actual inspection layer creates one lease, reused by nested layers. Charge capacity before allocation/growth/copy, including old and new arrays. | The creating layer closes only after downstream work, response callbacks and all represented aliases are gone. Transport readers are detached/revoked as in #710. | I2 merged; I3a/I4 complete remaining paths |
| WAF transaction state | Cumulative `waf_state` **estimated** bytes R1–R4; reserve each phase's worst case before its Coraza call while holding the slot, then true down only as calibrated. `B_total` includes spilled input. | Hold until the synchronous work has returned and the owning goroutine closes the transaction. Cancellation or a reset is not a refund. `ARGS_PATH` remains empty unless its term and tests change. | I3a request, I4 response |
| Phase-5 rules, audit construction and output | New `audit` **estimated reservation** R5, charged against the same `max_buffered_bytes`, not a second limit or an addition outside the accounted total. Reserve the complete per-transaction worst case before creating/entering a WAF transaction and before any response can make refusal impossible. | Retain through slotted or unslotted `ProcessLogging`, formatting and synchronous writer completion. Detach the immutable snapshot before transaction reset/pool reuse. If serialized bytes are asynchronously queued, transfer ownership to a distinct bounded queue; unsupported aliases cannot be refunded or claimed covered. Closing a request lease cannot refund an unowned retained record. | I3a request and I4 response calibration; exact R5 proof is blocked below |
| WAF execution | Process `slots`, one per synchronous rule-evaluating call, including implicit evaluations in body writes. No network read is performed while holding one. | Release only on actual return. Phases 1–4 try-acquire while request data is unread, otherwise use the existing bounded FIFO. Phase 5 never skips rules: it uses the same eligibility rule, but visibly runs unslotted if no slot is obtained. It is not strictly bounded by the slot count. | I3a/I4 |
| Guest instances | Process `guest_capacity` bytes: the effective `WithMemoryLimitPages` ceiling, in 64 KiB pages, for **each** starting, idle, busy or closing instance across candidate/current/retiring generations. This is neither actual guest allocation nor RSS. | Reserve before instantiation and `_initialize`; reuse a pooled instance without another reservation. Release exactly once only after the module/runtime has stopped and no owner can use its memory. A failed close remains accounted and visible. | I2b |
| Compiled rules/modules and transaction pools | Generation-owned compiled state and process/GC-retained pool capacity; configured capacity, calibrated estimates or observations where available, **not** a guest or request reservation. | Retire only after actual generation users and resources finish; a reset pooled transaction can retain backing arrays. Unmeasured baseline is unavailable, not zero. | Existing lifecycle; A9/A15 and I6b |
| Body spill and multipart files | Process `spool` logical reserved bytes and file count, independent of the request lease. Before every supported create, reserve a file; before a write/growth, reserve its possible resulting extent, including buffered-prefix copies and simultaneously retained multipart files. | A file remains charged through open handles, unlink/close, partial writes and failed cleanup. Refund only after all Jul-owned handles/aliases are closed and removal is confirmed. Crash leftovers are reconciled before new reservations. | I3b, blocked on the integration boundary below |
| Audit output and other persistent storage | R5 covers in-process audit construction, the detached snapshot, synchronous formatter copies and any serialized buffer until its ownership is transferred. The configured sink's durable files/remote destination and other Jul-owned storage categories are separate external boundaries, not inspection spool and not bounded by R5. | Their configured owners, retention and I/O health remain responsible; a spool quota is not a filesystem-wide or durable-audit quota. tmpfs and page cache can still count against a cgroup. | Existing sink/storage owners; I6a exposes the distinction |

Acquisition has no governor wait cycle: create/reuse the request lease, reserve R5 before WAF
transaction creation, and acquire each dynamic byte or spool reservation fail-fast before its
consumer. A rule-evaluating call acquires a slot before its R1–R4 term; reservations taken while
holding a slot never wait. For a guest, reuse an idle instance, or take the existing local plugin
slot, try the global guest reservation, then instantiate; on refusal release the local slot. The
existing bounded local-instance wait holds no global guest reservation. No governor mutex is held
across engine work, file I/O, a module close or an audit write. A synchronous blocked audit sink can
occupy slots and force phase-5 overflow, but cannot create an unbounded waiter queue; the writer
deadline below bounds how long it can hold them. An admitted transaction's R5 remains reserved
while it waits in the evaluation FIFO, so the waiter cap (`16 × slots`) times `R5_max` is part of
the sizing budget and A18 must exercise it.

### R5: admission before phase 5 becomes unavoidable

The previous assertion that phase 5 needs no reservation is withdrawn by this proposal. For each
effective WAF policy, Prepare must derive an overflow-checked `R5_max` from the maximum reachable
phase-5 rules, audit parts and formatters, including dynamic `ctl` changes, bounded request and
response bodies, headers, file metadata, matched-rule messages, escaping/serialization copies and
writer retention. `R5_max` is included **once** in the WAF route's single-request minimum in
addition to R1–R4 and buffer terms. It must not be double-counted as a separate additive total.
Its phase-5 rule allowance still applies when audit output is off if logging rules can run.

The request-side components and coefficients are calibrated in I3a (see I3a progress); the
response-side ones remain **blocked** pending calibration in I4. The part-C sample above is a
counterexample to zero; it is not a safe multiplier. Coraza's audit object can retain references to transaction-owned mutable
collections. In the probe, a writer retained its `AuditLog`, `tx.Close()` returned that
transaction to the pool, and a later transaction's argument became visible through the old
record. A writer can also return while retaining or asynchronously consuming the object, so
reserving until `Write` returns is not an ownership proof.

Before calling a writer, the supported integration must create an immutable, bounded snapshot
whose fields and backing storage no longer alias the transaction, request/response buffers,
collections or other pooled state. The snapshot and every synchronous formatter copy are covered
by R5. A supported synchronous writer must finish using the detached snapshot before returning
and may not retain it; initially this is limited to Jul-owned, reviewed writer adapters. A custom
writer that cannot satisfy this contract is unsupported in a protected profile in both modes
until an explicitly reviewed adapter exists; registration by name is not evidence that it is safe.
An asynchronous sink may enqueue only an immutable serialized buffer under a separately bounded
sink-queue owner, not a Coraza transaction, audit object or request-scoped snapshot. No async
sink without such a bounded owner is supported.
Asynchronous request-body readers or transports without a revocation/completion signal are
likewise unsupported for a protection claim. Their configurations must be rejected in `enforce`
and reported explicitly unavailable/unprotected in `observe`, never treated as measured zero.

No post-serialization check, truncation, skipped phase-5 rule or assumption that `Write` returning
means aliases are gone is an acceptable substitute. If immutable detachment and bounded output
ownership cannot be established with the pinned API, an upstream integration change is a
blocking prerequisite.

If R5 cannot be reserved, reject before a Coraza transaction is created: `503`,
`Retry-After: 1`, `inspection_overloaded`, or native gRPC `UNAVAILABLE`, with bounded reason
`audit`. Once a transaction has entered Coraza, phase 5 runs even on early exit, cancellation,
slot saturation or a blocked sink. I3a must preserve this ordering in every error path. A
compiled minimum exceeding the unchanged 256 MiB default is an explicit validation/migration
issue, not a reason to silently raise that default. The pre-I3a 258,555,904-byte default-route figure
excluded R5; I3a recalculates it (see the I3a decisions below).

### Spool integration and persistent ownership

**Required integration, not present today:** a reviewed, maintainable public Coraza integration
must hand Jul ownership of *both* body-buffer and multipart temporary-file creation, every
write/extent change, close and removal, including errors and transaction identity. It must make
quota refusal distinguishable from physical I/O errors all the way back through implicit
evaluation and multipart parsing. Coraza v3.8.1 can swallow a body-processor error: it sets
`REQBODY_ERROR`, evaluates phase 2 and returns a nil error. Jul's current wrapper treats only a
returned error or interruption as a processing failure, then invokes the origin. A generated
security rule is not a substitute: the marker may be set after the multipart parser has stopped,
and arbitrary supported rules need not reject it. The archived probe demonstrates a file-create
failure with no returned error/interruption and no match on a later multipart field.

The integration must surface quota refusals and physical file I/O/cleanup failures as typed,
out-of-band results to Jul before origin forwarding; never infer or suppress them from a SecRule,
generic `REQBODY_ERROR`, or HTTP status. In protected profiles, a quota refusal maps to governed
overload, and a physical create/write/close error fails inspection closed with an explicit 500
before forwarding. This applies in `observe` too: observe never bypasses physical failure.
Malformed-body and configured size-limit behavior remain separately classified and preserve their
accepted semantics. A failure discovered after response commitment cannot replace the response;
the record must identify partial delivery, stop further phase 1–4 evaluation (phase 5 still runs)
and retain cleanup debt.
It may be an upstream change or another reviewed supported boundary with equivalent proof; a
private reflection hook, undocumented fork or post-write audit is not sufficient. Response body
buffers currently use an in-memory limit equal to their total limit, but any future spill through
the common buffer must use the same boundary. I3b cannot claim protection until this dependency
and fail-closed behavior are tested on the pinned version.

The process owns a private, instance-specific root, not the shared OS temporary directory.
Creation must be exclusive, permissions restrictive, directory identity stable, and file operations
protected against symlink/path substitution and collisions on each supported platform. A
cross-process exclusive owner lock prevents two Jul instances from independently claiming the
same root. The root and every effective upload directory must also be disjoint from all static
file-serving roots across the current, candidate and retiring generations. Compare canonical
directory identity and ancestry, not only raw strings; resolve or reject symlink aliases. A path
whose relationship cannot be proved on a supported platform is invalid. Revalidate this invariant
before Publish whenever a reload changes either the spool or any static root. This prevents
ordinary non-hidden `body*`/`crzmp*` files from becoming reachable via a static route or directory
listing. Entries are attributable to this instance; no scan or removal of unrelated content is
allowed. Before admissions after restart, reconcile every attributable leftover and count it
until confirmed closed/removed. unclassifiable entries, failed reconciliation or unsupported
locking make protection unavailable and fail closed in a protected profile, not zero usage.
Use bounded cleanup concurrency and a bounded queue/sweep, as specified under "Spool cleanup and
reconciliation" below; a failure stays charged as cleanup debt
and is observable. A missing directory, `ENOSPC`, permissions or close/remove failure is not
silently converted into quota success. Logical quota excludes inode/block rounding and other
processes, and cannot promise physical free space or bound same-user external file handles.
Jul-owned descriptors must not leak to child processes.

`SecUploadKeepFiles On` and `RelevantOnly` are **unsupported in protected profiles**, in both
inspection modes, until a separately bounded persistent-upload owner exists: reject them at
configuration validation rather than delete retained data or refund it at transaction close.
An explicit `SecUploadDir` outside the owned root is likewise rejected, never silently redirected.
The historically ignored `SecTmpDir` is not a spool setting. Migration must identify these
directives and offer actionable choices; it must not claim that durable upload or audit retention
has been implemented.

`spool_dir` is startup-only. A change requires stopping the old owner and explicitly reconciling
its root before switching, including after a crash. A durable, cross-platform way to discover a
previous root after an out-of-band config change is **not yet established**; I3b must provide a
reviewed durable locator/transition or a verifiable offline migration protocol that refuses
startup when the previous identity cannot be reconciled. Operator instructions alone cannot
prove that no old root was forgotten. This is a blocking prerequisite for cross-root protection.
Prepare/rollback within a running process cannot move files or reset counters.

### Proposed public controls and defaults

These are proposed `[global.inspection]` names, using existing `Size` and TOML conventions:

| Field | Type and validation | Effective default / migration |
| --- | --- | --- |
| `max_guest_capacity_bytes` | Positive `Size`; aggregate sum of effective per-instance page ceilings, overflow checked. `0`/unlimited is not a protected configuration. | **Blocked:** choose a finite omitted-field default from representative, overlap-aware measurements before I2b ships. In `enforce`, the configured cap must at least fit one eager instance of each candidate plugin; `observe` warns. Offline `jul check` uses compiled metadata only, never instantiates a guest. |
| `max_spool_bytes` | Positive `Size`; process-wide logical file-extent reservation across all generations and owned roots in the transition. | **Blocked:** choose a finite omitted-field default after representative spill/multipart and filesystem-headroom measurements. |
| `max_spool_files` | Positive integer; counts zero-byte and nonzero files, including cleanup debt. Overflow or invalid values fail validation. | **Blocked:** choose a finite omitted-field default after representative file-count measurements. A byte ceiling is not a substitute. |
| `spool_dir` | Absolute operator-controlled directory path; validate private ownership, secure operations and exclusive identity. No request-derived path or path label. | **Blocked:** select a portable stable default/locator and migration mechanism; no silent shared-temp fallback. A path change is restart-required. |

Until supported defaults are selected, the future protected profile requires explicit positive
limits and a safe directory, with a precise error rather than an implicit unlimited value.
For scale only, `64m` guest capacity fits the four 16 MiB probe instances, and `1m` plus 64 files
fits each individual storage probe. These are **not production recommendations**: they do not
cover generation overlap or representative concurrency. Existing deployments need release notes,
`jul check`/preview diagnostics and explicit sizing guidance before any new enforcement is
enabled. Profiles without the relevant compiled capability or configured feature report
`not_applicable`; they do not fabricate an enforced zero.

I2b deliberately uses **fail-fast refusal, not idle eviction**: an idle instance remains charged
and may monopolize the cap. A new instance for another plugin or candidate can therefore fail
even when all currently reserved instances are idle. Reuse an existing idle instance where
possible; otherwise operators size the cap for expected concurrent pools **and** candidate/old
generation overlap, or lower per-plugin `max_instances` and retire unused generations. Never
evict a busy instance or depend on `sync.Pool` for module closure. This is a documented
compatibility/availability trade-off, not fairness.

### Control-mode and failure contract

Inspection `observe` is independent of WAF `detect`; WAF rule outcomes and existing body, parser,
plugin and server-timeout semantics remain intact in all four combinations.

| Control or failure | `enforce` | `observe` |
| --- | --- | --- |
| Existing `base`/`buffer`, R1–R4, slots and body deadline | Existing §7 behavior and error mapping. | Existing would-reject/timeout behavior; no new governor-driven 503, 408 or 500. |
| R5 audit reservation | Refuse before transaction creation with governor 503/`UNAVAILABLE`, including when the bounded `abandoned_audit` set is full; phase 5 is never refused for an admitted transaction. | Charge and record each would-refuse decision; continue phase 5. If the abandonment set is full, run phase-5 rules but drop and count that record instead of calling the writer. |
| Spool bytes or file count | Before create/growth, fail-fast governor 503/`UNAVAILABLE`, reason `spool_bytes` or `spool_files`, including while the bounded cleanup queue is full; do not forward partial input. | Count would-refuse decisions and continue **only** if real filesystem operations succeed; a full cleanup queue leaves files charged as debt for the bounded periodic sweep. |
| Guest capacity | Before new instance creation, governor 503/`UNAVAILABLE`, reason `guest_capacity`; eager candidate failure aborts Prepare and leaves the old generation serving. Existing local instance-limit behavior stays distinct. | Track full reservations and would-refuse decisions; instantiate without a governor cap. Real guest failures are unchanged. |
| Physical spool I/O, unsafe/unavailable ownership or unsupported retained uploads | Never bypass inspection. A typed create/write I/O failure before commitment is a WAF inspection error with `500`, not quota saturation; cleanup after commitment records debt/failure and cannot rewrite the response. Unsafe ownership/unsupported directives fail configuration validation. Coraza body-processor I/O failures must be surfaced out-of-band before forwarding; `REQBODY_ERROR` or a missing phase-2 interruption alone is insufficient. | The same physical and configuration failures apply; observe is not a filesystem or security bypass. |
| Mutable or asynchronously retained audit record | Only immutable transaction-detached snapshots may reach writers; unsupported retaining writers are rejected in protected profiles. Any queued serialized output has a distinct bounded owner. | Same safety contract; observe cannot make an unsafe alias or unbounded queue safe. |

H1 refusals preserve #710's `Connection: close` behavior for unread bodies; H2/H3 affect only the
stream. If a response is already committed, abort the affected stream using the existing fail-closed
abort semantics, retain the actual committed client status and partial-delivery signal, and stop
further phase 1–4 evaluation rather than claim a replacement 503; upgrades, streams and CONNECT
follow "Long-lived exchanges" below. The timer-only deadline owner, fetch-reader
revocation, plugin-header rollback, generation-retention fix #709, ADR 0020 subscription behavior
and rule 900001 remain unchanged.

### Lifecycle, status and operability

- **Prepare:** compile and validate effective facts without changing published limits. At startup,
  use the candidate limits. During reload, candidate eager instances and files reserve against the
  **currently published** process limits; a candidate cannot borrow its proposed increase before
  Publish. A full reload cannot be used to raise the cap when candidate warm-up itself needs the
  increase. I2b must provide a genuine limits-only publication path for this case: accept only
  changes confined to validated limits, do not rebuild the plugin set or instantiate replacement
  guests, and atomically publish the validated limits. Validation or publication failure leaves
  the old limits and generation unchanged. Candidates may use an increase only after that
  publication succeeds; they never borrow an unpublished increase. I2b must test this path,
  including a cap increase blocked by old-cap guest warm-up and failure atomicity. Failure or
  cancellation of a full candidate tears it down and releases only resources actually gone; the
  old generation continues. Offline checking compiles metadata only.
- **Publish / decrease / mode change:** atomically publish limits. Existing reservations survive;
  enforce admits no acquisition that would exceed the new limit, while existing usage may drain
  above it. Observe continues to account without enforcing. A transition from observe to enforce
  can therefore begin over limit and reports draining. Rollback is another Publish, not a reset.
- **Retire / shutdown / restart:** old generations keep charges until their work and aliases finish.
  Do not revive #709's closed-exchange references. Shutdown performs bounded cleanup and leaves
  unreleased files discoverable for startup reconciliation; neither cancellation nor process
  exit proves that a persistent file was removed. Directory migration follows the separate
  explicit boundary above.
- **Canonical source:** the process snapshot feeds `/api/v1/status` and its CLI, then the existing
  `/api/runtime/overview` projection. Preserve current `request_path_integration = "partial"`
  until the relevant paths actually ship. Add separate implementation state
  `not_applicable|not_implemented|available|unavailable|error`, pressure state
  `below_limit|at_limit|over_limit|unknown`, and an enforce-only draining indication per control.
  Below a limit does not promise that the next request fits. Missing, stale or uncalibrated values
  are unavailable, never measured zero.
- **Metric proposal:** extend `jul_inspection_bytes{class}` with `audit` and
  `jul_inspection_rejections_total{reason,mode}` with `audit`, `spool_bytes`, `spool_files` and
  `guest_capacity`. Add `jul_inspection_spool_bytes`/`jul_inspection_spool_files` gauges with
  bounded `state=reserved|cleanup_debt` (debt is a **subset**, not additive), corresponding
  `_limit` gauges, `jul_inspection_spool_failures_total{operation}` with a closed operation enum,
  `jul_inspection_guest_capacity_bytes` and `_limit`, and
  `jul_inspection_guest_instances{state=starting|idle|busy|closing}`, plus
  `jul_inspection_audit_drops_total{reason=abandoned_full|queue_full}` and an
  `jul_inspection_audit_abandoned` gauge (calls and bytes still charged). Starting/closing tokens
  count as reserved even if not yet/no longer live. Existing `plugin_guest_max_bytes` remains
  configured potential across unclosed generations; it is not actual memory or the new reservation.
  Counts are decisions, not unique rejected requests. No path, request, identity, raw error or
  generation label is allowed.
- **Storage headroom:** extend the existing closed `jul_storage_bytes` category set with
  `inspection_spool`; filesystem free space is advisory and separate from logical quota usage.
  Project actual write health without a duplicate semantic counter. A blocked or failed audit
  sink remains an existing sink-health boundary, not a durable-audit guarantee.
- **Permissions and UX:** status aggregates require existing `status:read`; a limiting route's
  identifying details require `config:read`, otherwise return a redacted indication rather than
  an equality oracle. Reuse the server-owned, permission-safe explanations of #625 and actual
  producer signals for #628 without claiming either broader issue complete. I6a adds the existing
  Overview disclosure, editor/authority behavior and bounded browser-local trends; no new
  notification service or frontend policy engine.

### Concurrency, ownership and protocol rules

These rules close gaps found by the delayed concurrency/protocol review of this proposal. They are
normative for the owning checkpoints and do not change current runtime behavior.

- **Process-scope reservation tickets (I2b, I3b).** `guest_capacity` and spool reservations are not
  request-lease byte classes; the current `ByteClass` set (`base`, `buffer`, `waf_state`) is
  unchanged, and R5 is the only new lease class. The governor issues a distinct process-scope
  ticket per reserved guest instance or spool file, owned by the plugin pool or spool owner, never
  by a request `Lease`. Closing a lease never refunds a ticket. A guest ticket is released once,
  after `Module.Close` returns success and the instance is unreachable; a spool ticket after every
  Jul-owned handle is closed and removal is confirmed. A failed close/remove keeps the ticket
  charged and visible as debt. Each ticket records its generation; the generation's charge is the
  sum of its live tickets, so candidate teardown releases only tickets whose resource is
  confirmed gone and never alters the old generation's charge. Tests: an instance idle when its
  request ends, a file still open when its request ends, close failure, and candidate teardown.
- **Guest instance states (I2b).** Each instance is `starting`, `idle`, `busy`, `closing`, `closed`
  or `failed_retained`. Reserve before `starting`; an instantiation trap, `_initialize` failure,
  timeout or request cancellation moves it to `closing`, and the ticket is released only from
  `closed`. A close failure moves it to `failed_retained`, which stays charged and reported.
  Request cancellation is propagated into instantiation (today `newInstance` uses
  `context.Background` with `instantiateTimeout`, and `discard` ignores the `Close` error, so
  I2b changes both). Tests: close failure, instantiate trap, timeout and cancellation.
- **Effective guest ceiling (I2b).** A positive `memory_limit` below one 64 KiB page currently
  rounds to 0 pages and then to the 256-page (16 MiB) default, so a 32 KiB setting reserves 512×
  the configured value. Before I2b ships, a positive `memory_limit` below 64 KiB is a validation
  error, and other values report the effective page-rounded ceiling that is reserved.
  `GuestCapacityStats` keeps reporting configured potential; I6a adds the reservation view
  (reserved bytes and instances by state) used by A22.
- **Serialized limit publication (I2b, I3b).** Every limits publish — startup, full reload,
  limits-only publication and rollback — goes through one serialized publisher that stamps a
  monotonically increasing limits version. A candidate records the version it prepared against;
  its Publish re-checks that version under the publisher and re-prepares or fails on mismatch, so
  a concurrent decrease cannot be overwritten by a stale candidate and an increase is never
  borrowed early. Tests: a decrease during Prepare, an increase during candidate warm-up, and a
  rollback racing a reload.
- **Observe accounting parity (I3a).** Observe's would-reject decisions must match what enforce
  would do. A phase-5 call that would overflow in enforce is counted as unslotted in observe and
  does not inflate the active-slot count used for later phase 1–4 decisions; today the observe
  path increments `active` for phase-5 tokens. On an observe→enforce transition, slots already
  held remain held and count against the enforced limit; new acquisitions follow enforce rules.
  Test: with `slots = 1`, an observe sequence of phase-5 overflow followed by phase 1–4 work
  reports the same would-reject decisions as enforce.
- **Engine off (I3a).** When the rule engine is off, no R5 is reserved; if R5 was reserved before
  the engine state is known, it is released before the early return. Test: an engine-off route
  holds no R5 after the request.
- **Refusal classification (I3a, I3b, I2b).** The new reasons `audit`, `spool_bytes`,
  `spool_files` and `guest_capacity` are governor overload, classified by the same overload
  classifier as today's byte, `waf_state` and `evaluation` saturation (`IsOverload` /
  `WriteOverloaded` are extended): `503`, `Retry-After: 1`, `inspection_overloaded`, H1
  `Connection: close` for an unread body, H2/H3 stream-local, native gRPC `UNAVAILABLE`.
  `guest_capacity` stays distinct from the existing local "plugin busy" outcome. Each reason has a
  per-protocol test.
- **Long-lived exchanges (I3a, I4).** For a 101/WebSocket upgrade, SSE or streaming response,
  streaming gRPC and CONNECT (H1, H2 extended CONNECT and H3), the request is inspected and then
  commits to a long-lived exchange. At the point where Jul stops inspecting the exchange — the
  upgrade/CONNECT commit, or for a streamed response the end of its last inspected phase or the
  decision not to inspect its body — Jul runs phase 5, detaches the immutable snapshot and ends
  **transaction ownership**: the slot is released when the Coraza call returns, and the
  transaction's `waf_state` and the transaction-scoped part of R5 are released. Transaction
  ownership is never held for the life of a hijacked connection or stream. Audit output that is
  still pending (an in-flight writer call, an abandoned call or a queued serialized buffer)
  **remains charged** under the R5 class to that output owner, as defined below, and drains only
  when the owner actually finishes with it. Test: many concurrent long-lived tunnels hold no slot,
  `waf_state` or transaction-scoped R5 after their commit point, while pending output stays
  visibly charged until written, dropped or returned.
- **Audit writers (I3a, I4).** The supported writers are named, Jul-owned adapters reviewed under
  the snapshot contract; Coraza's built-in writers (for example the serial, concurrent and HTTPS
  writers) are unsupported in protected profiles until an adapter for each is reviewed.
  `SecAuditLog*` directives that select a writer type, format or storage are rejected in
  protected profiles unless they select a supported adapter. A synchronous writer runs under a
  bounded deadline enforced by the Jul adapter, which returns to Coraza when the deadline expires,
  so the Coraza call and its slot end. The writer call itself is abandoned and counted; its
  snapshot ownership moves to a bounded `abandoned_audit` set (fixed entry and byte bound) and
  **stays charged** under the R5 class, visible in status and metrics, until the abandoned call
  actually returns. Only then is that charge refunded. Abandonment never resets or hides the
  charge. Whether Coraza's pinned `ProcessLogging` writer call is synchronous, and each writer's
  own deadline behavior, are verified in I3a rather than assumed.
  - *`abandoned_audit` full, `enforce`:* new WAF transactions are refused before creation with
    reason `audit` (governor overload, as for R5).
  - *`abandoned_audit` full, `observe`:* no governor-driven refusal. The transaction is admitted
    and the would-refuse decision is counted; phase-5 rules still run, but the audit record for
    that transaction is **not handed to the writer** and is counted as a bounded audit drop
    (`jul_inspection_audit_drops_total{reason="abandoned_full"}`). No writer call, goroutine or
    queue entry is created beyond the fixed bound, so observe cannot grow abandoned work without
    limit. Physical writer errors and unsupported-writer configuration are unaffected by mode.
- **Async output queue (I3a).** A sink that serializes and enqueues owns a single process-scope
  queue with a fixed byte and entry bound, charged under the R5 class until dequeued and written.
  When the queue is full, the serialized record is either dropped and counted
  (`jul_inspection_audit_drops_total{reason="queue_full"}`) or waits synchronously under the writer
  deadline and then follows the abandonment rule above. This choice is identical in both modes,
  because it governs output, not admission; the queue never grows without bound. If the request
  lease closes while serialization is still in progress, the R5 refund is deferred until ownership
  has been transferred to the queue.
- **Spool cleanup and reconciliation (I3b).** The spool owner runs one reconciliation sweep at
  startup before admission and a bounded cleanup worker pool afterwards. Cleanup requests go into a
  fixed-capacity queue. A file whose cleanup cannot be enqueued stays charged as debt in its
  fixed-size ticket, which is not a queue entry, and a bounded periodic sweep of the owned root
  (fixed interval and per-pass entry limit) retries it. Neither mode enqueues unbounded work or
  starts a goroutine per file.
  - *Cleanup queue full, `enforce`:* new spool admissions are refused with reason `spool_files`
    until the backlog drains.
  - *Cleanup queue full, `observe`:* no governor-driven refusal. The would-refuse decision is
    counted, the file stays charged as debt for the bounded periodic sweep, and new requests
    continue **only** if their real filesystem operations succeed.

  The startup sweep enumerates only the owned root and attributes each entry by the instance's
  naming and lock. Until every attributable entry is closed/removed or counted as debt, spool
  protection is unavailable. This is an ownership failure, not quota saturation: it fails closed
  in a protected profile in **both** modes, like the other physical I/O and ownership failures
  above.
- **Migration minimum (I3a).** The recalculated route minimum, including R5, is validated at
  startup and reload: in `enforce` a configured `max_buffered_bytes` below it refuses startup or
  the reload; in `observe` it warns. The pre-I3a 258,555,904-byte default-route figure
  excluded R5; the recalculated figure is 258,654,208 B (the 64 KiB fixed R5 term plus I3a's
  32 KiB request read buffer).

### Checkpoint mapping and acceptance extensions

The original I3 is split into **I3a** (request/evaluation/R5 request calibration) and new **I3b**
(spool). **I2b** is new guest-capacity work. I6 is split into **I6a** (operability/documentation)
and **I6b** (integrated qualification, a later prompt). Schedule I3a → I4 → I3b → I2b → I6a after
this amendment is accepted; this order does not make the guest mechanism technically dependent on
the WAF parser. The [checkpoint ledger](../reviews/700-inspection-implementation-ledger.md)
records actual state and blockers.

| Checkpoint | Required focused evidence before merge |
| --- | --- |
| I3a / I4 | R1–R5 calibration for supported request/response shapes, audit parts and writer aliases; immutable-record verification across `tx.Close()` and pool reuse; bounded synchronous/async sink ownership; implicit phases, blocked sink and unslotted phase 5; real H1/H2/H3 refusal and sibling isolation; no early refund. Also: writer deadline and abandonment with the abandoned charge staying visible until the call returns, `abandoned_audit` saturation in both enforce (refusal) and observe (counted audit drop, no refusal, bounded writers), async queue saturation, unsupported-writer directive rejection, release of transaction ownership at the upgrade/stream/CONNECT commit point while pending output stays charged, with a many-tunnel test, R5 held across the evaluation FIFO wait, engine-off R5, observe/enforce slot parity with `slots = 1`, per-reason overload classification on each protocol, WAF-only `body_timeout` including H3, and the startup/reload migration minimum. |
| I3b | Concurrent prefix spill plus multipart duplication, zero-byte files, partial writes, typed create/write/close/remove faults, cancellation, retained-directive rejection, static-root overlap/symlink aliases across reload, restart reconciliation, limits/modes and generation overlap on supported filesystems; observe-to-enforce with an unchanged ceiling and pre-existing over-limit reservations. Also: process-scope spool tickets that survive request-lease close, cleanup-queue saturation in both enforce (refusal) and observe (counted would-refuse, retained debt, bounded periodic sweep, no refusal), startup reconciliation failure failing closed in both modes, and serialized limit publication races. |
| I2b | Real guest eager/start failure, idle/busy pools, exact/over-capacity and concurrent instantiation, cancellation, traps, close failures, reload/rollback and limit decrease, including candidate and retired generations; a true limits-only cap increase that does not rebuild or instantiate guests, validates limits-only changes and publishes atomically, including failure atomicity and the old-cap warm-up counterexample. Also: process-scope guest tickets with the instance state machine, `failed_retained` after a close failure, cancellation propagated into instantiation, sub-page `memory_limit` rejection, and limit-version races (decrease during Prepare, increase during warm-up, rollback racing reload). |
| I6a | Real producer/API/CLI/Console states, permissions and file-owned mode, missing/stale/observe/draining states, bounded trends, actual alert handoff and accessible responsive interaction, including the guest reservation view distinct from configured potential. |
| I6b | Run the preserved full matrix and new criteria with the corrected baseline and recorded identities; do not infer acceptance from checkpoint tests or this probe. |

The A9 reload case additionally records compiled/static baselines, candidate and retiring guest
reservations and spool debt without resetting or multiplying any limit. A15 additionally records
post-burst pool/compiled-generation retention, guest instances and spool debt, distinguishing
logical drainage from heap, RSS and physical filesystem effects. Their existing thresholds remain
unchanged. Add the following unused identifiers:

| # | Case | Pass condition |
| --- | --- | --- |
| A19 | Inspection spool, including spill plus multipart, zero-byte files, faults, overlap and restart | In enforce, logical byte and file reservations never exceed the published ceilings except pre-existing reservations/debt draining after a decrease or an observe-to-enforce transition, including when the numerical ceiling is unchanged; after either transition, no new acquisition may exceed the enforced ceiling. No early refund, unsafe deletion or partial inspection. Observe reports would-refuse while real I/O failures remain visible. Owned leftovers are reconciled before admission. Test observe-to-enforce with existing over-limit bytes/files and verify new acquisitions are refused. |
| A20 | Guest instance capacity across plugins and generations | Every starting/live instance is reserved at its effective ceiling before instantiation; no double charge on reuse or early refund on failure/retirement. Enforce refuses over-capacity, observe records it, and candidate failure preserves the current generation. Idle-pool refusal is exercised and documented. A failed close stays charged; concurrent limit publications never let a stale candidate overwrite a newer limit. |
| A21 | Phase-5 audit pressure and blocked output | R5 is reserved before transaction entry, covers the supported calibrated request/response audit construction and immutable detached record, and remains owned through the last bounded output alias; transaction reuse cannot mutate a prior record. Phase-5 rules are never skipped and saturation cannot create a governor wait cycle. A hung writer is abandoned at its deadline: the slot is released when the Coraza call returns, but the abandoned writer's retained audit charge stays bounded, charged and visible until the call actually returns, and only then drains. Once every abandoned or queued output has actually finished (returned, written or dropped and counted), slot, R5 and queue counters return to baseline. Long-lived exchanges release transaction ownership at their commit point, while pending output stays charged to its owner. When the abandonment set or output queue is saturated, enforce refuses with `audit` and observe admits, counts would-refuse and audit drops, and creates no unbounded writers or queue entries. Unsupported unbounded policies do not receive a protection claim. |
| A22 | Operational usability and migration | API, CLI and Console agree with real producer state and permissions; units and unavailable/observe/draining/debt states are truthful; defaults, directive and directory migration are actionable; alerts use real bounded signals and do not claim a general notification or audit product. |

The common A1–A18 conditions remain and also apply to A19–A22: corrected baseline `858590dc`, at
least three repetitions,
2 vCPU/768 MiB/no swap and `GOMAXPROCS=2`, peak below 576 MiB where specified, and the 10%
unsaturated throughput rule. Noise remains inconclusive, never a pass.

### Explicit non-goals

This proposal does not add hard whole-process/RSS isolation, kernel/socket accounting, general
bandwidth or rate policing, a generic filesystem quota service, arbitrary Coraza CPU preemption, an
embedded time-series database, a notification platform, automatic memory tuning, or a general
durable WAF audit product.

## Implementation slices

The original slice names and dependencies below are retained as history; the proposed split and
current sequencing are in the dated amendment above.

| Slice | Content | Depends on |
| --- | --- | --- |
| I1 | `internal/inspection` governor, which supersedes the prototype: counters, lease, slots, FIFO waits, overflow-safe arithmetic, `SetLimits`. Config schema, defaults and validation (including the full effective-route minimum-budget rule). Lifecycle registry ownership and Publish wiring. Metrics and status/CLI projection (observe-capable, with no enforcement points yet). Measure the base charge. | ADR accepted (2026-11-10) |
| I2 | Plugin request path: lease creation before `p.prepare`, grow-before-allocate in `readBounded`, fail-fast 503, `body_timeout` through the per-request deadline owner (sticky retirement abort), narrow `SetReadDeadline` forwarding where a wrapper lacks it, and guest replacement charging. | I1 |
| I3 | WAF request path: lease reuse or creation; a Jul read loop that replaces `ReadRequestBodyFrom`; slots and reservations around every rule-evaluating request call, including implicit phase 2 and phase 5 (§3); queue eligibility by unread data; `B_buf` and `B_total` charging; cumulative R1 and R2 with true-down; the deadline owner; the calibration test (`-tags waf`) covering the §4 list; and outcome taxonomy additions. | I1 |
| I4 | Response paths: nested plugin subscription buffers (`reason="budget"`), WAF response buffer (2×), phase-3 and phase-4 slots including implicit phase 4 in `WriteResponseBody`, actual-size R3 reservation before header ingestion (configured ceiling overflow fails closed with 500; governor saturation remains 503), R4, and the JSON/XML/header response calibration including repeated `Set-Cookie` and authentication flows. | I2, I3 |
| I5 | Maintained measurement tooling: promote the #695 harness, the #699 patch and the F2/F3/F4 clients into `scripts/inspection-assessment/` with unit tests for scenario definitions, summarisation and manifest checksums. | none (parallel) |
| I6 | Acceptance: run A1–A18 and publish a dated review with raw evidence and checksums. Docs: operator guide and sizing, the ADR 0017 caution, release notes and a `feature-status.yaml` entry (Beta/unreleased). | I4, I5 |

### I1 progress

I1 merged through #706 as `a57afa4d`. It adds the typed process governor, configuration
and generated contracts, process ownership, reload publication, bounded
metrics, status/CLI projection, and a checksummed lease-scaffolding
measurement. Status reports configured guest-memory capacity across compiled,
unclosed plugin generations, distinguishes it from live instance counts, and
leaves actual guest allocation and uncalibrated evaluation headroom explicitly
unknown. The route-aware minimum-budget rule (§Minimum-budget rule) is
computed from compiled WAF and plugin facts and applied consistently by
`jul check`, startup, reload and admin apply. At the I1 merge, live request paths were still
untouched. I2 subsequently integrated plugin request paths; WAF and response integration and
A1–A18 qualification remain pending.

### I5 progress

I5 merged through #708 as `d8d0520b`. It promotes the #695, #699 and F3/F4 instruments into
`scripts/inspection-assessment/` with a frozen plan, verified goodput, an
envelope check, checksummed run evidence and deterministic summaries. It
records baselines for `858590dc` and post-I1 main
(`docs/reviews/2026-11-12-700-i5-assessment-tooling-baselines.md`). F2 is not
ported because the I1 governor supersedes it. The baselines are measurement
evidence only: no A1–A18 criterion is satisfied by I5. The campaign found no
over-10% regression after added repetitions (the uninspected H1 cell remains
inconclusive through noise). On both runtimes, reload churn and single-connection
admission with WAF and plugins were OOM-killed, and stalled bodies ended only at
the client's limit; these are the baselines I2–I4 must change.

### I2 progress

I2 merged through #710 as `c8bba7f2c1b6c312aa215f8576014a513710775b` from reviewed head
`7d7a607b0b054e4655f14d61e57d3bf8ee9fa423`. It integrates the plugin request path:
one lease per plugin chain held through the downstream handler, prepared
bodies grown by charge-before-allocate, request-side host copies (`fetch`,
request state, generated and replacement bodies) charged with ownership
transfer, fail-fast `503`/`UNAVAILABLE`, and `body_timeout` through
`internal/reqdeadline` with the §6 amendment 8 timer and sticky abort. WAF
paths were completed in I3a; response-subscription buffers are being implemented in I4 on an
unmerged branch, so no A1–A18 criterion is claimed. Design, test matrix, measurements and reviews:
`docs/reviews/2026-11-12-700-i2-plugin-request-path.md`.

Measurement under harness plan v3 covers several behaviours:

- plugin-only saturation delivering `503` while probes progress;
- the observe equivalent;
- `408` at a 5 s `body_timeout` on H1, H2 and H3, with healthy siblings;
- the drain after reload and budget churn.

The unsaturated 10% rule found no pooled failure but stayed inconclusive because of host noise.
That makes throughput an I6 validation gap. Peaks above 576 MiB in the plugin cells come from
static guest pools or from ungoverned socket buffers (§10). They do not come from governed bytes.
The I2 PR also fixes a pre-existing retention of retired handler generations under reload churn
(#709). With the fix, the p4 reload cell peaks at about 500 MiB.

### I3a progress

I3a integrates the WAF request path (`internal/waf/admission.go`, `audit.go`, `outcomes.go`).

- **Admission order.** The WAF reuses an outer lease or creates one, then reserves the fixed R5
  term (64 KiB) before `NewTransaction`, so a refusal there leaves no Coraza transaction and no
  WAF outcome. Once the transaction exists, phase 5 always runs.
- **Slots and reservations.** Phase 1 (from `ProcessURI` through `ProcessRequestHeaders`) and
  phase 2 each take a slot and reserve their worst case first, then true down to the held
  `ARGS_GET`/`ARGS_POST` counts. A slot waits (bounded) only when the body has already been read,
  by an outer plugin or the read loop; otherwise it is try-acquired. Phase 5 uses the
  never-refused logging slot. Refusals after the transaction exists record the new WAF outcome
  `inspection_refused`, and the origin is not called.
- **Read loop.** Jul's loop replaces `ReadRequestBodyFrom` with the same limit semantics
  (`Reject` → 413 without evaluating; `ProcessPartial` → implicit phase 2 on the write that
  reaches the limit, under its own slot and worst-case R2, with the unread remainder forwarded
  unchanged). Its 32 KiB scratch buffer is charged before allocation and added to the route
  minimum. When governed, the body read runs under the `body_timeout` arrival deadline
  (408; an unenforceable deadline fails closed with 500).
- **Audit writer.** Jul registers its own `serial` writer, the only supported writer under a
  governor; `concurrent`, `https` and any other writer, or a formatter other than Native and
  JSON, are rejected at build time. That leaves no asynchronous sink, so the async-queue items
  do not apply. Before `ProcessLogging`, Jul either turns the transaction's audit engine off
  (no writer output) or reserves the construction estimate. A refusal drops the record (reason
  `budget`) but phase-5 rules still run. The writer charges the format estimate before
  formatting and hands a detached, formatted copy to an I/O goroutine with a 5 s deadline.
  An abandoned write stays charged until the goroutine returns. A test checks that a record
  abandoned mid-write is unchanged after its pooled transaction is reused.
- **Calibration.** `TestWAFReservationsCoverMeasuredAllocation` drives Coraza v3.8.1 with
  CRS v4.25.0 over query, large-header, urlencoded, mixed-at-`ArgumentLimit`, JSON, XML,
  multipart and raw requests. It runs them with audit parts H before K and K before H, a 1 KiB
  in-memory limit (spill), and `ProcessPartial`. It asserts that:
  - retained heap after phase 1 is within R1, and after phases 1–2 within R1 + R2 (cumulative:
    phase-2 rules matching `ARGS_GET` and headers retain state under R1's terms);
  - `ARGS_PATH` is empty;
  - audit construction and Native/JSON formatting allocations stay within their estimates.

  Two findings changed the estimator:
  - Coraza fills a K entry's `ErrorLog` only when H precedes K in `SecAuditLogParts`. Only that
    order is quadratic, so the bound now follows the part order.
  - `ErrorLog` allocates about 4.8× its length, so its bound now uses the exact truncated and
    quoted fields with a factor of 6.

  Part J's first use initialises Go's process-wide MIME table (about 0.7 MiB), so `New` loads it
  once instead of inside a governed record. With 1000 matching arguments, a full-parts record can
  exceed 2 GiB. R5 then refuses and drops it rather than building it.
- **Engine off.** A Jul policy cannot compile with the rule engine off: Jul appends
  `SecRuleEngine On` or `DetectionOnly` after every directive. A per-transaction
  `ctl:ruleEngine=Off` skips phase-5 rules, but Coraza still builds the audit record, so R5
  stays reserved. Coraza ignores body writes after that `ctl`, so Jul skips its read loop and
  forwards the body untouched; any bytes Coraza does not take are forwarded, never dropped.
- **Protocols.** Real-server tests cover overload (503 with the reason body) and `body_timeout`
  (408) on H1 and H2, with the H2 connection kept and an uninspected sibling served. The WAF
  H3 cell uses the same `reqdeadline` owner and overload writer as I2's harness-measured H3
  path; its integrated measurement belongs to I6b.
- **Hijack commit point.** An H1 hijack (101 upgrade, CONNECT) is a commit point: phase 5 runs,
  the outcome is recorded and transaction ownership and the WAF's lease end inside `Hijack`,
  while the tunnel is still open. Later writes bypass the closed transaction. One narrow
  exception: if the handler hijacks without draining the inspected body, that body is still
  read from the transaction's buffer, so ownership ends when the handler returns. A test checks
  both cases on a real server.
- **I4 in progress.** R3/R4 and the response buffer are implemented on the unmerged I4 branch
  with response-state/parser/partial and response-audit calibration. Non-hijack commit points
  release transaction ownership after inspection for flushed SSE over H2/H3 and native streaming
  gRPC; tests also verify H2/H3 sibling connection reuse. A nested production-chain test verifies
  WAF inspection of plugin-transformed output, retained charges under downstream backpressure,
  cleanup after client disconnect, and an early H2 WAF decision with the request body still open.
  An undrained inspected transaction body still retains ownership. Extended CONNECT is not
  applicable: Jul supports HTTP/1.1 Upgrade only and does not advertise RFC 8441/9220, per
  ADR 0018 §17. The I4 response-side matrix covers H1/H2/H3 rejection and streaming, early H1/H2/H3
  responses, and native gRPC status/trailers; the integrated H3 request-deadline measurement remains
  an I6b gate.

Each slice follows the repository coverage, race, profile, generated-contract, security-review and
Console gates that apply to its diff.

## Acceptance plan

**Common conditions:**

- **Environment.** The #700 envelope: 2 vCPU, 768 MiB, no swap, `GOMAXPROCS=2`. The objective is
  "peak below 576 MiB and no OOM".
- **Comparison baseline.** The **corrected** runtime `858590dc`, running the same mode and the same
  intended security behaviour. Workloads whose security outcome differs are never compared.
- **Repetitions.** At least 3 per cell; report the median and the spread.
  - A throughput cell passes only when its median regression is at most 10%.
  - A median regression above 10% fails, whether or not the ranges overlap.
  - A cell whose spread is too wide to decide, such as a baseline spread above 10%, is
    **inconclusive**. It is investigated and given more repetitions, and never counts as a pass.
  - Failing and inconclusive runs are preserved. Added repetitions supplement them and never
    replace them.
- **Identities.** Every run records source, binary, configuration, module and harness hashes.
- **Evidence.** Raw JSONL, manifests and `SHA256SUMS` are committed under `docs/reviews/evidence/`.
- **Reporting.** Legitimate goodput is reported separately from rejection throughput.

| # | Case | Pass condition |
| --- | --- | --- |
| A1 | s11 (`none`, `waf`) C=1 to 16 | No OOM, peak below 576 MiB, legitimate goodput at least 90% of the `slots=2` feasibility median (6.7/s), overload rejections are 503 within 50 ms |
| A2 | s3, s4 (`none`, `both`) | No OOM, peak below 576 MiB, goodput within 10% of `858590dc` wherever it was not OOM |
| A3 | s5 (`none`, `both`) with `max_pending_requests` at 0 and at 16 | No OOM; goodput at least 90% of the theoretical maximum with pending 0; the pending-16 result documented under the caution |
| A4 | Combined WAF + body plugin + s11 bodies | No OOM, peak below 576 MiB, per-class usage at or below the limit |
| A5a | s6 static, s7 cache hit and r2 no-body on routes with neither the WAF nor a plugin | No lease created (`jul_inspection_leases` = 0); throughput within 10% |
| A5b | The same s6, s7 and r2 shapes on inspected routes (WAF and a plugin) | A lease per request; WAF evaluations and plugin invocations are slotted and charged; under saturation they are rejected or queued per §3; no OOM, peak below 576 MiB |
| A6 | s8 and r1 slow and cancelling uploads, H1/H2/H3 | Stalls end at `body_timeout`; probes p99 below 50 ms; charges return to 0 after drain |
| A7 | A legitimate slow upload under `body_timeout` | Succeeds |
| A8 | Two listeners saturated together | Combined usage at or below one ceiling |
| A9 | Reload churn (s9) with overlap, limit decrease, rollback, mode toggle and listener removal | No acquisition succeeds that takes usage above the limit in force when it is made; usage already held may drain above a lowered limit; no counter reset; drain to 0 |
| A10 | Real H3: stalled stream plus sibling on one connection; H3 rejection | Deadline fires per stream; sibling unaffected |
| A11 | Cancellation during evaluation | Slot held until return; abandoned gauge rises and returns to 0; no early release (unit and race tests) |
| A12 | Response retention: plugin 8 MiB subscription and WAF response inspection | Charged and released at `tx.Close` or lease close; fail-closed on WAF budget |
| A13 | s1, s2, r3 (normal) and s10 (H3) | Throughput within 10% of `858590dc`, enforce mode, unsaturated |
| A14 | `mode = "observe"`, deterministic inputs: a fixed budget, fixed body sizes and requests held at known points by a gated backend | For each control in the §7 table, observe records exactly the would-reject decision that enforce returns for the same governor state. No governor-driven `503`, `408` or `500`. Existing WAF, plugin and timeout outcomes unchanged. Not compared with a separate enforce load run, because rejections change later load |
| A15 | Post-burst retention: A1 and A4 bursts, then idle | Governed usage returns to 0; live heap after two GCs returns within the static baseline plus documented pool retention |
| A16 | Non-body plugins: fetch at `maxFetchResp`, request state, generated body, and several nested response subscriptions | Each is charged before allocation; the minimum-budget validation rejects a budget below the chain's worst case |
| A17 | H2 body requests while every slot is busy | Phase 1 is rejected fail-fast without queueing; admitted streams on the same connection keep making progress (no F3 starvation) |
| A18 | Implicit evaluation while every slot is busy: request `ProcessPartial` with a body above the limit, the default response `ProcessPartial` with a response above the limit, and an early response before upload completion | Each implicit phase-2 or phase-4 evaluation holds a slot and its reservation; calls with unread request data try-acquire and fail fast; phase 5 runs for every inspected request and never returns `503`, running unslotted and counted when no slot is free; with a blocked audit writer, phases 1–4 are rejected per §3 (try-acquire, or a bounded wait and timeout) and nothing deadlocks |

Unit and race tests cover:

- overflow and refund arithmetic;
- growth charging of old and new arrays, and no refund on slice shortening;
- cumulative R1–R4 reservations with `ARGS_GET`, `ARGS_POST` and `ResponseArgs` each at
  `ArgumentLimit`;
- the deadline owner, where a retirement abort wins over concurrent completion or expiry (race
  test; H6 item 8 replaced restore);
- observe decisions for each control;
- nested leases;
- FIFO waits with abandonment;
- limit decrease and rollback;
- generation overlap;
- the uninterruptible charge;
- the calibration test.

## Qlty findings on #703

The maintainer reported a successful Qlty status on #703 with three "blocking" findings. Their
content was **not retrievable** with the authorised tooling:

- The commit status for head `68dd588e` reads `qlty check: success — 3 blocking issues`.
- No Qlty check run, annotation, review comment or PR comment exists. The only bot comment is from
  Codecov.
- The status target on qlty.sh requires an interactive login that is not available here.
- The repository contains no `.qlty` configuration from which to reproduce the findings locally.

The rule, location, whether each finding was introduced by #703 or pre-existed, and the practical
impact are all **unknown**. No clearance is inferred from the green status, and nothing is
suppressed.

- **Evidence still needed:** the three findings as exported from the Qlty dashboard for #703
  (rule ID, file:line, introduced or pre-existing).
- **Owner:** the maintainer, who has dashboard access.
- **Impact:** none known on this design.
- **If any finding proves to be a material defect:** it is filed against the owning area and
  sequenced with I2 (plugin body path), since #703 changed that code.

## Maintainer decisions

Recorded 2026-11-10 from the maintainer's review of #704. They form part of the accepted contract;
the ADR was accepted on 2026-11-10 when #704 merged. Decisions 1 and 2 still carry their stated
validation conditions.

1. **Default mode: `enforce`**, once implementation acceptance (A1–A18) passes; the 2026-11-13
   proposed amendment extends this gate to A19–A22 for the controls it adds. `observe` is an
   explicit migration option, not a default for a release. The architecture is accepted; the exact
   256 MiB default still needs validation (A1–A4).
2. **`body_timeout`: absolute, 60 s default, explicit `0s` disables it.** It is an initial safety
   policy for receiving inspected bodies, not an experimentally proven optimum. Stricter existing
   deadlines and later response streaming are preserved.
3. **Placement: `[global.inspection]`.** The authority belongs to the process and survives
   generations, which matches the existing `GlobalConfig` scope.

The review also required these contracts to be settled in the ADR. This revision settles them:

4. **Reservation semantics.** Buffered bytes are distinct from total processed bytes. Reservations
   R1–R4 are cumulative across per-collection limits, and WAF state is a calibrated estimate (§4).
5. **Acquisition coverage.** Every rule-evaluating entry point is covered, including implicit
   evaluation inside body writes. Phase 5 is slotted but never refused, overflowing visibly. Queue
   eligibility follows unread network data (§3).
6. **Buffer capacity.** Live capacity is charged, including both arrays during growth. Shortening a
   slice earns no refund, and #703's initial allocation is kept (§4).
7. **Observe behaviour** is defined for each control, with no governor-driven `503`, `408` or `500`
   (§7).
8. **Deadline ownership.** Wrappers forward `SetReadDeadline` narrowly, without `Unwrap`, and a
   retirement abort cannot be undone (H6; restore replaced by the 2026-11-12 amendment, H6 item 8).

## Related

- #700 (owner), #695 (assessment), #697, #698 and #699 (fixed), #62 (programme).
- [ADR 0017](0017-upstream-resilience-and-overload-control.md),
  [ADR 0011](0011-reload-plan.md), [ADR 0014](0014-operability-surfaces.md),
  [ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md),
  [ADR 0020](0020-wasm-abi-v2-response-phase.md),
  [ADR 0031](0031-waf-operations-posture-isolation-and-alerting.md),
  [resource ownership](../resource-ownership.md).
