# #580 application-sharing design — review packet

> **Reviewed:** design branch `design/580-application-sharing-contract`; source
> baseline `main` at `64e4a8b29230134c5dee37dfd8bfa130a9580a2a` (2026-10-18).
> Design revisions: **r1** `e5b77b42` (adversarial round 1) and **r2** (this
> revision; round 2 recorded in §6.3). This packet holds evidence, decisions,
> review findings and traceability for the
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
| AD-01 | 2026-10-18 | Withdrawal mechanism (author) | superseded by SD-04 | Superseded | — |
| AD-02 | 2026-10-18 | F1 layout | separate host names on one listener **plus a first-declared default-deny block** (after S-3) | Proposed | F1 |
| AD-03 | 2026-10-18 | Vocabulary | design/API terms fixed; Console wording validated by #585 before #587 freezes it (after U-4) | Proposed | spec §2 |
| AD-04 | 2026-10-18 | Provider and public entries on one Jul | SH-11, scoped to off-host reach with explicit host trust (after S-4) | Proposed | spec §3, §6 |
| AD-05 | 2026-10-18 | Connector identity headers | SH-12 | Proposed | spec §3 |
| AD-06 | 2026-10-18 | Cloudflare origin enforcement | cloudflared `access.required` default; Jul `forward_auth` validator recommended, required with untrusted local processes (after S-4); Jul header-token change not authorised | Proposed | F3 |

"Shown without objection" is not acceptance; AD items become accepted only with
the maintainer's acceptance of #580.

### Deferred safely

| Item | Why no current contract depends on it | Owner | Trigger |
| --- | --- | --- | --- |
| Earlier read-only provider observation (show provider-policy membership in the view) | evidence model already carries provider-sourced records with freshness; nothing in #581–#586 needs it | #62 / #588 | #583 evidence or the SD-06 outcome gate |
| Cross-block application grouping key | operations act on explicit entry sets | #587 / later | a recipe needs one name across hosts |

## 4. Probe P1

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

## 5. Validation actually run

| ID | Command | Result |
| --- | --- | --- |
| V-1 | `go test -count=1 ./internal/server -run 'TestLongLivedMTLSStreamSurvivesClientRevocation\|TestReloadRefreshesClientCRLWithoutRestart\|TestReloadRotatesClientCAWithoutRestart\|TestReloadDrainsBeforeRetiringClosers\|TestReloadSwapsHandler' -v`; `go test -count=1 ./internal/handler -run 'TestLongLivedWebSocketReloadAndShutdown\|TestLongLivedSSEReloadAndShutdown' -v` | all passed (lean build) |
| V-2 | probe P1 (§4) | as recorded |
| V-3 | full-tag build of the baseline; `jul check -config` and `jul lint -config` on F1, F1 withdrawal, F1 mTLS, the r1 combined F1+F2+F3, and the r2 combined configuration (default-deny block, F2 without the device allow-list) and its withdrawal variant, with placeholder certificates, htpasswd and admin token | all valid; one intentional lint warning (compression disabled). Rejected as expected: Jul `cache` on a static root; `deny` action together with `proxy_pass` |
| V-4 | `python3 -c 'yaml.safe_load(...)'` on the F3 `cloudflared` example | parsed |
| V-5 | `python3 scripts/docs-check.py` | r1: 2,415 passed, 0 failed. r2: see §6.3 |
| V-6 | TOML blocks and relative links of the three new documents parsed with `tomllib` and checked for existence (this packet is outside docs-check scope) | all valid |

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

### 6.3 Round 2

(Recorded below.)

## 7. Traceability

| # | Criterion (abridged) | Design section | Review / validation evidence | Runtime verification owner |
| --- | --- | --- | --- | --- |
| 1 | Successor can identify service, audience, trust boundary, owning config/API | spec §2, §4, §5, §6, §9.7, §10 | §6 (A, S); V-3 | #581–#583; #586/#587; intent tranche |
| 2 | Each vision commitment has a primary owner and observable exit evidence | spec §17.4 | §6 (U) | #579 matrix update on publication |
| 3 | Four access modes, provider-independent operation, advanced escape hatches | spec §5, SH-8, SH-9, F1, §9.8 | V-3 (F1 without any provider) | #581, #587 |
| 4 | Revocation/session limits; #562/#563 GO/DEFER with trade-offs | spec §8; ADR D4; SD-02, SD-05 bundle | V-1, V-2, X01–X07; §6 (S) | #581 (continuation); complete withdrawal |
| 5 | Honest outcomes for partial configuration, external drift, provider outage, unknown verification | spec §4.6, §7.3, §9.2, §9.6, F3 | §6 (O) | #581, #583, #586 |
| 6 | Protocol matrix; three fixtures with same-instance isolation and security defaults | spec §10–§12 | V-3, V-4; §6 (S) | #581–#583, #634 |
| 7 | Resource method; evidence owners for #585, #584, #613 | spec §13–§15, §17 | §6 (O) | #581, #584, #585, #613 |
| 8 | Existing authorities authoritative; new metadata minimal, budgeted, versioned, approved | spec SH-1, §4.1, §4.5, §13 budgets; ADR D1–D2; SD-01, SD-05 | §6 (A) | intent tranche |
| 9 | Docs/links/examples/contract checks pass; decisions recorded in #580 and #579 | §5 | V-3–V-6 | issue records need publication authorisation |
| 10 | Summary and withdrawal implementable from a field/state/action contract without confusing admin OIDC, reachability or a URL with authorization | spec §4.3, §4.6, §9, SH-3, SH-4 | §6 (S, U) | #586, #587 |
| 11 | Example states: deliberate public, restricted success/denial, provider outage/unknown, stale evidence, removal not ending an open stream | spec §4.6 | — | #581, #583 |
| 12 | Intent mapped to #633 findings and #634 assertions; unsupported/unanalysed explicit; no second store | spec §4.5 tables | §6 (A) | #633, #634, intent tranche |
| 13 | Executable usability rubric for #585/#587; #588 keeps its later GO gate | spec §14, §16; ADR D8 | §6 (U) | #585, #587, #588 |

Issue deliverables 1–8 map to: (1) spec §1 and ADR 0025; (2) §4; (3) §5;
(4) §6–§7; (5) §8; (6) §10–§11; (7) §12; (8) §2, §4.3, §9, §9.8, §13. The
resource and value evidence contract maps to §13–§15 and §17.
