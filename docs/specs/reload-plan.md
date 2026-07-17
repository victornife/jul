# ReloadPlan implementation design

**Owner:** Jul.IA core team
**Date:** 2026-07-16
**Scope:** configuration reload transaction, secret handling, lifecycle governance, admin preflight, docs validation
**Depends on:** ADR 0011 — ReloadPlan: a single, side-effect-free reload transaction

## 1. Goal

Convert the current reload path from an ordered sequence of in-place mutations into a transaction that owns every candidate resource in a single `ReloadPlan` value. The transaction is:

- side-effect free until commit (validation can fail without live-state mutation);
- deterministic in restart-required classification (one registry);
- safe for HTTP/3 and TCP listener activation (barrier until publication);
- consistent for secret redaction (state installed only at commit);
- observable in diffs, admin preflight, and Console UI.

## 2. High-level transaction flow

```
                 ┌─────────────────────────────────────┐
   rawConfig     │  ResolveSecrets                     │
 ───────────────►│  (pure, no global mutation)         │
                 │  returns effective + redact.State   │
                 └──────────────┬──────────────────────┘
                                ▼
                 ┌─────────────────────────────────────┐
                 │  Validate                           │
                 │  on effective clone                 │
                 └──────────────┬──────────────────────┘
                                ▼
                 ┌─────────────────────────────────────┐
                 │  Lifecycle check                    │
                 │  candidate FP vs bound startup FP   │
                 └──────────────┬──────────────────────┘
                                ▼
                 ┌─────────────────────────────────────┐
                 │  Prepare                            │
                 │  handlers, pools, listeners, certs, │
                 │  stream plan, generation, closer    │
                 └──────────────┬──────────────────────┘
                                │
              failure ◄─────────┴─────────► success
                 │                              │
                 ▼                              ▼
      ┌────────────────────┐      ┌────────────────────┐
      │  Abort             │      │  Publish           │
      │  discard candidate │      │  atomic commit of  │
      │  close staged      │      │  effective/raw cfg │
      │  release resources │      │  redaction state   │
      └────────────────────┘      │  handler/pool gen  │
                                  │  bound fingerprints│
                                  └─────────┬──────────┘
                                            ▼
                                  ┌────────────────────┐
                                  │  Activate          │
                                  │  TCP/QUIC barriers │
                                  └─────────┬──────────┘
                                            ▼
                                  ┌────────────────────┐
                                  │  Retire            │
                                  │  drain old gen     │
                                  └─────────┬──────────┘
                                            ▼
                                  ┌────────────────────┐
                                  │  PostCommit        │
                                  │  log/worker policy │
                                  │  optional degraded │
                                  └────────────────────┘
```

## 3. Core types

### 3.1 `ReloadPlan`

```go
package server

import (
    "context"
    "net/http"

    "jul/internal/app"
    "jul/internal/config"
    "jul/internal/lifecycle"
    "jul/internal/redact"
    "jul/internal/server/certprovider"
    "jul/internal/stream"
    "jul/internal/upstream"
)

type ReloadPlan struct {
    ctx    context.Context
    cancel context.CancelFunc

    RawConfig       *config.Config
    EffectiveConfig *config.Config
    Redaction       redact.State
    SecretDigests   map[string]string

    StartupFP   lifecycle.Fingerprint
    CandidateFP lifecycle.Fingerprint

    Handlers       map[string]http.Handler
    HandlerCommit  func() func()
    HandlerAbort   func()
    HandlerCleanup func()

    UpstreamPlan upstream.Plan

    TCPListeners []*StagedListener

    HTTP3Listeners []*StagedHTTP3

    StreamPlan stream.Plan

    CertUpdates []certprovider.Update

    LogPolicy    LogPolicy
    WorkerPolicy WorkerPolicy
}

func (p *ReloadPlan) Abort() error
func (p *ReloadPlan) Publish() error
func (p *ReloadPlan) Activate() error
func (p *ReloadPlan) Retire() error
```

### 3.2 `redact.State`

```go
package redact

type State struct {
    values map[string]struct{}
    minLen int
}

func NewState(values []string, minLen int) State
func (s State) WithValue(v string) State
func (s State) WithMinLen(n int) State
func (s State) Apply(input string) string
func (s State) Writer(w io.Writer) io.Writer
func (s State) Count() int
func (s State) Clone() State

var live atomic.Pointer[State]

func Global() State
func Install(s State)
```

- The package-level `Replace`/`SetMinLen` mutators are deprecated and eventually removed.
- All log filtering uses `redact.Global().Apply` or `redact.Global().Writer`.
- Secret resolution returns a `State` that is installed only by `ReloadPlan.Publish`.

### 3.3 `lifecycle.Registry`

```go
package lifecycle

type Class int

const (
    HotReload Class = iota
    RestartRequired
    NewListenerOnly
)

type Entry struct {
    Path            string
    Class           Class
    Subsystem       string
    Reason          string
    StartupConsumed bool
    Gate            func(*config.Config) bool
}

type Fingerprint struct {
    Values map[string]any // registered startup-bound path -> value
}

var Registry []Entry

func RestartRequired(old, next *config.Config) (string, bool)
func NewListenerOnlyChanged(old, next *config.Config) bool
func PendingRestarts(startupFP, candidateFP Fingerprint) []string
func StartupFields() []Entry
func ComputeFingerprint(cfg *config.Config) Fingerprint
func LoadManifest(path string) ([]Entry, error) // from docs/config-lifecycle.yaml
```

The registry is populated at init from `docs/config-lifecycle.yaml` plus in-code overrides for fields the YAML cannot express (file-backed secret digests, runtime-derived values, `worker_threads` policy). A unit test asserts that the YAML entries are a subset of the code registry.

### 3.4 `upstream.Plan`

```go
package upstream

type Plan struct {
    Staged      map[string]*Pool
    Removed     []string
    NewNames    []string
    Changed     []string
    Commit      func() (old *Snapshot, cleanup func())
    Abort       func()
}

type Snapshot struct {
    Pools map[string]*Pool
    Gen   uint64
}

func (r *Registry) Plan(cfg *config.Config) (Plan, error)
```

`Plan` performs validation and staging; it does not update the live registry. `Commit` returns a generation-scoped snapshot that is handed to the handler factory. The factory embeds the snapshot in each `RequestContext` so that handlers cannot observe newer or removed pools.

### 3.5 `stream.Plan`

```go
package stream

type Plan struct {
    Configs []ListenerConfig
    Apply   func() (retire func(), err error)
    Abort   func()
}
```

The L4 runtime already has a reload path; wrap it in a `Plan` value so the server can abort it transactionally.

## 4. Phase-by-phase design

### 4.1 Resolve

```go
func (s *Server) resolveReload(raw *config.Config) (redact.State, *config.Config, lifecycle.Fingerprint, map[string]string, error) {
    // 1. deep copy raw so the original is never mutated
    expanded := raw.DeepCopy()

    // 2. resolve secrets and collect digests
    st, digests, err := secrets.Resolve(expanded)
    if err != nil {
        return redact.State{}, nil, lifecycle.Fingerprint{}, nil, err
    }

    // 3. validate TLS key pairs use the same cert source type
    if err := tls.ValidateKeyPairConsistency(expanded); err != nil {
        return redact.State{}, nil, lifecycle.Fingerprint{}, nil, err
    }

    // 4. compute effective fingerprint from expanded config
    fp := lifecycle.ComputeFingerprint(expanded)

    return st, expanded, fp, digests, nil
}
```

- `secrets.Resolve` returns `(State, map[secretRef]digest, error)`.
- File-backed secrets compute SHA-256 of the bytes read; env-backed secrets use a sentinel plus the environment variable name.
- The function must not call `redact.SetMinLen`, `redact.Replace`, or any other global mutator.

### 4.2 Validate

Run all validation that does not depend on live resources:

- TOML/schema validation (already done before this point).
- Handler tree construction errors.
- Upstream pool validation.
- Listener-address conflicts.
- mTLS/ACME consistency.

Validation operates on the expanded clone. Errors here abort the plan without touching live state.

### 4.3 Lifecycle check

```go
if reason, rr := lifecycle.RestartRequired(s.startupFP, candidateFP); rr {
    return fmt.Errorf("restart required: %s", reason)
}
```

- The check compares effective values, not raw references.
- `worker_threads` auto is resolved to the effective numeric cap before comparison.
- `log_format` is included in the fingerprint and therefore gated.
- ACME `cache_dir` and `ocsp_stapling` are included.

### 4.4 Prepare

Prepare creates candidate resources but does not publish them:

1. **Handlers** — `HandlerFactory.Prepare(raw, effective)` returns a generation, commit/abort callbacks, and a map of `listenAddr -> http.Handler`.
2. **Pools** — `Registry.Plan(effective)` returns staged pools and a commit callback.
3. **Listeners** — `buildListenerEntry` stages TCP sockets with a barrier.
4. **HTTP/3** — `buildHTTP3Listener` stages QUIC sockets without starting accept loops.
5. **Certificates** — `CertUpdates` are collected; private key material is not installed into live providers yet.
6. **Stream** — `Stream.Plan(effective)` builds L4 state.

All prepare steps append errors to a multi-error and, if any fail, the entire plan is aborted.

### 4.5 Abort

```go
func (p *ReloadPlan) Abort() error {
    p.cancel()
    p.HandlerAbort()
    p.UpstreamPlan.Abort()
    p.StreamPlan.Abort()
    for _, l := range p.TCPListeners { l.Abort() }
    for _, h := range p.HTTP3Listeners { h.Abort() }
    for _, u := range p.CertUpdates { u.Abort() }
    return nil
}
```

Abort must be idempotent and safe to call after partial Prepare.

### 4.6 Publish

```go
func (p *ReloadPlan) Publish() error {
    // 1. generation-scoped pool snapshot
    poolSnap, poolCleanup := p.UpstreamPlan.Commit()

    // 2. publish handler generation; this captures the pool snapshot
    p.HandlerCommit()

    // 3. atomically swap global live state
    s.mu.Lock()
    s.cfg = p.EffectiveConfig
    s.rawCfg = p.RawConfig
    s.startupFP = p.StartupFP
    s.handlers.Store(p.Handlers)
    s.poolSnapshot.Store(poolSnap)
    s.mu.Unlock()

    // 4. install redaction state
    redact.Install(p.Redaction)

    // 5. install bound fingerprints and pending-restart state
    s.boundFP.Store(p.CandidateFP)

    // 6. register pool cleanup with generation closer
    s.generation.AddCloser(poolCleanup)

    return nil
}
```

Publish is the only phase that writes live state. It must be short and protected where needed.

### 4.7 Activate

```go
func (p *ReloadPlan) Activate() error {
    for _, l := range p.TCPListeners { l.Activate() }
    for _, h := range p.HTTP3Listeners { h.Activate() }
    p.StreamPlan.Apply()
    for _, u := range p.CertUpdates { u.Apply() }
    return nil
}
```

Activation happens after Publish, so listeners never accept requests for a generation that is not yet live.

### 4.8 Retire

```go
func (p *ReloadPlan) Retire() error {
    // old generation closer is invoked after a grace period
    s.generation.ClosePrevious(p.ctx, gracefulDrainTimeout)
    return nil
}
```

The previous handler/pool/listener generation is closed only after the new generation is active and the old one has drained.

### 4.9 PostCommit

```go
func (s *Server) postCommit(p *ReloadPlan) error {
    p.LogPolicy.Apply()
    p.WorkerPolicy.Apply()

    // report optional subsystem failures truthfully
    if p.CertUpdates.HasFailures() {
        s.setLastReload("warning: reload succeeded but one or more certificate refreshes failed")
    }
    return nil
}
```

PostCommit must not fail the reload; it only updates runtime policy and status.

## 5. Listener staging details

### 5.1 TCP

```go
type StagedListener struct {
    ln       net.Listener
    barrier  chan struct{}
    handler  http.Handler
    server   *http.Server
}

func (l *StagedListener) Activate() { close(l.barrier) }
func (l *StagedListener) Abort() error { return l.ln.Close() }
```

The accept loop waits on `l.barrier` before handling the first connection. The barrier is closed by Activate after handler publication.

### 5.2 HTTP/3

```go
type StagedHTTP3 struct {
    conn       quic.EarlyListener
    server     *http3.Server
    acceptLoop chan struct{}
}

func (h *StagedHTTP3) Activate() error { close(h.acceptLoop); return nil }
func (h *StagedHTTP3) Abort() error    { return h.conn.Close() }
```

`buildHTTP3Listener` creates the `quic.EarlyListener` and `http3.Server` but does not start `Serve`. `Activate` starts the serve goroutine.

## 6. Generation-scoped pool snapshot

Each HTTP handler receives a `RequestContext` carrying the snapshot:

```go
type RequestContext struct {
    context.Context
    PoolGen uint64
    PoolSnapshot *upstream.Snapshot
    // ... existing fields
}

func (s *Server) handlerFor(req *http.Request, addr string) http.Handler {
    handlers := s.handlers.Load().(map[string]http.Handler)
    h := handlers[addr]
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ctx := &RequestContext{
            Context:      r.Context(),
            PoolSnapshot: s.poolSnapshot.Load().(*upstream.Snapshot),
        }
        h.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

Upstream lookups read from the snapshot; the snapshot is immutable for the generation.

## 7. Admin preflight and diff

### 7.1 Preflight

```go
func (p *Preflight) Apply(raw *config.Config) (*PreflightResult, error) {
    _, effective, fp, _, err := resolveReload(raw)
    if err != nil {
        return nil, err
    }

    if reason, rr := lifecycle.RestartRequired(p.server.boundFP(), fp); rr {
        return &PreflightResult{RestartRequired: true, Reason: reason}, nil
    }

    // run prepare without committing
    plan, err := p.server.planReload(effective, fp)
    if err != nil {
        return nil, err
    }
    defer plan.Abort()

    return &PreflightResult{Ok: true}, nil
}
```

### 7.2 Diff

Replace `diffConfigs` fallback with a structured, registry-driven diff:

```go
func Diff(old, new *config.Config) DiffResult {
    var res DiffResult
    for _, e := range lifecycle.Registry {
        oldV := getPath(old, e.Path)
        newV := getPath(new, e.Path)
        if !reflect.DeepEqual(oldV, newV) {
            res.Changes = append(res.Changes, Change{Entry: e, Old: oldV, New: newV})
        }
    }
    return res
}
```

The diff is complete by construction and can warn/reject based on `Class`.

## 8. Docs and governance

### 8.1 `docs/config-lifecycle.yaml`

Update the manifest so every config path has a `class` and `reason`. Example:

```yaml
lifecycle:
  - path: global.log_format
    class: restart_required
    reason: logging infrastructure initializes appenders on startup
  - path: global.worker_threads
    class: restart_required
    reason: GOMAXPROCS cap is set once at startup
  - path: listener.*.bind
    class: new_listener_only
    reason: address change requires a new listen socket
  - path: upstream.*.backends
    class: hot_reload
    reason: upstream registry supports staged replacement
```

### 8.2 `docs/reload-semantics.md`

Rewrite the reload semantics section to match the new transaction:

- validation is side-effect free;
- restart-required fields are single-sourced from `lifecycle.Registry`;
- HTTP/3 and TCP listeners are staged then activated after commit;
- secret content rotation requires restart if the value is startup-bound;
- pools are replaced generationally.

### 8.3 Validation

`scripts/docs-check.py` is updated to:

1. parse `docs/config-lifecycle.yaml`;
2. compare against the Go registry at runtime;
3. fail if a config field is missing, has no class, or the class disagrees with the code registry.

A new CI job runs `go test ./internal/lifecycle -run TestRegistryMatchesDocs`.

## 9. Testing strategy

### 9.1 Unit tests

- `redact`: `State` immutability, `Install` swaps live state.
- `lifecycle`: registry lookup, fingerprint stability, restart-required detection.
- `server`: abort after each Prepare sub-phase; verify no live mutation.
- `server`: listener barrier/activation ordering.
- `upstream`: plan commit returns correct snapshot; old snapshot remains valid.

### 9.2 Integration tests

- Rotate a secret file referenced by `mtls.ca_file`; assert reload is rejected with restart-required reason.
- Change `log_format`; assert preflight rejects.
- Change `upstream.X.backends`; assert active requests use old snapshot until drain.
- Add/remove an HTTP/3 listener; assert no dropped connections during the switch.

### 9.3 E2E

- Extend `internal/admin/ui/e2e/real-server.spec.ts` to verify that a rejected reload leaves redaction and handlers unchanged and that a successful reload switches traffic.

## 10. Migration plan

1. **Phase 0 — prep (1–2 weeks)**
   - Land ADR and this design doc.
   - Add `lifecycle` package and registry.
   - Add `redact.State` behind feature flag.

2. **Phase 1 — redaction + secrets (2–3 weeks)**
   - Convert `secrets.Resolve` to return `redact.State`.
   - Plumb `State` through all logging/filtering sites.
   - Remove `redact.Replace`/`SetMinLen` global mutation.

3. **Phase 2 — lifecycle + fingerprints (2 weeks)**
   - Implement `ComputeFingerprint`.
   - Replace hard-coded restart checks with registry.
   - Add `cache_dir` and `ocsp_stapling` to ACME fingerprint.

4. **Phase 3 — ReloadPlan skeleton (2–3 weeks)**
   - Introduce `ReloadPlan`.
   - Move handler preparation into plan.
   - Add abort semantics to each Prepare sub-phase.

5. **Phase 4 — listener + HTTP/3 staging (2 weeks)**
   - Implement TCP barrier and HTTP/3 staged start.
   - Reorder activation after Publish.

6. **Phase 5 — upstream snapshot (1–2 weeks)**
   - Generation-scoped pool snapshot.
   - Embed snapshot in `RequestContext`.

7. **Phase 6 — admin + docs (1–2 weeks)**
   - Preflight uses Resolve + Plan.
   - Diff uses registry.
   - Docs-check validates YAML against registry.

8. **Phase 7 — soak + hardening (2–3 weeks)**
   - Burn-in reload stress.
   - Fuzz config lifecycle transitions.
   - Audit log redaction under failure.

See `docs/specs/reload-tasks.md` for file-level tasks and estimates.
