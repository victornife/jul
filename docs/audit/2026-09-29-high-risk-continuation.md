# High-risk source continuation — 2026-09-29

> **Scope:** Targeted source review after PR #482 head `bd8f5b0`. This is a
> branch-local remediation record, not full-repository certification. The
> documentation coverage ledger still lists unreviewed material. No hourly
> soak is claimed or required by this batch.

## Findings and changes

| Boundary | Source evidence and effect |
| --- | --- |
| Admin exposure | `config.Validate` allowed an off-loopback admin listener with no token or RBAC; TLS protected transport but did not identify API callers. Validation now rejects this configuration and a whitespace-only configured token. Tokenless loopback remains supported. |
| Credential ambiguity | Admin legacy/RBAC, Basic, JWT and forward-auth previously selected one of several `Authorization` fields. They now reject duplicates. Forward-auth replaces client-controlled forwarding identity and TLS certificate assertions before contacting its service, and denies an unattributed trusted-proxy chain locally. |
| Admin mutation binding | Repeated or explicitly empty `Idempotency-Key` values, repeated `Content-Type` fields and duplicate or malformed `base_version` queries could make a write or replay binding ambiguous. Admission rejects these before mutation. |
| Bounded admin input | Several legacy JSON, raw TOML, preview and client-report handlers decoded or applied a valid prefix after a bounded reader silently truncated a larger body. All affected handlers now read one byte beyond their cap and reject the complete request on overflow. |
| Outbound trust material | JWKS and OCSP fetches could treat a truncated valid prefix as the complete document. Service discovery decoded unbounded Consul/Kubernetes replies; Kubernetes pagination and continuation tokens had no aggregate bound. These now fail closed at their stated limits, retaining last-good targets on discovery failure. |
| Files and credentials | CLI profiles/tokens, Basic htpasswd, file secret references, TOML sources, Kubernetes mounted credentials/CA, and backend/client TLS material had unbounded or ambiguous file reads. The touched readers now bound input, require regular files where appropriate, and reject malformed or duplicate credentials. TLS fingerprints stream files instead of loading them whole. |
| Remote CLI plaintext | `http://localhost` allowed a bearer token over ordinary name resolution. Plaintext connections now disable proxies and connect only to verified loopback IPs. Redirects remain disabled. |

## Verification and remaining scope

Each local commit passed `git diff --check`. The scratch environment has no Go
toolchain; package and build-tag tests await the single final CI pass after
publication. In particular, exercise core, `acme`, `consul`, `kubernetes`, WAF
and WASM build tags in the established preflight. The previous qualifying hour
soak remains the duration evidence for PR #482; this branch does not restart it.

The remaining review includes cache/revalidation semantics, plugin and WAF
runtime boundaries, and the unreviewed documentation/platform ledger rows.
Findings here only cover the named paths and cannot certify those scopes.
