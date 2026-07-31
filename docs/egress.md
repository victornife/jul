# Egress allow-list

> Optional hardening that constrains the destinations the server itself connects
> to for its **config-driven auxiliary fetches** — JWKS retrieval, forward-auth
> subrequests, Consul/Kubernetes service discovery, ACME/OCSP certificate
> issuance and revocation checks, and WASM plugin `fetch`. It is disabled by
> default and fully backward-compatible.

## Why

Jul.IA trusts its configuration: an operator who can edit the config can already
change routing, upstreams, and TLS. But several config fields are **URLs the
server dereferences on its own** — `jwks_url`, forward-auth `url`, and the
discovery `address`/`api_server`. A mistyped or maliciously edited value there
turns the server into an SSRF vector: it will happily fetch from an internal
metadata endpoint (`169.254.169.254`), a database admin port, or any host the
config names.

The `[egress]` allow-list reduces that blast radius. When enabled, those fetches
may only reach an operator-approved set of hosts and CIDRs; every other
destination is refused **at connect time**, before any bytes are sent.

## Trust model

- This is **defense-in-depth for operator error and config compromise**, not a
  substitute for protecting the config file. A trusted config is still the
  security boundary; the allow-list narrows what a bad config can reach.
- It is an **allow-list**, not an SSRF IP-reputation filter. You decide what is
  reachable; a private CIDR is allowed if you list it (internal IdPs and
  in-cluster discovery are legitimate).
- A host listed **by name** is trusted and resolved normally. A host **not**
  listed by name is permitted only when *every* resolved IP falls inside an
  allowed CIDR, so a record mixing an allowed and a disallowed address is
  rejected rather than raced. DNS rebinding of an explicitly name-trusted host
  is out of scope — use CIDR entries for IP-level enforcement.

## What is guarded

| Path | Guarded | Notes |
| --- | --- | --- |
| JWKS retrieval (`[…auth.jwt] jwks_url`) | ✅ | Subsystem `auth` |
| Forward-auth subrequest (`[…auth.forward_auth] url`) | ✅ | Subsystem `auth`; redirect relay preserved |
| Consul discovery (`[…discovery.consul] address`) | ✅ | Subsystem `discovery`; `-tags consul` |
| Kubernetes discovery (`[…discovery.kubernetes] api_server`) | ✅ | Subsystem `discovery`; `-tags kubernetes`; TLS preserved |
| ACME directory/order/challenge (`[…tls.acme]`) | ✅ | Subsystem `acme`; `-tags acme`; guarded only when `[egress]` is enabled — see [ACME & OCSP prerequisites](#acme--ocsp-prerequisites) |
| OCSP responder fetch (stapling) | ✅ | Subsystem `ocsp`; `-tags acme`; guarded only when `[egress]` is enabled |
| WASM plugin `fetch` (`[[plugins]] allowed_hosts`) | ✅ | Subsystem `plugin`; `-tags wasmplugins`; **intersection** of the plugin's `allowed_hosts` + SSRF guard **and** the global allow-list — see [Plugin fetch](#plugin-fetch) |
| DNS / DNS-SRV discovery | — | Uses the system resolver, not an HTTP client |
| **Upstream proxying / active health checks** | — | This *is* the intended traffic; the allow-list is for auxiliary fetches, not backends |
| **Console / browser calls** | — | Browser-originated, not server egress |

When `[egress]` is **disabled** (the default) none of these is restricted and the
ACME/OCSP clients keep their standard behaviour, including `HTTP_PROXY` /
`HTTPS_PROXY` support. When it is **enabled**, guarded clients pin `Proxy = nil`
and therefore ignore `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY`, so a proxy
address can never hide the real destination from the allow-list.

## Quick start

```toml
[egress]
enabled = true
allow = [
  "idp.example.com",       # exact hostname
  ".discovery.internal",   # any subdomain of discovery.internal
  "10.0.0.0/8",            # a CIDR (matches an IP)
  "203.0.113.10",          # a bare IP (treated as /32)
]
```

With this policy a `jwks_url` of `https://idp.example.com/keys` is allowed, while
`https://169.254.169.254/latest/meta-data/` is refused with an *egress blocked*
error at connect time.

## Configuration reference

### `[egress]`

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | Turn the allow-list on. When off, no restriction is applied. |
| `allow` | []string | — | Permitted destinations. Required when `enabled`. |

Each `allow` entry is one of:

- a **CIDR** — `10.0.0.0/8`, `2001:db8::/32` (matches an IP);
- a **bare IP** — `203.0.113.10` (treated as `/32` or `/128`);
- an **exact hostname** — `idp.example.com`;
- a **leading-dot suffix** — `.internal.corp` (matches any subdomain, *not* the
  apex `internal.corp`).

Hostnames are matched case-insensitively. A URL (`https://…`) is **not** a valid
entry — use the host, IP, or CIDR only; the config validator rejects the rest.

## Semantics & precedence

At dial time, for a destination `host:port`:

1. If `host` is an **IP literal**, it is allowed iff it is inside an allowed
   CIDR.
2. Else if `host` matches a **host rule** (exact or `.suffix`), it is allowed
   and resolved normally.
3. Else the server resolves `host`; it is allowed only if **every** resolved IP
   is inside an allowed CIDR. If any resolved IP is outside, or none resolve, the
   connection is refused.

Enforcement is at the transport `DialContext`, so it also covers **redirects**:
a JWKS endpoint that 302-redirects to a blocked host is still refused. TLS SNI
and the HTTP `Host` header keep the original hostname; only the dial target is
constrained.

## Reload behaviour

The policy is built once from the **startup** configuration; changing `[egress]`
takes effect after a **restart** (like listener bind-time settings). This keeps
the guard consistent for the process lifetime, including the discovery refreshers
that run for the whole run.

## Errors

A blocked fetch fails the operation with a clear cause:

- JWKS: the key cannot be fetched, so token validation fails (`401`).
- Forward-auth: the auth service is unreachable, surfaced as `503` (fail-closed,
  never a silent allow).
- Discovery: the resolve fails and the pool keeps its last-good backend set.
- ACME: certificate issuance/renewal fails until the CA hosts are allow-listed.
- OCSP: the staple cannot be refreshed, so the certificate is served unstapled
  (graceful) rather than failing the handshake.
- Plugin `fetch`: the guest receives a distinct blocked return code (`-5`) for a
  global-policy denial, separate from a plugin-local `allowed_hosts`/SSRF block
  (`-3`).

The underlying error names the subsystem, the normalized destination host, and a
bounded reason (`host_not_allowed`, `ip_not_allowed`, `mixed_dns_answers`,
`no_dns_answers`, `invalid_address`); it never includes credentials or query
strings. Every denial wraps the `egress blocked: destination not in the [egress]
allow-list` sentinel.

## ACME & OCSP prerequisites

When `[egress]` is enabled, the ACME client (directory, order, and challenge
calls) and the OCSP responder client are guarded like every other auxiliary
fetch. Certificate issuance therefore **requires the CA and OCSP hosts to be in
`allow`** — otherwise issuance and stapling fail closed. Public ACME CAs front
their endpoints with CDNs whose IPs rotate, so prefer **name** entries (or a
covering suffix) over fixed CIDRs. See
[tls-acme.md](tls-acme.md#egress-allow-list-prerequisites) for the concrete
Let's Encrypt host set.

## Plugin fetch

A WASM plugin's `fetch` must satisfy **both** its plugin-local rules (the
`allowed_hosts` list and the always-on SSRF guard that refuses loopback, private,
and CGNAT addresses) **and** the global allow-list. The global guard evaluates
the requested host and every resolved IP before the plugin's SSRF dialer runs, so
a destination the plugin allows but the server does not is refused. The guest
sees the distinct return code `-5` for a global-policy denial; see
[plugins.md](plugins.md).

## Metrics, logs, and diagnostics

Every decision is reported to bounded Prometheus counters — labelled only by
subsystem, result, and reason, **never** by destination host or IP:

- `jul_egress_decisions_total{subsystem,result,reason}` — allow/block decisions
  (`reason` is empty on an allow);
- `jul_egress_dns_answers_total{subsystem,result}` — CIDR-only hostname
  resolutions evaluated.

On a **block**, the server also emits a structured, rate-limited log line so an
operator can act on a refusal without scraping metrics. Each entry carries the
`subsystem`, the normalized `host`, an optional `resolved_ip`, and the bounded
`reason` — **never** a URL, query string, or credential. Identity, discovery, and
PKI blocks (`auth`, `discovery`, `acme`, `ocsp`) log at **warning**; plugin-fetch
denials (`plugin`), which are guest-triggered and expected, log at **info**.
Identical events (same subsystem, host, and reason) are collapsed within a short
window so a retry loop cannot flood the log.

The Console **Security** panel surfaces whether the allow-list is enabled, the
allow-rule count, a recent-blocked breakdown by subsystem and reason, and a link
to this page. No destination history is retained.

## Security notes

| Threat | Status | Mechanism |
| --- | --- | --- |
| SSRF via `jwks_url` / forward-auth `url` to an internal endpoint | 🟢 mitigated | Dial-time allow-list on the auth clients |
| SSRF via discovery `address` / `api_server` | 🟢 mitigated | Dial-time allow-list on the Consul/K8s clients |
| SSRF via ACME/OCSP or plugin `fetch` | 🟢 mitigated | Dial-time allow-list on the PKI and plugin clients (plugin: intersected with its own SSRF guard) |
| Redirect to a blocked host | 🟢 mitigated | Guard runs per connection, so redirected dials are re-checked |
| Environment proxy hides the target | 🟢 mitigated | Guarded clients pin `Proxy = nil` and ignore `HTTP(S)_PROXY`/`NO_PROXY` |
| DNS rebinding of a name-trusted host | 🟠 out of scope | Trust by name is explicit; use CIDR entries for IP-level enforcement |
| Config file compromise | 🟠 out of scope | The config is the trust boundary; this narrows, not replaces, it |

## Limits

- Guards the auxiliary fetch paths above — **not** upstream proxying or active
  health checks (see the table). The **data-plane reverse proxy** (routing client
  traffic to your backends) is deliberately **out of scope**: that is the traffic
  the server exists to carry, and it is governed by routing/upstream config, not
  the egress allow-list.
- Port is not part of a host rule: a name-allowed host is reachable on any port.
- Applied at startup; changes need a restart.

## Build tags

Egress is **core** — no build tag. The subsystem guards that live behind build
tags are only reachable in builds that compile them: Consul/Kubernetes discovery
(`-tags consul` / `-tags kubernetes`), ACME/OCSP (`-tags acme`), and WASM plugin
`fetch` (`-tags wasmplugins`).
