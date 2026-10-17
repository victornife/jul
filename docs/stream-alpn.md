# Offered-ALPN TCP passthrough routing

**Alpha, implemented on the #434 working branch, unreleased.** This extension
has separate evidence from the released GA L4 proxy. Required final coverage,
CI and migration-reference results are recorded in the
[delivery evidence](audit/434-alpn/README.md). Build with `-tags stream`.

Route two TLS services sharing a hostname and TCP port while leaving TLS keys,
certificates, negotiation and application authorization at their backends.
For example, a `jul-demo/1` application and an HTTP endpoint can share
`shared.example:8443`. This is an engineering demonstrator, not external demand.
SNI alone cannot distinguish them. See the complete
[example](../examples/alpn-passthrough/jul.toml).

```toml
[[stream]]
listen = "127.0.0.1:8443"
proxy_pass = "127.0.0.1:9444"

  [[stream.alpn_routes]]
  server_name = "shared.example"
  protocols = ["jul-demo/1"]
  proxy_pass = "127.0.0.1:9443"

  [[stream.alpn_routes]]
  server_name = "shared.example"
  protocols = ["h2", "http/1.1"]
  proxy_pass = "127.0.0.1:9444"
```

`protocols` matches complete, case-sensitive **client offers**. It does not
identify the negotiated protocol or gRPC. A client offering both h2 and
http/1.1 can reach a backend that negotiates http/1.1. Unknown opaque identifiers
remain unmatched; Jul never joins, lowercases or truncates them.

## Configuration and precedence

| Field | Default / limits | Meaning |
| --- | --- | --- |
| `alpn_routes` | empty; at most 256 entries per listener | Ordered rules; supported only on TCP |
| `server_name` | required; 1–255 ASCII bytes | Exact SNI name, compared case-insensitively; `*` also matches absent SNI. Other wildcard forms are rejected |
| `protocols` | required; 1–64 distinct entries | Any exact offered identifier may match. Each configured UTF-8 TOML string is encoded verbatim as 1–255 bytes, including punctuation/control escapes. A wire identifier not representable by UTF-8 cannot match a configured rule |
| `proxy_pass` | required | Known upstream or literal host:port; reuses the normal stream pool, balancing, health and admission path |

Each connection chooses one backend in this order:

1. Matching exact-SNI plus ALPN rules, in declaration order.
2. Matching wildcard-SNI (`*`) plus ALPN rules, in declaration order.
3. The existing exact `sni_routes` entry.
4. The existing `sni_routes["*"]` entry.
5. The stream's default `proxy_pass`; if absent, close the connection.

The first configured matching rule in a tier wins when several offers match;
client offer order cannot change priority. Rule order is part of configuration
and lifecycle diffs. Repeated wire identifiers are accepted, count towards the
cap, and do not change selection. Duplicate configured identifiers within a
rule are rejected.

## Input and resource boundaries

| Input | Routing result |
| --- | --- |
| Valid SNI without ALPN | Existing SNI-only/catch-all/default fallback |
| Valid ALPN without SNI | Wildcard ALPN rules, then catch-all/default |
| Unknown identifiers | Existing SNI-only/catch-all/default fallback |
| Malformed nested lengths, empty identifier, duplicate ALPN extension, count/byte/record cap, non-TLS or timeout | No partial SNI/ALPN result; catch-all/default, or close if neither exists |
| No ALPN rules configured | Previous SNI-only contract, including ignoring malformed or duplicate ALPN extension bodies |

Inspection remains bounded by **16,384 handshake bytes**, **16,384 payload bytes
per TLS record**, **64 records**, and **16,704 buffered preread bytes**. There
are at most **64 offered identifiers**, each **1–255 bytes**. Inspection uses
the existing `connect_timeout` read deadline; its zero/default remains 10s.
Incomplete clients cannot keep preread open indefinitely. No new parser worker,
background task or dependency is introduced.

Only the first ClientHello is inspected, once per connection. The preread bytes
and subsequent payload are forwarded unchanged. Metadata hidden by encrypted
ClientHello cannot be recovered; only visible outer offers/SNI are available.
This feature covers TCP TLS passthrough. It excludes QUIC Initial parsing,
HTTP/3 passthrough inspection, stream TLS termination and application payload
inspection. Existing HTTP/3 termination is independent.

## Reload and management

New rules take effect for new connections through the existing atomic listener
route swap. Established connections retain their original route/pool and
admission ownership. Invalid rules or unresolved targets fail startup/preflight;
a failed reload leaves the prior snapshot serving. See
[reload semantics](reload-semantics.md#l4-stream-listeners-are-not-affected).

`GET /api/v1/streams` exposes ordered `alpn_routes` with `server_name`,
`protocols` and `proxy_pass`. The Console lists ALPN rule counts and preserves
rule values/order through its guided stream editor; create/edit rules in the
Configuration editor. Internal `stream_set` requests that omit `alpn_routes`
preserve existing rules for older clients; an explicit empty array clears them.
Generated config/lifecycle/OpenAPI references describe the same fields.

## Security and migration limits

Offers are client-controlled routing hints. A peer can request any configured
backend route by offering its identifier; this does not grant protocol validity
or authorization. Backends must enforce both. The proxy does not possess the
backend private keys, terminate TLS or authenticate offered identifiers.
Metrics retain their existing bounded labels; raw offers are not logged or
used as labels. Operational diagnostics do not expose client protocol strings.

Native exact matching does **not** make arbitrary NGINX ALPN maps equivalent.
NGINX exposes a comma-joined string: a single identifier `x,h2` can satisfy a
source token-boundary h2 regex without offering h2. Such maps remain blocking
with `NGX_STREAM_ALPN_MAP_AMBIGUOUS`; there are **no automatically translated
ALPN map forms** in this tranche. Review priority, boundaries and fallback
explicitly before manually creating native rules. The pinned reference lane
requires the comma counterexample to differ, rather than concealing it. See
[the assessment contract](nginx-assessment.md#alpn-passthrough-routing).

[Tests and benchmarks](audit/434-alpn/README.md) cover fragmented input, exact
limits, malformed ALPN, legacy SNI compatibility, real backend TLS negotiation,
replay, reload, admission, fuzzing and establishment overhead. Short smoke/fuzz
runs are not a soak or GA certification.
