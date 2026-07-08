# Security Policy

> Last reviewed: 2026-07-02

This is the umbrella security document for **Jul.IA** (`jul`). It defines the
trust model the server is built around, the hardening defaults you should know
about, where each feature's detailed threat note lives, and how to report a
vulnerability. It is the anchor for GA criterion 7 ("security / threat note")
across the feature set — see [docs/ga-push.md](docs/ga-push.md).

## Reporting a vulnerability

**Please do not open a public GitHub issue for security problems.**

Report privately through GitHub's **"Report a vulnerability"** flow
(repository **Security** tab → **Advisories** → *Report a vulnerability*), which
opens a private security advisory visible only to the maintainers.

When reporting, please include:

- the affected version / commit and the build tags in use;
- a minimal configuration and request sequence that reproduces the issue;
- the impact you observed (crash, disclosure, bypass, amplification, …); and
- any suggested remediation, if you have one.

We aim to acknowledge a report within a few working days, agree on a disclosure
timeline, and credit reporters who want it once a fix is available. Security
fixes land on `main` first; back-port policy is described in
[Supported versions](#supported-versions).

## Supported versions

Jul.IA follows [Semantic Versioning](https://semver.org/) and the
[compatibility policy](docs/compatibility.md). Security fixes are handled as
follows:

- **`main`** — receives security fixes first. Development happens here.
- **Latest `v1.x` release** — security fixes from `main` are back-ported to the
  latest minor release branch when feasible.
- **Older releases** — no committed back-ports; upgrade to the latest `v1.x`
  release.

| Version | Security fixes |
| --- | --- |
| `main` | ✅ yes — fixes land here first |
| Latest `v1.x` release | ✅ yes — back-ported from `main` when feasible |
| Older `v1.x` / pre-1.0 tags | ⚠️ best effort; upgrade recommended |

The [compatibility policy](docs/compatibility.md) governs how breaking and
security changes are versioned.

## Trust model

Jul.IA is an edge server: it sits between an **untrusted** network of clients and
a set of **operator-chosen** upstreams. The boundaries are:

| Domain | Trust | Notes |
| --- | --- | --- |
| The TOML configuration | **trusted** | Operator-supplied. It can open files, dial hosts, and set headers; treat it as code and restrict who can edit it. |
| The `jul` binary + host filesystem | **trusted** | Protect the working directory, the ACME `cache_dir`, and any htpasswd/CA/key files (service-user-only). |
| Downstream client requests | **untrusted** | Headers, bodies, paths, tokens, and TLS client certs are all attacker-controlled and validated at the boundary. |
| Upstreams you configure | **trusted by default** | `proxy_pass` / gRPC targets are static config; request input never selects them (no SSRF by design). gRPC `use_reflection` extends trust to the backend's self-description — pin a descriptor set for untrusted topologies. |

**Core invariant — request input never widens the attack surface.** The upstream
target, the JWKS URL, the certificate set, and the FastCGI root are all
**operator configuration**, never derived from a request. This is why proxying,
JWKS fetching, and ACME are SSRF-safe by design rather than by filtering.

## Hardening defaults & recommendations

- **Terminate TLS at the edge; keep backends on a trusted network** (or use mTLS
  to them where supported). The TLS floor is **1.2**; set `min_version = "1.3"`
  on internet-facing listeners that do not need legacy clients.
- **ACME defaults to the staging CA on purpose** (avoids leaking production rate
  limits). Set `ca = "letsencrypt"` deliberately for production, and **protect
  `cache_dir`** — it holds the ACME account key and issued private keys.
- **Bound request inputs.** Set `client_max_body_size` per server/location (413
  on overflow) and set `read_timeout` / `write_timeout` (both **unset by
  default**, so slow-client protection is opt-in).
- **Authenticate at the edge.** Pair `proxy_pass` / gRPC locations with a location
  `auth` modifier (CIDR / Basic / JWT / forward-auth). At most one credential
  method per location; see [docs/auth.md](docs/auth.md).
- **Lock down the admin console.** Always set `[admin].token` (constant-time
  compare; strict CSP, `X-Frame-Options: DENY`, same-origin `/api`). There is
  only a single shared token — no per-user keys, scopes, or audit attribution —
  so treat it as a shared secret and rotate it promptly on team changes. The
  scoped multi-principal replacement (predefined + custom roles, revocable
  tokens, per-principal audit) is designed in
  [docs/specs/console-rbac.md](docs/specs/console-rbac.md)
  ([ADR 0010](docs/adr/0010-console-rbac.md)).
  Edits that change admin reachability (disabling admin, moving its listen
  address, rotating its token, or disabling the web console) are held for
  explicit confirmation so a single apply cannot silently lock you out. See the
  [console security model](docs/console.md#security-model) and the invariants in
  [ADR 0004](docs/adr/0004-console-ui-invariants.md).
- **Run least-privilege.** Use the provided systemd unit
  ([deploy/systemd](deploy/systemd/)) with a dedicated service user and a
  read-only deployment; grant write only to `cache_dir` / `history_dir`.
- **Persisted state is written tightly and atomically.** When the admin console
  saves the configuration, or the server records a history snapshot, or
  `jul import` writes a translated config, the file is created `0o600` (so a
  freshly written config that may carry inline credentials is never
  world-readable) and written via a same-directory temp file that is fsync'd and
  renamed into place — a crash mid-write leaves the previous complete file, never
  a truncated one. An **existing** file's mode is preserved, so an operator who
  deliberately changed it keeps that choice; tighten an over-permissive config
  with `chmod 600`.

## Per-feature threat notes

Each GA feature documents its own threat surface. This table is the index; follow
the link for the full note.

| Feature | Threat note | Key protections |
| --- | --- | --- |
| Core HTTP (static / proxy / FastCGI / vhosts / routing) | [core-http.md](docs/core-http.md#security--threat-notes) | path traversal blocked (`os.Root`); SSRF-safe static `proxy_pass`; CRLF/smuggling rejected by `net/http`; body/timeout limits |
| Authentication (CIDR / Basic / JWT / forward-auth) | [auth.md](docs/auth.md#security--threat-notes) | `alg`-confusion safe (`none` rejected); constant-time Basic + dummy hash; CIDR uses `RemoteAddr`; JWKS SSRF-safe + fetch-throttled; tokens never logged |
| TLS + automatic HTTPS (ACME) | [tls-acme.md](docs/tls-acme.md#security--threat-notes) | edge termination; 1.3-capable; staging-by-default CA; private-key dir protection; OCSP staples fail-open |
| mTLS client auth | [mtls.md](docs/mtls.md#certificate-revocation-crl) | CA-bundle verification; signature-verified CRL; SAN allow-list; identity via `$ssl_client_*` (see [operational notes](docs/mtls.md#operational-notes)) |
| gRPC transcoding | [grpc-transcoding.md](docs/grpc-transcoding.md#security--threat-notes) | bounded inputs (`max_message_size`); fuzzed path-template parser; descriptors are data, never executed; credential pass-through |
| Native gRPC passthrough | [grpc-proxy.md](docs/grpc-proxy.md#conformance--limitations) | streaming pass-through with trailers; LB + health apply; no body buffering |
| Admin console & API | [console.md](docs/console.md#security-model) | bearer token (constant-time); strict CSP; traversal-safe snapshot IDs; validate-before-apply |

## Parser hardening (fuzzing)

Every component that parses attacker-reachable input is fuzzed, and the
[`fuzz (smoke)` CI job](.github/workflows/ci.yml) runs each target on every
push/PR (see [scripts/fuzz.sh](scripts/fuzz.sh)):

- **Auth** — `FuzzParseJWKS` (JWKS document), `FuzzValidateToken` (bearer token);
- **Router** — `FuzzHostScore`, `FuzzMatchLocation` (host / location matching);
- **FastCGI** — `FuzzScriptName`, `FuzzParseSocketAddress`;
- **gRPC transcoding** — `FuzzParseTemplate` (`google.api.http` path templates).

A crasher found in CI is uploaded as an artifact so it can be committed as a
permanent regression seed.

## Cryptography

- **TLS** uses the Go standard library (floor 1.2, 1.3 available; cipher suites,
  session tickets, and 0-RTT are not tunable — stdlib defaults apply).
- **HTTP Basic** accepts only bcrypt htpasswd hashes (`$2a$` / `$2b$` / `$2y$`)
  and compares in constant time with a dummy hash for unknown users.
- **JWT** validation is **asymmetric-only** (RS/ES/PS · 256/384/512); the `none`
  algorithm is always rejected and the key type is matched to the `alg` to defeat
  algorithm confusion. Keys come from an operator-configured HTTPS JWKS endpoint.
- **mTLS** verifies client certificates against a CA bundle, honours a
  signature-verified CRL, and can gate on a SAN allow-list.

## Dependencies & supply chain

- The dependency set is intentionally small; the default build is a single static
  binary. Optional integrations sit behind build tags.
- [`govulncheck`](.github/workflows/ci.yml) runs in CI against the full opt-in tag
  set, alongside `go vet`, `golangci-lint`, the race detector, and the benchmark
  and fuzz smoke jobs.
- A statement-coverage floor is enforced in CI (the `coverage gate` job) so test
  erosion fails the build rather than going unnoticed.
- [Dependabot](.github/dependabot.yml) tracks GitHub Actions, Go modules, and the
  Docker base image weekly, keeping pinned versions current without manual digest
  chasing.
- Tagged releases ([`release.yml`](.github/workflows/release.yml)) ship with a
  signed software bill of materials and build provenance: an SPDX SBOM is
  generated for the release binary, and both the binary and its SBOM are attested
  via keyless Sigstore signing (GitHub OIDC, no long-lived keys). The attestations
  can be verified with `gh attestation verify <artifact> --repo <owner>/<repo>`.

