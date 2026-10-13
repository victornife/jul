# Audit contract follow-ups — #602

The #515 Console/API review reproduced two incomplete #72/#73 contracts: CSV
omitted the public credential ID already present in JSON events, and actual
credential rejections returned 401 without the specified anonymous audit event.
The maintainer's expanded scope selects small reproducible audit defects for
repair. This separate correction depends on #600; it does not reopen historical
programme issues or count either gap as an accepted limitation.

The original Go runtime is main
`616f0ca0ec7ef3f7568ec8b11d0fc1c783df14bc`. The retained baseline regressions
fail the CSV case and all 15 real-route rejection cases; success/403 controls
pass. This evidence is synthetic and contains no live credential.

## Correction and boundaries

A further regression found that open (no-credential) permission middleware
reported `shared`/`(legacy)` audit and managed-history metadata, while the
identity endpoint correctly reported `anonymous`. Audit identity projection now
maps a credential-free compatibility identity to anonymous/empty-ID metadata.
The internal control/idempotency identity and authorization policy are unchanged;
valid legacy credentials and named identities keep their actual attribution.

CSV appends `token_id` after the original eight columns. Their positions stay
unchanged, empty identifiers remain empty, and JSON/CSV agree. Authentication
rejection recording is owned by the common response helper used by all three
authorization entry paths: authenticated-only, single grant and logical-OR
grants. Missing/invalid, disabled/expired and invalid legacy credentials produce
one anonymous `auth.fail` event with a fixed category and socket-peer source.
The helper copies no failed credential, derived failed-token ID, header, query,
body or client-provided actor. Successful authentication followed by 403 does
not produce a false authentication-failure event.

401/WWW-Authenticate and versioned error-envelope semantics are retained. Public
and blocked-policy/transport/admission paths keep their existing behavior. The
outer transport and admission gates precede authentication. Events use the
existing bounded ring and configured size/count-rotated durable sink; writes
are synchronous, can contribute filesystem latency, and do not add per-event
fsync or retroactive operation rollback. No new issuance, revocation, external
identity or token-management capability is activated.

The first implementation attempt covered only one entry path; the regression
exposed the omission. Failed attempt logs are retained separately from the
passing shared-helper implementation.

## Verification

New tests use the real catalog/middleware and durable sink, asserting anonymous
identity, source, one event, absence of synthetic secrets/spoof metadata and
JSON/CSV compatibility. Owning lean/full-tag race/auth/catalog/audit tests and
vet run alongside the existing load-generator regressions. The complete changed
Go diff includes every production file from the cumulative corrective stack,
using the established HR-07C scorer and normal Codecov. Existing package,
global and frontend floors remain unchanged.

[Source/digest manifest and raw evidence](evidence/2026-10-13-audit-contract/)
record exact command outcomes and coverage. A short failure fixture is not
long-soak or independent security certification. NGINX migration is not
applicable: there is no routing or translation change. Final-head hosted checks,
approved integration and #515's actual human spot-check remain required.
