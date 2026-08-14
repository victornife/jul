# L4 stream proxy

Jul.IA can forward raw TCP/UDP connections and datagrams to backends without
parsing the application protocol. It supports TLS SNI-based routing (without
terminating TLS), the HAProxy PROXY protocol, and upstream pool load balancing.

The feature is compiled only into builds with the `"stream"` build tag. A lean
build (without the tag) rejects any configuration that declares `[[stream]]`
blocks, so misconfiguration fails loudly.

```bash
go build -tags stream -o jul ./cmd/jul
```

## Configuration

Declare stream listeners with `[[stream]]` blocks:

```toml
[[stream]]
listen = "0.0.0.0:5432"
proxy_pass = "postgres_pool"
connect_timeout = "5s"
idle_timeout = "1h"

[[stream]]
listen = "0.0.0.0:8443"
  [stream.sni_routes]
  "api.example.com" = "api_pool"
  "db.example.com"  = "127.0.0.1:9443"
  "*"               = "default_pool"

[[stream]]
listen = "127.0.0.1:5353"
protocol = "udp"
proxy_pass = "dns_pool"
idle_timeout = "30s"
max_udp_sessions = 1024
```

| Field | Default | Meaning |
|-------|---------|---------|
| `listen` | required | Bind address (`host:port`) |
| `protocol` | `tcp` | `tcp` or `udp` |
| `proxy_pass` | required | Backend: upstream name or literal `host:port` |
| `connect_timeout` | `5s` | TCP backend dial deadline |
| `idle_timeout` | `1h` | TCP connection idle timeout; UDP session idle reap |
| `proxy_protocol` | `""` | `"in"`, `"out"`, `"both"` — HAProxy PROXY protocol |
| `trusted_proxies` | `[]` | Peers allowed to assert a client address with an inbound PROXY header. Required when `proxy_protocol` ingests one; a connection from outside the set is refused |
| `sni_routes` | `{}` | TLS SNI → backend map (`"*"` = catch-all) |
| `max_udp_sessions` | `1024` | Per-listener UDP session cap |

## Conformance matrix

| Scenario | Protocol | Input | Expected | Covered by |
|----------|----------|-------|----------|------------|
| TCP echo relay | TCP | Client writes "ping" | Backend echoes "ping", byte counters ≥ 4 each | TestTCPProxyEcho |
| TCP via upstream pool | TCP | `proxy_pass = "pool"` | Balancer selects backend, echo works | TestTCPProxyToUpstream |
| SNI route match | TCP | `ClientHello` with SNI host | Routed to matching backend | TestTCPSNIRouting |
| SNI catch-all fallback | TCP | Unknown SNI with `"*"` route | Falls back to wildcard backend | TestTCPSNICatchAll |
| Inbound + outbound PROXY | TCP | `proxy_protocol = "both"` | Source address recovered and forwarded | TestProxyProtocolInAndOut |
| UDP echo relay | UDP | Client datagram "ping" | Backend echoes back | TestUDPProxyRelay |
| Reload swaps target | TCP | Reload with new backend | New connections use new target | TestReloadSwapsTargetAndStops |
| Reload bind failure rollback | TCP | Reload to busy address | Old listeners stay active | TestReloadBindFailureRollsBack |
| PROXY protocol v2 round-trip | TCP | v2 TCP4/TCP6 header | Address recovered correctly | TestProxyProtocolV2RoundTrip, TestProxyProtocolV2IPv6RoundTrip |
| PROXY protocol v1 parse | TCP | v1 text header | Address recovered correctly | TestProxyProtocolV1Parse |
| SNI peek non-TLS | TCP | Non-TLS bytes | Empty SNI, bytes not consumed | TestSNIPeekNonTLS |
| SNI peek does not consume | TCP | TLS ClientHello peek | Bytes remain for relay | TestSNIPeekDoesNotConsume |
| UDP session singleflight | UDP | Concurrent first datagrams from same client | Exactly one backend dial | TestUDPSessionForSingleflight |
| UDP rejects at cap | UDP | New client when session table full | Connection rejected, existing sessions stable | TestUDPRejectsNewClientAtCap |
| UDP admit LRU eviction | UDP | Cap hit with idle sessions | Oldest idle session reclaimed | TestAdmitUDPLocked |
| UDP idle eviction metric | UDP | Idle session timeout | `OnUDPSessionEvicted("idle")` fired | TestUDPIdleEvictionMetric |
| Preflight does not bind | TCP | `PreflightBuild` call | No socket held, address free | TestPreflightBuildDoesNotBind |
| Preflight rejects duplicate | TCP | Two `[[stream]]` with same listen | Error returned | TestPreflightBuildRejectsDuplicate |
| Preflight detects busy port | TCP | New listener address in use | Error returned | TestPreflightListenersDetectsBusyPort |
| Preflight skips existing | TCP | Existing listener unchanged | No bind collision | TestPreflightListenersSkipsExisting |
| Preflight releases probe | TCP | Free address probed | Address free after call | TestPreflightListenersReleasesSocket |
| Preflight UDP coverage | UDP | UDP address in use | Error returned | TestPreflightListenersUDP |
| Close stops all listeners | TCP+UDP | `Server.Close()` | All sockets released | TestCloseStopsAllListeners |
| Check empty config | — | No `[[stream]]` blocks | Returns nil | TestCheckEmpty |

## Benchmarks

Run: `go test -tags stream -bench='BenchmarkTCP|BenchmarkUDP' -run='^$' -benchmem ./internal/stream/`

| Benchmark | Payload | Concurrency | Ops | Time/op | B/op | Allocs/op |
|-----------|---------|-------------|-----|---------|------|-----------|
| TCPPassthrough | 64 B | 1 | 439 | 3,219 μs | 121,768 | 83 |
| TCPParallel | 64 B | GOMAXPROCS | 463 | 3,358 μs | 121,953 | 86 |
| UDPRelay | 64 B | GOMAXPROCS | 35,738 | 33 μs | 164 | 7 |

**Interpretation.** Per-connection TCP passthrough adds ~3.2 ms of wall-clock
latency on this test setup (connection setup + relay + teardown overhead
combined). UDP session creation and relay is much lighter at ~33 μs per
datagram once the session is established, because UDP is connectionless and the
backend socket is reused. The TCP parallel benchmark shows near-linear scaling
with worker count, suggesting the listener accept loop is not the bottleneck.

UDP session-cap admission:

Run: `go test -tags stream -bench='BenchmarkUDPAdmitAtCap' -run='^$' ./internal/stream/`

| Table size | Time/op |
|------------|---------|
| 256 sessions | 7.4 μs |
| 4,096 sessions | 107.9 μs |
| 10,000 sessions | 254.5 μs |

The O(n) LRU sweep at the cap is acceptable because it only runs when
admitting a new client to a full table; the normal relay path is O(1) map
lookup.

## Known limitations

1. **No application-layer inspection.** TCP relay is byte-for-byte; Jul.IA
cannot read HTTP headers, terminate TLS, or parse wire protocols inside the
stream. Use the HTTP server (`[[servers]]`) for L7 routing.
2. **SNI routing reads only the first TLS record.** A ClientHello that spans
multiple records or omits SNI falls back to the default/catch-all route.
3. **UDP sessions are memory-backed.** Each UDP client gets a session entry
(keyed by source address); spoofed source addresses can fill the session table.
`max_udp_sessions` bounds the table, but there is no application-layer
authentication.
4. **PROXY protocol v1 only.** Only HAProxy PROXY protocol v1 (text) is
supported for inbound; v2 (binary) is supported for outbound. LOCAL/UNKNOWN
inbound headers fall back to the on-the-wire address.
5. **No HTTP-specific features in stream.** Rate limiting, WAF, compression,
auth, and caching apply only to `[[servers]]` HTTP traffic, not `[[stream]]`
L4 traffic.

## Threat model

| Threat | Vector | Mitigation | Residual risk |
|--------|--------|------------|---------------|
| Source-address spoofing (UDP) | Attacker sends UDP with forged src, filling session table | `max_udp_sessions` caps table; LRU reclaims idle sessions; short `idle_timeout` accelerates cleanup | Spoofed address from a live client blocks that legitimate client's session until idle timeout |
| PROXY protocol injection | Attacker sends crafted PROXY header to recover internal addresses | `readProxyHeader` validates field counts, address format, and header length; rejects malformed input | A perfect mimic of a valid v1/v2 header from an untrusted client would be accepted (use network ACLs) |
| SNI routing leak | Attacker probes SNI routes to map internal backends | SNI routes are config-only; no introspection API exposes them; backends should not be directly reachable | Insider with config read access |
| Listener bind hijack | Attacker binds the stream listen port before Jul.IA starts | Preflight bind probe rejects in-use addresses; OS-level port binding is first-come-first-served | Race on startup between Jul.IA and a malicious process with same privileges |
| Backend pool exhaustion | Many concurrent TCP connections exhaust backend capacity | `connect_timeout` prevents indefinite hangs; upstream `max_fails` / `fail_timeout` eject unhealthy backends; idle timeout reclaims stale TCP connections | Flash crowd larger than pool capacity |
| UDP amplification | Attacker sends small UDP request triggering large backend response | Jul.IA relays backend replies only to the original source address (no open reflection); session-bound replies limit amplification | Attacker spoofs a victim's source address — victim receives backend replies (standard UDP reflection risk) |

## Fuzz coverage

| Target | File | What it fuzzes | Oracle |
|--------|------|----------------|--------|
| `FuzzReadProxyHeader` | `internal/stream/fuzz_test.go` | Random PROXY protocol v1/v2 headers | No panic; well-formed local/unknown yields nil addr + nil err; malformed yields error |
| `FuzzPeekSNI` | `internal/stream/fuzz_test.go` | Random byte streams as TLS ClientHello | No panic; result is valid string or empty |

Run: `go test -tags stream -fuzz='FuzzReadProxyHeader|FuzzPeekSNI' -fuzztime=15s ./internal/stream/`

## GA status

| Criterion | Status | Evidence |
|-----------|--------|----------|
| ① Behaviour conformance matrix | **Met** | 23-row matrix above + `internal/stream/stream_test.go`, `internal/stream/udp_session_test.go`, `internal/stream/reuseaddr_test.go` |
| ② Benchmarks | **Met** | 4 benchmarks in `internal/stream/bench_test.go` + `internal/stream/udp_bench_test.go` |
| ③ Known limitations | **Met** | 5-item list above |
| ④ Compatibility policy | **Met** | Stream config additive within TOML schema; documented in [configuration.md](configuration.md) |
| ⑤ Soak test | **Met** | 8h Linux isolated soak 2026-07-11 (`TestSoakUDPChurn`, 54,892,354 sends, 0% err, bounded goroutines/heap) plus 1h isolated Windows evidence 2026-07-06 — [evidence](soak-evidence.md#2026-07-11--l4-stream-proxy-8h-isolated-soak-linux-completed) |
| ⑥ Feature documentation | **Met** | This document + [configuration.md](configuration.md) + [testdata/stream.toml](../testdata/stream.toml) |
| ⑦ Threat model | **Met** | 6-row threat table above |
| ⑧ Parser/input fuzzing | **Met** | `FuzzReadProxyHeader`, `FuzzPeekSNI` in `internal/stream/fuzz_test.go` |
| ⑨ Console surface | **Met** | Stream status surfaced in admin `/api/stats` and Console v2 dashboard |
