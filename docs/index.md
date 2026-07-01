# Jul.IA documentation

Welcome to the Jul.IA documentation. Jul.IA is an NGINX-inspired HTTP edge
server written in Go, configured through TOML, and shipped as a single static
binary.

## Choose your path

### New to Jul.IA?
- **[README](../README.md)** — What Jul.IA is, what it can do, and how to install it.
- **[Getting started](getting-started.md)** — Your first server: zero-config mode, a static site, a reverse proxy, and TLS.
- **[Configuration reference](configuration.md)** — Every config key, type, and default, with examples.
- **[Troubleshooting](troubleshooting.md)** — Common first-run and operational issues and their fixes.
- **[Concepts appendix](vision/appendix.md)** — New to HTTP, proxies, TLS, caching, or observability? Start here.

### Evaluating or operating?
- **[Feature status & GA matrix](status.md)** — What is GA, what is Beta, and what the maturity bar means.
- **[Deployment guide](deployment.md)** — systemd, Windows service, Docker, and log rotation.
- **[Observability](observability.md)** — Metrics, tracing, logging, health checks, and the admin API.
- **[Reload semantics](reload-semantics.md)** — What reloads safely, what needs a restart, and how zero-downtime reload works.
- **[Security model](../SECURITY.md)** — Threat model, hardening defaults, and per-feature security notes.
- **[Troubleshooting](../README.md)** — Common issues and how to resolve them.

### Feature deep-dives
| Feature | Document |
|---------|----------|
| Authentication | [docs/auth.md](auth.md) |
| Response cache | [docs/cache.md](cache.md) |
| Web console | [docs/console.md](console.md) |
| gRPC passthrough | [docs/grpc-proxy.md](grpc-proxy.md) |
| gRPC-JSON transcoding | [docs/grpc-transcoding.md](grpc-transcoding.md) |
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
- **[Engineering specs](specs/)** — Year-by-year execution plans and detailed feature specs.
- **[Roadmap](roadmap/README.md)** & **[Vision](vision/README.md)** — Where the project is headed.
- **[Release process](release.md)** — How releases are cut, versioned, and signed.
- **[Soak evidence log](soak-evidence.md)** — Dated soak runs and where the CI/release soak artifacts are published (ADR-0005 post-GA gate).
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

- **Code blocks** labelled `toml` are valid configuration examples.
- **Build-tag notes** appear under feature headings and in feature docs.
- **Defaults** are shown as `default value` in reference tables.
- **Cross-links** use relative paths so they work both on GitHub and when
  rendered by the Console.

## Status of this documentation

This documentation is maintained alongside the code. If a doc and the code
disagree, the code is the source of truth — please open an issue or PR. See
`docs/ga-push.md` for the current effort to move every feature from Beta to GA.
