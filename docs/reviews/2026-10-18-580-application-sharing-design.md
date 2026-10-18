# #580 application-sharing design — review packet

> **Reviewed:** design branch `design/580-application-sharing-contract`; source
> baseline `main` at `64e4a8b29230134c5dee37dfd8bfa130a9580a2a` (2026-10-18).
> Design revisions reviewed are identified in §6. This packet holds evidence,
> decisions, review findings and traceability for the
> [specification](../specs/application-sharing.md) and
> [ADR 0025](../adr/0025-application-sharing-contract.md). It certifies nothing
> about runtime behaviour beyond the executed checks listed in §5.

## 1. Baseline and inputs

- **Source:** `main` = `origin/main` = `64e4a8b2…` (merge of #637). Published
  package baseline v2.2.0 (`b12e0511…`) was not re-inspected; behaviour below is
  source behaviour unless stated. Probes and checks ran on Go 1.27.2,
  linux/arm64, lean build for `go test`, full-tag build for `jul check`.
- **Issues refreshed 2026-10-18 10:34 CEST:** #580, #579 (with comments), #62
  (body and latest comments), #581–#589, #613, #562, #563, #620–#629, #633,
  #634, #441, #615. #580 was ready and unstarted; #434 completed after the
  planning baseline (TCP ALPN routing, Alpha/unreleased) and adds no sharing
  prerequisite.
- **Documents read:** brief and longer vision, practical guide, operating model,
  roadmap, specs index, ADRs 0009, 0011–0016, 0018 (route matching references),
  0019, 0022; known limitations, security posture, reload semantics,
  mTLS, auth, HTTP/3, stream, CONTRIBUTING, PR template.

## 2. Evidence ledger

Kinds: **F** observed source or test fact; **D** accepted decision (ADR or
issue); **H** hypothesis. Line numbers refer to the source baseline.

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
| E10 | Vhost selection by normalized Host (lowercase, port & trailing dot removed); nameless block is default; else 404; no SNI≠Host check | router/router.go:270-292; host.go:15-18 | F (verified) | domain fronting not rejected | high |
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
| O1 | `TLSConfig.ClientAuth` Go comment says client-auth changes need a restart; the lifecycle registry and #486 tests show hot reload | not changed here (unrelated code comment); listed in spec §17.4 |
| O2 | `jul lint` does not warn when a `request`-mode client-auth listener also serves open entries | candidate warning for #633 (spec §4.4, §17.4) |
| O3 | Jul's response `cache` is rejected on static `root` locations | F1 uses `cache_control` on the portal instead; spec §11 cache rule |
| O4 | `TestLongLivedMTLSStreamSurvivesClientRevocation` covers an established stream and a new handshake, not a new request on the open connection | settled by probe P1 (§4) |

## 3. Decision log

| ID | Date | Question | Options | Answer and owner | Status | Affected |
| --- | --- | --- | --- | --- | --- | --- |
| SD-01 | 2026-10-18 | Where does sharing intent live; what is the view's unit? | A derived view now, annotation pre-specified for #587; B accept location annotation now; C top-level `[[applications]]` | **A** — maintainer | Accepted | spec §4, ADR D1–D2 |
| SD-02 | 2026-10-18 | What does withdrawal promise; #562/#563 GO or DEFER? | A defer; B periodic connection age; C #562 only; D defer + re-decision; E event-driven entry-scoped retirement; E-first | **D+E, parallel** — maintainer, after three evidence rounds (lifecycle tests, probe P1, industry and provider research) | Accepted | spec §8, §17, ADR D4 |
| SD-03 | 2026-10-18 | May private-network membership be the audience boundary? | A coarse provider-enforced mode; B only with extra login; C never | **A** — maintainer | Accepted | spec §5, F2 |
| AD-01 | 2026-10-18 | Withdrawal mechanism | delete route; `deny` action; CIDR deny-all in place | deny-in-place, CIDR deny-all preferred — author, shown to maintainer without objection | Proposed | spec SH-7, §9.3 |
| AD-02 | 2026-10-18 | F1 host layout | one host with paths; separate host names on one listener | separate host names — author, shown without objection | Proposed | F1 |
| AD-03 | 2026-10-18 | User-facing term | "app"; "shared service" | "shared service"/"entry" — author, shown without objection | Proposed | §2 |
| AD-04 | 2026-10-18 | Provider-restricted and public entries on one Jul | Host separation; one listener per enforcement domain | SH-11 — author, from E10 | Proposed | §3, F2, F3 |
| AD-05 | 2026-10-18 | Connector identity headers | trust; ignore and forbid reliance | SH-12 — author, from E08/E09/X08 and the `headers` overwrite-only fact | Proposed | §3 |
| AD-06 | 2026-10-18 | Cloudflare origin enforcement default | cloudflared `access.required`; Jul forward-auth validator; new Jul token-header option | cloudflared default, forward-auth optional, Jul change not authorised — author, from X11–X12 | Proposed | F3 |

"Shown without objection" is not acceptance; AD items become accepted only with
the maintainer's acceptance of #580.

## 4. Probe P1

- **Question.** After a CRL reload revokes a client certificate
  (`client_auth.mode = "require"`), does a new request on the already-open
  HTTP/1.1 keep-alive or HTTP/2 connection still succeed?
- **Method.** A disposable test file in `internal/server` reusing
  `newMTLSReloadFixtureWithHandler`: request, revoke serial, reload
  (`applied_live`), request again on the same transport (`httptrace` reuse
  flag), then a fresh handshake. Loopback only; synthetic CA; about one second.
- **Result.** HTTP/1.1 and HTTP/2: the second request returned 200 with
  `reused = true`; the fresh handshake failed with `bad certificate`.
- **Cleanup.** The file was deleted; `git status` showed no residue.
- **Limits.** Handler-level fixture (not the location `require_client_cert`
  middleware); no HTTP/3; lean build.

## 5. Validation actually run

| ID | Command | Result |
| --- | --- | --- |
| V-1 | `go test -count=1 ./internal/server -run 'TestLongLivedMTLSStreamSurvivesClientRevocation\|TestReloadRefreshesClientCRLWithoutRestart\|TestReloadRotatesClientCAWithoutRestart\|TestReloadDrainsBeforeRetiringClosers\|TestReloadSwapsHandler' -v` and `go test -count=1 ./internal/handler -run 'TestLongLivedWebSocketReloadAndShutdown\|TestLongLivedSSEReloadAndShutdown' -v` | all passed (lean build) |
| V-2 | probe P1 (§4) | as recorded |
| V-3 | full-tag build of the baseline; `jul check -config` and `jul lint -config` for F1, F1-withdrawal, F1-mTLS and the combined F1+F2+F3 configuration with placeholder certificates, htpasswd and admin token | all valid; one intentional lint warning (compression disabled) |
| V-4 | `python3 -c 'yaml.safe_load(...)'` on the F3 `cloudflared` example | parsed; ingress hostnames as intended |
| V-5 | `python3 scripts/docs-check.py` | see §6 for the run against each reviewed revision |

Not run: live provider paths, independent clients, race detector, full CI. None
is claimed.

## 6. Adversarial review

(Recorded below as reviews complete.)

## 7. Traceability

See §7 table (added with the review record).
