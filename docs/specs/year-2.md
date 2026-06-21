<!-- Engineering execution spec. Detailed source-of-truth for the Year 2 — Protocol Gateway + Extensibility roadmap entry.
     Companion to ../roadmap/ and ../vision/. Update when a feature's design changes. -->

# JUL Engineering Execution Plan — Year 2 (Protocol Gateway + Extensibility Moat)

> Version 1.2 · Updated 2026-06-21
>
> Maturity note: shipped Y2-01…05 are **Beta**, not GA (see
> [ADR 0003](../adr/0003-maturity-and-ga.md)). gRPC transcoding + passthrough is
> the **first GA target**; most of the GA bar has now landed — published
> conformance matrices, benchmarks, known-limitations lists, a threat note, and
> parser fuzzing (see [grpc-transcoding.md](../grpc-transcoding.md) and
> [grpc-proxy.md](../grpc-proxy.md)). The remaining hard gate is the
> long-running **soak test**, so the feature stays **Beta** until then. Y2-08
> GraphQL is **deferred / demand-gated** and rescoped to explicit-resolver
> composition (see [ADR 0002](../adr/0002-protocol-adaptation.md)). The wording
> "transcoding GA" below is retained as the *target*, not a current claim.

Goal: turn JUL from "great single-node proxy" into a PROTOCOL GATEWAY + EXTENSIBLE PLATFORM. Headlines: (A) gRPC transcoding GA incl streaming, (B) WASM plugin system (the moat), (C) L4 stream proxy. Plus gRPC passthrough, service discovery, WAF, mTLS, GraphQL (exploratory), Console v2.
Exit: 3+ community WASM plugins; transcoding handles streaming; L4 TCP/UDP in prod; service discovery against K8s/Consul/DNS; mTLS + WAF available for platform teams.

Squads: SQ-PROTO (Y2-01 transcoding GA, Y2-04 gRPC passthrough, Y2-08 GraphQL) · SQ-PLUGINS new (Y2-02 WASM) · SQ-EDGE (Y2-03 L4, Y2-07 mTLS) · SQ-PROXY (Y2-05 discovery, Y2-06 WAF) · SQ-CONSOLE (Y2-09 v2).
Quarters: Q1 WASM core+ABI, transcoding streaming design, L4 TCP, discovery DNS. Q2 WASM GA+host ABI+hot reload, transcoding streaming GA, L4 UDP+SNI+PROXY, gRPC passthrough. Q3 service discovery Consul/K8s, mTLS, WAF, Console v2 log tail+plugin mgr. Q4 GraphQL exploratory, route designer, hardening+perf gate.

Build tags added Y2: wasm(plugins), stream, waf, graphql, consul, kubernetes. gRPC passthrough folds under grpc tag. mTLS in core.

## Y2-01 gRPC<->REST transcoding GA (streaming + route designer) — SQ-PROTO P0 L
- Objective: promote Y1-06 MVP to GA. Add server-streaming, client-streaming, bidi; metadata/trailers; Console route designer (Y2-09). Out: arbitrary gRPC-Web edge cases.
- Design: extend internal/transcode + handler/grpctranscode.go. Server-streaming -> NDJSON or SSE (use StatusWriter.Flush per message, google.api streaming rules). Client-streaming -> read JSON array/NDJSON body -> gRPC stream. Bidi -> WebSocket bridge (StatusWriter.Hijack) framing NDJSON. Honor grpc-timeout from location timeout; propagate metadata<->headers; map trailers (grpc-status/message) into trailer or final SSE event.
- Config: GRPCTranscodeConfig += Streaming bool, StreamMode "ndjson|sse|websocket", MaxMessageSize Size. Keep ActionGRPCTranscode.
- New/changed: transcode/streaming.go; grpctranscode.go stream paths; reuse descriptors.go/httprule.go. Tag grpc.
- Tasks: detect method streaming kind from descriptor -> NDJSON encoder w/ flush -> SSE encoder -> client-stream body decoder -> bidi WS bridge -> trailer mapping -> deadline propagation -> metadata mapping -> errors mid-stream -> jul_grpc_transcode_stream_msgs_total.
- Deps: same as Y1-06 (protobuf, grpc) + nhooyr/coder websocket (reuse if added). Inter: Y2-09 designer, Y1-04 bearer->metadata.
- Tests: unit (stream kind detect, NDJSON/SSE framing, trailer map); integration (real server-stream echo -> NDJSON+SSE; client-stream; bidi over WS); e2e example updated.
- DoD (GA bar, per [ADR 0003](../adr/0003-maturity-and-ga.md)): all 4 method kinds transcode; streaming flushes incrementally (no buffering); deadlines+trailers correct; designer round-trips descriptor->config. A published **conformance matrix** (method kind × HTTP rule × stream mode × pass/fail) + reproducible **benchmarks** + a **known-limitations** doc are mandatory before the *GA* label; until then the feature stays **Beta**. **Landed (2026-06-21):** conformance matrix + benchmarks + known-limitations + threat note now published in [docs/grpc-transcoding.md](../grpc-transcoding.md); path-template parser fuzzed (`FuzzParseTemplate`); the stale "unary" comment is corrected. Remaining GA gate: the long-running soak test. **Shipped delta:** bidi is framed as NDJSON/SSE over HTTP (no WebSocket bridge); `stream_mode` is `ndjson|sse` (the `websocket` mode in the original design was dropped).
- Risks: backpressure/slow client, half-close semantics, WS proxy infra, large messages. Mitigate limits+timeouts+flush.
- Rollout: tag grpc; remove "preview" note; streaming opt-in per location.
- Docs: README gRPC GA (streaming modes table), examples/grpc-gateway streaming, docs/grpc-transcoding.md, CHANGELOG.

## Y2-02 WASM plugin system (wazero) — SQ-PLUGINS P0 XL (THE MOAT)
- Objective: sandboxed WASM plugins as middleware AND location action; host ABI; hot reload; activates reserved [plugins]. Out: WASI networking, threads.
- Design: wazero (pure Go, no cgo -> keeps single static binary). Adopt http-wasm ABI (http-wasm/http-wasm-host-go) for net/http middleware compat + faster delivery; keep abi.go seam to add Proxy-Wasm later. Host functions: read method/path/headers/body, write response, set vars, log via slog, KV get/set, optional fetch (guarded). Guest exports handle_request/handle_response. Compile modules once (wazero CompilationCache), instantiate per-request or pooled. Sandbox: memory limit, ctx-cancel execution timeout, no host calls unless provided. Two integration points: (1) middleware in Chain (global/per-server), (2) ActionPlugin location action. Hot reload: recompile on config reload via existing atomic handler swap; module registry keyed by name+hash.
- Config: UNLOCK Plugins in validate.go (remove rejection). PluginsConfig: map name -> {path|inline_base64, type middleware|handler, config map, memory_limit Size, timeout Duration, kv bool, fetch bool, allowed_hosts[]}. LocationConfig += Plugin string (selects a handler plugin -> ActionPlugin).
- New: internal/plugins/{runtime.go,host.go,abi.go,kv.go,registry.go}; middleware/plugin.go; handler/pluginaction.go; ActionPlugin const+actionOf+validate+builder. Tag wasm.
- Tasks: vendor wazero+http-wasm-host -> module load+compile cache -> host ABI funcs -> request/response guest invoke -> memory+timeout sandbox -> KV store (in-mem, pluggable) -> guarded fetch (allowed_hosts) -> middleware integration -> ActionPlugin -> hot reload registry -> remove [plugins] rejection -> jul_plugin_invocations_total{plugin,result}, duration hist, jul_plugin_panics_total -> example plugins (header-inject, auth, transform) in TinyGo+Rust.
- Deps: tetratelabs/wazero, http-wasm/http-wasm-host-go. Inter: Y2-09 plugin manager; Console upload.
- Tests: unit (ABI host funcs, sandbox limits, registry hash reload); integration (sample wasm modifies headers/body, blocks request, panics contained, timeout kills runaway); fuzz host ABI boundary; goleak on reload.
- DoD: load .wasm at startup+reload w/o restart; plugin as middleware and as action; sandbox enforces memory+timeout; panic isolated (500, server alive); KV works; >=3 example plugins documented; still single binary.
- Risks: ABI churn (seam it), perf overhead (compile cache+pool, bench), sandbox escape (wazero capabilities only, audit), supply chain (signing deferred Y3 marketplace), fetch SSRF (allowed_hosts allowlist + block internal IPs).
- Rollout: tag wasm; [plugins] empty default; min build -> validate "wasm not compiled in".
- Docs: README Plugins + ABI reference, docs/plugins.md (authoring TinyGo+Rust, host funcs, examples), examples/plugins/*, server.toml [plugins], CHANGELOG.

## Y2-03 L4 stream proxy (TCP/UDP, SNI routing, PROXY protocol) — SQ-EDGE P1 L
- Objective: L4 reverse proxy; activates reserved [[stream]]. TCP+UDP, SNI-based routing for TLS, PROXY protocol v1/v2 emit+accept, TLS passthrough or terminate. Out: UDP for non-trivial protocols beyond datagram relay (QUIC passthrough basic).
- Design: separate stream server alongside HTTP. Reuse upstream.Pool/Balancer for L4 backends (Balancer.pick works on any backend; add dial target). TCP: accept -> optional PROXY header parse -> SNI peek (read TLS ClientHello without consuming) -> route by server_name -> dial backend (optionally send PROXY v2) -> bidirectional io.Copy. UDP: session table keyed by client addr -> relay datagrams w/ timeout. SNI routing only for TLS passthrough. Lifecycle integrated with reload (start/stop listeners via listener diff like HTTP).
- Config: UNLOCK Streams in validate.go. StreamConfig (typed, replace map[string]any): []StreamServer{listen, protocol tcp|udp, proxy_pass upstream|addr, sni_routes map[host]upstream, tls_passthrough bool, proxy_protocol in|out|both, timeouts}.
- New: internal/stream/{server.go,tcp.go,udp.go,sni.go,proxyproto.go}; config StreamConfig typed; cmd/jul/main.go start stream server; server reload integrates. Tag stream.
- Tasks: typed [[stream]] parse+validate -> TCP acceptor + io.Copy pump -> SNI ClientHello peek (buffered prefix replay) -> sni route table -> PROXY protocol v1/v2 parse+emit (reuse pires/go-proxyproto) -> UDP session map + idle GC -> upstream Pool for L4 -> reload start/stop -> metrics jul_stream_active_conns{proto}, bytes counters.
- Deps: pires/go-proxyproto; stdlib net. Inter: upstream Pool reuse; active health (Y1-05) applies to L4.
- Tests: unit (PROXY v1/v2 parse, SNI peek, UDP session GC); integration (TCP echo through proxy, SNI routes to 2 backends, PROXY header preserved client IP, UDP relay); reload start/stop no leak.
- DoD: TCP+UDP proxy work; SNI routes TLS by host w/o terminating; PROXY protocol preserves client IP both directions; survives reload; LB+health apply.
- Risks: SNI buffering edge cases, UDP statelessness/amplification (rate cap), half-open conns, PROXY spoofing (trust list). Mitigate timeouts+trusted-proxy.
- Rollout: tag stream; [[stream]] empty default.
- Docs: README L4/stream, docs/stream-proxy.md, testdata/stream.toml, examples/l4, CHANGELOG.

## Y2-04 gRPC passthrough proxy + h2c — SQ-PROTO P1 M
- Objective: transparently reverse-proxy native gRPC (HTTP/2 end-to-end, trailers, h2c cleartext) distinct from transcoding. Out: gRPC-Web translation.
- Design: extend internal/handler/proxy.go. Use golang.org/x/net/http2 Transport; h2c via http2.Transport{AllowHTTP, DialTLS=plain dial} for cleartext, or TLS h2. Detect gRPC (Content-Type application/grpc*) or explicit config. Preserve trailers (grpc-status/grpc-message), disable response buffering (flush each frame via StatusWriter.Flush), forward HTTP/2 to backend. Server must accept h2c inbound too (h2c.NewHandler wrap) for cleartext gRPC clients.
- Config: ProxyPass/LocationConfig += GRPC bool or Proxy.Mode "http|grpc"; Upstream scheme h2c:// or grpc://. Server-level h2c enable.
- New: handler/grpcproxy.go or proxy.go branch; h2c inbound wrap in server.go; balancingTransport gains http2 transport. Tag grpc.
- Tasks: h2c inbound (golang.org/x/net/http2/h2c) -> http2.Transport for backend (h2/h2c) -> trailer propagation -> per-frame flush (no buffer) -> gRPC detection/config flag -> error/status mapping -> bidi stream support -> jul_grpc_proxy_streams_total.
- Deps: golang.org/x/net/http2 (+h2c). Inter: upstream Pool; Y2-01 shares grpc tag.
- Tests: unit (trailer copy, h2c detect); integration (real gRPC client -> JUL -> gRPC server unary+streaming, trailers intact, h2c cleartext + TLS); bench streaming throughput.
- DoD: native gRPC unary+streaming pass through with trailers; h2c cleartext + TLS both; no buffering (streaming latency low); LB+health apply. **Landed (2026-06-21):** conformance matrix + benchmarks + known-limitations published in [docs/grpc-proxy.md](../grpc-proxy.md). Remaining GA gate: the long-running soak test.
- Risks: trailer support in Go proxy, h2c security (opt-in), flush correctness, HTTP/2 flow control. 
- Rollout: tag grpc; per-location grpc=true.
- Docs: README gRPC passthrough vs transcoding table, docs, testdata/grpc-proxy.toml, CHANGELOG.

## Y2-05 Service discovery / dynamic upstreams — SQ-PROXY P1 L
- Objective: dynamic backend sets from DNS/DNS-SRV/Consul/K8s; live update without reload. Out: full xDS (Y3 control plane).
- Design: ADD Pool.UpdateBackends(newBackends) preserving per-backend state (inflight/fails/downUntil) by address; ADD Pool.Close() lifecycle (also needed by Y1-05). Per-upstream refresher goroutine polls a Discoverer; on change -> UpdateBackends. Discoverer interface{ Resolve(ctx) ([]Target, error) }: dnsA (net.LookupHost), dnsSRV (net.LookupSRV), consul (catalog/health API), kubernetes (EndpointSlices via client-go or API). Reload stops old refreshers.
- Config: UpstreamConfig += Discovery{type static|dns|dns_srv|consul|kubernetes, target, refresh Duration, consul{addr,service,tag}, kubernetes{namespace,service,port}}.
- New: internal/upstream/{discovery.go,disco_dns.go,disco_consul.go,disco_k8s.go}; pool.go += UpdateBackends+Close; main factory tracks pools. Tags: consul, kubernetes (dns in core).
- Tasks: UpdateBackends state-preserving merge -> Pool.Close -> Discoverer interface -> DNS/SRV resolver -> Consul provider -> K8s EndpointSlice provider -> refresher goroutine + jitter -> reload lifecycle -> jul_upstream_backends{pool} gauge + discovery_errors_total.
- Deps: hashicorp/consul/api (tag consul), k8s.io/client-go (tag kubernetes). Inter: Y1-05 active health composes; Pool.Close shared.
- Tests: unit (UpdateBackends preserves state, merge add/remove); integration (DNS via test resolver, Consul via dev agent, K8s via envtest/fake); goleak on reload.
- DoD: backends update live from each provider without reload; state preserved across updates; health+LB apply; refreshers stop on reload; metrics reflect counts.
- Risks: provider downtime (keep last-good), churn/flapping (debounce), client-go binary bloat (build tag), DNS TTL. 
- Rollout: tags consul/kubernetes; dns in core; default static.
- Docs: README Service discovery, docs/service-discovery.md, testdata, examples/k8s, CHANGELOG.

## Y2-06 WAF (Coraza + OWASP CRS) — SQ-PROXY/SEC P2 L
- Objective: ModSecurity-compatible WAF; OWASP CRS bundle; block/detect; per-location. Enterprise hook. Out: custom ML rules (Y5 bot mgmt).
- Design: Coraza v3 (pure Go, SecLang+CRS). Middleware wrapping request+response phases (1-5): process URI/headers/body in, headers/body out; on interruption -> configured status (403) or detect-log. Body buffering up to limit for inspection (interacts with BodyLimit + compression ordering: WAF before compression, after auth). Optionally embed CRS via go:embed for zero-setup. Per-location enable like Auth modifier.
- Config: WAFConfig (global + LocationConfig.WAF override): enabled, mode block|detect, directives_files[], inline_rules, crs_enabled, paranoia int, request_body_limit Size, response_body_check bool.
- New: internal/waf/{waf.go,middleware.go}, optional embedded CRS assets; LocationConfig.WAF. Tag waf.
- Tasks: vendor coraza -> build WAF from directives+inline -> optional embedded CRS -> middleware phases in+out -> body buffering w/ limit -> interruption->status/log -> per-location override -> audit log sink (reuse Y1-10 sinks) -> jul_waf_events_total{rule,action}.
- Deps: corazawaf/coraza/v3 (+coreruleset bundle). Inter: Y1-02 ordering, Y1-10 audit sink, Y1-03 body limit.
- Tests: unit (rule fires block/detect, body limit); integration (CRS blocks SQLi/XSS payloads, detect mode logs only, allowed traffic passes); perf overhead bench.
- DoD: CRS loads+blocks OWASP test payloads; detect mode logs w/o blocking; per-location toggle; body inspection within limit; audit events emitted; off by default.
- Risks: false positives (paranoia tuning + detect-first rollout), perf (body buffering -> bench+limits), CRS update cadence (Y3 managed updates enterprise), large bodies. 
- Rollout: tag waf; disabled default; recommend detect->block.
- Docs: README WAF + CRS quickstart, docs/waf.md (tuning, paranoia), testdata/waf.toml, CHANGELOG.

## Y2-07 mTLS client auth + identity vars — SQ-EDGE P2 M
- Objective: mutual TLS; verify client certs against CA; expose identity ($ssl_client_*) for headers/auth. Out: SPIFFE/SVID (Y5 mesh).
- Design: extend internal/server/tls.go bind() tls.Config: ClientAuth (NoClientCert|RequestClientCert|RequireAndVerifyClientCert), ClientCAs x509 pool, optional VerifyPeerCertificate for custom checks (SAN allowlist, OCSP/CRL). Per-server config; per-location "require client cert" enforced in a middleware reading r.TLS.PeerCertificates -> 403 if absent. Identity vars (subject CN, SAN, serial, fingerprint) into request context (reuse Y1-04 claims pattern) -> available as proxy header vars $ssl_client_s_dn etc.
- Config: TLSConfig += ClientAuth{mode none|request|require, ca_file, verify_san[], crl_file}. LocationConfig += RequireClientCert bool. ProxyHeaders may reference $ssl_client_*.
- New: tls.go ClientAuth wiring; middleware/clientcert.go (enforce+extract vars). Core (no heavy deps).
- Tasks: load ClientCAs -> set ClientAuth per server -> VerifyPeerCertificate (SAN allowlist, optional CRL) -> per-location require enforcement -> extract identity vars to context -> proxy var resolver $ssl_client_* -> jul_mtls_handshakes_total{result}.
- Deps: stdlib crypto/x509. Inter: Y1-04 context/var pattern, Y1-01 cert provider coexists (server cert via ACME, client CA separate).
- Tests: unit (CA verify pass/fail, SAN allowlist, var extraction); integration (client w/ valid cert allowed + vars set, no cert -> handshake/403, wrong CA rejected, CRL revoked rejected); e2e testdata/mtls.toml.
- DoD: require mode rejects missing/invalid client certs; identity vars populate proxy headers; per-location require works; CRL/SAN checks function; coexists with ACME server certs.
- Risks: CRL/OCSP freshness, cert rotation, perf of verify, mixing per-server modes. 
- Rollout: core; per-server/per-location opt-in.
- Docs: README mTLS + identity vars table, docs/mtls.md, testdata/mtls.toml, CHANGELOG.

## Y2-08 GraphQL composition prototype (explicit resolvers) — SQ-PROTO · DEFERRED / demand-gated · L
- Status: **Deferred** behind the GraphQL evidence gate — built only when users need BFF/composition over existing REST/gRPC (see [ADR 0002](../adr/0002-protocol-adaptation.md) and [ADR 0003](../adr/0003-maturity-and-ga.md)). Not on the committed Year-2 track.
- Objective: a schema-first GraphQL composition layer with **explicit resolvers** over REST/gRPC backends; persisted queries; depth/complexity limits. Explicitly **NOT** "GraphQL without resolvers" and **NOT** auto-generated from proto/OpenAPI.
- Scope guardrails (ADR 0002): Query + Mutation only (no Subscriptions); resolvers map to gRPC/REST **unary** calls; **no federation**; depth + complexity limits and resolver tracing are required from day one.
- Design: schema-first gateway using gqlgen or graphql-go executor with a DECLARATIVE resolver map (field -> REST endpoint or gRPC method via Y2-01 transcoder). New ActionGraphQL location action serving /graphql (POST + GET introspection toggle). Add query depth+complexity limiting, persisted-query allowlist, APQ. Batch/dataloader for N+1. Alternative delivery as a WASM plugin (Y2-02) remains possible.
- Config: GraphQLConfig on LocationConfig (ActionGraphQL): schema_file, resolvers (field->{rest{url,method}|grpc{target,method}}), max_depth, max_complexity, introspection bool, persisted_only bool.
- New: internal/handler/graphql.go + ActionGraphQL; internal/graphql/{schema.go,resolve.go,limits.go}. Tag graphql.
- Tasks: schema load -> executor wiring -> declarative resolver -> REST resolver -> gRPC resolver (reuse transcode invoke) -> dataloader batching -> depth+complexity analyzer -> persisted queries/APQ -> introspection toggle -> jul_graphql_requests_total, resolver_errors_total.
- Deps: 99designs/gqlgen or graph-gophers/graphql-go; dataloader lib. Inter: Y2-01 gRPC invoke; Y2-02 alt delivery.
- Tests: unit (depth/complexity reject, resolver mapping); integration (query spanning REST+gRPC backends returns merged JSON, persisted-only blocks ad-hoc, introspection toggle).
- DoD: declarative schema with explicit resolvers serves queries over >=1 REST + >=1 gRPC backend; depth+complexity limits enforced; persisted queries; introspection toggle; documented limitations. Stays **Prototype/Alpha** until the gate trips and the GA bar is met.
- Risks: schema stitching complexity, N+1 (dataloader), security (introspection/complexity DoS), scope creep -> keep declarative explicit-resolver only, defer federation. 
- Rollout: tag graphql; clearly "deferred/experimental"; off by default; not built unless the gate trips.
- Docs: README GraphQL (deferred, explicit-resolver scope+limits), docs/graphql.md, examples/graphql, CHANGELOG.

## Y2-09 Console v2 (log tail, plugin manager, route designer) — SQ-CONSOLE P0/P1 L
- Objective: extend Console v1 with live log tail, WASM plugin manager, gRPC transcoding route designer, richer dashboards. Out: RBAC/SSO+multi-node (Y3).
- Design: build on admin.Server + Preact/Svelte SPA (Y1-07). (1) Log tail: add in-memory ring-buffer log sink (extends Y1-10 sinks) + GET /api/logs/stream (SSE) -> live tail view w/ filter. (2) Plugin manager: /api/plugins (list/enable/disable/upload .wasm) backed by Y2-02 registry + config writes via existing /api/config -> shows invocations/errors metrics. (3) Route designer: /api/transcode/descriptors (upload FileDescriptorSet) -> parse google.api.http -> visual mapping editor -> generate GRPCTranscodeConfig (Y2-01). (4) Dashboards: per-upstream health (Y1-05), stream (Y2-03) + WAF (Y2-06) panels. Seed AI-assisted config stub (suggest config snippets) per REV3 (lightweight, not the Y4 AI pillar).
- Config: AdminConfig += LogRingSize int; reuse Console flag.
- New: web/console views; admin/{logs.go,plugins.go,transcode.go}; observability ring sink; extend Deps. Tag console.
- Tasks: ring-buffer sink + SSE /api/logs/stream -> log tail UI+filter -> /api/plugins CRUD + upload (size/type validate) -> plugin metrics panel -> descriptor upload+parse -> route designer UI -> config gen+validate-before-apply -> stream/WAF/upstream panels -> AI-suggest stub -> Playwright e2e -> CSP for uploads.
- Deps: build-time SPA; Go go:embed. Inter: Y1-07 base, Y1-10 sinks, Y2-01, Y2-02, Y2-03, Y2-06.
- Tests: unit (ring buffer, descriptor parse, config gen); integration (SSE log stream auth, plugin enable->reload, designer produces valid config); e2e Playwright all views.
- DoD: live log tail streams w/ filter; upload+enable/disable plugin hot (no restart); designer turns descriptor into valid transcoding config applied via reload; new panels live; auth enforced; single binary.
- Risks: SSE scale, upload security (validate+size+sandbox), designer complexity, log PII (filter/redact). 
- Rollout: tag console; builds on v1.
- Docs: README Console v2 (screens), docs/console.md update, CHANGELOG.

## Cross-cutting Y2
- Perf: extend harness with L4 throughput, gRPC streaming, WASM per-invocation overhead, WAF cost; CI regression gate stays green.
- Console-first ([ADR 0004](../adr/0004-console-ui-invariants.md)): every Y2 feature ships a lean, self-explanatory Console surface as part of DoD; "done" includes operable + observable from the Console without reading docs. Console v2 (Y2-09) is the sum of these per-feature panels, not a separate monolith.
- Secrets references (SEC-1, pulled earlier from Y5-06): introduce `env`/`file` secret refs + log redaction + a lint for literal secrets, used by ACME/JWT/forward-auth/mTLS (and the AI MVP key). Vault/KMS/SPIFFE remain later.
- Maturity/GA ([ADR 0003](../adr/0003-maturity-and-ga.md)): shipped Y2 features are **Beta**; the *GA* label requires the full GA bar (conformance matrix, benchmarks, known-limitations, semver-stable contract, soak test, runnable example+docs, security/threat note, fuzzing, Console surface). gRPC transcoding + passthrough is the first GA target.
- Security: WASM sandbox threat model + audit; WAF tuning guide; mTLS verify paths; fetch SSRF allowlist; fuzz transcode httprule + plugin ABI + PROXY parser.
- Docs (exhaustive, in sync): every feature updates README feature table+config ref, docs/<feature>.md, testdata/*.toml, examples/<feature>/, CHANGELOG. DoD blocks merge w/o docs.
- Build/release: extend tag matrix (wasm,stream,waf,graphql,consul,kubernetes); build-min stays lean; document each tag's deps/size delta.

Year 2 DoD: gRPC transcoding GA (4 stream kinds) + passthrough; WASM plugin system w/ >=3 examples + Console plugin manager; L4 TCP/UDP+SNI+PROXY; service discovery DNS/Consul/K8s; mTLS + WAF available; GraphQL experimental documented; Console v2 (log tail+plugin mgr+route designer); reserved [plugins]+[[stream]] unlocked; perf gate green; CI tests+race+lint+vuln green; docs exhaustive; build-min+build-full both work.

## Dependency graph (mermaid)
graph LR
  Y106[Y1-06 transcode MVP] --> Y201[Y2-01 transcode GA]
  Y201 --> RD[Y2-09 route designer]
  WASM[Y2-02 WASM plugins] --> PM[Y2-09 plugin mgr]
  Y110[Y1-10 sinks] --> LT[Y2-09 log tail]
  Pool[upstream Pool +Close/+UpdateBackends] --> Y205[Y2-05 discovery]
  Pool --> Y105[Y1-05 active health]
  Y205 --> Y105
  TLS[Y1-01 tls.go] --> Y207[Y2-07 mTLS]
  Y201 --> Y208[Y2-08 GraphQL]
  WASM --> Y208

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-21 | 1.2 | Recorded the first-GA-target evidence that landed for Y2-01 transcoding and Y2-04 passthrough: published conformance matrices, benchmarks, known-limitations lists, a threat note, and path-template fuzzing; marked the stale `unary` comment fixed; noted the shipped bidi-framing delta (NDJSON/SSE over HTTP, no WebSocket; `stream_mode` = `ndjson\|sse`). | Both features stay **Beta** (soak test is the remaining GA gate); all squad/quarter plans and other feature specs unchanged. | [grpc-transcoding.md](../grpc-transcoding.md), [grpc-proxy.md](../grpc-proxy.md); [ADR 0003](../adr/0003-maturity-and-ga.md) |
| 2026-06-21 | 1.1 | Added a maturity note (Y2-01…05 are Beta, not GA); set gRPC transcoding + passthrough as the first GA target with a mandatory conformance matrix in DoD; rescoped Y2-08 from "GraphQL without resolvers" to a **deferred, demand-gated** explicit-resolver composition prototype; added Console-first (per-feature panels), secrets-references (SEC-1), and GA-bar cross-cutting items; flagged the stale `unary` comment in handler/grpctranscode.go as a code follow-up. | All squad/quarter plans, feature designs, configs, tasks, tests, dependency graph, and the other feature sections. | [review 2026-06-21](../reviews/); [ADR 0002](../adr/0002-protocol-adaptation.md), [ADR 0003](../adr/0003-maturity-and-ga.md), [ADR 0004](../adr/0004-console-ui-invariants.md) |
| 2026-06-21 | 1.0 | Initial Year-2 engineering execution spec. | — | — |
