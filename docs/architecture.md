# Jul.IA project layout

This document describes how the Jul.IA repository is organized.

## Overview

Jul.IA is a single-static-binary HTTP edge server written in Go, configured
through TOML. The repository follows a standard Go project structure with a few
domain-specific conventions.

## Directory structure

```
cmd/jul/               # Main entry point and CLI subcommands (jul serve, jul check, jul lint, jul fmt)
internal/
  admin/               # Admin HTTP API, web console backend, operational endpoints
  atomicfile/          # Atomic file writes used by config save and plugin upload
  auth/                # Authentication handlers (basic, JWT, forward-auth, CIDR)
  background/          # Generation-owned background-operation lease (context seam)
  cache/               # HTTP response cache (memory + disk backends)
  backendtls/          # Resolved outbound (backend) TLS policy shared by every transport
  clientaddr/          # Canonical client-address derivation and request-scoped identity
  config/              # TOML config schema, parser, validation, and defaults
  handler/             # HTTP request handlers (static files, proxy, gRPC, plugins)
  lifecycle/           # Machine authority for configuration reload behavior; generates the lifecycle mirrors
  middleware/          # Reusable middleware (compression, rate-limit, WAF)
  migrate/             # NGINX config importer
  observability/       # Metrics, tracing, access logging
  plugins/             # WASM plugin loader and runtime
  respwriter/          # Capability-preserving http.ResponseWriter wrappers
  router/              # HTTP request routing (prefix, exact, regex matching)
  server/              # HTTP/1, HTTP/2, HTTP/3 server construction and lifecycle
  signals/             # Graceful shutdown and config reload signal handling
  stream/              # L4 TCP/UDP stream proxy (build-tagged)
  tracing/             # OpenTelemetry tracer setup
  transcode/           # gRPC-JSON transcoding runtime
  upstream/            # Upstream pool management, health checks, discovery
  waf/                 # Web Application Firewall (Coraza integration)
docs/                  # Documentation (this tree)
examples/              # Runnable examples (auto-https, grpc-proxy, jwt-auth, etc.)
scripts/               # Development and CI helper scripts
testdata/              # Test fixtures and sample configs
deploy/                # systemd service files, Windows installers
docs/adr/              # Architecture Decision Records
```

## Key conventions

- **Build tags:** Optional features are compiled behind Go build tags
  (`acme`, `brotli`, `console`, `http3`, `otel`, `stream`, `wasmplugins`, etc.).
- **Config-driven:** All runtime behaviour is expressed through the TOML config
  schema in `internal/config/schema.go`.
- **One schema inventory, one lifecycle authority:** `config.SchemaPaths` is the
  single reflection over that schema, and `internal/lifecycle/registry.go`
  classifies every leaf it reports exactly once. The runtime never reads reload
  behavior from YAML, Markdown or JSON; `docs/config-lifecycle.yaml` and
  `docs/generated/config-lifecycle.{md,json}` are generated mirrors kept honest
  by `make generated-check`. An unclassified path fails closed rather than
  defaulting to hot reload.
- **Zero external runtime deps:** The shipped binary is statically linked and
  needs no interpreter or shared libraries (Go standard library + chosen deps).
- **One resilience subsystem:** admission, retry and circuit state are owned by
  `internal/upstream` and shared by every backend transport — HTTP, gRPC,
  transcoding, FastCGI, uWSGI and the L4 stream proxy — rather than reimplemented
  per protocol. Limits are per replica, not cluster-wide.
  [ADR 0017](adr/0017-upstream-resilience-and-overload-control.md) is the authority.
- **One route matcher, and the cache stores only what the origin sent:** route
  selection is a tiered enumeration over slices in declaration order — never a map
  iteration — and no surface reimplements it, including the admin route-test
  endpoint. Response-header policy and CORS are applied outside the response
  cache, and the cache captures response headers at header commit rather than
  re-reading the shared map afterwards, so nothing a layer outside the cache adds
  can enter a stored entry.
  [ADR 0018](adr/0018-bounded-route-matching-and-response-policy.md) is the proposed authority; the
  cache half already shipped in #327.

## Trust boundaries

Jul distinguishes seven trust boundaries. They are deliberately separate: each answers a different
question, is proved by a different mechanism, and fails in a different direction. Collapsing any pair
into a generic "trusted" notion reintroduces a known vulnerability class, so no single configuration
flag spans them. [ADR 0016](adr/0016-inbound-identity-and-backend-peer-trust.md) is the authority.

| | Boundary | Question | Proof | Failure |
| --- | --- | --- | --- | --- |
| **A** | Immediate transport peer | *What is the socket?* | Kernel fact | Cannot fail |
| **B** | Asserted original client | *Who does A claim came before it?* | A is inside `trusted_proxies` | Degrade to A |
| **C** | Auxiliary egress authorization | *May Jul connect outbound at all?* | Destination is allow-listed | Refuse the dial |
| **D** | Data-plane backend selection | *Which address do I dial?* | Routing, load balancing, discovery | Retry or eject |
| **E** | Backend peer identity | *Is that address the intended service?* | Certificate chain and name binding | Refuse the handshake |

**A and B — inbound identity.** The socket peer is always retained as a separate fact. Forwarding
headers are considered only when that peer is explicitly trusted by the matched listener's
`[servers.client_address]` policy; otherwise they are ignored entirely. One canonical client address is
derived once, per listen address and **before** virtual-host routing, and placed in the request context
by `internal/clientaddr` for every downstream consumer — CIDR authentication, rate limiting, the WAF,
access logs, upstream forwarding and the FastCGI environment. Deriving it before routing matters:
server blocks are selected by the `Host` header, so a policy resolved afterwards would let a client
choose the trust policy applied to its own request. `r.RemoteAddr` is never mutated.

**C — auxiliary egress.** `internal/egress` governs config-driven outbound clients (JWKS,
forward-auth, discovery, ACME/OCSP, plugin fetch). It authorizes a *destination*; it is not evidence
about who answered.

**D and E — backend trust.** Routing and discovery choose an address; they never establish identity.
A `backend_tls` policy — private roots, client certificate, SNI override, minimum version, explicit
peer identities — proves the peer is the intended backend. `internal/backendtls` resolves the public
block into one immutable `Policy`; every consumer receives that type and never parses public
configuration itself, which is what keeps a future named-profile feature a change to resolution
rather than a transport rewrite. `Policy.ClientConfig()` returns a fresh `*tls.Config` per consumer,
so a transport that sets `NextProtos` for HTTP/2 cannot affect another consumer of the same policy.
Peer identities are checked through `VerifyConnection`, after Go's standard chain and hostname
verification rather than instead of it. A discovery-returned address is a dial destination only: the
configured logical name remains the verified identity.

## Composition-root helpers (`internal/app/`)

The composition root — where every subsystem is initialised, dependencies are
wired, and reload is orchestrated — lives in `internal/app/serve.go` via
`app.Serve`. `cmd/jul/main.go` is a thin CLI dispatcher (≈90 LOC): it parses
flags and subcommands, loads the config source, and delegates to `app.Serve`.
This split was completed under ADR-0007 / #54 so the composition root can be
unit-tested independently of a full process boot.

`app.Serve` runs in four phases:

1. **Init** — logging, secrets, cache, metrics, and the process-lifetime
   runtime subsystems (tracing, ACME, stream server, build-tag feature gates)
   built once.
2. **HandlerFactory** — `HandlerFactory` holds the process-lifetime dependencies
   and rebuilds the per-listen-address HTTP handler tree on every reload.
   Generational teardown (`GenerationResources`) keeps old gRPC connections,
   plugin runtimes, and static handles alive until in-flight requests drain.
   Background work that legitimately outlives its request holds a **generation
   lease** so it is counted in the same drain accounting; see
   [Generation-owned background work](#generation-owned-background-work-internalbackground).
3. **Preflight** — `Preflight.Apply` is the admin-write validation gate:
   validate → TLS → handler dry-run → stream dry-run → bind probes →
   restart-required checks (ACME, listeners, tracing, access-log, cache,
   egress, admin, metrics).
4. **Admin deps** — `BuildAdminDeps` wires the Console and admin API, then
   `admin.New` starts the admin listener.

Supporting helpers in `internal/app/`:

| File | Responsibility | Tests |
|------|---------------|-------|
| `serve.go` | Composition root: four-phase startup, reload loop, process/generation lifetime | Yes (`*_test.go`) |
| `factory.go` | `HandlerFactory` — per-reload HTTP handler tree construction | Yes (`*_test.go`) |
| `wiring.go` | Scope keys, upstream indexing, reload channel fan-in, `ValidateRuntimeConfig` | Yes (`*_test.go`) |
| `admin_deps.go` | Build `admin.Deps` from initialised subsystems (`BuildAdminDeps`, adapters) | Yes (`*_test.go`) |
| `preflight.go` | Admin write preflight gates (`Preflight.Apply` with `StreamPreflighter` iface) | Yes (`*_test.go`) |
| `runtime.go` | Process-lifetime subsystems behind their build-tag gates (`RuntimeBuilder`/`Runtime`: tracing, ACME, HTTP/3, stream server) | Yes (`*_test.go`) |
| `generation.go` | Generational handler teardown (`GenerationResources`: live closers + `poolReg` Begin/Commit/Abort staging) | Yes (`*_test.go`) |
| `startup_restart.go` | Startup-bound subsystem restart checks (cache, egress, admin, metrics) | Yes (`*_test.go`) |

Each helper is independently testable and owns a well-defined lifecycle
responsibility. `serve.go` is the only file that assembles the full runtime;
the helpers keep it readable by extracting each phase into a focused,
testable unit.

## Generation-owned background work (`internal/background/`)

Some handler work legitimately outlives the request that started it — today the
response cache's `stale_while_revalidate` refresh. Left unmanaged, such work
escapes generational teardown and can use a gRPC connection, plugin runtime or
static root that a reload has already closed.

`internal/background` is the smallest durable seam that prevents this. It is a
dependency-free context seam, in the spirit of `internal/tracing`: the server
supplies the implementation, the cache consumes only the interface, and neither
package imports the other.

| Piece | Responsibility |
|------|---------------|
| `Operation` | Closed set of constants naming the work (`cache_revalidate`). Never caller-supplied, so it is safe as a metric label. |
| `Lease` | `Acquire(src, op) (ctx, release, ok)` plus `Generation()`. Installed in every request context by the server's dynamic handler. |
| `Group` | The concrete lease. Roots operations in a parent context, bounds each with a deadline, and calls `Admit`/`Done` hooks so the owner counts leased work in its own accounting. |
| `Detach` | The explicit request-context allow-list. Copies the generation upstream snapshot, mutual-TLS identity and request/trace ids; deliberately drops claims, plugin state, the client span, and client cancellation. |

`internal/server`'s `handlerGen` implements `Lease` by delegating to a `Group`
whose hooks drive the same `inflight` counter that requests use. The result is
that leased background work delays generation retirement exactly like an
in-flight request, while remaining cancellable by process shutdown and bounded by
`[global] shutdown_timeout`. See
[reload semantics](reload-semantics.md#generation-owned-background-work).

## Capability-preserving response writers (`internal/respwriter/`)

Every layer that observes or transforms a response wraps the
`http.ResponseWriter`: metrics, the access log, tracing, compression and the
response cache. A wrapper that implements nothing optional removes `Flush` (SSE
stalls) and `Hijack` (a WebSocket upgrade cannot take the connection); a wrapper
that implements everything and returns "unsupported" is worse, because handlers
branch on the type assertion rather than on the error, so an HTTP/2 stream is
told it can be hijacked.

`respwriter.Wrap(inner, under)` inspects `under` once and returns one of sixteen
shells implementing **exactly** the subset of `http.Flusher`, `http.Hijacker`,
`http.Pusher` and `io.ReaderFrom` that `under` implements, plus
`Unwrap() http.ResponseWriter` so `http.ResponseController` reaches the real
writer. An optional call goes to `inner` when `inner` implements the same
interface — that is how the cache intercepts a hijack to stop capturing — and
otherwise straight to `under`.

The guarantee composes: because every wrapper in the chain uses it, the innermost
handler sees the real connection's capability set no matter how many layers are
enabled.

## Large-file decomposition (`internal/admin/`)

Large first-party production files are decomposed into cohesive, same-package
files split by **seam** rather than by line budget (AUX-05 / #49, continuing
the #20 and #30 tranches). Same-package moves preserve every public and
package-internal API shape, so the decomposition is compiler-verified and
behaviour-preserving; no import graph or call site changes.

The admin configuration-patch surface is the worked example:

| File | Holds |
|------|-------|
| `patch.go` | `applyPatch` operation dispatch + apply logic (the behaviour) |
| `patch_types.go` | The JSON wire/DTO envelope and per-operation payload structs |
| `patch_builders.go` | Pure DTO→config builders and audit-summary formatters (unit-tested in `patch_builders_test.go`) |
| `server.go` | `Server` type, `New`, `Run`, auth, and the config/settings handlers |
| `routes.go` | The admin mux registration table (`routes()`) |

**Reproducible method** for the next target file:

1. Map the file's declarations and group them by seam (DTOs / builders /
   validators / routing / handlers / apply logic).
2. Extract the purest, least-coupled group first (types, then stateless
   helpers) into a new same-package file with a focused header comment.
3. Add characterization tests for any extracted logic that lacked direct
   coverage, so the seam is pinned before and after the move.
4. Verify both CI profiles (lean and the full build-tag set) build and pass —
   `consoleV2Compiled` and other tags gate admin behaviour, so both matter.

**Staged program.** Tier 1 (`cmd/jul/main.go` — via #30's `RuntimeBuilder`/
`GenerationResources`; `internal/admin/server.go`; `internal/admin/patch.go`)
is the first executed wave. Tiers 2–3 (`config/schema.go`, `admin/projections.go`,
`admin/diff_helpers.go`, and the remaining large files) follow the same method
in later tranches.
