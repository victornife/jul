# Service discovery (dynamic upstreams)

By default a `[[upstreams]]` pool has a static `servers` list. **Service
discovery** instead resolves the backend set from an external source and
refreshes the pool *live* — backends come and go without a config reload while
load balancing, passive health, and active health checks keep applying.

> **Troubleshooting:** an empty pool, stale backends, Consul/Kubernetes auth, and
> the discovery metrics to watch are covered in
> [troubleshooting.md](troubleshooting.md#service-discovery).

Four providers are available:

| Type | Source | Build | Carries |
| --- | --- | --- | --- |
| `dns` | A/AAAA records | core (always) | addresses (port from config) |
| `dns_srv` | SRV records | core (always) | host + port + weight |
| `consul` | Consul health API | `consul` tag | address + port + weight |
| `kubernetes` | Kubernetes EndpointSlices | `kubernetes` tag | address + port |

`dns` and `dns_srv` are compiled into every build. `consul` and `kubernetes` are
opt-in behind build tags:

```bash
go build -tags "consul kubernetes" -o jul ./cmd/jul
```

A binary built without a provider's tag still *parses and validates* a discovery
block of that type, but rejects it at load time (startup or reload) with a clear
error — the same fail-loud model as Jul.IA's other tagged features. Neither
provider links a vendor client library (no `client-go`, no Consul SDK); both
query the documented REST endpoint with the standard library, so enabling them
adds no third-party dependency.

## Contents

- [How it works](#how-it-works)
- [Configuration](#configuration)
- [DNS](#dns)
- [DNS SRV](#dns-srv)
- [Consul](#consul)
- [Kubernetes](#kubernetes)
- [Hot reload](#hot-reload)
- [Metrics](#metrics)

## How it works

- **Per-pool refresher.** When a pool has a non-static discovery type, a single
  goroutine resolves the source immediately, then re-resolves every `refresh`
  interval (with a little jitter) until the pool is closed.
- **State-preserving updates.** Each resolve is applied through the pool's
  `UpdateBackends`, which merges by address+weight: surviving backends keep their
  runtime state (in-flight count, passive-failure cooldown), new ones are added,
  removed ones drop out. Active health checks automatically begin probing
  newly-discovered backends.
- **Keep last-good.** A failed resolve — or one that returns *zero* targets — is
  logged, counted, and **skipped**: the pool keeps its previous backend set. A
  provider blip or a transient empty response never black-holes traffic.
- **Optional seed.** With discovery enabled the static `servers` list is
  optional. If present it seeds the pool until the first successful resolve; a
  discovery pool may also start empty (picks return "no available backend" until
  the first resolve lands).
- **Atomic reload.** Changing any discovery field rebuilds the pool (and restarts
  its refresher) on the next reload; an unchanged block reuses the running pool
  and its discovered backends untouched.

## Configuration

Discovery lives in an `[upstreams.discovery]` table on an upstream:

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `type` | string | `static` | `static` \| `dns` \| `dns_srv` \| `consul` \| `kubernetes` |
| `target` | string | — | Query for `dns` (`host:port`) and `dns_srv` (the SRV name) |
| `refresh` | duration | `30s` | Poll interval |
| `consul` | table | — | Consul settings (see below) |
| `kubernetes` | table | — | Kubernetes settings (see below) |

```toml
[[upstreams]]
name = "api"
strategy = "round_robin"
  [upstreams.discovery]
  type = "dns"
  target = "api.internal.svc:8080"
  refresh = "15s"
```

### Console workflow and secret boundary

The Apps creator supports `static`, `dns`, `dns_srv`, `consul`, and `kubernetes`
without switching to generated TOML. Non-static settings are emitted as one
`upstream_set_discovery` operation after any health-check operation and before an
optional route mount. Static discovery emits no operation. Existing discovery
pools are edited through the same one-operation preview path, and their already
configured Consul/Kubernetes token is preserved server-side when the provider
type is unchanged.

Typed App patches never carry a new token. When authenticated Consul or
Kubernetes creation requires one, the creator stops before preview and offers the
separately authorized raw configuration editor only to an identity with
`config:raw`; it never silently omits the token. Discovery owns the live backend
set, so the App detail drawer also hides manual add/remove controls until the pool
is returned to `static`.

## DNS

`type = "dns"` resolves a hostname's A/AAAA records and attaches the port from
`target` to every address (address records carry no port). Use it for headless
Kubernetes Services, internal DNS, or any name that fans out to multiple A
records.

```toml
  [upstreams.discovery]
  type = "dns"
  target = "api.internal.svc:8080"   # host:port — the port is applied to each address
```

## DNS SRV

`type = "dns_srv"` resolves SRV records, which carry the target host, port, and
weight directly — so no port is configured. `target` is the full SRV name.

```toml
  [upstreams.discovery]
  type = "dns_srv"
  target = "_grpc._tcp.svc.cluster.local"
```

SRV weights map to backend weights, so a weighted strategy (`strategy =
"weighted"`) honours the priorities published in DNS.

## Consul

`type = "consul"` queries Consul's health API
(`/v1/health/service/<service>`) and uses each passing instance's service
address+port. Requires the `consul` build tag.

```toml
  [upstreams.discovery]
  type = "consul"
    [upstreams.discovery.consul]
    address = "http://127.0.0.1:8500"   # default
    service = "web"                      # required
    tag = "v1"                           # optional: restrict to a tag
    datacenter = "dc1"                   # optional
    token = "..."                        # optional ACL token (X-Consul-Token)
    passing_only = true                  # default true
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `address` | string | `http://127.0.0.1:8500` | Consul HTTP API base URL |
| `service` | string | — | Service name to resolve (**required**) |
| `tag` | string | — | Restrict to instances carrying this tag |
| `datacenter` | string | — | Query a specific datacenter |
| `token` | string | — | ACL token, sent as `X-Consul-Token` |
| `passing_only` | bool | `true` | Only instances whose health checks pass |
| `tls` | table | — | Trust used to authenticate the agent over `https`; the same block as [`backend_tls`](upstreams.md) |

The instance service weight (`Weights.Passing`) maps to the backend weight.

### Authenticating the agent

The agent decides which addresses this pool dials, so it is authenticated on the
same terms as a backend (ADR 0016 §14 — Boundary F). Use an `https` address, and
add `[upstreams.discovery.consul.tls]` when the agent presents a private CA:

```toml
  [upstreams.discovery.consul]
  address = "https://consul.service.consul:8501"
  service = "web"
  token = "${file:/run/secrets/consul-token}"

    [upstreams.discovery.consul.tls]
    ca_file         = "/etc/jul/consul-ca.pem"
    ca_mode         = "file_only"
    peer_identities = ["dns:consul.service.consul"]
```

An `https` address without a `tls` block still verifies, against the platform
roots. **An ACL token over a plaintext `http://` address is readable and
replayable by anything on the network path**, so `jul lint` warns about it, and
`insecure_skip_verify` here is a lint **error** exactly as it is for a backend.

## Kubernetes

`type = "kubernetes"` lists the EndpointSlices of a Service
(`discovery.k8s.io/v1`) and uses each ready endpoint address with the selected
port. Requires the `kubernetes` build tag. In-cluster, the API server URL and
service-account token/CA are read from the standard pod locations; the fields
below override them when running outside a cluster.

```toml
  [upstreams.discovery]
  type = "kubernetes"
    [upstreams.discovery.kubernetes]
    namespace = "default"   # required
    service = "web"         # required
    port = "http"           # port name or number (default: first port)
    # api_server = "https://10.0.0.1:6443"   # default: in-cluster env
    # token = "..."                          # default: mounted SA token
    # ca_file = "/path/ca.crt"               # default: mounted SA CA
    # insecure_skip_tls_verify = false       # local testing only
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `namespace` | string | — | Target Service namespace (**required**) |
| `service` | string | — | Service name (**required**) |
| `port` | string | first port | Endpoint port by name or number |
| `api_server` | string | in-cluster env | API server base URL |
| `token` | string | mounted SA token | Bearer token |
| `ca_file` | string | mounted SA CA | API server CA bundle |
| `insecure_skip_tls_verify` | bool | `false` | Skip API server TLS verification (testing) |

Endpoints explicitly marked not-ready are skipped; an endpoint with no readiness
condition is treated as ready (matching Kubernetes semantics). The pod needs RBAC
to `list`/`watch` `endpointslices` in the namespace.

## Hot reload

Discovery pools take part in the normal atomic reload:

- An **unchanged** discovery block keeps the running pool and its live,
  discovered backends — the static seed is *not* re-applied over them.
- A **changed** block (any field) rebuilds the pool and restarts its refresher;
  the old pool is drained and closed.
- Switching an upstream **to or from** discovery rebuilds it as well.

## Backend identity

A backend's per-request state — in-flight count, failure history, health verdict — follows the
**workload**, not the address it happens to hold.

| Provider | Identity | Source |
| --- | --- | --- |
| Kubernetes | pod UID | `targetRef.uid` on the `EndpointSlice` endpoint |
| Consul | service ID | `Service.ID` |
| DNS, DNS SRV, static | none | the address is all there is |

This matters because an address is not an identity. Kubernetes recycles pod IPs within seconds, so
without it a replacement pod inherits the failure history of the one it replaced and arrives partway
to being taken out of rotation — for failures it never caused. With it, a refresh that reports the
same address under a **new** identity produces a **fresh backend with clean state**, and a refresh
that reports the same identity keeps everything it had.

A provider that offers no identity is unchanged: the address remains the reuse key, which is correct
for a DNS record or a static server list, where there is nothing else to go on.

> [!NOTE]
> **Consul re-registration now resets state.** A service that re-registers with a different
> `ServiceID` is treated as a logically replaced workload and starts clean. That is the intended
> semantic, and it is a behaviour change: previously the address alone decided.

The **dial** identity is deliberately separate and remains address-based. It answers "where do I
connect", which is why two workloads at one address are still one place to connect — retry exclusion
within a request must not re-try an address it has already tried just because the pod behind it
changed mid-request. Two keys, two questions.

## Metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `jul_upstream_backends` | gauge | `pool` | Current backend count (tracks discovery) |
| `jul_discovery_errors_total` | counter | `pool` | Failed/empty resolves (last-good kept) |

These complement the existing `jul_upstream_healthy` gauge and
`jul_upstream_probes_total` counter from active health checking, so a dashboard
can show both how many backends discovery currently sees and how many of them are
passing their probes.

## Local live integration runbook (issue #24)

Use this runbook to validate service discovery against live providers on a
developer machine (Docker Desktop + Kubernetes enabled). It executes two lanes:

1. **Consul lane:** Jul reads real Consul service-health entries and updates
  backends when an instance is deregistered.
2. **Kubernetes lane:** Jul reads real EndpointSlices from the Kubernetes API and
  updates backends when the EndpointSlice port is patched.

### Prerequisites

- Docker daemon running.
- Kubernetes context available (for example `docker-desktop`).
- Go toolchain present.

### One-command execution

From the repository root:

```powershell
.\scripts\test-discovery-live.ps1
```

The orchestrator runs both lanes and writes artifacts under `tmp/issue24/`.

### Lane scripts (individual)

Run only the Consul lane:

```powershell
.\scripts\test-discovery-consul-live.ps1
```

Run only the Kubernetes lane:

```powershell
.\scripts\test-discovery-k8s-live.ps1
```

### Expected evidence artifacts

Primary summary:

- `tmp/issue24/issue-24-evidence.md`

Consul lane artifacts:

- `tmp/issue24/consul-live.toml`
- `tmp/issue24/consul-before.txt`
- `tmp/issue24/consul-after.txt`
- `tmp/issue24/consul-jul.out.log`
- `tmp/issue24/consul-jul.err.log`
- `tmp/issue24/consul-summary.txt`
- `tmp/issue24/docker-issue24-consul.log`
- `tmp/issue24/docker-issue24-be1.log`
- `tmp/issue24/docker-issue24-be2.log`

Kubernetes lane artifacts:

- `tmp/issue24/k8s-live.toml`
- `tmp/issue24/k8s-live-manifests.yaml`
- `tmp/issue24/k8s-before.txt`
- `tmp/issue24/k8s-after.txt`
- `tmp/issue24/k8s-jul.out.log`
- `tmp/issue24/k8s-jul.err.log`
- `tmp/issue24/k8s-api.txt`
- `tmp/issue24/kubectl-proxy.out.log`
- `tmp/issue24/kubectl-proxy.err.log`
- `tmp/issue24/k8s-summary.txt`

### Pass criteria

Consul lane passes when:

1. `consul-before.txt` contains both `be1` and `be2`.
2. `consul-after.txt` contains only `be1` after Consul deregisters `web2`.

Kubernetes lane passes when:

1. `k8s-before.txt` (live API snapshot) contains EndpointSlice port `18081`.
2. `k8s-after.txt` (live API snapshot) contains EndpointSlice port `18082`
  after patch.
3. `k8s-summary.txt` reports `k8s_lane=PASS`.

### Issue closure format

Use `tmp/issue24/issue-24-evidence.md` as the source for the issue comment.

Acceptance mapping for issue #24:

1. Live Consul integration test exists and passes locally: **PASS** when
  `consul-summary.txt` reports `consul_lane=PASS`.
2. Live Kubernetes integration test exists and passes locally: **PASS** when
  `k8s-summary.txt` reports `k8s_lane=PASS`.

If both are PASS, close issue #24 as completed.

## CI automation for live service discovery (issue #46)

The local Consul lane above is also run **in CI** by the
[`discovery-live`](../.github/workflows/discovery-live.yml) workflow, so the live
convergence path is continuously enforced and not only proven on a developer
machine. The workflow reuses the exact #24 lane script — no separate test
framework — invoking it in a non-interactive CI mode:

```powershell
.\scripts\test-discovery-consul-live.ps1 -CI
```

### Which lane runs in CI, and why

Both the **Consul lane** and the **Kubernetes lane** are now automated in CI:

- **Consul lane** — [`discovery-live.yml`](../.github/workflows/discovery-live.yml):
  needs Docker + Go toolchain only; runs on `ubuntu-latest`. The PS1 script is
  invoked via `pwsh -CI`.

- **Kubernetes lane** — [`discovery-k8s-kind.yml`](../.github/workflows/discovery-k8s-kind.yml)
  *(new, Sprint 3)*: uses [kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker) on
  `ubuntu-latest`, with `helm/kind-action` to spin up a single-node cluster. The
  script `scripts/test-discovery-k8s-live.sh` mirrors the PS1 lane: applies an
  EndpointSlice to the kind cluster, starts jul with K8s discovery pointing at
  a local `kubectl proxy`, patches the EndpointSlice port 18081 → 18082, and
  asserts both the K8s API and **jul's admin upstream pool** converge. The
  `helm/kind-action` approach eliminates the Windows-only networking cmdlet
  dependency and the need for Docker Desktop.

### When the Kubernetes lane runs

- **On demand** — `workflow_dispatch`.
- **Nightly** — scheduled at 04:43 UTC.
- **On change** — pull requests and pushes touching `internal/upstream/**`,
  `scripts/test-discovery-k8s-live.sh`, or the workflow.

### CI evidence and failure diagnostics

Every run — green or red — uploads the full `tmp/issue24/` evidence bundle as the
`discovery-live-consul-evidence` artifact, mirroring the local evidence style.
The lane script additionally captures each container's `docker logs` into
`tmp/issue24/docker-issue24-*.log` **before** teardown, and on failure the
workflow prints the summary, the pre/post response windows, the jul logs, and
those container logs straight into the job log so a flake is actionable without
downloading the bundle.

To reproduce a **controlled failure** (verifying that failures surface with
actionable artifacts), break the convergence precondition — for example stop one
backend before the deregistration step (`docker stop issue24-be1`) or point the
config at a Consul address that is not serving: the lane throws on the missed
assertion, exits non-zero, and still emits `consul-summary.txt` with
`consul_lane=FAIL` plus the captured logs.

## Behaviour matrix

The matrix below enumerates every supported capability for each discovery
provider so operators can choose the right source for their deployment.

| Capability | `static` | `dns` | `dns_srv` | `consul` | `kubernetes` | Build tag |
| --- | :-: | :-: | :-: | :-: | :-: | --- |
| **Resolution** | | | | | | |
| Static server list | ✅ | ☐ | ☐ | ☐ | ☐ | core |
| DNS A/AAAA lookup | ☐ | ✅ | ☐ | ☐ | ☐ | core |
| DNS SRV lookup | ☐ | ☐ | ✅ | ☐ | ☐ | core |
| Consul health API | ☐ | ☐ | ☐ | ✅ | ☐ | `consul` |
| Kubernetes EndpointSlices | ☐ | ☐ | ☐ | ☐ | ✅ | `kubernetes` |
| **Address selection** | | | | | | |
| Host + port from source | ☐ | ☐ | ✅ | ✅ | ✅ | — |
| Port from config `target` | ☐ | ✅ | ☐ | ☐ | ☐ | — |
| Weight from source | ☐ | ☐ | ✅ | ✅ | ☐ | — |
| Health-filtered instances | ☐ | ☐ | ☐ | ✅ | ✅ | — |
| **Operational** | | | | | | |
| Periodic refresh | ☐ | ✅ | ✅ | ✅ | ✅ | — |
| Configurable refresh interval | ☐ | ✅ | ✅ | ✅ | ✅ | — |
| Keep-last-good on failure | ☐ | ✅ | ✅ | ✅ | ✅ | — |
| Keep-last-good on empty result | ☐ | ✅ | ✅ | ✅ | ✅ | — |
| Static seed (until first resolve) | ☐ | ✅ | ✅ | ✅ | ✅ | — |
| State-preserving update (in-flight, cooldown) | ☐ | ✅ | ✅ | ✅ | ✅ | — |
| **Integration** | | | | | | |
| Active health checks on backends | ✅ | ✅ | ✅ | ✅ | ✅ | — |
| Prometheus `jul_upstream_backends` | ☐ | ✅ | ✅ | ✅ | ✅ | — |
| Prometheus `jul_discovery_errors_total` | ☐ | ✅ | ✅ | ✅ | ✅ | — |
| Console Status panel | ☐ | ✅ | ✅ | ✅ | ✅ | — |
| Atomic reload (unchanged pool kept) | ✅ | ✅ | ✅ | ✅ | ✅ | — |
| **Security / access** | | | | | | |
| No credentials required | ✅ | ✅ | ✅ | ☐ | ☐ | — |
| Token required | ☐ | ☐ | ☐ | ✅ (ACL token) | ✅ (SA token) | — |
| TLS verification | ☐ | ☐ | ☐ | ☐ | ✅ (CA bundle) | — |

## Known limitations

- **Keep-last-good preserves stale backends indefinitely.** A provider that is
  permanently down (not just blipping) leaves the pool with the last-known
  backend set. Active health checks will eventually eject those backends if
  they become unreachable, but until then traffic is sent to potentially stale
  addresses. Monitor `jul_discovery_errors_total` and alert if it grows while
  `jul_upstream_backends` stays flat.
- **DNS TTL is not respected.** Jul.IA polls on a fixed `refresh` interval. The
  interval must be chosen to match the desired responsiveness; there is no
  adaptive polling or TTL-based backoff.
- **SRV priorities are not honoured.** Only SRV weights are used (mapped to
  backend weights). If you require strict priority-based SRV fallback, use a
  dedicated DNS plugin or external tool.
- **No endpoint readiness from DNS or Consul.** DNS A/AAAA records carry no
  health signal; Consul filters by its own health checks (`passing_only`), but
  Jul.IA does not perform application-level health probes beyond the optional
  active health check configuration on the pool.
- **No cross-provider migration.** Switching a pool from one discovery type to
  another (e.g., `dns` → `consul`) rebuilds the pool from scratch; any runtime
  state (in-flight, cooldown) resets.
- **No dual-stack preference control.** DNS resolves A and AAAA without explicit
  IPv4/IPv6 preference ordering.

## Threat note

### Token exposure

- **Consul ACL token** and the **Kubernetes service-account token** are
  sensitive credentials. They are stored in the config (possibly via secret
  references, `${env:…}` or `${secret:…}`), not in a dedicated keychain. Follow
  the same rotation and access-control discipline as any other config secret:
  protect the config file, use secret references, and rotate tokens on
  compromise. Tokens are never logged by Jul.IA (the config redaction mechanism
  masks them), but they travel over the network to the provider — ensure TLS is
  used.
- **Kubernetes `insecure_skip_tls_verify`.** Setting this to `true` for local
  testing bypasses API-server verification; never enable it in production.

### SSRF / trust boundary

- Upstream backends selected by discovery are **config-trusted**, not
  request-selected. This is the Jul.IA edge-trust model: the operator controls
  the provider and query target; requests cannot influence the resolved backend
  set. This eliminates request-driven SSRF through discovery.
- However, if an attacker compromises the operator's DNS, Consul, or Kubernetes
  control plane, they can redirect traffic to arbitrary backends. This is an
  **infrastructure-compromise** scenario, not a Jul.IA code bug. Protect the
  provider infrastructure with the same rigour as the proxy itself.
- **Defense-in-depth (egress allow-list).** The Consul `address` and Kubernetes
  `api_server` are URLs the server fetches on its own. When the optional
  [`[egress]`](egress.md) allow-list is enabled, discovery fetches are constrained
  to an operator-approved set of hosts/CIDRs and refused at dial time otherwise
  (subsystem `discovery`), so a mistyped or compromised provider address cannot
  reach an unintended host. It is disabled by default. (DNS/DNS-SRV discovery uses
  the system resolver and is out of the allow-list's scope.)

### Stale-backend risk

- The keep-last-good design intentionally** sacrifices freshness for
  availability during provider blips. During a prolonged outage the backends
  may be stale (redeployed, decommissioned, or failed). Combine discovery with
  active health checks so that permanently stale backends are ejected once
  probes fail.

## GA status

Service discovery has reached **GA** against the [ADR 0003](adr/0003-maturity-and-ga.md) bar.

| Criterion | Status | Evidence |
| --- | :-: | --- |
| 1. Conformance / behaviour matrix | ✅ | [Matrix above](#behaviour-matrix) |
| 2. Published benchmark numbers | ✅ | `BenchmarkPoolPick` (balancer_bench_test.go) covers picker with discovered backends |
| 3. Known-limitations list | ✅ | [Known limitations](#known-limitations) above |
| 4. Semver-guarded config/API contract | ✅ | Covered by v1 freeze ([compatibility.md](compatibility.md)) |
| 5. Long-running soak test | ✅ | soaked via Phase 2A 8h windows 2026-07-06 (/discovery/ via dns-backend) — [evidence](soak-evidence.md#2026-07-06--phase-2a-consolidated-burn-in-completed-local-windows-8-h-50-workers) |
| 6. Runnable example + docs | ✅ | [testdata/discovery.toml](../testdata/discovery.toml) + this doc |
| 7. Security / threat note | ✅ | [Threat note](#threat-note) above |
| 8. Fuzzing where parsing is involved | n/a | No custom parser (uses standard `net` stack or JSON from `encoding/json`) |
| 9. Self-explanatory Console surface | ✅ | Status panel lists discovery-enabled pools |

Soak gate (post-GA, ADR 0005): tracked in [status.md](status.md#soak-tracking-post-ga-gate).