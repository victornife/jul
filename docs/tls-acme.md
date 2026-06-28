# TLS & automatic HTTPS (ACME)

> **Maturity:** GA — soak pending (see [ADR 0003](adr/0003-maturity-and-ga.md); the soak test is
> a post-GA gate per [ADR 0005](adr/0005-soak-post-ga-gate.md)). Mutual TLS
> (client certificates) is documented separately in [mtls.md](mtls.md).

Jul.IA terminates TLS for HTTP/1.1, HTTP/2, and (with the `http3` tag) HTTP/3 on
the same certificates. Certificates come from one of two sources per listener:
**static PEM files** or **ACME** (Let's Encrypt and compatible CAs) for automatic
issuance and renewal. Selection is by SNI, so one listener can serve many names.

## Quick start

### Static certificate

```toml
[[servers]]
listen       = "0.0.0.0:443"
server_names = ["example.com", "www.example.com"]

  [servers.tls]
  enabled     = true
  cert        = "/etc/jul/example.com.crt"
  key         = "/etc/jul/example.com.key"
  min_version = "1.2"            # "1.2" (default) or "1.3"
```

### Automatic HTTPS (ACME)

ACME requires a binary built with the `acme` tag. A plain HTTP listener on :80 is
needed for the HTTP-01 challenge (or use TLS-ALPN-01 on :443 only).

```toml
[[servers]]
listen       = "0.0.0.0:443"
server_names = ["example.com", "www.example.com"]

  [servers.tls]
  enabled = true

    [servers.tls.acme]
    enabled   = true
    email     = "ops@example.com"          # required (ACME account contact)
    ca        = "letsencrypt-staging"       # default; use "letsencrypt" in prod
    challenge = "http-01"                   # or "tls-alpn-01"
    cache_dir = "./jul-data/certs"          # issued certs + account key
```

> The CA defaults to **staging** so an accidental deployment never burns
> production rate limits. Set `ca = "letsencrypt"` for trusted certificates.

## Configuration reference

### `[servers.tls]`

| Key | Type | Default | Notes |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | Enables TLS on the listener. Promoted to `true` automatically when ACME is enabled. |
| `cert` | string | — | Path to the PEM certificate (chain). Mutually exclusive with `acme`. |
| `key` | string | — | Path to the PEM private key. |
| `min_version` | string | `"1.2"` | Minimum TLS version: `"1.2"` or `"1.3"`. |
| `acme` | table | — | ACME block (below). Mutually exclusive with `cert`/`key`. |
| `client_auth` | table | — | Mutual TLS — see [mtls.md](mtls.md). |

### `[servers.tls.acme]`

| Key | Type | Default | Notes |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | Turns on ACME issuance for this listener. |
| `email` | string | — | **Required.** ACME account contact. |
| `ca` | string | `"letsencrypt-staging"` | `"letsencrypt"`, `"letsencrypt-staging"`, or a full `https://` directory URL. |
| `domains` | []string | server_names | Names to request. Falls back to the block's `server_names`. |
| `challenge` | string | `"http-01"` | `"http-01"` or `"tls-alpn-01"`. `"dns-01"` is **reserved for a future release** and rejected today. |
| `dns_provider` | string | — | Reserved for a future DNS-01 release; setting it is rejected. |
| `cache_dir` | string | `"./jul-data/certs"` | Directory cache for issued certs and the account key. |
| `ocsp_stapling` | bool | `true` | Staple OCSP responses for ACME-issued certificates. |

## Behaviour matrix

| Capability | Status | Notes |
| --- | --- | --- |
| TLS 1.2 / 1.3 termination | ✅ | `min_version` selects the floor; 1.3 always offered |
| HTTP/1.1 + HTTP/2 over TLS | ✅ | ALPN advertises `h2`, `http/1.1` |
| HTTP/3 (QUIC) on same certs | ✅ (`http3`) | shares the certificate provider |
| SNI multi-certificate | ✅ | one listener serves many `server_names` |
| Wildcard SNI (`*.example.com`) | ✅ | single label; exact match wins over wildcard |
| SNI fallback | ✅ | first-loaded certificate when no name matches |
| Static PEM certificates | ✅ | `cert` / `key` |
| Static cert **hot reload** | ✅ | atomic provider swap on config reload, no rebind |
| ACME HTTP-01 | ✅ (`acme`) | default; needs a plain HTTP listener |
| ACME TLS-ALPN-01 | ✅ (`acme`) | answered on the TLS listener (`acme-tls/1`) |
| ACME DNS-01 | ❌ | not implemented; reserved for a future release (rejected in validation) |
| ACME staging / production / custom CA | ✅ (`acme`) | via `ca` |
| ACME domain set **hot reload** | ❌ | fixed at startup; restart to change domains. The console refuses such an apply with a *restart required* notice rather than recording a no-op (see below) |
| OCSP stapling (ACME certs) | ✅ (`acme`) | default on; degrades gracefully on fetch failure |
| OCSP stapling (static certs) | ❌ | ACME-issued certificates only |
| Custom cipher suites / ordering | ❌ | Go stdlib defaults (safe for TLS 1.2/1.3) |
| Session tickets / 0-RTT control | ❌ | stdlib default tickets; 0-RTT not exposed server-side |

### SNI certificate selection

On each handshake the provider matches the client's SNI name in order:

1. **Exact** `server_name` (e.g. `api.example.com`).
2. **Wildcard** one label up (`*.example.com` matches `api.example.com`).
3. **Fallback** to the first-loaded certificate.

Names are lower-cased; an empty SNI goes straight to the fallback.

### ACME challenges

| Challenge | Port needed | How it is answered |
| --- | --- | --- |
| `http-01` (default) | plain HTTP (:80) | `/.well-known/acme-challenge/*` is served on the HTTP listener; all other requests fall through to the normal handler. |
| `tls-alpn-01` | the TLS listener (:443) | the listener advertises `acme-tls/1`; the challenge certificate is served only for handshakes that negotiate it, so normal clients are unaffected. |

`dns-01` is rejected at config-validation time with a clear error.

### Certificate reload semantics

| Source | On config reload |
| --- | --- |
| Static `cert`/`key` | **Reloaded** — the provider is rebuilt and swapped atomically; the listener keeps serving with no downtime and no rebind. |
| ACME domain set | **Not reloaded** — the issued-domain set is fixed when the manager starts. ACME *renewal* still happens in the background while running. Restart to add/remove domains. |
| `client_auth` (mTLS) | **Not reloaded** — bound at listener start (see [mtls.md](mtls.md)). |

When a configuration is applied (through the console or any validated write
path), every file-based `cert`/`key` pair it references is parsed **before** the
file is persisted. A broken or mismatched pair fails the apply up front with a
clear error, rather than surfacing only at the asynchronous reload where the
previous certificates would keep serving. ACME-served addresses are skipped by
this check because their certificates are obtained at handshake time.

#### Restart-required ACME changes

Because the autocert manager's issued-domain set and issuer (email/CA) are fixed
when it is built at startup, a hot apply cannot enable ACME, add or remove
domains, or change the issuer. The console (and every validated write path)
detects such a change by comparing the candidate against the running
configuration and **refuses it without writing**, returning a *restart required*
result (HTTP 409 with `restart_required: true`) instead of silently recording a
change that the live runtime would ignore. Update the configuration file
directly and restart the server to apply it.

Removing ACME entirely is *not* restart-required: the per-address provider
selection swaps to the static `cert`/`key` certificates on the next reload (which
the apply-time validation above confirms will load).

## Metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `jul_tls_cert_expiry_seconds` | gauge | `domain` | Leaf certificate expiry (Unix seconds), updated on each handshake. Alert when it approaches now. |
| `jul_acme_renewals_total` | counter | — | ACME renewals observed (a domain's expiry advanced); cache hits are filtered out. |
| `jul_mtls_handshakes_total` | counter | `result` | Mutual-TLS handshakes — see [mtls.md](mtls.md). |

The Console **Status** panel reports **TLS** and **Automatic HTTPS (ACME)** as
active capabilities with a per-block count.

## Benchmarks

From the in-tree benchmarks in `internal/server` (loopback, TLS 1.3, P-256 ECDSA;
indicative of cost, not a throughput claim):

| Benchmark | Result | Notes |
| --- | --- | --- |
| `BenchmarkTLSHandshakeServerAuth` | ~2 ms/op, ~122 KB/op, 866 allocs/op | full server-auth TLS 1.3 handshake (per-connection, loopback-noisy on latency; allocations are stable) |
| `BenchmarkSNICertSelection/exact` | ~114 ns/op, **0 allocs/op** | exact-name lookup on the `GetCertificate` hot path |
| `BenchmarkSNICertSelection/wildcard` | ~411 ns/op, **0 allocs/op** | wildcard match |
| `BenchmarkSNICertSelection/fallback` | ~323 ns/op, **0 allocs/op** | no-match fallback |

SNI selection is allocation-free and runs once per handshake (not per request).
Reproduce with:

```sh
go test -run '^$' -bench 'SNICertSelection|TLSHandshakeServerAuth' -benchmem ./internal/server/
```

## Security / threat notes

- **Terminate at the edge.** TLS is terminated by Jul.IA; traffic to upstreams
  follows the proxy configuration. Front untrusted networks with TLS and keep
  backends on a trusted network (or use mTLS to them where supported).
- **Use TLS 1.3 where you can.** Set `min_version = "1.3"` for internet-facing
  listeners that do not need legacy 1.2 clients. The default floor is 1.2.
- **ACME defaults to staging on purpose** to avoid leaking production rate limits
  from test deployments; this means an unconfigured `ca` issues **untrusted**
  certificates. Set `ca = "letsencrypt"` deliberately for production.
- **HTTP-01 exposes :80.** The plain HTTP listener answering the challenge should
  forward or redirect everything else to HTTPS. Prefer `tls-alpn-01` if you do
  not want a plain HTTP port.
- **Protect the cache directory.** `cache_dir` holds the ACME account key and
  issued private keys; restrict it to the service user.
- **OCSP stapling fails open.** If an OCSP responder is unreachable the
  certificate is served **unstapled** rather than failing the handshake; clients
  fall back to their own revocation behaviour.
- **One ACME issuer per process.** A single autocert manager is built at startup
  and shared by every ACME-enabled server block, so the issuer settings come from
  the first block. Validation rejects an apply where blocks disagree on `email`,
  `ca`, `challenge`, `cache_dir`, or `ocsp_stapling`; keep them identical across
  ACME blocks.

## Limits

- **No DNS-01 challenge.** Wildcard issuance via DNS is not implemented; the
  `dns-01` challenge and `dns_provider` are reserved for a future release and
  rejected by validation. Use per-name HTTP-01/TLS-ALPN-01.
- **ACME domain set is fixed at startup.** Adding or removing ACME domains needs
  a restart; static-file certificates and ACME *renewals* are unaffected.
- **OCSP stapling is ACME-only.** Static file certificates are served without a
  stapled OCSP response.
- **No cipher-suite, session-ticket, or 0-RTT configuration.** Jul.IA uses the Go
  standard library's TLS defaults; these are not tunable via config.
- **No pre-flight cert/key validation.** A bad static `cert`/`key` pair surfaces
  as a bind-time error, not during `jul -check`.

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), TLS + automatic HTTPS is **GA — soak pending**. The
soak test (criterion 5) is a post-GA gate per
[ADR 0005](adr/0005-soak-post-ga-gate.md); the other eight criteria are met.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [behaviour matrix](#behaviour-matrix) (cert source × challenge × feature) |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) (`BenchmarkTLSHandshakeServerAuth`, `BenchmarkSNICertSelection`) |
| 3 | Documented known-limitations | ✅ [Limits](#limits) |
| 4 | Stable config/API contract (semver-guarded) | ✅ [compatibility policy](compatibility.md) (v1 tag at release) |
| 5 | Long-running soak test passed | ☐ post-GA gate ([ADR 0005](adr/0005-soak-post-ga-gate.md)) — tracked in [ga-push.md](ga-push.md) |
| 6 | Runnable example + docs | ✅ [testdata/tls.toml](../testdata/tls.toml) + this doc |
| 7 | Security / threat note | ✅ [Security / threat notes](#security--threat-notes) |
| 8 | Fuzzing where parsing is involved | n/a — certificate/CA parsing is stdlib `crypto/x509`; config parsing is covered by the shared TOML/`validate` tests (no custom parser) |
| 9 | Self-explanatory Console surface | ✅ Console **Status** panel reports *TLS* and *Automatic HTTPS (ACME)* |

The one open item is the post-GA **soak test** (criterion 5).

## Build tags

- TLS termination (static certs, SNI, `min_version`, reload) is **core** — no tag.
- **ACME** issuance requires the `acme` build tag; without it, enabling ACME in
  config fails fast with a clear error.
- **HTTP/3** over the same certificates requires the `http3` tag.

## See also

- [mtls.md](mtls.md) — mutual TLS / client-certificate authentication
- [compatibility.md](compatibility.md) — config/API stability policy
- [testdata/tls.toml](../testdata/tls.toml) — `jul -check` sample
