# Jul.IA documentation

Welcome to the Jul.IA documentation. Jul.IA is a standalone edge and protocol
gateway written in Go, configured through TOML, and shipped as a static binary.

## Start with product truth

- **[Feature status and evidence](status.md)** — canonical human view of maturity
  and delivery; generated from/checkable against
  [`feature-status.yaml`](feature-status.yaml).
- **[Programme tracker #62](https://github.com/victornife/jul/issues/62)** —
  current issue-level execution state.
- **[Roadmap](roadmap/README.md)** — durable portfolio order, deliberately not a
  second issue tracker.
- **[Audit register](audit-register.md)** — current disposition of dated audits
  and historical evidence.
- **[Known limitations](known-limitations.md)** — active defects, deliberate
  limits, merged/unreleased constraints and restart-bound/deferred behavior.

## New to Jul.IA?

- **[README](../README.md)** — product scope, installation and capability
  overview.
- **[Getting started](getting-started.md)** — zero-config, static, proxy and TLS
  examples.
- **[Configuration concepts](configuration.md)** — human-authored structure,
  examples, trade-offs and workflows.
- **[Generated configuration reference](generated/config-reference.md)** —
  exhaustive field-level types, defaults, lifecycle, capabilities and values.
- **[JSON Schema](generated/config.schema.json)** and
  **[machine metadata](generated/config-metadata.json)** — generated machine
  contracts; runtime validation remains authoritative.
- **[Troubleshooting](troubleshooting.md)** — common first-run and operational
  problems.
- **[Concepts appendix](vision/appendix.md)** — HTTP, proxy, TLS, caching and
  observability foundations.

## Operating and evaluating

- **[Deployment](deployment.md)** — systemd, Windows service, Docker and log
  rotation.
- **[Reload, staging, authority and rollback](reload-semantics.md)** —
  transactional reload, planned restart, managed/file-owned authority and
  generation lifetimes.
- **[Generated lifecycle reference](generated/config-lifecycle.md)** and
  **[machine lifecycle metadata](generated/config-lifecycle.json)** — exhaustive
  field-level lifecycle truth from the Go registry.
- **[Observability](observability.md)** — logs, metrics, tracing, probes and
  runtime surfaces.
- **[Diagnostics and support bundles](diagnostics.md)** — deterministic local checks, bounded archives, privacy limits, and review-before-sharing guidance.
- **[NGINX migration corpus](nginx-migration-corpus.md)** — bounded importer evidence, exact fixture inventory, selected-dimension E2E, and explicit residuals.
- **[Prometheus contract](metrics-contract.json)** — metric names, types, labels
  and release state.
- **[Security model](../SECURITY.md)** and **[security posture](security-posture.md)**
  — threat boundaries and production hardening.
- **[Compatibility policy](compatibility.md)** — SemVer, deprecation and stable
  contract boundaries.
- **[Release process](release.md)** and **[soak evidence](soak-evidence.md)**.

## Feature guides

| Capability | Canonical guide | Status |
| --- | --- | --- |
| TLS + automatic HTTPS (ACME) | [tls-acme.md](tls-acme.md) | `GA` / `soaked` |
| Compression (gzip / Brotli / Zstd) | [compression.md](compression.md) | `GA` / `soaked` |
| Rate + connection limiting | [ratelimit.md](ratelimit.md) | `GA` / `soaked` |
| Authentication (CIDR / Basic / JWT / forward-auth) | [auth.md](auth.md) | `GA` / `soaked` |
| Active health checks (HTTP / TCP probes) | [health.md](health.md) | `GA` / `soaked` |
| Console (operations cockpit) | [console.md](console.md) | `GA` / `soaked` |
| Zero-config + jul lint | [zeroconf.md](zeroconf.md) | `GA` / `soaked` |
| NGINX config importer | [nginx-importer.md](nginx-importer.md) | `GA` / `soaked` |
| OTel tracing + access-log sinks | [otel.md](otel.md) | `GA` / `soaked` |
| HTTP/3 over QUIC | [http3.md](http3.md) | `GA` / `soaked` |
| gRPC ↔ JSON transcoding | [grpc-transcoding.md](grpc-transcoding.md) | `GA` / `soaked` |
| WASM plugin system | [plugins.md](plugins.md) | `GA` / `soaked` |
| L4 stream proxy | [stream.md](stream.md) | `GA` / `soaked` |
| Native gRPC passthrough + h2c | [grpc-proxy.md](grpc-proxy.md) | `GA` / `soaked` |
| Service discovery / dynamic upstreams | [service-discovery.md](service-discovery.md) | `GA` / `soaked` |
| Web application firewall (WAF) | [waf.md](waf.md) | `GA` / `soaked` |
| mTLS client auth | [mtls.md](mtls.md) | `GA` / `soaked` |
| Secrets references + log redaction | [secrets.md](secrets.md) | `GA` / `soaked` |
| Response cache (memory + disk) | [cache.md](cache.md) | `GA` / `soaked` |
| Core HTTP (static / proxy / FastCGI / vhosts / routing) | [core-http.md](core-http.md) | `GA` / `soaked` |
| Configuration reload transaction | [reload-semantics.md](reload-semantics.md) | `GA` / `soaked` |
| Trusted client address (client_address) | [configuration.md](configuration.md) | `Beta` / `merged` |
| Backend TLS trust (backend_tls) | [upstreams.md](upstreams.md) | `Beta` / `merged` |
| Auxiliary egress allow-list | [egress.md](egress.md) | `Beta` / `candidate` |
| Request predicates, response headers, and CORS | [core-http.md](core-http.md) | `Beta` / `merged` |
| Upstream resilience (admission, retry, circuit) | [upstreams.md](upstreams.md) | `Beta` / `merged` |
| Configuration authority and managed drift | [reload-semantics.md](reload-semantics.md) | `Beta` / `merged` |
| Generated configuration contracts and route identity | [generated/config-reference.md](generated/config-reference.md) | `Beta` / `merged` |
| NGINX migration assessment, provenance, and includes | [nginx-assessment.md](nginx-assessment.md) | `Beta` / `merged` |
| Local diagnostics and support bundles | [diagnostics.md](diagnostics.md) | `Beta` / `merged` |

Some capabilities share a canonical guide because they compose one subsystem.
The status manifest still gives each additive capability its own maturity and
delivery row where inheriting an older GA label would be misleading.

## Migration and automation

- **[NGINX importer](nginx-importer.md)** — translation boundary and supported
  constructs.
- **[NGINX migration assessment](nginx-assessment.md)** — schema-v2 findings,
  provenance, guidance and bounded include traversal.
- **[Configuration authority](reload-semantics.md#configuration-authority-managed-vs-file-owned)**
  — one writer at a time.
- **Generated external API:** not yet the stable contract. Existing Console
  routes remain internal unless explicitly classified by #150.

## Architecture and contribution

- **[Project layout and architecture](architecture.md)**.
- **[ADRs](adr/README.md)** — durable design decisions.
- **[Core Gateway Completeness](specs/core-gateway-completeness.md)** — approved
  standalone-product boundary.
- **[Operating model](operating-model.md)** — portfolio, evidence, WIP and
  experiment rules.
- **[Engineering specs](specs/)** and **[vision](vision/README.md)**.
- **[Contributing](../CONTRIBUTING.md)** and **[changelog](../CHANGELOG.md)**.

## Build tags

| Tag | Capability |
| --- | --- |
| `acme` | Automatic HTTPS |
| `brotli` / `zstd` | Optional compression encoders |
| `console` | Embedded operations Console |
| `consul` / `kubernetes` | Optional discovery providers |
| `grpc` | Native gRPC and transcoding |
| `http3` | HTTP/3 over QUIC |
| `importer` | NGINX migration tooling |
| `otel` | OpenTelemetry tracing |
| `stream` | L4 TCP/UDP proxy |
| `waf` | Coraza WAF |
| `wasmplugins` | WASM runtime |

The default binary is lean. The full profile uses the tag list maintained in CI,
release workflows and the Makefile.

## Documentation contract

Runtime behavior and generated contracts outrank prose. `feature-status.yaml`
owns maturity/delivery, #62 owns volatile execution, the roadmap owns durable
sequence, feature guides own operation and limitations, and the audit register
owns current audit disposition. Historical audits retain their original claims
and dates.

Open an issue or pull request when those sources disagree; do not create another
parallel status registry.
