# Admin security review: route-policy foundation

| Item | Evidence |
| --- | --- |
| Date | 2026-10-06 |
| Reviewed source | Post-#548 main, `1b449614bcbb7b22d8ddf466c1c9f432c5d49ea6` |
| Owner | [#514](https://github.com/victornife/jul/issues/514) |
| Disposition | Partial review installment; #514 remains open |
| Production changes | None; tests, CI wiring and documentation only |
| Changed/added Go production coverage | N/A: no production Go statements changed |
| NGINX migration classification | Not applicable: no listener, configuration, API or data-plane behavior changes |

## Claim boundary

This installment establishes the CI-enforced route/permission/transport guard
and records a targeted review of its adjacent security boundaries. It is **not**
a line-by-line certification of all admin handlers or all Console actions.
The broader review requested by #514 remains necessary before closing that
issue or treating it as evidence for #517/#527. No maturity decision is made.

## Machine-checked route map

The authoritative permission map is
[`Catalog`](../../internal/admin/route_catalog.go), extended by
[`v1WriteCatalog`](../../internal/admin/route_catalog_v1_write.go).
It includes 91 registered route patterns at the reviewed baseline, including
per-method permissions and logical-OR grants. Public routes and the
authenticated-only identity route declare their authorization mode explicitly.

[`TestRouteTransportPolicyInventory`](../../internal/admin/route_classification_test.go)
declares a transport policy for each pattern and requires exact-set equality
with the complete, initialized catalog. `/healthz` and `/readyz` are probe
exemptions; all other patterns require TLS or loopback, including the public
Console shell. Every declared method is exercised through `Server.routes()`:

- Non-probe requests on a non-loopback cleartext connection fail before auth.
- Secure requests without credentials fail authentication on protected routes.
- Valid RBAC credentials with no permissions fail authorization, except on
  the explicitly authenticated-only identity endpoint.

Existing `TestCatalog*` guards require exactly one authorization declaration,
known permissions, complete per-method grants, handlers and methods, unique
patterns and approved public routes. `TestCatalogOwnsMuxRegistrations` parses
production files, including optional build profiles, and rejects direct
`Handle`/`HandleFunc` registrations outside the one catalog registration.
This is a source-structure guard, not a general proof against arbitrary
future dispatch mechanisms or malicious code changes.

Both the dedicated [security workflow](../../.github/workflows/security-gates.yml)
and [`make security-gates`](../../scripts/security-gates.sh) run these guards
in lean and full profiles. Existing security-package floors are unchanged.

## Reviewed boundary checklist

Verdicts below apply only to the named boundaries inspected in this installment.
"Pass with limitation" is not closure of the broader line-level review.

| Checklist item | Verdict | Source and observed evidence | Limitation / remaining review |
| --- | --- | --- | --- |
| Every API/Console HTTP route: permission and transport | Pass | Catalog, v1 extension, `routes.go`, `rbac.go`, `transport_gate.go`; route inventory and catalog tests pass in lean/full profiles | UI action-to-endpoint completeness still needs a full action inventory |
| TLS-or-loopback exposure and browser origin assumptions | Pass with limitation | `transport_gate.go`, `origin_guard.go`; package transport/origin regressions pass | Server refusal cannot undo transmission of a credential over an insecure network; native non-loopback TLS deployment evidence remains separate |
| Token comparison, storage, scopes and lifecycle | Pass with limitation | `rbac.go`, `internal/rbac/policy.go`, `role.go`; `ui/src/api/client.ts`, `AuthGate.tsx`, `PermissionProvider.tsx`; constant-time legacy comparison, hashed RBAC credentials, disabled/expired-principal checks, per-tab session storage | Interactive token mint/revoke is not implemented; configuration rotation is the existing lifecycle. Session storage is not protection against same-origin script compromise |
| Apply/stage/rollback/upload/purge/adoption authorization | Pass with limitation | Catalog permissions and `rbac.go`; no-grant route matrix and existing `wave1_security_test.go` regressions pass | Full line review of mutation handlers, coordinator interactions and every scoped-role combination remains open |
| Principal audit attribution | Pass with limitation | `audit.go`, `route_audit_identity.go`; identity is obtained from authenticated request context, never request-supplied principal fields | Complete operation-to-audit emission coverage remains to be inventoried; legacy token attribution is shared, not per-user |
| Plugin upload size/path/reload boundary | Pass with limitation | `plugin_upload.go`, `runtime_snapshot.go`; captured request policy, bounded multipart/body reads, confined `os.Root` writes, owner-only temp files, atomic rename; upload regressions pass | Magic/version screening is not complete module validation; execution uses the separate plugin runtime/content-identity authority. Full identity/reload interaction review remains open |
| Support bundle redaction and size bounds | Pass with limitation | `supportbundle/generator.go`, `collectors.go`; collector and archive tests, including extracted-archive secret scans, pass | Local operator-generated bundle, not a new admin API; collector timeouts are cooperative and sanitization is not a guarantee that all business-sensitive identifiers disappear |
| Secret-safe error/log projections | Pass with limitation | `redact/redact.go`, `audit.go`, support-bundle sanitization and existing admin projection regressions | Redaction depends on registered values and minimum secret length; exhaustive handler/error-path review remains open |
| pprof enabled plus admin:manage | Pass with limitation | Catalog's `/debug/pprof/` entry, `runtime_snapshot.go`, pprof and operator/admin regressions pass | Profiles are sensitive even for authorized users; request-versus-live policy selection during reload needs further review |
| Admission and rate limiting | Pass with limitation | `ratelimit.go`, `routes.go`; transport-peer bucket keys, generation-pinned policy, admission before auth, SSE lease limits; package regressions pass | Limits are per peer and can be disabled; this review does not establish a process-global cap on peer bookkeeping |
| Browser CSP, clickjacking and cache behavior | Pass with limitation | `server.go`, `apiv1.go`; strict SPA CSP, nonce-bearing no-store shell, frame denial, external API no-store; Console tests pass | Legacy GUI allows first-party inline scripts/styles. Cache headers and DOM/error handling for every internal response/action still need exhaustive review |
| #501/#502 shared access-log sinks | Pass with limitation | All of `observability/sinks.go` and `sinks_policy_test.go`; `TestAccessFilePolicyChangeSharesOneWriter`, `TestAccessFilePolicyChangeRotationDuringDrain` pass | Canonicalization is absolute-path based, not filesystem-identity based; trusted operators must avoid aliases/multiple processes targeting the same log file. Retention/pruning remains lumberjack policy |

The support-bundle path is local/offline; adding a remote support endpoint is
**not applicable** to this installment. No new token-management API or plugin
runtime surface was added.

## Findings and disclosure

No exploitable finding has been established by this targeted installment and
no new public issue or private advisory has been filed. This is not a claim
that the unreviewed surface is free of defects. Any concrete security finding
in the remaining review must follow [SECURITY.md](../../SECURITY.md), which
requires private reporting rather than a public issue. Existing #502 stays
closed; the reviewed tests do not demonstrate a new rotation regression.

## Executed focused checks

```sh
go test ./internal/admin -run '^(TestRouteTransportPolicyInventory|TestCatalog)' -count=1
go test -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./internal/admin -run '^(TestRouteTransportPolicyInventory|TestCatalog)' -count=1
go test -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./internal/admin ./internal/adminapi ./internal/rbac ./internal/supportbundle ./internal/redact ./internal/observability -count=1
make security-gates
make console-check
make ci-fast
make ci-pr
make generated-check
python3 scripts/docs-check.py
python3 scripts/test_docs_check.py
python3 scripts/test_semantic_drift.py
```

These checks passed on the installment's source tree. Console: 75 files, 820
tests; docs: 1,911 checks, zero failures. `ci-pr` includes full-tag lint/tests,
vet, configuration-example validation, security floors and generated checks.
Govulncheck reported no reachable vulnerability; it also reported a non-reachable
package/module vulnerability. Exact-head CI disposition is recorded in the PR,
not inferred from prior main checks. No generated authority changed and all
generated projections verified unchanged. This test-only change adds no runtime
concurrency or protocol behavior; full runtime race/conformance reruns are not
claimed as local evidence for it.

## Remaining acceptance work

- Finish the line-level review and action inventory across the complete #514
  scope, including mutation/coordinator and plugin-content identity boundaries.
- Establish final verdicts for the limitations above and route any concrete
  findings under the security disclosure policy.
- Link exact-head CI, merge and final evidence on #514 and #62 before closure.
- Keep #517/#527 and all other candidate issues unactivated; no release or soak
  checkpoint is certified by this installment.