# Jul.IA — Security posture

Companion to [SECURITY.md](../SECURITY.md), which covers the threat model,
vulnerability reporting, and per-feature threat notes. This page covers the
**operational security posture** — what the current admin model looks like,
where the known gaps are, what the roadmap addresses, and how to harden a
production deployment today.

## Admin authentication model

### Current state

Jul.IA supports two local admin authentication modes:

| Mode | Authentication and authorization | Attribution |
| --- | --- | --- |
| Legacy shared token (default) | One bearer token with full control-plane access | Legacy operator identity; no per-person attribution |
| Opt-in `[admin.rbac]` | Named principals, predefined/custom roles, scoped revocable token hashes and deny-by-default route permissions | Principal, role and public token ID in audit/status projections |

The shared token remains a bootstrap/compatibility mode, not the only shipped
model. External OIDC/SAML/SCIM identity and browser login flows are not shipped.
Admin token rotation is tracked by the selected runtime-dynamics issue #95; use
the lifecycle result returned by the running server rather than assuming every
admin setting is already live-applicable.

### Console RBAC (HP-02 / ADR 0010) — delivered (opt-in)

Named-principal RBAC — predefined (`viewer`/`operator`/`admin`/`auditor`) and
custom roles, scoped revocable tokens (hashed at rest), deny-by-default
enforcement at the API boundary, per-principal audit attribution, proactive
Console permission gating, and a backward-compatible migration path — **ships as
an opt-in `[admin.rbac]` layer** (Phase 3). When it is disabled the single
shared-token model above applies; enabling it replaces all-or-nothing access
with least-privilege roles and attributable audit. External identity (OIDC/SSO)
remains a [Y3-02](roadmap/) horizon item. See:

One permission is deliberately outside every predefined role except `admin`:
**`config:trust`**, required to change a listener's
[`client_address`](configuration.md#client-address-and-trusted-proxies)
trusted-proxy policy. Widening `trusted_proxies` lets the named range assert any
client address to CIDR authentication, rate limiting, the WAF and the audit
trail, so it is privilege-escalation adjacent and is held to its own grant with
its own audit category (`config.client_address`). The check is on the effective
configuration difference, not on the endpoint used, so the general structured
patch surface cannot be used to route around it.

- Design spec: [docs/specs/console-rbac.md](specs/console-rbac.md)
- ADR: [docs/adr/0010-console-rbac.md](adr/0010-console-rbac.md)
- Migration: the numbered enable → migrate → revoke procedure in [docs/console.md](console.md)

When running with the legacy shared token (RBAC disabled):
- Treat the admin token as a root credential with no audit trail — or enable
  `[admin.rbac]` to get named principals and attribution.
- Do not expose the admin listener to untrusted networks under any circumstances.
- For CI/automation tokens, prefer a scoped RBAC principal; otherwise use a
  dedicated instance or restrict network access so only the automation host can
  reach the admin port.

---

## Admin listener hardening checklist

```toml
[admin]
enabled = true
listen  = "127.0.0.1:9090"   # loopback only — never 0.0.0.0
token   = "${env:JUL_ADMIN_TOKEN}"  # secret reference, never literal
console = true
```

| Control | Recommendation |
| --- | --- |
| Bind address | `127.0.0.1` (loopback). If remote access is required, use an SSH tunnel or a mutual-TLS proxy in front — never expose raw admin to the internet. |
| Token strength | Minimum 32 random bytes (256-bit); use a password manager or `openssl rand -base64 32`. |
| Token storage | Use `${env:}`, `${file:}`, or `${secret:}` references. See [docs/secrets.md](secrets.md). |
| Token rotation | Rotate through the validated configuration lifecycle; legacy shared-token cutover remains tracked by #95. RBAC token IDs can be revoked independently. |
| pprof endpoints | `/debug/pprof/` is mounted behind bearer-token auth. Do not disable auth when pprof is needed — authenticate with the admin token. |
| Rate limiting | The admin API has a built-in rate limiter (reads, writes, applies separately). Default limits are conservative. |

---

## Token storage in the Console

The Console SPA stores the admin token in `sessionStorage` (cleared on tab
close). This is intentional:

- `sessionStorage` is tab-scoped; closing the browser tab clears the credential.
- It is accessible to JavaScript in the same origin, but the Console SPA has a
  strict `Content-Security-Policy: default-src 'self'` header that blocks
  external scripts and inline script injection.
- `localStorage` was deliberately avoided because it persists across browser
  restarts, extending credential exposure time.

The practical risk is low for a loopback-bound admin listener with no external
access.

---

## Current trust-boundary limitations

- **Forwarded client identity:** `[servers.client_address]` derives one canonical client address per listen address, before virtual-host routing, and publishes it with the direct peer in the request context ([ADR 0016](adr/0016-inbound-identity-and-backend-peer-trust.md)). Forwarding headers are read only when the immediate peer is inside `trusted_proxies`, which is empty by default; anything malformed, oversized, ambiguous or beyond `max_hops` fails closed to the peer. Every consumer reads that one value: CIDR authentication, IP rate-limit keys, the WAF's `REMOTE_ADDR`, access-log `client_ip`/`peer_ip`, outbound `X-Forwarded-For` and the FastCGI `REMOTE_ADDR`/`JUL_PEER_ADDR`. No consumer parses a forwarding header itself, `RemoteAddr` is never mutated, and an inbound chain is never appended to an outbound one. The remaining risk is an over-broad `trusted_proxies`: anything it covers can assert any client address to all of those consumers at once, which is why there are no shorthands and why `jul lint` warns on a range covering everything.
- **Backend peer identity:** the `backend_tls` policy is defined, validated and resolved ([ADR 0016](adr/0016-inbound-identity-and-backend-peer-trust.md), [upstreams.md](upstreams.md#backend-tls)): trust roots with an explicit `ca_mode` enum, a client certificate, an SNI override that a discovery-returned address can never displace, a minimum version and explicit `peer_identities` checked after standard verification. `internal/backendtls` produces one immutable policy per backend and every consumer receives that type rather than the public fields. **The HTTP reverse proxy now enforces it** — for named pools, literal `https://` targets, and WebSocket/streaming upgrades — with one transport per handler generation so a policy change cannot be served by a connection established under the previous trust, and with an explicit refusal to downgrade an `https` route to a plaintext backend. **Every outbound consumer now enforces it**: the HTTP reverse proxy including WebSocket and streaming upgrades, native gRPC passthrough, gRPC-JSON transcoding with its reflection fetch, and active health probes — which verify a backend exactly as live traffic does, so a backend is never reported healthy under weaker verification than the requests Jul sends it. Neither an HTTPS route nor a TLS gRPC route may be downgraded to plaintext. An egress allow-list match is destination authorization, never proof of backend identity.
- **Probe trust follows the pool, not the route:** a route-level `backend_tls` override applies to that route's traffic only, since a pool may serve several routes with different overrides. `jul lint` still warns when an `https` pool with an active HTTP probe has no policy at all.
- **Auxiliary egress is separate:** `[egress]` constrains configuration-driven auxiliary destinations; it is not backend certificate authentication and does not govern the data-plane reverse proxy.
- **WAF request targets:** matched-rule warnings are path-only, bounded and secret-redacted. Raw queries and Coraza macro-expanded messages are omitted; metric labels contain no request target or matched value. Avoid secrets in URLs regardless of downstream or intermediary logging.

---

## SSRF posture

Jul.IA is designed to be SSRF-safe by construction:

**Core invariant:** the upstream target, JWKS URL, forward-auth URL, and
discovery address are **operator configuration**, never derived from a request.
A client request cannot cause Jul.IA to connect to a different host.

| Outbound path | SSRF safe? | Notes |
| --- | --- | --- |
| `proxy_pass` | ✅ Static config | Target is never request-derived |
| `grpc_transcode` / gRPC passthrough | ✅ Static config | |
| JWKS fetch | ✅ Static config | URL is `jwt_jwks_url` in auth config |
| Forward-auth probe | ✅ Static config | URL is `forward_url` in auth config |
| ACME HTTP-01 / renewal | ✅ Outbound to CA only | CA URL is static config |
| Consul / Kubernetes discovery | ✅ Static config | Provider address is static |
| WASM plugin `fetch` | ⚠️ Plugin-controlled | Bounded by `allowed_hosts` allow-list; plugin fetch without `allowed_hosts` is rejected |

**Defense-in-depth:** the optional `[egress]` allow-list
([docs/egress.md](egress.md)) constrains all config-driven auxiliary fetches
(JWKS, forward-auth, Consul/Kubernetes discovery, ACME/OCSP, and the WASM plugin
`fetch` intersection) to an operator-approved set of hosts/CIDRs, reducing the
blast radius of a misconfigured or compromised config. Enforcement is at dial
time (so redirects are re-checked), guarded clients ignore `HTTP(S)_PROXY`, and
blocks are surfaced with typed, secret-safe reasons, bounded
`jul_egress_decisions_total` metrics, and rate-limited structured block logs
(subsystem/host/reason only). It is disabled by default (opt-in hardening).

---

## Secret handling

Jul.IA supports secret references in config values — any string field accepts
`${env:NAME}`, `${file:/path}`, or `${secret:/path}`:

```toml
[admin]
token = "${env:JUL_ADMIN_TOKEN}"   # env var resolved at startup
```

```toml
[admin]
token = "${file:/run/secrets/admin_token}"   # file contents resolved at startup
```

- Resolved values are **masked from all log output** by the redact writer.
- The on-disk config file and history snapshots retain the **unresolved
  reference** (not the plaintext value), so a config backup does not leak
  credentials.
- `jul lint` (with `--strict`) flags literal admin, Consul, and Kubernetes
  tokens that should be secret references.

See [docs/secrets.md](secrets.md) for full reference.

---

## File permissions and atomic writes

All writes that may contain credentials (config file, history snapshots, plugin
upload) use:

- Mode `0o600` — owner-readable only on new files.
- Atomic write — a same-directory temp file is written, fsync'd, and renamed.
  A crash mid-write leaves the previous complete file, never a truncated one.
- History snapshots are stored in `history_dir` with the same `0o600` mode.

**Recommendation:** run Jul.IA as a dedicated service user with a restricted
`umask`, and grant write access only to `cache_dir`, `history_dir`, and the
ACME `cache_dir`. See [docs/deployment.md](deployment.md) for the systemd unit
with `NoNewPrivileges`, `PrivateTmp`, and related hardening.

---

## Plugin security

WASM plugins run in a wazero sandbox:

| Control | Detail |
| --- | --- |
| Memory isolation | Each plugin instance has its own linear memory; no shared memory between plugins or with the host |
| Time limit | Per-plugin `timeout` (default 1s); a hung guest is killed and the request fails safe |
| Memory cap | Per-plugin `memory_limit` (default 16 MiB) |
| Panic isolation | A guest trap or panic is contained; the host logs a `jul_plugin_panics_total` metric and continues serving |
| Capability gating | `kv` and `fetch` capabilities are opt-in per plugin declaration; a plugin without `fetch = true` cannot make outbound HTTP calls |
| Fetch allow-list | `allowed_hosts` (required when `fetch = true`) constrains plugin outbound calls to named hosts |
| Upload gate | Plugin upload via the Console requires `plugin_upload_enabled = true` (default false); uploads are magic-number validated and written atomically |

---

## TLS and mTLS recommendations

- Set `min_version = "1.3"` on internet-facing listeners that do not need
  legacy clients. The default floor is TLS 1.2.
- ACME defaults to the **staging CA** to avoid rate-limit leaks. Set
  `ca = "letsencrypt"` deliberately for production.
- Protect the ACME `cache_dir` — it holds the account private key and issued
  leaf keys. Use `0o700` directory permissions.
- For backend mTLS, use `require` mode (not just `request`) so connections
  without a valid certificate are always rejected. See [docs/mtls.md](mtls.md).

---

## Release supply-chain security

Each release includes:

- **SHA-256 checksums** (`SHA256SUMS` file) for every archive.
- **SBOM** (SPDX JSON) bundled inside each archive and as a Sigstore attestation.
- **Build provenance attestation** (keyless-signed via Sigstore) verifiable with
  `gh attestation verify`.
- **Dependabot** watching Go module and Docker digest pins.
- **`govulncheck`** in CI on every push and PR.

See [docs/release.md](release.md) for verification steps.

---

## See also

- [SECURITY.md](../SECURITY.md) — threat model, hardening defaults, vulnerability reporting
- [docs/secrets.md](secrets.md) — secret references and log redaction
- [docs/egress.md](egress.md) — outbound allow-list hardening
- [docs/deployment.md](deployment.md) — systemd / Docker hardening
- [docs/adr/0010-console-rbac.md](adr/0010-console-rbac.md) — RBAC design
- [docs/specs/console-rbac.md](specs/console-rbac.md) — RBAC implementation spec
