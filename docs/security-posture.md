# Jul.IA — Security posture

Companion to [SECURITY.md](../SECURITY.md), which covers the threat model,
vulnerability reporting, and per-feature threat notes. This page covers the
**operational security posture** — what the current admin model looks like,
where the known gaps are, what the roadmap addresses, and how to harden a
production deployment today.

## Admin authentication model

### Current state (single shared token)

The admin API and Console authenticate with a **single shared bearer token**
(`[admin].token`), compared in constant-time in `internal/admin/server.go`.
That token is an implicit super-user: any holder can read config, apply
changes, upload WASM, view pprof, purge cache, and roll back history.

| Property | Today |
| --- | --- |
| Authentication | Constant-time Bearer token comparison |
| Authorization | All-or-nothing (token = full access) |
| Identity / audit attribution | Hard-coded as `"operator"` in all audit events |
| Token lifecycle | Static config value; rotation = edit config + reload |
| Multi-user | Not supported |

**What this means in practice:**
- The token should be treated as a shared secret. Rotate it promptly on team
  changes.
- Store it in a secrets manager or `${env:}` / `${file:}` reference (see
  [docs/secrets.md](secrets.md)) — never commit it to version control.
- Keep the admin listener on loopback (`127.0.0.1`). `jul lint` warns when
  admin is bound off-loopback without a token.
- For multi-operator teams, today's option is to share the token (all operators
  are superusers) or run separate instances (each with their own token).

### Roadmap: Console RBAC (HP-02 / ADR 0010)

The full RBAC design — named principals, predefined and custom roles, scoped
revocable tokens, deny-by-default enforcement, per-principal audit attribution,
and a backward-compatible migration path — is **designed and implementable**. It
is not yet shipped. See:

- Design spec: [docs/specs/console-rbac.md](specs/console-rbac.md)
- ADR: [docs/adr/0010-console-rbac.md](adr/0010-console-rbac.md)

Until RBAC ships:
- Treat the admin token as a root credential with no audit trail.
- Do not expose the admin listener to untrusted networks under any circumstances.
- For CI/automation tokens, use a dedicated instance or restrict network access
  so only the automation host can reach the admin port.

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
| Token rotation | Edit `admin.token`, restart or reload. Audit log entries do not attribute to a user today, so rotation is the only revocation mechanism. |
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
(JWKS, forward-auth, discovery) to an operator-approved set of
hosts/CIDRs, reducing the blast radius of a misconfigured or compromised config.
It is disabled by default (opt-in hardening).

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
