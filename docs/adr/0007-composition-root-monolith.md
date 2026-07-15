# ADR 0007 — Composition-root monolith (`cmd/jul/main.go`)

- **Status:** Accepted — composition-root extraction complete; `main.go` < 100 LOC (CQ-2 / #54, 2026-07-15)
- **Date:** 2026-06-30 (updated 2026-07-15)
- **Deciders:** Jul.IA maintainers
- **Applies to:** `cmd/jul/main.go`, runtime-initialization architecture
- **Source:** Post-audit review — external audit recommendation A-1

## Context

`cmd/jul/main.go` was the single entry point and composition root. The `serve()`
function (~840 lines) initialised logging, secrets, cache, metrics, tracing,
access logs, ACME, HTTP/3, the stream proxy, WAF, rate limiter, upstream
registry, plugin manager, stream server, and the handler factory. A nested
`buildHandlers` closure (~300+ lines) assembled every location's middleware chain
(auth, rate-limit, WAF, compression) and action builder (static, proxy, FastCGI,
gRPC transcoding, plugins).

All composition-root logic has been extracted into `internal/app/` across four
scheduled milestones (SEQ-04 and CQ-2 / #54):

- **`BuildAdminDeps`** (`internal/app/admin_deps.go`): admin `Deps` wiring from initialized subsystems.
- **`Preflight.Apply`** (`internal/app/preflight.go`): the 6-gate admin write preflight.
- **`RuntimeBuilder` / `Runtime`** (`internal/app/runtime.go`): process-lifetime subsystem init (tracing, ACME, stream, feature gates). *(SEQ-04, 2026-07-08)*
- **`GenerationResources`** (`internal/app/generation.go`): generational handler-closer and pool-staging lifecycle. *(SEQ-04, 2026-07-08)*
- **`HandlerFactory`** (`internal/app/factory.go`): per-reload handler-tree builder (middleware chain, action builders, generational teardown). *(CQ-2 / #54, 2026-07-15)*
- **`app.Serve`** (`internal/app/serve.go`): full server orchestration (init, factory wiring, preflight, admin deps, server run, config file-watch). *(CQ-2 / #54, 2026-07-15)*

`cmd/jul/main.go` is now **91 LOC** (flag parsing, config loading, subcommand dispatch, and a one-line `app.Serve` call). The `< 250 LOC` target is met. Five characterization tests for `HandlerFactory` were added in `internal/app/factory_test.go`.

## Decision (original, 2026-06-30)

The original decision was "No refactor now" with a trigger condition. That trigger was satisfied by the CQ-2 audit milestone (#54), which scheduled the `HandlerFactory` and `app.Serve` extractions explicitly. This ADR is now closed.

## Completed extraction table

| Component | File | Milestone |
|-----------|------|-----------|
| `BuildAdminDeps` | `internal/app/admin_deps.go` | Pre-SEQ-04 |
| `Preflight.Apply` | `internal/app/preflight.go` | Pre-SEQ-04 |
| `RuntimeBuilder` / `Runtime` | `internal/app/runtime.go` | SEQ-04 (2026-07-08) |
| `GenerationResources` | `internal/app/generation.go` | SEQ-04 (2026-07-08) |
| `HandlerFactory` + `Build` | `internal/app/factory.go` | CQ-2 / #54 (2026-07-15) |
| `app.Serve` + `watchConfig` | `internal/app/serve.go` | CQ-2 / #54 (2026-07-15) |

## Consequences

- **Positive:** `main.go` is now a thin CLI entry point; every composition-root concern is unit-testable without a full process boot. Adding a new cross-cutting feature requires touching `internal/app/`, not `cmd/jul/main.go`.
- **Negative:** The orchestration logic in `internal/app/serve.go` (~320 LOC) is the new policy choke-point; it must be reviewed carefully on any cross-cutting change.
- **Mitigation:** Any change to `serve.go` or `factory.go` requires both the lean and full CI profiles to pass, including the race-detector job.

## Related

- `cmd/jul/main.go` — now a thin CLI shell
- `internal/server/server.go` — handler-factory contract (`OnReloaded` hook)
- `internal/app/` — all composition-root helpers
- ADR 0003 — maturity model
- ADR 0005 — soak post-GA gate
