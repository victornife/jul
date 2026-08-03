# Current product truth and temporary correction notices

**Effective date:** 2026-08-03  
**Source baseline:** `66c71b2d48f578a770d5c6e5d86a0e5a9dcada9a`  
**Authority:** [combined repository audit](audit/combined-audit-2026-08-03.md) and master programme #62

This page records current behavior where existing feature guides, maturity tables or historical plans are temporarily broader than the implementation evidence. It is intentionally conservative: a planned fix is not described as shipped.

Each correction notice has an owner issue. The implementation PR that closes the issue must update or remove the corresponding notice in the same change.

## Current release blockers and truth corrections

| Area | Current truth | Operator consequence | Remediation |
|---|---|---|---|
| HTTP/3 server-level mTLS | Equivalent TCP TLS and QUIC enforcement is not currently proven and the audited H3 TLS construction does not carry the complete server-level client-auth policy. | Do not rely on HTTP/3 for a listener whose security boundary requires server-level client certificates. Disable HTTP/3 on that listener or terminate QUIC in a trusted front end until the fix is merged and deployed. | #121 |
| ACME challenge selection | The configured `challenge` value does not currently provide a proven exclusive HTTP-01 versus TLS-ALPN-01 runtime mode. | Do not use the field as a guarantee that the non-selected challenge surface is absent. Treat both current challenge seams as potentially present until corrected. | #122 |
| Static certificate reload | Static `cert`/`key` changes on an already-bound listener remain restart-bound. Listener fingerprints can detect the change, but the live certificate provider is not replaced on the retained listener. | Stage and restart to rotate static certificates. ACME background renewal is separate and continues according to the running manager. | #100 |
| Response cache | The current cache has open lifecycle, immutability, shared-cache semantics and protocol-wrapper defects. | Do not treat the cache GA matrix as current conformance evidence. Use it only after reviewing the cache correctness epic; avoid cache on WebSocket/upgrade routes and highly sensitive authenticated responses until recertification. | #107, #131-#134 |
| Access-log disablement | An empty sink list is not an explicit, stable disable contract. Legacy global/per-server destination fields are compatibility no-ops where the runtime no longer consumes them. | Do not use `sinks = []` as a guaranteed off switch. Use the current documented sinks and wait for the explicit `enabled` field before relying on a typed disable operation. | #124 |
| Configuration strictness | Unknown fields and invalid scalar/enum/duration values are not yet guaranteed to fail identically across every entry path. | Run `jul check` and review generated/managed candidates carefully. A typo may not be rejected as early or consistently as the target contract requires. | #120, #123 |
| Prometheus contract | Current code and documentation are being reconciled against the last released metric names and labels. | Pin dashboards and alerts to the released contract and review #126 before adopting unreleased names or labels. | #126 |
| Compression | The middleware does not yet have complete evidence that `Cache-Control: no-transform` suppresses representation transformation. | Do not enable compression on responses that depend on `no-transform` until #125 is deployed. | #125 |
| WAF URI/query logging | URI and query logging is under explicit redaction, bounding and cardinality review. | Treat request targets as potentially sensitive; avoid secrets in URLs and review log access/retention. | #127 |
| Maturity and roadmap | Historical GA-push and year-horizon documents are not the active implementation sequence. AI is a gated technical experiment, not the automatic next phase. | Use #62 and the combined audit for current sequencing and release blockers. | #114, #162 |

## What remains valid today

The correction notices do not invalidate the complete product. Current strong, shipped foundations include:

- the single-binary Go server and TOML configuration model;
- HTTP/1.1 and HTTP/2 serving, routing, proxying and static content;
- transactional handler/listener reload foundations and planned restart support;
- static TLS termination and ACME issuance/renewal within the current startup-bound manager contract;
- local authentication methods and opt-in named-principal RBAC;
- health checks, service discovery, gRPC, streams, plugins, WAF, observability and Console features when built with their required tags;
- managed apply, history, rollback and operator-facing status foundations.

The exact maturity and release claim for a feature may still be temporarily qualified by this page and the current audit.

## Current lifecycle guidance

Until #89 establishes a closed-world generated lifecycle contract:

- trust the running server's preview/apply result over a prose table;
- treat static certificate/key, server-level client-auth, HTTP/3 listener settings, ACME manager identity/domain policy, egress, tracing providers and structural admin-listener changes as restart-bound unless a merged issue explicitly reclassifies the exact field;
- do not assume that a field is hot merely because a handler generation can be rebuilt;
- mixed candidates are whole-candidate operations: do not expect a hot subset to apply while a restart-bound field is staged;
- preserve the complete `stage_restart`/restart path for structural changes.

## Security guidance while fixes are open

### HTTP/3 and mTLS

For a listener that requires client certificates at the TLS handshake:

1. disable `[servers.http3].enabled` on that address;
2. use TCP TLS/HTTP2 for the protected service;
3. keep application-level authorization and per-location checks enabled;
4. deploy #121 before re-enabling QUIC;
5. verify with a real QUIC client lacking a certificate and with valid/invalid client certificates.

### ACME

- use a deterministic local/staging CA for testing;
- do not infer exclusive challenge exposure from the configured selector until #122;
- keep ACME account/cache directories owner-restricted;
- distinguish configuration application from actual issuance availability;
- remember that ACME renewal is not equivalent to static certificate hot reload.

### Cache

- bypass cache for WebSocket and upgrade routes;
- prefer `private`/`no-store` for user-specific responses;
- audit `Authorization`, `Set-Cookie`, `Vary`, validators and unsafe-method invalidation requirements;
- treat Range/If-Range as origin-bypass behavior in the corrected target contract;
- do not interpret the current GA table as proof of shared-cache RFC completeness.

## Shipped RBAC versus future identity

Jul.IA has an opt-in local RBAC layer with named principals, roles, scoped tokens and audit attribution. It does **not** currently provide OIDC, SAML, SCIM, browser login flows or external identity-provider lifecycle management.

Documentation must distinguish:

- default shared-token mode;
- shipped local named-principal RBAC;
- future external identity and interactive token-management horizons.

## Documentation ownership and removal rules

| Notice | Files that should be updated when fixed |
|---|---|
| HTTP/3 mTLS | `docs/http3.md`, `docs/mtls.md`, `docs/security-posture.md`, `SECURITY.md`, status/maturity sources |
| ACME challenge | `docs/tls-acme.md`, configuration reference, examples and status/maturity sources |
| Static cert rotation | TLS, reload, lifecycle and Console documentation |
| Cache correctness | `docs/cache.md`, known limitations, status/maturity sources, examples and changelog |
| Access-log enablement | configuration, observability, Console and lifecycle docs |
| Strict configuration | configuration/getting-started/CLI docs and generated schema/reference work |
| Metrics | observability reference, dashboards/examples and compatibility policy |
| Compression | compression guide, known limitations and changelog |
| WAF logging | WAF, observability, security posture and support-bundle redaction docs |

## Current implementation order

```text
#119 documentation truth
  -> #120 strict TOML decoding
  -> #121 HTTP/3 mTLS parity
  -> #125 compression no-transform
  -> #122 ACME challenge selection
  -> remaining immediate correctness
  -> cache correctness
  -> lifecycle authority
```

Independent PRs may be reviewed in parallel, but release claims remain gated by the complete evidence path.

## See also

- [Combined repository audit](audit/combined-audit-2026-08-03.md)
- [Known limitations](known-limitations.md)
- [Feature status](status.md)
- [Reload semantics](reload-semantics.md)
- [Security model](../SECURITY.md)
- [Master programme](https://github.com/victornife/jul/issues/62)
