# Configuration reference

Jul.IA is configured by a single TOML document. The top-level tables are
`[global]`, `[[servers]]`, `[[upstreams]]`, `[cache]`, `[admin]`,
`[compression]`, `[rate_limit]`, `[egress]`, `[observability]`, `[waf]`,
`[plugins.<name>]`, and `[[stream]]`. Several tables are only honoured when the matching build tag is
present (for example `[waf]` requires the `waf` tag, `[[stream]]` the `stream`
tag, and `[plugins.<name>]` the `wasmplugins` tag); absent tags are rejected at
preflight rather than silently ignored.

> **Looking for an exhaustive, field-by-field reference?** This page stays a
> conceptual guide — what each area means, how to configure it, and worked
> examples. The generated, factual reference over every configurable leaf
> (type, default, lifecycle, required capability, allowed values, and more)
> lives in [`docs/generated/config-reference.md`](generated/config-reference.md),
> rendered from the same normalized contract as
> [`docs/generated/config.schema.json`](generated/config.schema.json) (a JSON
> Schema 2020-12 document) and
> [`docs/generated/config-metadata.json`](generated/config-metadata.json) (compact
> machine metadata keyed by canonical path). All three are generated —
> regenerate with `make config-contract-generate` after changing the schema —
> and are authoritative over the field-level facts. **Schema validity is
> necessary and not sufficient: Jul's runtime configuration validation
> (`jul check`) remains authoritative**, and a document may satisfy the schema
> while `jul lint` still reports an error-severity finding.

A minimal, working example:

```toml
[global]
log_level = "info"
log_format = "text"
shutdown_timeout = "30s"
reload_timeout = "10s"
redact_min_secret_length = 4

[[servers]]
listen = "0.0.0.0:8080"
server_names = ["localhost", "example.com"]
client_max_body_size = "1m"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root  = "/srv/www/example"
  index = ["index.html", "index.htm"]
  try_files = ["$uri", "$uri/", "/index.html"]

  [[servers.locations]]
  match = { type = "prefix", path = "/api/" }
  proxy_pass = "http://backend"
  cache = true

    [servers.locations.headers]
    Host = "$host"
    X-Real-IP = "$remote_addr"
    X-Forwarded-For = "$proxy_add_x_forwarded_for"

[[upstreams]]
name = "backend"
strategy = "round_robin"
servers = ["127.0.0.1:3000", "127.0.0.1:3001"]

[cache]
enabled = true
memory_max_size = "64m"
default_ttl = "60s"
stale_while_revalidate = "30s"
stale_if_error = "5s"

[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "${env:JUL_ADMIN_TOKEN}"
console = true
history_dir = "./jul-data/config-history"
history_keep = 50
plugin_upload_dir = "./jul-data/plugins"
# plugin_upload_enabled defaults to false. Set to true only if you need WASM upload.
plugin_upload_enabled = false
plugin_upload_max_size = 32

```

---

## Strict TOML decoding and compatibility aliases

Jul.IA rejects unknown TOML fields in every path that uses the canonical parser. A misspelled security, routing, TLS or policy key is a fatal configuration error rather than a silent no-op. Errors include contextual field information where the TOML decoder exposes it.

The historical singular `server_name` key remains the one documented compatibility alias: it is accepted, canonicalized immediately to `server_names`, and never emitted by `jul fmt`/marshal. Setting both forms is rejected.

Known fields are also fail-closed. Jul.IA distinguishes three cases:

- **omitted** — apply the documented default;
- **explicit zero/disabled value** — apply that field's documented zero semantics;
- **explicit invalid value** — reject the whole candidate without writing or staging it.

Enums are case-sensitive and use the exact lowercase spellings shown in this
reference. `worker_threads` accepts only `auto` or a canonical positive base-10
integer. Negative durations/sizes, invalid HTTP statuses, out-of-range values,
and sizes that overflow `int64` are rejected before runtime construction. The
same validator governs startup, `jul check`, `jul lint`, `jul fmt`, raw and
structured apply/preview, planned-restart staging, rollback, and importer output.

The machine-readable [configuration value contract](config-value-contract.json)
records every public numeric leaf plus every enum/grammar leaf, including its
bounds, allowed values, zero semantics, and activation condition. A schema drift
test fails when a new public scalar is added without an audited disposition.

---

## `[global]`

The `[global]` block controls process-wide settings. Lifecycle is field-specific: some values are hot, some are startup-bound, and authority changes are deliberately restart-required. The generated [lifecycle reference](generated/config-lifecycle.md) is authoritative; this section explains meaning and workflow rather than duplicating every disposition.

```toml
[global]
log_level = "info"
log_format = "json"
shutdown_timeout = "30s"
reload_timeout = "10s"
redact_min_secret_length = 4
config_authority = "managed"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `worker_threads` | string | Exact lowercase `"auto"` (default) or a canonical positive base-10 integer (`1`, `2`, …); whitespace, signs, zero, fractions, overflow, and other spellings are invalid |
| `log_level` | string | Exact lowercase `debug`, `info` (default), `warn`, or `error`; any other explicit value is invalid |
| `log_format` | string | Exact lowercase `text` (human-readable, default) or `json`; invalid values are rejected rather than falling back to text |
| `access_log` / `error_log` | string | Deprecated compatibility fields; accepted but ignored by the current runtime. Configure access records under `[observability.access_log]`; application logs use the process logger. |
| `shutdown_timeout` | duration | Grace period to drain in-flight requests on shutdown (also bounds the HTTP/3 drain) |
| `reload_timeout` | duration | Maximum duration for a configuration reload before it is reported as `timed_out`. Zero or omitted defaults to 10s. The timeout is advisory: the swap still completes, but a warning is logged and the apply response includes `previous_reload.timed_out: true`. The Console surfaces this as a distinct "Applied — reload exceeded the configured timeout" warning so the operator knows to investigate slow reload paths (WAF rule compilation, WASM plugin loading, large config) or raise this value. See [reload-semantics.md](reload-semantics.md) |
| `redact_min_secret_length` | int | Shortest resolved secret value masked from logs; `0` uses the default (4). Lower it (down to 1) for short secrets, accepting possible masking of incidental log text |
| `config_authority` | string | Who owns configuration persistence and drift detection: `managed` (Jul.IA owns the file; the Console/API may write it) or `file_owned` (an external file or GitOps pipeline owns it; every mutating admin endpoint is refused). `controller_owned` is reserved and rejected. **Omitted resolves to `file_owned`** — a fixed default that is never derived from `[admin].enabled` or any other field. Restart-required; can only change through `stage_restart`. See [reload-semantics.md](reload-semantics.md#configuration-authority-managed-vs-file-owned) and [deployment.md](deployment.md#configuration-authority) |

The legacy `[global].access_log` / `error_log` values are known no-ops retained for compatibility in the current major version. They emit lint warnings, do not cause a restart, and do not select a sink. Use `[observability.access_log].enabled` and `sinks` for request records; route process stderr through the service supervisor.

Durations use Go syntax: `30s`, `5m`, `1h`. Sizes use `512k`, `1m`,
`512m`, etc. Values must fit the signed 64-bit representation used by the
runtime; parsing rejects overflow before unit multiplication. Zero is accepted
only with the meaning documented for the specific field.

Run `jul check -config server.toml` before deployment for structural validation
plus runtime preflight. `jul lint` adds advisory best-practice findings; it never
downgrades a runtime-invalid value to a warning. `jul fmt` validates before
printing or writing canonical TOML, so formatting cannot persist an invalid
candidate.

### Structured sparse global operations

The structured patch API supports three process-wide operations:

- `global_set` for `worker_threads`, `log_level`, `log_format`,
  `shutdown_timeout`, `reload_timeout`, and `redact_min_secret_length`
  (`config_authority` is intentionally excluded: it changes persistence
  ownership and may only move through `stage_restart` as a complete
  candidate, never a sparse hot field);
- `compression_set` for every field in `[compression]`;
- `rate_limit_global_set` for `enabled`, `key`, `rate`, `burst`, and
  listener-global `max_conns`.

Each payload is required and must contain at least one field. Omitted fields
preserve the current value; explicit false, zero, empty string, and empty arrays
are supplied values. Lists are copied before they enter candidate state. Every
supplied field is parsed and validated against a copy, and the target block is
assigned only after the whole operation succeeds. The ordered batch executor
then marshals, reparses, applies canonical defaults, validates the complete
candidate, performs build-tag preflight, computes the diff, and classifies the
whole candidate through the lifecycle registry.

`route_set_rate_limit` and `rate_limit_global_set` deliberately share the public
`rate_limit` JSON key. The `op` discriminator selects the contract: route
patches retain their established complete-replacement behavior and reject
`max_conns`; the global operation is sparse and accepts it.

Operation summaries are deterministic field-name lists and never contain
configured values. `global.log_format` is hot-reloadable (#91): the slog handler
encoding swaps atomically on the next successful reload without rebuilding the
logger. A changed
`max_conns` requires staging whenever any currently bound desired address is
retained; it can apply live only when all affected desired listeners are new in
the same complete candidate. Mixed hot/restart batches stage the whole
candidate. A `reload_timeout` change uses the currently serving timeout for that
transaction and governs later transactions only.

The structured global operations and matching Global/Traffic Controls Console forms are shipped. Guided editors and raw TOML remain supported surfaces over the same server-side validation, lifecycle and apply engine.

## Configuration apply modes

The admin API `POST /api/config/apply` accepts an optional `?mode=` query
parameter that controls how a valid candidate is applied:

| Mode | Description |
| ---- | ----------- |
| `hot` (default) | Validates, persists, and immediately triggers a live reload. Restart-required changes are rejected with `restart_required: true` and `can_stage: true`; nothing is written. |
| `stage_restart` | Validates and persists the candidate without triggering a live reload. The running process continues serving the previous configuration. The candidate takes effect on the next process restart. Use this mode for changes to startup-bound settings (cache, egress, admin, tracing, ACME, log format, listener settings). |

When a candidate is staged:

- `GET /api/config/pending-restart` returns the structured staging state
  (staged version, serving version, pending subsystems, discard availability).
- `POST /api/config/pending-restart/discard` atomically restores the previous
  configuration and clears the staged state. The running process is unaffected.
- Hot applies are refused with `HTTP 409` until the staged candidate is
  discarded or the process is restarted.

See [reload-semantics.md](reload-semantics.md#planned-restart-staging) for the
crash-consistent staging order and reconciliation rules.

---

## `[[servers]]`

A `[[servers]]` block defines a virtual host bound to a single listen address.
You can repeat the table to run multiple listeners (e.g. HTTP on `:80` and HTTPS
on `:443`). Each server matches incoming requests by `Host` header
(`server_names`) or falls back to the default server for that address.

```toml
[[servers]]
listen = "0.0.0.0:8080"
server_names = ["api.example.com"]
client_max_body_size = "8m"
read_timeout = "60s"
write_timeout = "60s"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://api_pool"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `listen` | string | Bind address, e.g. `0.0.0.0:8080` or `127.0.0.1:443` |
| `server_names` | []string | Host names matched against the `Host` header |
| `locations` | array | One or more `[[servers.locations]]` blocks |
| `tls` | table | TLS settings (see [TLS](#tls)) |
| `client_max_body_size` | size | Maximum request body (per-server default) |
| `max_header_bytes` | size | Maximum request header size (default 1 MiB) |
| `read_header_timeout` | duration | Time allowed to read request headers |
| `read_timeout` / `write_timeout` | duration | Hard request/response caps (off by default so SSE/WebSocket/large transfers are not severed) |
| `idle_timeout` | duration | Keep-alive idle timeout |
| `access_log` / `error_log` | string | Deprecated compatibility fields; accepted and linted but ignored. Use the global `[observability.access_log]` block and the process logger instead. |
| `error_pages` | table | Map of status code → file path or redirect URL |
| `redirect_https` | int | On an HTTP server, redirect to HTTPS with this status (`301` or `308`) |
| `h2c` | bool | On a plaintext listener, also accept cleartext HTTP/2 (h2c) for native gRPC clients without TLS; ignored on a TLS listener (HTTP/2 is negotiated via ALPN) |
| `client_address` | table | Trusted-proxy policy for deriving the canonical client address (see [Client address and trusted proxies](#client-address-and-trusted-proxies)) |

### Fields resolved once per listen address

Several `[[servers]]` fields describe the **socket**, not the virtual host, so
when two blocks share a `listen` value the listener resolves them once:

| Field | Rule when blocks disagree |
| --- | --- |
| `read_header_timeout`, `read_timeout`, `write_timeout`, `idle_timeout`, `max_header_bytes` | the **first** block on the address wins; later values are discarded |
| `h2c`, `http3.enabled` | **any** block enabling it turns it on for the whole address; a sibling cannot opt out |
| `http3.alt_svc_max_age` | taken from the first HTTP/3-enabled block on the address |

`jul lint` warns when blocks sharing a listener declare **different** explicit
values for any of these, naming both the winning block and the ignored one. It
is a warning rather than an error because the behaviour is long-standing and
some configurations rely on it; a configuration that agrees, or that leaves the
field unset in all but one block, is silent.

One case is deliberately not reported: when the *winning* block's value equals
the documented default. Defaults are filled in while the configuration is
loaded, so at that point a block that spelled the default out is
indistinguishable from one that never mentioned the field — and warning about
fields nobody wrote would be worse than missing this case.

Everything else under `[[servers]]` is per virtual host. In particular
`client_max_body_size` is applied by the router from the **matched** block, so
two vhosts on one listener may legitimately differ. `client_address` is the one
listener-scoped field whose divergence is a hard **error**, because it is a
security boundary — see
[Client address and trusted proxies](#client-address-and-trusted-proxies).

---

## Client address and trusted proxies

Jul answers every IP-based question — CIDR authentication, rate-limit keys, WAF
source address, access logs, forwarding headers — with one canonical client
address. By default that address is the immediate transport peer: no forwarding
header is believed. This block is how an operator says which proxies may assert
something different.

```toml
[[servers]]
listen = ":443"

[servers.client_address]
trusted_proxies   = ["10.0.0.0/8", "2001:db8:100::/48"]
forwarded_headers = ["x-forwarded-for"]
max_hops          = 16
```

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `trusted_proxies` | []string | empty | CIDR prefixes, or bare addresses meaning a single host, whose forwarding headers are believed. Prefixes must be canonical (host bits clear): `10.0.0.0/8`, not `10.1.2.3/8`. |
| `forwarded_headers` | []string | `["x-forwarded-for"]` | **Required when `trusted_proxies` is set.** Ordered preference; the first header present on the request is the only one used. An explicitly empty list (`[]`) disables both, keeping peer-only identity even for a trusted peer. |
| `max_hops` | int | `16` | Maximum asserted hops in a chain. A longer chain fails closed to the transport peer. Maximum 255. |

**List only headers your proxy overwrites.** Trusting a proxy means trusting what
it *writes*, not everything it *forwards*. Almost every proxy — nginx, HAProxy,
Cloudflare, CloudFront, ALB, ingress-nginx — writes `X-Forwarded-For` and passes
RFC 7239 `Forwarded` through untouched, so a client behind one of them can send
its own `Forwarded` header. If Jul believed that header it would believe the
client, and the trust boundary would be gone. That is why `x-forwarded-for` is
the default, why `forwarded_headers` must be stated explicitly once a proxy is
trusted, and why `jul lint` warns when `forwarded` is enabled. Enable it only if
you have confirmed your proxy overwrites `Forwarded` on every request.

**`trusted_proxies` is a security boundary.** Every address it covers may claim
any client address, and that claim flows into authentication, rate limiting, the
WAF and the audit trail. List the addresses of proxies you actually operate and
nothing else. There are deliberately no shorthands (`private`, `rfc1918`, cloud
provider lists): they encourage exactly the over-broad trust this policy exists
to bound. `jul lint` warns when an entry covers the whole address space.

### How the client address is derived

1. The direct socket peer is parsed and always retained.
2. If the peer is not in `trusted_proxies`, every asserted header is ignored and
   the peer is the client.
3. Otherwise the first configured header present on the request is selected.
   `Forwarded` (RFC 7239) and `X-Forwarded-For` chains are never merged.
4. The selected chain is walked right to left, discarding hops that are
   themselves trusted proxies.
5. The first untrusted valid address is the canonical client.
6. If every asserted hop is trusted, the leftmost asserted address is used.
7. Malformed, oversized, ambiguous or over-`max_hops` input fails **closed** to
   the direct peer; a bounded, rate-limited warning is logged. Obfuscated
   identifiers (`for=_hidden`), `unknown`, hostnames and invalid addresses are
   never canonical clients, and no DNS lookup happens at any point.

A spoofed left-hand entry therefore cannot win: with
`X-Forwarded-For: 127.0.0.1, 198.51.100.9, 10.8.8.8` from a trusted `10.0.0.0/8`
peer, the client is `198.51.100.9` — the first hop the trusted proxies did not
vouch for. The attacker-supplied `127.0.0.1` is never reached.

### One policy per listen address

Server blocks are selected by the `Host` header, but the client address is
derived **before** routing. A policy chosen after virtual-host selection would
let an attacker pick the trust policy applied to their own request by choosing a
`Host`. Therefore every `[[servers]]` block that shares a `listen` value must
declare the same effective `client_address` policy; validation rejects the
configuration otherwise:

```
servers[1].client_address: {trusted_proxies=[] ...} differs from
servers[0].client_address: {trusted_proxies=[10.0.0.0/8] ...}; client identity is
derived per listen address before the Host header selects a server block, so
every block sharing listen ":443" must declare the same policy
```

Prefix listing order, duplicate entries and spelling out a default value do not
count as a difference; anything that changes the trust actually applied does.

### Scope

The policy applies to HTTP/1.1, HTTP/2 and HTTP/3 on that listener — one
middleware chain serves all three. It does **not** apply to the admin listener,
which keeps peer-only identity by design.

`[[stream]]` L4 proxying derives identity under the same boundaries but with its
own configuration: the socket peer is always the transport peer, and an inbound
PROXY-protocol header is believed only from a declared
[`trusted_proxies`](stream-proxy.md) entry on that stream block. It never feeds
the HTTP canonical identity. See [known limitations](known-limitations.md).

`X-Real-IP` is not supported: it carries a single address with no chain, so it
cannot be evaluated against a trust boundary.

### PROXY protocol on an HTTP listener

A TCP load balancer that preserves the client with the PROXY protocol rather
than a forwarding header — AWS NLB, GCP TCP LB, HAProxy in TCP mode — sets
`proxy_protocol` on the listener:

```toml
[[servers]]
listen = ":443"
proxy_protocol = "in"

[servers.client_address]
trusted_proxies   = ["10.0.0.0/8"]   # the balancers, required
forwarded_headers = ["x-forwarded-for"]
```

The advertised address becomes the listener's transport peer, so everything
above it — CIDR authentication, rate limiting, the WAF, logs, the chain sent
upstream — behaves exactly as for a direct connection. A CDN in front of the
balancer still works: the balancer reports the CDN as the peer, and if that
address is in `trusted_proxies` the CDN's `X-Forwarded-For` is then read on top.
The two mechanisms compose rather than compete.

`trusted_proxies` is **required**: the header is an assertion, and a connection
from an address outside the set is refused rather than served on its own
address. Only ingest is offered; emitting a header to a backend is a different
concern.

**HTTP/3 cannot carry it.** QUIC is datagram-based and negotiates TLS inside the
transport, so there is no plaintext framing to prepend a header to. Enabling
both on one listener is a validation error rather than a silent asymmetry — a
listener must not derive the client address two different ways depending on the
protocol a client negotiated. Run HTTP/3 on a separate listener.

---

## `[[servers.locations]]`

Each location selects requests with a `match` expression and applies **exactly one**
action. Think of a location as a route: it decides *which* requests it handles
and *what* to do with them. Available actions are static file serving (`root`),
reverse proxy (`proxy_pass`), FastCGI (`fastcgi_pass`), uWSGI (`uwsgi_pass`),
gRPC-JSON transcoding (`grpc_transcode`), redirects (`redirect`/`return`), or an
explicit `deny`.

Only one action may be present per location; validation rejects ambiguous blocks.

**Matching:**

```toml
match = { type = "prefix", path = "/api/" }   # prefix, exact, or regex
```

A location may additionally constrain the request method, its headers and its
query parameters. See [Request predicates](#request-predicates) below.

### Request predicates

`type` and `path` decide which locations a request is a *candidate* for.
Predicates filter within that candidate set; they never promote a route past a
more specific path.

```toml
[[servers.locations]]
proxy_pass = "http://api"

[servers.locations.match]
type = "prefix"
path = "/api/"
methods = ["GET", "POST"]

[[servers.locations.match.headers]]
name = "X-Tenant"
op = "exact"          # present | exact | regex
value = "public"

[[servers.locations.match.query]]
name = "version"
op = "exact"          # present | exact
value = "v2"
```

**One Boolean rule:** a list inside one field is an OR-set; separate fields and
separate table entries are ANDed. There is no negation, no grouping and no OR
across fields.

`headers` and `query` are arrays of tables rather than maps, because declaration
order is preserved and round-tripped, and because one name may carry more than
one predicate.

#### `methods`

| Rule | Behaviour |
| --- | --- |
| Comparison | byte-exact against the request method; nothing is case-folded (RFC 9110 §9.1) |
| `GET` | also matches `HEAD`, which RFC 9110 §9.3.2 defines as GET without a body |
| `HEAD` alone | matches HEAD only |
| Omitted | the route does not constrain the method |
| `methods = []` | a validation error — a route that can never match is a mistake, not a way to disable one |
| `"get"` | rejected, naming `"GET"`; a genuinely lowercase extension method is accepted |
| Duplicates | rejected rather than silently collapsed |
| `CONNECT` | rejected: Jul implements no tunnelling, and Go gives an authority-form CONNECT an empty path, which matches no location |
| Extension methods | accepted when they are valid tokens |

#### `headers`

| `op` | Matches when |
| --- | --- |
| `present` | the field is present at all, **including** a present-but-empty one |
| `exact` | **any one** field line is byte-equal to `value` |
| `regex` | **any one** field line matches the RE2 pattern |

Field *names* are canonicalized, so they match case-insensitively on every
protocol version. Field *values* are compared byte-exactly, and are never split
on commas: `Accept: a, b` is one value.

`regex` is **unanchored**, matching the existing `match.type = "regex"` path
matcher. Write `^…$` when you mean the whole value.

`op = "exact"` with `value = ""` matches only a present-but-empty field, which is
how absent and present-empty stay distinguishable.

Rejected at validation: `Host` (Go moves it out of the request headers, so the
predicate could never fire — use `server_names`), and any `:`-prefixed
pseudo-header. Hop-by-hop names are accepted with a lint warning, because they
are connection-scoped and behave differently per protocol version.

`Forwarded`, `X-Forwarded-*` and RFC 9440 certificate-assertion names are
**rejected unless the listener declares
[`[servers.client_address]` `trusted_proxies`](#client-address-and-trusted-proxies)**.
Matching runs before Jul rebuilds the forwarded chain, so at that moment the
field still holds whatever the client sent. Even when accepted the predicate
produces a `SeverityError` lint finding: the declared trust reaches the proxy,
not the client behind it.

#### `query`

| `op` | Matches when |
| --- | --- |
| `present` | the key appears, including `?x` and `?x=` |
| `exact` | any occurrence of the key decodes to `value` |

The query string is parsed with `url.ParseQuery` semantics — `&` separates,
`;` does not, `+` decodes to a space, `%XX` is percent-decoded. It is parsed at
most once per request, lazily, and **only when a candidate route actually
carries a query predicate**, so a configuration without one never parses a query
string. A malformed escape makes only that pair absent; the request is never
turned into a 400.

#### Bounds

| Bound | Limit |
| --- | --- |
| `methods` entries | 16 |
| `headers` entries per location | 16 |
| `query` entries per location | 16 |
| `regex` header predicates per location | 8 |
| Header regex pattern length | 512 bytes |
| Header predicate `value` length | 1 KiB |
| Query `name` / `value` length | 1 KiB |
| Query pairs parsed per request | 1024 |

All but the last are checked before the configuration is published. These are
conservative initial ceilings, not measured capacity limits; raising one is
additive and needs evidence.

#### No automatic 405

A method mismatch makes a route non-matching, and the search continues. When no
candidate remains the answer is the ordinary **404** — there is no 405 and no
`Allow` header anywhere. `Allow` is a property of the resource, and a gateway
route listing `["GET"]` says nothing about whether the upstream implements POST.

Use the Console's route tester (`POST /api/routes/test`) to see which candidates
a request produced and which predicate rejected each one; a predicate mismatch
is never logged per request.

### Response headers and CORS

A location may add, set/replace or remove response headers, and enforce a CORS
policy. Both apply outside the cache and outside compression's own headers, and
both apply to every response the location produces — normal, error (401, 403,
429, 502, 413), a cache hit, and a panic recovered after route selection.

```toml
[[servers.locations.response_headers]]
op = "set"                    # add | set | remove
name = "X-Frame-Options"
value = "DENY"

[[servers.locations.response_headers]]
op = "add"
name = "Set-Cookie"
value = "flavour=chocolate; Path=/; Secure; HttpOnly"

[[servers.locations.response_headers]]
op = "remove"
name = "X-Powered-By"
```

An **ordered list**, not a map: operations apply top to bottom, and a later one
observes the earlier ones' effect — `set` followed by two `add`s is the
canonical way to express a deterministic multi-value header. `add` is
`Header.Add`, `set` is `Header.Set`, `remove` is `Header.Del`. `value` is
required for `add`/`set` (an empty string is legal and emits an empty field
value; omitting it is an error) and forbidden for `remove`.

**Rejected at validation:** names that are not RFC 9110 tokens or that start
with `:`; values carrying any byte outside RFC 9110 §5.5's field-value grammar
(not only CR/LF/NUL — the other C0 controls and DEL are rejected too, since Go
silently drops an invalid header at write time and configuration time is the
only place the operator finds out); `Connection`, `Content-Length`,
`Transfer-Encoding`, `Upgrade`, `Keep-Alive`, `Proxy-Connection`, `TE`,
`Trailer`, `Proxy-Authenticate`, `Proxy-Authorization`; `Content-Encoding`
(compression owns it); any `Vary` operation on a `cache = true` location; and
any `Access-Control-*` operation on a `cors.enabled = true` location.

**`Vary`:** on a location with `cache = true`, any operation is rejected — the
cache snapshots headers at commit, so an operator-added `Vary` would be
invisible to it and can leak a representation across a variance it truthfully
claims to declare (see [cache.md](cache.md)). Without a cache, `add` is
permitted as a directive to *downstream* caches only; `set`/`remove` are always
rejected.

| Bound | Limit |
| --- | --- |
| Operations per location | 32 |
| One value | 4 KiB |
| Total added to one response | 8 KiB (a static, conservative estimate) |

#### CORS

```toml
[servers.locations.cors]
enabled = true
allowed_origins = ["https://app.example.test"]
allowed_methods = ["GET", "POST"]
allowed_headers = ["Content-Type", "Authorization"]
exposed_headers = ["X-Request-Id"]
allow_credentials = true
max_age = "10m"
```

**CORS is not authorization.** A disallowed origin is still routed,
authenticated, rate-limited and served exactly as without a `[cors]` block; it
simply gets no `Access-Control-*`. Do not build access control on it.

**Order:** generic `response_headers` operations run first, then CORS, which is
authoritative — every `Access-Control-*` field an upstream response carries is
removed before Jul emits its own set, so a CORS-implementing upstream cannot
produce a duplicate grant. An upstream `Vary: Origin` is never stripped: if the
body genuinely varies by origin, that is a fact about the stored
representation, not an optimization opportunity.

**Origins** are exact, byte-compared: `scheme://host[:port]`, no path, no
explicitly-written default port. No wildcard subdomains, no regex. `"null"` is
accepted literally (a sandboxed iframe, a local file) and lint-warns.
**`allowed_origins = ["*"]` is unconditional**: it forbids
`allow_credentials = true`, forbids any other entry, and grants
`Access-Control-Allow-Origin: *` on **every** response — including one with no
`Origin` and `Origin: null` — with `Vary: Origin` correctly **omitted** (the
output is constant regardless of origin). Any other policy **always appends**
`Vary: Origin`, including on a disallowed or missing origin — that is what
stops a shared downstream cache replaying a no-origin variant cross-origin.

**`allowed_methods` / `allowed_headers` / `exposed_headers`** govern **preflight
approval only** — never ordinary requests, which is `match.methods`. Conflating
the two is the most common CORS misconfiguration there is.
`allowed_methods` defaults to the CORS-safelisted `["GET", "HEAD", "POST"]`
when omitted; an explicit empty list is a validation error. None of the three
accepts `"*"` — under Fetch a wildcard in `Access-Control-Allow-Headers` does
not cover `Authorization`, usually the header an operator writing `"*"` wants.
**Every token in `Access-Control-Request-Headers` must appear in
`allowed_headers`, with no implicit safelist exemption**, and a token is never
reflected back into the response.

**`max_age`:** omitted emits no header; otherwise a whole number of seconds
(`"500ms"` is a validation error, not a silent truncation), 0 to 24h.

**Preflight** is `OPTIONS` carrying exactly one `Origin` field line and exactly
one well-formed `Access-Control-Request-Method` field line (a single token —
repeated lines or a comma-separated list is not well-formed and is never
approved). An **approved** preflight is answered with **204 No Content**, no
upstream contact and no cache interaction. A **denied** preflight is **not**
short-circuited: it falls through to the ordinary chain and receives whatever
that route returns for `OPTIONS`, with no `Access-Control-*` header added on
its behalf — Jul invents no status that discloses a policy exists.

A `cors.enabled = true` location's `methods` predicate additionally accepts its
own preflight (§2); a location that also carries **header** predicates
lint-warns, because a browser preflight carries none of the application
headers the real request will and the location will not be selected for it.

**Execution order** adds three positions around the existing chain, all
outside the cache and outside `BodyLimit`'s body-size guard for the
`response_headers`/CORS wrapper, and inside `ClientCert`/outside `Auth` for the
preflight terminator — an approved preflight always skips authentication
(Fetch sends preflights without credentials) but is still rate-limited (its
own scope, keyed by client address) and passed through the location's WAF. See
[core-http.md](core-http.md#execution-order) for the full diagram.

**A panic after route selection produces a 500 carrying the location's
response-header and CORS policy**, so a cross-origin client can still read it;
only a panic before a location is chosen falls back to the process-wide
generic 500.

| Bound | Limit |
| --- | --- |
| `allowed_origins` entries / one origin | 64 / 256 bytes |
| `allowed_methods` entries | 32 |
| `allowed_headers` entries | 64 |
| `exposed_headers` entries | 32 |
| One method/header token | 256 bytes |
| `Access-Control-Request-Headers` tokens accepted at request time | 64 |
| `max_age` | 24h |
| Generated `Access-Control-*`/`Vary` header set (worst case) | 4 KiB |

### Static file serving

Serve files from a local directory. Ideal for SPAs, asset delivery, and simple
sites. Use `try_files` for SPA fallback routing (e.g. send all unmatched paths
to `index.html`).

```toml
[[servers.locations]]
match = { type = "prefix", path = "/" }
root = "/srv/www/myapp"
index = ["index.html"]
try_files = ["$uri", "$uri/", "/index.html"]
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `root` | string | Document root directory |
| `index` | []string | Index file candidates for directory requests |
| `try_files` | []string | Fallback sequence (supports `$uri`) |
| `directory_listing` | bool | Enable auto directory index |
| `allow_hidden` | bool | Serve dotfiles |
| `cache_control` | string | `Cache-Control` header for served files |

### Reverse proxy

Forward requests to an HTTP backend. This is the workhorse action for API
gateways, microservice front-ends, and load-balanced applications. Use
`proxy_pass = "http://upstream-name"` to reference a named `[[upstreams]]` pool
or a literal `http://host:port` for a single backend.

```toml
[[servers.locations]]
match = { type = "prefix", path = "/api/" }
proxy_pass = "http://backend"
proxy_connect_timeout = "5s"
proxy_read_timeout = "30s"
  [servers.locations.headers]
  Host = "$host"
  X-Real-IP = "$remote_addr"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `proxy_pass` | string | `http://upstream-name` or a concrete `http://host:port` |
| `proxy_connect_timeout` | duration | Connection establishment timeout (default 10s) |
| `proxy_read_timeout` | duration | Per-read inactivity bound on the upstream response — the maximum gap between successive reads, covering both the headers (time-to-first-byte) and a slow-trickle body. `0` (default) leaves it unbounded. A steadily streaming response is never interrupted while data keeps flowing |
| `proxy_send_timeout` | duration | Per-write inactivity bound on sending the request to the upstream — the maximum gap between successive writes. `0` (default) leaves it unbounded |
| `proxy_retries` | int | **Deprecated** — use `resilience.retry_attempts`, which is the same control under the name the pool block already uses. Still valid and unchanged; setting both is a validation error; removal in the next major |
| `grpc` | bool | Proxy `proxy_pass` as **native gRPC** over end-to-end HTTP/2 (trailers preserved, no buffering); `http://` dials the backend over cleartext HTTP/2 (h2c), `https://` over HTTP/2 with TLS — requires the `grpc` build tag |
| `headers` | table | Upstream request headers; values support `$host`, `$scheme`, `$remote_addr` (canonical client), `$realip_remote_addr` (direct transport peer), `$proxy_add_x_forwarded_for` (Jul's trusted chain) and `$ssl_client_*`. Applied **after** the `X-Forwarded-*` headers are constructed, so an explicit value wins — see [forwarded headers](core-http.md#forwarded-headers-to-the-backend) |

### FastCGI / uWSGI

Pass requests to FastCGI or uWSGI application servers (PHP-FPM, Python
applications, etc.). This replaces the need for a separate FastCGI front-end
when Jul sits at the edge.

```toml
[[servers.locations]]
match = { type = "prefix", path = "/" }
fastcgi_pass = "unix:/run/php/php-fpm.sock"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `fastcgi_pass` | string | A named upstream, or `unix:/path.sock`, `tcp://host:port`, `host:port` |
| `fastcgi_params` | table | Explicit CGI parameter overrides |
| `uwsgi_pass` | string | Same forms as `fastcgi_pass` |

**Both are full upstream pools.** Point either at a named `[[upstreams]]` and the
route gets load balancing, active health checking, failure accounting and
admission control on the same terms as `proxy_pass`:

```toml
[[upstreams]]
name = "php"
strategy = "least_conn"
servers = [
  { address = "unix:/run/php/php-fpm-1.sock" },
  { address = "unix:/run/php/php-fpm-2.sock" },
]

  [upstreams.resilience]
  max_active_requests = 64

[[servers.locations]]
match = { type = "prefix", path = "/" }
fastcgi_pass = "php"
```

Bounding concurrency matters more here than for a typical HTTP backend, not
less: PHP-FPM's `pm.max_children` is a hard ceiling, so admitting past it only
builds a queue inside the application server where Jul cannot see or bound it.

Two notes on backends reached over a unix socket:

- `health_check.type = "http"` cannot probe one — there is no host to put in a
  URL — so that combination is a validation error. Use `type = "tcp"`, which
  connects to the socket itself.
- A `fastcgi_pass` or `uwsgi_pass` value that is neither a configured upstream
  name nor a recognisable address is rejected at validation. It previously
  parsed as the TCP host of that name and failed only at runtime.

`uwsgi_pass` honours the location's `proxy_connect_timeout`.

### gRPC transcoding (`[servers.locations.grpc_transcode]`, `grpc` build tag)

Expose a gRPC service as a RESTful JSON API (unary and streaming). Jul translates
the HTTP request into a gRPC call and the protobuf reply back into JSON. This
lets mobile and web clients consume gRPC backends without a dedicated gateway.

Requires the `grpc` build tag: `go build -tags grpc ./cmd/jul`.

```toml
[[servers.locations]]
match = { type = "prefix", path = "/v1/" }
[servers.locations.grpc_transcode]
target         = "grpc-backend"     # upstream name or host:port
descriptor_set = "/etc/jul/api.pb"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `target` | string | Backend gRPC server: an upstream name or a literal `host:port` |
| `descriptor_set` | string | Path to a compiled `FileDescriptorSet` (`.pb`) describing the service |
| `use_reflection` | bool | Discover the service via gRPC server reflection instead of a descriptor file |
| `tls` | bool | Dial the backend over TLS (default plaintext h2c) |
| `preserve_proto_field_names` | bool | Emit original `snake_case` proto field names instead of `lowerCamelCase` JSON names |
| `streaming` | bool | Enable server-, client-, and bidirectional-streaming transcoding (default `false`) |
| `stream_mode` | string | Frame format for streamed responses: `ndjson` (default) or `sse` |
| `max_message_size` | string | Maximum per-message body size, e.g. `"4m"` (default `"4MiB"`) |

See [gRPC transcoding deep-dive](./grpc-transcoding.md) for the full streaming
matrix, path-variable mapping, and benchmark notes.

### Redirect / control

Issue redirects, bare status returns, regex rewrites, or explicit denies. Use
`rewrites` for pretty-URL transformation before the action runs. Set `cache =
true` to enable response caching for this location (requires `[cache].enabled`).

```toml
[[servers.locations]]
match = { type = "prefix", path = "/old-path" }
redirect = "/new-path"
return = 301

[[servers.locations]]
match = { type = "exact", path = "/health" }
return = 200
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `redirect` | string | Target URL (uses `return` code or 302) |
| `return` | int | Status for a redirect or bare return |
| `deny` | bool | Reject matching requests with 403 |
| `rewrites` | array | Regex rewrite rules (`pattern`, `replacement`, `flag`) |
| `cache` | bool | Enable response caching for this location (requires `[cache].enabled`) |
| `client_max_body_size` | size | Override the server default for this location |
| `rate_limit` | table | Override the global `[rate_limit]` for this location (`enabled`, `key`, `rate`, `burst`; `max_conns` is ignored) |

---

## `[[upstreams]]`

An upstream is a named pool of backend servers. Locations reference them via
`proxy_pass = "http://name"`. Upstreams decouple routing from backend topology,
so you can change servers, add health checks, or switch to service discovery
without touching the location rules.

In the Console, **New app** builds one typed, ordered configuration batch rather
than generated TOML: the first backend is carried by `upstream_add`, later
backends retain their visible order through `upstream_add_backend`, optional
health and discovery operations follow in that order, and an optional route mount
is added last. The mount choice is explicit: no route, one exact existing server
identity, or one exact new server identity. App deletion is a separately reviewed
one-operation `upstream_remove`; projected route references block it, the backend
re-checks those references during preview, and no route, server, plugin,
credential, or external discovery resource is removed as a cascade. Structured
preview requires `config:write`, final apply or staging requires `config:apply`,
and raw candidate/source access remains independently gated by `config:raw`.

```toml
[[upstreams]]
name = "backend"
strategy = "least_conn"
servers = [
  { address = "127.0.0.1:3000", weight = 2 },
  { address = "127.0.0.1:3001", weight = 1 },
]

  [upstreams.resilience]
  max_fails    = 3
  fail_timeout = "10s"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `name` | string | Pool name |
| `strategy` | string | `round_robin`, `weighted_round_robin`, or `least_conn` |
| `servers` | array | Bare addresses (`"127.0.0.1:3000"`) or tables with `address` + `weight` |
| `max_fails` | int | **Deprecated** — moved to [`resilience`](#resilience). Still valid; setting both is an error |
| `fail_timeout` | duration | **Deprecated** — moved to [`resilience`](#resilience). Still valid; setting both is an error |
| `backend_tls` | table | Outbound TLS policy for this pool — see [`backend_tls`](#backend-tls) |
| `resilience` | table | Admission, retry and circuit-breaker control — see [`resilience`](#resilience) |

`max_fails` and `fail_timeout` moved into `[upstreams.resilience]` so that block is the whole
resilience surface rather than most of it. Same names, same defaults, same meanings — only the
position changed, and the migration is moving one line. The old spelling stays valid and is scheduled
for removal in the next major. Setting a threshold in both places is a validation error rather than a
precedence rule: a configuration that means one of two values depending on a rule nobody remembers is
worse than one that refuses to start.

### `resilience`

Pool-scoped admission and overload control. Every default reproduces the behaviour Jul had before
these keys existed, so an omitted block changes nothing. Full reference and sizing guidance:
[upstreams.md](upstreams.md#admission-and-overload-control).

```toml
[upstreams.resilience]
max_active_requests    = 1000
max_active_per_backend = 600
max_pending_requests   = 100
pending_timeout        = "2s"

max_connections_per_backend = 256

retry_attempts        = 2
retry_deadline        = "3s"
retry_backoff_initial = "20ms"
retry_backoff_max     = "500ms"
retry_budget_percent  = 10

max_fails                = 3
fail_timeout             = "10s"
circuit_half_open_probes = 1
```

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `max_active_requests` | int | `0` (unlimited) | Admitted logical requests, streams and connections for the pool |
| `max_active_per_backend` | int | `0` (unlimited) | Per-backend limit, applied as a selection filter rather than a queue |
| `max_pending_requests` | int | `0` (**no queue**) | How many requests may wait for a slot; zero rejects immediately |
| `pending_timeout` | duration | `0` | How long a request may wait; zero leaves the request context as the only bound |
| `max_connections_per_backend` | int | `0` (unlimited) | Physical sockets per backend host, per transport; settable per location, which wins |
| `retry_attempts` | int | `0` | Total attempts for one retryable request; zero tries every distinct backend once. Settable per location, which wins |
| `retry_deadline` | duration | `0` | Bounds the whole retry sequence, attempts and backoff alike; zero leaves the request context as the only bound. Settable per location |
| `retry_backoff_initial` | duration | `0` (immediate failover) | First backoff interval, doubling per attempt with full jitter. Settable per location |
| `retry_backoff_max` | duration | `500ms` when backoff is on | Clamps the doubling; requires `retry_backoff_initial`. Settable per location |
| `retry_budget_percent` | int | `0` (unbudgeted) | Retries permitted as a percentage of primary attempts over a trailing window. **Pool-scoped only** |
| `circuit_half_open_probes` | int | `1` | How many requests may test a recovering backend at once. Omit for the default; an explicit `0` means unbounded. **Pool-scoped only** |
| `max_fails` | int | `3` | Consecutive failures that take a backend out of rotation. **Pool-scoped only** |
| `fail_timeout` | duration | `10s` | How long a backend stays out of rotation before it is probed. **Pool-scoped only** |

`max_pending_requests = 0` means *no queue*, not an unlimited one — an unbounded pending queue is the
failure this control prevents. `max_pending_requests` requires `max_active_requests`, and
`pending_timeout` may not exceed `global.shutdown_timeout`.

The admission keys are stateful and therefore pool-scoped, and so is `retry_budget_percent`, which
owns a window. A `[servers.locations.resilience]` block accepts `max_connections_per_backend` and the
four stateless retry keys; anything else written there is rejected. A rejected request is `503` with
`Retry-After`, never `429`.

`circuit_half_open_probes` is the one key here where omitting it and writing `0` mean different
things. `max_fails` consecutive failures take a backend out of rotation for `fail_timeout`; when that
elapses the backend does not simply return, it is *probed* by at most this many requests. Setting `0`
asks for the older behaviour, where every request waiting on the cooldown became eligible at the same
instant and a backend that had just come back took the full production load. It is pool-scoped
because two locations sharing an upstream would otherwise disagree about how many probes one
recovering backend may take.

`max_fails` and `fail_timeout` are retuned in place on reload: the failure counts and open circuits
survive, so raising a threshold during an incident does not put every ejected backend back under full
load at once.

Retries are for **transport errors only** — a 5xx is an answer, not a failure to reach a backend, and
retrying one would double load on a backend that is deliberately shedding. `POST` and `PATCH` are
never retried: a connection error does not prove the backend did not accept, commit and then die.
Full reference: [upstreams.md](upstreams.md#retry).

> [!NOTE]
> `proxy_retries` under `[[servers.locations]]` is the **deprecated spelling** of
> `retry_attempts`. It remains valid and behaves identically; setting both on one location is a
> validation error. It is scheduled for removal in the next major release.

The whole block can also be edited through the `upstream_set_resilience` typed
patch operation, gated by `config:write` like every other patch operation. It
replaces the block rather than merging into it, matching `upstream_set_health_check`:
a partial merge would make the result depend on state the caller cannot see in
the request they are sending. Omitting the payload removes the block, returning
every limit to its default.

Because `max_fails` and `fail_timeout` still have a deprecated upstream-level
spelling, and a configuration carrying both is rejected, setting either of them
through this operation moves them out of the old position. The move is reported
in the operation summary rather than done silently.

### `backend_tls`

The **outbound** TLS policy, used identically under `[[upstreams]]` and
`[[servers.locations]]`. It is a different key from the inbound `[servers.tls]`
block on purpose: `tls` under `[[servers]]` already means termination, and one
key with opposite directions in two places invites mistakes.

```toml
[[upstreams]]
name = "inventory"

  [upstreams.backend_tls]
  ca_file              = "/etc/jul/backend-ca.pem"
  ca_mode              = "system_and_file"
  client_cert          = "/etc/jul/client.pem"
  client_key           = "/etc/jul/client.key"
  server_name          = "inventory.internal"
  min_version          = "1.2"
  peer_identities      = ["dns:inventory.internal"]
  insecure_skip_verify = false
```

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `ca_file` | string | — | PEM bundle of trust roots; consulted only when `ca_mode` selects it |
| `ca_mode` | string | `system` | `system`, `system_and_file`, or `file_only`. **Never inferred** from the presence of `ca_file` |
| `client_cert` / `client_key` | string | — | Client certificate for mutual TLS; both or neither |
| `server_name` | string | derived from the target | The verified name and the SNI value. A discovery-returned address never becomes the identity |
| `min_version` | string | `1.2` | `1.2` or `1.3` |
| `peer_identities` | []string | — | Prefixed identities (`dns:`, `uri:`) ORed and matched **after** standard verification |
| `insecure_skip_verify` | bool | `false` | Disables peer verification. `jul lint` reports it as an **error**; `Validate` accepts it so an emergency path exists |

A block on a route whose backend is not reached over TLS is rejected rather than
silently ignored. When both an upstream and a location declare a policy, the
location's applies to that route and `jul lint` reports the override.

The full reference, including the trust-root modes, the discovery interaction
and the reload behaviour, is in [upstreams.md](upstreams.md).

### `[upstreams.health_check]`

Active health checking proactively probes each backend so failures are detected
(and recoveries observed) without waiting for live traffic. A backend leaves
rotation after `unhealthy_threshold` consecutive failed probes and returns after
`healthy_threshold` consecutive successful ones; this active verdict combines
with passive (`max_fails` / `fail_timeout`) health.

```toml
[[upstreams]]
name = "api"
servers = ["127.0.0.1:3000", "127.0.0.1:3001"]
  [upstreams.health_check]
  enabled = true
  type = "http"
  path = "/healthz"
  interval = "5s"
  timeout = "2s"
  healthy_threshold = 2
  unhealthy_threshold = 3
  expect_status = [200]
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Turn active probing on for this pool |
| `type` | string | `http` (default) or `tcp` |
| `path` | string | Request path for `http` probes (required) |
| `interval` | duration | Delay between probe rounds (default `5s`) |
| `timeout` | duration | Per-probe timeout; must be less than `interval` (default `2s`) |
| `healthy_threshold` | int | Consecutive successes to mark a backend healthy (default `2`) |
| `unhealthy_threshold` | int | Consecutive failures to eject a backend (default `3`) |
| `expect_status` | array | Acceptable HTTP status codes for `http` probes (default `[200]`) |
| `expect_body` | string | Optional: `http` probe body must contain this substring |

Metrics: `jul_upstream_healthy{pool,backend}` (1 healthy / 0 unhealthy),
`jul_upstream_probes_total{pool,result}`, and
`jul_upstream_probe_duration_seconds{pool}`.

### `[upstreams.discovery]`

Resolve a pool's backends from an external source and refresh them live, with no
config reload. With discovery enabled the static `servers` list is optional (a
seed/fallback until the first resolve). `dns` and `dns_srv` work in every build;
`consul` and `kubernetes` require the matching build tag.

```toml
[[upstreams]]
name = "api"
strategy = "round_robin"
  [upstreams.discovery]
  type = "dns"
  target = "api.internal.svc:8080"
  refresh = "15s"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `type` | string | `static` (off), `dns`, `dns_srv`, `consul`, or `kubernetes` |
| `target` | string | `host:port` for `dns`; the SRV name for `dns_srv` |
| `refresh` | duration | Poll interval (default `30s`) |
| `[consul]` | table | `address`, `service`, `tag`, `datacenter`, `token`, `passing_only` |
| `[kubernetes]` | table | `namespace`, `service`, `port`, `api_server`, `token`, `ca_file`, `insecure_skip_tls_verify` |

Metrics: `jul_upstream_backends{pool}` (current backend count) and
`jul_discovery_errors_total{pool}` (failed/empty resolves; last-good kept).

---

## gRPC ↔ JSON transcoding (`grpc` build tag)

A location with `[servers.locations.grpc_transcode]` exposes a gRPC
service as a RESTful JSON API, translating each HTTP request into a gRPC call
and the protobuf reply back into JSON.

Exactly one of `descriptor_set` or `use_reflection` must be set. Generate a
descriptor set from your `.proto` files with `protoc`:

```bash
protoc \
  --include_imports \
  --descriptor_set_out=api.pb \
  --proto_path=. \
  your/service.proto
```

```toml
[[servers.locations]]
match = { type = "prefix", path = "/v1/" }
[servers.locations.grpc_transcode]
target         = "grpc-backend"     # upstream name or host:port
descriptor_set = "/etc/jul/api.pb"
```

Path variables (`/v1/items/{id}`), the `body` mapping, and any leftover query
parameters are all mapped onto the request message. gRPC status codes are
translated to matching HTTP status, and per-call results are counted in
`jul_grpc_transcode_requests_total{method,code}`.

### Streaming methods

Set `streaming = true` to transcode the three streaming RPC kinds in addition to
unary calls:

| gRPC method kind | Request | Response |
| --- | --- | --- |
| **Unary** | one JSON object | one JSON object |
| **Server-streaming** | one JSON object | a stream of JSON frames, flushed per message |
| **Client-streaming** | a JSON array *or* newline-delimited JSON objects | one JSON object |
| **Bidirectional** | a JSON array *or* newline-delimited JSON objects | a stream of JSON frames |

Streamed responses are framed per `stream_mode`:

- `ndjson` (default) — `application/x-ndjson`, one JSON object per line.
- `sse` — `text/event-stream`, each message as a `data:` event.

```toml
[servers.locations.grpc_transcode]
target           = "grpc-backend"
descriptor_set   = "/etc/jul/api.pb"
streaming        = true
stream_mode      = "ndjson"   # or "sse"
max_message_size = "4m"
```

---

## `[cache]`

Jul's two-tier response cache stores upstream responses in memory (L1) and
optionally on disk (L2), reducing backend load and improving latency for
repeatable reads. It respects `Cache-Control`, `Expires`, and `Vary` headers,
and supports background revalidation (`stale_while_revalidate`) and
error-tolerant serving (`stale_if_error`).

Enable `[cache].enabled` globally, then opt individual locations in with
`cache = true`. Cache entries survive config reloads but are lost on restart
unless `disk_path` is configured. `memory_max_size`, `disk_max_size`,
`default_ttl`, `stale_while_revalidate`, and `stale_if_error` are all
hot-reloadable (#92): a change applies atomically at the next successful
reload, resizing the memory/disk stores in place (evicting strict LRU
entries/files immediately if a cap is lowered) without resetting counters or
rebuilding the cache. `enabled` and `disk_path` remain restart-required.

```toml
[cache]
enabled = true
memory_max_size = "128m"
disk_path = "./jul-data/cache"
disk_max_size = "1g"
default_ttl = "120s"
stale_while_revalidate = "60s"
stale_if_error = "30s"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch |
| `memory_max_size` | size | In-memory tier cap |
| `disk_path` | string | Enables the disk overflow tier when set |
| `disk_max_size` | size | Disk tier cap |
| `default_ttl` | duration | Used when upstream gives no explicit freshness |
| `stale_while_revalidate` | duration | Serve stale entries while refreshing asynchronously |
| `stale_if_error` | duration | Extend stale serving when a background revalidation encounters an upstream error (5xx or timeout) |

Per-location caching also requires `cache = true` on the location. See
[docs/cache.md](cache.md) for the two-tier model, on-disk format, and
overflow/eviction semantics.

---

## `[compression]`

Compress responses on the fly based on the client's `Accept-Encoding` header.
This reduces bandwidth for text-heavy payloads (HTML, JSON, XML) without
pre-generating compressed assets. `gzip` is available in every build; Brotli
(`br`) and Zstd (`zstd`) require the matching build tags and offer better
compression ratios at the cost of CPU.

Enable `precompressed` to serve pre-generated `.br` / `.gz` sidecar files for
static content, skipping runtime compression entirely.

```toml
[compression]
enabled = true
encoders = ["zstd", "br", "gzip"]
min_size = "512"
types = ["text/*", "application/json", "application/javascript"]
precompressed = true
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`) |
| `encoders` | list | Allowed encoders in server-preference order; any subset of `gzip`, `br`, `zstd` (default `["gzip"]`) |
| `level` | int | Compression level; `0` selects each encoder's own default |
| `min_size` | size | Smallest response body that is compressed (default `1k`) |
| `types` | list | MIME allow-list; a `type/*` entry matches a whole family. Defaults to text, JSON, JS, XML, SVG, and WASM when omitted |
| `precompressed` | bool | Serve sidecar `.br`/`.gz` files for static responses when present and acceptable |

---

## `[rate_limit]`

Token-bucket rate limiting protects backends from traffic spikes and abusive
clients. The global policy applies to every location; individual locations may
override the rate, burst, and key under `[servers.locations.rate_limit]`. A
per-listener concurrent-connection cap (`max_conns`) is also available.

Rate limiting is compiled into every build — no build tag required.

```toml
[rate_limit]
enabled = true
rate = 100
burst = 150
key = "ip"
max_conns = 1000
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`) |
| `key` | string | Bucket identity: `ip` (client address, default), `header:<Name>`, or `jwt:<claim>` |
| `rate` | int | Sustained requests/second allowed per key |
| `burst` | int | Maximum momentary burst above `rate` (defaults to `rate`) |
| `max_conns` | int | Concurrent connections per listener; `0` = unlimited. Active only when the block is `enabled`; listener-global, so it is ignored on per-location overrides |

---

## `[egress]`

An optional outbound-destination **allow-list** that constrains the
config-driven auxiliary fetches the server makes on its own — JWKS retrieval
(`jwks_url`), forward-auth subrequests (`url`), Consul/Kubernetes service
discovery (`address`/`api_server`), ACME/OCSP certificate calls, and WASM plugin
`fetch`. When enabled, those fetches may only reach a
destination that matches an `allow` entry; every other destination is refused at
dial time, before any bytes are sent. This bounds the SSRF blast radius of a
mistyped or maliciously edited config value.

It is **disabled by default** and compiled into every build — no build tag
required — so the block is fully backward-compatible. The **data-plane reverse
proxy** — upstream proxying and active health checks — is intentionally out of
scope: that is the traffic the server exists to carry, not an auxiliary fetch.
See [egress.md](egress.md) for the full trust model and examples.

```toml
[egress]
enabled = true
allow = ["idp.example.com", ".internal.corp", "10.0.0.0/8", "203.0.113.7"]
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`). When off, no restriction is applied |
| `allow` | list | Permitted destinations. Each entry is a CIDR (`10.0.0.0/8`, `2001:db8::/32`), a bare IP (`203.0.113.10`, treated as `/32` or `/128`), an exact hostname (`idp.example.com`), or a leading-dot suffix (`.internal.corp`, matching any subdomain). A host listed by name is resolved normally; a host not listed by name is permitted only when every resolved IP falls inside an allowed CIDR. Required (non-empty) when `enabled` |

---

## `[servers.locations.auth]`

Protect a location with access control. An optional CIDR gate runs first
(allow/deny by IP), then at most one credential-based method: HTTP Basic,
JWT validation, or forward-auth delegation.

Authentication is compiled into every build — no build tag required.

```toml
[[servers.locations]]
match = { type = "prefix", path = "/admin/" }
proxy_pass = "http://admin_panel"
  [servers.locations.auth]
  allow = ["10.0.0.0/8"]
  deny  = ["10.0.5.0/24"]
    [servers.locations.auth.basic]
    file = "/etc/jul/htpasswd"
    realm = "Admin Area"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `allow` | []string | CIDR ranges permitted to proceed. When non-empty, a client must match one |
| `deny` | []string | CIDR ranges blocked. **Deny takes precedence over allow** |

A location may then set **one** of the following sub-tables:

`[servers.locations.auth.basic]` — HTTP Basic against an `htpasswd` file:

| Key | Type | Description |
| --- | ---- | ----------- |
| `file` | string | Path to an `htpasswd` file of **bcrypt** hashes (required) |
| `realm` | string | `WWW-Authenticate` realm (default `Restricted`) |

`[servers.locations.auth.jwt]` — JWT bearer tokens validated against a JWKS endpoint:

| Key | Type | Description |
| --- | ---- | ----------- |
| `jwks_url` | string | **HTTPS** URL of the issuer's JWKS document (required); keys are cached and refreshed. A host naming a configured upstream is load-balanced |
| `timeout` | duration | Bounds one JWKS fetch; `0` (default) means 10s. Max 60s |
| `issuer` | string | When set, the token's `iss` claim must match |
| `audience` | string | When set, the token's `aud` claim must contain this value |
| `algorithms` | []string | Allowed signing algorithms (default `RS256/384/512`, `ES256/384/512`, `PS256/384/512`). Symmetric (`HS*`) and `none` are always rejected |

`[servers.locations.auth.forward_auth]` — delegate the decision to an external service:

| Key | Type | Description |
| --- | ---- | ----------- |
| `url` | string | `http(s)` URL of the auth service. The request is mirrored with `X-Forwarded-Method/Uri/Host` |
| `auth_response_headers` | []string | Response headers copied onto the upstream request on a 2xx decision |
| `timeout` | duration | Bounds one forward-auth subrequest; `0` (default) means 10s. Max 60s. A dependency that cannot be reached **denies**, never allows |

---

## `[admin]`

The admin endpoint provides runtime observability and operational control:
Prometheus metrics (`/metrics`), health (`/health`), config history/rollback,
and the optional web console dashboard. Bind it to loopback in production and
protect it with a bearer token.

The console requires the `console` build tag: `go build -tags console ./cmd/jul`.

```toml
[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "${env:JUL_ADMIN_TOKEN}"
console = true
history_dir = "./jul-data/config-history"
history_keep = 50
plugin_upload_dir = "./jul-data/plugins"
# plugin_upload_enabled defaults to false. Set to true only if you need WASM upload.
plugin_upload_enabled = false
plugin_upload_max_size = 32
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch |
| `listen` | string | Bind address — keep it on loopback (e.g. `127.0.0.1:9090`) |
| `token` | string | When set, requires `Authorization: Bearer <token>` |
| `console` | bool | Serve the web console dashboard at the admin root (default `true`; requires `console` build tag) |
| `history_dir` | string | Directory for configuration snapshots used by the console history/rollback panel |
| `history_keep` | int | Maximum number of configuration snapshots to retain; older ones are pruned (default `50`) |
| `plugin_upload_dir` | string | Directory for uploaded `.wasm` modules from the Console Plugins panel (default `./jul-data/plugins`) |
| `plugin_upload_enabled` | bool | Default `false`; set `true` to enable the `.wasm` upload endpoint. Also requires positive `plugin_upload_max_size`. |
| `plugin_upload_max_size` | int | Maximum `.wasm` upload size in megabytes (default `32`) |

### `[admin.tls]`

Terminates the admin listener with an operator-supplied certificate instead of
plaintext (#336), reusing the same certificate-rotation seam as
[`servers.*.tls`](#tls): certificate content and same-path rotation
hot-apply, with no rebind and no dropped connection. There is no ACME option
here — an operator-supplied certificate is the bounded starting point.
Enabling or disabling it is a structural transition and requires a restart,
exactly like `servers.*.tls.enabled`.

```toml
[admin.tls]
enabled = true
cert = "/etc/jul/admin-cert.pem"
key = "/etc/jul/admin-key.pem"
min_version = "1.3"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Terminate the admin listener with TLS instead of plaintext. Restart-required. |
| `cert` | string | Path to the PEM certificate file. Content and same-path rotation hot-apply. |
| `key` | string | Path to the PEM private key matching `cert`. Content and same-path rotation hot-apply. |
| `min_version` | string | `"1.2"` (default) or `"1.3"`. Restart-required. |

A malformed or missing certificate/key pair is rejected before the candidate
configuration is persisted or applied. Binding off-loopback without `[admin.tls]`
enabled produces a `jul lint` warning (L7); binding off-loopback with TLS
configured is a supported configuration.

#### `[admin.tls.client_auth]`

Optionally requires or requests a client certificate on the admin listener,
reusing [`servers.*.tls.client_auth`](#mutual-tls-client-certificates)'s exact
vocabulary and validation directly rather than a second one. It **composes
with — does not replace —** the bearer-token/RBAC layer: the handshake itself
gates the connection, and every request that reaches the handler still goes
through the normal auth chokepoint, so a valid client certificate never
bypasses the token or RBAC check and a valid token never bypasses the
certificate requirement. The whole block is restart-required, like the data
plane's mutual TLS — there is no hot path for handshake policy.

```toml
[admin.tls.client_auth]
mode = "require"
ca_file = "/etc/jul/admin-clients-ca.pem"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `mode` | string | `"none"` (default), `"request"`, or `"require"`. Restart-required. |
| `ca_file` | string | PEM bundle of CAs client certificates are verified against. Required unless `mode` is `"none"`. Content and same-path rotation are captured at restart. |
| `verify_san` | list of string | Optional allow-list of subject alternative names. |
| `crl_file` | string | Optional PEM/DER certificate revocation list, verified against `ca_file`. |
| `forward_certificate` | string | Must stay `"none"` (the default): the admin API has no backend to forward a client certificate to. Set to anything else and validation rejects the configuration. |

---

## `[observability.tracing]`

OpenTelemetry distributed tracing exports request spans to an OTLP collector,
making it easy to diagnose latency across proxy hops, cache hits, and upstream
calls. Tracing is disabled by default and requires a binary built with the
`otel` build tag.

Tracing configuration is read once at boot; a reload keeps the running tracer
(the server logs a warning if the block changed) — restart to apply tracing
changes.

```toml
[observability.tracing]
enabled = true
exporter = "otlp-grpc"
endpoint = "otel-collector:4317"
sample_ratio = 0.1
service_name = "jul-gateway"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`) |
| `exporter` | string | OTLP transport: `otlp-grpc` (default) or `otlp-http` |
| `endpoint` | string | Collector address: `host:port` for gRPC, URL/host for HTTP. Required when enabled |
| `sample_ratio` | float | Head-sampling probability for root spans, `0`..`1` (defaults to `1.0`) |
| `service_name` | string | Resource `service.name` (defaults to `jul`) |
| `insecure` | bool | Export over plaintext instead of TLS (default `false`) |

---

## `[observability.metrics]`

Tune the Prometheus metrics exposed at the admin `/metrics` endpoint. These
metrics cover HTTP requests, cache events, upstream health, rate limiting, and
more. No build tag is required.

The `host_label` setting controls whether the request `Host` header is added as
a Prometheus label. It is **off by default** because unbounded host values can
explode metric cardinality.

```toml
[observability.metrics]
host_label = false
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `host_label` | bool | Add the request `Host` as the `host` label on `jul_http_requests_total` and `jul_http_request_duration_seconds` (default `false`) |

The `host` label is **off by default**: the Host header is client-controlled, so
recording it unconditionally lets a flood of distinct Host values explode metric
cardinality. Enable `host_label` only when the set of hosts is bounded. The
setting is read once at boot; a reload keeps the running value — restart to
apply a change.

Every other metric label is bounded by construction — the request `method` is
folded to a fixed set (unknown tokens become `other`), and no request path,
query, client IP, or user-agent is ever a label. See the full
[label-cardinality policy and relabel cookbook](core-http.md#metrics) for the
authoritative inventory and scale guidance.

---

## `[observability.access_log]`

Control where the HTTP access log is written. You can send logs to `stdout`, a
rotating file, or the local syslog daemon (Unix only). This is useful for
shipping logs to SIEM or audit systems without external agents.

No build tag required.

```toml
[observability.access_log]
enabled = true
sinks = ["stdout", "file"]
file = "/var/log/jul/access.log"
format = "json"
rotate_max_mb = 500
rotate_keep = 14
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Emit request access records. Omitted defaults to `true`; explicit `false` disables stdout/file/syslog and the Console access-record tail only. |
| `sinks` | []string | Destinations: any of `stdout`, `file`, `syslog`. Omitted defaults to `["stdout"]`; an explicit empty list is invalid while enabled. |
| `file` | string | Access-log file path. Required whenever `file` is listed, including dormant disabled configuration. |
| `format` | string | Encoding of the `file` and `syslog` sinks: `text` (logfmt, default) or `json`. The `stdout` sink always follows `[global].log_format`. |
| `rotate_max_mb` | int | File size in MB at which the file rotates (default `100`). |
| `rotate_keep` | int | Maximum number of rotated files to retain (default `7`). |

When `enabled = false`, Jul opens no access-log file or syslog resource and emits no request access records to the Console tail. Process/application, reload, security/WAF, audit, health, metrics, and tracing output remain independent. Sink details remain stored and validated while dormant so re-enabling is deterministic.

The `syslog` sink uses the local system log and is **not supported on Windows**. Every field in this block hot-applies (#98): a candidate sink generation is built and validated before Publish, then committed with the new handler generation, and the previous generation's file/syslog resources close only after its own in-flight requests drain. The permanent Console Operations-Log tail is never recreated by an access-log change.

---

## TLS

TLS is configured per server block. Jul supports TLS 1.2 and 1.3, SNI
certificate selection, and dynamic certificate selection at listener startup.
Static `cert`/`key` rotation on an already-bound listener hot-applies, without a
rebind or dropped connections (#100); an ACME domain-set or issuer change still
requires a process restart. Use `redirect_https` on a plain HTTP server to force clients onto HTTPS.

```toml
[[servers]]
listen = "0.0.0.0:8443"
server_names = ["example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/tls/example.crt"
  key  = "/etc/jul/tls/example.key"
  min_version = "1.2"   # "1.2" or "1.3"
```

To force HTTPS, add an HTTP server block with `redirect_https = 308`:

```toml
[[servers]]
listen = "0.0.0.0:80"
server_names = ["example.com"]
redirect_https = 308
```

### Mutual TLS (client certificates)

Jul can authenticate the *client* by its certificate. With
`[servers.tls.client_auth]` it verifies the certificate against a CA bundle,
optionally checks a CRL and a SAN allow-list, and exposes the verified identity
to upstreams as `$ssl_client_*` proxy variables.

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["api.example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/server.crt"
  key  = "/etc/jul/server.key"

    [servers.tls.client_auth]
    mode = "require"
    ca_file = "/etc/jul/clients-ca.pem"

  [[servers.locations]]
  match = { type = "prefix", path = "/api" }
  proxy_pass = "http://127.0.0.1:9000"
  require_client_cert = true
    [servers.locations.headers]
    X-Client-CN = "$ssl_client_cn"
```

The full reference lives in [docs/mtls.md](mtls.md).

---

## Automatic HTTPS (ACME)

Jul can obtain and renew certificates automatically from an ACME certificate
authority (Let's Encrypt by default) using the **HTTP-01** or **TLS-ALPN-01**
challenge. This eliminates manual certificate provisioning and renewal.
This feature is gated behind the `acme` build tag.

```bash
go build -tags acme -o jul ./cmd/jul
```

Configure it under `[servers.tls.acme]` on a `:443` server block, and keep a
plain `:80` block running so the challenge can be answered:

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com", "www.example.com"]

  [servers.tls.acme]
  enabled = true
  email = "ops@example.com"
  ca = "letsencrypt-staging"
  challenge = "http-01"
  cache_dir = "./jul-data/certs"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv/www/example"

[[servers]]
listen = "0.0.0.0:80"
server_names = ["example.com", "www.example.com"]
redirect_https = 308
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Turn on ACME for this server block |
| `email` | string | **Required.** ACME account contact address |
| `ca` | string | `letsencrypt-staging` (default), `letsencrypt`, or a custom `https://` directory URL |
| `domains` | []string | Certificate host names; defaults to `server_names` |
| `challenge` | string | `http-01` (default) or `tls-alpn-01`. `dns-01` is reserved for a future release |
| `cache_dir` | string | Directory where issued certificates are cached (default `./jul-data/certs`) |
| `ocsp_stapling` | bool | Staple OCSP responses onto served certificates (default `true`) |

- The **default CA is staging** (untrusted certificates, generous rate limits).
  Switch to `ca = "letsencrypt"` only after staging works end to end.
- A single listener address may not mix ACME and static `cert`/`key` server
  blocks; validation rejects that.
- The ACME domain set is fixed at startup — enabling ACME or adding domains
  needs a restart.
- Certificate expiry and renewals are exported as `jul_tls_cert_expiry_seconds`
  and `jul_acme_renewals_total`.

See [examples/auto-https](../examples/auto-https) for a runnable walkthrough.

---

## HTTP/3 (QUIC)

Add a `[servers.http3]` block to a **TLS-enabled** server to also serve HTTP/3
over QUIC on the same address (UDP), sharing the same TLS certificate. Clients
discover it via an `Alt-Svc` response header. This requires the `http3` build
tag.

```bash
go build -tags http3 -o jul ./cmd/jul
```

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/tls/example.crt"
  key = "/etc/jul/tls/example.key"

  [servers.http3]
  enabled = true
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Turn on the HTTP/3 listener for this server block |
| `alt_svc_max_age` | int | `Alt-Svc` advertisement lifetime in seconds (default `86400`) |

Notes:

- **TLS is required.** QUIC mandates TLS 1.3; validation rejects `http3` on a
  plain listener.
- **Open the UDP port.** HTTP/3 listens on UDP at the same port as TCP.
- **Shared certificates, including renewals.** Static-cert reload or ACME
  renewal applies to HTTP/3 automatically.
- **WebSocket is not supported over HTTP/3.** Clients transparently fall back
  to HTTP/2.
- Settings are fixed at startup; changing them needs a restart.

---

## `[plugins.<name>]`

> Requires a binary built with `-tags wasmplugins`.

The WebAssembly plugin runtime lets you extend Jul without recompiling the
server. Each entry under `[plugins]` declares one Wasm module by name.
Plugins can act as middleware (wrapping a location) or as a terminal handler (replacing
a location action). Capabilities — KV store, outbound HTTP fetch — are disabled
by default and granted explicitly per plugin for security.

```toml
[plugins.header-inject]
path = "./plugins/header-inject.wasm"
type = "middleware"
memory_limit = "16m"
timeout = "100ms"
config = { header = "X-Plugin", value = "header-inject" }

[plugins.kv-counter]
path = "./plugins/kv-counter.wasm"
kv = true
```

| Key | Meaning |
| --- | ------- |
| `path` / `inline` | Module source — supply exactly one |
| `type` | `middleware` (wraps a handler) or `handler` (terminal location action) |
| `config` | String map handed to the guest as JSON via `get_config` |
| `memory_limit` | Guest linear-memory ceiling (default 16 MiB) |
| `timeout` | Deadline for a single invocation; guest is torn down on overrun (default 100ms) |
| `kv` | Grant the key/value store host functions (namespaced per plugin) |
| `fetch` / `allowed_hosts` | Grant guarded outbound HTTP to the listed hosts |

Attach a plugin to traffic by referencing its name. Server- and location-level
`plugins = [...]` lists run as **middleware** (outermost first); a location
`plugin = "name"` is a terminal **handler** action.

See [docs/plugins.md](plugins.md) for the authoring guide.

---

## `[[stream]]`

> Requires a binary built with `-tags stream`.

Each `[[stream]]` block is one L4 (TCP or UDP) reverse-proxy listener that
forwards raw connections — no HTTP parsing. Use this for databases, game
servers, or any non-HTTP TCP workload. UDP listeners are stateful relays with
session expiry. TLS SNI passthrough lets you route encrypted traffic by server
name without terminating TLS.

```bash
go build -tags stream -o jul ./cmd/jul
```

```toml
# Plain TCP load balancing.
[[stream]]
listen = "0.0.0.0:5432"
proxy_pass = "postgres_pool"
connect_timeout = "10s"
idle_timeout = "5m"

# TLS SNI routing (passthrough — TLS is never terminated).
[[stream]]
listen = "0.0.0.0:443"
  [stream.sni_routes]
  "api.example.com" = "api_pool"
  "*"               = "default_pool"

# UDP relay with PROXY protocol.
[[stream]]
listen = "0.0.0.0:53"
protocol = "udp"
proxy_pass = "dns_pool"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `listen` | string | Bind address `host:port` (**required**) |
| `protocol` | string | `tcp` (default) or `udp` |
| `proxy_pass` | string | Default backend — named upstream or literal `host:port` |
| `sni_routes` | table | TLS server-name → backend map; routes by SNI **without terminating TLS** |
| `tls_passthrough` | bool | Informational; implied whenever `sni_routes` is set |
| `proxy_protocol` | string | HAProxy PROXY-protocol handling: `""`, `"in"`, `"out"`, or `"both"` |
| `trusted_proxies` | []string | Peers permitted to assert a client address with an inbound PROXY header. Required when `proxy_protocol` is `"in"` or `"both"`, rejected otherwise; a connection from outside the set is refused |
| `connect_timeout` | duration | Backend dial timeout (default `10s`) |
| `idle_timeout` | duration | Close relayed connection / UDP session after this idle (default `5m`) |
| `max_udp_sessions` | int | Cap concurrent UDP sessions (default `10000`) |

Provide at least one of `proxy_pass` or `sni_routes`. UDP listeners are plain
relays: `sni_routes`, `tls_passthrough`, and `proxy_protocol` are TCP-only and
rejected on a UDP block.

See [docs/stream-proxy.md](stream-proxy.md) for the full runtime model.

---

## CLI JSON output

`jul lint` and `jul check` accept `-json` for machine-readable output so CI can
parse findings instead of scraping text. Field names are lowercase and stable.

### `jul lint -json`

```json
{
  "source": "server.toml",
  "errors": ["servers[0].locations[0]: match is required"],
  "warnings": [
    {
      "severity": "warning",
      "field": "servers[0] (listen \":8080\")",
      "message": "server has no locations; every request will return 404",
      "hint": "add a [[servers.locations]] block, or set redirect_https for an HTTP->HTTPS redirector"
    }
  ]
}
```

| Field | Type | Description |
| --- | ---- | ----------- |
| `source` | string | Config source name (path or `stdin`) |
| `errors` | string[] | Validation errors; omitted when empty. Any entry ⇒ exit code `1` |
| `warnings` | object[] | Lint findings; omitted when empty |
| `warnings[].severity` | string | `"warning"` or `"error"` — always a string, never a number |
| `warnings[].field` | string | Config path the finding applies to; omitted when empty |
| `warnings[].message` | string | Human-readable description of the finding |
| `warnings[].hint` | string | Suggested fix; omitted when empty |

Exit codes: `0` = no errors, `1` = validation error(s), `2` = warnings present
under `-strict`.

### `jul check -json`

```json
{ "source": "server.toml", "ok": true }
```

On failure `ok` is `false` and either `error` (single message) or `errors`
(string array) is present.

### Console handoff and planned-restart race safety (#81)

A structured console preview carries the exact ordered operations, pinned base
version, value-free diff and lifecycle result, validation and operation
summaries, recommended action, candidate availability, and a value-free snapshot
of managed pending-restart state. ConfigPanel loads current pending state before
enabling its primary action. If the snapshot changed it re-previews the exact
ordered operations against the pinned base; if the base moved it blocks and
requires regeneration. It never pairs an old candidate with a newer token.

A managed staged update preserves the original rollback base, and a mixed
hot/restart candidate stages as one complete candidate. Final result correlation
continues through the exact apply ID; overview values and unrelated polls are
not success evidence. A `reload_timeout` edit uses the currently active timeout
for its own transaction and governs later transactions.

Full candidate TOML remains a `config:raw` capability and memory-only UI state.
The versioned browser-storage handoff contains only established non-secret
structured operations and preview metadata. Literal candidate persistence was
intentionally not implemented because it would weaken the raw/secret boundary.
