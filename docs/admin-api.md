# Jul.IA — supported external admin API

> Updated 2026-08-31 · Contract: [ADR 0019](adr/0019-configuration-authority-generated-contracts-and-resource-identity.md) §24–§30

Jul.IA's admin listener serves many HTTP routes. **Being served is not the same
as being supported.** This document defines which of them are part of the
compatibility contract, what a client may rely on, and what deliberately is not
published.

The machine-readable form is [`generated/openapi.json`](generated/openapi.json).
It is generated from the Go route catalog and the Go request and response types
and is checked in CI — it cannot drift from the server. Do not edit it; change
the source and run `make api-contract-generate`.

## The rule

> **A route is part of the external contract only when it is explicitly
> classified as such.**

The route catalog carries a `Stability` field whose zero value is *internal*, so
a route added without anyone deciding is not published. Every internal route
additionally records *why* it is not external, in
`internal/admin/route_classification.go`, and a guard test holds that inventory
and the catalog to exact-set equality in both directions. A route cannot become
external by being useful, by being called by the Console, or by being forgotten.

| Classification | Meaning |
| --- | --- |
| `public` | Supported, and requires no authentication. Only `/healthz` and `/readyz`. |
| `external` | Supported, and requires a credential and a permission. |
| `deprecated` | Still supported and still served, under `Deprecation` and `Sunset` headers, and still described in OpenAPI so a client can find it. |
| `internal` | Served for the Console. Not supported, not published, and free to change shape in any release. |

## Transport: TLS or loopback, on every authenticated route

**A request that arrives in cleartext on a listener that is neither
TLS-terminated nor bound to loopback is refused**, with `403
insecure_transport`, before route lookup and before authentication.

This applies to **every route that consumes an admin credential** — reads as
well as writes, `/api/v1` and the existing `/api/…` routes alike, and
`/metrics`. Only `/healthz` and `/readyz` are exempt, because they are genuinely
credential-free.

There is no override and no configuration key. A server-side bypass would be the
same hole as an `--insecure` client flag.

**What the server promises, and what it cannot.** It refuses to authenticate or
act on an unsafe channel. It does *not* promise that a credential already
present in the request never crossed the network — by the time any handler runs,
the `Authorization` header has already traversed it, and server-side ordering
cannot unsend it. That half of the control belongs to the client, which is why a
Jul CLI refuses a non-loopback `http://` endpoint before it loads or transmits a
token at all.

The refusal carries only `"required": "tls_or_loopback"`. It is returned before
authentication, so anything it disclosed would be disclosed to an anonymous
caller; the listen address is a configuration value and is deliberately absent.

**Two supported remedies:**

- terminate TLS in front of the admin listener (a reverse proxy, a
  systemd-activated socket, a sidecar), or
- bind the listener to loopback and reach it through an SSH tunnel.

See [compatibility.md](compatibility.md#admin-transport-security-adr-0019-281)
for the migration, and [observability.md](observability.md#scraping-metrics)
for what it means for Prometheus.

## The error envelope

Every `/api/v1` response that is not a success has one shape:

```json
{
  "error": {
    "code": "stale_base_version",
    "message": "The configuration changed since this edit was prepared.",
    "details": {
      "base_version": "1c0d5e9a77b34f21",
      "current_version": "9f2c1ab7d4e05863"
    },
    "request_id": "01M1BKZFQ4JH1831PT56ACVT2D"
  }
}
```

- **`code` is the machine contract.** Switch on it. It is drawn from a bounded
  catalogue, and each code maps to exactly one HTTP status.
- **`message` is not.** It is for a human reading a terminal and may change in
  any release. Never parse it.
- **`details` is a bounded, per-code object.** It carries field *paths*, bounded
  contract constants and identifiers the caller itself supplied. It never
  carries candidate bytes, a resolved secret, a token, or a value read from a
  configuration field.
- **`request_id` is server-minted** and echoed in the `X-Request-ID` response
  header. A client-supplied `X-Request-ID` is never reflected. Quote it in a bug
  report: it correlates the response with the server log, which holds the detail
  the response deliberately does not.

The complete catalogue, with each code's status and its `details` shape, is the
`ErrorCode` component in
[`generated/openapi.json`](generated/openapi.json) — generated from the same Go
declarations the server uses, so it cannot be out of date.

Adding a code is an additive change. Changing a code's meaning, or its status,
is breaking and requires `/api/v2`.

## Versioning

`/api/v1` is the namespace. The version is in the URI — not a media type and not
a header — because it has to be visible in a log line, a `curl` command and a
Console link.

**Additive within `v1`:** new optional request fields, new response fields, new
enum members in response-only positions, new endpoints, new optional query
parameters. **A client must ignore response fields it does not recognise.**

**Breaking, requiring `/api/v2`:** removing or renaming a field or an endpoint,
changing a type, narrowing an accepted enum, changing a status-code mapping,
changing the meaning of an error code, or making an optional request field
required.

There is **no implicit version selection** — never from a user agent, never from
a header default. A request to an unversioned path gets the internal, unstable
route it asked for.

## What is published today

| Path | Method | Classification | Permission |
| --- | --- | --- | --- |
| `/healthz` | GET | public | — |
| `/readyz` | GET | public | — |
| `/metrics` | GET | external | `metrics:read` |

The probes and the scrape target keep their **current unversioned paths**.
Moving a liveness probe or a Prometheus target under `/api/v1` would break every
deployment for no benefit, and their contracts are already released.

`/metrics` is **external authenticated, not public**: it requires `metrics:read`,
so publishing it as public would describe a contract the server does not
implement and imply an unauthenticated scrape endpoint that does not exist.

**The `/api/v1` configuration surface is not published yet.** ADR 0019 §24 fixes
its 25 paths, and the machinery that publishes them — classification, the error
envelope, the transport gate, the generator and its guard tests — is in place;
the operations themselves are tracked on
[#150](https://github.com/victornife/jul/issues/150). Publishing a `v1` path is
a one-way door, so a half-specified one is worse than none.

## What is deliberately not published

Each internal route records its own reason in
`internal/admin/route_classification.go`. The recurring ones:

- **Console dashboard shapes** (`/api/stats`, `/api/apps`, `/api/search`,
  `/api/traffic-controls`, `/api/runtime/overview`) change with the UI that
  renders them.
- **Observability ring-buffer projections** (`/api/observability/*`) are sized
  and shaped for the Console; their capacity is not a contract.
- **Server-sent events** (`/api/events`, `/api/observability/logs/stream`) have
  no `Last-Event-ID` resume. Publishing them would freeze a Console transport
  and hand external clients a stream they cannot resume. Automation polls a
  terminal result instead of holding a connection open.
- **Uploads** (`/api/plugins/upload`,
  `/api/transcode/descriptor-upload`) need their own size, filename,
  path-traversal and streaming-error review before they are exposed.
- **Audit export** (`/api/audit`, `/api/audit/export`) is a strong candidate,
  deferred so the export format is designed on its own terms rather than frozen
  by accident.
- **Raw configuration and raw history snapshot bodies** (`/api/config`,
  `/api/config/history/{id}`, `/api/history/get`, `/api/config/preview`,
  `/api/config/patch/candidate`) are the same data class: a history snapshot
  *is* a configuration file, so it may contain literal secret values. **No
  `/api/v1` route returns raw configuration bytes.** They remain available on
  the internal routes under `config:raw` and `history:raw` for the Console and
  for local operators, and a test fails if either is promoted.
- **Legacy operational endpoints** (`/cache/purge`, `/reload`) and
  `/debug/pprof/` are not part of the configuration surface `v1` publishes.

## Authentication and authorization

Authentication is a bearer token in the `Authorization` header. **There is no
token-issuance API**: tokens are configured out of band, in the configuration
file. The published OpenAPI security scheme carries no example, and none should
be copied from documentation into a deployment.

Authorization is per operation, using the existing RBAC permissions. Three rules
matter to a client:

| Caller | Response for a protected resource |
| --- | --- |
| Unauthenticated | `401 unauthenticated` — no signal about whether the resource exists |
| Authenticated, lacks the permission | `403 forbidden` — also no existence signal |
| Authorized, resource absent | `404 not_found` |

**The permission check runs before the resource is looked up.** That ordering is
what makes the 403/404 boundary meaningful: the reverse order turns every
404-versus-403 difference into an existence oracle for an unauthorized caller.
Resource identifiers are guessable by design — a `route_id` appears in the
Console URL bar — and are never an authorization mechanism.

The one exception is the transport gate above, which returns `403
insecure_transport` where the table would give a `401`. It is deliberate and
discloses nothing: the verdict is a property of the listener, identical for
every request and every principal, and reached without consulting the credential
or the target.

## Generated clients

`generated/openapi.json` is suitable input for a client generator. Two caveats:

- **A generated client is not itself a supported artifact.** Jul.IA publishes
  the document, not a library.
- **The server remains authoritative.** A generated client must not reimplement
  validation, lifecycle classification or authority rules; it should send the
  request and read the answer.

## Related

- [Compatibility policy](compatibility.md) — what is stable and what a
  deprecation window means.
- [Authentication and RBAC](auth.md) — permissions, roles and principals.
- [Reload, staging, authority and rollback](reload-semantics.md) — the
  server-side semantics the API surfaces.
- [Observability](observability.md) — metrics, probes and scrape requirements.
- [ADR 0019](adr/0019-configuration-authority-generated-contracts-and-resource-identity.md)
  — the accepted record this document implements.
