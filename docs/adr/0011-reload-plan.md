# ADR 0011 — ReloadPlan: a single, side-effect-free reload transaction

- **Status:** Accepted — ReloadPlan transaction implemented; lifecycle registry single-sources restart classification (R5-01 through R5-17, 2026-07-19)
- **Date:** 2026-07-16 (updated 2026-07-19)
- **Deciders:** Jul.IA maintainers
- **Applies to:** configuration reload, secret resolution, listener lifecycle, upstream pool lifecycle, HTTP/3, ACME, admin preflight, lifecycle governance
- **Source:** Round 5 external re-audit (R5-01 through R5-17)

## Context

The reload path had evolved into an ordered sequence of in-place mutations across several independently mutable states: the global redaction registry, the handler generation pointer, upstream registry live/staged maps, TCP and HTTP/3 listener goroutines, raw and effective config pointers, certificate providers, the L4 stream runtime, and dynamic runtime policy (`log_level`, `worker_threads`).

Round 5 found that this ordering allowed candidate state to leak into live state before commit: secret expansion mutated global redaction during validation; HTTP/3 accept loops started before publish; TCP listeners served the previous generation while the new handler tree was still being built; upstream pool backends were visible before the new handler generation was published; and restart-required classification was duplicated across preflight, direct reload, pending-restart checks, and documentation.

## Decision

Adopt a single **`ReloadPlan`** value that owns every piece of candidate state from resolution through publish or abort, and a **`lifecycle.Registry`** that is the single source of truth for restart-required classification.

### 1. ReloadPlan transaction

`internal/server/server.go` defines `ReloadPlan` with the phases:

1. **Resolve** — expand secrets once and build the immutable `config.Candidate` (raw config, effective config, redaction state, secret digests, candidate fingerprint).
2. **Validate** — run structural/runtime validation on `Candidate.Effective`.
3. **Lifecycle** — compare `CandidateFP` against the bound startup fingerprint.
4. **Prepare** — build handlers and stage upstream/generation resources.
5. **StageListeners** — bind new TCP listeners and HTTP/3 resources without serving.
6. **Publish** — atomically install redaction state, swap configs, publish handler generation, and commit pool/generation resources.
7. **Activate** — start serving on staged listeners.
8. **Retire** — stop listeners no longer in the config and retire the old handler generation.
9. **Refresh** — reload TLS certificates.
10. **PostCommit** — apply dynamic side effects (`log_level`, `GOMAXPROCS`, stream reload).

On any failure before Publish, `Abort` releases all candidate resources without touching live state.

### 2. Pure secret resolution and redaction state

Secret expansion was moved from `internal/config/secrets.go` into `config.NewCandidate`, which returns a deep-copied effective config and a self-contained `redact.State`. The global redaction registry is updated only inside `ReloadPlan.Publish`, so validation, preflight, and aborted reloads cannot alter live redaction behavior.

### 3. Lifecycle registry

`internal/lifecycle/lifecycle.go` now declares every restart-required, new-listener-only, and hot-reloadable field in a single registry. `lifecycle.RestartRequired`, `lifecycle.PendingRestarts`, `lifecycle.FieldClass`, and `lifecycle.StartupFields` are consumed by:

- `Preflight.Apply` (admin write gate);
- `Server.doReload` (direct reload gate);
- pending-restart checks for the Console banner;
- diff warnings;
- docs generation/validation.

The registry also drives the effective startup fingerprint, which captures resolved values and file-content digests for startup-bound fields.

### 4. Activation order

Successful reload now publishes the handler generation before listeners start accepting:

```
commitFn()              // promote pools/generation
s.handlers.Store(...)   // publish new handler generation
stagedTCP.Activate()    // start serving TCP
stagedHTTP3.Activate()  // start serving HTTP/3
```

### 5. Generation-scoped pool snapshots

The handler factory receives an immutable `upstream.SnapshotMap` at commit time. In-flight requests from the previous generation continue using their own snapshot, so they never observe backends introduced or removed by a newer generation.

## Completed implementation table

| Component | File | Responsibility |
|-----------|------|----------------|
| `ReloadPlan` | `internal/server/reload_plan.go` | Transaction object owning candidate state and reload phases |
| `config.Candidate` | `internal/config/candidate.go` | Immutable raw + effective config, redaction state, and secret digests |
| `redact.State` | `internal/redact/state.go` | Self-contained redaction state installed only at Publish |
| `lifecycle.Registry` | `internal/lifecycle/lifecycle.go` | Single source of truth for field lifecycle classification |
| `Preflight.Apply` | `internal/app/preflight.go` | Admin write gate using live snapshot and lifecycle registry |
| `Server.doReload` | `internal/server/server.go` | Orchestrates `ReloadPlan` phases |
| `GenerationResources` | `internal/app/generation.go` | Generational handler-closer and pool staging lifecycle |
| `Registry` | `internal/upstream/registry.go` | Pool staging/reuse across reloads |
| `listenerEntry` | `internal/server/listener.go` | Staged TCP listeners with deferred activation |
| `h3Listener` | `internal/server/http3.go` | Staged HTTP/3 listeners with `Activate`/`Abort` |
| Stream reload | `internal/stream/server.go` | L4 stream runtime reload invoked in PostCommit |

## Consequences

- **Positive:** one object owns the reload transaction; validation and preflight are side-effect-free; secret rotation is detected via effective-value fingerprints; HTTP/3 obeys the staging contract; activation order guarantees listeners serve only a published generation; lifecycle classification is single-sourced across preflight, reload, pending-restart checks, and docs; older in-flight requests never observe a newer generation's backends.
- **Negative / trade-off:** a cross-cutting change touching config, redaction, app, server, upstream, admin, and docs; the orchestration in `internal/server/server.go` remains a policy choke-point.
- **Invariant:** no endpoint or failed reload can alter live redaction behavior; no listener serves a candidate generation before it is published; no old request observes a newer generation's pool backends.

## Related

- Round 5 audit findings R5-01 through R5-17
- `internal/server/server.go` — reload orchestration
- `internal/server/reload_plan.go` — `ReloadPlan` implementation
- `internal/app/serve.go` — composition root and file-watch
- `internal/app/factory.go` — `HandlerFactory.Prepare`
- `internal/app/generation.go` — generational resource lifecycle
- `internal/app/preflight.go` — admin write preflight
- `internal/upstream/registry.go` — pool staging
- `internal/config/candidate.go` — immutable candidate
- `internal/config/secrets.go` — secret expansion
- `internal/redact/state.go` — redaction state
- `internal/lifecycle/lifecycle.go` — lifecycle registry
- `docs/config-lifecycle.yaml` — field lifecycle manifest
- `docs/reload-semantics.md` — operator-facing reload semantics
- `docs/specs/reload-plan.md` — detailed implementation design
- `docs/specs/reload-tasks.md` — file-level task breakdown
