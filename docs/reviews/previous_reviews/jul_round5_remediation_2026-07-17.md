# Round 5 security/reload-transaction audit — remediation tracking

> **COMPLETED (historical archive) — 2026-07-17.** This remediation is complete and merged to `main`. The burn-in reload soak checkbox remains open pending a soak run; all other definition-of-done items are satisfied. This document is retained under `previous_reviews/` for history and is **no longer maintained**.

**Report date:** 2026-07-16
**Remediation plan date:** 2026-07-17
**Repository:** victornife/jul
**Branch:** main
**Head at planning:** 1cacc5c6bd130319e3fc61e9bcca3173f3ab95b8

## Summary

Round 5 confirmed that the reload path still spans multiple independently mutable states without a single authoritative transaction object. The agreed remediation is to introduce a `ReloadPlan` value and a `LifecycleRegistry` as the single source of truth for restart-required classification. This document maps every Round 5 finding to the concrete code change that resolves it and tracks status.

## Status legend

- `PLANNED` — design complete, no code landed
- `IN PROGRESS` — branch/open PR
- `DONE` — merged to main
- `N/A` — not applicable or already satisfied

## Findings → remediation mapping

| ID | Finding | Root cause | Resolution | Status | Primary files | Phase |
|----|---------|------------|------------|--------|---------------|-------|
| R5-01 | Secret resolution mutates global redaction state before commit. | `config.ExpandSecrets` calls `redact.Replace`/`redact.SetMinLen` during validation/preparation. | Replace with `secrets.Resolve(cfg)` returning `redact.State`; install only in `ReloadPlan.Publish`. | DONE | `internal/config/secrets.go`, `internal/redact/redact.go`, `internal/server/server.go` | 1 |
| R5-02 | Startup-bound secret-content rotation bypasses restart detection. | Restart checks compare raw config references, not consumed secret bytes. | Include file-backed secret digests and env-var sentinels in `lifecycle.Fingerprint`; compare effective values. | DONE | `internal/config/secrets.go`, `internal/lifecycle/fingerprint.go`, `internal/server/listener_fingerprint.go` | 2 |
| R5-03 | Secret-backed mTLS paths compare expanded bound state to raw candidate state. | `listener_fingerprint.go` mixes expanded and unexpanded path values. | Compute fingerprints only from expanded effective config; compare like-for-like. | DONE | `internal/server/listener_fingerprint.go`, `internal/lifecycle/fingerprint.go` | 2 |
| R5-04 | HTTP/3 starts during uncommitted listener-staging phase. | `buildListenerEntry` immediately started the QUIC accept loop. | Introduce staged `h3Listener.Activate()`; defer `Serve` until after Publish. | DONE | `internal/server/http3.go`, `internal/server/http3_seam.go`, `internal/server/http3_stub.go` | 4 |
| R5-05 | Resource/listener activation precedes handler publication. | `doReload` activated listeners before swapping `s.handlers`. | Reorder: Publish (handlers + pools) → Activate (TCP/QUIC listeners); `startServing` gates TCP serve and h3 activation. | DONE | `internal/server/server.go`, `internal/server/reload_plan.go` | 3–4 |
| R5-05 | In-flight requests can observe backends from a newer/removed generation. | Reused upstream pools are updated in place at `Registry.Commit`, visible to old-generation requests. | Generation-scoped `PoolSnapshot` captured at commit and injected into request context; `PickCtx`/`BackendsCtx` prefer snapshot. | DONE | `internal/upstream/snapshot.go`, `internal/app/factory.go`, `internal/handler/proxy.go` | 5 |
| R5-06 | Admin preflight omits log-format restart-required gate. | `Preflight.Apply` uses an incomplete hard-coded gate list. | Use `lifecycle.RestartRequired` and include `global.log_format` in the registry. | DONE | `internal/admin/preflight.go`, `internal/lifecycle/registry.go` | 2, 6 |
| R5-07 | ACME startup fingerprint omits cache_dir and ocsp_stapling. | `acmeFingerprint` builds a struct manually and misses fields. | Include `cache_dir` and `ocsp_stapling` in the effective startup fingerprint via `LifecycleRegistry`. | DONE | `internal/server/tls.go`, `internal/lifecycle/registry.go` | 2 |
| R5-08 | Numeric worker_threads cannot return to auto. | `OnReloaded` skips `worker_threads = auto` and does not track previous numeric cap. | Resolve `auto` to effective GOMAXPROCS cap; include in fingerprint; restore previous cap on rollback to auto. | DONE | `internal/app/serve.go`, `internal/lifecycle/fingerprint.go` | 2 |
| R5-10 | Structured diff cannot guarantee completeness. | `diffConfigs` falls back to string comparison and has ad-hoc field lists. | Drive diff from `lifecycle.Registry` so it is complete by construction. | DONE | `internal/admin/diff.go`, `internal/lifecycle/diff.go` | 6 |
| R5-11 | E2E proves persistence, not serving behavior. | `real-server.spec.ts` only checks config history API. | Extend E2E to assert traffic serving/handling after reload and after a rejected reload. | DONE | `internal/admin/ui/e2e/real-server.spec.ts`, `internal/admin/projections.go`, `internal/admin/ui/playwright.config.ts` | 7 |
| R5-12/R5-13 | Feature-status/lifecycle validation is presence-based. | `scripts/docs-check.py` checks that files exist rather than semantic consistency. | Validate `docs/config-lifecycle.yaml` against the Go registry and require non-empty reasons. | DONE | `scripts/docs-check.py`, `internal/lifecycle/diff.go`, `docs/config-lifecycle.yaml` | 6 |
| R5-14 | Documentation contradicts implementation. | `docs/reload-semantics.md` and `docs/status.md` describe ordering that does not match current code. | Rewrite reload semantics to match the new transaction; update status/known-limitations. | DONE | `docs/reload-semantics.md`, `docs/status.md`, `docs/known-limitations.md`, `docs/feature-status.yaml`, `docs/roadmap/README.md` | 6 |

## Architecture work not tied to a single finding

| Work | Why it matters | Status | Primary files | Phase |
|------|----------------|--------|---------------|-------|
| `ReloadPlan` transaction object | Centralizes every candidate resource and makes abort safe. | DONE | `internal/server/server.go`, `internal/server/reload_plan.go` | 3 |
| `LifecycleRegistry` single source of truth | Removes duplicated restart-required lists across preflight, direct reload, Console, and docs. | DONE | `internal/lifecycle/registry.go` | 2 |
| Generation-scoped upstream pool snapshot | Guarantees in-flight requests never observe backends from a newer/removed generation. | DONE | `internal/upstream/snapshot.go`, `internal/app/factory.go`, `internal/handler/proxy.go`, `internal/handler/grpcproxy.go`, `internal/transcode/invoke.go` | 5 |
| TCP + HTTP/3 listener activation barrier | Prevents serving connections before handler publication. | DONE | `internal/server/server.go`, `internal/server/http3.go`, `internal/server/http3_seam.go` | 4 |

## Sequencing and phasing

| Phase | Weeks | Goal | Key deliverables | Findings addressed |
|-------|-------|------|------------------|-------------------|
| 0 | 1–2 | Land planning and skeletons | ADR 0011, design spec, task breakdown, `lifecycle` package skeleton, `redact.State` feature flag | — |
| 1 | 2–3 | Pure secret resolution | `secrets.Resolve`, `redact.State`, remove global redaction mutation | R5-01 |
| 2 | 2 | Lifecycle registry + fingerprints | `LifecycleRegistry`, effective startup fingerprint, ACME fix, worker_threads auto fix, log_format gate | R5-02, R5-03, R5-06, R5-07, R5-08 |
| 3 | 2–3 | ReloadPlan skeleton | `ReloadPlan`, handler/pool staging, abort semantics, Publish boundary | R5-05 (partial) |
| 4 | 2 | Listener + HTTP/3 staging | TCP barrier, staged HTTP/3, activation after Publish | R5-04, R5-05 |
| 5 | 1–2 | Pool snapshot isolation | Generation-scoped pool snapshot embedded in request context | R5-05 (complete) |
| 6 | 1–2 | Admin + docs governance | Preflight uses Resolve+Plan, registry-driven diff, docs-check validation, reload-semantics rewrite | R5-06, R5-10, R5-12, R5-13, R5-14 |
| 7 | 2–3 | Hardening + E2E | Soak reload stress, fuzz transitions, E2E serving assertions | R5-11, all |

**Total estimated effort:** 10–16 engineer-weeks.

## Cross-cutting architecture recommendation

The `### Cross-cutting architecture recommendation` from the audit — introduce a single `ReloadPlan` value and a `LifecycleRegistry` — is implemented across phases 1–6. It is not a standalone phase because it is the organizing principle of the entire remediation. The earliest pieces that must land to unblock downstream work are:

1. `internal/lifecycle` registry and fingerprint types (phase 0/2).
2. `internal/redact.State` (phase 0/1).
3. `ReloadPlan` type and `Server.doReload` refactor (phase 3).

Everything else builds on those three foundations.

## Definition of done for the whole remediation

- [x] All PLANNED items above are DONE.
- [x] `go test ./...` passes, including new reload-transaction tests.
- [ ] Burn-in reload soak passes without redaction leaks, listener races, or HTTP/3 crashes.
- [x] Admin Console E2E asserts both persistence and serving behavior.
- [x] `scripts/docs-check.py` enforces lifecycle-registry consistency.
- [x] `docs/reload-semantics.md` accurately describes the implemented behavior.

## References

- [docs/adr/0011-reload-plan.md](../../adr/0011-reload-plan.md)
- [docs/specs/reload-plan.md](../../specs/reload-plan.md)
- [docs/specs/reload-tasks.md](../../specs/reload-tasks.md)
- [docs/reviews/jul_full_repository_audit_2026-07-09.md](../jul_full_repository_audit_2026-07-09.md)
