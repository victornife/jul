<!-- Engineering execution spec. Detailed source-of-truth for the Year 3 — Scale, Fleet & Ecosystem roadmap entry.
     Companion to ../roadmap/ and ../vision/. Update when a feature's design changes. -->

# JUL Engineering Execution Plan — Year 3 (Scale, Fleet & Ecosystem — open-core monetization)

> Version 1.0 · Updated 2026-06-21 · **Concept horizon — not committed.**
> This document captures long-term technical possibilities, not a scheduled
> delivery plan. It enters the operating roadmap only when its evidence gate
> trips (see [ADR 0003](../adr/0003-maturity-and-ga.md)). All implementation
> details must be revalidated before work starts.

Goal: take JUL from single-node platform to FLEET-SCALE + ENTERPRISE-READY + K8S-NATIVE. This is where open-core monetization lands. Headlines: (A) multi-node control plane (fleet config sync + staged rollout), (B) Kubernetes Ingress + Gateway API, (C) Console RBAC + SSO/SAML. Plus distributed cache/rate-limit, traffic management, hot binary upgrade, plugin marketplace, audit logging.
Exit: manage N nodes from one control plane w/ staged rollout+rollback; run as a K8s ingress controller at scale; RBAC+SSO gate the Console; distributed cache+rate-limit across nodes; canary/blue-green/mirroring; signed-plugin marketplace; tamper-evident audit log.

Open-core note: most Y3 features are ENTERPRISE/open-core (control plane, RBAC+SSO, distributed state, K8s-at-scale, audit). Add a licensing seam: internal/license (feature gate by signed license token); OSS build = single-node + community features; enterprise build tag `enterprise` enables gated features. Keep OSS fully functional single-node.

> Historical design notes: earlier drafts assigned squad names and quarter
> schedules. Those have been removed; Git history preserves them if needed.
> What remains are the target problems, possible scope, architectural
> hypotheses, risks, non-goals, and evidence gate.

Build tags added Y3: enterprise (license-gated), redis (distributed tiers — could be core dep, tag to stay lean), saml. K8s reuses `kubernetes` tag from Y2. controlplane under `enterprise`.

## Y3-01 Multi-node control plane + config sync + staged rollout — SQ-FLEET P0 XL (enterprise)
- Objective: one control plane manages a fleet of JUL data-plane nodes: central config, push w/ validation, STAGED rollout (canary cohorts), health-gated promotion, one-click fleet rollback. Out: full service mesh xDS (Y5), cross-region (Y5 GSLB).
- Design: NEW component `jul control-plane` (same binary, subcommand) exposing a gRPC+REST API + a HYBRID embedded store behind ONE Store interface (engine swappable): bbolt = desired-state SOURCE OF TRUTH (config-version blobs, node registry, cohort assignment, current-assignment pointers; hot rollout path, simple keyed access); modernc.org/sqlite (pure-Go/CGO-free) = query-heavy history (audit FTS5, fleet rollout history, RBAC users/roles/tokens w/ JOINs). Both pure-Go -> single binary preserved. Eventual consistency (bbolt=truth, SQLite=derived/observational; no cross-store ACID). OSS single-node degrades to bbolt-only. CP HA = single CP active-passive in Y3 (raft deferred to stretch/Y4). Each data-plane node runs an agent (goroutine in existing process) that connects out to the CP (mTLS, reuse Y2-07), long-poll/stream for assigned config version, applies via the EXISTING validate-before-apply + atomic reload (admin handleConfigRaw path is the seam — reuse LoadConfig/WriteConfigRaw/Reload from admin.Deps). Staged rollout: CP assigns config vN to cohort A (e.g. 10%), watches node health/metrics (nodes push status: applied/failed/healthy via existing /metrics + readyz), auto-promotes to cohort B/C or auto-rolls-back on error budget breach. Node identity+enrollment via bootstrap token -> mTLS cert (CP acts as a tiny CA or uses Y1-01 ACME-internal). Lock-free reload already exists per-node; CP just orchestrates which version each node runs.
- Config: NEW [control_plane] (server role): listen, store_path, ca, rollout{cohorts,health_gate,error_budget,auto_promote,promote_interval}. NEW [agent] (node role): control_plane_addr, enroll_token, node_id, labels (for cohort selection). Both behind `enterprise`.
- New: cmd/jul/main.go subcommands control-plane|agent; internal/controlplane/{server.go,store.go,rollout.go,ca.go,api.go}; internal/agent/{agent.go,sync.go}; reuse admin.Deps reload seam. Tag enterprise.
- Tasks: CP store (versioned configs, node registry, cohort assignment) -> node enrollment + mTLS -> agent connect+stream desired version -> agent applies via existing validate+reload -> node status reporting -> staged rollout state machine (cohort -> health gate -> promote/rollback) -> fleet rollback (pin all nodes to vN-1) -> CP REST/gRPC API for Console v3 -> jul_cp_nodes{cohort,state}, rollout_status, config_version gauges.
- Deps: grpc (reuse), bbolt (operational/desired-state store) + modernc.org/sqlite (pure-Go CGO-free, query/history store), mTLS (Y2-07), both behind a Store interface. Inter: Y3-02 RBAC gates CP API; Y3-09 fleet UI; Y3-08 audit every CP action; reuse single-node reload.
- Tests: unit (rollout state machine: promote on healthy, rollback on breach; cohort selection by label; version store); integration (3-node fleet in-process: push config -> staged rollout -> one node fails validation -> auto rollback; fleet rollback); chaos (node disconnect/reconnect resync); goleak.
- DoD: enroll nodes; push a config version; staged rollout across cohorts with health gating; auto-rollback on failure; manual fleet rollback to prior version; node reconnect resyncs to assigned version; every action audited; CP API authz'd.
- Risks: split-brain/config drift (CP is single source; node reports actual; reconcile loop), CP availability (HA: 2-3 CP w/ raft later, Y3 ship single CP + warn; or back store w/ raft via hashicorp/raft — flag as stretch), enrollment security (short-lived tokens + mTLS), large fleets (stream + backoff). 
- Rollout: tag enterprise; OSS stays single-node; CP+agent opt-in.
- Docs: README Fleet/Control plane, docs/control-plane.md (enroll, rollout, rollback, HA notes), examples/fleet, server.toml [control_plane]+[agent], CHANGELOG.

## Y3-02 Console RBAC + SSO/SAML + OIDC — SQ-CONSOLE P0 L (enterprise)
- Objective: replace single Bearer token with multi-user auth: OIDC + SAML SSO, role-based access (viewer/operator/admin), per-resource scoping, API tokens w/ scopes. Out: fine-grained ABAC (later).
- Design: extend internal/admin auth() (currently subtle.ConstantTimeCompare Bearer) into an auth layer: (1) session login via OIDC (Authorization Code+PKCE) or SAML; (2) issue signed session cookie/JWT; (3) RBAC middleware maps identity->roles->permission check per route; (4) API tokens (scoped PATs) for automation. Roles: viewer (GET), operator (reload, cache purge, traffic ops), admin (config write, RBAC, plugins, fleet). Store users/roles/tokens in CP store (Y3-01) or local bbolt for single-node. Keep loopback+token as a fallback/bootstrap. Gate ALL mutating admin + CP endpoints.
- Config: AdminConfig += Auth{mode token|oidc|saml, oidc{issuer,client_id,client_secret,redirect_url}, saml{idp_metadata,sp_cert,sp_key}, session{secret,ttl}, rbac{role bindings: subject->role}}. Tag enterprise (OSS keeps token).
- New: internal/admin/{auth_oidc.go,auth_saml.go,rbac.go,tokens.go,session.go}; user/role store; gate routes. Tags: enterprise, saml.
- Tasks: OIDC AuthCode+PKCE flow -> SAML SP (ACS, metadata) -> session cookie/JWT signing+verify -> role model + permission matrix -> RBAC middleware per route -> scoped API tokens (create/revoke) -> bootstrap admin + token fallback -> bind into CP API authz -> jul_admin_auth_total{method,result}, sessions gauge.
- Deps: coreos/go-oidc + x/oauth2 (OIDC), crewjam/saml (SAML, tag saml). Inter: Y3-01 CP authz; Y3-08 audit user actions; Y3-09 RBAC admin UI.
- Tests: unit (RBAC matrix per role/route; token scope; session verify/expire); integration (OIDC against mock IdP -> login -> role enforced; SAML against mock IdP; viewer blocked from write; token scopes); security (no-auth bypass, CSRF on session, alg pinning).
- DoD: OIDC+SAML login work against test IdPs; roles enforced on every route (viewer/operator/admin); scoped API tokens; CP endpoints authz'd; bootstrap path documented; token fallback for OSS/break-glass.
- Risks: SSO misconfig lockout (break-glass token), session security (CSRF, secure cookies, alg pin), SAML complexity (lean on lib), IdP variance. 
- Rollout: tag enterprise; OSS = token; SSO opt-in.
- Docs: README RBAC/SSO, docs/auth-sso.md (OIDC+SAML setup, roles table, break-glass), CHANGELOG.

## Y3-03 Distributed cache + distributed rate limit (Redis/gossip) — SQ-SCALE P1 XL (enterprise)
- Objective: shared cache + shared rate-limit across nodes so a fleet behaves as one. Out: strong consistency (eventual is fine).
- Design (cache): add redisStore implementing the EXISTING cache `store` interface { get;set;del;purge } as a remote tier between mem and disk (or replacing disk in clustered mode). Cache becomes mem(L1) -> redis(L2 shared). Distributed purge via Redis pub/sub (extend existing admin /cache/purge to publish; all nodes subscribe+evict). Tag/pattern purge. Design (rate limit): replace/augment Y1-03 in-memory sharded limiters with a distributed token bucket using Redis (atomic Lua INCR+EXPIRE, or sliding window). Falls back to local on Redis outage (fail-open configurable). Alternative gossip backend (hashicorp/memberlist) for rate-limit counters without Redis — offer Redis first, gossip as P2.
- Config: CacheConfig += Distributed{backend none|redis, redis{addr,password,db,tls}, purge_channel}. RateLimitConfig += Distributed{backend none|redis|gossip, redis{...}, fail_open bool}.
- New: internal/cache/redisstore.go + pubsub purge; internal/middleware/ratelimit_redis.go (or a Limiter interface w/ local|redis impls); refactor Y1-03 to a Store interface. Tag redis.
- Tasks: redisStore (get/set/del/purge via SET/GET/DEL + TTL from Entry.ExpiresAt) -> wire as L2 tier -> pub/sub distributed purge (publish on write/purge, subscribe+evict) -> Limiter interface {Allow(key)} local+redis -> redis Lua token bucket -> fail-open on outage -> gossip backend (P2) -> jul_cache_redis_ops_total, ratelimit_backend_errors_total.
- Deps: redis/go-redis/v9 (tag redis), hashicorp/memberlist (P2 gossip). Inter: Y1-03 refactor; Y3-01 fleet (nodes share backend); Y3-09 shows distributed stats.
- Tests: unit (redisStore round-trip via miniredis, Lua bucket math, fail-open); integration (2 nodes share cache: node A caches, node B HIT; purge on A evicts B; distributed rate-limit caps across 2 nodes); resilience (redis down -> cache bypass + rate-limit fail-open).
- DoD: cache shared across nodes (cross-node HIT); distributed purge evicts all nodes; rate-limit enforced globally across fleet; graceful degradation when backend down; mem L1 still accelerates.
- Risks: redis as SPOF (fail-open + optional HA redis), latency added to hot path (L1 mem absorbs; async write-behind option), cache stampede (single-flight already? add lock), consistency (eventual; document). 
- Rollout: tag redis; backend=none default (OSS local); distributed opt-in.
- Docs: README Distributed cache+rate-limit, docs/clustering.md, testdata, CHANGELOG.

## Y3-04 Kubernetes Ingress Controller + Gateway API + Helm — SQ-K8S P0 XL (cloud adoption channel)
- Objective: run JUL as a first-class K8s ingress controller: watch Ingress + Gateway API, translate to JUL config, hot-apply; ship a Helm chart. Out: full mesh (Y5).
- Design: NEW controller mode `jul ingress-controller` (subcommand) using client-go informers (reuse `kubernetes` tag from Y2-05) to watch Ingress, IngressClass, Gateway API (GatewayClass/Gateway/HTTPRoute/GRPCRoute), Services, EndpointSlices, Secrets (TLS). Translate watched objects -> in-memory *config.Config -> apply via the EXISTING validate + atomic reload (no file). Status writeback (Ingress .status.loadBalancer, Gateway/Route conditions). TLS from K8s Secrets feeds a CertProvider variant (reuse Y1-01 seam). Leverages Y2-05 EndpointSlice discovery for backends and gRPCRoute -> Y2-01/Y2-04. Leader election (client-go lease) for HA controllers. Helm chart + CRD-less for Ingress; Gateway API CRDs documented.
- Config: [ingress_controller]: ingress_class, gateway_api bool, namespaces[], leader_election, default_backend. Mostly K8s-driven (annotations: jul.io/* for features: rate-limit, auth, canary).
- New: internal/k8s/{controller.go,translate_ingress.go,translate_gateway.go,status.go,leader.go,secrets.go}; cmd subcommand. Tag kubernetes.
- Tasks: informers for Ingress/Gateway/Service/EndpointSlice/Secret -> ingress->config translator (paths, tls, annotations->features) -> Gateway API translator (HTTPRoute/GRPCRoute matchers->locations) -> Secret->CertProvider -> apply via validate+reload -> status writeback -> leader election -> annotation feature mapping (auth/ratelimit/canary/waf reuse Y1/Y2) -> Helm chart + RBAC + example manifests -> jul_ingress_objects{kind}, reconcile_errors_total.
- Deps: k8s.io/client-go, k8s.io/api, sigs.k8s.io/gateway-api. Inter: Y2-05 EndpointSlice; Y1-01 cert seam; Y3-05 canary via annotations; Y3-01 (CP can manage controllers).
- Tests: unit (translator: Ingress->config, HTTPRoute->location, annotation map); integration (envtest/fake clientset: create Ingress -> JUL serves it; update -> reload; Gateway API route; TLS secret); e2e kind cluster (smoke) in CI optional.
- DoD: watches Ingress + Gateway API; translates to working JUL config applied without restart; TLS from Secrets; status written back; annotations enable JUL features; leader election for HA; Helm install works; conformance subset (Gateway API) passes.
- Risks: Gateway API churn (pin version), client-go bloat (tag), conformance scope (document subset), reconcile storms (rate-limit+resync). 
- Rollout: tag kubernetes; separate controller mode; OSS-usable but "at scale" features (multi-namespace, fleet) enterprise.
- Docs: README K8s ingress + Gateway API, docs/kubernetes.md, helm/jul chart + README, examples/k8s manifests, CHANGELOG.

## Y3-05 Traffic management (canary, blue-green, weighted split, mirroring) — SQ-PROXY P1 L
- Objective: progressive delivery at the proxy: weighted traffic split across upstream versions, canary by header/cookie/percentage, blue-green switch, request mirroring (shadow). Out: automated analysis/rollback (that's Y3-01 rollout for config; here it's request routing).
- Design: extend router + upstream. A location can reference MULTIPLE upstreams with weights (split) or rules. Add a routing layer before backend selection: match (header/cookie/percentage) -> choose upstream variant. Weighted split reuses weightedRR concept but across upstreams. Blue-green = 100/0 then flip (config or admin API toggle, hot reload). Mirroring = after serving primary, async duplicate the request to a shadow upstream, discard response (clone body via buffering up to limit). Sticky canary via cookie. Integrate with Console v3 + K8s annotations (Y3-04).
- Config: LocationConfig += Traffic{split[]{upstream,weight}, canary{upstream,match{header,cookie,percentage},sticky bool}, mirror{upstream,sample_ratio}}. Or new ActionSplit. Keep single-action rule by making traffic a modifier on proxy.
- New: internal/router/traffic.go (variant selector); handler/proxy.go mirror hook; admin API for blue-green flip. Core.
- Tasks: multi-upstream weighted selector -> percentage/header/cookie canary matcher -> sticky cookie -> blue-green flip via admin API + reload -> async mirror (buffered body clone, fire-and-forget, isolate failures) -> metrics jul_traffic_variant_total{location,variant}, mirror_total.
- Deps: stdlib. Inter: upstream Pool/Balancer; Y3-04 annotations; Y3-09 traffic UI; Y3-01 CP can orchestrate fleet-wide canary.
- Tests: unit (weight distribution ~ correct over N; canary match; sticky; mirror sampling); integration (split 90/10 observed; header canary routes; blue-green flip live; mirror hits shadow w/o affecting client); mirror failure isolated.
- DoD: weighted split matches configured ratios; canary by header/cookie/percentage w/ stickiness; blue-green flips without dropping connections; mirroring shadows traffic without affecting client latency/errors; observable per variant.
- Risks: mirror load on shadow (sample + isolate), body buffering cost (limit + skip large), weight precision, sticky cookie privacy. 
- Rollout: core; per-location opt-in.
- Docs: README Traffic management, docs/traffic-management.md, testdata, examples/canary, CHANGELOG.

## Y3-06 Hot binary upgrade (zero-downtime, socket handoff) — SQ-CORE P2 M
- Objective: upgrade the JUL binary with zero dropped connections via listener handoff + graceful drain. Out: Windows parity (document limitation; Windows uses service restart w/ overlap).
- Design (unix): on SIGUSR2, fork+exec the new binary passing inherited listener FDs (via ExtraFiles + env, like tableflip/overseer). New process resumes accepting on the same sockets; old process stops accepting and drains in-flight (reuse existing graceful drain in server.go). Coordinate via a PID/handoff protocol. Reuse internal/signals (already unix/windows split) to add SIGUSR2 handler on unix. Existing atomic reload handles config; this handles the BINARY. Windows: no fork/exec FD inheritance -> use SCM service restart with SO_REUSEADDR overlap or a brief connection-draining proxy; document as not-zero-downtime on Windows (or use a small supervisor).
- Config: GlobalConfig += hot_upgrade bool (unix). Mostly signal-driven; CLI `jul upgrade` triggers SIGUSR2.
- New: internal/upgrade/{handoff_unix.go,handoff_windows.go}; signals_unix.go += SIGUSR2; server.go expose listener FDs. Build-constrained by GOOS.
- Tasks: export listener fds from server bind -> SIGUSR2 handler fork+exec w/ ExtraFiles+env -> child detects inherited fds + resumes accept -> parent stop-accept + drain + exit -> handoff readiness handshake (child signals ready) -> rollback if child fails to start -> `jul upgrade` CLI -> jul_upgrades_total, drain_duration.
- Deps: prefer stdlib (os/exec, net FileListener); optionally cloudflare/tableflip (unix). Inter: server.go drain; signals package.
- Tests: unit (fd export/inherit on linux); integration (linux: start, hold a long request, SIGUSR2 -> new binary serves new conns, old drains the long request, no drop); Windows path documented+tested as service restart.
- DoD (unix): SIGUSR2 hands off listeners to a new process w/ zero dropped connections; in-flight requests drain; child-start failure rolls back to old; CLI triggers it. Windows: documented graceful-restart fallback works.
- Risks: fd inheritance correctness, child crash mid-handoff (keep parent until child ready), Windows lack of fork (document + supervisor option), config compatibility across versions. 
- Rollout: unix only (build-constrained); off by default; Windows documented alternative.
- Docs: README Zero-downtime upgrade (+Windows note), docs/hot-upgrade.md, CHANGELOG.

## Y3-07 Plugin marketplace + signed modules (jul add) — SQ-PLUGINS P2 L
- Objective: discover, verify, and install WASM plugins (Y2-02) from a registry with signature verification. Out: paid plugin billing (Y5 cloud).
- Design: a plugin registry (OCI registry or a simple HTTPS index + object store) hosting .wasm + signed manifests. `jul add <plugin>` downloads, verifies signature (cosign/sigstore or minisign/ed25519), checks declared capabilities (kv/fetch/allowed_hosts) against policy, installs into plugins dir + updates config. Manifest declares name, version, abi (http-wasm|proxy-wasm — both supported per Y2 decision), capabilities, checksum, signature. Console v3 plugin manager (Y2-09) gains a "browse marketplace" tab. Reuse Y2-02 registry to load. Signing keys: project-trusted + allow custom trust roots.
- Config: [plugin_registry]: url, trusted_keys[], verify bool, install_dir. Per-plugin manifest (jul-plugin.toml).
- New: internal/marketplace/{client.go,verify.go,manifest.go}; cmd subcommand `add|search|remove|update`; reuse internal/plugins registry. Core CLI (verify deps lean).
- Tasks: manifest format + parse -> registry client (search/pull) -> signature verify (ed25519/minisign + optional sigstore) -> capability policy check + prompt -> install + config update + reload -> `jul add/search/remove/update` -> Console marketplace tab -> jul_plugins_installed gauge.
- Deps: sigstore/cosign or aead/minisign (lean: ed25519 stdlib + minisign format). Inter: Y2-02 runtime/registry; Y2-09 Console; abi.go (both ABIs).
- Tests: unit (manifest parse, signature verify good/bad, capability policy); integration (add from a local test registry -> verify -> install -> plugin active after reload; reject unsigned/tampered; remove); security (signature bypass, path traversal in install, capability escalation).
- DoD: `jul add` installs a signed plugin and it's active after reload; tampered/unsigned rejected; capabilities surfaced+enforced; remove/update work; both http-wasm and proxy-wasm modules install; Console browse works.
- Risks: supply chain (mandatory signatures + trusted roots), capability escalation (policy + explicit consent), registry availability (cache + checksums), name squatting (namespacing). 
- Rollout: core CLI; registry URL configurable; OSS uses public registry.
- Docs: README Plugin marketplace (jul add), docs/marketplace.md (signing, publishing, capabilities), CHANGELOG.

## Y3-08 Audit logging / compliance — SQ-SEC P1 M (enterprise)
- Objective: tamper-evident audit trail of all admin/CP/RBAC actions for compliance (SOC2/ISO). Out: full SIEM (export to SIEM instead).
- Design: an audit middleware/hook on all mutating admin + CP endpoints capturing who (identity from Y3-02), what (action+resource), when, from where (IP), result, and a config diff for config writes. Append-only log with a hash chain (each entry includes prev-entry hash -> tamper-evident), PERSISTED to the CP SQLite store (Y3-01) in an FTS5 table so the Console v3 audit viewer (Y3-09) searches by user/action/resource/time + full-text. ALSO mirrored to Y1-10 sinks (file/json/syslog) + optional SIEM forward (OTLP logs/webhook) for external retention. Redaction of secrets in diffs. Stored separately from request access logs. Signed/sealed periodically.
- Config: [audit]: enabled, sinks[], hash_chain bool, redact[], forward{webhook|syslog|otlp}.
- New: internal/audit/{audit.go,chain.go,diff.go}; hook into admin + controlplane handlers; reuse Y1-10 sinks. Tag enterprise.
- Tasks: audit event model -> hook on mutating handlers (config write, reload, cache purge, RBAC change, plugin install, fleet rollout) -> identity capture (Y3-02) -> config diff + secret redaction -> hash chain (prev_hash) -> sinks + forward -> verify-chain tool (`jul audit verify`) -> jul_audit_events_total{action,result}.
- Deps: stdlib + Y1-10 sinks. Inter: Y3-02 identity; Y3-01 CP actions; Y1-10 sinks.
- Tests: unit (event capture, diff+redaction, hash chain continuity, tamper detection); integration (each mutating action emits an audit entry w/ identity; chain verifies; broken chain detected); compliance (redaction of secrets verified).
- DoD: every mutating admin/CP/RBAC action audited w/ identity+diff; hash chain tamper-evident + verifiable; secrets redacted; forward to syslog/webhook/OTLP; `jul audit verify` validates chain.
- Risks: PII/secret leakage in diffs (redaction allowlist + tests), log volume (sampling NOT for audit — keep all; rotate+archive), performance (async write). 
- Rollout: tag enterprise; disabled default; recommend on for prod.
- Docs: README Audit logging, docs/audit-compliance.md (event schema, verification, SOC2 mapping), CHANGELOG.

## Y3-09 Console v3 (fleet view, RBAC admin, traffic mgmt UI) — SQ-CONSOLE P1 L
- Objective: extend Console to a fleet control plane UI: multi-node fleet dashboard + staged-rollout control, RBAC/user admin, traffic-management UI, audit-log viewer, distributed-cache stats. Out: AI assistant (Y4).
- Design: build on Console v2 SPA. (1) Fleet view: consume Y3-01 CP API -> node list w/ cohort/version/health, rollout progress + promote/rollback buttons, fleet-wide config editor. (2) RBAC admin: manage users/roles/tokens (Y3-02), login via SSO. (3) Traffic UI: visual canary/split/blue-green/mirror config (Y3-05) w/ live per-variant metrics. (4) Audit viewer: searchable audit log (Y3-08, backed by SQLite FTS5) w/ chain-verify badge — filter by user/action/resource/time + full-text. (5) Distributed stats: redis cache hit, distributed rate-limit (Y3-03). All gated by RBAC (viewer/operator/admin). Connect to CP for fleet, local admin for single-node (Console auto-detects mode).
- Config: reuse admin + control_plane config; Console flag.
- New: web/console fleet/rbac/traffic/audit views; admin/CP API glue. Tags console + enterprise (fleet/RBAC views).
- Tasks: CP API client + fleet dashboard -> rollout control (promote/rollback/cohort) -> RBAC/user/token admin UI -> SSO login flow in SPA -> traffic mgmt visual editor + per-variant charts -> audit log viewer + search + chain badge -> distributed cache/rate-limit panels -> RBAC-gated nav -> Playwright e2e.
- Deps: SPA build; go:embed. Inter: Y3-01,02,03,05,08.
- Tests: unit (CP client, rollout state render); integration (fleet ops authz'd, rollout from UI triggers CP, RBAC hides/blocks, traffic config applies, audit search); e2e Playwright all views w/ roles.
- DoD: fleet dashboard shows nodes+cohorts+versions+health; staged rollout driven from UI w/ promote/rollback; user/role/token admin; SSO login; traffic mgmt configured visually w/ live metrics; audit viewer w/ chain verify; RBAC enforced in UI+API; single binary.
- Risks: CP API coupling/versioning, RBAC UI/API drift (single source of truth = API), real-time fleet scale (paginate+stream). 
- Rollout: tag console; fleet/RBAC views enterprise; single-node Console still works.
- Docs: README Console v3, docs/console.md update (fleet, RBAC, traffic, audit), CHANGELOG.

## Cross-cutting Y3
- Licensing/open-core: internal/license (signed license token, feature gates), `enterprise` build tag; OSS build excludes gated features but stays fully functional single-node; document boundary clearly.
- HA/scale: control-plane HA story (raft stretch or active-passive), redis HA, controller leader election; load/scale tests for fleet (100s of nodes simulated).
- Perf: fleet sync overhead, distributed cache/rate-limit latency, ingress reconcile throughput; CI regression gate.
- Security: control-plane mTLS + enrollment threat model; SSO/SAML security review; audit tamper-evidence; plugin signing supply chain; pen-test enterprise surface.
- Docs (exhaustive, in sync): every feature updates README feature table+config ref, docs/<feature>.md, testdata/*.toml, examples/, helm/, CHANGELOG. DoD blocks merge w/o docs. Add compliance docs (SOC2/ISO control mapping) + enterprise deployment guide.
- Build/release: tag matrix += enterprise, redis, saml; OSS vs enterprise build artifacts; SBOM per artifact; signed releases.

Year 3 DoD: multi-node control plane w/ staged rollout+fleet rollback; K8s ingress + Gateway API + Helm; Console RBAC + OIDC/SAML; distributed cache + rate-limit; traffic management (canary/blue-green/split/mirror); hot binary upgrade (unix) + Windows fallback; signed-plugin marketplace (`jul add`); tamper-evident audit log; Console v3 fleet/RBAC/traffic/audit; open-core licensing seam in place; perf+security gates green; CI green; docs exhaustive incl compliance; OSS single-node + enterprise builds both work.

## Dependency graph (mermaid)
graph LR
  Reload[single-node validate+atomic reload] --> CP[Y3-01 control plane]
  mTLS[Y2-07 mTLS] --> CP
  CP --> CV3[Y3-09 Console v3]
  RBAC[Y3-02 RBAC+SSO] --> CP
  RBAC --> CV3
  RBAC --> Audit[Y3-08 audit]
  CP --> Audit
  Store[cache store iface] --> Dist[Y3-03 distributed cache/RL]
  RL[Y1-03 rate limit] --> Dist
  K8sDisco[Y2-05 EndpointSlice] --> K8s[Y3-04 ingress/gateway]
  CertSeam[Y1-01 CertProvider] --> K8s
  K8s --> Traffic[Y3-05 traffic mgmt]
  Traffic --> CV3
  WASM[Y2-02 plugins] --> Mkt[Y3-07 marketplace]
  Drain[server graceful drain] --> Upg[Y3-06 hot upgrade]

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-21 | 1.0 | Added a version stamp and a **Vision horizon — demand-gated** note; no scope change. | The entire Year-3 spec body. | [review 2026-06-21](../reviews/); [ADR 0003](../adr/0003-maturity-and-ga.md) |
