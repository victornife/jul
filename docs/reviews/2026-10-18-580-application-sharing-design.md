# #580 application-sharing design — review packet

> **Reviewed:** design branch `design/580-application-sharing-contract`; source
> baseline `main` at `64e4a8b29230134c5dee37dfd8bfa130a9580a2a` (2026-10-18).
> Design revisions: **r1** `e5b77b42` (round 1), **r2** `75264d61` (round 2),
> **r3** `fe44f6a0` (round 3, §6.4), **r4** `a76f197e` (independent principal
> review, §6.5), **r5** `c53b14b5` (round 5, §6.6), **r6** `ecde75fa` (round 6,
> §6.7), **r7** `7e3382c4` (round 7, §6.8), **r8** `292a294c` (round 8, §6.8;
> independent principal re-review, §6.9) and **r9** (this revision; round 10 in
> §6.10). This packet holds evidence, decisions, review findings and
> traceability for the
> [specification](../specs/application-sharing.md) and
> [ADR 0025](../adr/0025-application-sharing-contract.md). It certifies nothing
> about runtime behaviour beyond the executed checks in §5.

## 1. Baseline and inputs

- **Source:** `main` = `origin/main` = `64e4a8b2…` (merge of #637). Published
  package v2.2.0 (`b12e0511…`) was not re-inspected; behaviour below is source
  behaviour unless stated. Checks ran with Go 1.27.2 on linux/arm64: lean build
  for `go test`, full-tag build for `jul check`/`jul lint`.
- **Issues refreshed 2026-10-18 10:34 CEST:** #580, #579 (with comments), #62
  (body and latest comments), #581–#589, #613, #562, #563, #620–#629, #633,
  #634, #441, #615. #580 was ready and unstarted. #434 (TCP ALPN routing,
  Alpha/unreleased) completed after the planning baseline and adds no sharing
  prerequisite.
- **Documents read:** brief and longer vision, practical guide, operating model,
  roadmap, specs index, ADRs 0009, 0011–0016, 0018 (references), 0019, 0022;
  known limitations, security posture, reload semantics, mTLS, auth, HTTP/3,
  stream, soak procedures, accessibility, CONTRIBUTING, PR template.

## 2. Evidence ledger

Kinds: **F** observed source or test fact; **D** accepted decision (ADR or
issue); **H** hypothesis. Line numbers refer to the source baseline. E10 was
corrected during review (finding S-3); the original wrong statement is kept in
the row.

| ID | Statement | Source | Kind | Limitation | Conf. |
| --- | --- | --- | --- | --- | --- |
| E01 | `route_id` optional `*string`, 1-64 `[a-z0-9_-]`, globally unique; minted only on managed structured create | internal/config/schema.go:457-468; validate_route_id.go; patch_types.go:103-120 | F | file-owned preview never mints | high |
| E02 | Jul never writes IDs into config it does not own (I1); no identity sidecar (I2) | ADR 0019 §6 | D | — | high |
| E03 | Server `name` is descriptive, not identity; vhost is a revision-scoped selector | ADR 0019 §5.3; lifecycle `servers.name` hot_reload | D/F | no uniqueness | high |
| E04 | Console Apps = upstream pools (`AppProjection`), "An app is a named pool of backend instances" | projection_types.go:153; AppDetail.tsx:473 | F | static sites have no upstream | high |
| E05 | No exposure/audience/public model exists in code or UI | explore grep internal/, ui/src | F | grep-based | medium-high |
| E06 | Location auth: CIDR (canonical client address) then at most one of basic/jwt/forward; 403/401/503 | internal/auth/auth.go:112-170; validate.go:462-477 | F | — | high |
| E07 | JWT read only from single `Authorization: Bearer`; exp required, 60s leeway; JWKS 15 min refresh, 1 h stale grace | internal/auth/jwt.go:22-60,103-116,145-205 | F (verified) | no Cf-Access-Jwt-Assertion/cookie | high |
| E08 | Forward-auth GET probe copies inbound headers incl. cookies; fail-closed 503; strips client copies of `auth_response_headers` | internal/auth/forward.go:27-123; auth.go:161-170 | F | other identity headers not stripped | high |
| E09 | X-Forwarded-* cleared/rebuilt on proxy; RFC 9440 client-cert headers stripped unconditionally | handler/proxy.go:695-720; ADR 0016 §12 | F/D | arbitrary operator header names not sanitized | high |
| E10 | Vhost selection by normalized Host (lowercase, port and trailing dot removed); an unmatched Host falls back to the **first-declared** block on the listener (not a nameless block, not 404); 404 only when the listener has no blocks; no SNI≠Host check. Corrected 2026-10-18 after review finding S-3 (original entry said nameless-default/404) | router/router.go:60-85,270-292 (`def` = first declared); router_test.go `TestHostRouting`; host.go:15-18 | F (verified) | domain fronting not rejected | high |
| E11 | client_auth mode none/request/require is listener-wide (strongest wins, CAs/CRLs unioned); `require_client_cert` per location (403) | docs/mtls.md:91-130; schema.go:1105-1140 | F/D | public + mTLS on same listener needs `request` | high |
| E12 | CA/CRL reload applies to new full and resumed handshakes; established TLS keeps identity (`TestLongLivedMTLSStreamSurvivesClientRevocation`) | server/mtls.go:340-355; mtls_reload_test.go:85 | F | — | high |
| E13 | Auth wraps the action; cache sits inside auth | internal/app/factory.go:648-720 | F (verified) | — | high |
| E14 | Cache key = method + raw Host + RequestURI; reload does not invalidate; Set-Cookie never stored; Authorization responses only with public/s-maxage; cookie sessions unprotected | cache.go:480-481,801-808,1068-1078,1270-1282; known-limitations.md:335-420 | F | cross-user leak possible for cookie/mTLS/CIDR-restricted cacheable responses | high |
| E15 | Reload publishes new handler generation atomically; new H1 requests and new H2/H3 streams on existing connections use it; in-flight exchanges keep old generation | reload-semantics.md:711-759; reload_test.go | F | — | high |
| E16 | Hijacked H1 WebSocket survives HTTP drain until it ends or process exit; no max connection age; no targeted termination API | known-limitations.md:436-459; reload-semantics.md WS-S | F | — | high |
| E17 | HTTP/3 0-RTT disabled (`Allow0RTT=false`); session tickets appear enabled by Go default (no direct test) | docs/http3.md:119,150-157 | F/H | ticket test not found | medium |
| E18 | WebSocket only via H1 Upgrade; no H2/H3 Extended CONNECT; no HTTP/3 to origin; native gRPC to origin via h2/h2c | known-limitations.md:503-509; grpc-proxy.md | F | — | high |
| E19 | Lifecycle: location auth/require_client_cert/proxy_pass/route_id/server_names hot_reload; servers.listen new_listener_only; config_authority & admin.listen restart_required | docs/generated/config-lifecycle.json | F | — | high |
| E20 | Apply outcomes: applied_live, applied_degraded, no_change, not_applied, saved_not_live; poll /api/v1/config/applies/{apply_id}; Idempotency-Key replay; ledger process-local | server/reload_result.go:15-35; adminapi/v1_mutation.go; admin-api.md:258-284 | F | ledger lost on restart | high |
| E21 | config_state enum incl. file_owned_desired_ahead, managed_drift…; file-owned mutation = 409 config_authority_read_only; preview allowed in file-owned | ADR 0019 §15-16; adminapi/errors.go | F/D | — | high |
| E22 | Typed ops include location_set_auth/clear_auth/set_action/remove/toggle_require_client_cert; selector + base_version targeting | admin/patch.go; patch_types.go:28-50 | F | — | high |
| E23 | Admin is a separate listener; lint flags non-loopback admin without auth/TLS | server/reload_plan.go:217; config/lint.go:200-224 | F | lint findings lack stable codes | medium-high |
| E24 | Egress allow-list covers JWKS/forward-auth/discovery; no doctor path fetches arbitrary URLs | egress/policy.go:27-43; doctor/checks.go | F | — | high |
| E25 | Metrics: jul_listener_conns, jul_http_requests_in_flight, jul_http3_connections, jul_stream_active_conns, jul_upstream_*; Go/process collectors (go_goroutines, process_open_fds, process_resident_memory_bytes) used by soak; pprof gated by admin:manage | metrics-contract.json; scripts/soak-scrape.go; diagnostics.md:229 | F | no WS session gauge | high |
| E26 | Tools: scripts/burn-in-load.go, soak.sh, fault-evidence.sh (/proc sampling), bench.sh; config-check validates examples/ with jul check; scripts/docs-check.py | Makefile; scripts/ | F | no k6/hey | high |
| E27 | Admin OIDC (#441/#615, ADR 0022) authenticates administrators only; no application audiences | ADR 0022 | D | Beta/unreleased | high |
| E28 | Stream ALPN routing (#434) Alpha/unreleased; [[stream]] never terminates TLS | stream-alpn.md; ADR 0016 §11 | F/D | — | high |
| E29 | #563/#562 candidates: activate only with explicit requirement and accepted availability cost | issues #562/#563 | D | — | high |
| E30 | #633 consumes only implemented versioned intent; #634 fixture assertions are test expectations, not deployed policy | issues #633/#634 | D | — | high |
| E31 | After CRL revocation reload (client_auth=require), a NEW request on the already-established connection still succeeds (H1 keep-alive and H2: 200, reused=true); a new handshake is rejected (`bad certificate`) | Disposable probe P1, 2026-10-18, lean build, go1.27.2 linux/arm64, loopback, fixture newMTLSReloadFixtureWithHandler; file deleted after run | F (probe) | loopback; synthetic CA; no H3; handler-level (not location require_client_cert) | high |
| E32 | Existing tests pass at 64e4a8b2: TestLongLivedMTLSStreamSurvivesClientRevocation (h1,h2), TestReloadRefreshesClientCRLWithoutRestart, TestReloadRotatesClientCAWithoutRestart, TestReloadSwapsHandler, TestReloadDrainsBeforeRetiringClosers, TestLongLivedWebSocketReloadAndShutdown, TestLongLivedSSEReloadAndShutdown | go test -count=1 ./internal/server ./internal/handler -run ..., 2026-10-18 | F (executed) | lean build only | high |
| E33 | Default server idle_timeout 60s (activity resets it); read/write timeouts default 0 for long-lived streams | internal/server/server.go:1776-1790 | F | — | high |
| E34 | htpasswd loaded at reload; a Basic `file` forces the normal (non-no-op) reload path; the watcher watches server.toml only, so credential-file edits need SIGHUP/config touch (file_owned) or managed Apply/restart (managed) | internal/server/serving_change.go:189-223; docs/auth.md:122; docs/mtls.md:226-232 | F/D | managed identical-bytes apply not re-run here | high |
| E35 | No 0-RTT anywhere: Go TCP TLS server accepts early data only in QUIC mode; Jul QUIC Allow0RTT=false | GOROOT crypto/tls/handshake_server_tls13.go:160-170; docs/http3.md:119 | F | — | high |
| E36 | Managed mode: SIGHUP/watcher do not reload, only re-assess drift; legacy POST /reload unavailable | docs/reload-semantics.md:146-170; mtls.md:232 | D/F | — | high |
| E37 | Router canonicalises the decoded path (dot segments, repeated slashes) before location selection and re-escapes the forwarded target, so `/public/../admin` and percent-encoded variants select `/admin` | internal/router/canonical.go:10-60; canonical_test.go | F | — | high |
| E38 | WASM plugins wrap outermost (before auth) and the CORS preflight terminator sits outside auth (approved preflights skip it); a plugin that generates a response is not stopped by a location deny | internal/app/factory.go:648-720 | F | plugin behaviour is plugin-specific | high |
| E39 | `require_client_cert` checks only that `r.TLS.PeerCertificates` is non-empty (connection state); no per-request revocation check | internal/middleware/clientcert.go:60-80 | F | — | high |
| E40 | `MarkFailedApply` sets `managed_failed_apply` **before** the restoration decision; restoration may then fail (`managed_inconsistent`). The terminal apply result carries `restored` and `restore_error` | internal/app/managed_baseline.go:725-741; internal/app/config_apply.go:878-906; internal/adminapi/v1_mutation.go:40-41; `TestManagedFailedApplyObservableDuringRestorationWindow` | F (found by principal review R01; verified) | — | high |
| E41 | `file_owned_clean` is returned for no configured path, an unreadable or missing file, or a valid file; it never compares the file with the serving generation | internal/app/config_apply_baseline.go:171-198 | F (R01; verified) | — | high |
| E42 | `redirect`/`permanent` rewrites answer in the router after location selection and before the location handler (so before auth); a server-level `redirect_https` answers plaintext requests before location selection | internal/router/router.go:290-325; internal/router/matcher.go:19-45 | F (R04; verified) | — | high |
| E43 | Console v2 and `/api/v1` projections load the saved file (`deps.LoadConfig = src.Load`); the server holds an atomically published serving snapshot (`LiveSnapshot`: `EffectiveConfig`, pre-expansion `RawConfig`, `Generation`) | internal/admin/api.go:74-91; internal/app/serve.go:409-420; internal/admin/apiv1_resources.go:200-213; internal/server/server.go:550-584 | F (R05; verified) | — | high |
| E44 | Compatibility guarantees apply to GA features within a MAJOR line; internal `/api/*` routes may change in any release | docs/compatibility.md:50-80 | D | — | high |
| E45 | An empty CIDR `allow` list admits every address; only a non-empty allow list restricts | internal/auth/cidr.go:18-21,44-68 | F (round 5 S5-1) | — | high |
| E46 | `client_auth` `request` admits clients with or without a certificate (`VerifyClientCertIfGiven`); only `require` rejects them | internal/server/mtls.go:37-50 | F (round 5 P5-5) | — | high |
| E47 | One process-wide response cache wraps every cache-enabled location; its key has no listener or entry component | internal/app/serve.go:124-128; internal/app/factory.go:398-401; internal/cache/cache.go:478-485 | F (round 5 P5-3) | — | high |
| E48 | `BeginWrite` changes only the on-disk marker; in-memory status can read `managed_clean` while an apply is in flight | internal/app/managed_baseline.go:175-190; internal/app/config_apply.go:716-747 | F (round 5 P5-6) | — | high |
| E49 | WAF rule files and inline rules are post-CRS tuning that may hold exclusions and allow rules. Detect mode emits `SecRuleEngine DetectionOnly`, in which the engine never interrupts a request, and Coraza's constructor then forces the request and response body-limit actions to `ProcessPartial`, so detect mode does **not** reject oversized bodies (probe P2: `block` 413, `detect` 200 with the whole body delivered). **Corrected in round 9 (RR01):** r7 and r8 claimed the opposite from the transaction's reject branch and default alone, missing the constructor rule. A location `waf` override replaces the global `[waf]` wholesale | internal/config/schema.go:180-200; internal/waf/firewall.go:54-90,212-228; internal/app/wiring.go:112-116; coraza v3.8.1 waf.go:107-117, internal/corazawaf/transaction.go:335-363 | F (rounds 6 and 9; P2) | transport errors while reading a body fail the request with or without the WAF | high |
| E50 | Each listener binds exactly one address (`net.Listen("tcp", addr)`); moving it from loopback to another address stops the loopback address answering | internal/server/server.go:742-752 | F (round 6, P5-4 reopened) | — | high |
| E51 | Coraza's `ctl:ruleEngine` action changes the engine for one transaction, so a rule can switch blocking on in detect mode; in DetectionOnly mode `allow` is not enforced. The embedded CRS v4.25.0 contains no `ctl:ruleEngine` | coraza v3.8.1 internal/actions/ctl.go:261-271, internal/corazawaf/transaction.go:353-363; coraza-coreruleset v4.25.0 (no match) | F (round 9) | operators' rule files may use it | high |
| E52 | Host dispatch: each block scores the request host (3 exact, 2 leading wildcard matching any deeper name, 0 none) after lower-casing and trimming a trailing dot; the highest score wins, ties go to the earlier-declared block, and no match falls to the listener's first-declared block | internal/router/host.go:20-39; internal/router/router.go:73-85 | F (round 9, RR04; the reviewer's real-router probe: 410 → 200 after renaming only an exact host) | — | high |
| E53 | `serving_version` and `desired_version` are both `CanonicalVersion`: the first 8 bytes of SHA-256 over the canonical marshalled **effective** configuration. Equal versions mean an identical effective configuration; the contents of referenced files are not included | internal/server/reload_result.go:101-111; internal/app/config_apply.go:624-625; internal/app/config_apply_restore.go:34 | F (round 9) | 64-bit truncation; not a practical concern for equality checks | high |

### External primary sources

Collected 2026-10-18 by read-only research. tailscale.com was unreachable from
the research network; Tailscale facts come from source at commit `e3c3ef18` and
must be re-checked against the knowledge base by #582.

| ID | Statement | Source | Conf. |
| --- | --- | --- | --- |
| X01 | Envoy max_connection_duration (unset default) starts drain; drain never force-terminates active streams; upgraded WS reset only on proactive server-wide drain; max_stream_duration resets streams | envoyproxy.io protocol.proto, faq timeouts, draining; conn_manager_impl.cc@45f1e100 | high |
| X02 | NGINX keepalive_time (1h default) closes keep-alive/H2/H3 connections at next request only; does not bound upgraded WebSockets; WS bounded by idle proxy_read_timeout | nginx.org core/websocket/proxy docs; source@1725db90 | high (WS: medium) |
| X03 | Go 1.27.2 net/http: no public per-connection graceful close; HTTP/2 GOAWAY only internal or via `Connection: close` response header on a new request; Shutdown ignores hijacked conns | go1.27.2 server.go, internal/http2/server.go | high |
| X04 | quic-go v0.64.0 http3: GOAWAY only server-wide on Shutdown; per-connection only CloseWithError (abrupt) | quic-go http3/server.go, connection.go | high |
| X05 | Cloudflare Access app token valid for its lifetime (default 24h app/global session) unless revoked; per-app "Revoke existing tokens", per-user revoke, logout 20–30 s; established WS on revocation undocumented | developers.cloudflare.com session-management | high (WS: unverified) |
| X06 | Tailscale packet filter accepts non-SYN TCP; established flows survive ACL changes (issue #859 open since 2020) | tailscale filter.go@e3c3ef18; GH issue 859 | high |
| X07 | Grafana Live uses WebSockets by default (one per tab); Jellyfin requires WebSockets | grafana.com docs; jellyfin.org reverse-proxy docs | high |
| X08 | Serve terminates TLS in tailscaled; forwards original Host; overwrites X-Forwarded-For with peer IP; strips client Tailscale-User-*; sets them only for user-owned devices (not tagged, not Funnel); Authorization passes through | tailscale ipn/ipnlocal/serve.go@e3c3ef18 | high |
| X09 | Serve: WebSockets and streaming via Go ReverseProxy; h2c to backend only for gRPC content-type; no HTTP/3 code found; --tcp/--tls-terminated-tcp exist | serve.go; serve_v2.go | high (H3 unverified) |
| X10 | Funnel requires node `funnel` attribute; `tailscale funnel status` / `serve status --json` (AllowFunnel empty) verify it is off | tailscale ipn/serve.go, cli/funnel.go | high |
| X11 | Access token delivered as Cf-Access-Jwt-Assertion header + CF_Authorization cookie; never documented as Authorization: Bearer; keys at <team>.cloudflareaccess.com/cdn-cgi/access/certs, rotate 6 weeks | developers.cloudflare.com application-token, validating-json | high |
| X12 | cloudflared originRequest.access {required, teamName, audTag} validates signature/iss/exp/aud before proxying; 403 missing/aud mismatch, 502 other failures; needs outbound 443 to team domain | cloudflared proxy.go, jwtvalidator.go@8e090e5b; tunnel docs | high |
| X13 | Create the Access app before the tunnel route or the hostname is public; validate token in cloudflared or origin | developers.cloudflare.com self-hosted-public-app | high |
| X14 | Tunnel: WebSockets supported; SSE flushed; public-hostname gRPC unsupported and Access ignores gRPC; http2Origin needs HTTPS origin; HTTP/3 to origin unsupported; upload cap 100 MB Free/Pro | developers.cloudflare.com websockets, grpc, origin-parameters, http3; cloudflared connection.go | high |
| X15 | `cloudflared` pins go-oidc v3.17.0; `RemoteKeySet.verify` tries cached keys first and fetches only on a miss; a **successful** fetch replaces the cached set, while a failed fetch leaves it intact (corrected in round 5, P5-2) | cloudflared go.mod@8e090e5b; go-oidc oidc/jwks.go@35b8e031 lines 160-219 | high (source; no live outage test) |

### Contradictions and observations

| ID | Observation | Disposition |
| --- | --- | --- |
| O1 | `TLSConfig.ClientAuth` Go comment says client-auth changes need a restart; lifecycle registry and #486 tests show hot reload | not changed (unrelated code comment); spec §17.5 |
| O2 | `jul lint` does not warn when a `request`-mode client-auth listener also serves open entries | candidate warning (spec §4.4, §17.5) |
| O3 | Jul's response `cache` is rejected on static `root` locations | F1 uses `cache_control`; spec §11 |
| O4 | `TestLongLivedMTLSStreamSurvivesClientRevocation` covers an established stream and a new handshake, not a new request on the open connection | settled by probe P1 (§4); docs clarified |
| O5 | Author's ledger E10 (from an explore pass and a misread spot check) claimed nameless-default/404 for unmatched Host | corrected after S-3 with source and test evidence |

## 3. Decision log

| ID | Date | Question | Answer and owner | Status | Affected |
| --- | --- | --- | --- | --- | --- |
| SD-01 | 2026-10-18 | Where does intent live; what is the unit? | A: derived view, unit = entry, no store — maintainer | Accepted; intent timing superseded by SD-05 | spec §4.1; ADR D1 |
| SD-02 | 2026-10-18 | Withdrawal promise; #562/#563 | D+E: today's promise displayed; #563 with #562 selected for event-driven entry-scoped retirement on narrowing; gates #587/#613 claims — maintainer, after lifecycle tests, probe P1 and industry/provider research | Accepted | spec §8; ADR D4 |
| SD-03 | 2026-10-18 | Private-network membership as audience | A: coarse, provider-enforced, never named identity — maintainer | Accepted | spec §5, F2 |
| SD-04 | 2026-10-18 | Withdrawal mechanism (S-1, S-2, O-1) | A: `auth.deny` in place now with plan guards; dedicated outermost address-independent gate with sparse typed op before #587 guided withdrawal — maintainer | Accepted | spec §9.3; ADR D4 |
| SD-05 | 2026-10-18 | Held-intent timing (dissent A-3, U-1) | B+: accept minimal block now (`mode`, `audience_label`, `route_id` required) and build it early with intent-guarded changes before #581's evidence run; bundle the SD-04 gate with SD-02 retirement as "complete withdrawal" — maintainer | Accepted | spec §4.5, §8.4, §17; ADR D2, D4 |
| SD-06 | 2026-10-18 | Product-outcome gate (U-7) | A: predeclared targets; zero dangerous failures T1–T6; evidence-forced #588 GO/REDUCE/DEFER — maintainer | Accepted | spec §14; ADR D8 |
| SD-07 | 2026-10-18 | R03(a): does #587 guided withdrawal need the whole #642? | whole bundle — maintainer | Accepted | spec §8.4 delivery table; ADR D4 |
| SD-08 | 2026-10-18 | R03(b): scope on shared H2/H3 connections | exact-entry; collateral is a disclosed downgrade and a W3 failure — maintainer | Accepted | spec §8.4, §13 |
| SD-09 | 2026-10-18 | R02/R03(c): changes Jul cannot classify | treated as narrowing with a previewed keep-open exception; metadata-only never retires — maintainer | Accepted | spec §9.5, §8.4 |
| SD-10 | 2026-10-18 | R05: saved or serving source for current access | serving snapshot; saved layer separate — maintainer | Accepted | spec SH-13, §4.3, §7.2 |
| SD-11 | 2026-10-18 | R06: file-owned confirmation | keep, fully specified (producer and server-owned evaluator in #641; observation only) — maintainer chose "keep"; the author applied the audit's conditions for keeping it | Accepted | spec §9.4; #641 |
| SD-12 | 2026-10-18 | R08: source of "who enforces" on provider paths | B: optional declared `external_enforcer` in #641 — maintainer; **reconfirmed** as context only after round 5 withdrew its warning-reduction benefit (answer 10) | Accepted | spec §4.5; #641 |
| SD-13 | 2026-10-18 | RR02/RR03: what the postcondition may claim | scoped: exact-candidate and access answers shown separately; only both together say the change is serving; expected absence; revision-bound references never cross revisions — maintainer | Accepted | spec §9.4; ADR D2; #641 |
| SD-14 | 2026-10-18 | RR01: WAF detect-mode classes | precondition-based rules (no per-request engine control) with a real-HTTP regression guard; `uncertain` where engine control exists — maintainer | Accepted | spec §9.5; #641, #642 |
| AD-01 | 2026-10-18 | Withdrawal mechanism (author) | superseded by SD-04 | Superseded | — |
| AD-02 | 2026-10-18 | F1 layout | separate host names on one listener **plus a first-declared default-deny block** (after S-3) | Proposed | F1 |
| AD-03 | 2026-10-18 | Vocabulary | design/API terms fixed; Console wording validated by #585 before #587 freezes it (after U-4) | Proposed | spec §2 |
| AD-04 | 2026-10-18 | Provider and public entries on one Jul | SH-11, scoped to off-host reach with explicit host trust (after S-4) | Proposed | spec §3, §6 |
| AD-05 | 2026-10-18 | Connector identity headers | SH-12 | Proposed | spec §3 |
| AD-06 | 2026-10-18 | Cloudflare origin enforcement | cloudflared `access.required` default; Jul `forward_auth` validator recommended, required with untrusted local processes (after S-4); Jul header-token change not authorised | Proposed | F3 |
| AD-07 | 2026-10-18 | Postcondition scope for remote authorities (S6-2) | the access comparison covers configuration and local access files; a JWT key set, forward-auth decisions and provider policy (for entries whose declared intent relies on a provider, after S7-1) are listed in `external_inputs` and shown as not covered. Rejected: binding a key set (network fetch while planning; routine rotation reported as divergence) and `unverifiable` for every JWT entry (hides what Jul did verify) | Proposed; reversible before #641 ships | spec §9.4 |

"Shown without objection" is not acceptance; AD items become accepted only with
the maintainer's acceptance of #580.

### Verbatim answer record (R10)

The maintainer answered through the authoring session's structured question
tool on 2026-10-18 (Europe/Madrid), in this order. Answers are transcribed
verbatim, spelling included; option texts are abridged.

| # | Question (abridged) and options offered | Verbatim answer | Recorded as |
| --- | --- | --- | --- |
| 1 | SD-01 intent location (A derived now / B block now / C `[[applications]]`); SD-02 promise (A defer / B GO #563 / C GO #562); SD-03 coarse private network (A / B / C) | `sd01=A, sd02=whats the impact here? be explicit with pros an cons, sd03=A, notes=no disagreement so far` | SD-01 A, SD-03 A; SD-02 open |
| 2 | SD-02 with explicit impacts (D defer + re-decision / A / B / C) | `notes=not sure... I tend to agree with you but need more evidence, sd02=not sure again... what are the pros and cons?` | SD-02 open; evidence gathered (V-1, P1, X01–X07) |
| 3 | SD-02 with evidence (D / A / B / C) | `notes=OK i tend to agree but is this the highest value long term? are we being bold and ambitious? should we not push further?, sd02=I tend to agree with option 1 to defer both but are we being bold and brave? should we not push further?` | leaning to option 1 (D); bolder option requested |
| 4 | SD-02 with option E (DE parallel / E first / D / B) | `notes=OK this makes sense, sd02=what are the real differences between options 1 and 2? I tend to agree with you but 2 seems bolder` | open; comparison requested |
| 5 | DE (option 1) versus E-first (option 2) | `notes=ok agree with option 1, sd02=DE` | SD-02 = D+E |
| 6 | SD-04 withdrawal mechanism (A/B/C); SD-05 held intent (A/B/C); SD-06 outcome gate (A/B) | `notes=what are the bolder, more useful long term alternatives? even if it means more effort today (reasonable)I tend to agree with your reommendation for first decision, not sure i understand the second one. third one also i tend to agree with you, sd04=A, sd05=This one I am not sure about option 1 vs 2, sd06=A` | SD-04 A, SD-06 A; SD-05 open |
| 7 | SD-05 (B+ / B / A) and bundling the gate with #563/#562 | `bundle=true, notes=this is the best for the user and bolder move? are we really pushing further for a complete and seamless experience, sd05=Bplus` | SD-05 B+; bundle accepted |
| 8 | Acceptance of the contract; publication authority | `accept=Is this final? 10/10? I will review after you confirm you consider this to be a 10/10 ready for ready for review delivery, issues=true, notes=No additional notes other than keeping github issues and documentation updated, push_pr=True but once we merge - it closes 580, register=true` | contract **not** accepted; publication authorised; PR kept non-closing until acceptance |
| 9 | After the principal review: confirm SD-01–SD-06 (R10); R03 delivery, scope and uncertain edits; R05 source; R06 confirmation; R08 enforcer | `notes=If you think the principal engineer is more sound than my answers, it makes sense to follow his guidance. We are here to get THE BEST product. The only authority is quality., q1=Follow the feedback from the audit unless you disagree with it or think it materally changes direction, q2a=whole, q2b=exact, q2c=narrowing, q3a=serving, q3b=keep, q4=B` | SD-07–SD-12; SD-01–SD-06 stand (the audit recommends preserving genuine prior decisions with provenance; nothing in it changes their direction) |
| 10 | After the principal re-review: postcondition scope (scoped / whole-candidate); SD-12 after its changed premise (keep as context / defer); WAF detect-mode classes (precondition-based / always conservative); publication after a clean focused review | `postcondition_scope=scoped, sd12=keep_context, waf_detect=precondition, publish=true, notes=Remember to be bold and focus on the user longer term - no short term "shortcuts"` | SD-13 scoped; SD-12 reconfirmed; SD-14 precondition-based; publication authorised after a clean focused review |

Final whole-contract acceptance is a separate, later act and is not implied by
any row above.

### Deferred safely

| Item | Why no current contract depends on it | Owner | Trigger |
| --- | --- | --- | --- |
| Earlier read-only provider observation (show provider-policy membership in the view) | evidence model already carries provider-sourced records with freshness; nothing in #581–#586 needs it | #62 / #588 | #583 evidence or the SD-06 outcome gate |
| Cross-block application grouping key | operations act on explicit entry sets | #587 / later | a recipe needs one name across hosts |

## 4. Probes

### P1 — revoked client certificate on an open connection

- **Question.** After a CRL reload revokes a client certificate
  (`client_auth.mode = "require"`), does a new request on the already-open
  HTTP/1.1 keep-alive or HTTP/2 connection still succeed?
- **Method.** A disposable test file in `internal/server` reusing
  `newMTLSReloadFixtureWithHandler`: request, revoke the serial, reload
  (`applied_live`), request again on the same transport (`httptrace` reuse flag),
  then a fresh handshake. Loopback only; synthetic CA; about one second.
- **Result.** HTTP/1.1 and HTTP/2: the second request returned 200 with
  `reused = true`; the fresh handshake failed with `bad certificate`.
- **Cleanup.** The file was deleted; `git status` showed no residue.
- **Limits.** Handler-level fixture (not the location `require_client_cert`
  middleware); no HTTP/3; lean build.

### P2 — WAF body limit in `block` and `detect` modes (round 9)

- **Question.** Does a body over the WAF limit get rejected in `detect` mode as
  in `block` mode (the r7 claim challenged by RR01)?
- **Method.** The principal re-reviewer's disposable test, reproduced locally in
  `internal/waf` (`waf` build tag): `waf.New` and `Firewall.Middleware` behind
  `httptest.NewServer`, limit 16 bytes, inline `SecRequestBodyAccess On` and
  `SecRequestBodyLimitAction Reject`, one 32-byte POST per mode over loopback
  HTTP/1.1; the action echoes what it reads.
- **Result.** `block`: 413, action not reached. `detect`: 200, action reached,
  all 32 bytes delivered.
- **Cleanup.** The file was deleted; `git status` showed only the pre-existing
  unrelated editor-settings change.
- **Limits.** HTTP/1.1; one body size; per-request engine control (E51) was not
  exercised and is source-only.

### P3 — soundness of the host-dispatch rule (round 9)

- **Question.** Does the spec §9.5 host-dispatch rule include every block whose
  traffic can change when host names, block order or the set of blocks change?
- **Method.** A disposable property test in `internal/router` driving the real
  `addrRouter.match` and `hostScore`: 200,000 random configurations of one to
  four blocks over a vocabulary of overlapping exact and wildcard names, each
  with one random mutation (rename, add alias, swap order, insert or remove a
  block), evaluated on 12 hosts; the rule was implemented independently in the
  test and every block whose winner changed was checked against it.
- **Result.** 2,400,000 host evaluations; 472,507 moved between blocks; every
  old and new winner was in the affected set (fixed seed, about 2 s).
- **Cleanup.** The file was deleted; `git status` showed no residue.
- **Limits.** One listener; a bounded vocabulary; relocation was modelled as
  removal from one listener and insertion into another, not run across two
  listeners. It tests soundness, not how tight the expansion is.

## 5. Validation actually run

| ID | Command | Result |
| --- | --- | --- |
| V-1 | `go test -count=1 ./internal/server -run 'TestLongLivedMTLSStreamSurvivesClientRevocation\|TestReloadRefreshesClientCRLWithoutRestart\|TestReloadRotatesClientCAWithoutRestart\|TestReloadDrainsBeforeRetiringClosers\|TestReloadSwapsHandler' -v`; `go test -count=1 ./internal/handler -run 'TestLongLivedWebSocketReloadAndShutdown\|TestLongLivedSSEReloadAndShutdown' -v` | all passed (lean build) |
| V-2 | probe P1 (§4) | as recorded |
| V-3 | full-tag build of the baseline; `jul check -config` and `jul lint -config` on F1, F1 withdrawal, F1 mTLS, the r1 combined F1+F2+F3, and the r2 combined configuration (default-deny block, F2 without the device allow-list) and its withdrawal variant, with placeholder certificates, htpasswd and admin token | all valid; one intentional lint warning (compression disabled). Rejected as expected: Jul `cache` on a static root; `deny` action together with `proxy_pass` |
| V-4 | `python3 -c 'yaml.safe_load(...)'` on the F3 `cloudflared` example | parsed |
| V-5 | `python3 scripts/docs-check.py` | r1: 2,415 passed, 0 failed; r2 to r4: 2,419 passed, 0 failed; r5 to r9: 2,422 passed, 0 failed |
| V-6 | TOML blocks and relative links of the three new documents parsed with `tomllib` and checked for existence (this packet is outside docs-check scope) | all valid |
| V-7 | The exact F1, F2 and F3 TOML blocks extracted from the final specification, plus the F1 withdrawal variant and the combined instance, checked with `jul check` and `jul lint` (full-tag build, placeholder files); the specification's `cloudflared` YAML parsed. Re-run at r5 and r7 (the four TOML blocks are byte-identical between them): F1, F1 withdrawal, F1 mTLS, F1+F2 and F1+F2+F3, each in `managed` and `file_owned` authority (10 files), with `JUL_ADMIN_TOKEN` set to a throwaway placeholder; negative control: `proxy_pass` with `deny = true` | 10/10 `check` and `lint` exit 0 with one intentional warning (compression disabled); negative control exit 1 (`conflicting actions [proxy_pass deny]`). The proposed `sharing` block is not implemented and was not checked by `jul`. At r9 the five fixture blocks (TOML and YAML) were compared byte for byte with r8 and are identical, so `jul` was not re-run; the principal re-review independently ran all 20 `check`/`lint` commands at r8 (exit 0) |
| V-8 | probe P2 (§4): disposable `go test -count=1 -tags waf ./internal/waf -run TestPR640ProbeDetectBodyLimit -v` | `block` 413 (action not reached); `detect` 200 (32 bytes delivered); file deleted |
| V-9 | probe P3 (§4): disposable property test `go test -count=1 ./internal/router -run TestPR640HostDispatchOverlapRuleIsSound -v` | passed: 2,400,000 evaluations, 472,507 moves, all within the affected set; file deleted |

Not run: live provider paths, independent clients, race detector, full CI. None
is claimed. The full-tag build needed one direct module download after repeated
proxy stream errors; no module files changed.

## 6. Adversarial review

### 6.1 Method and limits

Four independent read-only reviewer agents, one per lens: architecture and
compatibility (A), security and sessions (S), operability and recovery (O),
usability and evolution (U). **Phase 1 (blind):** each received the issues,
evidence ledger and repository, was told not to open the draft, and derived
critical invariants, a minimal baseline and its strongest attack scenarios.
**Phase 2:** each reviewed the frozen r1 commit `e5b77b42` through `git show`
against its baseline and an assigned subset of the 14 scenarios (every scenario
covered at least once). **Limit:** blindness was instruction-based; the draft
files existed in the working tree, so isolation is not technically guaranteed.
The reviewers are AI agents, not independent humans.

Phase-1 baselines broadly converged with the draft: entry-level unit, existing
identities, separated intent/posture/evidence, phase-specific access effects.
Two diverged materially: A proposed a held location-level intent block now; S
proposed separate F1 listeners. Both were tested in phase 2.

### 6.2 Round 1 findings (r1 `e5b77b42`) and dispositions

| ID | Sev. | Type | Finding | Disposition | Resolution in r2 | Owner |
| --- | --- | --- | --- | --- | --- | --- |
| S-1 / O-1 | blocker | demonstrated | `location_set_auth` replaces auth wholesale; withdrawal through it drops Basic | accepted; SD-04 | spec §9.3: complete-candidate `auth.deny`; typed op forbidden; dedicated gate before #587 | #580, #587 |
| S-2 | blocker | demonstrated | request plugins and CORS preflight answer before auth; deny-only CIDR admits unidentifiable addresses | accepted; SD-04 | plan guard; limits stated; outermost address-independent gate | #580, #587, #634 |
| A-1 | blocker | demonstrated | file-owned preview/apply "same resource" claim false | accepted | §4.2: guarantee scoped to managed; file-owned postcondition check; unconfirmed until verified | #580, #587 |
| O-2 | blocker | demonstrated | outcome mapping misstated `applied_degraded`, `no_change`, `not_applied`; "partial only across systems" false | accepted | §9.2 rewritten; reload outcome and `config_state` separated | #580 |
| A-2 / U-2 | high | demonstrated | "reach set" was a hidden application identity that could widen withdrawal | accepted | §4.2: explicit entry sets; same-backend list advisory | #580, #587 |
| A-3 / U-1 | high | risk / demonstrated | dissent: product cannot answer "who is this for?" without held intent; manifests a second intent source | escalated → SD-05 B+ | §4.5 accepted block, built before #581's evidence run; intent findings and guarded plans | maintainer; intent tranche |
| A-4 | high | demonstrated | singular `audience`/`provider_ref` premature one-way names | accepted | `display_label`, `audience_label`; additive evolution; provider refs to #588 | #580, #587, #588 |
| O-3 | high | demonstrated | retry guidance ignored boot-scoped ledger loss | accepted | §9.2 ambiguous-outcome rule with `boot_id` | #580, #587 |
| O-4 | high | risk | W2/W3 not executable with named tools | accepted | §13 harness extension and event log; portal probe bound | #581 |
| O-5 | high | risk | no outage, drain or attribution phases | accepted | §13 W4, cgroups, soak pass criteria | #580, #581 |
| O-6 | high | demonstrated | backup/export overstated | accepted | §15 rewritten: no archive; exact file, sidecars, restore order | #580, #584 |
| S-3 | high | demonstrated | unknown Host reaches the first-declared block, not 404 | accepted | E10 corrected; F1 default-deny block; §11 row | #580, #581 |
| S-4 | high | demonstrated | loopback is not connector-exclusive; F2 forwarded-address allow forgeable | accepted | host trust explicit (§6); SH-11 off-host scope; F2 allow-list removed; F3 forward-auth recommended/required | #580, #582, #583 |
| S-5 | high | risk | retirement trigger set incomplete; collateral H2/H3 closure | accepted | §8.4 trigger and collateral rows | #563, #562 |
| S-7 | high | risk | cache key lacks listener/identity partition | accepted | restricted/private cache off; exception needs cache owner | #580, #620, #623 |
| U-3 | high | demonstrated | wireframe allowed withdrawal without preview | accepted | §9.8 two-step flow | #587 |
| U-5 | high | demonstrated | rubric gated dangerous failures on three tasks only | accepted | §14 all six tasks; predeclared protocol | #585, #587 |
| A-5 | medium | risk | recovery after unknown external outcome could duplicate or broaden | accepted | §9.6 stop-and-reconcile | #580, #583, #588 |
| A-6 | medium | demonstrated | derived view and vocabulary under-classified as readily reversible | accepted | ADR door table; versioned projection | ADR 0025 |
| S-6 | medium | demonstrated | JWKS refresh is lazy with 1 h stale grace | accepted | §8.1 row | #580 |
| U-4 | medium | risk | vocabulary exposes implementation structure | accepted | §2: Console wording tested by #585 | #585, #587 |
| U-6 | medium | demonstrated | L4 shown as an entry without enforcement or withdrawal | accepted | L4 outside the contract (§2, §12) | #580 |
| U-7 | medium | risk | risk of a status-and-handoff product | escalated → SD-06 A | §14 outcome gate; #588 evidence-forced | maintainer |

Rejected findings: none. Accepted trade-offs that remain: §6 host trust
(same-host processes trusted unless Jul-side checks are added); intent findings
are warnings, not rejections; restricted caching off.

Scenario coverage (verdicts from reviewers, after r2 where noted): 1 shared
listener — fixed by default-deny (S-3); path canonicalisation verified (E37).
2 bypass/forgery — fixed by host-trust scope and F3 validator rule (S-4).
3 cache — fixed by categorical rule (S-7). 4 local-as-global — no finding (O, U).
5 fresh vs open sessions — current wording honest; §8.4 broadened (S-5).
6 withdrawal misstatement — fixed with mechanism limits (S-1/S-2) and
harness (O-4). 7 preview/apply target — fixed for file-owned (A-1). 8 persisted
but not live — fixed (O-2/O-3). 9 provider ambiguity — rule added (A-5). 10
restore/move — fixed (O-6). 11 lean/unsupported — no finding (S). 12 second
application/audience — fixed (A-2, A-4). 13 two-way reversed — reclassified
(A-6). 14 fresh operator — addressed by SD-05, U-3, U-5, U-4.

### 6.3 Round 2 (r2 `75264d61`)

Each reviewer judged its round-1 findings against r2 and attacked the new
material. **Round-1 status:** resolved — A-2, A-3, A-4, A-5, A-6, O-1, O-3,
O-4, S-1, S-3, S-6, S-7, U-1, U-2, U-3, U-6, U-7; resolved at design level —
S-2; partially resolved — A-1, O-2, O-5, O-6, S-4, S-5, U-4, U-5 (each
completed by a round-2 item below). The architecture reviewer confirmed that the
SD-05 block does not contradict ADR 0019 §6. The usability reviewer judged the
design "ambitious enough for a complete Jul-controlled sharing experience…
not yet seamless across providers", naming one evidence-selected #588 provider
slice as the most valuable next step.

| ID | Sev. | Type | Finding | Disposition | Resolution in r3 | Owner |
| --- | --- | --- | --- | --- | --- | --- |
| A2-1 | high | demonstrated | file-owned postcondition had no correlation mechanism without hidden preview state | accepted | §4.2: stateless postcondition object; unconfirmed without it; Jul never claims to verify intent | intent tranche, #587 |
| S2-3 | high | risk | target-change retirement trigger was subjective | accepted | §8.4: every target change with admitted long-lived exchanges narrows; explicit exception forfeits complete-withdrawal claim | #563, #562 |
| S2-4 | high | risk | F3 validator contract undefined; could trust forgeable headers | accepted | SH-12 covers every decision-maker; F3 cryptographic validation contract | #583 |
| O2-1 | high | demonstrated | `config_state` mapping incomplete and partly misleading | accepted | §9.2: all ten values; `managed_inconsistent` from its reason | #580, #587 |
| O2-3 | high | risk | restore omitted package/profile compatibility, ACME state, exact sidecars | accepted | §15: package and capabilities, ACME, exact sidecar names, restore with Jul stopped | #580, #584 |
| U2-1 | high | demonstrated | file-owned guidance would keep prescribing `auth.deny` after the gate | accepted | §9.8: exact edit from the plan; #587 never recommends `auth.deny` | complete withdrawal, #587 |
| A2-2 | medium | risk | projection versioning not implementable; codes could be repurposed | accepted | §4.3: `sharing_view_version`; additive; codes never repurposed | #586, #587 |
| A2-3 | medium | demonstrated | #581 baseline change conflicts with #581's body | accepted | §18: development-evidence label; rerun on a published package; #581/#579/#62 updated on publication | maintainer, #581 |
| S2-1 | medium | demonstrated | missing SNI does not produce 404 | accepted | F1 and §11: SNI fallback separate from Host routing | #580, #581 |
| S2-2 | medium | demonstrated | detaching plugins/CORS breaks single-step recovery | accepted | §9.3: first journeys do not withdraw such entries | #581, #587 |
| S2-5 | medium | demonstrated | limitation wording overstated test evidence | accepted | known limitations: probe vs source-inspection distinguished | #580 |
| O2-2 | medium | risk | W4 recovery deadline and baseline comparison undefined | accepted | §13: comparison method, dashboard probe, recovery deadline, attribution | #581 |
| U2-2 | medium | demonstrated | safe task failure could pass the outcome gate | accepted | §14: completion within an assistance ceiling required | #585, #587 |
| U2-3 | medium | risk | early Console strings precede the comprehension test | accepted | §2: provisional, no compatibility promise | intent tranche, #585 |

Rejected findings in round 2: none.

### 6.4 Round 3 (verification of r3 `fe44f6a0`)

Each reviewer verified its round-2 items against the r2→r3 diff and checked
for new blocker or high findings.

| Reviewer | Items verified | Verdict | New blocker/high |
| --- | --- | --- | --- |
| Architecture | A2-1, A2-2, A2-3 | all resolved | none |
| Security | S2-1 to S2-5, S-4, S-5 | all resolved | none |
| Operability | O2-1, O2-2, O2-3 | all resolved (state semantics checked against ADR 0019 §10/§16) | none |
| Usability | U2-1, U2-2, U2-3 | all resolved | none |

Totals across rounds: 40 finding rows (5 blocker, 21 high, 14 medium), of
which three pairs overlap (S-1/O-1, A-2/U-2, A-3/U-1); none rejected, all
dispositioned. The maintainer decided the escalated items (SD-04 for the
withdrawal blockers, SD-05 for A-3/U-1, SD-06 for U-7). Reviewer agreement is
recorded as evidence, not as proof of correctness.

### 6.5 Round 4 — independent principal-engineer review (head `a76f197e`)

An external principal-engineer review (four internal adversarial passes plus a
remedy challenge, independent source probes and its own CI attribution)
recommended **request changes**. The author re-verified every load-bearing claim
against source or primary documentation before accepting it (E40–E44, X15). All
findings were accepted; none was rejected. Where the review offered a choice,
the maintainer decided (SD-07–SD-12).

| ID | Sev. | Finding | Author verification | Resolution (r5) |
| --- | --- | --- | --- | --- |
| R01 | high | `managed_failed_apply` and `file_owned_clean` renderings claimed restoration or convergence their inputs do not prove | confirmed (E40, E41) | spec §9.2: transient state; restoration only from terminal `restored: true`; file-owned equality only from `desired_version == serving_version` |
| R02 | high | audience delta lacked conservative semantics for incomparable or opaque policies | agreed (design gap) | spec §9.5: defined access inputs, expansion to referencers, five classes, monotone table, `uncertain` never "unchanged"; SD-09 |
| R03 | high | withdrawal scope and delivery gates inconsistent; wireframe contradicted retirement | confirmed | SD-07/SD-08; spec §8.4 rewritten with delivery table and collateral rule; §9.8 wireframe; ADR D4 |
| R04 | medium | redirect rewrites answer before auth; gate placement unspecified | confirmed (E42) | spec §9.3 guard, `redirect_https` handling, exact gate placement, exact recovery; §4.3/§4.4/§4.5 codes |
| R05 | medium | saved versus serving semantics not chosen | confirmed (E43) | SD-10; SH-13; spec §4.1, §4.3 layers, §4.6 saved example, §7.2 |
| R06 | medium | file-owned confirmation producer and evaluator undefined | agreed | SD-11: spec §9.4 producer, server-owned evaluator, outcomes and limits; owner #641 |
| R07 | medium | key-endpoint outage equated with universal denial | confirmed (X15) | spec F3 failure cases separated; §4.6 example |
| R08 | medium | no defined input for external enforcer/provider | agreed | SD-12: declared `external_enforcer` (§4.5), never verified; findings refined |
| R09 | medium | `also_reaches_backend` quadratic versus linear budget | confirmed (arithmetic) | spec §4.2/§4.3 bounded response-local `backend_groups` with truncation counts |
| R10 | evidence | decision provenance not auditable | agreed (record gap; decisions were made in-session) | §3 verbatim answer record; maintainer instruction recorded |
| O01 | — | "additive only" / "one-way once shipped" overstated | confirmed (E44) | spec §4.3/§4.5 and ADR doors scoped to the compatibility policy |
| O02 | — | T1/T5 scope wording imprecise | agreed | spec §14 T1/T5 require stating selected addresses and known alternate paths |
| O03 | — | W1/W3 can trip the portal's per-address rate limit | agreed | spec §13 rate-limit interaction |
| O04 | — | 641→581 mislabelled as a hard technical edge | agreed | spec §17.2: accepted product prerequisite with stated cost |
| O05 | — | combined effort not stated | agreed | spec §17.1: nominal 16–32 days, owner re-estimates |
| O06 | — | derived URLs not concrete for wildcard or regex | agreed | spec §4.3 `urls[].kind` |
| O07 | — | ADR conflated a same-authority resource with a second store | agreed | ADR alternatives split, with real costs |
| O08 | — | "≥ 90 %" versus strictly more than 90 % | agreed | spec §17.1 strict wording, Go and frontend separately |

**Process lesson.** Three internal rounds missed R01, R04 and R05, which are
source facts. The internal reviewers were directed at the draft's claims; the
principal review probed the implementation underneath each rendered statement.
Round 5 below targets exactly that.

### 6.6 Round 5 (re-review of r5 `c53b14b5`)

Two fresh independent reviewers, instructed to verify the implementation
beneath every rendered claim. **Prior items:** R01, R05, R06, R09, O01, O04–O08
resolved; R04 and R07 partially resolved (completed by P5-1 and P5-2). Both
reviewers found the gate placement sound (it dominates rewrites, plugins,
preflight, checks, cache, error pages, HEAD and selected OPTIONS) and
stream-level H2/H3 retirement implementable in principle. All 13 new findings
were verified against source and accepted; none was rejected.

| ID | Sev. | Finding | Resolution (r6) |
| --- | --- | --- | --- |
| P5-3 | blocker | §11 promised the cache never crosses entries or listeners; the cache is process-wide and keyed by method, Host and URI (E47) | §11 split: restricted/private never cached; public entries sharing host and path share cache entries; new `cache_shared_across_entries` warning; partitioning to #620/#623 |
| P5-1 | high | §8.1 omitted redirect rewrites from the withdrawal exception | §8.1 row corrected |
| P5-4 | high | bind order treated disjoint audiences (RFC 1918 vs 100.64/10) as monotone | §9.5: only loopback→other and specific→all-interfaces are monotone; other specific changes and port changes `uncertain` |
| S5-1 | high | empty `allow` means every address, so "subset" mis-classed removing the last entry (E45) | §9.5 computes on the permitted address set |
| S5-2 | high | predicate narrowing on one entry lets requests fall through to another | §9.5: match/predicate/host/order/removal changes make every entry of the block `uncertain` |
| S5-3 | high | listener-wide CA union and issuer-scoped CRLs not reflected | §9.5: listener expansion; CRL compared as `(issuer, serial)`; CA/SAN/issuer changes `uncertain` |
| S5-4 | high | credentialed CORS and WAF relaxations do widen use | CORS and WAF are access inputs with explicit monotone rules; other rule changes `uncertain` |
| S5-5 | high | postcondition could confirm while an old credential file serves | §9.4 access fingerprint including loaded credential-file digests; `unverifiable` outcome; `confirmed` only when all match |
| P5-2 | medium | key rotation: a successful JWKS fetch replaces the cache | F3 rotation case corrected; X15 corrected |
| P5-5 | medium | `client_auth` none→request is not a narrowing (E46) | §9.5: only transitions to/from `require` are monotone |
| P5-6 | medium | `managed_clean` can coexist with an in-flight apply (E48) | §9.2: equality only from version equality; "change in progress" otherwise |
| S5-6 | medium | evaluator with `status:read` only could act as a posture oracle | §9.4: requires `status:read` and `config:read` |
| S5-7 | medium | declared `external_enforcer` downgraded `intent_restricted_unenforced` | finding unchanged by the declaration; enforcer shown as unverified context. **Consequence for SD-12:** the "fewer false alarms" benefit cited when the maintainer chose option B no longer holds; the in-product answer to "who enforces (declared)" remains |

### 6.7 Round 6 (verification of r6 `ecde75fa`)

The same two reviewers verified their round-5 items against the r5→r6 diff and
attacked the rewritten §9.4 and §9.5. **Verified resolved:** P5-1, P5-2, P5-3,
P5-5, P5-6 and S5-1 to S5-7. **Reopened:** P5-4. All findings were verified
against source and accepted; one proposed remedy was corrected by the author
from source.

| ID | Sev. | Finding | Resolution (r7) |
| --- | --- | --- | --- |
| P5-4 (reopened) | high | loopback → another address was still classed `widened`; Jul listens on exactly one address, so a local connector reaching loopback loses access (E50) | §9.5: widened only for a specific address → its own family's wildcard (`0.0.0.0`/`::`); any other bind change, empty host or port change `uncertain`; F1 walkthrough shows the `uncertain` class |
| P6-1 = S6-1 | high | "WAF rules only removed" classed `widened`, but rule files hold exclusions and allow rules, so removal can narrow (E49) | every WAF rule, rule-file, CRS, paranoia, body-limit or response-inspection change `uncertain`; mode and enablement rules apply only with everything else unchanged; WAF rule files join the access inputs, fingerprint and global-override expansion. The reviewer's suggestion that enabling detect mode is admission-neutral was **not** adopted: Coraza rejects oversized bodies in detect mode too (E49), so enablement in either mode is `narrowed` |
| S6-2 | high | the postcondition can confirm while a remote JWT key set has changed admission | AD-07: confirmation scoped to configuration and local access files; remote authorities listed as `external_inputs` and shown as not covered |

> **Correction (round 9, RR01).** The P6-1 disposition above is wrong in one
> respect: enabling `detect` mode was classed `narrowed` because "Coraza rejects
> oversized bodies in detect mode too". It does not (E49 corrected; probe P2).
> The row is kept as recorded; the current rules are in §6.9 (SD-14).

### 6.8 Round 7 (verification of r7 `7e3382c4`)

**Verified resolved:** P5-4, P6-1 (with E49 independently confirmed: detect
mode still rejects oversized bodies), S6-1 and S6-2. Source-truth reviewer: no
new blocker or high finding.

| ID | Sev. | Finding | Resolution (r8) |
| --- | --- | --- | --- |
| S7-1 | high | `external_inputs` omitted provider policy, so an entry enforced only by a provider or tailnet could read `confirmed` without naming its real enforcer | §9.4: `provider_policy` added, listed for every entry that declares an `external_enforcer`, declares `restricted` without a Jul check, or declares `private_network`; undeclared entries keep `unanalysed` external enforcement |

Round 8: the security reviewer verified S7-1 resolved in r8 (before this note)
and found no new blocker or high finding. No blocker or high finding is open.

> **Correction (round 9).** "E49 independently confirmed" above was mistaken:
> the author and the reviewer both read the transaction's reject branch and
> default, but not the constructor rule that applies in detection mode.
> Reviewer agreement was not proof.

### 6.9 Round 9 — independent principal re-review (head `292a294c`)

The external principal engineer re-reviewed head `292a294c` (four internal
passes, independent source probes, CI attribution) and recommended bounded
corrections before acceptance: no blocker or high finding, five medium
findings and several low refinements. Prior findings: R01, R03–R05, R08 and R10
addressed; R02, R07 and R09 substantially addressed; R06 partially addressed
(completed by RR02 and RR03). The author verified every finding against source
and agreed with all of them; none was rejected. The maintainer decided the
three open choices (answer 10: SD-13, SD-14, SD-12 reconfirmed).

| ID | Sev. | Finding | Author verification | Resolution (r9) |
| --- | --- | --- | --- | --- |
| RR01 | medium | `detect` mode does not enforce the body limit; E49 and the round-6/7 dispositions were wrong | reproduced (probe P2); constructor rule found; additionally, a rule can switch blocking on per request and `detect` ignores `allow` (E51) | SD-14: precondition-based WAF rows; real-HTTP regression guard in #641; E49 corrected; correction notes in §6.7 and §6.8 |
| RR02 | medium | matching access facts is not proof that the change is serving (empty list, pre-publication match); undeclared entries described as having no Jul check | counterexamples hold | SD-13: separate `candidate` and `access` answers, a `result` precedence table, no bare "confirmed"; undeclared entries report the checks they actually apply |
| RR03 | medium | no expected absence; revision of selector references unspecified | agreed | `expect: present \| absent`; references without a `route_id` taken from the candidate (present) or base (absent) and resolved only in that revision |
| RR04 | medium | host expansion omitted competing non-default blocks | verified in router source (E52); the new rule property-tested against the real router (probe P3) | normative host-dispatch overlap rule with a soundness argument; listener-wide fallback; property test in #641 against the real router |
| RR05 | medium | routes × aliases output; budget stated in entries only; stale two-field budget | agreed; the same amplification also existed in per-entry selectors and postcondition references | `host_sets` once per block; capped `urls` with counts; paged entries bound to revisions; `server_names_digest` in references; caps fixed by #586 before exposure; budget corrected to three fields |
| Low | — | alias and prefix scope; cached Cloudflare keys outlive publication; SD range in headers; PR verification wording; freshness of access files under an unchanged version | agreed | §4.2 aliases and prefix scope; F3 rotation wording; SD-01–SD-14 in spec and ADR; PR body corrected; §9.4: an access file changed under the exact candidate yields `diverged` |

The #641 author estimate rose to 6–12 focused days (bounded paged projection,
host-dispatch analysis with its property test, expected absence); the two
tranches total a nominal 18–36 days.

### 6.10 Round 10 (verification of r9)

(Recorded below.)

## 7. Traceability

| # | Criterion (abridged) | Design section | Review / validation evidence | Runtime verification owner |
| --- | --- | --- | --- | --- |
| 1 | Successor can identify service, audience, trust boundary, owning config/API | spec §2, §4 (serving/saved layers, `external_enforcer`), §5, §6, §9.5, §9.7, §10 | §6 (A, S), §6.5 R02/R05/R06/R08; V-3, V-7 | #581–#583; #586/#587; intent tranche |
| 2 | Each vision commitment has a primary owner and observable exit evidence | spec §17.4 | §6 (U) | #579 matrix update on publication |
| 3 | Four access modes, provider-independent operation, advanced escape hatches | spec §5, SH-8, SH-9, F1, §9.8 | V-3 (F1 without any provider) | #581, #587 |
| 4 | Revocation/session limits; #562/#563 GO/DEFER with trade-offs | spec §8 (delivery table, exact-entry, uncertain = narrowing); ADR D4; SD-02, SD-05 bundle, SD-07–SD-09 | V-1, V-2, X01–X07; §6 (S); §6.5 R03 | #581 (continuation); complete withdrawal |
| 5 | Honest outcomes for partial configuration, external drift, provider outage, unknown verification | spec §4.6, §7.3, §9.2 (R01), §9.4 (SD-13 result table), §9.6, F3 (R07) | §6 (O); §6.5 R01/R06/R07; §6.9 RR02/RR03 | #581, #583, #586 |
| 6 | Protocol matrix; three fixtures with same-instance isolation and security defaults | spec §10–§12 | V-3, V-4; §6 (S) | #581–#583, #634 |
| 7 | Resource method; evidence owners for #585, #584, #613 | spec §4.3 response bounds, §13–§15, §17 | §6 (O); §6.9 RR05 | #581, #584, #585, #613 |
| 8 | Existing authorities authoritative; new metadata minimal, budgeted, versioned, approved | spec SH-1, SH-13, §4.1, §4.5, §13 budgets; ADR D1–D2; SD-01, SD-05, SD-12 | §6 (A); §3 verbatim record (R10); §6.5 O01 | intent tranche |
| 9 | Docs/links/examples/contract checks pass; decisions recorded in #580 and #579 | §5 | V-3–V-6 | issue records need publication authorisation |
| 10 | Summary and withdrawal implementable from a field/state/action contract without confusing admin OIDC, reachability or a URL with authorization | spec §4.3, §4.6, §9 (R04 guards, R06 postcondition), SH-3, SH-4, SH-13 | §6 (S, U); §6.5 | #586, #587 |
| 11 | Example states: deliberate public, restricted success/denial, provider outage/unknown, stale evidence, removal not ending an open stream | spec §4.6 (plus saved-not-serving and complete-withdrawal examples) | §6.5 R03/R05/R07 | #581, #583 |
| 12 | Intent mapped to #633 findings and #634 assertions; unsupported/unanalysed explicit; no second store | spec §4.5 tables | §6 (A) | #633, #634, intent tranche |
| 13 | Executable usability rubric for #585/#587; #588 keeps its later GO gate | spec §14, §16; ADR D8 | §6 (U) | #585, #587, #588 |

Issue deliverables 1–8 map to: (1) spec §1 and ADR 0025; (2) §4; (3) §5;
(4) §6–§7; (5) §8; (6) §10–§11; (7) §12; (8) §2, §4.3, §9, §9.8, §13. The
resource and value evidence contract maps to §13–§15 and §17.

## 8. Residual risks and open items

| Item | Status | Owner and trigger |
| --- | --- | --- |
| Tailscale behaviour taken from source because the knowledge base was unreachable | unverified against current docs | #582 re-checks before closure |
| Cloudflare behaviour of open WebSockets on Access revocation | undocumented | #583 measures |
| Route-level `require_client_cert` on an open connection after revocation | source inspection only (E39); probe P1 was handler-level | candidate regression for #634 or the mTLS owner |
| Effort for the intent tranche (#641, now including the postcondition and delta classes) and complete withdrawal (#642) | author estimates (nominal 18–36 days after r9), not owner estimates | re-estimated at selection |
| Stream-level retirement and per-request revocation re-checks on Go HTTP/2 and quic-go HTTP/3 (SD-08) | feasible in principle (handler-owned contexts, hijacked-connection ownership, middleware lookup) but unproven | #642 design; where infeasible for a protocol, the disclosed collateral downgrade applies and W3 fails |
| `uncertain` classes will be common (any auth-setting edit), so retirement on edit may surprise operators | accepted trade-off (SD-09); keep-open exception exists | #641/#642 tests; #585/#587 usability evidence |
| Intent field names frozen before first-use evidence | accepted trade-off (SD-05) | additive evolution only |
| Host trust includes all same-host processes | stated assumption (§6 of the spec) | Jul-side checks where untrusted processes run |
| Provider seams (membership, invitations, revocation) remain handoffs | by design | SD-06 outcome gate; #588 |
| Remote authorities (JWT key sets, forward-auth decisions, provider policy) change access without a Jul generation | named in every postcondition result as not covered (AD-07) | #641; external evidence per §7 |
| WAF effects classified only for mode and enablement; every rule change is `uncertain` | conservative by design; retirement on rule tuning may surprise operators | #641/#642 tests; keep-open exception |
| WAF detect-mode rules depend on pinned Coraza behaviour (E49, E51) | a dependency upgrade could change them | #641 regression test; rules re-derived on any engine upgrade |
| Host-dispatch rule under-expands if implemented wrongly | property test against the real router over generated host sets; listener-wide fallback | #641 |
| Projection caps (names, URLs, members, page size) are proposals | measured and fixed before exposure | #586 gate for #586/#587 |
| Reviewer independence | AI agents; instruction-based blindness | maintainer acceptance is the decision |
| Issue records for the new baseline and order | posted 2026-10-18: comments on #580, #579, #62, #562, #563; #581 body amended; #641 and #642 registered (all pending #580 acceptance) | maintainer acceptance switches #640 to `Closes #580` |
