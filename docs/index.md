# Jul.IA documentation

Welcome to the Jul.IA documentation. Jul.IA is an NGINX-inspired HTTP edge
server written in Go, configured through TOML, and shipped as a single static
binary.

## Current audit and programme

- **[2026-08-03 combined repository re-audit](audit/combined-audit-2026-08-03.md)** — Current source of truth for repository health, confirmed defects, architecture decisions and implementation sequencing.
- **[Audit register](audit-register.md)** — Current audit pointer plus historical findings, fixes, tests and closure evidence.
- **[Stage 0/1 programme closure](audit/2026-08-05-stage-0-1-programme-closure.md)** — Exact-SHA maintainer certification, historical-audit disposition, residual transfers and the gate before #131.
- **[Master implementation tracker](https://github.com/victornife/jul/issues/62)** — Current staged execution programme and decision register.
- **[Project operating model](operating-model.md)** — Durable portfolio, execution, evidence and experiment rules.
- **[Core Gateway Completeness](specs/core-gateway-completeness.md)** — Bounded standalone-product completion contract.
- **[2026-07-31 full repository audit](audit/2026-07-31-full-repository-audit.md)** — Preserved historical audit and remediation evidence; no longer the active implementation plan.

## Choose your path

### New to Jul.IA?
- **[README](../README.md)** — What Jul.IA is, what it can do, and how to install it.
- **[Getting started](getting-started.md)** — Your first server: zero-config mode, a static site, a reverse proxy, and TLS.
- **[Configuration reference](configuration.md)** — Every config key, type, default, and validation rule, with examples.
- **[Configuration value contract](config-value-contract.json)** — Machine-readable enum, grammar, bound, activation, and zero-semantics inventory used by drift tests.
- **[Troubleshooting](troubleshooting.md)** — Common first-run and operational issues and their fixes.
- **[Concepts appendix](vision/appendix.md)** — New to HTTP, proxies, TLS, caching, or observability? Start here.

### Evaluating or operating?
- **[Feature status & GA matrix](status.md)** — What is GA, what is Beta, and what the maturity bar means. Read it together with the current audit; cache recertification is the active correctness programme.
- **[Known limitations](known-limitations.md)** — Documented gaps and current correction notices.
- **[Deployment guide](deployment.md)** — systemd, Windows service, Docker, and log rotation.
- **[Observability](observability.md)** — Metrics, tracing, logging, health checks, and the admin API.
- **[Prometheus metric contract](metrics-contract.json)** — Machine-readable names, types, help strings, labels, and released/additive state.
- **[Reload semantics](reload-semantics.md)** — What reloads safely, what needs a restart, and how zero-downtime reload works.
- **[Security model](../SECURITY.md)** — Threat model, hardening defaults, and per-feature security notes.
- **[Security posture](security-posture.md)** — Admin auth model, current limits, shipped local RBAC, and production hardening checklist.
- **[Security testing gates](security-testing.md)** — Dedicated RBAC/WAF/WASM negative matrices, package floors, exact baselines, and local commands.
- **[Troubleshooting](troubleshooting.md)** — Common issues and how to resolve them.

### Feature deep-dives
| Feature | Document |
|---------|----------|
| Authentication | [docs/auth.md](auth.md) |
| Response cache | [docs/cache.md](cache.md) |
| Web console | [docs/console.md](console.md) |
| gRPC passthrough | [docs/grpc-proxy.md](grpc-proxy.md) |
| gRPC-JSON transcoding | [docs/grpc-transcoding.md](grpc-transcoding.md) |
| Health checks | [docs/health.md](health.md) |
| Mutual TLS | [docs/mtls.md](mtls.md) |
| WASM plugins | [docs/plugins.md](plugins.md) |
| Secrets resolution | [docs/secrets.md](secrets.md) |
| Service discovery | [docs/service-discovery.md](service-discovery.md) |
| L4 stream proxy | [docs/stream-proxy.md](stream-proxy.md) |
| TLS + ACME | [docs/tls-acme.md](tls-acme.md) |
| WAF | [docs/waf.md](waf.md) |

### Extending and contributing
- **[Project layout](architecture.md)** — How the repository is organized.
- **[ADRs](adr/)** — Architecture Decision Records for durable design choices.
- **[Reviews & decision log](reviews/)** — Dated architecture reviews, product direction changes, and historical audit findings.
- **[Engineering specs](specs/)** — Detailed feature and implementation specifications.
- **[Roadmap v2.0](roadmap/README.md)** & **[Vision](vision/README.md)** — Current portfolio and durable direction; #62 owns issue-level sequencing.
- **[Release process](release.md)** — How releases are cut, versioned, and signed.
- **[Soak evidence log](soak-evidence.md)** — Dated soak runs and where CI/release soak artifacts are published.
- **[Changelog](../CHANGELOG.md)** — Release history.

## Build tags quick reference

Jul.IA optional features are compiled behind Go build tags:

| Tag | Feature |
|-----|---------|
| `acme` | Automatic HTTPS (Let's Encrypt) |
| `brotli` | Brotli compression |
| `console` | Admin web console |
| `consul` | Consul service discovery |
| `grpc` | gRPC passthrough + transcoding |
| `http3` | HTTP/3 over QUIC |
| `importer` | NGINX config migration |
| `kubernetes` | Kubernetes EndpointSlice discovery |
| `otel` | OpenTelemetry tracing |
| `stream` | L4 TCP/UDP proxy |
| `waf` | Web Application Firewall (Coraza) |
| `wasmplugins` | WebAssembly plugin runtime |
| `zstd` | Zstd compression |

Default binaries are **lean** (no tags). Build a **full** binary with:

```bash
go build -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" -o jul ./cmd/jul
```

## Documentation conventions

- **Code blocks** labelled `toml` are valid configuration examples unless a nearby correction notice states otherwise.
- **Build-tag notes** appear under feature headings and in feature docs.
- **Defaults** are shown as `default value` in reference tables.
- **Cross-links** use relative paths so they work both on GitHub and when rendered by the Console.
- **Current defects** link to their remediation issues and are not described as shipped future behavior.
- **Historical documents** keep their original evidence and carry an explicit historical relationship to the current audit.

## Status of this documentation

Current runtime behavior, executable tests, the combined audit, accepted ADRs,
and canonical feature/operational guides form the documentation contract. Open
correction notices live in their owning documents and linked issues rather than
in a separate temporary ledger. Historical GA-push and year-horizon material is
context, not the active execution sequence. Please open an issue or PR whenever
a document and current behavior disagree.
