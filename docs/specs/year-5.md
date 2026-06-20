<!-- Engineering execution spec. Detailed source-of-truth for the Year 5 — Global scale, Mesh & Cloud roadmap entry.
     Companion to ../roadmap/ and ../vision/. Update when a feature's design changes. -->

# JUL Engineering Execution Plan — Year 5 (Global scale, Mesh & Cloud — commercial completion)

Goal: complete the OSS -> open-core -> CLOUD ladder and the "global, powerful, still friendly" endgame. Headlines: (A) JUL CLOUD (managed SaaS, STAGED: hosted Console + bring-your-own-nodes BEFORE building anycast network), (B) GSLB (geo-routing + multi-region health-aware failover), (C) Service mesh mode (ambient/sidecar, mTLS east-west, SPIFFE/SVID). Plus bot/DDoS mitigation, RUM + synthetic + SLO tracking in Console, secrets/identity integrations (Vault/KMS/SPIFFE), ecosystem maturity (certification, gallery, learning hub).
Exit: paying JUL Cloud tenants managing their fleets from a hosted Console (BYO nodes); geo-aware multi-region routing+failover; JUL runs as a mesh data plane w/ SPIFFE identities + east-west mTLS; bot/DDoS mitigation live; RUM+SLO in Console; secrets via Vault/KMS; mature ecosystem (certified plugins, templates, docs hub).
REV3 honored: JUL Cloud STARTS THIN (hosted Console + BYO-nodes), build global anycast + usage billing only once demand proven (avoid capital-heavy upfront). Mesh + GSLB are the "most powerful" endgame; friendliness preserved via Console + AI assist (Y4-05) across all of it.

Squads: SQ-CLOUD new (Y5-01 Cloud platform, Y5-08 billing/usage) · SQ-EDGE (Y5-02 GSLB, Y5-09 standards/perf) · SQ-MESH new (Y5-03 mesh mode, Y5-06 secrets/identity) · SQ-SEC (Y5-04 bot/DDoS) · SQ-CONSOLE (Y5-05 RUM+SLO) · SQ-DX/ECO (Y5-07 ecosystem).
Quarters: Q1 Cloud control-plane-as-a-service (multi-tenant CP) + hosted Console, GSLB geo-routing, SPIFFE identity foundation. Q2 BYO-node enrollment to Cloud, mesh east-west mTLS + sidecar, bot/DDoS detection, Vault/KMS secrets. Q3 GSLB multi-region failover, ambient mesh, RUM+synthetic+SLO, usage metering. Q4 Cloud billing (when demand proven), ecosystem (certification/gallery/hub), GA hardening, final perf+security+compliance gate.

Build tags added Y5: cloud (Cloud-agent/tenant features), mesh, spiffe, gslb, botmgmt. Most are enterprise/cloud-gated (reuse Y3 `enterprise` + license seam). Core stays lean.

## Y5-01 JUL Cloud platform (managed SaaS — staged: hosted Console + BYO-nodes) — SQ-CLOUD P0 XL (COMMERCIAL ENDGAME)
- Objective: a multi-tenant hosted service where customers manage their own JUL nodes (bring-your-own infra) from a hosted Console, w/o running their own control plane. STAGE 1 (this year): hosted multi-tenant CP + Console + node enrollment + fleet ops + billing hooks. STAGE 2 (later, demand-gated): JUL-operated anycast PoPs. Out STAGE 2 global network this year.
- Design: promote the Y3-01 single-tenant control plane to MULTI-TENANT, hosted by JUL. Reuse the EXACT Y3 components: controlplane (server.go/store.go/rollout.go), the bbolt+SQLite hybrid store (Y3 decision) now partitioned by tenant_id, agent enrollment (mTLS), staged rollout, Console v3 (Y3-09) + AI assist (Y4-05). Add: tenant model + isolation (row-level tenant_id scoping in SQLite, per-tenant bbolt buckets/namespaces), org/team/user under RBAC (Y3-02 SSO extended to multi-org), per-tenant API tokens, hosted ingress for the Console+CP API (JUL dogfoods itself as its own edge). Customer nodes run the existing agent pointed at cloud.jul.io w/ a tenant enroll token. NO customer data-plane traffic flows through JUL Cloud in stage 1 — only CONTROL traffic (config/telemetry/health) — keeps it light + privacy-friendly. Telemetry: nodes push metrics/health to the tenant's cloud workspace (reuse Prometheus remote-write or OTLP from Y1-10).
- Config: cloud-side [cloud] (tenant partitioning, ingress, auth); node-side [agent] += cloud_mode + tenant_token (extends Y3 agent). Tag cloud + enterprise.
- New: internal/cloud/{tenant.go,isolation.go,enroll.go,workspace.go}; multi-tenant wrappers over internal/controlplane + admin RBAC; telemetry ingestion. Reuse Y3-01/02/09, Y4-05. Tags cloud, enterprise.
- Tasks: tenant model + isolation (SQLite row-scoping + bbolt namespacing) -> multi-org RBAC (extend Y3-02) -> cloud node enrollment (tenant token -> mTLS) -> per-tenant workspace (configs, fleet, telemetry) -> hosted Console+CP behind JUL's own edge (dogfood) -> telemetry ingest (remote-write/OTLP) -> per-tenant quotas + isolation tests -> billing hooks (Y5-08) -> jul_cloud_tenants, nodes_per_tenant, control_rps.
- Deps: reuse Y3 controlplane+store+RBAC, Y4-05 AI assist; remote-write/OTLP (Y1-10). Inter: Y5-08 billing, Y5-05 RUM/SLO surfaced per-tenant, Y3-01/02/09.
- Tests: unit (tenant isolation: no cross-tenant read/write; token scoping); integration (2 tenants, N nodes each, enroll -> staged rollout per tenant -> isolation enforced; telemetry routed to correct workspace); security (cross-tenant access attempts blocked, token forgery, noisy-neighbor quotas); load (many tenants/nodes).
- DoD: multiple tenants manage their own BYO nodes from a hosted Console; full tenant isolation (data + config + telemetry); staged rollout + fleet ops per tenant; SSO multi-org; per-tenant tokens+quotas; only control traffic to Cloud (no customer data-plane); billing hooks emit usage; JUL Cloud runs on JUL.
- Risks: tenant isolation (defense-in-depth: row-scoping + namespace + tests + audit), multi-tenant blast radius (quotas + rate limits + per-tenant CP sharding), data residency (region per tenant), trust (control-only, document data flows), scope (stage 1 only — no anycast). 
- Rollout: tags cloud+enterprise; OSS+self-host unaffected (can still self-host CP); Cloud is opt-in SaaS.
- Docs: README JUL Cloud (what it is/isn't, BYO-nodes, data flows, staged plan), docs/cloud.md, security/data-handling doc, CHANGELOG.

## Y5-02 GSLB: geo-routing + multi-region health-aware failover — SQ-EDGE P1 XL
- Objective: route clients to the nearest healthy region and fail over across regions automatically. DNS-based GSLB + anycast-ready HTTP redirection. Out: running JUL's own anycast PoPs (that's Cloud stage 2).
- Design: two mechanisms. (1) DNS GSLB: a JUL authoritative-DNS responder (new `jul gslb` mode) that answers A/AAAA by client geo (GeoIP) + region health -> returns nearest healthy region's IPs; health from the control plane (Y3-01) aggregating per-region node health (Y1-05). (2) HTTP-level geo steering: an edge JUL can 302/early-redirect or proxy to the nearest region (for cases w/o DNS control). GeoIP via MaxMind DB (or pure-Go IP-geo). Region health-aware: CP tracks region up/down + latency -> GSLB excludes unhealthy regions, weights by proximity/load. Anycast-ready: works behind anycast IPs when available, degrades to DNS otherwise. Reuse upstream Pool/health concepts at region granularity.
- Config: [gslb]: mode dns|http, regions[]{name,endpoints,geo,weight}, geoip_db, health_source controlplane, failover{strategy nearest|weighted, drain}. DNS mode: zones, ttl.
- New: internal/gslb/{dns.go (authoritative responder),geo.go,health.go,steer.go}; cmd `gslb` mode; reuse CP health (Y3-01), Y1-05. Tags gslb (+ enterprise).
- Tasks: GeoIP lookup -> region health aggregation from CP -> DNS authoritative responder (A/AAAA by geo+health) -> HTTP geo-steering (redirect/proxy nearest) -> failover (exclude unhealthy, reroute) -> drain on region down -> anycast compatibility -> jul_gslb_queries{region,decision}, region_health, failovers_total.
- Deps: miekg/dns (authoritative DNS), oschwald/geoip2-golang (MaxMind); reuse Y3-01 CP, Y1-05 health. Inter: Y3-01 region health, Y5-01 Cloud (multi-region tenants), Y5-05 RUM (real latency).
- Tests: unit (geo->region selection, health exclusion, weighting, DNS record gen); integration (simulated multi-region: client geo routes to nearest, region-down fails over, recovery re-includes); DNS conformance (dig queries return correct geo answers).
- DoD: clients routed to nearest healthy region (DNS + HTTP modes); unhealthy region auto-excluded + traffic failed over; recovery re-includes; GeoIP accurate; health sourced from CP; works anycast-ready or DNS-only.
- Risks: DNS caching/TTL lag on failover (low TTL + HTTP steering backup), GeoIP accuracy/licensing (MaxMind license; pure-Go fallback), authoritative DNS ops burden (document; or integrate managed DNS), split-horizon. 
- Rollout: tag gslb; enterprise/cloud; single-region unaffected.
- Docs: README GSLB (DNS + HTTP geo-routing, failover), docs/gslb.md, CHANGELOG.

## Y5-03 Service mesh mode (ambient/sidecar, east-west mTLS, SPIFFE/SVID) — SQ-MESH P1 XL
- Objective: run JUL as a service-mesh data plane: transparent east-west mTLS between services w/ SPIFFE identities, sidecar AND ambient (node-level) modes, L7 traffic policy. Out: building a new control plane (reuse Y3-01 + optional Istio xDS compat).
- Design: JUL as mesh data plane. (1) Sidecar mode: a JUL instance per pod/service intercepts inbound+outbound (iptables/eBPF redirect or explicit) -> applies east-west mTLS (Y2-07 mTLS extended w/ SPIFFE SVIDs as identities) + L7 policy (reuse routing/auth/traffic-mgmt Y3-05). (2) Ambient mode: one JUL per node handles mesh traffic for all local pods (ztunnel-style) — lighter than per-pod sidecars. Identity: SPIFFE/SVID issued by a workload API (JUL CP acts as SPIFFE issuer, or integrate SPIRE) -> mTLS certs w/ SPIFFE IDs -> authz policies by SPIFFE identity. Reuse: Y2-07 mTLS, Y1-01 cert provider (now issues SVIDs), Y3-01 CP (distributes policy/identity), Y3-05 traffic mgmt (mesh L7). Optional Istio compat: speak xDS so JUL can be an Istio data plane (stretch). 
- Config: [mesh]: mode sidecar|ambient, spiffe{trust_domain, issuer jul|spire, workload_api}, mtls{mode strict|permissive}, policies[]{from_spiffe_id, to, action}. Tag mesh + spiffe.
- New: internal/mesh/{sidecar.go,ambient.go,intercept.go,policy.go}; internal/spiffe/{svid.go,workloadapi.go,issuer.go}; extend Y2-07 mTLS w/ SVID; reuse Y3-01 CP, Y3-05. Tags mesh, spiffe (+ enterprise).
- Tasks: SPIFFE SVID issuance (CP issuer or SPIRE integration) -> workload API (deliver SVIDs to data planes) -> east-west mTLS w/ SVID identity (extend Y2-07) -> sidecar intercept (iptables/redirect) + transparent proxy -> ambient node-level mode (ztunnel-style) -> identity-based authz policy -> L7 policy reuse (Y3-05) -> optional xDS compat (stretch) -> jul_mesh_connections{mode}, mtls_identities, policy_denials.
- Deps: spiffe/go-spiffe/v2; reuse Y2-07 mTLS, Y3-01 CP, Y3-05. Inter: Y2-07, Y1-01, Y3-01, Y3-05, Y5-06 secrets/identity.
- Tests: unit (SVID issue/verify, policy match by SPIFFE ID, intercept rules); integration (two services w/ JUL sidecars -> mTLS w/ SPIFFE identities -> authz policy allows/denies; ambient mode handles node pods; permissive->strict migration); security (identity spoofing blocked, SVID rotation, policy bypass).
- DoD: east-west mTLS between services w/ SPIFFE/SVID identities; sidecar AND ambient modes work; identity-based authz policies enforced; SVID issuance + rotation; permissive+strict modes; integrates w/ existing CP for policy distribution; optional xDS compat demoable.
- Risks: intercept complexity (iptables/eBPF — start explicit/sidecar, ambient later), SPIFFE/SPIRE integration depth (support both self-issued + SPIRE), perf of per-conn mTLS (session resumption), scope vs Istio (reuse Y3 CP, don't rebuild). 
- Rollout: tags mesh+spiffe; enterprise; non-mesh unaffected; sidecar first, ambient second.
- Docs: README Service mesh (sidecar/ambient, SPIFFE, policy), docs/mesh.md, examples/mesh, CHANGELOG.

## Y5-04 Bot management + DDoS mitigation — SQ-SEC P2 L (enterprise/security)
- Objective: detect+mitigate automated abuse + volumetric/application DDoS: bot fingerprinting, challenge (JS/PoW/CAPTCHA), adaptive rate limiting, anomaly-based L7 DDoS defense. Out: L3/L4 scrubbing (network-layer; partner/cloud-provider concern).
- Design: a defense layer composing existing pieces + new detectors. (1) Bot detection: TLS/JA3 fingerprint + HTTP/2 fingerprint + header heuristics + behavioral (rate/pattern) -> bot score. (2) Challenges: lightweight JS challenge or proof-of-work (privacy-friendly, no third party) + optional CAPTCHA provider; issue+verify challenge tokens. (3) Adaptive rate limiting: extend Y1-03/Y3-03 limiter to ADAPT thresholds under load/attack (tighten when anomaly detected). (4) L7 DDoS: anomaly detection (Y4-05 EWMA reuse) on traffic shape -> auto-enable challenges/tarpit/block for suspicious cohorts. Reuse WAF (Y2-06) signals, guardrail-style actions. Fleet-coordinated via CP (Y3-01) so all nodes share attacker intel (distributed via Y3-03 redis/pubsub). 
- Config: [bot_defense]: enabled, fingerprinting{ja3,h2,headers}, challenge{type js|pow|captcha, difficulty, ttl}, adaptive_rate{enabled, baseline, attack_multiplier}, ddos{anomaly_threshold, action challenge|tarpit|block}, allowlist[]. Tag botmgmt.
- New: internal/botdefense/{fingerprint.go,challenge.go,score.go,ddos.go}; reuse Y1-03/Y3-03 limiter, Y2-06 WAF, Y4-05 anomaly, Y3-03 pubsub. Tags botmgmt (+ enterprise).
- Tasks: JA3/H2/header fingerprint -> bot score model -> JS/PoW challenge issue+verify -> CAPTCHA adapter -> adaptive rate limiter (tighten under attack) -> L7 DDoS anomaly + auto-mitigation (challenge/tarpit/block) -> fleet attacker-intel sharing (CP+redis) -> allowlist + bypass for known-good -> jul_bot_score, challenges_issued/passed, ddos_mitigations_total.
- Deps: reuse Y1-03/Y3-03, Y2-06, Y4-05; JA3 via TLS ClientHello (Y2-07 hook). Inter: Y1-03, Y2-06, Y3-01, Y3-03, Y4-05, Y2-07.
- Tests: unit (fingerprint extraction, score thresholds, challenge issue/verify, PoW validation, adaptive math); integration (bot UA challenged + verified human passes, attack pattern triggers mitigation, adaptive RL tightens under load, fleet shares attacker IPs); security (challenge bypass, replay, fingerprint evasion, allowlist abuse).
- DoD: bots fingerprinted+scored; JS/PoW challenge issued+verified (privacy-friendly, no mandatory third party); adaptive rate limiting tightens under attack; L7 DDoS anomalies auto-mitigated; fleet shares attacker intel; allowlist for known-good; configurable actions.
- Risks: false positives blocking real users (score tuning + allowlist + challenge-not-block first), challenge UX/accessibility (PoW invisible, CAPTCHA optional), fingerprint evasion (layered signals + behavioral), PoW client cost (tune difficulty). 
- Rollout: tag botmgmt; enterprise; disabled default; challenge-first.
- Docs: README Bot/DDoS (fingerprinting, challenges, adaptive, privacy), docs/bot-ddos.md, CHANGELOG.

## Y5-05 RUM + synthetic checks + SLO tracking in Console — SQ-CONSOLE P2 L
- Objective: close the observability loop with REAL user monitoring (client-side), synthetic uptime checks, and SLO/error-budget tracking in Console. Out: full APM (lean on OTel + partners).
- Design: (1) RUM: a tiny JS beacon (served by JUL, opt-in) collects client-side metrics (page load, LCP/CLS/INP web vitals, errors, geo) -> POST to a JUL RUM ingest endpoint -> aggregate -> Console RUM dashboard. Privacy-first (no PII, sampling, consent-aware). (2) Synthetic: JUL runs scheduled HTTP/L4 probes (reuse Y4-06 cron + Y1-05 health concepts) from one/multiple nodes -> uptime/latency from JUL's vantage; multi-region via GSLB regions (Y5-02). (3) SLO tracking: define SLOs (availability, latency) over Prometheus metrics -> compute error budgets + burn rate -> Console SLO view + alerts. Reuse Console v3 (Y3-09) + AI incident explanations (Y4-05) for SLO breaches. 
- Config: [rum]{enabled, sample_rate, ingest_path, consent}, [synthetic]{checks[]{url,interval,regions,assert}}, [slo]{objectives[]{metric,target,window}}. Tag enterprise (RUM/SLO at scale) but RUM beacon core.
- New: internal/rum/{beacon.go,ingest.go,aggregate.go}; internal/synthetic/{checks.go}; internal/slo/{slo.go,budget.go}; Console views; reuse Y4-06 cron, Y1-10 metrics, Y4-05 incidents. Tags console (+ enterprise).
- Tasks: RUM JS beacon + ingest endpoint + aggregation (web vitals, geo, sampling, privacy) -> synthetic scheduler + probes + multi-region (GSLB) -> SLO definitions + error-budget + burn-rate calc -> Console RUM/synthetic/SLO dashboards -> SLO breach -> AI incident explanation (Y4-05) -> alerts -> jul_rum_beacons, synthetic_up, slo_error_budget_remaining, burn_rate.
- Deps: stdlib + Console SPA; reuse Y4-06 cron, Y1-10, Y4-05, Y5-02 regions. Inter: Y3-09 Console, Y4-05 incidents, Y5-02 GSLB, Y1-10 metrics.
- Tests: unit (web-vitals parse, sampling, SLO/budget/burn math, synthetic assertions); integration (beacon ingested+aggregated, synthetic check detects downtime, SLO breach computes budget+triggers incident); privacy (no PII, consent honored, sampling).
- DoD: RUM collects web vitals+errors from real clients (privacy-first, opt-in); synthetic probes report uptime/latency (multi-region); SLOs compute error budget + burn rate; Console dashboards live; SLO breaches generate AI incident explanations + alerts.
- Risks: RUM privacy/consent (no PII, sampling, consent-aware, GDPR), beacon overhead (tiny + async), synthetic noise (retries + multi-vantage), SLO config complexity (templates + wizard). 
- Rollout: RUM beacon core; RUM-at-scale+SLO enterprise; opt-in.
- Docs: README RUM+SLO (beacon, synthetic, SLOs, privacy), docs/rum-slo.md, CHANGELOG.

## Y5-06 Secrets / identity integrations (Vault, cloud KMS, SPIFFE) — SQ-MESH P2 M (enterprise)
- Objective: centralize secret + identity management: pull TLS certs/keys/API keys/provider creds from Vault/cloud KMS; SPIFFE identity federation. Replaces scattered file/env secrets across all features. Out: being a secrets manager (integrate, don't replace).
- Design: a SecretProvider interface { Resolve(ref) ([]byte, error) } with backends: file/env (today), HashiCorp Vault, AWS/GCP/Azure KMS+secret managers, SPIFFE (Y5-03 SVID). EVERY secret in config becomes a ref (e.g. vault://path#key, kms://..., env://, file://) resolved at load + on rotation. Centralizes: TLS keys (Y1-01), provider API keys (Y4-01 keys.go vault), JWT/OIDC secrets (Y3-02), session secrets, plugin secrets (Y4-06). Auto-rotation: watch/lease-renew for Vault; re-resolve on TTL -> hot-apply via existing reload (certs swap via dynamicCertProvider). SPIFFE identities (Y5-03) for workload auth to Vault/KMS (no static creds). 
- Config: [secrets]: providers[]{type vault|aws_kms|gcp|azure|spiffe, config}, default_provider; secret refs usable anywhere a secret is configured (api_key_ref, key_file, etc. accept scheme://). Tag enterprise (+ provider tags).
- New: internal/secrets/{provider.go,vault.go,kms_aws.go,kms_gcp.go,kms_azure.go,resolve.go}; integrate resolver across config load (parser/validate) + Y1-01/Y3-02/Y4-01/Y4-06. Tags enterprise (provider subtags vault/awskms/etc.).
- Tasks: SecretProvider iface + ref scheme parser -> file/env (default) -> Vault (auth, kv, lease renew) -> cloud KMS/secret-manager adapters -> SPIFFE workload auth to providers -> resolve at config load + inject -> rotation watch -> re-resolve + hot-apply (cert swap/reload) -> redact resolved secrets everywhere -> jul_secret_resolves_total{provider}, rotation_total.
- Deps: hashicorp/vault/api, aws/gcp/azure SDKs (tagged per provider), spiffe (Y5-03). Inter: Y1-01 TLS, Y3-02 auth, Y4-01 keys, Y4-06 plugin secrets, Y5-03 SPIFFE.
- Tests: unit (ref parse, each provider resolve via mock, rotation re-resolve, redaction); integration (Vault dev server: resolve TLS key + provider key, rotate -> hot reload; KMS mock; SPIFFE auth to Vault); security (no plaintext secrets logged/persisted, least-priv, rotation).
- DoD: secrets resolved from Vault + >=1 cloud KMS via refs anywhere in config; rotation re-resolves + hot-applies (certs/keys) w/o restart; SPIFFE-based workload auth (no static creds); file/env still default for OSS; secrets never logged.
- Risks: provider outage at load (cache last-good + fail policy), rotation races (atomic swap), SDK bloat (per-provider tags), secret sprawl (centralize + audit). 
- Rollout: tags enterprise + per-provider; OSS = file/env; integrations opt-in.
- Docs: README Secrets/identity (Vault/KMS/SPIFFE, refs, rotation), docs/secrets.md, CHANGELOG.

## Y5-07 Ecosystem maturity (certification, template/plugin gallery, learning/docs hub) — SQ-DX/ECO P2 L (ongoing)
- Objective: make JUL a durable ECOSYSTEM: certified+rated plugins/templates, a web gallery, a learning/docs hub, contributor program. Out: paid marketplace billing (folds into Cloud Y5-08).
- Design: build on Y3-07 marketplace + Y4-08 templates. (1) Certification: a review+signing pipeline (automated checks: ABI compliance, capability audit, security scan, license) -> "certified" badge in manifest + signature by JUL root. (2) Gallery: a web catalog (plugins + templates) w/ search, ratings, reviews, install counts, screenshots — surfaced in Console (Y2-09/Y3-09) + public site. (3) Learning hub: structured docs (tutorials, guides, recipes), interactive examples, versioned; consolidate all docs/ (Y1-Y5) into a navigable hub. (4) Contributor program: plugin/template authoring SDK + templates + CI actions + a publishing flow (jul publish). 
- Config: [gallery]: registry url, ratings endpoint; mostly a service + CLI, minimal node config.
- New: tooling/registry side (gallery service) + cmd `jul publish`; certification CI pipeline; docs hub site (from docs/); reuse Y3-07 sign/verify, Y4-08. Mostly out-of-binary (services/site) + thin CLI.
- Tasks: certification pipeline (ABI/security/capability/license checks + JUL-root signing) -> gallery service (search/ratings/reviews/counts) -> Console gallery integration -> learning hub (consolidate docs, tutorials, interactive) -> authoring SDK + templates + `jul publish` -> contributor docs + governance -> ecosystem metrics (installs, ratings).
- Deps: reuse Y3-07 marketplace, Y4-08 templates; web/site stack (out of binary). Inter: Y3-07, Y4-08, Y2-09/Y3-09 Console, all docs/.
- Tests: unit (certification checks pass/fail, publish flow, manifest+sig); integration (publish -> certify -> appears in gallery -> install from Console); pipeline (security scan catches bad plugin).
- DoD: plugins/templates can be published + certified (signed by JUL root after automated review); gallery w/ search+ratings+reviews surfaced in Console + public; learning hub consolidates all docs + tutorials; `jul publish` + authoring SDK; >=10 certified ecosystem entries at GA.
- Risks: certification scale (automate + community reviewers), gallery moderation (reports + governance), supply chain (mandatory signing + scans — reuse Y3-07), docs drift (CI-built hub from in-repo docs). 
- Rollout: services/site + thin CLI; community-facing; ties into Cloud billing later.
- Docs: README Ecosystem (publish, certify, gallery, hub), docs/ecosystem.md + the hub itself, CONTRIBUTING, CHANGELOG.

## Y5-08 Cloud usage metering + billing (demand-gated) — SQ-CLOUD P2 L (commercial)
- Objective: meter + bill JUL Cloud usage so the SaaS can charge — BUILT ONLY ONCE STAGE-1 DEMAND IS PROVEN (REV3). Metering foundation first; billing activation gated on traction. Out: complex enterprise contracts (sales-assisted).
- Design: a metering pipeline in JUL Cloud (Y5-01): per-tenant usage events (managed nodes, control RPS, telemetry volume, AI gateway tokens via Y4-03, requests proxied) -> aggregate -> usage records -> billing provider (Stripe) for metered/subscription billing. Plans: free (N nodes), pro (more + features), enterprise (custom). Reuse Y4-03 cost/token metering for AI usage. Self-serve plan management in Console; invoices; quota enforcement tied to plan (reuse Y5-01 quotas). Metering is build-now; billing-activation is config/flag gated on demand.
- Config: cloud-side [billing]: provider stripe, plans[], metering{dimensions, aggregation}, enforce_quotas. Tag cloud + enterprise.
- New: internal/cloud/{metering.go,billing.go,plans.go,quota.go}; reuse Y5-01 tenant, Y4-03 token metering. Tags cloud, enterprise.
- Tasks: usage event collection (nodes, RPS, telemetry, AI tokens, requests) -> aggregation + usage records -> plan model + quota enforcement -> billing provider integration (Stripe metered+subscription) -> self-serve plan mgmt in Console -> invoices -> demand-gate flag (metering on, billing activatable) -> jul_cloud_usage{dimension,tenant}, billing_events.
- Deps: stripe-go; reuse Y5-01 tenant, Y4-03 metering. Inter: Y5-01 Cloud, Y4-03 AI cost, Y3-09 Console.
- Tests: unit (usage aggregation, plan/quota enforcement, billing event mapping); integration (tenant usage -> metered records -> Stripe test mode invoice; quota exceeded -> enforced/upgraded prompt); accuracy (metered usage matches actual).
- DoD: per-tenant usage metered accurately across dimensions (nodes/RPS/telemetry/AI tokens/requests); plans + quotas enforced; Stripe billing integrated (test-mode proven); self-serve plan mgmt; metering live, billing flag-activatable when demand proven.
- Risks: metering accuracy (reconcile + audit), billing correctness (test-mode + idempotency), premature monetization (REV3: gate activation on demand), quota UX (warn before enforce). 
- Rollout: tags cloud+enterprise; metering on, billing demand-gated; OSS/self-host never billed.
- Docs: README JUL Cloud pricing/plans, docs/cloud-billing.md, CHANGELOG.

## Y5-09 Global performance + final hardening (capstone) — SQ-EDGE P1 M (cross-cutting capstone)
- Objective: a 5-year capstone: validate global-scale performance, finalize the lean-vs-full story, and complete security/compliance for commercial GA. Out: new features (this is hardening).
- Design: (1) Global perf: distributed load tests across regions (GSLB+Cloud), tail-latency optimization, connection reuse, p99 budgets per feature; publish benchmarks vs NGINX/Caddy/Envoy + AI-gateway vs LiteLLM/Portkey. (2) Lean story: final build-tag matrix audit — ensure build-min still tiny (core proxy/static/cache/TLS) and every heavy feature (ai/mesh/gslb/cloud/wasm/...) is opt-in; publish size/perf deltas per tag. (3) Security/compliance GA: full audit (auth/mesh/cloud/secrets/bot), pen-test, SOC2/ISO evidence (audit logs Y3-08), supply-chain (SBOM+signing all artifacts), responsible-disclosure + CVE process. (4) Reliability: chaos testing (region/node/CP failures), upgrade paths (Y3-06) across versions.
- Config: none (process/CI/release).
- New: bench/global; CI gates (perf regression, size budget per tag, vuln, SBOM); compliance evidence. Cross-cutting.
- Tasks: global distributed load harness -> tail-latency + connection-reuse optimization -> per-tag size+perf budget gates in CI -> published benchmark suite (vs NGINX/Caddy/Envoy/LiteLLM/Portkey) -> full security audit + pen-test -> SOC2/ISO evidence package -> SBOM + signing all artifacts -> chaos + cross-version upgrade tests -> CVE/disclosure process.
- Deps: k6/vegeta/wrk, syft/grype, govulncheck; reuse all prior gates. Inter: ALL features (capstone validates them).
- Tests: global load (multi-region p99 budgets met), size budgets (build-min under threshold), chaos (failures recover), security (pen-test findings resolved), upgrade (N-1 -> N zero-downtime).
- DoD: published benchmarks (JUL competitive vs NGINX/Caddy/Envoy on proxy/static/TLS; vs LiteLLM/Portkey on AI gateway); build-min stays lean (documented size/perf per tag); security audit + pen-test passed; SOC2/ISO evidence ready; SBOM+signed releases; chaos + cross-version upgrade green; all 5-year DoDs verified.
- Risks: perf regressions accumulated over 5 yrs (continuous gate caught them), scope of audit (prioritize attack surface), benchmark fairness (reproducible, documented method). 
- Rollout: process/release; GA milestone.
- Docs: README Benchmarks + Security + Build matrix, docs/benchmarks.md, security.md, SECURITY.md, compliance package, CHANGELOG (5-year GA).

## Cross-cutting Y5
- Privacy/compliance/trust: Cloud data-flow transparency (control-only stage 1), RUM consent/GDPR, secrets never logged, tenant isolation audits, SOC2/ISO completion, data residency per region.
- Perf: global tail latency, mesh per-conn mTLS cost, GSLB decision latency, RUM beacon overhead, Cloud control-plane scale; CI regression gate (5-year capstone in Y5-09).
- Security: multi-tenant isolation, mesh identity, bot/DDoS surface, secrets providers, Cloud attack surface; pen-test (Y5-09).
- Docs (exhaustive, in sync): every feature updates README feature table+config ref, docs/<feature>.md, testdata/*.toml, examples/<feature>/, CHANGELOG; consolidated into the learning hub (Y5-07). DoD blocks merge w/o docs.
- Build/release: tag matrix += cloud, mesh, spiffe, gslb, botmgmt; build-min stays lean (Y5-09 audit); SBOM+signed per artifact; OSS vs enterprise vs cloud builds.

Year 5 DoD: JUL Cloud (multi-tenant hosted Console + BYO-node enrollment + isolation + telemetry, control-only) running on JUL itself; GSLB geo-routing + multi-region failover (DNS+HTTP); service mesh mode (sidecar+ambient, SPIFFE/SVID, east-west mTLS, identity policy); bot/DDoS mitigation (fingerprint+challenge+adaptive+L7); RUM+synthetic+SLO in Console w/ AI incident explanations; secrets/identity via Vault/KMS/SPIFFE w/ rotation; ecosystem (certification+gallery+learning hub, >=10 certified entries, jul publish); Cloud usage metering live (billing demand-gated); global perf benchmarks published + security/compliance GA (pen-test, SOC2/ISO, SBOM); build-min still lean; CI green; docs exhaustive in the hub. => 5-YEAR ROADMAP COMPLETE: OSS -> open-core -> Cloud ladder delivered; most-powerful (AI gateway, mesh, GSLB, edge) + friendliest (Console, AI assist, zero-config, templates) + lean (build tags, single binary) all intact.

## Dependency graph (mermaid)
graph LR
  CP[Y3-01 control plane] --> Cloud[Y5-01 JUL Cloud multi-tenant]
  RBAC[Y3-02 RBAC/SSO] --> Cloud
  CV3[Y3-09 Console v3] --> Cloud
  AIassist[Y4-05 AI Console] --> Cloud
  Cloud --> Bill[Y5-08 metering/billing]
  AICost[Y4-03 token/cost] --> Bill
  CPHealth[Y3-01 region health] --> GSLB[Y5-02 GSLB]
  Health[Y1-05 active health] --> GSLB
  GSLB --> RUM[Y5-05 RUM+SLO]
  mTLS[Y2-07 mTLS] --> Mesh[Y5-03 service mesh]
  CertSeam[Y1-01 cert provider] --> Mesh
  CP --> Mesh
  Traffic[Y3-05 traffic mgmt] --> Mesh
  Mesh --> Secrets[Y5-06 secrets/identity SPIFFE]
  Limiter[Y1-03/Y3-03] --> Bot[Y5-04 bot/DDoS]
  WAF[Y2-06 WAF] --> Bot
  Anomaly[Y4-05 anomaly] --> Bot
  Cron[Y4-06 cron] --> RUM
  Incidents[Y4-05 incidents] --> RUM
  Mkt[Y3-07 marketplace] --> Eco[Y5-07 ecosystem]
  Tmpl[Y4-08 templates] --> Eco
  AllSecrets[Y1-01/Y3-02/Y4-01 secrets] --> Secrets
  ALL[every feature] --> Cap[Y5-09 global perf + GA hardening]
