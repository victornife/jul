# Jul.IA — Feature status & GA matrix

> Version 1.27 · Updated 2026-06-30

The single, canonical at-a-glance view of **every shipped feature**, its
**maturity**, and how it stands against the nine-criteria GA bar
([ADR 0003](adr/0003-maturity-and-ga.md)).

**Keep this current.** When a feature's maturity or any GA criterion changes,
update this file — it is step 2 of the roadmap
[Maintenance](roadmap/README.md#maintenance) checklist. Other documents point
here as the source of truth, so the per-doc tables cannot drift apart (the
condition that left Y2-07 mTLS listed as "remaining" after it had already
shipped).

Maturity ladder: **Alpha · Beta · GA — soak pending · GA · Deprecated**. Per
[ADR 0005](adr/0005-soak-post-ga-gate.md) the long-running soak test (criterion 5)
is a **post-GA gate**, so a feature that meets the other eight criteria is
labelled **GA — soak pending** until its soak run completes.

## GA criteria legend

| # | Criterion |
| --- | --- |
| 1 | Conformance / behaviour matrix |
| 2 | Published benchmark numbers |
| 3 | Known-limitations list |
| 4 | Semver-guarded config/API contract |
| 5 | Long-running soak test (**post-GA gate**) |
| 6 | Runnable example + docs |
| 7 | Security / threat note |
| 8 | Fuzzing where parsing is involved |
| 9 | Self-explanatory Console surface |

Cell key: ✅ met · ☐ open · n/a not applicable (no custom parser).

## GA — soak pending

Eight criteria met; only the soak test (5) is open. Per-feature detail lives in
each linked doc's *GA status* table.

| Feature | ID | Tag | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |
| --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |
| Core HTTP (static/proxy/FastCGI/vhosts/routing) | — | core | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | ✅ | ✅ | [core-http.md](core-http.md) |
| TLS + automatic HTTPS | Y1-01 | core · `acme` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | n/a | ✅ | [tls-acme.md](tls-acme.md) |
| Authentication (CIDR/Basic/JWT/forward-auth) | Y1-04 | core | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | ✅ | ✅ | [auth.md](auth.md) |
| gRPC ↔ JSON transcoding | Y2-01 | `grpc` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | ✅ | ✅ | [grpc-transcoding.md](grpc-transcoding.md) |
| Native gRPC passthrough + h2c | Y2-04 | `grpc` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | n/a | ✅ | [grpc-proxy.md](grpc-proxy.md) |
| mTLS client auth + `$ssl_client_*` | Y2-07 | core | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | n/a | ✅ | [mtls.md](mtls.md) |
| Console (operations cockpit) | Y1-07 · Y2-09 | `console` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | n/a | ✅ | [console.md](console.md) |

Criterion 8 is **n/a** for TLS + ACME, gRPC passthrough, mTLS, and the Console:
their parsing is delegated to the Go standard library (`crypto/x509`,
`encoding/json`) or the separately-tracked TOML config parser (Y1-08), or is an
opaque forward — none add a custom parser of their own to fuzz.

> **Continuous panels status** (Y2-09): Live log tail ✅ shipped; WASM plugin
> manager ✅ shipped (`.wasm` upload shipped v1.27); gRPC route designer ✅ shipped
> (visual designer with descriptor upload + method picker, v1.27).

## Beta (shipped; remaining GA gaps)

Feature-complete and in use, not yet through the GA bar. Gaps reference the
criteria above; see [ga-push.md](ga-push.md) for the per-feature push plan.

| Feature | ID | Tag | Remaining GA gaps (excl. soak) |
| --- | --- | --- | --- |
| Compression (gzip; brotli/zstd) | Y1-02 | `brotli`,`zstd` | ⑥ docs · ① encoder matrix · ② throughput bench · ⑦ BREACH note |
| Rate + connection limiting | Y1-03 | core | ⑥ docs · ① key/algorithm matrix · ② limiter bench · ⑦ bypass note |
| Active health checks | Y1-05 | core | ① probe matrix · ③ limits |
| Zero-config + `jul lint` | Y1-08 | core | ⑥ docs · ① lint-checks matrix · ⑧ TOML config-parser fuzz |
| NGINX config importer | Y1-09 | `importer` | ⑥ docs · ① directive-support matrix · ⑧ nginx.conf parser fuzz · ③ unmapped-directive limits |
| OTel tracing + access-log sinks | Y1-10 | `otel` | ⑥ docs · ① exporter/sink matrix · ② overhead bench · ⑦ PII note |
| HTTP/3 over QUIC | Y1-11 | `http3` | ⑥ docs · ① QUIC/Alt-Svc matrix · ② bench · ③ bind-time/no-WS · ⑦ 0-RTT/amplification |
| WASM plugin system | Y2-02 | `wasmplugins` | ① ABI/caps matrix · ② call-overhead bench · ⑦ sandbox note · ⑧ ABI fuzz |
| L4 stream proxy | Y2-03 | `stream` | ① TCP/UDP/SNI/PROXY matrix · ② throughput bench · ⑧ PROXY+SNI parser fuzz · ⑦ spoofing note |
| Service discovery / dynamic upstreams | Y2-05 | `consul`,`kubernetes` | ① provider matrix · ③ keep-last-good limits · ⑦ K8s-token/SSRF (docs ✅) |
| Web application firewall (WAF) | Y2-06 | `waf` | ① rule/CRS/mode matrix · ② request-overhead bench · ⑦ false-positive/bypass note (docs ✅) |
| Secrets references + log redaction | SEC-1 | core | ① ref-source matrix · ② resolve-cost bench · ⑦ leak/precedence note (docs ✅) |
| Response cache (memory + disk) | — | core | ⑥ docs · ① key/TTL/overflow matrix · ② hit/miss bench · ⑦ poisoning/isolation |

### GA evidence burndown (Beta)

The authoritative, per-release burndown of the GA evidence bundle for each Beta
feature. Columns are the evidence-bearing criteria: ① matrix, ② benchmark,
⑦ threat note, ⑧ fuzz, ⑤ soak. Cell key: ✅ done · ☐ open · n/a not applicable
(no custom parser to fuzz). A cell is ☐ exactly when it appears in that feature's
*Remaining GA gaps* row above; ⑤ soak is open for every Beta feature by
definition. Update this table as bundles land; a feature reaches GA when its row
is ☐-free except ⑤, then GA — soak pending until ⑤ closes.

| Feature | ① Matrix | ② Bench | ⑦ Threat | ⑧ Fuzz | ⑤ Soak | Open |
| --- | :-: | :-: | :-: | :-: | :-: | :-: |
| Compression | ☐ | ☐ | ☐ | n/a | ☐ | 4 |
| Rate + connection limiting | ☐ | ☐ | ☐ | n/a | ☐ | 4 |
| Active health checks | ☐ | ✅ | ✅ | n/a | ☐ | 2 |
| Zero-config + `jul lint` | ☐ | ✅ | ✅ | ☐ | ☐ | 3 |
| NGINX config importer | ☐ | ✅ | ✅ | ☐ | ☐ | 3 |
| OTel tracing + access-log sinks | ☐ | ☐ | ☐ | n/a | ☐ | 4 |
| HTTP/3 over QUIC | ☐ | ☐ | ☐ | n/a | ☐ | 4 |
| WASM plugin system | ☐ | ☐ | ☐ | ☐ | ☐ | 5 |
| L4 stream proxy | ☐ | ☐ | ☐ | ☐ | ☐ | 5 |
| Service discovery / dynamic upstreams | ☐ | ✅ | ☐ | n/a | ☐ | 3 |
| Web application firewall (WAF) | ☐ | ☐ | ☐ | n/a | ☐ | 4 |
| Secrets references + log redaction | ☐ | ☐ | ☐ | n/a | ☐ | 4 |
| Response cache (memory + disk) | ☐ | ☐ | ☐ | n/a | ☐ | 4 |

> A ✅ in this table means the criterion is not an open gap in the per-feature
> analysis above (docs may already exist even where ⑦ remains open — the "(docs
> ✅)" notes above refer to criterion ⑥). This burndown tracks the evidence
> bundle only; ③ limits, ④ semver contract, ⑥ docs, and ⑨ Console surface are
> tracked in [ga-push.md](ga-push.md).

## Soak tracking (post-GA gate)

Criterion 5 for the GA — soak pending features. A soak failure is a
release-blocking regression. Mirrors the
[GA push soak table](ga-push.md#soak-tracking-post-ga-gate-per-adr-0005).
Dated soak runs and where the CI/release artifacts are published are recorded in
the [soak evidence log](soak-evidence.md).

| Feature | GA on | Soak status |
| --- | --- | --- |
| Core HTTP | 2026-06-21 | ☐ pending |
| TLS + automatic HTTPS (Y1-01) | 2026-06-21 | ☐ pending |
| Authentication (Y1-04) | 2026-06-21 | ☐ pending |
| gRPC ↔ JSON transcoding (Y2-01) | 2026-06-21 | ☐ pending |
| Native gRPC passthrough (Y2-04) | 2026-06-21 | ☐ pending |
| mTLS client auth (Y2-07) | 2026-06-21 | ☐ pending |
| Console (Y1-07 · Y2-09) | 2026-06-23 | ☐ pending |

## Recently shipped continuous panels

**Y2-09 Console v2 continuous panels** (tracked above as part of the **Console**
row, Y1-07 · Y2-09):

| Panel | Status | Note |
| --- | --- | --- |
| Live log tail | **Shipped** | Operations panel, SSE stream, filter/pause |
| WASM plugin manager | **Shipped** | Plugins panel, structured CRUD + attach/detach; `.wasm` file upload included |
| gRPC route designer | **Shipped** | Descriptor upload (inspection only), `google.api.http` parse, visual mapping editor |

Deferred / demand-gated: **Y2-08** GraphQL composition. Time-boxed bet:
**AI-MVP** AI Gateway. Years 3–5 are the demand-gated vision horizon. See the
[roadmap](roadmap/README.md) for the full plan.

## Changelog

| Date | Ver | What changed | Source |
| --- | --- | --- | --- |
| 2026-06-30 | 1.27 | **Console continuous panels status correction:** the Console v2 Y2-09 remaining-work note is updated to show that live log tail, the WASM plugin manager, the gRPC route designer, and the `.wasm` upload incremental feature are all shipped. The roadmap `Not yet shipped` wording is corrected from "none" to an explicit panel table so the source of truth does not drift. | [status.md](status.md), [roadmap/README.md](roadmap/README.md) |
| 2026-06-29 | 1.26 | **gRPC transcoding upstream pools + plugin fetch truncation + re-audit residuals:** upstream pool support for `grpc_transcode` targets; plugin fetch response truncation guard; documentation and validation fixes from external audit round. | [status.md](status.md) |
