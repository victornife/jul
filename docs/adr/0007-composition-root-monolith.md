# ADR 0007 — Composition-root monolith (`cmd/jul/main.go`)

- **Status:** Deferred — recorded as technical-debt
- **Date:** 2026-06-30
- **Deciders:** Jul.IA maintainers
- **Applies to:** `cmd/jul/main.go`, runtime-initialization architecture
- **Source:** Post-audit review — external audit recommendation A-1

## Context

`cmd/jul/main.go` is the single entry point and composition root.  The `serve()`
function (~840 lines, lines 96–935) initialises logging, secrets, cache,
metrics, tracing, access logs, ACME, HTTP/3, the stream proxy, WAF, rate
limiter, upstream registry, plugin manager, stream server, and the handler
factory.  A nested `buildHandlers` closure (~300+ lines) assembles every
location's middleware chain (auth, rate-limit, WAF, compression) and action
builder (static, proxy, FastCGI, gRPC transcoding, plugins).

This concentration is not a "god package" in the domain-logic sense — business
logic lives in `internal/handler`, `internal/router`, `internal/upstream`, etc.
— but it *is* a policy choke-point: reload lifecycle, build-tag wiring,
generational resource teardown, dry-run preflight, metrics hooks, and feature
flag gating all meet in one function.  Each new cross-cutting feature increases
the probability of regressions in reload or preflight behaviour.

## Decision

**No refactor now.** The current seam (`serve()` + `buildHandlers` + factory
closures) is stable, well-tested, and working.  We will extract an
`internal/runtime` or `internal/app` package only when the following trigger
condition is met:

> **Trigger:** adding a new cross-cutting feature requires touching more than
> three distinct sections of `serve()` (e.g. init, preflight, factory wiring,
> generational cleanup, metrics hooks).

Until then, the debt is documented and monitored.

## Recommendations for future extraction

When the trigger fires, the extracted package should contain approximately these
seams, preserving the existing public contract with `internal/server`:

| Component | Responsibility |
|-----------|---------------|
| `RuntimeBuilder` | Feature-flag checks, ACME init, tracing init, HTTP/3 setup, stream server wiring |
| `PreflightPolicy` | Unify `validateRuntimeConfig` and the admin-apply dry-run path |
| `GenerationResources` | Manage `liveHandlerClosers`, `poolReg.Begin/Commit/Abort`, plugin-manager lifecycle |
| `ReloadPolicy` | Merge reload channels (`sigReload`, `fileWatch`, `adminReload`) and generational swap |

The existing `server.HandlerFactory` contract must remain unchanged — the server
package must not import the new runtime package.

## Consequences

- **Positive:** No churn on a stable, heavily-tested path; avoids the risk of
  introducing subtle reload bugs during a large refactor.
- **Negative:** Every new feature still adds cognitive load to `serve()`;
  reviewers must carefully inspect reload/preflight interaction.
- **Mitigation:** Any change to `serve()` or `buildHandlers` requires both the
  **lean** and **full** CI profiles to pass, including the race-detector job.

## Related

- `cmd/jul/main.go` — current composition root
- `internal/server/server.go` — handler-factory contract (`OnReloaded` hook)
- ADR 0003 — maturity model; this debt is explicitly classified as **Beta**
  architecture until resolution
