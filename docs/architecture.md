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

These helpers are **additive** — `main.go` calls them, but they do not own
lifecycles, do not change initialization order, and do not replace the
`buildHandlers` closure or generational resource teardown.

When the ADR-0007 trigger fires (a cross-cutting change touching 3+ sections
of `serve()`, or a reload/preflight bug), new helpers should continue to
reside in `internal/app/` rather than being added inline.
