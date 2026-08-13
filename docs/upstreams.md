# Upstreams and backend trust

> **Maturity:** the pool itself is GA (see [status.md](status.md)); `backend_tls`
> is new and its lifecycle is deliberately conservative — see
> [Reload behaviour](#reload-behaviour).

An `[[upstreams]]` block is a named pool of backends with a balancing strategy,
passive and optional active health checking, optional dynamic discovery, and —
new — an explicit outbound TLS policy.

```toml
[[upstreams]]
name     = "inventory"
strategy = "round_robin"
servers  = ["10.0.10.41:8443", "10.0.10.42:8443"]

  [upstreams.backend_tls]
  ca_file     = "/etc/jul/backend-ca.pem"
  ca_mode     = "system_and_file"
  server_name = "inventory.internal"
```

Routes reference the pool by name:

```toml
[[servers.locations]]
match      = { type = "prefix", path = "/inventory/" }
proxy_pass = "https://inventory"
```

## Contents

- [Pool basics](#pool-basics)
- [`backend_tls`](#backend-tls)
- [Trust roots](#trust-roots)
- [Client certificates (mutual TLS)](#client-certificates-mutual-tls)
- [The verified name](#the-verified-name)
- [Peer identities](#peer-identities)
- [Disabling verification](#disabling-verification)
- [Where the policy may be declared](#where-the-policy-may-be-declared)
- [Reload behaviour](#reload-behaviour)
- [Health checks and backend trust](#health-checks-and-backend-trust)

## Pool basics

| Key | Type | Description |
| --- | ---- | ----------- |
| `name` | string | The pool's name, referenced as `proxy_pass = "https://name"` |
| `servers` | []string \| []table | Backends, as `"host:port"` or `{ address, weight }` |
| `strategy` | string | `round_robin` (default), `weighted_round_robin`, `least_conn` |
| `max_fails` / `fail_timeout` | int / duration | Passive health: park a backend after N consecutive failures |
| `health_check` | table | Active probes — see [health.md](health.md) |
| `discovery` | table | Dynamic backends — see [service-discovery.md](service-discovery.md) |
| `backend_tls` | table | Outbound TLS policy — below |

> **Resilience.** `max_fails` and `fail_timeout` are Jul's circuit breaker: N consecutive failures
> open the backend for the cooldown, after which the next request probes it and the next failure
> re-trips it. [ADR 0017](adr/0017-upstream-resilience-and-overload-control.md) makes that model
> explicit and decides the concurrency, pending, connection, retry-budget and half-open controls that
> extend it. Those controls are an accepted decision under implementation (#141, #142, #143, #144);
> the keys above are what the running binary reads today.

## backend_tls

`backend_tls` is the **outbound** TLS policy. It is a different key from the
inbound `[servers.tls]` block on purpose: `tls` already means *inbound*
termination under `[[servers]]`, and one key with opposite directions in two
places is a mistake waiting to happen.

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `ca_file` | string | — | PEM bundle of trust roots. Consulted only when `ca_mode` selects it. |
| `ca_mode` | string | `system` | `system`, `system_and_file`, or `file_only`. |
| `client_cert` / `client_key` | string | — | Client certificate presented to the backend. Both or neither. |
| `server_name` | string | derived | The name the backend certificate is verified against, and the SNI value sent. |
| `min_version` | string | `1.2` | `1.2` or `1.3`. |
| `peer_identities` | []string | — | Prefixed identities (`dns:`, `uri:`) required of the backend certificate, in addition to standard verification. |
| `insecure_skip_verify` | bool | `false` | Disables verification. `jul lint` reports this as an **error**. |

Without a `backend_tls` block an `https://` backend is verified the way any Go
client verifies: the platform trust store, and the hostname from the target.
The block exists for everything that needs more — a private CA, a client
certificate, an SNI that differs from the dial address, or an explicit identity.

## Trust roots

`ca_mode` is an **explicit enum and is never inferred from the presence of
`ca_file`**:

| Mode | Meaning |
| --- | --- |
| `system` *(default)* | The platform trust store only. A `ca_file` set under this mode is a configuration error, not a silent no-op. |
| `system_and_file` | The platform trust store **plus** `ca_file`. |
| `file_only` | `ca_file` **replaces** the platform store. |

Inference is unrevertable: if the presence of `ca_file` implied augmentation,
changing that later to replacement would silently alter which backends verify,
with no error anywhere. An explicit enum makes the choice reviewable.

`file_only` is the stricter setting: a backend signed by a public CA will fail,
which is usually what you want for a purely internal service.

## Client certificates (mutual TLS)

```toml
[upstreams.backend_tls]
client_cert = "/etc/jul/client.pem"
client_key  = "/etc/jul/client.key"
```

Both are required together; either alone is a validation error. The pair is
parsed when the configuration is prepared, so a mismatched certificate and key
fail the reload rather than the first request. The key is read, parsed, and its
bytes dropped: it is never logged, never projected into the Console, and never
represented in status output — not even as a digest.

Protect the key file with `0600` permissions and a service-owned directory, as
with any private key ([deployment.md](deployment.md)).

## The verified name

`server_name` sets both the SNI value and the name the certificate is checked
against. When it is omitted, the name is derived from the configured logical
target — the upstream name or the literal host — with any port removed.

This matters most for a **discovery-backed** pool. Discovery returns addresses;
addresses are dial destinations only. The configured logical name stays the
verified identity, so a compromised registry that returns an attacker's address
cannot also choose the name that address is verified against — the handshake
fails instead.

An IP-literal target with no `server_name` is verified against the certificate's
IP SANs under Go's standard rules. If your backend certificate carries a DNS
name rather than an IP SAN, set `server_name` explicitly.

## Peer identities

Standard verification proves the certificate chains to a trusted root and
carries the expected name. `peer_identities` additionally requires the
certificate to carry one of a set of identities:

```toml
peer_identities = ["dns:inventory.internal", "uri:spiffe://example.org/inventory"]
```

- Entries are **prefixed** (`dns:`, `uri:`) from the first release, so future
  identity types are additive rather than ambiguous.
- Identities are **ORed** — matching any one is enough.
- The check runs **after** standard verification, never instead of it. A
  configuration cannot use an identity check to skip chain validation.
- Matching is exact, using the certificate's own SAN semantics. There is no
  regex, no substring matching, and no invented wildcard grammar; a wildcard in
  a certificate is matched by the certificate's own rules.

## Disabling verification

```toml
insecure_skip_verify = true   # jul lint: ERROR
```

This disables **peer verification only** — the connection is still encrypted,
but Jul no longer knows who is on the other end, which is the property that
matters. It exists as an emergency path, and it is treated accordingly:

- `jul lint` reports it as an **error**, so the command exits 1 even without
  `-strict`.
- The server logs one warning per affected backend at startup.
- The Console security projection counts the affected policies.
- `Validate()` still accepts it, because a field whose only purpose is opting
  into an insecure mode cannot be a validation rejection — it would be unusable.

Two combinations *are* hard errors, because they are self-contradictory:
`insecure_skip_verify` with `peer_identities` (nothing can be proven about a
peer that was never verified), and with a non-`system` `ca_mode` (trust roots
that would never be consulted).

No metric label ever carries an upstream name, so the count is bounded even
when the names are not.

## Where the policy may be declared

The same block, with the same meaning, appears in two places:

| Place | Applies to |
| --- | --- |
| `[upstreams.backend_tls]` | Every route that reaches this pool over TLS |
| `[servers.locations.backend_tls]` | That one route, whether it targets a pool, a literal `https://` address, native gRPC over TLS, or a TLS transcoding target |

When both exist, **the location's policy applies to that route** and `jul lint`
reports the override so it is never silent.

A `backend_tls` block on a route whose backend is *not* reached over TLS is a
validation error rather than a no-op: an operator who writes one believes the
backend is verified, and a silently inert security policy is worse than a
rejected configuration.

## What enforces the policy today

| Consumer | Status |
| --- | --- |
| HTTP reverse proxy (named pools and literal `https://` targets, including WebSocket and streaming upgrades) | **enforced** |
| Native gRPC passthrough | **enforced** |
| gRPC-JSON transcoding, including the reflection fetch | **enforced** |
| Active health probes | **enforced** — see [Health checks and backend trust](#health-checks-and-backend-trust) |

The HTTP proxy builds one `http.Transport` per handler generation from the
resolved policy, so a policy change produces a new connection pool: a request
admitted after the change cannot reuse a keep-alive or HTTP/2 connection
established under the previous trust, and the retiring generation closes its
idle connections when it retires. Requests already in flight finish on their own
connection, which is the generation drain contract rather than an abrupt cut.

A route configured for `https` never dials a plaintext backend, and a TLS gRPC
route never falls back to cleartext h2c. No retry, failover or discovery result
may downgrade the connection; the request fails instead.

For gRPC the policy's TLS config advertises `h2` explicitly, because those
transports speak HTTP/2 only — a backend that negotiated `http/1.1` would break
gRPC framing. The transcoder's connection cache lives on the transcoder, which
is rebuilt with its handler generation, so a policy change produces fresh
connections rather than reusing ones established under the previous trust.

### Failure categories

A backend-trust failure is logged with a bounded `tls_failure` category
alongside the usual proxy error, so it can be grepped and later counted without
the raw error, host, certificate subject or file path becoming a label:

`unknown_authority` · `hostname_mismatch` · `peer_identity_mismatch` ·
`client_certificate` · `certificate_expired` · `certificate_invalid` ·
`tls_version` · `tls_handshake` · `tls_other`

## Reload behaviour

`backend_tls` is **hot reload**, and that classification was earned rather than
assumed: every consumer demonstrably rebuilds from the candidate policy.

- The HTTP proxy, native gRPC and the transcoder build their clients with the
  handler generation that owns them.
- The **resolved policy's fingerprint is part of the pool's identity**, so a
  changed policy rebuilds the pool and with it the probe client. Because the
  fingerprint digests file *contents*, rotating a certificate in place — with no
  configuration edit at all — is detected and applied on the next reload.

A malformed policy fails while the candidate is prepared, so the reload aborts
before anything is published rather than leaving a backend unverifiable.

The classification is conditional on the **backend set**, not on the listener
set:

| Change | Verdict |
| --- | --- |
| Adding a pool or route that carries a policy | applies on this reload — there is no running client to strand |
| Removing one | applies on this reload |
| Editing the policy of a pool or route that survives the reload | restart required |

Trust material is registered as secret-bearing from the first release, so the
lifecycle fingerprint digests the **contents** of `ca_file`, `client_cert` and
`client_key`. Rotating a certificate in place, without editing the
configuration, is therefore detected correctly — even while the action remains a
restart. Detection and action are separable, and getting detection right early
costs nothing. See [reload-semantics.md](reload-semantics.md).

## Health checks and backend trust

An active HTTP probe inherits the pool's scheme, and now also its trust: the
probe client uses the **same resolved policy as live traffic**. A backend is
never reported healthy under weaker verification than the requests Jul will send
it — and, equally, a private-CA backend that live traffic verifies is no longer
reported unhealthy because the probe could not.

The policy that governs probes is the **pool's**. A route-level `backend_tls`
block applies to that route's traffic only; a pool may serve several routes with
different overrides, so there is no single route policy a probe could adopt. Put
the trust roots a probe needs on the pool.

A TCP probe remains a reachability check and never represents identity
verification.

`jul lint` still warns when an `https` pool with an enabled HTTP probe has no
policy at all, because such a pool is verified against the platform store alone.

See [health.md](health.md).
