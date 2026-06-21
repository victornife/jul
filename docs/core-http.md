# Core HTTP

> **Maturity:** GA — soak pending (see [ADR 0003](adr/0003-maturity-and-ga.md); the soak test is
> a post-GA gate per [ADR 0005](adr/0005-soak-post-ga-gate.md)). TLS termination
> is documented in [tls-acme.md](tls-acme.md); client certificates in
> [mtls.md](mtls.md).

Core HTTP is the foundation every other Jul.IA feature builds on: it accepts a
connection, picks a virtual host by `Host` header, matches a location by path,
and dispatches to a content handler — static files, a reverse proxy, or a
FastCGI/uWSGI application — following a simplified, predictable subset of NGINX
`server`/`location` semantics.

## Quick start

```toml
[[servers]]
listen       = "0.0.0.0:8080"
server_names = ["example.com", "*.example.com"]

  # Serve static assets at the site root.
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root  = "/var/www/example"
  index = ["index.html"]

  # Reverse-proxy the API to an upstream pool.
  [[servers.locations]]
  match      = { type = "prefix", path = "/api/" }
  proxy_pass = "http://app"

  # Hand PHP off to a FastCGI worker.
  [[servers.locations]]
  match        = { type = "regex", path = "\\.php$" }
  root         = "/var/www/example"
  fastcgi_pass = "unix:/run/php/php-fpm.sock"

[[upstreams]]
name     = "app"
strategy = "round_robin"
servers  = [
  { address = "10.0.0.1:3000" },
  { address = "10.0.0.2:3000" },
]
```

## Request lifecycle

1. **Listener** accepts the connection on the configured `listen` address.
2. **Virtual host** selection: the `Host` header (lowercased, port-stripped) is
   scored against each server block's `server_names`; the best score wins, with
   the first-declared block as the default.
3. **Location** selection: the request path is matched against the server's
   locations by precedence (exact → longest prefix → regex → `/` fallback).
4. **Rewrites** (if any) run against the matched location's path.
5. **Middleware** wraps the handler (request ID, recover, body limit, optional
   timeout / rate limit / access log).
6. **Handler** serves the response (static, proxy, FastCGI, uWSGI, or a config
   action such as `return`/`redirect`/`deny`).

## Virtual host matching

`Host` is normalized (lowercased, whitespace-trimmed, port and IPv6 brackets
handled) and scored against each block's `server_names`:

| Server name form | Matches | Score |
| --- | --- | --- |
| Exact (`www.example.com`) | only that host | 3 (highest) |
| Leading wildcard (`*.example.com`) | `a.example.com`, **not** `example.com` | 2 |
| No match | — | 0 |

The highest score wins. When nothing scores above 0, the request falls to the
**default server** — the first server block declared for that listen address.

## Location matching

A request path is resolved against the server's locations in this fixed order:

| Step | Match type | Rule |
| --- | --- | --- |
| 1 | `exact` | `match.path` equals the request path exactly |
| 2 | `prefix` | longest `match.path` that is a prefix of the request path (the `/` catch-all is excluded here) |
| 3 | `regex` | first regex (in config order) that matches |
| 4 | fallback | the `prefix` location whose path is `/`, if present |

If no location matches and there is no `/` fallback, the request is unhandled
(the router's default action returns **501 Not Implemented**).

### Rewrites

Each location may carry `rewrites` (regex → replacement). Flags follow a
simplified NGINX model:

| Flag | Behaviour |
| --- | --- |
| *(none)* | rewrite the path and continue evaluating further rewrites |
| `last` / `break` | rewrite and stop; the already-matched location handles it (no fresh location search) |
| `redirect` | external 302 to the target |
| `permanent` | external 301 to the target |

## Static file serving

| Capability | Behaviour |
| --- | --- |
| Root confinement | `root` opened via `os.Root` (Go chroot-style); `..` cannot escape |
| Index files | `index` (default `["index.html"]`) tried for directory requests |
| `try_files` | ordered fallbacks before the final URI |
| Directory listing | off by default (`directory_listing`) |
| Hidden files | dotfiles blocked unless `allow_hidden` |
| Range requests | served via `http.ServeContent` (partial content / `If-Range`) |
| Caching validators | `ETag` = `"<mtime-unixnano>-<size>"`; conditional GETs honoured |
| MIME types | `mime.TypeByExtension` |
| Precompressed assets | `.br` then `.gz` sidecars served when the client accepts them |
| Error pages | per-status file (served) or URL (302 redirect) |

## Reverse proxy

Built on the standard library's `httputil.ReverseProxy` over a balancing
transport that picks a backend from the named (or anonymous) upstream pool.

| Aspect | Behaviour |
| --- | --- |
| Target | `proxy_pass` references a named upstream or a single hardcoded backend (static config only) |
| Forwarded headers | `X-Forwarded-For` regenerated; `$proxy_add_x_forwarded_for`, `$remote_addr`, `$host`, `$scheme`, `$ssl_client_*` expandable in custom `headers` |
| Failover | one retry per backend, **idempotent methods only** (GET/HEAD/OPTIONS/TRACE/PUT/DELETE) and only when the body is re-readable |
| Timeouts | `proxy_connect_timeout` (default 10s), `proxy_read_timeout`, 90s idle |
| Connection reuse | `MaxIdleConns` 100, `MaxIdleConnsPerHost` 32, HTTP/2 attempted |
| Error mapping | 503 no backend, 504 timeout, 502 connection error |

## FastCGI / uWSGI

| Aspect | FastCGI | uWSGI |
| --- | --- | --- |
| Transport | `gofast` with a small client pool | custom uWSGI packet protocol (modifier1 = 0) |
| Address | `fastcgi_pass` (`unix:…` or `tcp://…`) | `uwsgi_pass` (`unix:…` or `tcp://…`) |
| Connection reuse | pooled | reconnect per request |
| Script name | `scriptNameFor` appends the index file (default `index.php`) for directory requests | n/a |
| Param overrides | `fastcgi_params` (last write wins) | — |

> **SCGI is not implemented.**

## Load balancing

Configured per upstream via `strategy`:

| Strategy | Behaviour |
| --- | --- |
| `round_robin` *(default)* | lock-free rotation, ignores weight |
| `weighted_round_robin` | smooth weighted round-robin (NGINX algorithm), proportional to `weight` |
| `least_conn` | fewest in-flight requests |

Passive health checking is built in: after `max_fails` (default 1) consecutive
failures a backend is parked for `fail_timeout` (default 10s). There is **no**
`ip_hash` / `random` strategy and no circuit breaker beyond passive health.

## Core middleware

| Middleware | Trigger | Behaviour |
| --- | --- | --- |
| Request ID | always | `X-Request-ID` (incoming preserved or generated) |
| Recover | always | converts a panic to 500; re-panics `http.ErrAbortHandler` |
| Body limit | always | `client_max_body_size` via `MaxBytesReader` → 413 |
| Timeout | when configured | `http.TimeoutHandler` → 503 |
| Access log | when a sink is set | structured `slog` access records |
| Rate limit | when enabled | 32-shard token bucket |
| Compression | `compression` build tag | response compression |

## Metrics

| Metric | Labels |
| --- | --- |
| `jul_http_requests_total` | `method`, `host`, `code` |
| `jul_http_request_duration_seconds` | `method`, `host` |
| `jul_http_requests_in_flight` | — |
| `jul_upstream_healthy` | `pool`, `backend` |
| `jul_upstream_backends` | `pool` |

## Benchmarks

Representative CPU costs of the hot path (Windows, amd64, Virtual CPU @ 3.41GHz;
numbers are indicative — run `go test -bench` on your own hardware).

| Benchmark | Time/op | Allocs/op |
| --- | --- | --- |
| `BenchmarkHostScore/exact` | ~115 ns | 0 |
| `BenchmarkHostScore/wildcard` | ~103 ns | 0 |
| `BenchmarkMatchLocation/exact` | ~6 ns | 0 |
| `BenchmarkMatchLocation/prefix` | ~39 ns | 0 |
| `BenchmarkMatchLocation/regex` | ~187 ns | 0 |
| `BenchmarkMatchLocation/fallback` | ~96 ns | 0 |
| `BenchmarkBalancerRoundRobin` (8 backends) | ~15 ns | 0 |
| `BenchmarkBalancerWeightedRR` (8 backends) | ~36 ns | 0 |
| `BenchmarkBalancerLeastConn` (8 backends) | ~13 ns | 0 |
| `BenchmarkPoolPick` (full hot path, 4 backends) | ~91 ns | 1 (32 B) |
| `BenchmarkStaticServe` (small file, end to end) | ~450 µs | 53 (7.6 KB) |

Routing and balancer selection are allocation-free. The static-serve number is
dominated by per-request filesystem `open`/`stat` syscalls (filesystem-bound,
not CPU-bound) and varies widely by OS and disk.

Reproduce:

```
go test -run '^$' -bench . -benchmem ./internal/router/ ./internal/upstream/ ./internal/handler/
```

## Security / threat notes

| Threat | Status | Mechanism |
| --- | --- | --- |
| Path traversal | 🟢 safe | static `root` opened with `os.Root`; `..` cannot escape (`TestStaticTraversalBlocked`) |
| FastCGI `SCRIPT_NAME` traversal | 🟢 safe | `scriptNameFor` cleans to an absolute path with no `..` segment (`FuzzScriptName`) |
| SSRF | 🟢 safe by design | `proxy_pass` is static config; no request input selects the upstream target |
| Header injection / CRLF | 🟢 safe | Go `net/http` rejects embedded CR/LF; custom headers use simple variable substitution (no eval) |
| Request smuggling | 🟢 safe | strict `net/http` request parsing |
| DoS (large bodies) | mitigated | `client_max_body_size` → 413 (default unlimited; set per server/location) |
| DoS (slow clients) | partial | `read_timeout` / `write_timeout` mitigate; default unset |

## Limits

- **No SCGI** (FastCGI and uWSGI only).
- **No `ip_hash` / `random`** load-balancing strategies.
- **No `try_files` at the FastCGI level** (`try_files` is static-only).
- **No circuit breaker** beyond passive `max_fails` / `fail_timeout` health.
- **No per-backend rate limiting** (rate limiting is per-listener).
- `read_timeout` / `write_timeout` are **unset by default** (no slow-client
  protection until configured).
- An unmatched request with no `/` fallback returns **501**, by design.

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), Core HTTP is **GA — soak pending**. The soak test
(criterion 5) is a post-GA gate per [ADR 0005](adr/0005-soak-post-ga-gate.md);
the other eight criteria are met.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [host](#virtual-host-matching), [location](#location-matching), [static](#static-file-serving), [proxy](#reverse-proxy), [FastCGI/uWSGI](#fastcgi--uwsgi), [balancing](#load-balancing) tables |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) (routing, balancing, static serve) |
| 3 | Documented known-limitations | ✅ [Limits](#limits) |
| 4 | Stable config/API contract (semver-guarded) | ✅ [compatibility policy](compatibility.md) (v1 tag at release) |
| 5 | Long-running soak test passed | ☐ post-GA gate ([ADR 0005](adr/0005-soak-post-ga-gate.md)) — tracked in [ga-push.md](ga-push.md) |
| 6 | Runnable example + docs | ✅ [testdata/static.toml](../testdata/static.toml), [testdata/vhosts.toml](../testdata/vhosts.toml) + this doc |
| 7 | Security / threat note | ✅ [Security / threat notes](#security--threat-notes) |
| 8 | Fuzzing where parsing is involved | ✅ `FuzzHostScore`, `FuzzMatchLocation` (router), `FuzzScriptName`, `FuzzParseSocketAddress` (FastCGI) |
| 9 | Self-explanatory Console surface | ✅ Console **Status** → *Traffic* group reports *Virtual hosts*, *Static file serving*, *Reverse proxy*, *FastCGI / uWSGI* |

The one open item is the post-GA **soak test** (criterion 5).

## Build tags

Core HTTP — virtual hosts, location matching, static serving, reverse proxy,
FastCGI/uWSGI, and load balancing — is **core**: it requires no build tags.
Optional middleware (e.g. compression) and protocols (HTTP/3, h2c, gRPC) are
documented separately and gated behind their own tags.

## See also

- [tls-acme.md](tls-acme.md) — TLS termination and automatic HTTPS
- [mtls.md](mtls.md) — mutual TLS / client-certificate authentication
- [compatibility.md](compatibility.md) — config/API stability policy
- [ga-push.md](ga-push.md) — GA hardening tracking log
