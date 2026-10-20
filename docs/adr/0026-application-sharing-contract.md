# ADR 0026 — Application-sharing contract: derived view, declared intent and complete withdrawal

- **Status:** Accepted — 2026-10-19, maintainer acceptance of the whole contract under [#580](https://github.com/victornife/jul/issues/580); amended 2026-10-19 under [#642](https://github.com/victornife/jul/issues/642) (`SHARE-01B`, bundling [#563](https://github.com/victornife/jul/issues/563) and [#562](https://github.com/victornife/jul/issues/562)) to specify the outermost withdrawal gate, publish-time file-content classification, owned H1 upgraded-session lifecycle, and exact-entry retirement on narrowing. Decisions SD-01 to SD-16 were answered individually on 2026-10-18 and 2026-10-19; the rest was accepted with the contract (verbatim record in the review packet §3, answer 14).
- **Date:** 2026-10-18 (amended 2026-10-19 for #642 / #563 / #562)
- **Deciders:** Jul.IA maintainer (decision owner); #580 / #642 design authors
- **Applies to:** the sharing programme [#579](https://github.com/victornife/jul/issues/579) and successors #581–#588, #613; inputs to #627, #633, #634; #562/#563
- **Source:** `main` at `64e4a8b29230134c5dee37dfd8bfa130a9580a2a` (#641 `SHARE-01A` merged at `7280e255373a088540c9981008ab1b265ce2b4df`); [specification](../specs/application-sharing.md); [design review packet](../reviews/2026-10-18-580-application-sharing-design.md)

## Context

Jul is a standalone gateway. Sharing an application still requires the operator
to coordinate reachability, audience, authentication and operation across the
host, connectors, identity providers and the application. The source baseline
constrains any design:

- There is no exposure, audience or application model. Console **Apps** are
  upstream pools; static sites have none.
- `route_id` is optional and minted only by a managed structured create; server
  `name` is descriptive; ADR 0019 forbids an identity sidecar and forbids Jul
  writing identifiers into configuration it does not own.
- Location checks are CIDR then one of Basic, JWT or forward-auth; redirect
  rewrites, WASM request plugins and the CORS preflight terminator answer before
  them; client-certificate
  policy is listener-wide; JWTs are read only from `Authorization: Bearer`; an
  unmatched `Host` reaches the first-declared block; the response cache key is
  method + Host + URI.
- Existing Console and API projections read the saved configuration file; the
  server separately holds an atomically published serving snapshot.
- A change reaches every new request, including new requests and streams on open
  connections. In-flight exchanges, hijacked WebSockets and certificate identity
  on an open TLS connection continue (a local probe confirmed the last). There
  is no connection-age limit and no termination API. Comparable gateways'
  connection-age limits do not end upgraded streams; Go and quic-go lack
  per-connection graceful close.
- Providers keep established flows (Tailscale) or sessions (Cloudflare Access,
  default 24 h) after a policy change unless explicitly revoked.

## Decision

1. **Derived view (SD-01).** The sharing view is derived from the authoritative
   configuration. Its unit is the *entry* (one HTTP location). Entries are
   grouped by server block for display only. Operations act on explicit,
   previewed entry sets; bounded, response-local backend groups are advisory,
   never identity. No application resource, mapping store or new identifier
   namespace. Statements about current access come from the **serving**
   generation, read from one captured snapshot: text from the raw
   configuration, closed-set access facts from the effective configuration.
   Saved-but-not-serving configuration is a separately labelled layer (SD-10,
   SH-13).
2. **Declared intent in the configuration, built early (SD-05 "B+").** One
   optional location block, `[servers.locations.sharing]` with `mode`,
   `audience_label` and a declared, never-verified `external_enforcer` (SD-12),
   requires a `route_id` on the same location, has no runtime effect, and drives
   **intent findings** in lint and plans plus an explicit Console acknowledgement
   before any change classed `widened` or `uncertain` (spec §9.5). The declared
   enforcer is context only: it never changes a finding (reconfirmed by the
   maintainer once its warning-reduction benefit was withdrawn). Every plan also
   returns a stateless **postcondition** carrying expected presence or absence
   per affected entry and an access fingerprint over the access inputs as
   written, and digests of the configuration as written. File contents
   (htpasswd, CA, CRL, WAF rules, plugin modules, `${file:…}` values) never
   enter plan or postcondition output, because write access is not read access;
   they are named as not compared, and their changes are classified only at
   publish, for retirement, whose effect appliers, status readers and affected clients can observe. Every digest is keyed
   under a key held only by the running process, so the object allows no
   offline check of any secret, whether referenced, literal or inside a local
   file. It can be compared until that process restarts. Afterwards a re-plan
   confirms a raw candidate or repeatable typed operations; non-repeatable ones
   (for example adding or removing a route) stay unverifiable and are
   reconciled by hand. It never discloses a comparison of values its caller cannot
   read: a raw-candidate plan, or a typed operation that sets a field hidden
   from the caller, returns one only to a caller who may also read the raw
   configuration. A read-only server operation, with the same permissions as
   configuration inspection, evaluates it against the serving snapshot (SD-11)
   and answers two questions separately: is the exact planned configuration
   serving, and do the expected access facts match (SD-13)? Only both together
   may be described as the change serving. References without a `route_id` are
   positions resolved only in the exact configuration revision they name. Remote authorities (a JWT key set,
   forward-auth decisions, and the provider policy of any entry whose declared
   intent relies on one) are named as not compared. Delivered as a bounded
   tranche (#641) before #581's evidence run.
3. **Four access modes (SD-03).** `preview`, `private_network`, `restricted`,
   `public`. Private-network membership may be a declared *coarse* audience,
   enforced by the network provider and never presented as a named identity.
   `[[stream]]` listeners are outside the sharing contract.
4. **Withdrawal promise and complete withdrawal (SD-02 "D+E", SD-04, bundle).**
   First journeys promise denial of new access from the published generation and
   display what continues. Withdrawal uses `auth.deny` in place through a
   complete candidate, refusing entries with redirect rewrites, request plugins
   or CORS. #587's guided withdrawal ships only with **#642 as a whole** (SD-07):
   a dedicated, address-independent withdrawal gate placed after entry
   selection and before the entry's rewrites, plugins, preflight and checks,
   together with #563/#562 **retirement on narrowing**. Retirement is
   **exact-entry** (SD-08): stream-level, with per-request revocation re-checks
   instead of closing shared connections; any collateral is a disclosed
   downgrade. Changes Jul cannot classify are treated as narrowing, with an
   explicit keep-open exception (SD-09). No periodic connection age and no
   general session-termination API.
5. **Withdraw in place.** Withdrawal never deletes a route.
6. **Separation, trust and cache.** One Jul listener per external enforcement
   domain; same-host processes are inside the trusted boundary, otherwise
   provider entries need a Jul-side check (SH-11). No authorization
   decision-maker — application, forward-auth service or validator — trusts
   connector identity headers; provider identity is accepted only as a
   cryptographically validated token (SH-12). Restricted and private entries
   must keep Jul's response cache off in the first release; Jul warns when they
   do not, and does not refuse yet. Refusal is the long-term default for
   entries that declare `restricted` or `private_network` or carry a Jul check,
   introduced with a migration (SD-16). Jul's cache is
   process-wide and keyed by method, Host and URI, so public entries answering
   the same host and path on different listeners share entries; plans warn, and
   partitioning is the cache owner's decision.
7. **Three layers never merge.** Declared intent, configured posture and observed
   evidence are reported separately, with freshness and vantage. Ambiguous
   outcomes, including a lost apply ledger after restart, are reported as
   unknown and never retried automatically. File-owned changes stay unconfirmed
   until a stateless postcondition returned by preview is evaluated against the
   serving revision by the same Jul process (after a restart, by re-planning a
   raw candidate or repeatable operations); Jul stores no preview state. The view is versioned
   (`sharing_view_version`) and its closed codes are never repurposed.
8. **Outcome gate (SD-06).** #585 predeclares targets for decisions, handoffs and
   interventions; #587 claims improvement only with zero dangerous failures
   across the six rubric tasks; if provider handoffs dominate, #588 returns GO
   (one provider vertical slice), REDUCE or DEFER on that evidence.
9. **Classification.** Operational enhancements composing existing capabilities,
   plus the two bounded tranches above. Provider lifecycle (#588), group sharing
   (#589), native application login, provider mutation and invitations remain
   separately gated.

## Amendment — Complete withdrawal, outermost withdrawal gate and retirement on narrowing (#642 with #563 and #562)

This amendment settles the public schema, runtime mechanisms, protocol matrix,
enforced bound and collateral rules required by Decision 4 above (`SD-02 "D+E"`,
`SD-04`, `SD-07`–`SD-09`, `R04`, `RR12`, `RR13`) before `#642` runtime
implementation:

1. **Outermost withdrawal gate (`[[servers.locations]]` `withdrawn = true`).**
   - **Public TOML field and shape:** An optional boolean `withdrawn = true` on
     `[[servers.locations]]` (default `false`; lifecycle `hot_reload` under
     `servers.locations.withdrawn`; available in both lean and full builds with
     no build tag). It requires neither `[servers.locations.sharing]` nor a
     `route_id` in raw TOML, and coexists with every location action/target
     (`proxy_pass`, `root`, `fastcgi_pass`, `uwsgi_pass`, `return`, `redirect`,
     `deny`, `grpc_transcode`) and every modifier (`rewrites`, `plugins`,
     `cors`, `auth`, `waf`, `rate_limit`, `cache`, `require_client_cert`).
     Recovery is a single reversible edit (`withdrawn = false` or field
     omitted) that restores the exact prior route behavior.
   - **Stage-0 placement (`R04`):** Evaluated in the router immediately after
     path canonicalisation (`canonicalizeRequest`) and location selection
     (`srv.selectLocation(req)`) and after metric route label attribution, and
     **before** location rewrites (`applyRewrites`, including `redirect` and
     `permanent`), request-phase WASM plugins, CORS preflight
     (`middleware.Preflight`), client-certificate verification
     (`middleware.ClientCert`), location authentication (`auth.*`), WAF, rate
     limiting, response caching and the location's action. The server-level
     `redirect_https` remains before location selection.
   - **HTTP response and `no-store` precedence:** When `withdrawn = true`, Jul
     answers immediately with `403 Forbidden`, `Cache-Control: no-store` and
     `Content-Type: text/plain; charset=utf-8`, without executing CORS preflight
     `204` or storing in the response cache. Non-conflicting location
     `response_headers` may still decorate the response, but **withdrawal's
     cache prohibition (`Cache-Control: no-store` and removal of `Expires`) is
     enforced after any `response_headers` (`set`, `add`, `remove`) or
     `expires` decoration**, so a route configured with `expires` or
     `Cache-Control: public, max-age=…` (or `remove = ["Cache-Control"]`) never
     emits a cacheable `403` when withdrawn.
   - **Sparse repeatable typed patch operation:** `location_set_withdrawn`
     (`withdrawn: bool`, addressed by `route_id`, `route_selector`,
     `server_index` + `location_index`, or top-level
     `listen`/`server_names`/`match_type`/`path`/`match_ordinal` selector
     fields; positional `server_index` + `location_index` and `match_ordinal`
     are guarded by `base_version`), classified as repeatable
     (`IsPatchOpRepeatable == true`) for `§9.4` postcondition recovery.
   - **Delta and projection integration:** Setting `withdrawn` from `false` to
     `true` is classed `narrowed` (triggers `§8.4` retirement); clearing it from
     `true` to `false` is classed `widened` (requires Console acknowledgement).
     `GET /api/sharing` reports `withdrawn = "yes" | "no" | "unknown"` (`"yes"`
     when `withdrawn = true`, `deny = true`, or CIDR `deny` covers all IPv4 and
     IPv6 addresses; `"unknown"` in the `saved` layer when written as a secret
     reference).

2. **Publish-time content classification for file-backed access inputs (`§9.5`, `RR12`, `RR13`).**
   - Plans **may read files while preparing and validating the candidate**, as
     today, but plan output (audience-delta classes, intent findings, warnings
     and postconditions) must **never compare or disclose file contents**
     (`RR11`, `RR12`), and postcondition evaluation
     (`POST /api/sharing/postcondition/evaluate`) must **never reread** external
     files.
   - At `Publish`, the server compares the loaded contents of file-backed
     access inputs between the retiring serving generation and the newly
     published generation to drive `§8.4` retirement only:
     - `htpasswd` (`auth.basic.file`): entries only removed with remaining
       lines byte-identical → `narrowed`; entries added or modified →
       `uncertain`; byte-identical → `unchanged`.
     - `client_auth` CRL (`crl_file`): revoked `(issuer, serial)` pairs only
       added for the same issuer set → `narrowed`; only removed → `widened`;
       issuer set changed → `uncertain`; byte-identical → `unchanged`.
     - `client_auth` CA bundle (`ca_file`): byte-identical → `unchanged`; any
       change → `uncertain`.
     - WAF external rule/include files (`external_digest`): byte-identical →
       `unchanged`; any change → `uncertain`.
     - WASM plugin modules (`ModuleIdentity.Digest`): byte-identical →
       `unchanged`; any change → `uncertain`.
     - `${file:…}` secret references in effective access inputs: resolved value
       unchanged → `unchanged`; resolved value changed → classified by `§9.5`
       directional rules on the resolved values (`narrowed`, `widened`, or
       `uncertain`).
   - **Visibility boundary (`RR13`):** Publish-time retirement (per-entry ended
     exchange counts in apply results and status, plus client stream closure) is
     a bounded equality/narrowing signal about previously loaded file contents
     produced only by an actual publish; it adds nothing beyond `config:apply`
     (`E63`).

3. **Exact-entry retirement on narrowing, constituent-reason eligibility, atomic admission and enforced bound (`#563` + `#642`, `SD-08`, `SD-09`).**
   - **Trigger set:** Any entry whose publish-time effective access delta
     (combining raw configuration changes and publish-time loaded file-content
     changes) is `narrowed` or `uncertain` (including any target or upstream
     backend change while open exchanges remain), plus any entry removed in the
     new generation.
   - **Per-entry keep-open exception (`SD-09`) — defined from constituent reasons:**
     - Apply and patch-apply requests accept an optional `keep_open_entries`
       list (by `route_id` or canonical entry coordinate).
     - Eligibility is determined from the **constituent change reasons, never
       from the combined entry class alone**: an entry is eligible
       (`keep_open_eligible: true`) **if and only if** it remains present in the
       candidate, its retirement trigger arises **solely** from `uncertain`
       constituent changes (such as a backend target/upstream change, host/match
       reshaping or unclassified edit), and **zero** constituent changes are
       mandatory `narrowed` tightenings (withdrawal gate set, Jul network,
       credential or client-certificate check added or tightened,
       `require_client_cert` set, `client_auth` mode raised to `require`,
       listener bind narrowed, CORS narrowed, WAF enabled/tightened, or
       publish-time `htpasswd` entry removal / CRL serial addition).
     - **Mandatory tightening, withdrawal or removal always overrides
       `keep_open_entries`**, even when combined with an `uncertain` change in
       the same candidate (for example, adding a CIDR restriction **and**
       changing `proxy_pass` produces a combined class of `uncertain`, sets
       `keep_open_eligible: false`, overrides any supplied `keep_open_entries`
       item, and retires the entry's open exchanges). Likewise, if an entry is
       `keep_open_eligible` in the plan (written inputs only) but publish-time
       file-content classification detects a `narrowed` tightening (`htpasswd`
       user removal or CRL revocation addition), mandatory tightening overrides
       `keep_open_entries` at publish.
     - When `keep_open_entries` is honoured for an eligible entry, stream and
       upgrade retirement is skipped for that entry on that publish,
       `keep_open: true` is recorded in the apply result and status, and the
       entry's complete-withdrawal claim is forfeited (`"new access updated;
       open connections kept by operator exception"`).
   - **Never triggers:** `unchanged`, `metadata_only` (`[servers.locations.sharing]`
     edits), `widened`, non-access inputs (`rate_limit`, non-WAF
     `client_max_body_size`, `cache`, `compression`, timeouts,
     `response_headers`), and external events.
   - **Deadline origin, grace, hard upper bound and cross-generation atomic admission (`#563`):**
     - Time origin: `Publish` of the new serving generation (before
       `applied_live` / `applied_degraded` returns).
     - Enforced bound (fixed server contract, no new TOML knob): **`1s`
       maximum graceful protocol close window (`RetirementGrace = 1s`) + `1s`
       forced cutoff = `2s` absolute closure deadline (`RetirementBound = 2s`)**
       from `Publish`.
     - **Atomic admission-or-retire invariant:** Exchange registration is
       synchronised with generation publication so a request that selected an
       older handler generation just before `Publish` cannot register after the
       retirement scan and escape retirement. Every admitted exchange either
       evaluates the newly published generation's policy or registers into the
       retiring set with that publish's original `(publishTime + 2s)` deadline
        already attached.
     - **Coverage across older generations:** Active exchanges are tracked
       across generations in a process-lifetime, entry-correlated registry (by
       durable `route_id` or correlated `(genID, server_index, location_index)`
       chain). When generation $G_k$ triggers retirement for an entry, it
       retires **every** still-open exchange mapped to that entry from **all**
       prior generations ($G_1 \dots G_{k-1}$), including exchanges preserved
       across an intermediate reload by `keep_open_entries`.
     - **Monotonic, irrevocable deadline invariant:** Client/backend I/O
       activity, repeated reloads, and any subsequent `keep_open_entries`
       request **never** disarm or extend an already-armed retirement deadline
       (`deadline = min(existingDeadline, publishTime + 2s)`).

4. **Protocol retirement matrix, per-request certificate re-check and owned H1 upgrades (`#563`, `#562`, `SD-08`).**
   - **Per-request client-certificate policy re-check (`#563`, `#642`):**
     - On every HTTP/1.1, HTTP/2 and HTTP/3 request arriving on a TLS listener,
       Jul re-evaluates `r.TLS` against the listener's **current** published
       `clientAuthBundle` (mode `require`/`request`/`none`, current CA pool
       chain verification, current CRLs by `(issuer, serial)`, and current
       `verify_san` allow-list) and the location's current
       `require_client_cert` setting before location checks and action run.
     - If the current policy requires a verified client certificate (`mode ==
       "require"` or `mode == "request" && loc.RequireClientCert`) and
       `r.TLS.PeerCertificates` is absent or fails current CA, CRL or SAN
       verification, Jul denies the request with `403 Forbidden` on that stream
       **without closing the underlying TLS, HTTP/2 or HTTP/3 connection**, so
       other entries multiplexed on the same connection continue uninterrupted.
     - Full and resumed TLS 1.2/1.3 and QUIC handshakes continue to enforce
       `VerifyConnection` (`#486`), and 0-RTT remains disabled.
   - **In-flight HTTP/1.1, HTTP/2, HTTP/3 requests, SSE and gRPC streams:**
     - Every request admitted to an entry registers an entry-scoped exchange
       handle carrying a cancellable `context.Context` and stream abort hook.
     - **HTTP/2 streams (requests, SSE, downloads, native gRPC, transcoded
       gRPC):** Cancelling the narrowed entry's exchange context aborts only
       that HTTP/2 stream (`http.ErrAbortHandler` / `RST_STREAM` on that stream
       ID) within `<= 2s`, leaving sibling streams for other entries on the
       same HTTP/2 connection uninterrupted (`collateral_count = 0`).
     - **HTTP/3 (QUIC) streams:** Cancelling the narrowed entry's exchange
       context and resetting/closing that request's QUIC stream ends only that
       stream on the `quic.Conn` within `<= 2s`, leaving sibling HTTP/3 streams
       for other entries on the same QUIC connection uninterrupted
       (`collateral_count = 0`).
     - **HTTP/1.1 in-flight requests / SSE / downloads:** HTTP/1.1 carries at
       most one active request per connection; cancelling the exchange context
       and closing the connection by the `2s` bound ends only that entry's
       exchange (`collateral_count = 0`). Idle HTTP/1.1 keep-alive connections
       carry no entry binding and evaluate the new generation on their next
       request.
   - **Owned H1 upgraded sessions (`#562` — WebSockets and opaque `101` upgrades):**
     - At the `101 Switching Protocols` boundary in `internal/handler/proxy.go`,
       Jul wraps the hijacked client `net.Conn` and upstream connection in an
       **owned upgraded session** registered with both the cross-generation
       entry exchange registry and the listener/server shutdown registry,
       preserving `#284`'s exactly-once admission release and `#428`'s
       resource-ownership invariants.
     - **RFC 6455 WebSocket upgrades (`Upgrade: websocket`) — serialized writes,
       masking and conditional graceful counting:**
       - Jul owns write serialization and closure on **both** legs:
         - **Jul → client leg (server-to-client):** frames originated by Jul
           are **unmasked** (`MASK = 0`, RFC 6455 §5.1) and inserted only at a
           WebSocket frame boundary so a control frame never interleaves
           mid-payload of a data frame (RFC 6455 §5.4).
         - **Jul → upstream leg (client-to-server):** any Close frame
           originated by Jul toward the backend is serialized at an upstream
           frame boundary and **masked with a fresh 4-byte key** (`MASK = 1`,
           RFC 6455 §5.1/§5.3) before half-closing/closing the upstream leg.
       - **Best-effort Close within `1s`, forced close by `2s`:** On narrowing
         retirement, Jul attempts best-effort Close initiation (`1008 Policy
         Violation` on narrowing; `1001 Going Away` on server shutdown or
         listener removal) within the `1s` graceful window (`RetirementGrace`)
         and enforces absolute socket closure of both `net.Conn` legs by `2s`
         (`RetirementBound`). If a slow peer or a partially transmitted frame
         prevents safe Close-frame insertion within `1s`, or if the peer does
         not complete the closing handshake in time, Jul forcibly closes both
         legs by `2s`.
       - **Accurate RFC 6455 `graceful_count` vs `forced_count`:** Per RFC 6455
         §7.1.4, an owned WebSocket session counts toward `graceful_count` **only
         when the closing handshake completes** (Jul sends a Close control frame
         and receives a valid reciprocal Close control frame from the client
         within the bound before raw TCP EOF or reset). **Raw EOF, connection
         reset, inability to insert a Close frame at a frame boundary within
         `1s`, or expiry of the `2s` deadline is counted in `forced_count`,
         never in `graceful_count`.**
     - **Non-WebSocket / opaque `101` upgrades:** Jul never fabricates a
       WebSocket frame; on narrowing retirement or server shutdown, Jul closes
       both `net.Conn` legs within the `2s` bound and reports the closure in
       `forced_count`.
     - **Unrelated reloads vs listener removal / server shutdown (`#562`):**
       Unrelated reloads (`unchanged`, `metadata_only`, `widened`) leave owned
       H1 upgraded sessions running uninterrupted in the cross-generation
       registry. Listener removal on reload or full server shutdown drains all
       owned upgraded sessions on that listener within the bounded deadline
       (initiated asynchronously rather than blocking inside
       `http.Server.RegisterOnShutdown`).
   - **Collateral accounting (`SD-08`, `§8.4`):**
     - Stream-level cancellation (`H2`/`H3`), H1 single-request close, owned H1
       upgrade close, and per-request TLS client-certificate re-checks produce
       `collateral_count = 0`. If any fallback ever closes a shared connection
       carrying another entry's active exchange, `collateral_count` is
       incremented, reported per entry in the apply result and status,
       downgrades the entry's status claim to `"new access stopped; open
       connections ended with collateral interruption"`, and counts as a
       failure in `#581` W3.

5. **Preview, Apply Result and Sharing View Observability (`§8.4`, `§4.3`, `§9.8`).**
   - Plans report per-entry `triggers_retirement`, `open_exchanges_snapshot`
     (active requests/streams plus owned H1 upgrades across all live
     generations at plan time), `keep_open_eligible` (`true` only when the
     entry's retirement trigger comes solely from `uncertain` constituent
     changes with no mandatory tightening, withdrawal or removal), and
     `retirement_bound: "2s"`, alongside the once-per-plan
     `file_backed_entries` notice.
   - Terminal apply results and `GET /api/sharing` report per-entry
     `ended_exchanges` (`graceful_count`, `forced_count`),
     `completed_within_bound`, `collateral_count`, `keep_open`, and
     `retirement_bound: "2s"`, with no client identities and no new metric
     labels (`SH-10`).

6. **NGINX migration disposition.**
   - Not applicable: NGINX has no per-location outermost withdrawal gate or
     entry-scoped narrowing stream retirement.

7. **Mandatory Phase 4 acceptance and regression obligations.**
   - **Mixed-change `keep_open_entries` override:** Adding a mandatory
     tightening (for example a CIDR restriction, credential check,
     `require_client_cert`, WAF tightening, or publish-time `htpasswd` removal
     / CRL revocation addition) together with an `uncertain` change (for
     example a backend target/upstream change) classifies the entry as
     `uncertain`, sets `keep_open_eligible: false`, overrides any supplied
     `keep_open_entries` item, and retires open exchanges within `2s`.
   - **Atomic admission vs `Publish` and multi-generation retirement:** A
     request or upgrade racing with `Publish` either evaluates the new
     generation's policy or registers into the retiring set with the original
     `Publish` deadline; a subsequent narrowing retires exchanges retained from
     earlier generations (including those preserved across an intermediate
     reload by `keep_open_entries`); and a later `keep_open_entries` request
     never disarms an already-armed retirement deadline.
   - **RFC 6455 Close handshake vs forced closure counting:** Owned WebSocket
     tests cover completed Close handshake (`graceful_count`), peer TCP EOF
     without a reciprocal Close frame (`forced_count`), slow/backpressured peer
     or mid-frame write preventing Close insertion within `1s` (`forced_count`
     by `2s`), and masked upstream Close framing.
   - **Withdrawal `Cache-Control: no-store` precedence:** A withdrawn location
     configured with `expires` or `response_headers` setting
     `Cache-Control: public, max-age=3600` (or removing `Cache-Control`) still
     returns `403 Forbidden` with `Cache-Control: no-store` and no `Expires`
     header.

## Reversibility and one-way doors

| Decision | Classification | Why | Exit or migration |
| --- | --- | --- | --- |
| Derived view (1) | Reversible until a surface or runbook consumes it; costly afterwards | operators, tests and rubrics encode its semantics; internal API routes carry no compatibility guarantee | `sharing_view_version`; new fields rather than repurposed ones; side-by-side replacement with a deprecation period; closed codes never repurposed |
| `sharing` block names and `mode` values (2) | **Costly once adopted** | public TOML, generated contracts, GitOps files | under the [compatibility policy](../compatibility.md): changeable with notice while Alpha/Beta; a MAJOR release with deprecation once GA; renames need dual-read |
| Intent findings as warnings (2) | Reversible to relax; costly to tighten into rejections | file-owned pipelines would break | tightening needs its own decision and notice |
| Coarse private-network mode (3) | Costly to tighten once displayed | deployments rely on network-only audiences | migration notice; Jul checks stay optional |
| Withdrawal promise (4) | **One-way to weaken**; additive to strengthen | security commitment | complete withdrawal strengthens it |
| Withdrawal gate field (4) | **Costly once adopted** | public schema (`[[servers.locations]]` `withdrawn = true`) and typed operation (`location_set_withdrawn`) | same compatibility rules; settled by the #642 amendment above |
| Postcondition object and evaluator (2) | Costly once adopted | automation and pipelines consume it | `postcondition_version`; observation-only semantics fixed now |
| Retirement on narrowing (4) | Reversible until merged; costly afterwards | operators will rely on the `2s` (`1s` grace + `1s` forced) bound and exact-entry stream retirement | if infeasible, gated claims stay unshipped and §8.2 of the spec remains |
| Withdraw in place (5) | Mechanism reversible; "never delete" one-way to weaken | safety invariant | — |
| One listener per enforcement domain (6) | Costly to relax | topologies built around it | needs a separately decided SNI/Host binding guarantee |
| Restricted cache off (6) | Reversible to relax with evidence | a relaxation risks disclosure | cache-owner decision with partitioned keys |
| Vocabulary in the Console | Reversible until #587 freezes it | — | #585 comprehension check first |
| Public exposure of an entry | **Irreversible disclosure** | content seen cannot be unseen | stated in every widening plan |
| External state (provider policy, DNS, sessions) | Not rolled back by Jul | outside Jul's authority | rollback plans show their audience delta; unknown external outcomes stop and reconcile |

## Consequences

**Positive.** The product can answer "who is this for?" from the configuration
and warn before a change contradicts it. Withdrawal becomes complete for
Jul-routed traffic without a session store or provider integration. Successors
implement and verify without a new state owner. File-owned and expert TOML users
lose nothing; intent is optional.

**Negative and accepted.** Two runtime tranches occupy the shared-code stream
before #587. Field names become public before first-use evidence. #581's
evidence uses a build newer than v2.2.0. Until complete withdrawal lands, open
connections of a withdrawn entry continue unless the operator restarts Jul or
stops a connector. Provider paths need more listeners. Restricted entries must
not use Jul's response cache; a configuration that does is warned about but
currently accepted, until refusal arrives with a migration (SD-16). Classes
involving values the caller cannot read are `uncertain`, which costs
conservative acknowledgement and retirement.

**Neutral.** NGINX migration disposition for this design: not applicable. Each
runtime tranche records its own.

## Alternatives considered

| Alternative | Why not |
| --- | --- |
| Separate application store or sidecar | a second desired state; conflicts with ADR 0019 I2 |
| First-class `[[applications]]` resource in the same authoritative TOML | not a second authority, and a credible later option; its real costs are identities for every member, references that dangle on delete, two edit sites per change, and a migration for existing files. Deferred until evidence shows entry-level operation plus advisory groups is insufficient |
| Defer held intent to #587 (SD-01's original timing) | product could not answer "who is this for?" during #581–#585; reviewers A-3/U-1; maintainer chose SD-05 B+ |
| Require `route_id` globally | contradicts ADR 0019 §4.2 |
| Application scope inferred from upstreams or roots | hidden identity; can widen withdrawal blast radius (A-2/U-2) |
| Periodic maximum connection age | disconnects everyone; does not end upgraded streams in comparable gateways |
| Defer all session work | leaves "who can access" permanently partial; maintainer chose D+E |
| `location_set_auth` or the `deny` action for withdrawal | drops the credential or the target; still inside plugins and preflight |
| `[servers.locations.sharing] withdrawn = true` or a nested `[servers.locations.withdrawal]` table | putting the gate inside `sharing` would require `mode` and `route_id`, violating the §9.3 requirement that the gate works without the intent block; a top-level boolean `withdrawn = true` on `[[servers.locations]]` matches `deny = true` ergonomics while preserving the route's action, target and credentials |
| Configurable per-route or global retirement timeout knob | adds configuration surface before evidence shows a fixed bound is insufficient; a fixed server-enforced `2s` bound (`1s` graceful close + `1s` forced cutoff) keeps the consumer contract deterministic across surfaces |
| Whole-connection closure on mTLS CRL/CA reload | disconnects unrelated entries multiplexed on the same HTTP/2 or HTTP/3 connection (browser connection coalescing); per-request client-certificate policy re-check across mode, CA pool, CRL and SAN allow-list enforces revocation per stream with zero collateral |
| Native Cloudflare token validation in Jul now | not needed while `cloudflared` validates; left to #583 evidence |
| Host-only separation of provider and public entries | no SNI-to-Host check; first-block fallthrough |

## Related

[ADR 0012](0012-oss-open-core-boundary.md), [ADR 0014](0014-operability-surfaces.md),
[ADR 0015](0015-managed-apply-terminal-ledger.md),
[ADR 0016](0016-inbound-identity-and-backend-peer-trust.md),
[ADR 0018](0018-bounded-route-matching-and-response-policy.md),
[ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md),
[ADR 0022](0022-admin-console-oidc.md); [specification](../specs/application-sharing.md);
[known limitations](../known-limitations.md#long-lived-connections).
