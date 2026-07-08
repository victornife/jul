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
  cache/               # HTTP response cache (memory + disk backends)
  config/              # TOML config schema, parser, validation, and defaults
  handler/             # HTTP request handlers (static files, proxy, gRPC, plugins)
  middleware/          # Reusable middleware (compression, rate-limit, WAF)
  migrate/             # NGINX config importer
  observability/       # Metrics, tracing, access logging
  plugins/             # WASM plugin loader and runtime
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
- **Zero external runtime deps:** The shipped binary is statically linked and
  needs no interpreter or shared libraries (Go standard library + chosen deps).

## Composition-root helpers (`internal/app/`)

`cmd/jul/main.go` is the composition root — it initialises every subsystem,
wires dependencies, and coordinates reload.  To keep the root readable and
unit-testable, pure wiring helpers that depend only on lightweight packages
live in `internal/app/`:

| File | Responsibility | Tests |
|------|---------------|-------|
| `wiring.go` | Scope keys, upstream indexing, reload channel fan-in, `ValidateRuntimeConfig` | Yes (`*_test.go`) |
| `admin_deps.go` | Build `admin.Deps` from initialised subsystems (`BuildAdminDeps`, adapters) | Yes (`*_test.go`) |
| `preflight.go` | Admin write preflight gates (`Preflight.Apply` with `StreamPreflighter` iface) | Yes (`*_test.go`) |
| `runtime.go` | Process-lifetime subsystems behind their build-tag gates (`RuntimeBuilder`/`Runtime`: tracing, ACME, HTTP/3, stream server) | Yes (`*_test.go`) |
| `generation.go` | Generational handler teardown (`GenerationResources`: live closers + `poolReg` Begin/Commit/Abort staging) | Yes (`*_test.go`) |

Most of these helpers are **additive** — `main.go` calls them, they do not
change initialization order, and they do not replace the `buildHandlers`
closure.  `runtime.go` and `generation.go` (extracted under ADR-0007 / #30) are
the exception: they intentionally **own** the process-lifetime and generational
lifecycles respectively, which is what let `serve()` shed that bookkeeping.

When the ADR-0007 trigger fires (a cross-cutting change touching 3+ sections
of `serve()`, or a reload/preflight bug), new helpers should continue to
reside in `internal/app/` rather than being added inline.

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
