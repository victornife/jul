<!-- Engineering execution spec. Detailed source-of-truth for the Year 4 — AI-native + Edge platform roadmap entry.
     Companion to ../roadmap/ and ../vision/. Update when a feature's design changes. -->

# JUL Engineering Execution Plan — Year 4 (AI-native + Edge platform)

> Version 1.0 · Updated 2026-06-21 · **Vision horizon — demand-gated.** Entered via
> the time-boxed AI Gateway MVP (`ai` tag) with a kill/continue gate; the full
> program is not committed (see [ADR 0003](../adr/0003-maturity-and-ga.md)). The
> "most powerful" framing below is superseded by the vision's positioning —
> *leanest serious edge/protocol gateway*, not most-powerful overall.

Goal: deliver on the ".IA" in Jul.IA — make JUL simultaneously the MOST POWERFUL (AI Gateway category, edge compute, CDN-grade cache) and FRIENDLIEST (natural-language ops) light server. Headlines: (A) AI GATEWAY (LLM proxy: multi-provider routing+failover, semantic cache, token rate-limit, guardrails, cost observability), (B) AI-assisted Console (NL->config, anomaly detection, plain-English incidents), (C) Edge compute / WASM FaaS (staged: richer plugins first). Plus CDN-grade caching, 1-click app templates, new standards (Early Hints 103, WebTransport, post-quantum TLS).
Exit: proxy LLM traffic across >=3 providers w/ failover; semantic cache cuts token spend; per-key token budgets + cost dashboards; guardrails block prompt-injection/PII; Console turns plain English into validated config; edge plugins get KV+fetch+cron; CDN tiering+image-opt+ESI+tag-purge; PQ-TLS + Early Hints + WebTransport.
REV3 decision honored: AI Gateway is FULL-SCOPE here in Y4 (no early Y2/3 preview). Edge = richer WASM plugins first; full Workers-style FaaS only if ecosystem pulls.

Squads: SQ-AI new (Y4-01 gateway core, Y4-02 semantic cache, Y4-03 token/cost, Y4-04 guardrails) · SQ-CONSOLE (Y4-05 AI Console) · SQ-PLUGINS (Y4-06 edge/FaaS) · SQ-CACHE (Y4-07 CDN-grade) · SQ-DX (Y4-08 templates) · SQ-EDGE (Y4-09 standards).
Quarters: Q1 gateway core (provider adapters+routing+failover), CDN tiered cache+tag-purge, edge KV host ABI, PQ-TLS. Q2 semantic cache, token/cost, image-opt, FaaS cron+fetch, Early Hints. Q3 guardrails, AI Console NL->config, ESI, WebTransport, app templates. Q4 AI Console anomaly+incident, AI hardening, perf+security gate, docs.

Build tags added Y4: ai (AI gateway), imageopt (image optimization), webtransport. PQ-TLS + Early Hints = core (stdlib). Edge/FaaS extends `wasm` tag. Templates = core CLI.

## Y4-01 AI Gateway core: multi-provider routing + failover — SQ-AI P0 XL (THE Y4 FLAGSHIP)
- Objective: an OpenAI-compatible LLM gateway endpoint that proxies chat/completions/embeddings to multiple providers (OpenAI, Anthropic, Azure OpenAI, AWS Bedrock, Google, local/Ollama/vLLM) with normalization, routing, weighted load-balancing, automatic failover/fallback chains, and streaming. Out: training/fine-tune APIs, image-gen (later).
- Design: NEW location action ActionAIGateway (LocationConfig field + actionOf case + ActionAIGateway const + validate + register router.Builder in main.go + handler in internal/handler/aigateway.go) — same action model as ActionProxy/ActionGRPCTranscode. internal/aigateway/ package built around ONE internal CANONICAL NEUTRAL MODEL (provider-agnostic chat/completions/embeddings types) with TWO exposed front doors that both translate to/from it: (1) OpenAI-compatible API (/v1/chat/completions etc.) = DEFAULT for max ecosystem/SDK compat; (2) a first-class JUL-NATIVE NEUTRAL schema endpoint (/v1/jul/* or content-negotiated) = stable, versioned, provider-agnostic public wire format (NOT just internal). Decouples JUL from OpenAI schema drift: if OpenAI changes, only its front-door adapter changes; neutral schema stays stable. Provider adapters map NEUTRAL<->provider via a Provider interface { Translate(neutralReq)->providerReq; Do(ctx)->resp/stream; Normalize(resp)->neutralResp }. CRUCIAL: guardrails (Y4-04), semantic cache (Y4-02), token/cost (Y4-03) all operate on the INTERNAL NEUTRAL MODEL, so they work identically regardless of which front door the client used. Routing+failover REUSE upstream.Pool/Balancer pattern: providers modeled as backends; weighted_round_robin/least_conn pick; MarkFailure on 429/5xx/timeout -> failover to next in fallback chain; passive+active health (Y1-05) for provider endpoints. Streaming: SSE passthrough reusing StatusWriter.Flush (the existing SSE seam in responsewriter.go); translate provider SSE deltas -> neutral delta -> front-door delta (OpenAI or neutral). Per-model routing rules (model name -> provider/deployment). API-key vault for upstream provider creds (env/file/secret ref). Retries w/ backoff + circuit breaking.
- Config: AIGatewayConfig on LocationConfig (ActionAIGateway): front_doors[openai,neutral] (which exposed schemas to serve; default both), providers[]{name,type openai|anthropic|azure|bedrock|google|ollama, base_url, api_key_ref, models[], weight}, routing{strategy, model_map, fallback_chain[]}, timeouts, retries{max,backoff}, streaming bool.
- New: handler/aigateway.go + ActionAIGateway; internal/aigateway/{neutral.go (canonical neutral model + types + versioning), frontdoor_openai.go (OpenAI<->neutral), frontdoor_neutral.go (JUL-native neutral wire), provider.go (Provider iface neutral<->provider), openai.go,anthropic.go,azure.go,bedrock.go,google.go,ollama.go (provider adapters), router.go,keys.go,stream.go}. Tag ai.
- Tasks: action plumbing -> define canonical NEUTRAL model + versioned types -> OpenAI front-door (decode/encode OpenAI<->neutral) -> JUL-native neutral front-door (decode/encode + content negotiation) -> Provider iface + adapters (neutral<->provider req/resp + SSE deltas) per vendor -> provider pool reusing Balancer -> fallback chain + retry/backoff + circuit breaker -> MarkFailure on 429/5xx -> streaming SSE translate (provider->neutral->front-door delta) -> model_map routing -> api-key vault/refs -> request/response metadata (model, provider, latency) -> jul_ai_requests_total{provider,model,code,frontdoor}, ai_provider_failovers_total, ai_request_duration.
- Deps: stdlib net/http (avoid heavy vendor SDKs — call REST directly to keep lean); reuse upstream Pool, Y1-05 health. Inter: Y4-02 cache, Y4-03 token/cost, Y4-04 guardrails all wrap this; Y4-05 dogfoods it.
- Tests: unit (neutral model round-trip; OpenAI front-door <->neutral; JUL-native front-door <->neutral; each adapter translate req/resp + SSE delta; routing/model_map; fallback on simulated 429/5xx; circuit breaker); integration (mock provider servers: route, weighted-LB, failover chain, streaming end-to-end via BOTH front doors, key injection); contract tests vs recorded provider fixtures; equivalence test (same logical request via OpenAI front door and neutral front door -> identical provider call + identical guardrail/cache/token behavior).
- DoD: BOTH front doors live (OpenAI-compatible default + first-class JUL-native neutral schema), both translating to/from one canonical neutral model; OpenAI-compatible endpoint proxies chat+completions+embeddings to >=3 real providers; weighted routing + automatic failover across a fallback chain; streaming works for all adapters on both front doors; per-model routing; guardrails/semantic-cache/token-cost operate on the neutral model (front-door-agnostic); neutral schema is versioned+documented; provider keys never logged; metrics per provider/model/frontdoor.
- Risks: provider API drift (adapter versioning + contract fixtures), streaming format variance (per-adapter SSE translate + tests), key leakage (vault + redact + no-log), cost blowups (Y4-03 budgets), latency stacking (timeouts+circuit breaker), vendor SDK bloat (use REST directly). 
- Rollout: tag ai; ActionAIGateway opt-in; min/full validate-time "ai not compiled in".
- Docs: README AI Gateway (providers table, BOTH APIs: OpenAI-compat + JUL-native neutral schema, fallback), docs/ai-gateway.md (incl. versioned neutral schema reference + when to use which front door), examples/ai-gateway (both wire formats), server.toml, CHANGELOG.

## Y4-02 Semantic caching — SQ-AI P0 L
- Objective: cache LLM responses by SEMANTIC similarity (not exact key) to cut tokens/latency/cost: embed prompt -> vector -> nearest-neighbor search -> if cosine sim >= threshold return cached completion. Out: cross-tenant cache sharing (privacy).
- Design: a semantic tier layered onto the EXISTING cache store interface {get;set;del;purge} + Entry storage (store the completion as an Entry; add a vector index keyed to it). Embedding via a configured provider (reuse Y4-01 adapters embeddings path) or local model. Vector index: pure-Go HNSW (e.g. coder/hnsw) in-mem for single-node; optional Redis-vector / external (qdrant) tier reusing the Y3-03 distributed seam for fleets. Similarity threshold gates HIT; respect max-tokens/model/params in the cache key namespace (don't serve a gpt-4 answer for a gpt-3.5 request — namespace by model+params). TTL + max-entries eviction reusing LRU concepts from memStore. Privacy: per-key/tenant namespace; opt-in only; never cache flagged-sensitive (Y4-04 guardrails signal).
- Config: AIGatewayConfig += SemanticCache{enabled, embedding{provider,model}, similarity_threshold (e.g. 0.95), ttl, max_entries, namespace_by[model,user], backend memory|redis|qdrant}.
- New: internal/aigateway/semcache.go (embed->index->search) + vector index wrapper; reuse cache.Entry + store. Tag ai.
- Tasks: embedding call (provider) -> vector index (HNSW build/add/search) -> similarity gate + namespace key -> store completion as Entry + vector -> TTL/LRU eviction -> redis/qdrant backend (fleet) -> sensitivity exclusion hook -> X-Cache: SEMANTIC-HIT header + jul_ai_semcache_hits_total, tokens_saved, cost_saved.
- Deps: coder/hnsw (pure Go) or hnswlib-go; reuse Y3-03 redis. Inter: Y4-01 (wraps it), Y4-03 (tokens/cost saved metrics), Y4-04 (sensitivity exclusion).
- Tests: unit (cosine math, threshold gate, namespace isolation, eviction); integration (semantically-equiv prompts HIT, dissimilar MISS, model namespace prevents cross-serve, TTL expiry); accuracy harness (precision/recall on a prompt set at various thresholds).
- DoD: semantically-similar prompts return cached completions above threshold; dissimilar miss; namespaced by model+params+tenant; measurable tokens/cost saved metric; TTL+eviction bound memory; fleet backend shares cache; sensitive responses excluded.
- Risks: false-positive HITs serving wrong answer (conservative default threshold + namespace + eval harness), embedding cost/latency (cache embeddings, batch), privacy (opt-in, namespace, exclusion), index memory (max_entries + eviction). 
- Rollout: tag ai; disabled default; recommend high threshold first.
- Docs: README Semantic cache (threshold tuning, privacy), docs/ai-gateway.md section, CHANGELOG.

## Y4-03 Token-based rate limiting + cost observability — SQ-AI P0 M
- Objective: limit and account for LLM usage by TOKENS and COST (not just requests): per-key/model/tenant token budgets, spend caps, real-time cost dashboards. Out: invoicing/billing (Y5 cloud).
- Design: extend the Limiter interface (added Y1-03, distributed in Y3-03) with a TOKEN limiter: count tokens per request (pre-count via a tokenizer for the model family; reconcile post-response from provider usage field) -> debit a token/cost bucket keyed by api-key/user/model -> 429 + Retry-After or 402-style budget-exceeded when over. Cost = tokens x per-model pricing table -> Prometheus counters + per-key budget gauges. Pricing table is config + updatable. Distributed token/cost buckets reuse Y3-03 redis backend for fleets. Streaming: count from final usage chunk. Budgets: soft (warn) + hard (block) thresholds, per period.
- Config: AIGatewayConfig += TokenLimit{enabled, key api_key|user|header, limits[]{model, tokens_per_min, tokens_per_day}, budgets[]{key, usd_per_day, usd_per_month, action warn|block}}, pricing{model->{input_per_1k,output_per_1k}}.
- New: internal/aigateway/tokenlimit.go (tokenizer + bucket), pricing.go; reuse Limiter (Y1-03/Y3-03) + observability. Tag ai.
- Tasks: tokenizer per model family (pre-count) -> reconcile from provider usage -> token bucket (local + redis) keyed by api-key/user/model -> pricing table -> cost computation -> soft/hard budget enforcement -> 429/budget responses -> jul_ai_tokens_total{provider,model,dir}, ai_cost_usd_total{provider,model,key}, ai_budget_remaining.
- Deps: tiktoken-go (or pure-Go BPE) for counting; reuse Y3-03 redis Limiter. Inter: Y4-01 (wraps), Y4-02 (saved tokens credited), Y4-05 (cost shown in Console).
- Tests: unit (token count accuracy per model, bucket debit, pricing math, soft/hard thresholds); integration (token-rate cap -> 429; daily budget exhausts -> block; streaming usage reconciled; distributed across 2 nodes); cost accuracy vs known fixtures.
- DoD: token-per-min/day limits enforced per key/model; USD budgets warn then block; cost metrics accurate vs provider usage; distributed across fleet; streaming accounted; pricing table updatable w/o restart.
- Risks: tokenizer drift vs provider count (reconcile post-hoc + alert on delta), pricing changes (config + updatable), streaming accounting (final usage chunk; estimate if absent), budget race in distributed (atomic redis). 
- Rollout: tag ai; disabled default.
- Docs: README Token limiting + cost (pricing config, budgets), docs/ai-gateway.md section, CHANGELOG.

## Y4-04 Prompt/response guardrails — SQ-AI P1 L
- Objective: safety+compliance layer on AI traffic: prompt-injection detection, PII detection/redaction, content moderation, topic/term blocklists, output filtering, schema/JSON enforcement. Out: full DLP suite (enterprise add-on later).
- Design: a guardrail chain wrapping the AI gateway action (request phase on input, response phase on output) — mirrors the middleware pattern but operates on parsed AI messages. Built-in detectors: regex/heuristic PII (email/CC/SSN/phone) w/ redaction, prompt-injection signatures, blocklist/allowlist terms, max-tokens/format enforcement. Pluggable detectors: (1) a moderation provider call (OpenAI/Azure moderation), (2) CUSTOM guardrails as WASM plugins (REUSE Y2-02 plugin host ABI — a guardrail is a plugin that inspects/transforms the message) — this is the extensibility payoff. Actions on violation: block (4xx + reason), redact (mutate+continue), flag (annotate+log), or route-to-safe-model. Signals feed Y4-02 (exclude-from-cache) + Y3-08 audit.
- Config: AIGatewayConfig += Guardrails{input{pii redact|block, injection block|flag, moderation{provider}, blocklist[], max_input_tokens}, output{pii, moderation, format json_schema, blocklist[]}, plugins[] (wasm guardrail refs), on_violation block|redact|flag}.
- New: internal/aigateway/guardrails/{chain.go,pii.go,injection.go,moderation.go,blocklist.go,schema.go,plugin.go}; reuse Y2-02 plugin runtime. Tag ai (+ wasm for plugin guardrails).
- Tasks: guardrail chain (input+output phases) -> PII detector+redactor -> injection signatures -> blocklist/allowlist -> moderation provider adapter -> JSON-schema/format enforcement -> WASM custom guardrail (plugin ABI) -> violation actions (block/redact/flag/reroute) -> cache-exclusion + audit signals -> jul_ai_guardrail_events_total{type,action}.
- Deps: reuse Y2-02 wazero plugins; moderation via Y4-01 adapters. Inter: Y4-01 (wraps), Y4-02 (exclusion), Y3-08 (audit), Y2-02 (custom guardrails).
- Tests: unit (PII patterns redact, injection signatures, blocklist, schema reject, plugin guardrail invoked); integration (injection prompt blocked, PII redacted end-to-end, moderation flags, custom WASM guardrail mutates); red-team prompt set.
- DoD: PII detected+redacted/blocked; prompt-injection blocked/flagged; moderation integrated; blocklists+schema enforced; custom WASM guardrails run; violations audited + excluded from cache; configurable per-location.
- Risks: detector false pos/neg (tunable + flag-first rollout + eval set), moderation latency/cost (async where possible, cache), injection arms race (signatures + plugin updates), PII regex gaps (document limits, allow custom). 
- Rollout: tag ai; disabled default; recommend flag->redact->block.
- Docs: README Guardrails (detectors, actions, custom plugins), docs/ai-gateway.md security section, examples/guardrail-plugin, CHANGELOG.

## Y4-05 AI-assisted Console (NL->config, anomaly detection, incident explanations) — SQ-CONSOLE P1 L
- Objective: the friendliness endgame — operate JUL in plain English. NL->config (describe intent -> generated, validated config diff), anomaly detection on metrics, plain-English incident explanations, autotuning suggestions. Out: fully autonomous changes (always human-approve).
- Design: build on Console v3 (Y3-09). An assistant panel calls an LLM THROUGH JUL's own AI Gateway (Y4-01) — dogfooding. (1) NL->config: prompt + current config + schema -> LLM proposes a config diff -> run EXISTING validate (config.Validate) + lint (Y1-08) + diff (Y3-08) -> show diff -> human approves -> apply via existing reload seam. NEVER auto-apply. (2) Anomaly detection: lightweight time-series analysis on Prometheus metrics (z-score/EWMA, no heavy ML) -> flag spikes (latency/errors/traffic) -> optional LLM explanation. (3) Incident explanations: gather recent metrics+logs+audit -> LLM summarizes "what happened, likely cause, suggested fix" in plain English. (4) Autotuning: suggest pool sizes/timeouts/cache TTLs from observed patterns (advisory). All grounded: feed the LLM the real schema + metrics so output is valid; validate everything before showing.
- Config: AdminConfig += Assistant{enabled, gateway_location (which AI Gateway endpoint), model, features{nl_config,anomaly,incident,autotune}, auto_apply false}.
- New: internal/admin/{assistant.go,anomaly.go,incident.go}; web/console assistant panel; reuse config.Validate+lint+diff+reload, Y4-01 gateway. Tags console + ai.
- Tasks: assistant API (proxy to AI Gateway w/ schema+context) -> NL->config prompt+grounding -> validate+lint+diff pipeline -> approval UI (show diff, never auto-apply) -> anomaly detector (EWMA/z-score on metrics) -> incident summarizer (collect metrics/logs/audit -> LLM) -> autotune advisor -> guardrails on assistant I/O (Y4-04) -> jul_assistant_requests_total, anomalies_detected.
- Deps: Y4-01 gateway; reuse config validate/lint/diff. Inter: Y4-01, Y1-08 lint, Y3-08 diff/audit, Y3-09 Console.
- Tests: unit (config-gen validated before surface, diff render, anomaly math, redaction of secrets in prompts); integration (NL "add a rate limit to /api" -> valid diff -> apply via reload; anomaly flagged on synthetic spike; incident summary from fixture); safety (never auto-applies, invalid gen rejected, secrets not sent to LLM).
- DoD: plain-English request yields a VALIDATED config diff requiring human approval; anomalies surfaced on metrics; incident explanations generated from real telemetry; autotune suggestions advisory; secrets redacted before LLM; all changes go through existing validate+reload.
- Risks: hallucinated/invalid config (validate+lint gate, never auto-apply), secret leakage to LLM (redact context, on-prem/local model option), over-reliance (advisory framing), anomaly noise (tunable). 
- Rollout: tags console+ai; disabled default; human-in-the-loop enforced.
- Docs: README AI Console (NL ops, privacy, local-model option), docs/ai-console.md, CHANGELOG.

## Y4-06 Edge compute / WASM FaaS (staged: richer plugins first) — SQ-PLUGINS P1 L->XL
- Objective: grow Y2-02 WASM plugins toward edge compute by adding richer host capabilities — persistent KV, outbound fetch, scheduled/cron triggers, secrets, request-scoped storage — so plugins become mini edge-functions. Full Workers-style FaaS runtime only if ecosystem demand pulls (REV3). Out: speculative full FaaS arms race.
- Design: extend internal/plugins host ABI (Y2-02 host.go/abi.go — BOTH http-wasm + proxy-wasm per Y2 decision) with new host functions: (1) KV get/set/del/list backed by bbolt (single-node) or redis (fleet, Y3-03) — namespaced per plugin; (2) fetch outbound HTTP reusing the proxy transport + allowed_hosts SSRF allowlist (seeded in Y2-02 config); (3) cron: a scheduler (robfig/cron) invokes plugin entrypoints on schedule (new guest export on_schedule); (4) secrets: read injected secrets by ref (no plaintext in wasm); (5) request KV/cache access. Cron plugins run as background jobs in-process; reuse Pool.Close-style lifecycle for reload. Capability-gated (kv/fetch/cron/secrets booleans + allowlists) + surfaced in marketplace manifest (Y3-07). Sandbox limits (memory/timeout) from Y2-02 still apply.
- Config: [plugins] entries += capabilities{kv,fetch{allowed_hosts},cron{schedule},secrets[]}, kv_backend memory|bbolt|redis. (extends Y2-02 schema).
- New: internal/plugins/{kv.go (backends),fetch.go,cron.go,secrets.go}; extend host.go ABI; scheduler lifecycle. Tag wasm (edge caps under same tag).
- Tasks: KV host funcs + bbolt/redis backends + namespace -> fetch host func + transport + SSRF allowlist -> cron scheduler + on_schedule export + reload lifecycle -> secrets injection by ref -> capability gating + manifest surfacing -> example edge plugins (geo-router, A/B, scheduled cache-warm, webhook) -> jul_plugin_kv_ops_total, fetch_total, cron_runs_total.
- Deps: robfig/cron; reuse wazero (Y2-02), bbolt (Y3-01), redis (Y3-03). Inter: Y2-02 ABI, Y3-01 bbolt, Y3-03 redis, Y3-07 marketplace (capability manifest), Y4-07 (cache-warm).
- Tests: unit (KV CRUD+namespace isolation, fetch allowlist blocks internal, cron fires, secret ref no-plaintext); integration (plugin persists KV across requests, fetches allowed host, scheduled plugin warms cache, reload stops crons — goleak); security (SSRF blocked, KV cross-plugin isolation, sandbox holds).
- DoD: plugins use persistent KV (bbolt/redis), outbound fetch (allowlist-guarded), and cron schedules; secrets by ref (no plaintext); capabilities gated+manifested; crons stop cleanly on reload; >=4 example edge plugins; sandbox+single-binary intact.
- Risks: SSRF via fetch (strict allowlist + block internal/link-local + DNS rebinding guard), KV growth (quotas+eviction), cron reload leaks (lifecycle+goleak), capability creep (explicit gating+consent), staying lean (gate behind wasm tag). 
- Rollout: tag wasm; capabilities opt-in per plugin; FaaS framed as "richer plugins", not Workers clone.
- Docs: README Edge plugins (KV/fetch/cron/secrets, ABI additions), docs/edge-compute.md, examples/edge-plugins, CHANGELOG.

## Y4-07 CDN-grade caching (tiered, tag-purge, image-opt, ESI) — SQ-CACHE P1 L
- Objective: turn the cache into a CDN-grade edge cache: hierarchical/tiered cache (origin shield), surrogate-key/tag-based purge, on-the-fly image optimization, ESI assembly. Out: global multi-PoP (Y5 GSLB/cloud).
- Design: build on the existing two-tier cache (mem L1 + disk L2) and Y3-03 redis L3. (1) Tiered/origin-shield: a designated parent/shield node absorbs origin misses for a cluster (config: this node is shield, or points to a shield) — reduces origin load; reuse proxy + cache. (2) Tag purge: support Surrogate-Key / Cache-Tag response headers -> index entries by tag -> extend the EXISTING admin /cache/purge to purge by tag/pattern/surrogate-key (today it's key-based) + distributed via Y3-03 pub/sub. (3) Image optimization: a handler/middleware that resizes/recompresses/format-converts (webp/avif) on the fly w/ query params (?w=&q=&fmt=) -> cache the variant (Vary on the derived key); pure-Go codecs to avoid cgo. (4) ESI: parse <esi:include src> in responses -> fetch fragments (cacheable independently) -> assemble; surrogate-control. All reuse cache store + Entry; Vary already supported.
- Config: CacheConfig += Tiering{role normal|shield, shield_upstream}, TagPurge{enabled, header Surrogate-Key}, ImageOpt{enabled, formats[webp,avif], max_dim, quality, cache_variants}, ESI{enabled, max_includes, max_depth}.
- New: internal/cache/{tags.go,tiering.go}; internal/handler/imageopt.go; internal/middleware/esi.go; extend admin purge. Tags: imageopt (image deps), ESI+tiering+tag-purge core.
- Tasks: tag index (entry->tags, tag->entries) -> tag/pattern purge in admin + distributed pub/sub -> Surrogate-Key parsing -> origin-shield routing -> image handler (decode->resize->encode webp/avif) + variant cache + Vary -> ESI parser+fragment fetch+assemble+limits -> jul_cache_purge_total{type}, image_opt_total, esi_includes_total.
- Deps: disintegration/imaging or x/image + kolesa-team/go-webp / gen2brain/avif (pure-Go where possible) for imageopt; reuse Y3-03 pubsub. Inter: Y3-03 distributed purge, Y1-02 compression ordering, Y4-06 (cache-warm cron).
- Tests: unit (tag index purge, surrogate-key parse, image resize/format, ESI parse+assemble+limits); integration (tag purge evicts tagged entries across nodes, image variant cached+served webp/avif by Accept, ESI assembles fragments, shield absorbs origin misses); perf (image-opt throughput, ESI overhead).
- DoD: purge by tag/surrogate-key/pattern (single + fleet); origin-shield reduces origin hits; on-the-fly image resize+format(webp/avif) cached as variants; ESI includes assembled w/ depth/count limits; metrics for each.
- Risks: image-opt CPU/memory (limits+variant cache+async), pure-Go avif maturity (feature-flag+fallback), ESI SSRF/loops (allowlist+depth/count limits), tag index memory (bounded), cgo avoidance (pick pure-Go codecs). 
- Rollout: imageopt tag; tiering/ESI/tag-purge core; disabled defaults.
- Docs: README CDN caching (tiering, tag purge, image-opt, ESI), docs/cdn-cache.md, testdata, CHANGELOG.

## Y4-08 1-click app templates / self-host catalog — SQ-DX P2 M
- Objective: on-brand for self-hosters — a curated catalog of ready configs (and optional compose stacks) to put popular apps behind JUL with auto-HTTPS in one step: `jul template <app>` + Console catalog. Out: hosting the apps themselves.
- Design: reuse the marketplace pattern (Y3-07: signed registry + manifest) but for APP TEMPLATES instead of plugins. A template = a parameterized JUL config snippet (+ optional docker-compose + notes) for an app (WordPress, Ghost, Nextcloud, Gitea, n8n, etc.). `jul template list|show|apply <app>` prompts for params (domain, upstream) -> generates a validated config (reuse Y1-08 zeroconf/wizard + validate) merged into server.toml -> auto-HTTPS via Y1-01. Console v3 gets a catalog browser (reuse marketplace UI) that does the same via API. Templates are signed+versioned in a registry (reuse Y3-07 verify).
- Config: [template_registry]: url, trusted_keys[]; per-template manifest (jul-template.toml: app, params, config snippet, compose optional).
- New: internal/templates/{catalog.go,apply.go,manifest.go}; cmd subcommand template; Console catalog view; reuse Y3-07 verify + Y1-08 wizard. Core CLI.
- Tasks: template manifest format -> registry client (reuse Y3-07) + signature verify -> param prompts (domain/upstream/etc.) -> config generation + validate + merge -> auto-HTTPS wiring (Y1-01) -> `jul template list/show/apply` -> Console catalog browser -> jul_templates_applied.
- Deps: reuse Y3-07 marketplace client/verify, Y1-08 wizard, Y1-01 ACME. Inter: Y3-07, Y1-08, Y1-01, Y2-09 Console.
- Tests: unit (manifest parse, param substitution, config gen validates, signature verify); integration (apply WordPress template -> valid config + HTTPS-ready -> merged + reload; reject unsigned); e2e (catalog browse->apply in Console).
- DoD: `jul template apply <app>` produces a validated config with auto-HTTPS for >=5 popular apps; params prompted; signed templates verified; Console catalog applies same; optional compose emitted.
- Risks: template staleness (versioned+community-maintained), signature/supply chain (reuse Y3-07 trust), app-specific quirks (notes+tested set), scope creep (curate a starter set). 
- Rollout: core CLI; public template registry; OSS-usable.
- Docs: README App templates (jul template, catalog), docs/templates.md, examples/templates, CHANGELOG.

## Y4-09 Standards: Early Hints (103), WebTransport, post-quantum TLS — SQ-EDGE P2 M
- Objective: stay at the protocol frontier: HTTP 103 Early Hints (preload), WebTransport over HTTP/3, post-quantum/hybrid TLS key exchange. Out: bleeding-edge drafts beyond these.
- Design: (1) Early Hints: emit 103 with Link: rel=preload headers before the final response — for static (push critical assets) and proxy (forward upstream 1xx). Use Go 1.24 http.ResponseController / 1xx support to write 103 then the final response. Config-driven Link hints per-location + passthrough of upstream 103. (2) WebTransport: over the Y1-11 HTTP/3 listener (quic-go), add a WebTransport session handler (webtransport-go) as a new location action ActionWebTransport (or upgrade path) bridging to a backend or plugin; reuse the h3 server. (3) PQ-TLS: enable hybrid key exchange (X25519MLKEM768) via tls.Config CurvePreferences in bind() (Go 1.24 stdlib supports ML-KEM hybrid) — just a config toggle; coexists w/ classical. 
- Config: LocationConfig += EarlyHints[]{rel,href,as} + early_hints_passthrough bool; ServerConfig/HTTP3 += webtransport bool + ActionWebTransport{backend|plugin}; TLSConfig += post_quantum bool (or curve_preferences[]).
- New: internal/handler early-hints emit in static.go/proxy.go; internal/server/webtransport.go + ActionWebTransport; tls.go CurvePreferences. Tags: webtransport (deps); Early Hints + PQ-TLS core.
- Tasks: 103 writer (ResponseController) + Link config + upstream 1xx passthrough -> WebTransport handler on h3 + ActionWebTransport + backend/plugin bridge -> PQ hybrid curve enable in tls.Config + config toggle -> negotiation/fallback (classical if peer lacks PQ) -> jul_early_hints_total, webtransport_sessions, tls_pq_handshakes_total.
- Deps: quic-go/webtransport-go (tag webtransport, reuse Y1-11 quic-go); stdlib crypto/tls (PQ) + net/http (103). Inter: Y1-11 HTTP/3, Y1-01 TLS, Y2-02 plugins (WT->plugin).
- Tests: unit (103 Link emit, curve config, WT handshake); integration (client receives 103 then 200 w/ preload; upstream 103 forwarded; WT session over h3 echoes; PQ handshake negotiated + classical fallback); interop (curl/browser where available).
- DoD: 103 Early Hints emitted (config + upstream passthrough) and honored by clients; WebTransport sessions work over HTTP/3; hybrid PQ TLS negotiated with classical fallback; all opt-in.
- Risks: 103 client/proxy support variance (opt-in + safe ignore), WT immaturity (tag + experimental), PQ interop/perf (hybrid keeps classical fallback; bench handshake cost), quic-go churn. 
- Rollout: webtransport tag (experimental); Early Hints + PQ-TLS core, off by default.
- Docs: README Standards (Early Hints, WebTransport, PQ-TLS), docs/standards.md, CHANGELOG.

## Cross-cutting Y4
- AI safety/eval: a prompt eval harness (injection red-team, PII recall, semantic-cache precision/recall, cost-accuracy) in CI; model-agnostic fixtures; provider contract tests.
- Privacy/compliance: AI data-handling docs (what's sent to providers, redaction, local-model option, no-train headers); cache/guardrail privacy review; secrets never to LLM.
- Perf: AI gateway overhead+streaming latency, semantic-cache hit latency, image-opt throughput, PQ handshake cost; CI regression gate.
- Security: provider key vaulting, SSRF (fetch+ESI+image remote), guardrail bypass red-team, WebTransport/QUIC surface; fuzz AI request parser + ESI parser.
- Docs (exhaustive, in sync): every feature updates README feature table+config ref, docs/<feature>.md, testdata/*.toml, examples/<feature>/, CHANGELOG. DoD blocks merge w/o docs. New: docs/ai-gateway.md, ai-console.md, edge-compute.md, cdn-cache.md, standards.md, templates.md.
- Build/release: tag matrix += ai, imageopt, webtransport; document size deltas; AI gateway in build-full only; min stays lean.

Year 4 DoD: AI Gateway (>=3 providers, routing+failover, streaming) + semantic cache (tokens saved) + token/cost limits+dashboards + guardrails (PII/injection/moderation/custom-WASM); AI-assisted Console (NL->validated config human-approved, anomaly detection, incident explanations); edge plugins (KV+fetch+cron+secrets, >=4 examples); CDN-grade cache (tiering+tag-purge+image-opt+ESI); 1-click app templates (>=5 apps); Early Hints+WebTransport+PQ-TLS; AI eval+privacy+perf+security gates green; CI green; docs exhaustive; build-min lean + build-full (ai/imageopt/webtransport) both work.

## Dependency graph (mermaid)
graph LR
  Action[location action model] --> AIGW[Y4-01 AI Gateway core]
  Pool[upstream Pool/Balancer] --> AIGW
  SSE[StatusWriter.Flush] --> AIGW
  AIGW --> SemC[Y4-02 semantic cache]
  AIGW --> Tok[Y4-03 token/cost]
  AIGW --> Guard[Y4-04 guardrails]
  Store[cache store iface] --> SemC
  Store --> CDN[Y4-07 CDN-grade cache]
  Limiter[Y1-03/Y3-03 Limiter] --> Tok
  WASM[Y2-02 plugins] --> Guard
  WASM --> Edge[Y4-06 edge/FaaS]
  bbolt[Y3-01 bbolt] --> Edge
  redis[Y3-03 redis] --> Edge
  redis --> SemC
  CV3[Y3-09 Console v3] --> AICon[Y4-05 AI Console]
  AIGW --> AICon
  Mkt[Y3-07 marketplace] --> Tmpl[Y4-08 templates]
  H3[Y1-11 HTTP/3] --> Std[Y4-09 standards]
  TLS[Y1-01 TLS] --> Std

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-21 | 1.0 | Added a version stamp and a **Vision horizon — demand-gated** note; flagged that the "most powerful" framing is superseded by the vision's *leanest serious gateway* positioning and that entry is via the time-boxed AI MVP. No scope change to the spec body. | The entire Year-4 spec body. | [review 2026-06-21](../reviews/); [ADR 0002](../adr/0002-protocol-adaptation.md), [ADR 0003](../adr/0003-maturity-and-ga.md) |
