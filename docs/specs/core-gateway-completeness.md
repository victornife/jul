# Core Gateway Completeness

**Status:** current bounded product contract  
**Effective date:** 2026-08-03  
**Parent programme:** #108  
**Related decisions:** #114-#118

> **Document roles:** ADR 0013 decides how work enters the portfolio; ADR 0014 decides the required operator/developer surfaces; `docs/operating-model.md` defines execution discipline; `docs/specs/core-gateway-completeness.md` defines the bounded product; the roadmap and #62 own current order and status.

This specification defines when Jul.IA can be considered a coherent, complete
**standalone edge and protocol gateway**. It is intentionally bounded. It does
not require every feature found in NGINX, Envoy, Caddy, HAProxy, API-management
platforms, service meshes or hosted control planes.

The completeness objective is:

> An operator can deploy one Jul.IA node, express common gateway policy safely,
> trust inbound and backend identities, bound upstream failure behavior, manage
> configuration through a clear authority model, observe and diagnose the
> runtime, and understand every unsupported or restart-bound boundary without
> relying on hidden behavior or a second control plane.

## 1. Completion states

Every domain must end in one of these states:

- **complete:** implementation, tests, operator surface and documentation meet
  the contract;
- **complete with a truthful limitation:** the bounded product deliberately
  excludes a capability and documents the safe alternative;
- **selected operational enhancement:** valuable but not required to define the
  core product boundary;
- **experiment:** implemented only behind an explicit technical experiment;
- **outside the standalone product boundary:** fleet/distributed/hosted concern;
- **blocked:** confirmed P0/P1 defect or missing architecture decision prevents
  completion.

A domain is not complete merely because code exists or a historical GA table is
checked.

## 2. Required foundation

Before Core Gateway Completeness can close:

- current correctness/security defects are resolved or explicitly release-gated;
- cache correctness and maturity are re-certified;
- configuration parsing/validation is strict and shared across every entry path;
- every public configuration leaf has one closed-world lifecycle disposition;
- current documentation, generated contracts and runtime behavior agree;
- exact-SHA default/full/race/frontend/docs/protocol evidence exists.

Current prerequisite programmes are tracked by the current audit, roadmap and parent epic.

## 3. Core HTTP and protocol serving

### Required

- HTTP/1.1 and HTTP/2 serving;
- optional HTTP/3 with truthful TLS/client-auth behavior;
- static files and redirects/returns;
- reverse proxy and common application gateway targets;
- WebSocket and streaming transparency;
- native gRPC and JSON transcoding when compiled;
- optional L4 TCP/UDP stream proxy;
- deterministic routing and error behavior;
- bounded request/header/body/timeouts;
- graceful startup, reload and shutdown.

### Complete with limitations

The core product does not require:

- universal protocol sniffing on one port;
- permanent simultaneous TLS and plaintext service on one address;
- gRPC-Web;
- arbitrary body transformations;
- mail proxying;
- every NGINX module or directive;
- transparent service-mesh interception.

## 4. Inbound identity and trusted proxies

> **Decided.** [ADR 0016](../adr/0016-inbound-identity-and-backend-peer-trust.md) accepts D09 and D10 and
> freezes the public names: `[servers.client_address]` inbound (`trusted_proxies`, `forwarded_headers`,
> `max_hops`) and `backend_tls` outbound. Implementation is tracked by #135, #136 and #259.

### Target contract — D09

Each server has an explicit trusted-proxy policy. Jul.IA preserves:

- the direct socket peer;
- parsed forwarding-chain evidence;
- one canonical client identity stored in request context;
- the trust decision and source;
- bounded safe projection for logs/status/traces.

Accepted interpretation:

1. use standardized `Forwarded` first when valid and configured;
2. use `X-Forwarded-For` as the documented fallback;
3. evaluate proxy hops from right to left;
4. trust a forwarded client only across configured trusted proxies;
5. treat malformed/untrusted chains conservatively;
6. never let arbitrary client headers redefine security identity.

### Consumers

The canonical identity is used consistently by:

- CIDR authentication;
- rate limiting;
- WAF and security logging;
- access logs and traces;
- forwarding/header policy;
- operator status where safe.

### Non-goals

- geolocation or reputation service;
- PROXY protocol on every HTTP listener unless separately configured;
- external identity provider;
- client identity inferred from public DNS.


## 5. Backend peer trust

### Target contract — D10

Named upstreams and direct literal targets normalize to one internal
`BackendTLSConfig`/resolved policy. It supports:

- plaintext versus TLS target mode;
- system roots and optional private CA roots;
- client certificate/key for backend mTLS;
- explicit/derived SNI server name;
- minimum TLS version;
- standard chain/hostname verification;
- optional bounded additional peer identities;
- explicit insecure mode with prominent warning;
- immutable prepared trust material;
- secret-safe status/error categories.

### Protocol parity

The same resolved policy is consumed by:

- HTTP reverse proxy;
- native gRPC proxy;
- JSON/gRPC transcoding;
- descriptor reflection;
- active HTTPS/gRPC health checks;
- discovery-backed backend addresses.

Health checks must not mark a backend healthy under weaker trust than live
traffic.

### Lifecycle

- candidate trust material is parsed before Publish;
- new requests/connections after Publish use the candidate policy;
- old pools/connections cannot be reused by new-generation work after a policy
  change;
- old active requests/streams retire according to documented generation
  lifetime;
- apply success does not falsely claim backend reachability when clients connect
  lazily.

### Deferred

Reusable named TLS profiles are not required initially. Every consumer must use
the normalized internal policy so profiles can be additive later.


## 6. Upstream capacity and resilience

### Target sequence — D11

```text
admission and connection limits
  -> retry attempts/deadline/backoff/budget
  -> simple circuit breaker
  -> evidence-gated outlier ejection
```

### Limits

Jul.IA can bound:

- active logical requests per upstream pool;
- active requests/streams per backend where configured;
- pending work or fail-fast behavior;
- physical connections where the protocol exposes a truthful control;
- long-lived HTTP/gRPC streams for their full lifetime.

Logical work is distinct from physical connections because HTTP/2 and gRPC
multiplex streams.

### Retries

- finite total attempts;
- finite overall retry deadline;
- bounded exponential backoff with jitter;
- pool-level retry budget;
- cancellation-aware waiting;
- safe method/body replay rules;
- no retry after response commitment;
- no security downgrade or plaintext fallback;
- conservative gRPC/streaming scope.

### Circuit breaker

- stable per-backend state;
- closed/open/half-open transitions;
- bounded recovery probes;
- explicit failure taxonomy;
- integration with, but separation from, active health, passive cooldown,
  discovery presence and saturation;
- state continuity/reset based on logical backend identity.

### Outlier ejection

Not a baseline requirement. It is implemented only when realistic multi-backend
fault evidence shows that the simple breaker is insufficient.


## 7. Routing and response policy

> **Decided.** [ADR 0018](../adr/0018-bounded-route-matching-and-response-policy.md) accepts D12
> and freezes the public names: `[servers.locations.match]` gains `methods` plus the
> `[[match.headers]]` / `[[match.query]]` predicate arrays, response policy is the ordered
> `[[servers.locations.response_headers]]` operation list, and CORS is `[servers.locations.cors]`.
> It also fixes route precedence, the no-405 contract, the cache interaction and middleware order.
> Implementation: #145 (request matching) and #146 (response headers/CORS) are merged. #147 (typed
> patch API, Console editors, NGINX importer mapping, real-server E2E) has landed the backend,
> Console and importer surfaces; full H1/H2/H3-explicit E2E and the CORS+cache/error/auth/WAF
> real-server combinations remain covered at the Go integration-test level rather than re-derived
> as separate real-server E2E (see [known-limitations.md](../known-limitations.md)).

### Required bounded feature set — D12

#### Request matching

A location can constrain:

- path using existing exact/prefix/regex semantics;
- method;
- request-header presence/exact/regex predicates;
- query-parameter presence/exact predicates.

Predicates are compiled during handler generation, bounded, deterministic and
ANDed within one route unless a later explicit contract adds a small alternative.

#### Response headers

A location can:

- add response-header values;
- set/replace values;
- remove values;
- reject attempts to mutate framing/hop-by-hop headers unsafely;
- prevent CRLF/control injection.

#### CORS

A location can define:

- exact/bounded allowed origins;
- allowed methods and request headers;
- exposed headers;
- credentials;
- max age;
- deterministic preflight behavior;
- correct `Vary` and cache interaction.

### Determinism

- route order/precedence never depends on map iteration;
- regex is compiled before publication;
- malformed query input is handled safely;
- no request values become metric labels;
- server validation remains authoritative;
- typed patch/API/Console preserve declared order.

### Explicit exclusions

- general expression language;
- arbitrary scripting;
- request-body/content matching;
- arbitrary origin regex;
- canary, mirroring or weighted route experiments;
- response-body rewriting;
- automatic 405 behavior unless explicitly designed;
- universal NGINX `if` emulation.


## 8. Authentication and authorization

### Required

- Basic, JWT/JWKS, forward-auth and CIDR request authentication;
- mTLS identity and per-location verified-certificate requirement;
- local admin shared-token mode;
- opt-in named-principal RBAC with roles and scoped tokens;
- deny-by-default permission enforcement for protected admin routes;
- secret-safe audit attribution;
- secure credential rotation path where selected.

### Complete with limitations

- application authorization remains upstream-owned beyond configured gateway
  policy;
- no OIDC/SAML/SCIM/browser-login service;
- no automatic certificate-to-application-role mapping;
- no distributed identity session store.

## 9. WAF, rate limiting, compression and cache

### WAF

- block/detect behavior;
- bounded request/response inspection;
- clear build-tag behavior;
- redacted/bounded request-target logging;
- no rule-update claim without rebuild/runtime evidence.

### Rate limiting

- bounded local token/connection policy;
- canonical client identity when trusted proxies are configured;
- truthful local/process-reset semantics;
- no distributed/global limit claim.

### Compression

- correct negotiation;
- optional Brotli/Zstd build capabilities;
- `Cache-Control: no-transform` compliance;
- streaming and content-type safety;
- security guidance for secret-bearing reflected content.

### Cache

The cache is complete only after #107 closes with:

- generation-owned cancellable revalidation;
- immutable published entries;
- explicit shared-cache directive/auth/invalidation/`304` matrix;
- Range/If-Range bypass in the first corrected tranche;
- transparent WebSocket/SSE/ResponseWriter behavior;
- race/leak/soak and real-protocol evidence;
- truthful maturity/status documentation.

Distributed cache is outside the standalone boundary.

## 10. Configuration authority and lifecycle

> **Decided.** [ADR 0019](../adr/0019-configuration-authority-generated-contracts-and-resource-identity.md)
> accepts D13 and the widened D14, and fixes the authority field, drift and adoption semantics,
> durable resource identity, the generated-contract set, the external API boundary and the remote CLI
> contract.

### Authority model — D13

One desired-state writer exists at a time. The public field is `[global].config_authority`.

#### `managed`

- Jul owns persistence, history, rollback and planned restart;
- Console/admin/remote CLI may mutate subject to RBAC/CAS/validation;
- unexpected external file edits are drift, not silently adopted — **neither the file watcher nor
  SIGHUP adopts them**;
- explicit adoption uses the complete validation/apply pipeline and its own `config:adopt` permission.

#### `file_owned`

- external file/GitOps owns desired state;
- file watch/SIGHUP remains authoritative;
- Console/API/CLI mutation is denied server-side before any side effect with one stable error;
- read, validation, plan/preview, status and diagnostics remain available;
- Jul does not rewrite or roll back the external file, and records no history.

#### Default

When `config_authority` is omitted the mode is **`file_owned`**. The default is fixed, not derived:
`[admin].enabled` proves the admin surface exists, not that it owns configuration, so a deployment
running the Console for visibility while shipping its file from a pipeline must not be placed in
`managed`. `file_owned` is chosen because a wrong `managed` default fails silently — SIGHUP stops
working — while a wrong `file_owned` default fails loudly, with a Console banner naming the field to
set. Both the effective mode and its origin are reported.

Authority mode is restart-bound and transitions through the complete staged candidate. No other
configuration field can change it.

### Lifecycle authority — D07

Every public config leaf has one disposition:

- `hot_reload`;
- `new_listener_only`;
- `restart_required`;
- `ignored_deprecated`;
- `validation_rejected_reserved`;
- another reviewed explicit class.

Unknown fields and unknown lifecycle paths fail closed. The Go registry is the
machine authority; generated/checkable artifacts are mirrors.


## 11. Generated contracts and automation

### Generated configuration contracts and resource identity — D14

From the code-defined schema plus explicit metadata, generate/check:

- JSON Schema;
- lifecycle/capability/deprecation/secret/identity metadata;
- exhaustive factual configuration reference;
- stable anchors/version information.

Runtime validators remain authoritative for cross-field behavior.

Every externally addressable configuration resource has exactly one identity model — durable
identity, natural key, or revision-scoped selector — and no second field or identity registry exists.
Routes gain an optional durable `route_id`, globally unique within one configuration. Every other
resource keeps the key it already has: upstream and plugin names, RBAC principal names, the listen
address, and `(protocol, listen)` for L4 streams. Server blocks remain a revision-scoped selector;
`servers.*.name` stays a projection label.

### External admin API

- explicit versioned supported subset under `/api/v1`;
- internal Console routes are not stable accidentally, and a route is internal unless classified;
- common error envelope and code/status catalog;
- authentication/RBAC/authority behavior;
- optimistic concurrency, with `base_version` required on every external mutation and a
  client-supplied idempotency key for unambiguous retry;
- preview/apply/stage/pending/history/status contracts;
- deterministic OpenAPI checked against route/DTO catalogs;
- no secret readback, and no raw configuration export in the first external version — which
  includes raw *history snapshot* bodies, since a snapshot is a configuration file; v1 publishes
  history listing, diff and rollback, and raw bodies stay on the internal route.

Remote automation additionally requires admin listener transport security: an external request in
cleartext on a non-loopback listener is rejected on **every authenticated route**, read or write,
before authentication; only `/healthz` and `/readyz` are exempt. The client refuses a non-loopback
`http://` endpoint before transmitting a credential. The transport work is a hard
prerequisite rather than an assumption.

### Remote CLI

A thin client over the supported API:

- plan, diff, apply, stage, status, rollback, export and diagnostics;
- stable JSON and exit-code contract;
- secure token/profile handling;
- exact transaction result;
- no local duplicate apply/lifecycle engine;
- file-owned mutation denial represented truthfully.


## 12. Observability and diagnostics

### Required

- stable released Prometheus contract;
- bounded metric labels;
- request, upstream, health, TLS, cache, WAF, reload and admin signals;
- structured logs with redaction;
- optional tracing with clear build/lifecycle behavior;
- authenticated runtime/status projections;
- audit and managed-apply result history;
- health/readiness distinction;
- no raw secret/error/path/identity as unbounded labels.

### Operator diagnostics

- `jul doctor` is read-only and network-free by default;
- optional network checks are explicit, bounded and policy-aware;
- support bundles are operator-triggered, closed-set, bounded, owner-restricted
  and never uploaded automatically;
- configuration included by default is safe metadata/projection, not raw secret
  values;
- partial collection is reported in a manifest.


## 13. NGINX migration

Completeness requires an honest migration aid, not full NGINX emulation.

- every parsed directive/block receives an assessment result;
- stable classes: supported, approximated, ignored, blocking, informational,
  parse/validation error;
- security/routing/availability risk is explicit;
- source file/span/include provenance;
- generated Jul path and remediation code;
- strict validation of generated candidates;
- no secret values in reports;
- safe bounded include traversal;
- sanitized corpus and selected-dimension real E2E;
- no universal compatibility percentage.


## 14. Console and operator experience

Every core runtime feature requires an appropriate operator surface, not
necessarily a bespoke dashboard.

Use:

- structured forms for common safe edits;
- raw TOML as the complete expert escape hatch;
- server-authoritative validation/lifecycle;
- preview/diff and exact terminal result;
- live versus desired/staged state;
- permissions and strong confirmation for dangerous changes;
- accessible keyboard/focus/error behavior;
- no secret readback;
- no TypeScript duplicate of routing, lifecycle or resilience algorithms.

A runtime/operator capability requires an understandable Console surface in the Full build. Machine-native capabilities such as schema generation, migration assessment, CI validation and controller integration may be API/CLI-first without a bespoke editor. Diagnostics may be CLI/API-first but should expose Console status or a trigger when that adds operational value. All surfaces reuse the same server-side semantics, per ADR 0014.

## 15. Runtime dynamics and restart boundary

Universal hot reload is not a completion requirement.

### Selected examples

- static certificate/key provider rotation;
- admin authentication snapshot rotation;
- log format and bounded metrics option;
- access-log sink generations;
- selected cache scalar policy after cache recertification;
- Alt-Svc max-age/clear state;
- selected admin operational policies.

### Structural/gated examples

- cache backend/path replacement;
- egress policy across all clients/workers/pools;
- tracing provider generations;
- admin listener relocation/self-disable;
- mTLS policy connection epochs;
- H3 listener enable/disable;
- ACME account/issuer/cache handover;
- TLS/plaintext/h2c retained-address transitions;
- history backend relocation.

A complete planned-restart path and truthful lifecycle classification is an
acceptable final outcome.

## 16. Explicitly outside core completeness

- hosted Jul Cloud;
- fleet controller and staged multi-node rollout;
- distributed configuration/state stores;
- Kubernetes/Gateway API controller;
- service mesh/data-plane sidecar model;
- OIDC/SAML/SCIM identity platform;
- GraphQL composition engine;
- AI provider/model gateway;
- semantic cache/vector store;
- billing/multi-tenant quota platform;
- universal NGINX directive/module parity;
- arbitrary scripting/expression runtime;
- automatic telemetry upload or phone-home.

## 17. Evidence and closure

#108 may close only when:

- all prerequisite P0/P1 correctness programmes are complete;
- decisions #114-#118 are merged and reflected in implementation issues;
- canonical client identity is implemented;
- backend trust is enforced consistently across protocols and health;
- limits/retries/circuit have integrated evidence;
- routing/response policy and typed operator surfaces are complete;
- authority/generated schema/OpenAPI/CLI contracts are complete;
- every core domain has an explicit completion state;
- real H1/H2/h2c/H3/TLS/mTLS/gRPC/WebSocket/SSE/stream tests pass as applicable;
- default/full/race/frontend/docs/generated/security gates pass on the exact SHA;
- soak/resource trends are stable;
- current audit, status, known limitations and release notes are updated;
- residual exclusions remain explicit and safe.

## 18. Programme map

| Domain | Decisions | Implementation |
| --- | --- | --- |
| Operating model and boundary | #114, this specification | #108 closure |
| Inbound identity | #115 — accepted, [ADR 0016](../adr/0016-inbound-identity-and-backend-peer-trust.md) | #135-#136, #259 |
| Backend trust | #115 — accepted, [ADR 0016](../adr/0016-inbound-identity-and-backend-peer-trust.md) | #109, #137-#140 |
| Resilience | #116 | #110, #141-#144 |
| Routing/response policy | #117 — accepted, [ADR 0018](../adr/0018-bounded-route-matching-and-response-policy.md) | #145-#147 |
| Authority/generated contracts/API/CLI | #118 — accepted, [ADR 0019](../adr/0019-configuration-authority-generated-contracts-and-resource-identity.md) | #111, #148-#151 |
| Lifecycle authority | D07 | #89, #128, #149 |
| Cache correctness | D05-D06 | #107, #131-#134 |
| Selected runtime dynamics | D08/D15 | #88-#106, #157-#161 |
| Migration/diagnostics | operating model | #112, #152-#156 |
| AI experiment | experiment governance | #113, #162 |

See [operating-model.md](../operating-model.md), the
[combined audit](../audit/combined-audit-2026-08-03.md) and #62.
