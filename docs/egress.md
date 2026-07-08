# Egress allow-list

> Optional hardening that constrains the destinations the server itself connects
> to for its **config-driven auxiliary fetches** — JWKS retrieval, forward-auth
> subrequests, and Consul/Kubernetes service discovery. It is disabled by
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
| JWKS retrieval (`[…auth.jwt] jwks_url`) | ✅ | |
| Forward-auth subrequest (`[…auth.forward_auth] url`) | ✅ | Redirect relay preserved |
| Consul discovery (`[…discovery.consul] address`) | ✅ | `-tags consul` |
| Kubernetes discovery (`[…discovery.kubernetes] api_server`) | ✅ | `-tags kubernetes`; TLS preserved |
| DNS / DNS-SRV discovery | — | Uses the system resolver, not an HTTP client |
| **Upstream proxying / active health checks** | — | This *is* the intended traffic; the allow-list is for auxiliary fetches, not backends |
| **ACME issuance** | — | Talks to a well-known, multi-host CA; guarding it risks breaking issuance for little SSRF value |

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

The underlying error is `egress blocked: destination not in the [egress]
allow-list`.

## Security notes

| Threat | Status | Mechanism |
| --- | --- | --- |
| SSRF via `jwks_url` / forward-auth `url` to an internal endpoint | 🟢 mitigated | Dial-time allow-list on the auth clients |
| SSRF via discovery `address` / `api_server` | 🟢 mitigated | Dial-time allow-list on the Consul/K8s clients |
| Redirect to a blocked host | 🟢 mitigated | Guard runs per connection, so redirected dials are re-checked |
| DNS rebinding of a name-trusted host | 🟠 out of scope | Trust by name is explicit; use CIDR entries for IP-level enforcement |
| Config file compromise | 🟠 out of scope | The config is the trust boundary; this narrows, not replaces, it |

## Limits

- Guards only the auxiliary fetch paths above — **not** upstream proxying, active
  health checks, or ACME (see the table).
- Port is not part of a host rule: a name-allowed host is reachable on any port.
- Applied at startup; changes need a restart.

## Build tags

Egress is **core** — no build tag. The Consul/Kubernetes guard is only reachable
in builds that compile those discoverers (`-tags consul` / `-tags kubernetes`).
