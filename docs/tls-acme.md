# TLS & automatic HTTPS (ACME)

> **Maturity:** GA (see [ADR 0003](adr/0003-maturity-and-ga.md)). Mutual TLS
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
needed for the HTTP-01 challenge, or use TLS-ALPN-01 on :443 only. Jul.IA
activates only the configured challenge surface.

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
| `cert` | string | — | Path to the PEM certificate chain. Mutually exclusive with `acme`. |
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
| `domains` | []string | `server_names` | Names to request. Falls back to the block's `server_names`. |
| `challenge` | string | `"http-01"` | `"http-01"` or `"tls-alpn-01"`; the modes are exclusive. `"dns-01"` is reserved and rejected. |
| `dns_provider` | string | — | Reserved for a future DNS-01 release; setting it is rejected. |
| `cache_dir` | string | `"./jul-data/certs"` | Directory cache for issued certificates and the account key. |
| `ocsp_stapling` | bool | `true` | Staple OCSP responses for ACME-issued certificates. |

All enabled ACME blocks share one process-lifetime manager and must agree on
`email`, `ca`, `challenge`, `cache_dir`, and `ocsp_stapling`. Validation rejects
a configuration whose blocks disagree.

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
| Static cert **hot reload** | ❌ | current static file changes remain restart-bound; see #100 |
| ACME HTTP-01 | ✅ (`acme`) | exclusive mode; needs a plain HTTP listener |
| ACME TLS-ALPN-01 | ✅ (`acme`) | exclusive mode; advertises `acme-tls/1` on the TLS listener |
| ACME DNS-01 | ❌ | not implemented; reserved for a future release and rejected in validation |
| ACME staging / production / custom CA | ✅ (`acme`) | via `ca` |
| ACME domain set **hot reload** | ❌ | fixed when the manager starts; restart to change domains |
| OCSP stapling (ACME certs) | ✅ (`acme`) | default on; degrades gracefully on fetch failure |
| OCSP stapling (static certs) | ❌ | ACME-issued certificates only |
| Custom cipher suites / ordering | ❌ | Go standard-library defaults |
| Session tickets / 0-RTT control | ❌ | standard-library defaults; not exposed server-side |

### SNI certificate selection

On each handshake the provider matches the client's SNI name in order:

1. **Exact** `server_name` value (for example `api.example.com`).
2. **Wildcard** one label up (`*.example.com` matches `api.example.com`).
3. **Fallback** to the first-loaded certificate.

Names are lower-cased; an empty SNI goes straight to the fallback.

### ACME challenges

| Challenge | Port needed | How it is answered |
| --- | --- | --- |
| `http-01` (default) | plain HTTP (:80) | `/.well-known/acme-challenge/*` is served by autocert; all other requests fall through to the normal handler. ACME does **not** add `acme-tls/1` to TLS ALPN. |
| `tls-alpn-01` | TLS listener (:443) | The listener advertises `acme-tls/1`; the challenge certificate is served only for handshakes that negotiate it. ACME does **not** install the plain HTTP challenge handler. |

The two modes are exclusive. An empty programmatic challenge value is treated
defensively as the documented HTTP-01 default; normal parsed configurations
receive that default before runtime construction. `dns-01` is rejected at
configuration-validation time with a clear error.

### Certificate reload semantics

| Source or policy | On configuration reload |
| --- | --- |
| Static `cert`/`key` | **Not reloaded** — restart to pick up a new path or changed file contents until #100 lands. |
| ACME renewal under the running manager | **Automatic** — renewal continues without configuration reload. |
| ACME enablement, domains, challenge, account, issuer, cache, or OCSP policy | **Not reloaded** — process-owned manager state requires planned restart. |
| `client_auth` (mTLS) | **Not reloaded** — bound at listener start; see [mtls.md](mtls.md). |

When configuration is applied through a validated write path, every referenced
static `cert`/`key` pair is parsed before persistence. A broken or mismatched pair
fails the apply up front rather than surfacing only during asynchronous reload.
ACME-served addresses are skipped because their certificates are obtained at
handshake time.

#### Restart-required ACME changes

The autocert manager's domain allow-list, account, issuer, challenge, cache, and
OCSP policy are process-lifetime state. A candidate changing one of these values
must be reported as restart-required and must not partially publish only its hot
routing subset while claiming the complete candidate live.

Removing ACME entirely currently follows the existing validated static-provider
transition contract: static `cert`/`key` material must be configured and pass
preflight. Any future change to this lifecycle must be made through the
closed-world lifecycle registry and documented atomically.

## Metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `jul_tls_cert_expiry_seconds` | gauge | `domain` | Leaf certificate expiry as Unix seconds, updated on handshake. Alert when it approaches now. |
| `jul_acme_renewals_total` | counter | — | ACME renewals observed; ordinary cache hits are filtered out. |
| `jul_mtls_handshakes_total` | counter | `result` | Mutual-TLS handshakes — see [mtls.md](mtls.md). |

The Console **Status** panel reports TLS and Automatic HTTPS (ACME) as active
capabilities with a per-block count. These metric names and labels are frozen by
the [Prometheus contract](metrics-contract.json); destination, subject, SAN,
responder URL, or raw error values must not become unbounded labels.

## Benchmarks

From the in-tree benchmarks in `internal/server` (loopback, TLS 1.3, P-256 ECDSA;
indicative of cost, not a throughput claim):

| Benchmark | Result | Notes |
| --- | --- | --- |
| `BenchmarkTLSHandshakeServerAuth` | ~2 ms/op, ~122 KB/op, 866 allocs/op | Full server-auth TLS 1.3 handshake |
| `BenchmarkSNICertSelection/exact` | ~114 ns/op, **0 allocs/op** | Exact-name lookup on the `GetCertificate` path |
| `BenchmarkSNICertSelection/wildcard` | ~411 ns/op, **0 allocs/op** | Wildcard match |
| `BenchmarkSNICertSelection/fallback` | ~323 ns/op, **0 allocs/op** | No-match fallback |

SNI selection is allocation-free and runs once per handshake, not per request.
Reproduce with:

```sh
go test -run '^$' -bench 'SNICertSelection|TLSHandshakeServerAuth' -benchmem ./internal/server/
```

## Security / threat notes

- **Terminate at the edge.** TLS is terminated by Jul.IA; traffic to upstreams
  follows the proxy configuration. Backend TLS/mTLS policy is a separate trust
  boundary tracked by the Core Gateway Completeness programme.
- **Use TLS 1.3 where possible.** Set `min_version = "1.3"` for exposed listeners
  that do not need TLS 1.2 clients.
- **ACME defaults to staging intentionally.** Set `ca = "letsencrypt"`
  deliberately for publicly trusted certificates.
- **Challenge surfaces are exclusive.** HTTP-01 exposes the plain challenge path
  but not ACME TLS ALPN; TLS-ALPN-01 exposes `acme-tls/1` but not the plain HTTP
  handler.
- **Protect the cache directory.** It holds the ACME account key and issued
  private keys; restrict access to the service identity.
- **OCSP stapling fails open.** If an OCSP responder is unavailable, the
  certificate is served unstapled and clients fall back to their own behavior.
- **One ACME manager per process.** Validation requires all enabled blocks to
  agree on account, issuer, challenge, cache, and OCSP settings.
- **A valid local configuration is not proof of issuance.** DNS, firewall, CA,
  egress-policy, clock, and external responder failures remain operational
  dependencies.

## Egress allow-list prerequisites

The optional [egress allow-list](egress.md) is disabled by default. When enabled,
the ACME directory/order/challenge client and OCSP responder client are guarded
like other auxiliary fetches, so issuance and stapling fail until every required
host is allowed.

Public ACME CAs may front endpoints with rotating infrastructure. Prefer exact
hosts or an intentional suffix rather than brittle IP ranges. For Let's Encrypt,
a representative configuration is:

```toml
[egress]
enabled = true
allow = [
  ".api.letsencrypt.org",
  ".acme-v02.api.letsencrypt.org",
  ".acme-staging-v02.api.letsencrypt.org",
  ".o.lencr.org",
]
```

Actual endpoints can evolve. Treat the configured directory URL and observed
bounded egress-denial reason as authoritative. See
[troubleshooting.md](troubleshooting.md#egress-blocks-an-outbound-fetch).

## Limits

- **No DNS-01 challenge.** Wildcard issuance through DNS automation is not
  implemented; `dns-01` and `dns_provider` are rejected.
- **Static certificate rotation is restart-bound.** #100 owns the planned
  dynamic provider publication work.
- **ACME manager transitions are restart-bound.** Domain, account, issuer,
  challenge, cache, and OCSP policy changes require planned restart.
- **OCSP stapling is ACME-only.** Static certificates are served without a
  stapled OCSP response.
- **No cipher-suite, session-ticket, or server 0-RTT configuration.** Jul.IA uses
  Go's standard-library defaults.
- **Preflight is local, not external issuance validation.** Jul.IA validates
  referenced static key pairs before persistence, but cannot prove public DNS,
  firewall, CA reachability, or successful future renewal.

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), TLS + automatic HTTPS is GA. The
soak criterion was completed on 2026-07-05 via Phase 2A. The challenge-selection
correction is a compatibility/security repair inside that existing feature.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [Behaviour matrix](#behaviour-matrix) |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) |
| 3 | Documented known limitations | ✅ [Limits](#limits) |
| 4 | Stable config/API contract | ✅ [compatibility policy](compatibility.md) |
| 5 | Long-running soak test passed | ✅ [evidence](soak-evidence.md#2026-07-05--phase-2a-consolidated-burn-in-completed-local-8-hours-50-workers-all-features) |
| 6 | Runnable example + docs | ✅ [testdata/tls.toml](../testdata/tls.toml) + this guide |
| 7 | Security / threat note | ✅ [Security / threat notes](#security--threat-notes) |
| 8 | Fuzzing where parsing is involved | n/a — certificate parsing uses `crypto/x509`; configuration uses shared TOML validation |
| 9 | Appropriate operability surface | ✅ Console status and validated configuration path |

The current correction adds focused untagged and `acme`-tagged tests for both
exclusive challenge modes, normal-handler fall-through, ALPN selection, and the
defensive default.

## Build tags

- TLS termination, static certificates, SNI, and `min_version` are core.
- ACME issuance requires the `acme` build tag; without it, enabling ACME fails
  before serving.
- HTTP/3 over the same certificate provider requires the `http3` tag.

## Verification

```sh
go test -tags acme ./internal/server -run 'TestACME|TestAcme|TestListenerNextProtos|TestNewACMEManager|TestDirectoryURL' -count=1
go test -race -tags acme ./internal/server -run 'TestACME|TestAcme|TestListenerNextProtos|TestNewACMEManager|TestDirectoryURL' -count=1
go test ./internal/config -run 'ACME|Acme' -count=1
```

## See also

- [mtls.md](mtls.md) — mutual TLS / client-certificate authentication
- [http3.md](http3.md) — QUIC listener behavior
- [egress.md](egress.md) — guarded ACME/OCSP outbound clients
- [compatibility.md](compatibility.md) — contract stability policy
- [examples/auto-https](../examples/auto-https/README.md) — automatic HTTPS example
- [testdata/tls.toml](../testdata/tls.toml) — static TLS sample
