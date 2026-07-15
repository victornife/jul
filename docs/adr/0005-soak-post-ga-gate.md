# ADR 0005 — Soak test reclassified to a post-GA stability gate

- **Status:** Accepted
- **Date:** 2026-06-21
- **Deciders:** Jul.IA maintainers
- **Applies to:** the GA bar in [ADR 0003](0003-maturity-and-ga.md) (criterion 5)
- **Source:** GA push decision 2026-06-21 (see [docs/ga-push.md](../ga-push.md))

## Context

[ADR 0003](0003-maturity-and-ga.md) defines a 9-criterion GA bar in which **all**
criteria are mandatory, including **criterion 5 — a long-running soak test**.
ADR 0003 already anticipated this revisit in its *Review triggers*: "The GA bar
proves impractical for edge features → consider a tiered bar in a superseding
ADR."

In practice, criterion 5 is the one criterion that cannot be satisfied by a focused
burst of work: it needs long wall-clock runtime and stable infrastructure, and it
lags behind the other eight (conformance, benchmarks, known-limitations, a
semver-guarded contract, docs/examples, a threat note, fuzzing, and a Console
surface) which together already establish production readiness. Holding the GA
label hostage to soak — for a solo, part-time project — would keep
production-ready, documented, benchmarked, hardened features pinned at Beta
indefinitely, which is its own form of dishonesty.

A deliberate push is underway to move the existing feature set from Beta to GA
(tracked in [docs/ga-push.md](../ga-push.md)). This ADR sets the bar that push
declares against.

## Decision

**The long-running soak test (ADR 0003 GA criterion 5) is reclassified from a GA
blocker to a continuous post-GA stability gate.**

1. A feature may be labeled **GA** once it satisfies criteria **1–4 and 6–9**:
   conformance matrix, published benchmarks, known-limitations list, a
   semver-guarded config/API contract, a runnable example + docs, a security /
   threat note, fuzzing where parsing is involved, and a self-explanatory Console
   surface.
2. The soak test **remains mandatory**, but runs **after** the GA declaration as a
   standing gate. Each GA feature carries an explicit, openly-tracked soak status
   (in [docs/ga-push.md](../ga-push.md)). A soak **failure** on a GA feature is a
   release-blocking **regression** — not a reason to retract the GA label, but a
   defect to fix.
3. Honesty is preserved by tracking soak status in the open rather than by
   withholding the GA label. The maturity ladder, the eight other criteria, and
   the *Operable by design* invariant ([ADR 0004](0004-console-ui-invariants.md))
   are unchanged.

This amends **only** criterion 5 of ADR 0003. The maturity ladder, the evidence
gates, and every other GA criterion stand as written.

## Minimum soak test parameters

Soak tests are categorised by scope. The table below defines the wall-clock runtime minimums and recommendations:

| Scope | Minimum duration | Recommended duration |
| --- | --- | --- |
| **Per feature** (e.g., rate limiting, compression, WAF) | **1 hour** | **8 hours** |
| **Multiple features / consolidated** (two or more features exercised together) | **4 hours** | **8 hours** |

Shorter runs are acceptable only as **smoke tests** (CI validation that the harness compiles and the feature does not immediately crash). They do **not** count toward the post-GA soak gate.

Rationale for the minimums:
- **1 hour per feature** is the floor at which most goroutine leaks, memory growth, and resource exhaustion patterns become observable under sustained load.
- **4 hours consolidated** balances cost with coverage: running multiple features together multiplies the interaction surface, so the minimum is higher than a single-feature smoke but lower than the ideal 8-hour run.
- **8 hours recommended** aligns with overnight/off-hours CI windows and captures slow-burn issues (e.g., gradual connection pool exhaustion, log rotation edge cases, certificate renewal timing windows) that a 1–4 hour run can miss.

## Rationale

- **Capacity.** Solo / part-time delivery cannot block the whole label on the one
  criterion that is dominated by wall-clock time and infrastructure.
- **The other eight already prove readiness.** Conformance + benchmarks + fuzzing +
  threat note + stable contract + Console operability is a strong, auditable bar.
- **Continuous, not gated.** Treating soak as an ongoing gate (like the perf and
  security gates) fits the "continuous hardening" posture better than a one-time
  pre-GA milestone.
- **Anticipated.** ADR 0003's own review trigger foresaw a superseding ADR if the
  bar proved impractical for parts of the surface.

## Consequences

**Positive**

- The existing, well-hardened feature set can earn an honest GA label now, with
  soak tracked transparently.
- Soak becomes a durable, repeatable gate rather than a one-off blocker.

**Negative / trade-offs**

- A GA feature can, in principle, be declared before a long soak has run — mitigated
  by (a) the eight remaining criteria, (b) open per-feature soak tracking, and
  (c) treating any soak failure as a release-blocking regression.

## Alternatives considered

- **Keep soak as a hard GA blocker (ADR 0003 as-is)** — rejected: pins
  production-ready features at Beta indefinitely under solo capacity.
- **Drop the soak test entirely** — rejected: long-run stability is real signal;
  it is reclassified, not removed.
- **A tiered bar (flagship vs. peripheral)** — deferred: a single bar with soak as
  a post-GA gate is simpler and applies uniformly.

## Review triggers

- A GA feature fails its post-GA soak → treat as a release-blocking regression and
  fix; do not silently retract GA.
- Soak infrastructure becomes cheap/continuous enough to fold back in pre-GA →
  revisit whether criterion 5 returns to a blocker.
- A new feature enters `GA — soak pending` → surface that state to operators in
  the Console Status panel and README feature table until the soak closes.
  Implementation tracked in [#55](https://github.com/victornife/jul/issues/55).
