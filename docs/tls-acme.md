# TLS & automatic HTTPS (ACME)

> **Maturity:** The existing TLS and HTTP-01/TLS-ALPN-01 capability is GA (see
> [ADR 0003](adr/0003-maturity-and-ga.md)). The new RFC 2136 DNS-01 path is
> Beta, implemented in this source but not independently released or soaked.
> Public Let's Encrypt staging issuance and renewal remain unverified. Both
> must be exercised on an authorized disposable zone before DNS-01 is called
> production-qualified.
> Mutual TLS is documented separately in [mtls.md](mtls.md).

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
needed for the HTTP-01 challenge, or use TLS-ALPN-01 on :443 only. DNS-01
instead updates an authoritative DNS server and needs neither inbound
challenge listener. Jul.IA activates only the configured challenge surface.

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

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200                            # replace with your site's action

# HTTP-01 needs a reachable plaintext listener on port 80. Its challenge path
# is answered by ACME before the normal HTTPS redirect runs.
[[servers]]
listen = "0.0.0.0:80"
server_names = ["example.com", "www.example.com"]
redirect_https = 308

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200
```

> The CA defaults to **staging** so an accidental deployment never burns
> production rate limits. Set `ca = "letsencrypt"` for trusted certificates.
> Replace the domains and email, make both ports reachable, and give the Jul
> service identity write access to the cache directory. If you select
> `challenge = "tls-alpn-01"`, the port 80 block is unnecessary.

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
| `challenge` | string | `"http-01"` | `"http-01"`, `"tls-alpn-01"`, or `"dns-01"`; the modes are exclusive. |
| `dns_provider` | string | — | Required for DNS-01: `"rfc2136"`. Must be absent for other challenges. |
| `cache_dir` | string | `"./jul-data/certs"` | Directory cache for issued certificates and the account key. |
| `ocsp_stapling` | bool | `true` | Staple OCSP responses for ACME-issued certificates. |

All enabled ACME blocks share one process-lifetime manager and must agree on
`email`, `ca`, `challenge`, provider settings, `cache_dir`, and
`ocsp_stapling`. Validation rejects a configuration whose blocks disagree.

### DNS-01 with RFC 2136 and TSIG

The only DNS update mechanism is RFC 2136, authenticated with HMAC-SHA256 TSIG.
The deployment selected this mechanism rather than a provider-specific token
API; Cloudflare was considered as an alternative, but no Cloudflare zone was
specified. Jul uses the maintained `github.com/miekg/dns` (BSD-3-Clause) for
DNS wire messages and the existing `golang.org/x/crypto/acme` primitives for
the bounded ACME cycle; it does not fork autocert. CertMagic is a maintained
alternative with DNS-01, renewal and pluggable storage, but adopting its separate
manager would also require adapting Jul's cache and shutdown ownership and
constraining its independently constructed ACME transport to managed egress.
The narrower adapter keeps those contracts with Jul's existing process owner.
Existing HTTP-01 and TLS-ALPN-01 continue to use autocert. Where an existing
certificate service already handles renewal, or DNS cannot safely allow scoped
dynamic updates, external issuance and hot-reloaded static PEM may be simpler.

Prerequisites:

1. Use an authoritative server that supports TCP RFC 2136 updates, TSIG-signed
   TXT queries, and RRset prerequisites. Restrict its HMAC-SHA256 key to TXT
   updates at the exact challenge owners or use an NS-delegated challenge zone
   with similarly scoped permissions. The CA must resolve the public delegation;
   Jul does not need a publicly reachable challenge port.
2. Provide the base64 TSIG key (at least 16 decoded bytes) through a single
   `${env:NAME}` or `${file:/path}` secret reference. Restrict its file and
   environment access. Never put the key itself in TOML or the Console.
3. Use an `acme`-tagged build and a writable, private certificate cache.
   Allow the CA, update server and observers through [egress](egress.md) if
   enabled.
4. Configure explicit `server_names` and certificate `domains` (or omit
   `domains` to use the server names). A name is accepted only when both the
   certificate and that listener's configured SNI patterns cover it. A wildcard
   covers exactly one label: include the apex separately when needed, and
   explicitly configure any other names. At most 100 distinct certificate names
   are supported per listener. To bound CA work, an order may contain at most
   200 authorization URLs, and each authorization must name a requested
   certificate identity.

| `[servers.tls.acme.rfc2136]` key | Meaning |
| --- | --- |
| `server` | Authoritative RFC 2136 update endpoint, as host or IP and numeric port. |
| `observers` | Required list of 1–8 CA-facing authoritative DNS endpoints, each as host:port. |
| `zone` | Explicit authoritative update zone containing every direct owner or mapped TXT target. |
| `tsig_key_name` | TSIG key name configured on that server. |
| `tsig_secret` | A single environment or file secret reference; its resolved value is base64 TSIG key material. |
| `cname_delegations` | Optional map from each challenge owner to its one-hop `target` inside `zone` and its own `observers` list. |
| `propagation_timeout` | Polling bound after the initial DNS observation, 1s–15m; default `2m`. |
| `poll_interval` | Poll interval, 250ms–30s and no greater than the timeout; default `2s`, with jitter. |

[testdata/acme-dns-mtls.toml](../testdata/acme-dns-mtls.toml) combines apex,
wildcard, staging and optional mTLS. For a syntax-only check, set
`JUL_DNS_TSIG` to a test base64 value containing at least 16 bytes, then run
`go run -tags acme ./cmd/jul check -config testdata/acme-dns-mtls.toml`.
Before serving, replace the example domain, DNS server, zone and test-only
client CA, and supply the real key through the service's secret mechanism.
Start with staging (its certificates are not publicly trusted), verify
issuance and cleanup, then stage `ca = "letsencrypt"` and restart.

#### Delegation and propagation

The update endpoint and the observers have separate roles. Jul sends signed
queries and RFC 2136 updates to `server`; observers receive only unsigned,
read-only TCP DNS queries. Before challenge submission, **every** observer must
return an authoritative response containing the exact TXT value. Each observer
must be authoritative for the target. For a CNAME, its separate per-owner
observers must be authoritative for the alias. Choose endpoints representing the DNS servers
the CA can reach. For example, an NS-delegated challenge zone replicated to
public authoritative secondaries can use those secondaries as observers. For a
hidden primary with delayed replication, do not list only the primary: it may
already see the value while a public secondary does not, and increasing
`propagation_timeout` alone cannot fix that observation gap. Include the
CA-facing secondary instead. Even all configured observers succeeding cannot
prove that every recursive cache or every CA vantage point has converged.

Direct TXT and NS-delegated owners need no CNAME mapping. To delegate an owner
to one explicit target, use a zone containing that target, for example:

```toml
[servers.tls.acme.rfc2136]
server = "hidden-primary.example.net:53"
observers = ["ns1.example.net:53", "ns2.example.net:53"]
zone = "validation.example.net"
tsig_key_name = "jul-acme.validation.example.net"
tsig_secret = "${env:JUL_DNS_TSIG}"

[servers.tls.acme.rfc2136.cname_delegations."_acme-challenge.example.com"]
target = "txt.validation.example.net"
observers = ["ns1.example.com:53", "ns2.example.com:53"]
```

The published CNAME must match exactly on every alias observer before any update;
Jul then writes only the target. Missing or unconfigured aliases, mismatches,
and CNAME chains are rejected. Both the alias and the exact target TXT must
remain visible to their respective observers before every submission or retry. Configure the CA-facing
authorities and their replication accordingly; Jul does not discover or follow
arbitrary CNAMEs.

Existing string-valued CNAME mappings must be migrated to the table form above,
including an explicit alias observer list.

DNS or CA outages retry with bounded backoff; a still-valid certificate remains
in service. Each configured
certificate identity has independent ECDSA P-256 and RSA-2048 variants, with
separate ACME orders and renewals; account and provider configuration are
shared. Plan for twice the issuance and renewal activity and corresponding CA
rate limits. Check each variant's Console state, DNS authority/TSIG permission,
delegation, egress, cache permissions and clock before retrying. Client-auth
errors instead concern the separate client CA, CRL or allowed SANs.

Cache loading and certificate issuance use separate fixed-size worker pools,
so a stalled order does not prevent unrelated cached certificates from loading.
When all issuance workers are busy, additional orders wait. Within an order, a
bounded worker pool presents all pending challenges before waiting for
propagation. A busy shared authorization is deferred without refetching it or
already-valid siblings, without preventing independent challenges in the order
from progressing. Up to 200 authorization waiters per order can progress
independently, while at most four DNS exchanges and four ACME HTTP requests
run concurrently per manager; one DNS exchange slot is reserved for cleanup.
Propagation queries wait for DNS admission outside the polling timeout. After the
first observation, the timeout counts polling delays and admitted exchanges;
one final observation is attempted at the end of that budget. Each exchange
has a 10-second deadline. ACME HTTP requests have a 15-second deadline and
responses are limited to 1 MiB; authorization polling and challenge retries
respect their respective `Retry-After` hints independently. Their deadlines are
shared across orders even when a hint exceeds an order's timeout; extreme hints
are bounded by Go's maximum duration (about 292 years). Transient failures with
absent or invalid hints use exponential backoff, starting at one second and
capping its base delay at one minute. Added jitter never advances a retry.
Polling stops on terminal authorization states. A validated authorization can be reused
by another matching order while its cleanup continues, no later than its expiry
or 20 minutes after validation; completed retry state is released after cleanup.
At most 2,048 authorization states are retained;
if all are still active or awaiting a retry, additional orders report an error.
TSIG timestamps are refreshed after admission, and a sibling failure does not
cancel another authorization's progress.
Each order's ACME work has a 20-minute deadline. If that deadline or shutdown
cancels a submitted or uncertain challenge, Jul retains its owned TXT records
while the CA might still validate and uses the existing order retry path for
bounded reconciliation. A challenge known not to have been submitted can be
cleaned immediately. When the CA reports a terminal challenge or authorization,
cleanup runs separately from finalization: a deletion failure does not prevent
installation of an otherwise valid certificate. Cleanup admission waits up to
20 minutes for the owner lock and DNS and cleanup slots; each admitted operation
then has 15 seconds to complete, with at most three attempts and four concurrent
cleanup operations per manager. An unresolved cleanup is logged and shown
separately from certificate state. State is in memory: shutdown or restart can
leave records requiring manual recovery. A blocked operating-system I/O call
may remain until the filesystem recovers.

#### DNS-01 manual recovery

For each authorization Jul adds the TXT value and a
`jul-dns01-<sha256>-<nonce>` ownership marker to the same RRset. Atomic
prerequisites protect concurrent writers; cleanup removes only that value and
marker. If the value already exists, Jul observes it without claiming or
deleting it. Do not remove a value still used by another issuer.

If the Console shows an unresolved cleanup warning, or logs report retained
records, first check the CA account's authorization state. **Leave the records
in place while validation is pending or uncertain.** Once the CA confirms the
challenge/authorization is terminal, prevent concurrent issuance for that owner
and inspect the update server at the logged `owner` (the mapped target for
CNAMEs). The log's `marker` identifies the owned record; its 64 hexadecimal
characters after `jul-dns01-` are SHA-256 of the exact TXT value. For each
candidate, compare `printf '%s' '<TXT value>' | sha256sum` with that digest.
If the marker is absent, the value cannot be attributed safely, another issuer
is still using it, or CA state cannot be confirmed, do not delete it.

With an appropriately protected `nsupdate` key file and the exact owner, zone,
value and marker substituted, delete **individual records**, never the RRset:

```text
nsupdate -v -k /run/secrets/jul-tsig.key
> server dns.example.com 53
> zone _acme-challenge.example.com
> update delete _acme-challenge.example.com TXT "<exact-owned-value>"
> update delete _acme-challenge.example.com TXT "<logged-marker>"
> send
```

Re-query the authoritative update server and verify all unrelated TXT values
remain. The key file is for the DNS utilities; do not place its contents in
command arguments, logs or TOML. Warnings are not persisted across restart, so
retain the relevant log and complete recovery even if the restarted Console no
longer shows it.

#### Reproducible DNS interoperability test

On Linux with Docker and the pinned images available, run
`JUL_BIND_E2E=1 go test -tags 'acme http3' ./internal/server -run '^TestDNS01Bind9Interoperability$' -count=1 -v`.
The fixture uses BIND 9.20.11 primary and delayed secondary processes from
`ubuntu/bind9@sha256:45d541e9688f2c9010d22a78200117b66ed2df31b78f3464876f7e6e15cfaf77`
and the pinned Pebble image. It uses TCP RFC 2136, an explicit update zone,
and an HMAC-SHA256 key restricted to exact TXT owners. It checks NS and one-hop
CNAME issuance and renewal, delayed replication, alias mismatch before mutation,
unrelated-value preservation, cleanup, wrong-key and clock-skew diagnostics,
and denial of an out-of-scope update. It does not validate public DNS or
Let's Encrypt staging; those require a separately authorized disposable zone.

## Behaviour matrix

| Capability | Status | Notes |
| --- | --- | --- |
| TLS 1.2 / 1.3 termination | ✅ | `min_version` selects the floor; 1.3 always offered |
| HTTP/1.1 + HTTP/2 over TLS | ✅ | ALPN advertises `h2`, `http/1.1` |
| HTTP/3 (QUIC) on same certs | ✅ (`http3`) | shares the certificate provider |
| SNI multi-certificate | ✅ | one listener serves many `server_names` |
| Wildcard SNI (`*.example.com`) | ✅ | single label; exact match wins over wildcard |
| SNI fallback | ✅ | first-loaded static certificate; DNS-01 rejects unconfigured SNI |
| Static PEM certificates | ✅ | `cert` / `key` |
| Static cert **hot reload** | ✅ | `cert`/`key` path or file-content changes are preflighted and atomically swap the retained listener provider (#100) |
| ACME HTTP-01 | ✅ (`acme`) | exclusive mode; needs a plain HTTP listener |
| ACME TLS-ALPN-01 | ✅ (`acme`) | exclusive mode; advertises `acme-tls/1` on the TLS listener |
| ACME DNS-01 | ✅ (`acme`, Beta) | RFC 2136/TSIG; no inbound challenge listener |
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
3. **Fallback** to the first-loaded static certificate.

Names are lower-cased; an empty SNI goes straight to the static fallback.
DNS-01 instead rejects empty, unconfigured and certificate-uncovered SNI.

### ACME challenges

| Challenge | Port needed | How it is answered |
| --- | --- | --- |
| `http-01` (default) | plain HTTP (:80) | `/.well-known/acme-challenge/*` is served by autocert; all other requests fall through to the normal handler. ACME does **not** add `acme-tls/1` to TLS ALPN. |
| `tls-alpn-01` | TLS listener (:443) | The listener advertises `acme-tls/1`; the challenge certificate is served only for handshakes that negotiate it. ACME does **not** install the plain HTTP challenge handler. |
| `dns-01` | no inbound challenge listener | TSIG-authenticated RFC 2136 update and authoritative TXT polling; neither the HTTP handler nor `acme-tls/1` is activated. |

The three modes are exclusive. An empty programmatic challenge value is treated
defensively as the documented HTTP-01 default; normal parsed configurations
receive that default before runtime construction.

### DNS-01 TLS and mTLS compatibility

The issued server certificate feeds the same SNI, OCSP, TCP and QUIC policy
as existing ACME certificates. Server-CA trust and client-CA trust are
independent. The local Pebble matrix exercises all cells with certificate chain,
SNI and ALPN verification:

DNS-01 automatically prefers an available ECDSA P-256 certificate when the
client supports it, otherwise it serves an available RSA-2048 certificate
when compatible. Each variant is cached and renewed independently; failure of
one does not discard its still-valid certificate or the other variant.

| Transport | Versions | Client authentication |
| --- | --- | --- |
| HTTP/1.1 | TLS 1.2, 1.3 | none, request, require |
| HTTP/2 | TLS 1.2, 1.3 | none, request, require |
| HTTP/3 (`http3` tag) | TLS 1.3 | none, request, require |

`request` allows anonymous clients on unrestricted routes but denies them on
`require_client_cert` routes; `require` rejects missing or invalid client
certificates during the handshake. Fresh and resumed handshakes recheck client
CA/CRL/SAN policy after reload, and existing connections recheck protected
requests; see [mtls.md](mtls.md#operational-notes) for stream retirement.
Renewal changes the server certificate for fresh handshakes, not client trust.

To inspect a TCP connection, use `openssl s_client -connect HOST:PORT
-servername NAME -alpn h2 -tls1_2 -CAfile SERVER_CA.pem` and inspect the
certificate chain, negotiated version and ALPN. For a protected route, add
`-cert CLIENT.pem -key CLIENT.key` or use `curl --cacert SERVER_CA.pem
--cert CLIENT.pem --key CLIENT.key https://NAME/`. A client signed by the
configured client CA succeeds; a missing certificate is denied on the
protected route and an untrusted client certificate fails the handshake.
With TLS 1.2, add `-cipher ECDHE-ECDSA-AES128-GCM-SHA256` or
`-cipher ECDHE-RSA-AES128-GCM-SHA256` to inspect the respective certificate
variant.
Use `curl --http3-only` with a QUIC-enabled curl to check HTTP/3. Use a
staging CA trust bundle only for staging tests, never as the production trust
store.

### Certificate reload semantics

| Source or policy | On configuration reload |
| --- | --- |
| Static `cert`/`key` | **Hot reload** — candidate material is parsed during Prepare and the retained listener provider swaps atomically at Publish (#100). |
| ACME renewal under the running manager | **Automatic** — renewal continues without configuration reload. |
| ACME enablement, domains, challenge, provider/TSIG, account, issuer, or cache | **Not reloaded** — process-owned manager identity/policy remains restart-bound or deferred. |
| DNS-01 listener `server_names` | **Hot reload** when still covered by the fixed certificate names; the listener SNI scope swaps atomically. |
| ACME `ocsp_stapling` | **Hot reload** — an atomic policy on the stable provider wrapper changes new certificate lookups without replacing the ACME manager or listener (#106). |
| `client_auth` (mTLS) | Stable v2.0.0 is restart-bound. On post-tag `main`, data-plane client-auth policy is hot for new handshakes (#486, Beta); admin listener client auth remains restart-bound. See [mtls.md](mtls.md). |

When configuration is applied through a validated write path, every referenced
static `cert`/`key` pair is parsed before persistence. A broken or mismatched pair
fails the apply up front rather than surfacing only during asynchronous reload.
ACME-served addresses are skipped because their certificates are obtained at
handshake time.

#### Restart-required ACME changes

The autocert manager's domain allow-list, account, issuer, challenge and cache
remain process-lifetime state. A candidate changing one of those values must be
reported as restart-required and must not partially publish only its hot subset.
`ocsp_stapling` is deliberately different: the manager/provider identity stays
stable while an atomic wrapper policy decides whether a new certificate lookup
uses the existing stapler/cache. Disabling starts no new OCSP refresh; in-flight
work may finish, and re-enabling may reuse still-valid cached state.

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
- **Challenge surfaces are exclusive.** HTTP-01 exposes the plain challenge path;
  TLS-ALPN-01 exposes `acme-tls/1`; DNS-01 exposes neither.
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
the ACME directory/order/challenge client, RFC 2136 DNS TCP dialer and OCSP
responder client are guarded like other auxiliary fetches. Issuance requires
the CA endpoints and configured DNS server to be allowed;
stapling separately requires its OCSP responder. An OCSP denial can leave the
certificate unstapled, but does not itself block issuance or a TLS handshake.

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

- **One DNS-01 update mechanism.** RFC 2136 with TSIG is supported; provider
  APIs, arbitrary CNAME traversal and recursive/global propagation checks are
  not. One-hop CNAME mappings must be explicit. Public Let's Encrypt staging
  issuance and renewal remain unverified pending an authorized disposable zone.
- **Static certificate rotation is hot-reloadable on a retained listener
  (#100).** A cert/key content or path change is validated during preflight and
  swapped atomically into the listener's existing dynamic certificate
  provider at Publish — no rebind, no dropped connections. Enabling or
  disabling TLS itself and changing the TLS minimum version remain
  restart-bound (`HR-16`). Data-plane client-auth policy can hot reload for new
  handshakes on `main` (#486, Beta); the stable v2.0.0 policy remains
  restart-bound. Admin listener client auth remains restart-bound.
- **ACME manager transitions are restart-bound.** Domain, account, issuer,
  challenge, and cache changes require planned restart/deferred manager work.
  `ocsp_stapling` itself is hot and does not replace manager identity (#106).
- **OCSP stapling is ACME-only.** Static certificates are served without a
  stapled OCSP response.
- **No cipher-suite, session-ticket, or server 0-RTT configuration.** Jul.IA uses
  Go's standard-library defaults.
- **Preflight is local, not external issuance validation.** Jul.IA validates
  referenced static key pairs before persistence, but cannot prove public DNS,
  firewall, CA reachability, or successful future renewal.

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), the existing TLS + automatic HTTPS
capability is GA. The soak criterion was completed on 2026-07-05 via Phase 2A.
That historical evidence does not certify the new DNS-01 path, which remains
Beta without release or soak evidence.

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
scripts/acme-dns-pebble-e2e.sh
```

## See also

- [mtls.md](mtls.md) — mutual TLS / client-certificate authentication
- [http3.md](http3.md) — QUIC listener behavior
- [egress.md](egress.md) — guarded ACME/OCSP outbound clients
- [compatibility.md](compatibility.md) — contract stability policy
- [examples/auto-https](../examples/auto-https/README.md) — automatic HTTPS example
- [testdata/tls.toml](../testdata/tls.toml) — static TLS sample
