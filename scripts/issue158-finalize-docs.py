from pathlib import Path

# Temporary branch-only finalizer. Removed before merge.


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    if new in text:
        return
    if old not in text:
        raise SystemExit(f"anchor not found in {path}: {old[:80]!r}")
    p.write_text(text.replace(old, new, 1))


def append_once(path: str, marker: str, section: str) -> None:
    p = Path(path)
    text = p.read_text()
    if marker in text:
        return
    p.write_text(text.rstrip() + "\n\n" + section.strip() + "\n")


replace_once(
    "docs/reload-semantics.md",
    """- Changes to **hot-reloadable** fields (routes, handlers, upstreams,\n  compression, rate limiting, etc.) apply exactly as they do through the\n  Console.\n- Changes to **restart-required** fields (cache, egress, admin listener/rate\n  limits/history/plugin-upload/audit, tracing, ACME, log\n  format, listener bind settings) are **rejected at swap time** — the swap is\n""",
    """- Changes to **hot-reloadable** fields (routes, handlers, upstreams,\n  compression, global rate limiting, admin Console/plugin-upload policy, and\n  admin read/write/apply limits plus the shared SSE cap) apply through the same\n  transaction. Admin admission policy is carried by the immutable request\n  generation while token buckets and active SSE leases remain process-stable,\n  so reload neither resets abuse state nor disconnects existing streams.\n- Changes to **restart-required** fields (cache/egress fields that retain that\n  lifecycle, `admin.enabled`, `admin.listen`, admin history/audit resources,\n  tracing, ACME, and retained-listener bind settings) are **rejected at swap time** — the swap is\n""",
)

append_once(
    "docs/console.md",
    "## Admin admission controls (HR-07A)",
    """
## Admin admission controls (HR-07A)

The Admin runtime settings drawer can change the live read, write and high-impact
apply request budgets plus the per-transport-client SSE connection cap without
restarting an existing admin listener. Request-rate values preserve the canonical
configuration semantics: zero selects the configured default, a negative value
disables that request class, and a positive value is an explicit requests-per-minute
limit. For `max_event_conns`, zero selects the canonical default (4), positive values
set the cap, and negative/unlimited values are not supported.

Reload does not reset accumulated client quotas. A tighter policy governs the first
new admission after Publish. Lowering the SSE cap never terminates an existing event
or live-log stream; clients already at or above the new per-client cap must fall below
it before opening another stream. Lifecycle badges and preview remain server-derived,
and mixed changes that include `admin.enabled` or `admin.listen` remain whole-candidate
restart/staging operations.
""",
)

append_once(
    "docs/security-posture.md",
    "## Admin admission-state continuity (HR-07A)",
    """
## Admin admission-state continuity (HR-07A)

Admin request limiting is keyed by the transport peer IP from `RemoteAddr`; untrusted
`Forwarded` and `X-Forwarded-For` values never select a bucket. The limiter manager is
stable for the admin runtime lifetime, while the read/write/apply policy is taken from
the same immutable admin generation captured once per request. This deliberately
preserves accumulated abuse state across reloads: a configuration change cannot grant
a fresh burst, reset active SSE counts, or erase another request class's history.

Secure-transport approval remains ahead of admission/authentication, and rate-limit
refusal does not inspect bearer-token contents or authorization results. External
`/api/v1` refusals retain the closed `rate_limited` response contract, including a
server request ID and a `Retry-After` value matching `details.retry_after_seconds`.
Operational metrics/status use bounded classes and aggregates only; client IPs,
principals, tokens, request IDs and policy generations are not metric labels.
""",
)

append_once(
    "docs/known-limitations.md",
    "## Admin admission policy (HR-07A)",
    """
## Admin admission policy (HR-07A)

Admin request-rate buckets and SSE lease accounting are in-memory and process-local;
there is no distributed/fleet-wide quota coordination. The admission identity remains
the transport peer IP. Configuration reload preserves that in-memory state, but an
actual process restart resets it. Reducing `admin.max_event_conns` affects only new
SSE admissions: existing event/log streams are intentionally not forcibly drained.
""",
)

append_once(
    "docs/observability.md",
    "## Admin admission status (HR-07A)",
    """
## Admin admission status (HR-07A)

Authenticated admin runtime status reports the configured read/write/apply budgets and
per-client SSE cap from one captured admin generation, then samples bounded mutable
manager statistics: tracked clients, active SSE totals/clients, clients above the
captured cap, maximum streams held by one client, and rejection counters by closed
class. The process-wide active SSE total is not compared directly with a per-client
cap. No client identity is emitted as a metric label.
""",
)

append_once(
    "CHANGELOG.md",
    "**Hot-reload admin admission policy (#158):**",
    """
- **Hot-reload admin admission policy (#158):** `admin.rate_limit_read_per_min`,
  `admin.rate_limit_write_per_min`, `admin.rate_limit_apply_per_min`, and
  `admin.max_event_conns` now publish live on an existing admin server. Per-client
  token buckets and shared event/log SSE leases survive reload, tighter policies apply
  to the first new admission without a quota reset, existing SSE streams are not
  force-closed when the cap is reduced, and versioned API 429 responses retain the
  `rate_limited`/`Retry-After` contract.
""",
)
