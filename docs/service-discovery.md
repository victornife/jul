# Service discovery (dynamic upstreams)

By default a `[[upstreams]]` pool has a static `servers` list. **Service
discovery** instead resolves the backend set from an external source and
refreshes the pool *live* — backends come and go without a config reload while
load balancing, passive health, and active health checks keep applying.

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

The instance service weight (`Weights.Passing`) maps to the backend weight.

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

## Metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `jul_upstream_backends` | gauge | `pool` | Current backend count (tracks discovery) |
| `jul_discovery_errors_total` | counter | `pool` | Failed/empty resolves (last-good kept) |

These complement the existing `jul_upstream_healthy` gauge and
`jul_upstream_probes_total` counter from active health checking, so a dashboard
can show both how many backends discovery currently sees and how many of them are
passing their probes.
