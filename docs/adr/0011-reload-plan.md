# ADR 0011 — ReloadPlan: a single, side-effect-free reload transaction

- **Status:** Proposed
- **Date:** 2026-07-16
- **Deciders:** Jul.IA maintainers
- **Applies to:** configuration reload, secret resolution, listener lifecycle, upstream pool lifecycle, HTTP/3, ACME, admin preflight, lifecycle governance
- **Source:** Round 5 external re-audit (R5-01 through R5-17)

## Context

The current reload path has improved substantially (handler preparation is now a three-phase prepare/commit/abort, TCP bind failure aborts the generation, raw config advances after success, bound listener fingerprints exist, stream errors propagate into `LastReload`). However, Round 5 identified that the transaction still spans several independently mutable states:

- process-wide redaction map and floor (`internal/config/secrets.go` → `redact.Replace` / `redact.SetMinLen`);
- handler generation pointer (`s.handlers`);
- upstream registry live/staged maps (`internal/upstream/registry.go`);
- generation closer ownership (`internal/app/generation.go`);
- TCP listener map and serve goroutines (`s.listeners`);
- HTTP/3 UDP/QUIC accept loops (`internal/server/http3.go`);
- expanded effective configuration (`s.cfg`) and raw source configuration (`s.rawCfg`);
- dynamic certificate providers (`dynamicCertProvider`);
- L4 stream runtime (`rt.Stream.Reload`);
- log level and GOMAXPROCS (`OnReloaded`);
- pending-restart and bound fingerprints.

The code has improved ordering but does not yet define one authoritative object that owns all candidate state and can abort or commit it coherently. As a result:

1. Secret expansion mutates global redaction state during validation and preparation; an aborted reload can leave candidate redaction state installed (R5-01).
2. Restart-required checks compare raw configuration references, so rotating the contents behind an unchanged secret reference silently bypasses restart detection (R5-02).
3. HTTP/3 starts its accept loop during listener staging, before commit (R5-04).
4. TCP listeners start serving before the handler pointer is swapped (R5-05).
5. Upstream pool backends are updated before the new handler generation is published (R5-05).
6. Admin preflight omits the `log_format` restart gate (R5-06).
7. The ACME startup fingerprint omits `cache_dir` and `ocsp_stapling` (R5-07).
8. `worker_threads = auto` does not restore the previous numeric GOMAXPROCS cap (R5-08).
9. Documentation and governance checks are presence-based rather than semantic (R5-10, R5-12, R5-13, R5-14).

## Decision

Introduce a single **`ReloadPlan`** value that owns every piece of candidate state, and a **`LifecycleRegistry`** that is the single source of truth for restart-required classification.

### 1. ReloadPlan

```go
type ReloadPlan struct {
    RawConfig       *config.Config          // unexpanded source config
    EffectiveConfig *config.Config          // expanded clone
    Redaction       redact.State            // values + minLength
    StartupFP       lifecycle.Fingerprint   // bound effective startup values
    CandidateFP     lifecycle.Fingerprint   // candidate effective values
    Handlers        map[string]http.Handler // per-listen-address handler tree
    HandlerCommit   func() func()           // promote handler generation
    HandlerAbort    func()                  // discard handler generation
    TCPListeners    []*StagedListener       // staged TCP listeners
    HTTP3Listeners  []*StagedHTTP3          // staged HTTP/3 listeners
    StreamPlan      stream.Plan             // L4 stream reload plan
    CertUpdates     []CertUpdate            // TLS provider refreshes
}
```

Phases:

1. **Resolve** — pure secret expansion and effective fingerprint computation; no global mutation.
2. **Validate** — structural/runtime validation on the expanded clone.
3. **Lifecycle** — compare candidate effective fingerprint against bound startup fingerprint; reject if any startup-bound value changed.
4. **Prepare** — build handlers, stage pools, stage listeners, stage certs, build stream plan; all still abortable.
5. **Abort** — close all candidate resources; no live-state writes.
6. **Publish** — one coordinator critical section: publish effective/raw config, redaction state, handler/pool generation, bound fingerprints.
7. **Activate** — release TCP and QUIC accept barriers.
8. **Retire** — drain old handler/pool/listener resources.
9. **PostCommit** — apply dynamic log level/worker policy and report degraded optional subsystem failures truthfully.

### 2. Pure secret resolution

`config.ExpandSecrets` is replaced by:

```go
func ResolveSecrets(raw *config.Config) (effective *config.Config, state redact.State, digests map[string]string, err error)
```

- Returns a deep-copied expanded config and a self-contained `redact.State`.
- Does not call `redact.SetMinLen` or `redact.Replace`.
- The caller installs `state` only at the Publish boundary.

### 3. Redaction State

```go
package redact

type State struct {
    values map[string]struct{}
    minLen int
}

func (s State) Apply(string) string
func (s State) Writer(io.Writer) io.Writer
func (s State) Count() int
func Global() State
func Install(State)
```

The global package retains an atomic `State` for the live runtime. Legacy helpers (`Snapshot`/`Restore`) are removed once all callers migrate.

### 4. LifecycleRegistry

A single Go registry declares every restart-required/new-listener-only/hot-reloadable field:

```go
package lifecycle

type Class int

const (
    HotReload Class = iota
    RestartRequired
    NewListenerOnly
)

type Entry struct {
    Path            string             // TOML path, e.g. "[global].log_format"
    Class           Class
    Subsystem       string             // "log_format"
    Gate            func(*Config) bool // optional runtime gate
    Reason          string
    StartupConsumed bool               // included in effective startup fingerprint
}

var Registry = []Entry{...}

func RestartRequired(old, next *config.Config) (string, bool)
func PendingRestarts(startup, current *config.Config) []string
func FieldClass(path string) Class
func StartupFields() []Entry
```

The registry is used by:
- `Preflight.Apply` (admin write gate);
- `Server.doReload` (direct reload gate);
- `PendingRestartCheck` (Console banner);
- diff warnings;
- docs generation/validation.

### 5. Effective startup fingerprint

For every field marked `StartupConsumed`, the registry captures the resolved value and, for file-backed values, a digest of the bytes actually consumed. The fingerprint is stored on the live `Server`/`Runtime` after initial startup and compared against the candidate fingerprint before Prepare.

### 6. HTTP/3 staging

Extend `h3Listener` with `Activate()` and `Abort()`:

```go
type h3Listener interface {
    Close(ctx context.Context) error
    Activate() error
    Abort() error
}
```

`buildListenerEntry` creates the UDP/QUIC resources but does not start the accept loop. `Activate` is called after Publish. `Abort` is called on any pre-commit failure.

### 7. Activation order

Reorder the successful reload so that handler publication precedes listener activation:

```
commitFn()            // promote pools/generation
s.handlers.Store(...) // publish new handler generation
for _, l := range stagedTCP { l.Activate() }
for _, h := range stagedHTTP3 { h.Activate() }
```

### 8. Generation-owned pool view

`Registry.Commit` continues to stage/replace pools, but the handler factory receives a generation-scoped snapshot of backends at commit time. Old in-flight requests use the snapshot, so they never observe backends introduced by a later generation or stopped by pool replacement.

## Alternatives considered

- **Keep the current ordering and add more equality checks.** Rejected: it does not solve the redaction side-effect problem, the secret-reference bypass problem, or the HTTP/3 pre-commit serving problem. It also requires maintaining duplicate gate lists in preflight, direct reload, pending status, and docs.
- **Make `ExpandSecrets` idempotent and keep global mutation.** Rejected: even idempotent mutation is unsafe during validation/preflight because a draft config can remove secrets that the serving config relies on.
- **Compute the effective fingerprint from raw config plus file mtime.** Rejected: environment-variable-backed secrets have no mtime, and the digest must reflect the bytes actually consumed, not the file metadata.
- **Version the pool internally.** Rejected: unbounded version growth and complex cleanup; snapshot at generation commit is simpler and sufficient.

## Consequences

- **Positive:** one object owns the reload transaction; side-effect-free validation/preflight; secret rotation is detected; HTTP/3 obeys the staging contract; activation order is correct; lifecycle classification is single-sourced; docs can be generated from code.
- **Negative / cost:** a cross-cutting refactor touching `internal/config`, `internal/redact`, `internal/app`, `internal/server`, `internal/upstream`, `internal/admin`, and docs. Estimated 10–16 engineer-weeks for the full remediation.
- **Invariant:** no endpoint or failed reload can alter live redaction behavior; no listener serves a candidate generation before it is published; no old request observes a newer generation's pool backends.

## Related

- Round 5 audit findings R5-01 through R5-17
- `internal/server/server.go` — current reload orchestration
- `internal/app/serve.go` — composition root and `OnReloaded`
- `internal/app/factory.go` — `HandlerFactory.Prepare`
- `internal/app/generation.go` — generational resource lifecycle
- `internal/upstream/registry.go` — pool staging
- `internal/config/secrets.go` — secret expansion
- `internal/redact/redact.go` — redaction registry
- `docs/config-lifecycle.yaml` — field lifecycle manifest
- `docs/reload-semantics.md` — operator-facing reload semantics
- `docs/specs/reload-plan.md` — detailed implementation design
- `docs/specs/reload-tasks.md` — file-level task breakdown
