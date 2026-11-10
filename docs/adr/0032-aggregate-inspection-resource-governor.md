# ADR 0032 — Aggregate inspection resource governor

- **Status:** Proposed
- **Date:** 2026-11-10
- **Deciders:** Jul.IA maintainer
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

> **Proposed design record.** This ADR selects a contract for #700. It does not add configuration
> fields, enforcement or metrics. It does not establish protection, release readiness, a maturity
> change or soak. Implementation follows only after the ADR is accepted.

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
   | Prepare body | `buffer` += B, grow-before-allocate |
   | Guest call | Plugin instance (existing bounded wait; guest memory is a static baseline); host fetch, state and copies charged as `buffer` before allocation |
   | Instance released | Bytes stay charged |
   | WAF finds the lease | No new admission |
   | Phase 1 | Slot (bounded wait here, because the plugin has already consumed the body; otherwise try-acquire), then reserve `waf_state` for the counted query pairs; release the slot and true down |
   | Body read into Coraza | `buffer` += 2×B, grow-before-read |
   | Phase 2 | Slot (bounded wait), then reserve worst-case `waf_state` (fail-fast); release the slot and true down to the held arguments |
   | `next.ServeHTTP` | No new charge |
   | Plugin response layer buffers | `buffer` += R, before append |
   | WAF response buffer | `buffer` += R2, before write |
   | Phases 3 and 4 | Slot; phase 4 reserves response state, then trues down |
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
     - the only wait is a short, bounded, FIFO **evaluation-slot wait**, entered only when the
       request has no unread body: either the body is complete and charged, or there is no body.
       Phase 1 for a request whose body is still unread try-acquires its slot and fails fast
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
   - A bounded wait only for evaluation slots, after the body is charged.
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
5. **Restoring the deadline.** `ResponseController` can set a deadline but cannot read the current
   one. The server entry point therefore stores the absolute read deadline it applied in the request
   context (`internal/server`). The inspection layer sets `min(stored, now + body_timeout)` and
   restores exactly the stored value afterwards. It never extends a stricter server deadline.
   - If a writer returns `http.ErrNotSupported` while `body_timeout > 0`, the request fails closed
     with `500` and the metric `jul_inspection_body_timeouts_total{protocol, result="unsupported"}`.
   - Tests (A10) must show that no shipped protocol path reaches that branch.
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
   - Whether Jul's wrapper chain unwraps to the quic-go writer is **unverified**. It is acceptance
     item A10.

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
- **Cost-free paths.** Requests that reach no inspection layer pay nothing:
  - static files;
  - cache hits served before the WAF in the route chain;
  - routes without the WAF and without plugins.
  The chain order is unchanged.

### 3. Counter classes and acquisition order

| Order | Class | Unit | Kind | Wait |
| --- | --- | --- | --- | --- |
| 1 | `base` | bytes (16 KiB per lease, to be calibrated in slice 1) | conservative estimate | **none** — fail-fast |
| 2 | `buffer` | bytes | exact capacity charged before allocation | **none** — fail-fast |
| 3 | `slots` | count | exact | bounded FIFO wait (`evaluation_queue_timeout`) **only when the request has no unread body**; otherwise try-acquire, fail-fast |
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
- **No queue before the body.** Phase 1 runs before the request body is read. A request that still
  has an unread body therefore never queues for a phase-1 slot: it try-acquires and receives a fail-fast
  503 when none is free. Queueing there would recreate the H2 flow-control starvation of F3. Requests
  with no body, or whose body a plugin has already prepared and charged, may queue. Phases 2, 3 and 4
  run after the request body is complete, so they may queue.

### 4. Byte accounting rules

- **Grow before allocate.**
  - A buffer is charged for its *capacity* before `make` or `append` grows it.
  - When `Content-Length` is known and within the layer limit, that length is pre-charged once, so
    the buffer never needs to grow.
  - Unknown lengths grow in doubling steps, each charged before growing.
  - The transient peak during growth, when the old and new arrays coexist, is at most 2× the
    capacity (F2 measured 1.92×). It is documented as overshoot.
  - When capacity is reached, it is trimmed to the final length.
- **Dishonest or unknown lengths.**
  - A `Content-Length` larger than the layer limit is rejected by the existing 413 path before any
    charge.
  - A body shorter than its declared length gets a refund of the unused capacity at end of body.
  - A body longer than its declared length is rejected with `413`, which is existing behaviour.
  - A chunked or unknown-length body starts with a 32 KiB pre-charge.
- **Arithmetic.** It is saturating and overflow-checked in `int64`: a charge whose addition would
  overflow fails as over-budget. Refunds never take a class below zero, and an attempt to do so is
  a bug that panics in tests.
- **Coraza request buffer.** Bytes passed to `ReadRequestBodyFrom` are charged as `buffer`, at 2×
  per byte up to the in-memory limit. That allows for the buffer's own growth. Spill to a temporary
  file beyond the in-memory limit is disk, not memory, and is excluded.
- **WAF state.** The estimate is `64 KiB + 56 KiB × A + 48 × B_mem + 48 × H`, where:
  - `A` is the number of arguments Coraza holds;
  - `B_mem` is the in-memory body;
  - `H` is the total request header and cookie bytes. Its factor of 48 is **inferred** and must
    be calibrated in I3.

  Amplification happens *during* a phase, so a post-phase check would come too late. The rule is
  therefore **reserve, then true down**:
  - **Before each request phase**, while holding the slot, Jul reserves the worst case the phase
    can reach:
    - phase 1 uses `A = min(ArgumentLimit, query pairs counted from the URL)`;
    - phase 2 uses `A = ArgumentLimit` (default 1000) and the actual `B_mem`.
  - **After the phase returns**, the reservation is trued down to the estimate for the arguments
    actually held. That count is `Variables().Args().FindAll()`, which covers `ARGS_GET`,
    `ARGS_POST` and `ARGS_PATH`, through Coraza's experimental `plugintypes.TransactionState`. If
    the type assertion fails, the reservation stays at the worst case.
  - **Before phase 4**, when response body inspection is active, Jul reserves response state of
    `48 × response limit + 56 KiB × ArgumentLimit` for `ResponseArgs` and `TX.json_response_body`.
    It trues it down afterwards, counting `Variables().ResponseArgs().FindAll()` rather than
    request `Args()`.
  - **Before phase 3**, Jul reserves response-header state of `48 × response header bytes` for the
    response header collections, then trues it down.
  - The calibration test (I3) pins the API and every constant. It covers request query, form,
    JSON, XML and multipart bodies, large headers and cookies, and JSON and XML responses.
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
| Admission (`base` or first `buffer` charge) fails | `503`, `Retry-After: 1` | `UNAVAILABLE` | `inspection_overloaded` (metric reason `bytes`) | Not read. H1: Go discards up to 256 KiB, then closes the connection. H2/H3: stream reset; siblings unaffected |
| A growth charge fails mid-body | `503`, `Retry-After: 1` | `UNAVAILABLE` | `inspection_overloaded` (`bytes`) | Partial body dropped; charge released |
| Worst-case `waf_state` reservation fails before a phase | `503`, `Retry-After: 1` | `UNAVAILABLE` | `inspection_overloaded` (`waf_state`) | Slot released first. The phase does not run, so its amplification never happens. The transaction is closed on its owning goroutine. The request is not forwarded, so no verdict is skipped |
| Slot wait times out, the queue is full, or phase 1 finds no free slot while the body is still unread | `503`, `Retry-After: 1` | `UNAVAILABLE` | `inspection_overloaded` (`evaluation`) | Any charged body is released. An unread body is handled as for admission failure |
| Plugin response buffer charge fails | Existing subscription policy for `bodyTooLarge` (ADR 0020) | — | `jul_plugin_response_body_unavailable_total{reason="budget"}` | Passthrough or fail-closed per subscription |
| WAF response buffer or slot unavailable | `503` (headers not yet sent while buffering) | `UNAVAILABLE` | `inspection_overloaded` | Fails closed: overload never skips response inspection |
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
  - The phase-3 true-down (Decision §4) lets the charge follow that reduction.
- **A prepared plugin body** stays charged until the lease closes. The request may still replay it,
  so an early refund would be unsafe.

### 7. Configuration

```toml
[global.inspection]
mode = "enforce"                 # "enforce" (default) | "observe"
max_buffered_bytes = "256MiB"    # Size, > 0; default 256 MiB
max_concurrent_evaluations = 0   # int, 0 = auto (GOMAXPROCS); 1..1024 otherwise
evaluation_queue_timeout = "1s"  # Duration, 0s..10s; 0s = no wait (pure fail-fast)
body_timeout = "60s"             # Duration; omitted = 60s; explicit "0s" disables
```

Example for the 2 vCPU / 768 MiB envelope (inferred sizing; it must be validated by A1–A3):

```toml
[global.inspection]
max_buffered_bytes = "192MiB"
max_concurrent_evaluations = 2
```

**Field semantics:**

| Field | Semantics |
| --- | --- |
| `mode` | `observe` charges and reports exactly as `enforce` does but never rejects, so operators can size before enforcing. There is no `off`: accounting is cheap, and an unmeasured process cannot be sized. |
| `max_buffered_bytes` | Required to be at least the largest single-request worst-case charge. Validation computes it from the effective configuration, for the worst route chain, as the sum of: `base`; the largest plugin `max_request_body`; 2 × the WAF in-memory body limit; the worst-case request `waf_state`; the request-phase fetch and state allocations of every plugin on the chain; and the response-phase allocations that can coexist — every subscribed plugin's `max_response_body`, its replacements, 2 × the WAF response limit and the worst-case response state. If the budget is smaller, validation fails with an error naming both fields, because some requests could never be admitted. |
| `max_concurrent_evaluations` | `0` resolves to `GOMAXPROCS` at Prepare. The effective value is reported. |
| `evaluation_queue_timeout` | Waiters are also capped internally at `16 × slots`. That count is a constant, not a field. |
| `body_timeout` | Applies only while an inspection layer reads a request body. Downloads, streaming responses and post-inspection reads are never affected. |
| All | Process-wide. Not overridable per listener or route. |

**Defaults are default-on.** Without them, #700's protection is opt-in and the shipped default stays
exposed.

**Compatibility:**

- **New 503s.** Upgrading can add overload 503s on saturation that previously risked out-of-memory
  kills.
- **New 408s.** Upgrading can add 408s for inspected uploads slower than 60 s.
- **Migration path.** Run `mode = "observe"` and watch peak usage and the would-reject counters, then
  enforce.
- **Release notes** must say all three.

Defaults, field placement and names, `mode` values and `body_timeout` semantics are public-contract
decisions (§Maintainer decisions).

### 8. Lifecycle under reload (ADR 0011)

- **Prepare.** Validate the new `[global.inspection]` and resolve `auto` against the current
  `GOMAXPROCS`. Construct no state.
- **Publish.** Atomically swap the `Limits` pointer.
  - Existing leases, charges and slot holders are untouched.
  - **Increase:** new admissions see more capacity immediately, and waiters are woken.
  - **Decrease below current usage:** existing work is never revoked. New admissions fail (bytes) or
    wait (slots) until usage drains below the new limit. Status reports `draining = true`.
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
| `jul_inspection_rejections_total` | counter | `reason` = `bytes`, `waf_state`, `evaluation`; `mode` = `enforce`, `observe` |
| `jul_inspection_body_timeouts_total` | counter | `protocol` = `h1`, `h2`, `h3`; `result` = `timeout`, `unsupported` |
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
  - `evaluation_headroom_estimate_bytes` = slots × the worst-case evaluation working set.

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

**Guaranteed in `mode = "enforce"`**, once implemented and verified by A1–A17:

1. Governed (logical) bytes stay at or below `max_buffered_bytes`, plus a transient growth
   overshoot of at most 2× the single largest growing buffer. Governed bytes are:
   - every prepared plugin body;
   - every Coraza in-memory request body, charged at 2×;
   - every plugin and WAF response buffer;
   - guest replacement bytes;
   - per-lease base charges;
   - plugin fetch responses, request state, generated bodies and temporary host copies;
   - worst-case WAF transaction state reserved for each phase, trued down after it.
2. Concurrent synchronous Coraza evaluations of phases 1–4 stay at or below
   `max_concurrent_evaluations`. Phase 5 (logging, which runs inside `tx.Close`) is not slotted; the
   number of leases bounds it.
3. Evaluation waiters stay at or below `16 × slots`, each waiting at most `evaluation_queue_timeout`.
   Plugin-instance waiters are bounded by leases and each waits at most one call timeout.
4. With `body_timeout > 0`, every inspection body-arrival phase ends within `body_timeout`, per
   stream, on H1, H2 and H3.
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
  - Coraza temporary-file spill, which is disk;
  - transient evaluation working set beyond retained state, which is bounded only through slots.
- **WAF state is an estimate.** It is calibrated per pinned Coraza and CRS. A future engine or rule
  set can exceed it until recalibrated.
- **Holding time is unbounded with `body_timeout = 0s`.** Held bytes remain bounded.
- **Uninterruptible evaluation** continues after cancellation, bounded by slot count × the
  per-evaluation duration.
- **Observe mode** provides no protection.

### 11. Ownership and lifecycle matrix

| Phase/resource | Scope and owner | What is charged | Acquire point | Release condition | Cancellation/retirement |
|---|---|---|---|---|---|
| Governor | Process; lifecycle registry | — (holds limits and counters) | Process start | Process exit | Survives every reload; limits swapped at Publish |
| Lease base | Request; first inspection layer | `base`, 16 KiB (to be calibrated) | Before any plugin invocation (`p.prepare` or `p.call`), or before `newTransaction` | `defer` in the creating layer after the inner handler returns | Released only on handler return; cancellation does not shorten it |
| Plugin prepared body | Request; plugin layer | `buffer`: exact capacity | Before each growth in `readBounded` | Lease close: request completion, since the body survives instance release | Read aborted by deadline, reset or retirement; charge released when the read returns |
| Plugin instance wait | Plugin, per generation | Nothing new (holds lease and bytes) | `acquire` | Instance obtained or call timeout | Context done ends the wait; bytes stay with the lease |
| Guest call and linear memory | Plugin pool, per generation | Static baseline (reported) | — | Pool retirement | Interruptible; instance discarded |
| Guest replacement bytes, fetch responses, request state, temporary copies | Request; invocation | `buffer` | Host function, before allocation or copy | Temporary copies when they are dropped; the rest at lease close | Released with the lease |
| WAF transaction state | Request; WAF layer | `waf_state`: worst-case reservation per phase, trued down to the held-argument estimate | Before each phase, while holding the slot | `tx.Close` | `tx.Close` only on the owning goroutine after the synchronous call returns |
| WAF request buffer | Request; WAF layer | `buffer`: 2× bytes up to the in-memory limit | Before each chunk read by `ReadRequestBodyFrom` | `tx.Close` | Read stopped by deadline; charge held until `tx.Close` |
| WAF evaluation (phases 1–4) | Process; slots | `slots` plus a worst-case `waf_state` reservation | Phase 1 with an unread body: try-acquire, fail-fast. Otherwise a bounded FIFO wait | The synchronous call returns; the reservation is then trued down | Not interruptible; stays charged until return |
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
   - New admissions receive 503 until usage falls below 96 MiB. No in-flight request is cut.
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
- **H3 deadline reach.** Whether Jul's writer chain unwraps to quic-go's `SetReadDeadline` is
  unverified (A10). If it does not, the wrappers must implement `SetReadDeadline` and `Unwrap`.
- **H3 sibling isolation after a rejection without reading the body** is inferred.
- **Base charge.** 16 KiB per lease is inferred.
- **Header factor.** The header and cookie factor (48 × H) is inferred.
- **Cost of the reservation.** At the default `ArgumentLimit` of 1000, one phase-2 reservation costs
  about 56 MiB, so `slots` × 56 MiB of the budget can be transiently reserved. Validated sizing for
  small budgets (A1–A4) may require a larger budget or a lower `SecArgumentsLimit`.
- **Phase-1 fail-fast** for requests with unread bodies may reduce goodput under saturation compared
  with queueing (A17). The deferred alternative is to receive the body into the charged buffer
  before phase 1.
- **Evaluation working set beyond retained state** (F1 shows 25 MiB of allocation churn for 991
  arguments) is bounded only through slots. Its live peak per slot in the envelope is unmeasured
  (A1).
- **Combined plugin and WAF behaviour in the envelope** is unmeasured (A4).
- **Feasibility measurements** are single or double repetitions, with c1 noise of 2.3–4.6/s.

## Implementation slices

| Slice | Content | Depends on |
| --- | --- | --- |
| I1 | `internal/inspection` governor, which supersedes the prototype: counters, lease, slots, FIFO waits, overflow-safe arithmetic, `SetLimits`. Config schema, defaults and validation (including the minimum-budget rule). Lifecycle registry ownership and Publish wiring. Metrics and the status object (observe-capable, with no enforcement points yet). Measure the base charge. | ADR accepted |
| I2 | Plugin request path: lease creation before `p.prepare`, grow-before-allocate in `readBounded`, fail-fast 503, `body_timeout` through `ResponseController` with restore, the H3 unwrap fix if needed, and guest replacement charging. | I1 |
| I3 | WAF request path: lease reuse or creation, slots around phases 1 and 2 (phase 1 try-acquire while the body is unread), Coraza body charging, worst-case `waf_state` reservation and true-down, the server-stored read deadline, the calibration test (`-tags waf`) covering query, form, JSON, XML, multipart, headers and cookies, and outcome taxonomy additions. | I1 |
| I4 | Response paths: nested plugin subscription buffers (`reason="budget"`), WAF response buffer (2×), phase-3 and phase-4 slots, response-state reservation (fail-closed), and the JSON/XML response calibration. | I2, I3 |
| I5 | Maintained measurement tooling: promote the #695 harness, the #699 patch and the F2/F3/F4 clients into `scripts/inspection-assessment/` with unit tests for scenario definitions, summarisation and manifest checksums. | none (parallel) |
| I6 | Acceptance: run A1–A17 and publish a dated review with raw evidence and checksums. Docs: operator guide and sizing, the ADR 0017 caution, release notes and a `feature-status.yaml` entry (Beta/unreleased). | I4, I5 |

Each slice follows the repository coverage, race, profile, generated-contract, security-review and
Console gates that apply to its diff.

## Acceptance plan

**Common conditions:**

- **Environment.** The #700 envelope: 2 vCPU, 768 MiB, no swap, `GOMAXPROCS=2`. The objective is
  "peak below 576 MiB and no OOM".
- **Comparison baseline.** The **corrected** runtime `858590dc`, running the same mode and the same
  intended security behaviour. Workloads whose security outcome differs are never compared.
- **Repetitions.** At least 3 per cell; report the median and the spread. A throughput cell fails
  only when the median regresses by more than 10% *and* the ranges do not overlap. Failing runs are
  preserved, not re-rolled.
- **Identities.** Every run records source, binary, configuration, module and harness hashes.
- **Evidence.** Raw JSONL, manifests and `SHA256SUMS` are committed under `docs/reviews/evidence/`.
- **Reporting.** Legitimate goodput is reported separately from rejection throughput.

| # | Case | Pass condition |
| --- | --- | --- |
| A1 | s11 (`none`, `waf`) C=1 to 16 | No OOM, peak below 576 MiB, legitimate goodput at least 90% of the `slots=2` feasibility median (6.7/s), overload rejections are 503 within 50 ms |
| A2 | s3, s4 (`none`, `both`) | No OOM, peak below 576 MiB, goodput within 10% of `858590dc` wherever it was not OOM |
| A3 | s5 (`none`, `both`) with `max_pending_requests` at 0 and at 16 | No OOM; goodput at least 90% of the theoretical maximum with pending 0; the pending-16 result documented under the caution |
| A4 | Combined WAF + body plugin + s11 bodies | No OOM, peak below 576 MiB, per-class usage at or below the limit |
| A5 | s6 static, s7 cache hit, r2 no-body | No lease created (`jul_inspection_leases` = 0); throughput within 10% |
| A6 | s8 and r1 slow and cancelling uploads, H1/H2/H3 | Stalls end at `body_timeout`; probes p99 below 50 ms; charges return to 0 after drain |
| A7 | A legitimate slow upload under `body_timeout` | Succeeds |
| A8 | Two listeners saturated together | Combined usage at or below one ceiling |
| A9 | Reload churn (s9) with overlap, limit decrease, rollback, mode toggle and listener removal | Usage never exceeds the published limit; no counter reset; drain to 0 |
| A10 | Real H3: stalled stream plus sibling on one connection; H3 rejection | Deadline fires per stream; sibling unaffected |
| A11 | Cancellation during evaluation | Slot held until return; abandoned gauge rises and returns to 0; no early release (unit and race tests) |
| A12 | Response retention: plugin 8 MiB subscription and WAF response inspection | Charged and released at `tx.Close` or lease close; fail-closed on WAF budget |
| A13 | s1, s2, r3 (normal) and s10 (H3) | Throughput within 10% of `858590dc`, enforce mode, unsaturated |
| A14 | `mode = "observe"` | No rejections; would-reject counters match the enforce run |
| A15 | Post-burst retention: A1 and A4 bursts, then idle | Governed usage returns to 0; live heap after two GCs returns within the static baseline plus documented pool retention |
| A16 | Non-body plugins: fetch at `maxFetchResp`, request state, generated body, and several nested response subscriptions | Each is charged before allocation; the minimum-budget validation rejects a budget below the chain's worst case |
| A17 | H2 body requests while every slot is busy | Phase 1 is rejected fail-fast without queueing; admitted streams on the same connection keep making progress (no F3 starvation) |

Unit and race tests cover:

- overflow and refund arithmetic;
- grow-before-allocate;
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

These are public-contract choices that remain open after the design work:

1. **Default-on enforcement.** Proposed: `mode = "enforce"` by default with 256 MiB and auto slots.
   The alternative is an `observe` default for one release.
2. **`body_timeout`.** Proposed: absolute, 60 s default, explicit `0s` disables it.
3. **Placement and names.** Proposed: `[global.inspection]` with the five fields above. The
   alternative is a top-level `[inspection]`.

## Related

- #700 (owner), #695 (assessment), #697, #698 and #699 (fixed), #62 (programme).
- [ADR 0017](0017-upstream-resilience-and-overload-control.md),
  [ADR 0011](0011-reload-plan.md), [ADR 0014](0014-operability-surfaces.md),
  [ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md),
  [ADR 0020](0020-wasm-abi-v2-response-phase.md),
  [ADR 0031](0031-waf-operations-posture-isolation-and-alerting.md),
  [resource ownership](../resource-ownership.md).
