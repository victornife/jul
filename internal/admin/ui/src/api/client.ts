/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { z } from "zod";

// ── Shared reload-result schema ──────────────────────────────────────────────
//
// Mirrors server.ReloadResult. The same shape is returned as `reload` in an
// apply response and as `last_reload` in the runtime overview. Keeping one
// schema avoids drift between the two consumption paths.

export const ReloadSubsystemStatusSchema = z.enum([
  "ok",
  "failed",
  "timed_out",
  "skipped",
  "not_run",
]);
export const ReloadOutcomeSchema = z.enum([
  "applied_live",
  "applied_degraded",
  "not_applied",
  "saved_not_live",
]);

const ReloadSubsystemResultSchema = z.object({
  // Go serializes zero-value subsystem results as {status:""}. Treat that as
  // absent for provisional and pre-Publish results.
  status: z.preprocess(
    (value) => (value === "" ? undefined : value),
    ReloadSubsystemStatusSchema.optional(),
  ),
  duration_ms: z.number().optional(),
  error: z.string().optional(),
});

export const ReloadResultSchema = z.object({
  id: z.string().optional(),
  source: z.union([z.string(), z.number()]).optional(),
  outcome: ReloadOutcomeSchema.optional(),
  desired_version: z.string().optional(),
  serving_version: z.string().optional(),
  started_at: z.string().optional(),
  completed_at: z.string().optional(),
  duration_ms: z.number().optional(),
  persisted: z.boolean().optional(),
  published: z.boolean().optional(),
  timed_out: z.boolean().optional(),
  timed_out_phase: z.string().optional(),
  failed_phase: z.string().optional(),
  phase_durations_ms: z.record(z.number()).optional(),
  http: ReloadSubsystemResultSchema.optional(),
  stream: ReloadSubsystemResultSchema.optional(),
  admin: ReloadSubsystemResultSchema.optional(),
  error: z.string().optional(),
});
export type ReloadResult = z.infer<typeof ReloadResultSchema>;

export const PendingRestartStatusSchema = z.object({
  state: z.enum(["none", "managed_staged", "external_divergence", "inconsistent"]),
  managed: z.boolean(),
  staged: z.boolean(),
  external: z.boolean().optional(),
  staged_at: z.string().optional(),
  staged_version: z.string().optional(),
  persisted_version: z.string().optional(),
  serving_version: z.string().optional(),
  subsystems: z.array(z.string()).optional(),
  discard_available: z.boolean(),
  inconsistent: z.boolean(),
});
export type PendingRestartStatus = z.infer<typeof PendingRestartStatusSchema>;

const ApplyPendingRestartStatusSchema = PendingRestartStatusSchema.extend({
  // Older mutation responses predate the explicit state enum. The dedicated
  // status endpoint and runtime overview remain strict.
  state: PendingRestartStatusSchema.shape.state.optional(),
});

// ── Design-token helpers ─────────────────────────────────────────────────────

/** Central auth-token store (sessionStorage so it clears on tab close). */
export const authToken = {
  get: (): string => sessionStorage.getItem("jul_admin_token") ?? "",
  set: (t: string): void => {
    sessionStorage.setItem("jul_admin_token", t);
  },
  clear: (): void => {
    sessionStorage.removeItem("jul_admin_token");
  },
};

/**
 * Name of the window event dispatched whenever any admin API call returns 401.
 * The AuthGate listens for it to surface a first-class token prompt instead of
 * relying on the ?token= query parameter, which leaks the credential into
 * access logs, history, and the Referer header (P1-8). Centralizing the signal
 * here means every fetch path — the typed api() helper and the raw fetch flows
 * (apply/patch/events/audit-export) — can advertise an auth failure uniformly.
 */
export const UNAUTHORIZED_EVENT = "jul:unauthorized";

/** Broadcasts that the admin API rejected the current token (or its absence). */
export function notifyUnauthorized(): void {
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT));
  }
}

/**
 * FORBIDDEN_EVENT signals a gated action was rejected with 403: the credential
 * is valid but lacks the required permission. The permission layer listens for
 * it to refresh the cached identity so proactive gating recovers after a hot
 * RBAC policy change instead of trusting a stale permission set (N-02).
 */
export const FORBIDDEN_EVENT = "jul:forbidden";

/** Broadcasts that the admin API forbade a gated action (403). */
export function notifyForbidden(): void {
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent(FORBIDDEN_EVENT));
  }
}

// ── Typed fetch client ───────────────────────────────────────────────────────

export class ApiError extends Error {
  constructor(
    public readonly path: string,
    public readonly status: number,
    message: string,
    /** Parsed Retry-After (whole seconds), when the response carried one. */
    public readonly retryAfter?: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/**
 * parseRetryAfter reads a Retry-After header into whole seconds. The value is
 * either a non-negative integer (delta-seconds, what Jul emits) or an HTTP-date;
 * both forms are handled. Returns undefined when absent or unparseable.
 */
function parseRetryAfter(resp: Response): number | undefined {
  const raw = resp.headers.get("Retry-After");
  if (!raw) return undefined;
  const secs = Number(raw);
  if (Number.isFinite(secs) && secs >= 0) return Math.round(secs);
  const when = Date.parse(raw);
  if (!Number.isNaN(when)) {
    const delta = Math.round((when - Date.now()) / 1000);
    return delta > 0 ? delta : 0;
  }
  return undefined;
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const token = authToken.get();
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch(`/api${path}`, { ...init, headers });
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    else if (resp.status === 403) notifyForbidden();
    let msg = `${String(resp.status)} ${resp.statusText}`;
    try {
      const body = (await resp.json()) as { error?: string };
      if (body.error) msg = body.error;
    } catch {
      // ignore parse failure
    }
    throw new ApiError(path, resp.status, msg, parseRetryAfter(resp));
  }
  return resp.json() as Promise<T>;
}

// ── Error taxonomy ───────────────────────────────────────────────────────────

/**
 * Classification of a failed data-load so panels can explain *why* a screen is
 * blank instead of rendering an identical "Failed to load X" for every cause.
 * A 401 (re-auth) and a 503 (feature unavailable) demand very different operator
 * actions; collapsing them into one message hides that.
 */
export type ApiErrorKind =
  | "unauthorized" // 401 — token missing or no longer valid
  | "forbidden" // 403 — authenticated but not permitted
  | "notFound" // 404 — endpoint/feature absent in this build or config
  | "conflict" // 409 — server state changed under an optimistic edit
  | "rateLimited" // 429 — too many requests
  | "server" // 5xx — the server itself failed
  | "network" // fetch rejected: offline, DNS, reset (no HTTP status)
  | "unknown"; // anything else

export interface ApiErrorDescription {
  readonly kind: ApiErrorKind;
  /** Short, human-facing headline (e.g. "Session expired"). */
  readonly title: string;
  /** One-sentence explanation plus the corrective action. */
  readonly message: string;
  /** HTTP status when the failure carried one; undefined for network errors. */
  readonly status?: number;
  /** Whether retrying the same request might plausibly succeed. */
  readonly retryable: boolean;
  /** Suggested wait before retrying, in whole seconds (from Retry-After). */
  readonly retryAfter?: number;
}

/**
 * describeApiError maps a thrown error (an {@link ApiError}, a `fetch` network
 * `TypeError`, or anything else) onto a stable taxonomy with operator-facing
 * copy. `resource` is a lowercase noun phrase that slots into the message as the
 * object being loaded, e.g. "routes", "the configuration", "security info".
 */
export function describeApiError(error: unknown, resource: string): ApiErrorDescription {
  if (error instanceof ApiError) {
    const status = error.status;
    if (status === 401) {
      return {
        kind: "unauthorized",
        status,
        retryable: false,
        title: "Session expired",
        message: `Your admin token is missing or no longer valid. Re-enter it to view ${resource}.`,
      };
    }
    if (status === 403) {
      return {
        kind: "forbidden",
        status,
        retryable: false,
        title: "Access denied",
        message: `This admin token is not permitted to view ${resource}.`,
      };
    }
    if (status === 404) {
      return {
        kind: "notFound",
        status,
        retryable: false,
        title: "Not available",
        message: `Could not find ${resource}. The feature may be disabled in this build or configuration.`,
      };
    }
    if (status === 409) {
      return {
        kind: "conflict",
        status,
        retryable: true,
        title: "Out of date",
        message:
          error.message || `${resource} changed on the server. Reload to see the latest state.`,
      };
    }
    if (status === 429) {
      const wait = error.retryAfter;
      const waitMsg =
        wait !== undefined && wait > 0
          ? `Wait ${String(wait)} second${wait === 1 ? "" : "s"}, then retry.`
          : "Wait a moment, then retry.";
      return {
        kind: "rateLimited",
        status,
        retryable: true,
        title: "Too many requests",
        message: `The console is being rate-limited. ${waitMsg}`,
        ...(wait !== undefined ? { retryAfter: wait } : {}),
      };
    }
    if (status >= 500) {
      return {
        kind: "server",
        status,
        retryable: true,
        title: "Server error",
        message: error.message
          ? `The server failed while loading ${resource}: ${error.message}`
          : `The server failed while loading ${resource}.`,
      };
    }
    return {
      kind: "unknown",
      status,
      retryable: true,
      title: "Request failed",
      message: error.message || `Could not load ${resource}.`,
    };
  }
  // fetch() rejects with a TypeError when the request never reached the server
  // (offline, DNS failure, connection reset, blocked by CORS). There is no HTTP
  // status to inspect, so this is the one case we infer from the error type.
  if (error instanceof TypeError) {
    return {
      kind: "network",
      retryable: true,
      title: "Can't reach the server",
      message: `The console couldn't reach Jul. Check that the server is running and your connection is stable, then retry.`,
    };
  }
  return {
    kind: "unknown",
    retryable: true,
    title: "Something went wrong",
    message:
      error instanceof Error && error.message ? error.message : `Could not load ${resource}.`,
  };
}

// ── Schemas & types ──────────────────────────────────────────────────────────

export const FeatureStatusSchema = z.object({
  group: z.string(),
  name: z.string(),
  active: z.boolean(),
  detail: z.string().optional(),
});
export type FeatureStatus = z.infer<typeof FeatureStatusSchema>;

export const StatsSnapshotSchema = z.object({
  available: z.boolean().optional(),
  uptimeSeconds: z.number(),
  requestsTotal: z.number(),
  requestsPerSec: z.number(),
  inFlight: z.number(),
  connections: z.number(),
  errorRate: z.number(),
  statusClasses: z.record(z.string(), z.number()).optional(),
  latencyAvgMs: z.number(),
  latencyP50Ms: z.number(),
  latencyP95Ms: z.number(),
  latencyP99Ms: z.number(),
  cacheHitRatio: z.number(),
  cacheEvents: z.record(z.string(), z.number()).optional(),
  methods: z.record(z.string(), z.number()).optional(),
  rateLimited: z.record(z.string(), z.number()).optional(),
});
export type StatsSnapshot = z.infer<typeof StatsSnapshotSchema>;

export const TrafficSourcesSchema = z.object({
  hosts: z.record(z.string(), z.number()).optional(),
  origins: z.record(z.string(), z.number()).optional(),
  referers: z.record(z.string(), z.number()).optional(),
  preflight_count: z.number().optional(),
  same_origin: z.number().optional(),
  cross_origin: z.number().optional(),
});
export type TrafficSources = z.infer<typeof TrafficSourcesSchema>;

// AuditSinkStatus mirrors the backend AuditSinkStatus: the health of the durable
// audit trail, surfaced so a broken sink is visible rather than silently dropped.
export const AuditSinkStatusSchema = z.object({
  configured: z.boolean(),
  path: z.string().optional(),
  healthy: z.boolean(),
  error: z.string().optional(),
  write_failures: z.number().optional(),
});
export type AuditSinkStatus = z.infer<typeof AuditSinkStatusSchema>;

export const CertRiskSchema = z.object({
  count: z.number(),
  expiring_soon: z.number(),
  expired: z.number(),
  errors: z.number(),
  details: z.string().optional(),
});
export type CertRisk = z.infer<typeof CertRiskSchema>;

// AdminHealthStatusSchema mirrors admin.AdminHealthStatus: the health of the
// admin subsystem itself (e.g. a failed post-Publish admin reload). Surfaced in
// RuntimeOverview so the console can display a persistent banner when the admin
// subsystem is degraded rather than silently leaving /readyz to catch it (M-05).
export const AdminHealthStatusSchema = z.object({
  healthy: z.boolean(),
  reason: z.string().optional(),
  detail: z.string().optional(),
});
export type AdminHealthStatus = z.infer<typeof AdminHealthStatusSchema>;

// ManagedApplyOutcomeSchema mirrors admin.ManagedApplyOutcome: the terminal
// async result of a managed configuration apply (including any restoration).
// Exposed in RuntimeOverview as last_managed_apply so the console can surface
// the final state of a previously timed-out apply without polling (H-06/M-05).
export const ManagedApplyOutcomeSchema = z.object({
  id: z.string(),
  mode: z.string(),
  ok: z.boolean(),
  outcome: ReloadOutcomeSchema,
  restored: z.boolean().optional(),
  restore_error: z.string().optional(),
  final_disk_version: z.string().optional(),
  final_serving_version: z.string().optional(),
  completed_at: z.string(),
  actor: z.string().optional(),
  source_ip: z.string().optional(),
  // AC-14 finalization provenance, mirrored from the per-ID ledger record onto
  // the overview outcome. Advisory only: a committed apply can be ok=true while
  // history_error/finalization_error is non-empty (its history sidecar
  // degraded). This is NOT a reload failure and must never affect readiness.
  history_snapshot_id: z.string().optional(),
  history_error: z.string().optional(),
  finalization_error: z.string().optional(),
});
export type ManagedApplyOutcome = z.infer<typeof ManagedApplyOutcomeSchema>;

export const OverviewSchema = z.object({
  product: z.string(),
  version: z.string(),
  status: z.array(FeatureStatusSchema),
  stats: StatsSnapshotSchema.optional(),
  traffic_sources: TrafficSourcesSchema.optional(),
  // stream_status is the most recent L4 stream-proxy reload outcome: "ok",
  // "failed: <reason>", or absent when no stream is configured. Stream
  // listeners reload asynchronously after the HTTP swap, so this is the only
  // truthful signal of their state and is surfaced by polling.
  stream_status: z.string().optional(),
  // audit_sink reports durable audit-trail health (P3-08). Present only when a
  // durable audit sink is configured; a degraded sink (open or write failure)
  // is surfaced here so the broken compliance trail is visible, not silent.
  audit_sink: AuditSinkStatusSchema.optional(),
  // cert_risk surfaces real certificate health (counts, expiry, errors) so the
  // Overview "Certificates" card is truthful rather than just reporting TLS
  // configuration presence. Absent when no TLS server blocks are configured.
  cert_risk: CertRiskSchema.optional(),
  // pending_restart lists startup-bound subsystems that have changed on disk
  // since the running process was built (e.g. ["cache", "admin"]). Absent when
  // no restart is needed. The Console surfaces this as a persistent banner so
  // operators know the saved config is not fully live until restart.
  pending_restart: z.array(z.string()).optional(),
  // pending_restart_status is the structured managed planned-restart state
  // (P2-04). Present when a managed staged restart is pending; nil otherwise.
  // Supersedes the flat pending_restart list for the banner and discard action.
  pending_restart_status: z
    .object({
      state: z.enum(["none", "managed_staged", "external_divergence", "inconsistent"]),
      managed: z.boolean(),
      staged: z.boolean(),
      external: z.boolean().optional(),
      staged_at: z.string().optional(),
      staged_version: z.string().optional(),
      serving_version: z.string().optional(),
      subsystems: z.array(z.string()).optional(),
      discard_available: z.boolean(),
      inconsistent: z.boolean(),
    })
    .optional(),
  // last_reload is the correlated result of the most recent hot reload (P2-04).
  // Absent when no reload has run since startup.
  last_reload: ReloadResultSchema.optional(),
  // admin_health reports admin-subsystem degradation (C1/M-05). Present only
  // when the admin subsystem is degraded (e.g. a post-Publish admin reload
  // failure). Absent when the admin subsystem is healthy.
  admin_health: AdminHealthStatusSchema.optional(),
  // last_managed_apply is the terminal outcome of the most recent managed
  // configuration apply, including any async restoration (H-06/M-05). Absent
  // until the first managed apply completes.
  last_managed_apply: ManagedApplyOutcomeSchema.optional(),
});
export type Overview = z.infer<typeof OverviewSchema>;

export const TLSProjectionSchema = z.object({
  enabled: z.boolean(),
  acme: z.boolean(),
  client_auth: z.string().optional(),
  min_version: z.string().optional(),
});
export type TLSProjection = z.infer<typeof TLSProjectionSchema>;

// LocationWAFStateSchema is a location's own [waf] override state, present only
// when the location defines one. As of Phase 4e it carries the full override —
// the basic knobs plus the advanced SecLang fields — so the guided per-location
// editor seeds and round-trips every field; the route supplies the coordinates.
export const LocationWAFStateSchema = z.object({
  enabled: z.boolean(),
  mode: z.string().optional(),
  crs_enabled: z.boolean().default(false),
  block_status: z.number().optional(),
  paranoia: z.number().optional(),
  request_body_limit: z.string().optional(),
  response_body_check: z.boolean().optional(),
  directives_files: z.array(z.string()).optional(),
  inline_rules: z.string().optional(),
});
export type LocationWAFState = z.infer<typeof LocationWAFStateSchema>;

// LocationAuthStateSchema is a location's access-control rule, present only when
// the location defines one. It seeds the guided auth editor and carries no
// secret values — the htpasswd path, JWKS URL, issuer/audience, and CIDR lists
// are identifiers, not credentials.
export const LocationAuthStateSchema = z.object({
  method: z.string(),
  allow: z.array(z.string()).optional(),
  deny: z.array(z.string()).optional(),
  basic_file: z.string().optional(),
  basic_realm: z.string().optional(),
  jwt_jwks_url: z.string().optional(),
  jwt_issuer: z.string().optional(),
  jwt_audience: z.string().optional(),
  forward_url: z.string().optional(),
});
export type LocationAuthState = z.infer<typeof LocationAuthStateSchema>;

export const TranscodeProjectionSchema = z.object({
  descriptor_set: z.string().optional(),
  use_reflection: z.boolean().optional(),
  tls: z.boolean().optional(),
  preserve_proto_field_names: z.boolean().optional(),
  streaming: z.boolean().optional(),
  stream_mode: z.string().optional(),
  max_message_size: z.string().optional(),
});
export type TranscodeProjection = z.infer<typeof TranscodeProjectionSchema>;

export const LocationProjectionSchema = z.object({
  index: z.number().default(0),
  match: z.string(),
  type: z.string(),
  action: z.string(),
  target: z.string().optional(),
  auth: z.boolean(),
  auth_detail: LocationAuthStateSchema.optional(),
  cache: z.boolean(),
  compression: z.boolean().default(false),
  rate_limit: z.boolean().default(false),
  rate_limit_detail: z
    .object({
      enabled: z.boolean(),
      rate: z.number().optional(),
      burst: z.number().optional(),
      key: z.string().optional(),
    })
    .optional(),
  secure: z.boolean(),
  require_client_cert: z.boolean().default(false),
  upstream: z.string().optional(),
  waf: LocationWAFStateSchema.optional(),
  transcode: TranscodeProjectionSchema.optional(),
  warnings: z.array(z.string()).optional(),
});
export type LocationProjection = z.infer<typeof LocationProjectionSchema>;

export const RouteProjectionSchema = z.object({
  name: z.string().optional(),
  listen: z.string(),
  server_names: z.array(z.string()).optional(),
  tls: TLSProjectionSchema.optional(),
  http3: z.boolean(),
  h2c: z.boolean(),
  locations: z.array(LocationProjectionSchema),
});
export type RouteProjection = z.infer<typeof RouteProjectionSchema>;

export const BackendProjectionSchema = z.object({
  address: z.string(),
  weight: z.number(),
  healthy: z.boolean().optional(),
  inflight: z.number().optional(),
});
export type BackendProjection = z.infer<typeof BackendProjectionSchema>;

export const AppProjectionSchema = z.object({
  name: z.string(),
  strategy: z.string(),
  backends: z.array(BackendProjectionSchema),
  health_check: z.boolean(),
  discovery: z.string().optional(),
  max_fails: z.number().optional(),
  fail_timeout: z.string().optional(),
  health_check_type: z.string().optional(),
  health_check_path: z.string().optional(),
  health_check_interval: z.string().optional(),
  discovery_target: z.string().optional(),
  routes_using: z.array(z.string()).optional(),
  warnings: z.array(z.string()).optional(),
  // Guided App-editor seed fields. Non-secret health-check and discovery
  // detail; tokens are never projected (only has_token is exposed).
  health_check_timeout: z.string().optional(),
  health_check_healthy_threshold: z.number().optional(),
  health_check_unhealthy_threshold: z.number().optional(),
  health_check_expect_status: z.array(z.number()).optional(),
  health_check_expect_body: z.string().optional(),
  discovery_refresh: z.string().optional(),
  discovery_consul: z
    .object({
      address: z.string().optional(),
      service: z.string().optional(),
      tag: z.string().optional(),
      datacenter: z.string().optional(),
      passing_only: z.boolean().optional(),
      has_token: z.boolean().optional(),
    })
    .optional(),
  discovery_kubernetes: z
    .object({
      namespace: z.string().optional(),
      service: z.string().optional(),
      port: z.string().optional(),
      api_server: z.string().optional(),
      ca_file: z.string().optional(),
      insecure_skip_tls_verify: z.boolean().optional(),
      has_token: z.boolean().optional(),
    })
    .optional(),
});
export type AppProjection = z.infer<typeof AppProjectionSchema>;

export const CertProjectionSchema = z.object({
  server_names: z.array(z.string()),
  source: z.string(),
  issuer: z.string().optional(),
  not_after: z.string().optional(),
  days_left: z.number().optional(),
  error: z.string().optional(),
});
export type CertProjection = z.infer<typeof CertProjectionSchema>;

// LocationWAFSchema is one per-location [waf] override surfaced by the security
// projection. The identity fields mirror the structured-patch location selector
// so a guided editor can target the exact block; the policy fields carry the
// full override (Phase 4e) so the editor seeds and round-trips every field.
export const LocationWAFSchema = z.object({
  listen: z.string(),
  server_names: z.array(z.string()).optional(),
  match_type: z.string().optional(),
  path: z.string().optional(),
  enabled: z.boolean(),
  mode: z.string().optional(),
  crs_enabled: z.boolean().optional().default(false),
  block_status: z.number().optional(),
  paranoia: z.number().optional(),
  request_body_limit: z.string().optional(),
  response_body_check: z.boolean().optional(),
  directives_files: z.array(z.string()).optional(),
  inline_rules: z.string().optional(),
});
export type LocationWAF = z.infer<typeof LocationWAFSchema>;

// RBACStatusSchema mirrors admin.RBACStatusProjection: the secret-free summary
// of the admin RBAC posture. It carries only counts and booleans — never a
// token, token ID, or hash.
export const RBACStatusSchema = z.object({
  enabled: z.boolean(),
  principal_count: z.number(),
  role_count: z.number(),
  legacy_token_active: z.boolean(),
  // Secret-free posture digest, used to detect serving-vs-persisted drift.
  generation: z.string().optional().default(""),
});
export type RBACStatus = z.infer<typeof RBACStatusSchema>;

// RBACPostureSchema mirrors admin.RBACPostureProjection: the serving (installed,
// actively-enforced) vs persisted (on-disk) RBAC posture, with pending set when
// a staged change is not yet live (N-03).
export const RBACPostureSchema = z.object({
  serving: RBACStatusSchema,
  persisted: RBACStatusSchema,
  pending: z.boolean(),
});
export type RBACPosture = z.infer<typeof RBACPostureSchema>;

// EgressBlockedCountSchema mirrors admin.EgressBlockedCount: a secret-free
// egress block tally by subsystem and reason. No destination host or IP.
export const EgressBlockedCountSchema = z.object({
  subsystem: z.string(),
  reason: z.string(),
  count: z.number(),
});
export type EgressBlockedCount = z.infer<typeof EgressBlockedCountSchema>;

// EgressProjectionSchema mirrors admin.EgressProjection: the outbound egress
// allow-list posture. Counts and a bounded block breakdown only — never a
// destination.
export const EgressProjectionSchema = z.object({
  enabled: z.boolean(),
  allow_rule_count: z.number(),
  recent_blocked: z.array(EgressBlockedCountSchema).optional(),
});
export type EgressProjection = z.infer<typeof EgressProjectionSchema>;

export const SecurityProjectionSchema = z.object({
  auth_enabled: z.boolean(),
  client_auth: z.string().optional(),
  body_limit: z.string().optional(),
  require_cert_count: z.number(),
  waf_enabled: z.boolean(),
  // waf_compiled reports whether this binary includes the WAF engine (the waf
  // build tag). When false the apply preflight rejects an enabled WAF, so the
  // panel warns up front. Defaults to true so an omitted field never shows a
  // spurious "not compiled" banner.
  waf_compiled: z.boolean().optional().default(true),
  waf_mode: z.string().optional(),
  waf_locations: z.number(),
  // Per-mode distribution of protected locations, so the panel shows the real
  // block/detect/CRS mix rather than implying one global mode.
  waf_block_locs: z.number().optional().default(0),
  waf_detect_locs: z.number().optional().default(0),
  waf_crs_locs: z.number().optional().default(0),
  // The global [waf] policy, so the guided editor seeds from the real config
  // instead of clobbering unshown fields on save.
  waf_global_enabled: z.boolean().optional().default(false),
  waf_global_mode: z.string().optional(),
  waf_block_status: z.number().optional(),
  waf_crs_enabled: z.boolean().optional().default(false),
  waf_paranoia: z.number().optional(),
  waf_request_body_limit: z.string().optional(),
  waf_response_body_check: z.boolean().optional().default(false),
  waf_directives_files: z.array(z.string()).optional(),
  waf_inline_rules: z.string().optional(),
  // Per-location [waf] overrides, each replacing the global policy for that
  // route. Surfacing them lets the panel disclose per-location WAF rather than
  // implying the single global policy governs every route.
  location_wafs: z.array(LocationWAFSchema).optional(),
  secret_refs: z.number(),
  // rbac summarises the admin access-control posture as serving vs persisted.
  // Optional for forward compatibility with older servers that predate it.
  rbac: RBACPostureSchema.optional(),
  // egress is the outbound egress allow-list posture (P4-01). Optional for
  // forward compatibility with servers that predate it.
  egress: EgressProjectionSchema.optional(),
});
export type SecurityProjection = z.infer<typeof SecurityProjectionSchema>;

export const LifecycleFieldProjectionSchema = z.object({
  class: z.string(),
  subsystem: z.string(),
  reason: z.string(),
  conditional: z.boolean().optional().default(false),
});
export type LifecycleFieldProjection = z.infer<typeof LifecycleFieldProjectionSchema>;

export const GlobalSettingsProjectionSchema = z.object({
  worker_threads: z.string().default("auto"),
  log_level: z.enum(["debug", "info", "warn", "error"]).default("info"),
  log_format: z.enum(["text", "json"]).default("text"),
  shutdown_timeout: z.string().default("0s"),
  reload_timeout: z.string().default("0s"),
  redact_min_secret_length: z.number().int().nonnegative().default(0),
  lifecycle: z.record(z.string(), LifecycleFieldProjectionSchema).default({}),
});
export type GlobalSettingsProjection = z.infer<typeof GlobalSettingsProjectionSchema>;

export const CompressionProjectionSchema = z.object({
  enabled: z.boolean(),
  encoders: z.array(z.string()).default([]),
  level: z.number().int().default(0),
  min_size: z.string().default(""),
  types: z.array(z.string()).default([]),
  precompressed: z.boolean().default(false),
});
export type CompressionProjection = z.infer<typeof CompressionProjectionSchema>;

// Location rate-limit projection. Deliberately does not expose global-only
// max_conns semantics.
export const RateLimitProjectionSchema = z.object({
  enabled: z.boolean(),
  rate: z.number().optional(),
  burst: z.number().optional(),
  key: z.string().optional(),
});
export type RateLimitPatch = z.infer<typeof RateLimitProjectionSchema>;

export const GlobalRateLimitProjectionSchema = z.object({
  enabled: z.boolean(),
  rate: z.number().default(0),
  burst: z.number().default(0),
  key: z.string().default(""),
  max_conns: z.number().int().nonnegative().default(0),
});
export type GlobalRateLimitProjection = z.infer<typeof GlobalRateLimitProjectionSchema>;

export const CacheProjectionSchema = z.object({
  enabled: z.boolean(),
  memory_max_size: z.string().default(""),
  // One-release compatibility alias for older console consumers.
  memory_max: z.string().optional(),
  disk_path: z.string().default(""),
  disk_max_size: z.string().default(""),
  default_ttl: z.string().default(""),
  stale_while_revalidate: z.string().default(""),
  stale_if_error: z.string().default(""),
});
export type CacheProjection = z.infer<typeof CacheProjectionSchema>;

export const ServerLimitsProjectionSchema = z.object({
  listen: z.string(),
  server_names: z.array(z.string()).optional(),
  client_max_body_size: z.string().default(""),
  read_timeout: z.string().default(""),
  write_timeout: z.string().default(""),
  idle_timeout: z.string().default(""),
});
export type ServerLimitsProjection = z.infer<typeof ServerLimitsProjectionSchema>;

export const TracingProjectionSchema = z.object({
  enabled: z.boolean(),
  exporter: z.string().optional(),
  endpoint: z.string().optional(),
  sample_ratio: z.number().optional(),
  service_name: z.string().optional(),
  insecure: z.boolean().optional(),
});

export const AccessLogProjectionSchema = z.object({
  enabled: z.boolean(),
  sinks: z.array(z.string()).optional(),
  file: z.string().optional(),
  format: z.string().optional(),
  rotate_max_mb: z.number().optional(),
  rotate_keep: z.number().optional(),
});

export const TrafficControlsSchema = z.object({
  global: GlobalSettingsProjectionSchema.optional(),
  compression: CompressionProjectionSchema.optional(),
  rate_limit: GlobalRateLimitProjectionSchema.optional(),
  cache: CacheProjectionSchema.optional(),
  servers: z.array(ServerLimitsProjectionSchema).default([]),
  tracing: TracingProjectionSchema.optional(),
  access_log: AccessLogProjectionSchema.optional(),
});
export type TrafficControls = Partial<z.output<typeof TrafficControlsSchema>>;

// PluginAttachmentSchema is one place a plugin is referenced — a location
// middleware chain, a location handler action, or a server-level chain. The
// identity fields mirror the structured-patch location selector so the guided
// attach/detach editor can target the exact location.
export const PluginAttachmentSchema = z.object({
  scope: z.string(), // "location" | "server"
  role: z.string(), // "middleware" | "handler"
  listen: z.string(),
  server_names: z.array(z.string()).optional(),
  match_type: z.string().optional(),
  path: z.string().optional(),
});
export type PluginAttachment = z.infer<typeof PluginAttachmentSchema>;

// PluginProjectionSchema is one declared [plugins.NAME] for the Plugins panel.
// The inline module bytes are never projected — only the source kind ("path" or
// "inline"). attachments lists where the plugin is used, so the panel can show
// usage and guard removal.
export const PluginProjectionSchema = z.object({
  name: z.string(),
  source: z.string(), // "path" | "inline"
  path: z.string().optional(),
  type: z.string(), // "middleware" | "handler"
  config: z.record(z.string(), z.string()).optional(),
  memory_limit: z.string().optional(),
  timeout: z.string().optional(),
  kv: z.boolean(),
  fetch: z.boolean(),
  allowed_hosts: z.array(z.string()).optional(),
  attachments: z.array(PluginAttachmentSchema).optional(),
});
export type PluginProjection = z.infer<typeof PluginProjectionSchema>;

export const PluginsProjectionSchema = z.object({
  // compiled reports whether this binary includes the WASM plugin runtime (the
  // wasmplugins build tag). When false, declarations still validate but the
  // apply preflight rejects them, so the panel warns up front.
  compiled: z.boolean(),
  upload_enabled: z.boolean(),
  upload_max_size_mb: z.number().int().nonnegative(),
  plugins: z.array(PluginProjectionSchema),
});
export type PluginsProjection = z.infer<typeof PluginsProjectionSchema>;

// StreamProjectionSchema is one declared [[stream]] L4 (TCP/UDP) listener for
// the Streams panel and its guided editor. The protocol is normalized to its
// effective value (tcp default) so the editor seeds a faithful round-trip.
export const StreamProjectionSchema = z.object({
  listen: z.string(),
  protocol: z.string(), // "tcp" | "udp"
  proxy_pass: z.string().optional(),
  sni_routes: z.record(z.string(), z.string()).optional(),
  tls_passthrough: z.boolean(),
  proxy_protocol: z.string().optional(), // "" | "in" | "out" | "both"
  connect_timeout: z.string().optional(),
  idle_timeout: z.string().optional(),
});
export type StreamProjection = z.infer<typeof StreamProjectionSchema>;

export const StreamsProjectionSchema = z.object({
  // compiled reports whether this binary includes the L4 stream proxy (the
  // stream build tag). When false, declarations still validate but a lean
  // binary refuses to start with them, so the panel warns up front.
  compiled: z.boolean(),
  streams: z.array(StreamProjectionSchema),
});
export type StreamsProjection = z.infer<typeof StreamsProjectionSchema>;

// MTLSLocationProjectionSchema is one location's per-route client-certificate
// requirement, addressed by its match type + path. Unlike the server-level
// client_auth, require_client_cert hot-reloads (it is enforced per request).
export const MTLSLocationProjectionSchema = z.object({
  match: z.string(),
  type: z.string(),
  require_client_cert: z.boolean().default(false),
});
export type MTLSLocationProjection = z.infer<typeof MTLSLocationProjectionSchema>;

// MTLSServerProjectionSchema is one TLS-enabled server's mutual-TLS posture.
// mode is normalized to "none" when client_auth is absent or inactive, so the
// editor seeds a faithful round-trip and can enable it on a server that lacks
// it. ca_file/crl_file are paths (not secrets); verify_san is the SAN allow-list.
export const MTLSServerProjectionSchema = z.object({
  listen: z.string(),
  server_names: z.array(z.string()).optional(),
  mode: z.string(),
  ca_file: z.string().optional(),
  crl_file: z.string().optional(),
  verify_san: z.array(z.string()).optional(),
  locations: z.array(MTLSLocationProjectionSchema),
});
export type MTLSServerProjection = z.infer<typeof MTLSServerProjectionSchema>;

export const MTLSProjectionSchema = z.object({
  servers: z.array(MTLSServerProjectionSchema),
});
export type MTLSProjection = z.infer<typeof MTLSProjectionSchema>;

// ── Query functions ──────────────────────────────────────────────────────────

export function fetchOverview(): Promise<Overview> {
  return api<unknown>("/runtime/overview").then((d) => OverviewSchema.parse(d));
}

export function fetchRoutes(): Promise<RouteProjection[]> {
  return api<unknown>("/routes").then((d) => z.array(RouteProjectionSchema).parse(d));
}

// ── Route testing (Milestone 2.3) ────────────────────────────────────────────

export interface RouteTestInput {
  method?: string;
  path: string;
  host?: string;
  headers?: Record<string, string>;
}

export const RouteTestResultSchema = z.object({
  matched: z.boolean(),
  listen: z.string().optional(),
  server_names: z.array(z.string()).optional(),
  match: z.string().optional(),
  match_type: z.string().optional(),
  action: z.string().optional(),
  target: z.string().optional(),
  upstream: z.string().optional(),
  auth: z.boolean(),
  cache: z.boolean(),
  compression: z.boolean(),
  rate_limit: z.boolean(),
  secure: z.boolean(),
  warnings: z.array(z.string()).optional(),
  explanation: z.string(),
});
export type RouteTestResult = z.infer<typeof RouteTestResultSchema>;

export async function testRoute(input: RouteTestInput): Promise<RouteTestResult> {
  const data = await api<unknown>("/routes/test", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  return RouteTestResultSchema.parse(data);
}

export function fetchApps(): Promise<AppProjection[]> {
  return api<unknown>("/apps").then((d) => z.array(AppProjectionSchema).parse(d));
}

export function fetchTLS(): Promise<CertProjection[]> {
  return api<unknown>("/tls").then((d) => z.array(CertProjectionSchema).parse(d));
}

export function fetchSecurity(): Promise<SecurityProjection> {
  return api<unknown>("/security").then((d) => SecurityProjectionSchema.parse(d));
}

// ── Current identity (GET /api/admin/me) ─────────────────────────────────────
//
// IdentitySchema mirrors admin.identityResponse: the secret-free view of the
// authenticated caller used to display the current principal/role and gate
// controls proactively. `permissions` is the resolved concrete permission set
// (wildcards already expanded server-side), so the Console gates with a simple
// set-membership test. It never carries the raw token or its digest.
export const IdentitySchema = z.object({
  principal: z.string(),
  role: z.string(),
  token_id: z.string().optional().default(""),
  permissions: z.array(z.string()),
  legacy: z.boolean(),
});
export type Identity = z.infer<typeof IdentitySchema>;

export function fetchMe(): Promise<Identity> {
  return api<unknown>("/admin/me").then((d) => IdentitySchema.parse(d));
}

export function fetchTrafficControls(): Promise<TrafficControls> {
  return api<unknown>("/traffic-controls").then((d) => TrafficControlsSchema.parse(d));
}

export function fetchPlugins(): Promise<PluginsProjection> {
  return api<unknown>("/plugins").then((d) => PluginsProjectionSchema.parse(d));
}

export function fetchStreams(): Promise<StreamsProjection> {
  return api<unknown>("/streams").then((d) => StreamsProjectionSchema.parse(d));
}

export function fetchMTLS(): Promise<MTLSProjection> {
  return api<unknown>("/mtls").then((d) => MTLSProjectionSchema.parse(d));
}

/** Fetches the runtime stats snapshot directly (used by traffic-control editors). */
export function fetchStats(): Promise<StatsSnapshot> {
  return api<unknown>("/stats").then((d) => StatsSnapshotSchema.parse(d));
}

/**
 * Purges the response cache (Milestone 3.2). With no key the whole cache is
 * cleared; a key removes a single entry. The admin endpoint lives at the server
 * root (/cache/purge), not under /api, so this bypasses the /api-prefixed
 * client helper.
 */
export async function purgeCache(key?: string): Promise<void> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const url = key ? `/cache/purge?key=${encodeURIComponent(key)}` : "/cache/purge";
  const resp = await fetch(url, { method: "POST", headers });
  if (!resp.ok) {
    throw new ApiError("/cache/purge", resp.status, `${String(resp.status)} ${resp.statusText}`);
  }
}

// ── History ──────────────────────────────────────────────────────────────────

export const HistoryEntrySchema = z.object({
  id: z.string(),
  time: z.string(),
  size: z.number(),
  // Optional, redacted provenance projected from the snapshot's metadata
  // sidecar (AC-05). Older raw-only snapshots omit every field; a malformed
  // sidecar surfaces as metadata_error on that single entry.
  apply_id: z.string().optional(),
  operation: z.string().optional(),
  mode: z.string().optional(),
  outcome: z.string().optional(),
  actor: z.string().optional(),
  reason: z.string().optional(),
  previous_version: z.string().optional(),
  candidate_version: z.string().optional(),
  metadata_error: z.string().optional(),
});
export type HistoryEntry = z.infer<typeof HistoryEntrySchema>;

export const HistorySnapshotSchema = z.object({
  id: z.string(),
  raw: z.string(),
});
export type HistorySnapshot = z.infer<typeof HistorySnapshotSchema>;

export function fetchHistory(): Promise<HistoryEntry[]> {
  return api<unknown>("/config/history").then((d) => z.array(HistoryEntrySchema).parse(d));
}

export function fetchHistorySnapshot(id: string): Promise<HistorySnapshot> {
  return api<unknown>(`/config/history/${encodeURIComponent(id)}`).then((d) =>
    HistorySnapshotSchema.parse(d),
  );
}

export async function rollback(
  id: string,
  confirmAdmin = false,
  baseVersion?: string,
): Promise<ApplyResult> {
  const headers = new Headers({ "Content-Type": "application/json", Accept: "application/json" });
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const query = confirmAdmin ? "?confirm_admin=true" : "";
  // base_version binds the rollback to the exact configuration the operator
  // reviewed in the preview; the server rejects it with 409 if the persisted
  // config changed since, so a concurrent edit is never silently reverted.
  const resp = await fetch(`/api/config/rollback${query}`, {
    method: "POST",
    headers,
    body: JSON.stringify(baseVersion ? { id, base_version: baseVersion } : { id }),
  });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    classifyApplyFailure("/config/rollback", resp.status, data);
  }
  return parseConfigMutationResult(data);
}

// ── Planned restart (P2-04) ──────────────────────────────────────────────────

/**
 * PendingRestartStatusSchema mirrors admin.PendingRestartStatus: the structured
 * state of a managed or external staged restart. Managed restarts have
 * discard_available=true and carry version/subsystem metadata; external ones
 * have managed=false and discard_available=false.
 */
export const PendingRestartResponseSchema = z.object({
  pending: z.boolean(),
  status: PendingRestartStatusSchema.optional(),
});
export type PendingRestartResponse = z.infer<typeof PendingRestartResponseSchema>;

/**
 * Fetches the current managed planned-restart status. Returns {pending: false}
 * when no staged restart exists (never 404).
 */
export function fetchPendingRestart(): Promise<PendingRestartResponse> {
  return api<unknown>("/config/pending-restart").then((d) => PendingRestartResponseSchema.parse(d));
}

/**
 * Discards the managed staged restart and atomically restores the previous
 * configuration. Rejects with ApiError (409) when the discard is unsafe
 * (inconsistent state, disk digest mismatch, or serving version changed).
 */
export async function discardPendingRestart(): Promise<ApplyResult> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch("/api/config/pending-restart/discard", { method: "POST", headers });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    let msg = `${String(resp.status)} ${resp.statusText}`;
    const body = data as { error?: string; message?: string } | null;
    if (body?.message) msg = body.message;
    else if (body?.error) msg = body.error;
    throw new ApiError("/config/pending-restart/discard", resp.status, msg);
  }
  return ApplyResultSchema.parse(data);
}

// ── Raw config ───────────────────────────────────────────────────────────────

export const RawConfigSchema = z.object({
  raw: z.string().optional(),
  product: z.string().optional(),
  version: z.string().optional(),
  path: z.string().optional(),
  // base_version is the optimistic-concurrency fingerprint of the config at read
  // time. The raw editor sends it back on apply so a stale edit is rejected with
  // 409 rather than silently clobbering a concurrent change.
  base_version: z.string().optional(),
});
export type RawConfig = z.infer<typeof RawConfigSchema>;

export function fetchRawConfig(): Promise<RawConfig> {
  return api<unknown>("/config").then((d) => RawConfigSchema.parse(d));
}

export const DiffEntrySchema = z.object({
  kind: z.string(),
  name: z.string().optional(),
  before: z.string().optional(),
  after: z.string().optional(),
  detail: z.string().optional(),
});

export const ConfigDiffSchema = z.object({
  summary: z.string(),
  affected: z.array(z.string()).optional(),
  warnings: z.array(z.string()).optional(),
  additions: z.array(DiffEntrySchema).optional(),
  removals: z.array(DiffEntrySchema).optional(),
  modifications: z.array(DiffEntrySchema).optional(),
});
export type ConfigDiff = z.infer<typeof ConfigDiffSchema>;

export async function diffConfig(candidate: string): Promise<ConfigDiff> {
  const data = await api<unknown>("/config/diff", {
    method: "POST",
    headers: { "Content-Type": "application/toml" },
    body: candidate,
  });
  return ConfigDiffSchema.parse(data);
}

// The rollback-preview endpoint returns the diff fields at the top level plus
// base_version — the canonical configuration the preview was computed against.
// The Console retains base_version and submits it with the rollback so the
// server can reject a rollback whose reviewed baseline no longer matches the
// persisted config (optimistic concurrency; Net-new issue 1).
export const HistoryRollbackDiffSchema = ConfigDiffSchema.extend({
  base_version: z.string().optional(),
});
export type HistoryRollbackDiff = z.infer<typeof HistoryRollbackDiffSchema>;

/**
 * diffHistorySnapshot previews what rolling back to a stored snapshot would
 * change. The server reads the snapshot itself and diffs it against the
 * persisted config, so a least-privilege rollback-only role (history:rollback)
 * can obtain the preview without config:write and without submitting candidate
 * TOML (N-02). The response carries base_version so the caller can bind the
 * subsequent rollback to the exact configuration reviewed here (Net-new issue 1).
 */
export async function diffHistorySnapshot(id: string): Promise<HistoryRollbackDiff> {
  const data = await api<unknown>(`/config/history/${encodeURIComponent(id)}/diff`);
  return HistoryRollbackDiffSchema.parse(data);
}

// ── Structured config patch (Wave B) ─────────────────────────────────────────

/**
 * A single structured edit to the running configuration. Each op targets an
 * existing object (a route by listen + server_names + match type + path, or an
 * upstream by name) and the server applies it to the PARSED config model,
 * returning the full diff for review — it does not persist and withholds the
 * candidate TOML (fetchPatchCandidate exposes it under config:raw). The caller
 * then applies the ops atomically through applyPatchBatch.
 */
export interface ServerLimitsPatch {
  client_max_body_size?: string;
  read_header_timeout?: string;
  read_timeout?: string;
  write_timeout?: string;
  idle_timeout?: string;
  max_header_bytes?: string;
}

// Route ops address a location by the full coordinates the route projection
// exposes — listen, the server's server_names set, and the location's match
// type + path — so a patch can never land on the wrong virtual host (shared
// listen) or the wrong location (path reused under a different match type).
export type RouteTarget = {
  listen: string;
  server_names: string[];
  match_type: string;
  path: string;
};

// LocationWAFPatch is the per-location [waf] override the guided editor sets. As
// of Phase 4e it carries the full override — the basic knobs plus the advanced
// SecLang fields (block status, paranoia, request-body limit, response-body
// inspection, rule files, inline rules). The editor seeds every field from the
// projection first, so location_waf_set replaces the override faithfully rather
// than clobbering unshown rules.
export type LocationWAFPatch = {
  enabled: boolean;
  mode?: "block" | "detect";
  crs_enabled?: boolean;
  block_status?: number;
  paranoia?: number;
  request_body_limit?: string;
  response_body_check?: boolean;
  directives_files?: string[];
  inline_rules?: string;
};

// LocationAuthPatch is the per-location access-control rule the guided auth
// editor sets. Exactly one method is chosen; the backend builds a fresh
// AuthConfig from the method's fields and replaces the location's auth
// wholesale. No secret values are sent — only identifiers the projection already
// discloses.
export type LocationAuthPatch = {
  method: "cidr" | "basic" | "jwt" | "forward";
  allow?: string[];
  deny?: string[];
  basic_file?: string;
  basic_realm?: string;
  jwt_jwks_url?: string;
  jwt_issuer?: string;
  jwt_audience?: string;
  forward_url?: string;
};

// LocationMatchPatch is the new match (type + path) for location_set_match. It
// renames the route's matching pattern in place. An empty/omitted type defaults
// to "prefix"; changing the match changes the route's identity, so the diff
// lists the old route removed and the renamed route added.
export type LocationMatchPatch = {
  type?: "exact" | "prefix" | "regex";
  path: string;
};

// LocationActionPatch is the new action for location_set_action. The backend
// clears every other action field first, so exactly one action remains. Only
// the tag-free actions the console edits structurally are offered; richer
// actions (gRPC, transcode, FastCGI/uWSGI, handler plugin) stay read-only.
export type LocationActionPatch =
  | { kind: "proxy"; target: string }
  | { kind: "grpc_proxy"; target: string }
  | { kind: "static"; target: string }
  | { kind: "redirect"; target: string; status?: number }
  | { kind: "return"; status: number }
  | { kind: "deny" };

// TranscodePatch is the location_set_transcode payload — the quick-edit knobs
// for a grpc_transcode route. The backend replaces the location's
// GRPCTranscode block wholesale (and clears any conflicting action fields).
export type TranscodePatch = {
  target: string;
  descriptor_path?: string;
  use_reflection?: boolean;
  tls?: boolean;
  preserve_names?: boolean;
  streaming?: boolean;
  stream_mode?: "ndjson" | "sse";
  max_message_size?: string;
};

// PluginDefPatch is the plugin_set payload — the guided editor's view of a
// single [plugins.NAME] declaration. The module is identified by source ("path"
// sets a new file; "inline" keeps an existing inline plugin's bytes, which the
// console never transmits), never by raw WASM bytes. Durations/sizes are strings
// (e.g. "16m", "100ms"); the validated apply re-parse enforces the rest (the
// path exists, fetch needs allowed_hosts).
export type PluginDefPatch = {
  source: "path" | "inline";
  path?: string;
  type?: "middleware" | "handler";
  config?: Record<string, string>;
  memory_limit?: string;
  timeout?: string;
  kv?: boolean;
  fetch?: boolean;
  allowed_hosts?: string[];
};

// StreamDefPatch is the stream_add / stream_set payload — the guided editor's
// view of a single [[stream]] L4 listener. Durations are strings (e.g. "10s");
// the validated apply re-parse enforces the rest (the target resolves to a
// known upstream or host:port, no duplicate listener, the TCP-only constraints).
export type StreamDefPatch = {
  listen: string;
  protocol?: "tcp" | "udp";
  proxy_pass?: string;
  sni_routes?: Record<string, string>;
  tls_passthrough?: boolean;
  proxy_protocol?: "in" | "out" | "both";
  connect_timeout?: string;
  idle_timeout?: string;
};

// ClientAuthPatch is the server_set_client_auth payload — the guided mTLS
// editor's view of a server block's mutual-TLS (client-certificate) settings. A
// "none" mode disables it. ca_file/crl_file are paths (not secrets); the
// validated apply re-parse enforces that they are readable PEM/DER files and
// that request/require modes carry a ca_file. Server-level changes take effect
// when the listener binds (on restart), so the editor surfaces that caveat.
export type ClientAuthPatch = {
  mode: "none" | "request" | "require";
  ca_file?: string;
  crl_file?: string;
  verify_san?: string[];
};

// GlobalPatch is the sparse, non-secret global_set payload. Omitted properties
// preserve the existing value; intentional false/zero/empty values remain on
// the wire because the operation is discriminated by op.
export type GlobalPatch = {
  worker_threads?: string;
  log_level?: "debug" | "info" | "warn" | "error";
  log_format?: "text" | "json";
  shutdown_timeout?: string;
  reload_timeout?: string;
  redact_min_secret_length?: number;
};

export type CompressionPatch = {
  enabled?: boolean;
  encoders?: string[];
  level?: number;
  min_size?: string;
  types?: string[];
  precompressed?: boolean;
};

export type GlobalRateLimitPatch = {
  enabled?: boolean;
  key?: string;
  rate?: number;
  burst?: number;
  max_conns?: number;
};

export type ConfigPatch =
  | ({ op: "route_set_target"; target: string } & RouteTarget)
  | ({ op: "route_toggle_cache"; enabled: boolean } & RouteTarget)
  | ({ op: "route_toggle_rate_limit"; enabled: boolean } & RouteTarget)
  | ({ op: "route_set_rate_limit"; rate_limit: RateLimitPatch } & RouteTarget)
  | ({ op: "location_waf_set"; waf: LocationWAFPatch } & RouteTarget)
  | ({ op: "location_waf_clear" } & RouteTarget)
  | ({ op: "location_set_auth"; auth: LocationAuthPatch } & RouteTarget)
  | ({ op: "location_clear_auth" } & RouteTarget)
  | ({ op: "location_set_match"; match_set: LocationMatchPatch } & RouteTarget)
  | ({ op: "location_set_action"; action: LocationActionPatch } & RouteTarget)
  | ({ op: "location_set_transcode"; transcode: TranscodePatch } & RouteTarget)
  | ({ op: "location_attach_plugin"; plugin_name: string } & RouteTarget)
  | ({ op: "location_detach_plugin"; plugin_name: string } & RouteTarget)
  | { op: "plugin_set"; plugin_name: string; plugin: PluginDefPatch }
  | { op: "plugin_remove"; plugin_name: string }
  | { op: "stream_add"; stream: StreamDefPatch }
  | { op: "stream_set"; listen: string; stream_protocol?: string; stream: StreamDefPatch }
  | { op: "stream_remove"; listen: string; stream_protocol?: string }
  | { op: "route_rename"; listen: string; server_names: string[]; new_server_names: string[] }
  | { op: "upstream_add_backend"; upstream: string; address: string; weight?: number }
  | { op: "upstream_remove_backend"; upstream: string; address: string }
  | { op: "upstream_set_strategy"; upstream: string; strategy: string }
  | { op: "upstream_set_health_check"; upstream: string; health_check: HealthCheckPatch }
  | { op: "upstream_set_discovery"; upstream: string; discovery: DiscoveryPatch }
  | { op: "server_add"; listen: string; server_names?: string[] }
  | { op: "server_remove"; listen: string; server_names: string[] }
  | { op: "server_set_limits"; listen: string; limits: ServerLimitsPatch }
  | { op: "server_toggle_http3"; listen: string; enabled: boolean }
  | { op: "server_toggle_h2c"; listen: string; enabled: boolean }
  | {
      op: "server_set_client_auth";
      listen: string;
      server_names: string[];
      client_auth: ClientAuthPatch;
    }
  | {
      op: "location_add";
      listen: string;
      server_names?: string[];
      match_set: LocationMatchPatch;
      action: LocationActionPatch;
    }
  | {
      op: "location_remove";
      listen: string;
      server_names?: string[];
      match_type: string;
      path: string;
    }
  | { op: "upstream_add"; upstream: string; address: string; weight?: number; strategy?: string }
  | { op: "upstream_remove"; upstream: string }
  | { op: "global_set"; global: GlobalPatch }
  | { op: "compression_set"; compression: CompressionPatch }
  | { op: "rate_limit_global_set"; rate_limit: GlobalRateLimitPatch }
  | ({ op: "location_toggle_require_client_cert"; enabled: boolean } & RouteTarget);

// HealthCheckPatch is the upstream active health-check block the guided Apps
// editor sets. Durations are strings (e.g. "5s"); empty/zero fields fall back to
// the backend defaults (interval 5s, timeout 2s, thresholds 2/3, expect [200]).
// A disabled payload removes the block (passive health only).
export type HealthCheckPatch = {
  enabled: boolean;
  type?: "http" | "tcp";
  path?: string;
  interval?: string;
  timeout?: string;
  healthy_threshold?: number;
  unhealthy_threshold?: number;
  expect_status?: number[];
  expect_body?: string;
};

// DiscoveryPatch is the upstream dynamic-discovery block the guided Apps editor
// sets. Type "static" removes discovery (the static backend list is used).
// Secret tokens are never sent; when the provider type is unchanged the backend
// preserves the existing Consul/Kubernetes token.
export type DiscoveryPatch = {
  type: "static" | "dns" | "dns_srv" | "consul" | "kubernetes";
  target?: string;
  refresh?: string;
  consul?: {
    address?: string;
    service?: string;
    tag?: string;
    datacenter?: string;
    passing_only?: boolean;
  };
  kubernetes?: {
    namespace?: string;
    service?: string;
    port?: string;
    api_server?: string;
    ca_file?: string;
    insecure_skip_tls_verify?: boolean;
  };
};

// ValidationIssue mirrors the backend validationError shape (code/path/summary/
// detail/severity). Shared by the patch preview and the validate/apply flows.
export const ValidationIssueSchema = z.object({
  code: z.string(),
  path: z.string().optional(),
  summary: z.string(),
  detail: z.string().optional(),
  severity: z.string(),
});
export type ValidationIssue = z.infer<typeof ValidationIssueSchema>;

export const PatchOperationSummarySchema = z.object({
  op_index: z.number().int().nonnegative(),
  op: z.string(),
  summary: z.string(),
});
export type PatchOperationSummary = z.infer<typeof PatchOperationSummarySchema>;

export const PatchLifecycleChangeSchema = z.object({
  path: z.string(),
  declared: z.string(),
  effective: z.string(),
  subsystem: z.string(),
  reason: z.string(),
  detail: z.string().optional(),
  ignored: z.boolean(),
  reserved: z.boolean(),
});
export type PatchLifecycleChange = z.infer<typeof PatchLifecycleChangeSchema>;

export const PatchLifecycleSchema = z.object({
  changes: z.array(PatchLifecycleChangeSchema).default([]),
  can_apply_hot: z.boolean(),
  can_stage_restart: z.boolean(),
  hot_paths: z.array(z.string()).default([]),
  restart_required_paths: z.array(z.string()).default([]),
  new_listener_only_paths: z.array(z.string()).default([]),
  ignored_deprecated_paths: z.array(z.string()).default([]),
  validation_rejected_paths: z.array(z.string()).default([]),
  pending_subsystems: z.array(z.string()).default([]),
});
export type PatchLifecycle = z.infer<typeof PatchLifecycleSchema>;

export const RawConfigPreviewSchema = z.object({
  ok: z.literal(true),
  base_version: z.string(),
  valid: z.boolean(),
  validation_errors: z.array(ValidationIssueSchema).default([]),
  diff: ConfigDiffSchema,
  lifecycle: PatchLifecycleSchema,
});
export type RawConfigPreview = z.infer<typeof RawConfigPreviewSchema>;

/**
 * Classifies a raw candidate against the exact editable baseline used to build
 * it. The candidate is sent only in this authorized request and is never
 * returned, logged, or persisted in browser storage.
 */
export async function previewRawConfig(
  candidate: string,
  baseVersion: string,
): Promise<RawConfigPreview> {
  const headers = new Headers({
    Accept: "application/json",
    "Content-Type": "application/toml",
    "X-Jul-Base-Version": baseVersion,
  });
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch("/api/config/preview", {
    method: "POST",
    headers,
    body: candidate,
  });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    else if (resp.status === 403) notifyForbidden();
    classifyPatchFailure("/config/preview", resp.status, resp.statusText, data, resp);
  }
  return RawConfigPreviewSchema.parse(data);
}

export const PatchOperationFailureSchema = z.object({
  ok: z.literal(false),
  message: z.string(),
  errors: z.array(ValidationIssueSchema).default([]),
  op_index: z.number().int().nonnegative(),
  op: z.string(),
});
export type PatchOperationFailure = z.infer<typeof PatchOperationFailureSchema>;

export const PatchResultSchema = z.object({
  ok: z.literal(true),
  // Preserve the legacy combined string; ordered machine-readable entries live
  // in operation_summaries instead of silently changing summary to an array.
  summary: z.string(),
  operation_summaries: z.array(PatchOperationSummarySchema).default([]),
  // Keep the established property for Console handoff code, but always discard
  // preview payloads. A legacy/misconfigured server cannot surface raw TOML.
  candidate: z
    .unknown()
    .optional()
    .transform((): undefined => undefined),
  diff: ConfigDiffSchema,
  // Optional in the parser for compatibility with old fixtures/servers; #77
  // responses always include the authoritative current editable version.
  base_version: z.string().optional(),
  valid: z.boolean().default(true),
  validation_errors: z.array(ValidationIssueSchema).default([]),
  lifecycle: PatchLifecycleSchema.optional(),
});
export type PatchResult = z.infer<typeof PatchResultSchema>;

/**
 * Previews a structured edit server-side and resolves with the diff for review.
 * It does NOT return the candidate TOML (that is gated on config:raw via
 * fetchPatchCandidate); the caller applies the ops atomically through
 * applyPatchBatch. Rejects with ConfigRejectedError when the edit cannot be
 * applied (target not found, invalid op, last backend, …).
 */
export async function patchConfig(patch: ConfigPatch): Promise<PatchResult> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  headers.set("Content-Type", "application/json");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch("/api/config/patch", {
    method: "POST",
    headers,
    body: JSON.stringify(patch),
  });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    classifyPatchFailure("/config/patch", resp.status, resp.statusText, data, resp);
  }
  return PatchResultSchema.parse(data);
}

/**
 * Previews a batch of structured patch operations server-side without
 * persisting. The ops are applied in order to a freshly-loaded config; the
 * returned diff represents the combined effect (the candidate TOML is withheld —
 * see fetchPatchCandidate). This lets editors that create compound objects
 * (e.g. a new server plus its first location) hand off a single preview to the
 * ConfigPanel (F-06).
 */
export async function patchConfigBatch(
  ops: ConfigPatch[],
  baseVersion?: string,
): Promise<PatchResult> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  headers.set("Content-Type", "application/json");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch("/api/config/patch/preview", {
    method: "POST",
    headers,
    body: JSON.stringify({ base_version: baseVersion, ops }),
  });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    classifyPatchFailure("/config/patch/preview", resp.status, resp.statusText, data, resp);
  }
  return PatchResultSchema.parse(data);
}

// PatchCandidate is the config:raw-gated response of /api/config/patch/candidate:
// the full candidate TOML that results from applying a batch of ops, plus the
// base_version it was computed against. Unlike the preview endpoints this DOES
// carry the candidate, so the source view can display the true proposed
// configuration instead of the current persisted bytes (N-01, WS05).
export const PatchCandidateSchema = PatchResultSchema.partial().extend({
  ok: z.literal(true),
  candidate: z.string(),
  base_version: z.string(),
});
export type PatchCandidate = z.infer<typeof PatchCandidateSchema>;

/**
 * fetchPatchCandidate returns the full candidate TOML produced by applying a
 * batch of structured ops, from the config:raw-gated /api/config/patch/candidate
 * endpoint. base_version echoes the fingerprint the candidate was computed
 * against so the caller can detect a stale preview.
 *
 * It preserves the server's error contract as typed errors:
 *   - 403 (the token lacks config:raw) and any other transport failure →
 *     ApiError, carrying the HTTP status;
 *   - 409 (the config changed since the edit was prepared) →
 *     ConfigConflictError, carrying the current version so the caller can
 *     recompute and retry;
 *   - 400 (an op could not be applied) → PatchOperationRejectedError, carrying
 *     the structured issues plus the zero-based op index and discriminator.
 */
export async function fetchPatchCandidate(
  ops: ConfigPatch[],
  baseVersion?: string,
): Promise<PatchCandidate> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  headers.set("Content-Type", "application/json");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch("/api/config/patch/candidate", {
    method: "POST",
    headers,
    body: JSON.stringify({ base_version: baseVersion, ops }),
  });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    classifyPatchFailure("/config/patch/candidate", resp.status, resp.statusText, data, resp);
  }
  return PatchCandidateSchema.parse(data);
}

// ── Validate / Apply / Wizard (write flows) ──────────────────────────────────

export const ValidationResultSchema = z.object({
  ok: z.boolean(),
  message: z.string().optional(),
  errors: z.array(ValidationIssueSchema).optional(),
});
export type ValidationResult = z.infer<typeof ValidationResultSchema>;

/** Side-effect-free validation of a candidate TOML document. Always HTTP 200. */
export async function validateConfig(candidate: string): Promise<ValidationResult> {
  const data = await api<unknown>("/config/validate", {
    method: "POST",
    headers: { "Content-Type": "application/toml" },
    body: candidate,
  });
  return ValidationResultSchema.parse(data);
}

/** Raised when /config/apply rejects a candidate (HTTP 400 + structured body). */
export class ConfigRejectedError extends Error {
  constructor(
    message: string,
    public readonly issues: ValidationIssue[],
  ) {
    super(message);
    this.name = "ConfigRejectedError";
  }
}

/** A structured patch batch rejection tied to one zero-based request op. */
export class PatchOperationRejectedError extends ConfigRejectedError {
  constructor(
    message: string,
    issues: ValidationIssue[],
    public readonly opIndex: number,
    public readonly op: string,
  ) {
    super(message, issues);
    this.name = "PatchOperationRejectedError";
  }
}

// ReloadSnapshotSchema mirrors the Go admin.ReloadSnapshot: the outcome and
// timing of the most recent configuration reload. The server includes it in
// the apply response as previous_reload so the Console can surface a slow
// reload (timed_out) as a distinct, operator-legible outcome.
//
// @deprecated Use the `reload` field in ApplyResultSchema instead. This field
// carries the correlated result of the reload triggered by the apply and
// supersedes previous_reload for all new consumers. previous_reload will be
// removed in the next MINOR release after the structured reload contract has
// been validated in production.
export const ReloadSnapshotSchema = z.object({
  ok: z.boolean(),
  timed_out: z.boolean().optional(),
  duration: z.number().optional(),
  at: z.string().optional(),
  error: z.string().optional(),
});
export type ReloadSnapshot = z.infer<typeof ReloadSnapshotSchema>;

const ConfigApplyResultBaseSchema = z.object({
  ok: z.boolean(),
  // apply_id is the monotonic transaction ID, present even when reload is absent
  // (e.g., enqueue failure). Used for outcome correlation and sequence guarding.
  apply_id: z.string().optional(),
  // mode is "hot" or "stage_restart". When absent the response is from the
  // legacy path (pre-P2-02) and should be treated as "hot".
  mode: z.enum(["hot", "stage_restart"]).optional(),
  // pending_reload reflects the server's truthfulness contract: the config has
  // been validated and persisted, but the live runtime swap is asynchronous, so
  // the status below is the configuration taking effect, not a confirmation
  // that the running runtime has already switched. Optional for back-compat.
  pending_reload: z.boolean().optional(),
  // version is the fresh config fingerprint after this apply, used to advance
  // the raw editor's optimistic-concurrency token so a follow-up edit does not
  // trip a spurious conflict.
  version: z.string().optional(),
  persisted_version: z.string().optional(),
  desired_version: z.string().optional(),
  serving_version: z.string().optional(),
  conflict: z.boolean().optional(),
  current_version: z.string().optional(),
  admin_change: z.boolean().optional(),
  changes: z.array(z.string()).optional(),
  restart_required: z.boolean().optional(),
  can_stage: z.boolean().optional(),
  validation_errors: z.array(z.string()).optional(),
  persisted: z.boolean().optional(),
  restored: z.boolean().optional(),
  restore_error: z.string().optional(),
  final_disk_version: z.string().optional(),
  final_serving_version: z.string().optional(),
  staged_restart_is_update: z.boolean().optional(),
  // AC-08: names the pre-persistence preflight phase that exceeded the
  // transaction deadline. Present only on a 504 timeout where nothing was
  // persisted; distinct from reload.timed_out_phase, which reports a
  // post-persistence reload that overran while the candidate is already serving.
  timed_out_phase: z.string().optional(),
  message: z.string().optional(),
  status: z.union([z.array(FeatureStatusSchema), z.string()]).optional(),
  // previous_reload is the outcome of the most recent reload before this apply.
  // When timed_out is true the prior reload exceeded reload_timeout; the new
  // config is serving but the slow path should be investigated.
  previous_reload: ReloadSnapshotSchema.optional(),
  // reload is the correlated result of the live reload triggered by this apply
  // (P2-04). Replaces previous_reload for new consumers; both are present
  // during the compatibility window.
  reload: ReloadResultSchema.optional(),
  // pending_restart is set when mode=stage_restart and the staged config is now
  // waiting for a process restart to take effect.
  pending_restart: ApplyPendingRestartStatusSchema.optional(),
  errors: z.array(ValidationIssueSchema).optional(),
  id: z.string().optional(),
});
export const ApplyResultSchema = ConfigApplyResultBaseSchema.transform((value) => ({
  ...value,
  status: Array.isArray(value.status) ? value.status : undefined,
  mutation_status: typeof value.status === "string" ? value.status : undefined,
  mutation_id: value.id,
}));
export type ApplyResult = z.infer<typeof ApplyResultSchema>;

/**
 * The apply mode: hot applies the configuration live; stage_restart saves it
 * for the next process restart without changing the live runtime.
 */
export type ApplyMode = "hot" | "stage_restart";

export type ConfigApplyErrorKind =
  | "pending-restart"
  | "not-applied"
  | "enqueue"
  | "unavailable"
  | "timeout"
  | "rejected";

export class ConfigApplyOutcomeError extends ApiError {
  constructor(
    path: string,
    status: number,
    public readonly kind: ConfigApplyErrorKind,
    public readonly result: ApplyResult,
  ) {
    super(path, status, result.message ?? "Configuration apply failed.");
    this.name = "ConfigApplyOutcomeError";
  }
}

function parseApplyResult(data: unknown): ApplyResult | null {
  const parsed = ApplyResultSchema.safeParse(data);
  return parsed.success ? parsed.data : null;
}

const LegacyConfigMutationResponseSchema = z
  .object({
    status: z.string(),
    id: z.string().optional(),
  })
  .transform((value) =>
    ApplyResultSchema.parse({
      ok: true,
      status: value.status,
      id: value.id,
    }),
  );

function parseConfigMutationResult(data: unknown): ApplyResult {
  const managed = ApplyResultSchema.safeParse(data);
  if (managed.success) return managed.data;
  return LegacyConfigMutationResponseSchema.parse(data);
}

// ── Managed-apply terminal ledger (AC-02 / AC-09) ────────────────────────────
//
// ManagedApplyRecordSchema mirrors admin.publicManagedApplyRecord: the single
// object the console retrieves by *exact* apply ID from
// GET /api/config/applies/{id} to observe the terminal result of a recent
// accepted apply regardless of later transactions. Polling this endpoint — not
// the runtime overview's global last_managed_apply — closes the window where a
// newer, unrelated apply could be mistaken for the awaited one (AC-09).
//
// A record is `pending` or `finalizing` (both HTTP 202, both non-terminal) until
// it reaches exactly one terminal result (HTTP 200, state=terminal). `finalizing`
// means the runtime outcome already exists but history/audit/ledger finalization
// is still running; clients must keep polling until state=terminal. `result` is
// the same structured ConfigApplyResult the apply response carries; it already
// omits secrets, actor, and source IP. The three finalization fields are advisory
// provenance (AC-14): a committed apply can be ok=true while history_error is
// non-empty (its history sidecar degraded) — that is NOT an apply failure.
export const ManagedApplyStateSchema = z.enum(["pending", "finalizing", "terminal"]);
export type ManagedApplyState = z.infer<typeof ManagedApplyStateSchema>;

export const ManagedApplyRecordSchema = z.object({
  id: z.string(),
  state: ManagedApplyStateSchema,
  // operation is a bounded audit label (config.apply, config.patch, …). Kept as
  // a string so an added operation kind never fails the parse for a field the
  // console only surfaces, never branches on for correctness.
  operation: z.string().optional(),
  started_at: z.string().optional(),
  // deadline is the absolute transaction deadline projected for deadline-aware
  // polling (AC-08); omitted when the server recorded no bounded deadline.
  deadline: z.string().optional(),
  completed_at: z.string().optional(),
  result: ApplyResultSchema,
  // AC-14 finalization provenance: advisory, never readiness-affecting.
  history_snapshot_id: z.string().optional(),
  history_error: z.string().optional(),
  finalization_error: z.string().optional(),
});
export type ManagedApplyRecord = z.infer<typeof ManagedApplyRecordSchema>;

// ManagedApplyLookup is the discriminated result of an exact-ID ledger lookup.
// It replaces the ambiguous `ManagedApplyRecord | null`: a `missing` result (the
// server has no record for the ID, HTTP 404) is a distinct, explicit case that a
// caller must handle as "keep waiting or expire", never as an implicit success.
// Modeling absence as a variant instead of `null` removes the risk that a `null`
// is silently coerced into a truthy/terminal branch.
export type ManagedApplyLookup =
  | { readonly kind: "record"; readonly record: ManagedApplyRecord }
  | { readonly kind: "missing" };

/**
 * fetchManagedApply retrieves the terminal-or-pending ledger record for an exact
 * apply ID (AC-09). It resolves with:
 *   - `{ kind: "record", record }` for a terminal (200) or pending (202)
 *     transaction;
 *   - `{ kind: "missing" }` when the server has no record for the ID (404) — a
 *     missing record is NEVER treated as a success by callers; polling either
 *     keeps waiting or expires without ever claiming the new configuration is
 *     serving.
 * It rejects with ApiError for a malformed ID (400) or any other transport
 * failure. The endpoint is always Cache-Control: no-store on the server; this
 * client sets no-store on the request too so an intermediary cannot serve a
 * stale pending record after the transaction has finalized.
 */
export async function fetchManagedApply(id: string): Promise<ManagedApplyLookup> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  headers.set("Cache-Control", "no-store");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch(`/api/config/applies/${encodeURIComponent(id)}`, { headers });
  if (resp.status === 404) return { kind: "missing" };
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  // 202 (pending) is a non-error status the fetch API still reports as !ok, so
  // it must pass through to parsing; only genuine failures throw.
  if (!resp.ok && resp.status !== 202) {
    if (resp.status === 401) notifyUnauthorized();
    let msg = `${String(resp.status)} ${resp.statusText}`;
    const body = data as { error?: string } | null;
    if (body?.error) msg = body.error;
    throw new ApiError(`/config/applies/${id}`, resp.status, msg, parseRetryAfter(resp));
  }
  return { kind: "record", record: ManagedApplyRecordSchema.parse(data) };
}

function classifyPatchFailure(
  path: string,
  status: number,
  statusText: string,
  data: unknown,
  response: Response,
): never {
  if (status === 403) notifyForbidden();
  const conflict = ConflictBodySchema.safeParse(data);
  if (status === 409 && conflict.success && conflict.data.conflict) {
    throw new ConfigConflictError(
      conflict.data.message ??
        "The configuration changed since this edit was prepared; reload and try again.",
      conflict.data.current_version,
    );
  }
  const operation = PatchOperationFailureSchema.safeParse(data);
  if (status === 400 && operation.success) {
    throw new PatchOperationRejectedError(
      operation.data.message,
      operation.data.errors,
      operation.data.op_index,
      operation.data.op,
    );
  }
  const rejected = ValidationResultSchema.safeParse(data);
  if (status === 400 && rejected.success) {
    throw new ConfigRejectedError(
      rejected.data.message ?? "The edit was rejected.",
      rejected.data.errors ?? [],
    );
  }
  let message = `${String(status)} ${statusText}`;
  const body = data as { error?: string } | null;
  if (body?.error) message = body.error;
  throw new ApiError(path, status, message, parseRetryAfter(response));
}

function classifyApplyFailure(path: string, status: number, data: unknown): never {
  // A gated write was forbidden: refresh proactive gating (N-02).
  if (status === 403) notifyForbidden();
  const result = parseApplyResult(data);
  const conflict = ConflictBodySchema.safeParse(data);
  if (conflict.success && conflict.data.admin_change) {
    throw new ConfigAdminChangeError(
      conflict.data.message ??
        "This change affects how you reach the admin console; confirm to proceed.",
      conflict.data.changes ?? [],
    );
  }
  if (conflict.success && conflict.data.restart_required) {
    throw new ConfigRestartRequiredError(
      conflict.data.message ?? "This change requires a server restart to take effect.",
      conflict.data.can_stage ?? false,
      conflict.data.pending_restart?.subsystems ?? [],
    );
  }
  if (conflict.success && conflict.data.conflict) {
    throw new ConfigConflictError(
      conflict.data.message ??
        "The configuration changed since this edit was prepared; reload and try again.",
      conflict.data.current_version,
    );
  }
  const operation = PatchOperationFailureSchema.safeParse(data);
  if (status === 400 && operation.success) {
    throw new PatchOperationRejectedError(
      operation.data.message,
      operation.data.errors,
      operation.data.op_index,
      operation.data.op,
    );
  }
  const rejected = ValidationResultSchema.safeParse(data);
  if (status === 400 && rejected.success) {
    throw new ConfigRejectedError(
      rejected.data.message ?? "The configuration was rejected.",
      rejected.data.errors ?? [],
    );
  }
  if (result?.timed_out_phase) {
    // AC-08: a pre-persistence preflight timeout (HTTP 504). Nothing was
    // persisted, so classify it distinctly from a rejection: the panel renders
    // the preflight-timeout outcome and must not imply the candidate is serving.
    throw new ConfigApplyOutcomeError(path, status, "timeout", result);
  }
  if (result) {
    const kind: ConfigApplyErrorKind =
      result.reload?.failed_phase === "enqueue"
        ? "enqueue"
        : result.reload?.outcome === "not_applied"
          ? "not-applied"
          : result.pending_restart !== undefined
            ? "pending-restart"
            : status === 503
              ? "unavailable"
              : "rejected";
    throw new ConfigApplyOutcomeError(path, status, kind, result);
  }
  throw new ApiError(path, status, `HTTP ${String(status)}`);
}

/**
 * Applies a candidate config through the authoritative write path and resolves
 * with the post-apply runtime status delta. Rejects with ConfigRejectedError
 * when the backend refuses the draft, ConfigConflictError when baseVersion is
 * supplied and the live config has since changed (optimistic concurrency), or
 * ApiError on transport failure.
 */
export async function applyConfig(
  candidate: string,
  baseVersion?: string,
  confirmAdmin = false,
  mode: ApplyMode = "hot",
): Promise<ApplyResult> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  headers.set("Content-Type", "application/toml");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const params = new URLSearchParams();
  if (baseVersion) params.set("base_version", baseVersion);
  if (confirmAdmin) params.set("confirm_admin", "true");
  if (mode !== "hot") params.set("mode", mode);
  const query = params.toString();
  const url = query ? `/api/config/apply?${query}` : "/api/config/apply";
  const resp = await fetch(url, { method: "POST", headers, body: candidate });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    classifyApplyFailure("/config/apply", resp.status, data);
  }
  return ApplyResultSchema.parse(data);
}

// ConfigConflictError is thrown when applyPatchBatch is rejected with 409
// because the live config changed since the edit was prepared. currentVersion
// lets the caller reload, recompute, and retry.
export class ConfigConflictError extends Error {
  constructor(
    message: string,
    public readonly currentVersion?: string,
  ) {
    super(message);
    this.name = "ConfigConflictError";
  }
}

// ConfigRestartRequiredError is thrown when an apply is valid but cannot be
// hot-applied because it changes a setting fixed at process start (currently the
// ACME issued-domain set and issuer). The server did NOT write the change, so
// the live config is unchanged; the operator must restart for it to take effect.
// It is distinguished from ConfigConflictError by the restart_required body flag
// on the shared 409 response. canStage is true when the candidate can be saved
// for the next restart via mode=stage_restart; subsystems lists the affected
// startup-bound settings.
export class ConfigRestartRequiredError extends Error {
  constructor(
    message: string,
    public readonly canStage: boolean = false,
    public readonly subsystems: string[] = [],
  ) {
    super(message);
    this.name = "ConfigRestartRequiredError";
  }
}

// ConfigAdminChangeError is thrown when an apply would change how the operator
// reaches the admin console (disabling admin, moving its listen address,
// rotating its token, or disabling the web console). The server did NOT write
// the change; the caller confirms the risk and retries with confirmAdmin=true.
// `changes` lists the human-readable reachability changes to show the operator.
export class ConfigAdminChangeError extends Error {
  constructor(
    message: string,
    public readonly changes: string[],
  ) {
    super(message);
    this.name = "ConfigAdminChangeError";
  }
}

const ConflictBodySchema = z.object({
  conflict: z.boolean().optional(),
  restart_required: z.boolean().optional(),
  can_stage: z.boolean().optional(),
  admin_change: z.boolean().optional(),
  changes: z.array(z.string()).optional(),
  message: z.string().optional(),
  current_version: z.string().optional(),
  // pending_restart carries subsystem info when restart_required is true
  // (H-06 fix: full ConfigApplyResult returned at 409).
  pending_restart: z
    .object({
      subsystems: z.array(z.string()).optional(),
    })
    .optional(),
});

export const PatchApplyResultSchema = ConfigApplyResultBaseSchema.extend({
  // mode is "hot" or "stage_restart" (D1). Absent for legacy responses.
  mode: z.enum(["hot", "stage_restart"]).optional(),
  // pending_reload mirrors applyConfig: persisted and validated, but the live
  // runtime swap is asynchronous.
  pending_reload: z.boolean().optional(),
  // version is the fresh config fingerprint for the next optimistic-concurrency
  // check after this apply lands.
  version: z.string().optional(),
  summary: z.array(z.string()),
  operation_summaries: z.array(PatchOperationSummarySchema).default([]),
  diff: ConfigDiffSchema,
  lifecycle: PatchLifecycleSchema.optional(),
  // status is the post-apply runtime delta derived from the persisted config.
}).transform((value) => ({
  ...value,
  status: Array.isArray(value.status) ? value.status : undefined,
  mutation_status: typeof value.status === "string" ? value.status : undefined,
  mutation_id: value.id,
}));
export type PatchApplyResult = z.infer<typeof PatchApplyResultSchema>;

/**
 * Applies a batch of structured edits server-side, atomically. The server
 * recomputes every op from a freshly-loaded config and persists the result
 * through the authoritative write path, so the client never renders or trusts a
 * candidate. When baseVersion is supplied and the live config has since changed,
 * the apply is rejected with ConfigConflictError (optimistic concurrency).
 * Rejects with ConfigRejectedError when an op cannot be applied or the result is
 * invalid, or ApiError on transport failure. mode selects the apply path:
 * "hot" applies live (default); "stage_restart" saves for the next restart.
 */
export async function applyPatchBatch(
  ops: ConfigPatch[],
  baseVersion?: string,
  mode: ApplyMode = "hot",
  confirmAdmin = false,
): Promise<PatchApplyResult> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  headers.set("Content-Type", "application/json");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const params = new URLSearchParams();
  if (mode !== "hot") params.set("mode", mode);
  if (confirmAdmin) params.set("confirm_admin", "true");
  const query = params.toString();
  const url = query ? `/api/config/patch/apply?${query}` : "/api/config/patch/apply";
  const resp = await fetch(url, {
    method: "POST",
    headers,
    body: JSON.stringify({ base_version: baseVersion, ops }),
  });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    classifyApplyFailure("/config/patch/apply", resp.status, data);
  }
  return PatchApplyResultSchema.parse(data);
}

export const WizardInputSchema = z.object({
  mode: z.enum(["serve", "proxy", "app"]),
  path: z.string().optional(),
  target: z.string().optional(),
  listen: z.string().optional(),
  // App-mode fields (put an application behind Jul).
  name: z.string().optional(),
  backends: z.array(z.string()).optional(),
  preset: z.string().optional(),
  route_path: z.string().optional(),
  health_check: z.boolean().optional(),
  health_path: z.string().optional(),
  strategy: z.string().optional(),
});
export type WizardInput = z.infer<typeof WizardInputSchema>;

const WizardResultSchema = z.object({
  toml: z.string().optional(),
  ops: z.array(z.unknown()).optional(),
});

/** Generates a starter TOML document from wizard inputs (non-mutating). */
export async function generateConfig(input: WizardInput): Promise<string> {
  const data = await api<unknown>("/wizard/generate", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  return WizardResultSchema.parse(data).toml ?? "";
}

/** Generates a structured patch batch from wizard inputs (F-06). */
export async function generateConfigPatches(input: WizardInput): Promise<ConfigPatch[]> {
  const data = await api<unknown>("/wizard/generate?format=patch", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  const parsed = WizardResultSchema.parse(data);
  if (!Array.isArray(parsed.ops)) {
    throw new Error("The server did not return patch operations.");
  }
  // Each op was returned as a generic JSON object; re-encode/decode through the
  // discriminated ConfigPatch type to validate shapes without a second round-trip.
  return parsed.ops.map((op) => JSON.parse(JSON.stringify(op)) as ConfigPatch);
}

// ── Search & discovery (/api/search) ─────────────────────────────────────────

export const SearchResultSchema = z.object({
  kind: z.enum(["route", "app"]),
  title: z.string(),
  detail: z.string(),
  score: z.number(),
  target: z.string().optional(),
  upstream: z.string().optional(),
  routes: z.array(z.string()).optional(),
  badges: z.array(z.string()).optional(),
});
export type SearchResult = z.infer<typeof SearchResultSchema>;

/**
 * Ranked discovery across routes and apps via the backend /api/search endpoint.
 * The server reflects route↔app relationships (which app a route targets, which
 * routes use an app, and which apps are unused) so the UI does not re-derive them.
 */
export function searchConfig(
  query: string,
  type: "all" | "routes" | "apps" = "all",
): Promise<SearchResult[]> {
  const params = new URLSearchParams();
  if (query) params.set("q", query);
  if (type !== "all") params.set("type", type);
  const qs = params.toString();
  return api<unknown>(`/search${qs ? `?${qs}` : ""}`).then((d) =>
    z.array(SearchResultSchema).parse(d),
  );
}

// ── Operational depth (Phase 5) ─────────────────────────────────────────────

export const RequestSampleSchema = z.object({
  time: z.string(),
  method: z.string(),
  path: z.string(),
  host: z.string().optional(),
  status: z.number(),
  duration_ms: z.number().optional(),
  cache_state: z.string().optional(),
  compressed: z.boolean().optional(),
  rate_limited: z.boolean().optional(),
  origin: z.string().optional(),
  user_agent: z.string().optional(),
});
export type RequestSample = z.infer<typeof RequestSampleSchema>;

export function fetchRequestSamples(): Promise<RequestSample[]> {
  return api<unknown>("/observability/requests").then((d) => z.array(RequestSampleSchema).parse(d));
}

export const RouteFailureSchema = z.object({
  path: z.string(),
  total: z.number(),
  status_4xx: z.number(),
  status_5xx: z.number(),
  error_rate: z.number(),
  latency_p95_ms: z.number(),
  last_error_class: z.string().optional(),
});
export type RouteFailure = z.infer<typeof RouteFailureSchema>;

export function fetchFailingRoutes(limit = 20): Promise<RouteFailure[]> {
  return api<unknown>(`/observability/failing-routes?limit=${String(limit)}`).then((d) =>
    z.array(RouteFailureSchema).parse(d),
  );
}

export const HealthEventSchema = z.object({
  time: z.string(),
  healthy: z.boolean(),
});

export const BackendHealthHistorySchema = z.object({
  pool: z.string(),
  backend: z.string(),
  healthy: z.boolean(),
  transitions: z.number(),
  flapping: z.boolean(),
  last_up: z.string().optional(),
  last_down: z.string().optional(),
  recent: z.array(HealthEventSchema).optional(),
});
export type BackendHealthHistory = z.infer<typeof BackendHealthHistorySchema>;

export function fetchUpstreamHistory(): Promise<BackendHealthHistory[]> {
  return api<unknown>("/observability/upstream-history").then((d) =>
    z.array(BackendHealthHistorySchema).parse(d),
  );
}

export const CertRenewalEventSchema = z.object({
  time: z.string(),
  success: z.boolean(),
  error: z.string().optional(),
  not_after: z.string().optional(),
  issuer: z.string().optional(),
  staging: z.boolean().optional(),
});

export const CertRenewalHistorySchema = z.object({
  domain: z.string(),
  next_expiry: z.string().optional(),
  days_left: z.number(),
  issuer: z.string().optional(),
  staging: z.boolean().optional(),
  last_attempt: z.string().optional(),
  last_success: z.string().optional(),
  last_error: z.string().optional(),
  last_error_time: z.string().optional(),
  recent: z.array(CertRenewalEventSchema).optional(),
});
export type CertRenewalHistory = z.infer<typeof CertRenewalHistorySchema>;

export function fetchCertHistory(): Promise<CertRenewalHistory[]> {
  return api<unknown>("/observability/cert-history").then((d) =>
    z.array(CertRenewalHistorySchema).parse(d),
  );
}

export const TimelineEventSchema = z.object({
  time: z.string(),
  category: z.string(),
  type: z.string(),
  severity: z.string(),
  message: z.string(),
  ref: z.string().optional(),
});
export type TimelineEvent = z.infer<typeof TimelineEventSchema>;

export function fetchTimeline(): Promise<TimelineEvent[]> {
  return api<unknown>("/observability/timeline").then((d) => z.array(TimelineEventSchema).parse(d));
}

// ── Operations log tail (Phase 4g) ───────────────────────────────────────────

export const LogEntrySchema = z.object({
  time: z.string(),
  method: z.string(),
  host: z.string(),
  path: z.string(),
  status: z.number(),
  bytes: z.number(),
  duration_ms: z.number(),
  // client_ip is the canonical client address; peer_ip appears only when a
  // trusted proxy asserted a different address than the socket peer.
  client_ip: z.string().optional(),
  peer_ip: z.string().optional(),
  request_id: z.string().optional(),
  trace_id: z.string().optional(),
  user_agent: z.string().optional(),
  proto: z.string().optional(),
});
export type LogEntry = z.infer<typeof LogEntrySchema>;

/**
 * Fetches up to limit recent access-log entries (newest first) from the bounded
 * Operations Log ring buffer. The buffer is privacy-preserving: paths are
 * redacted, query strings dropped, and User-Agents reduced to a coarse family.
 */
export function fetchLogs(limit = 200): Promise<LogEntry[]> {
  return api<unknown>(`/observability/logs?limit=${String(limit)}`).then((d) =>
    z.array(LogEntrySchema).parse(d),
  );
}

// ── Console health & frontend error reporting (Milestone 5.7) ────────────────

export const ClientErrorSchema = z.object({
  time: z.string(),
  message: z.string(),
  source: z.string().optional(),
  line: z.number().optional(),
  col: z.number().optional(),
});
export type ClientError = z.infer<typeof ClientErrorSchema>;

export const ConsoleHealthSchema = z.object({
  status: z.string(),
  requests: z.number(),
  errors: z.number(),
  latency_p50: z.number(),
  latency_p95: z.number(),
  latency_p99: z.number(),
  sse_conns: z.number(),
  client_errors: z.array(ClientErrorSchema).optional(),
});
export type ConsoleHealth = z.infer<typeof ConsoleHealthSchema>;

export function fetchConsoleHealth(): Promise<ConsoleHealth> {
  return api<unknown>("/admin/health").then((d) => ConsoleHealthSchema.parse(d));
}

/**
 * Reports a frontend JavaScript error to the Console's bounded error sink
 * (Milestone 5.7). Best-effort: failures are swallowed so the reporter never
 * cascades into more error noise. The backend redacts and caps the payload.
 */
export function reportClientError(input: {
  message: string;
  source?: string;
  line?: number;
  col?: number;
}): void {
  const token = authToken.get();
  const headers = new Headers();
  headers.set("Content-Type", "application/json");
  if (token) headers.set("Authorization", `Bearer ${token}`);
  void fetch("/api/admin/client-errors", {
    method: "POST",
    headers,
    body: JSON.stringify(input),
    keepalive: true,
  }).catch(() => {
    // Swallow: the reporter must never throw.
  });
}

// ── Audit log (Milestone 6.6) ────────────────────────────────────────────────

export const AuditEventSchema = z.object({
  id: z.number(),
  time: z.string(),
  actor: z.string(),
  // token_id is the public, non-secret credential identifier the server derives
  // from the presented token. It is safe to display and never contains secret
  // bytes. Optional for compatibility with events recorded before attribution.
  token_id: z.string().optional().default(""),
  operation: z.string(),
  resource: z.string().optional(),
  result: z.string(),
  detail: z.string().optional(),
  source_ip: z.string().optional(),
});
export type AuditEvent = z.infer<typeof AuditEventSchema>;

export interface AuditFilter {
  op?: string | undefined;
  result?: string | undefined;
  limit?: number | undefined;
}

export function fetchAudit(filter: AuditFilter = {}): Promise<AuditEvent[]> {
  const params = new URLSearchParams();
  if (filter.op) params.set("op", filter.op);
  if (filter.result) params.set("result", filter.result);
  if (filter.limit) params.set("limit", String(filter.limit));
  const qs = params.toString();
  return api<unknown>(`/audit${qs ? `?${qs}` : ""}`).then((d) =>
    z.array(AuditEventSchema).parse(d),
  );
}

/** Builds the relative path for an audit export (used for tests and links). */
export function auditExportUrl(format: "json" | "csv", filter: AuditFilter = {}): string {
  const params = new URLSearchParams();
  params.set("format", format);
  if (filter.op) params.set("op", filter.op);
  if (filter.result) params.set("result", filter.result);
  return `/api/audit/export?${params.toString()}`;
}

/**
 * Downloads an audit export. The export endpoint requires the bearer token in
 * the Authorization header, which a plain anchor download cannot supply, so the
 * file is fetched and saved via an object URL.
 */
export async function downloadAuditExport(
  format: "json" | "csv",
  filter: AuditFilter = {},
): Promise<void> {
  const headers = new Headers();
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch(auditExportUrl(format, filter), { headers });
  if (!resp.ok) {
    throw new ApiError("/audit/export", resp.status, `${String(resp.status)} ${resp.statusText}`);
  }
  const blob = await resp.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `audit.${format}`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// ── Events (SSE over fetch) ──────────────────────────────────────────────────

export interface SseEvent {
  type: string;
  time: string;
  data?: unknown;
}

/**
 * Opens the GET /api/events stream and returns a cleanup function. Uses fetch +
 * ReadableStream rather than EventSource so the bearer token travels in the
 * Authorization header — never in the URL where it would leak into access logs
 * — and reconnects with capped exponential backoff until cleaned up.
 */
export function subscribeEvents(
  onEvent: (ev: SseEvent) => void,
  onError?: (err: unknown) => void,
): () => void {
  const controller = new AbortController();
  void streamEvents("/api/events", controller.signal, onEvent, onError);
  return () => {
    controller.abort();
  };
}

/**
 * Opens the GET /api/observability/logs/stream tail and returns a cleanup
 * function. The stream replays a bounded backlog on connect, then delivers each
 * new access-log entry. The "connected" control frame fires onOpen and "ping"
 * frames are ignored; only LogEntry payloads reach onEntry. Like subscribeEvents
 * it uses fetch + ReadableStream so the bearer token travels in the
 * Authorization header, and it reconnects with capped exponential backoff until
 * cleaned up.
 */
export function subscribeLogs(
  onEntry: (entry: LogEntry) => void,
  handlers?: { onOpen?: () => void; onError?: (err: unknown) => void },
): () => void {
  const controller = new AbortController();
  void streamEvents(
    "/api/observability/logs/stream",
    controller.signal,
    (ev) => {
      if (ev.type === "connected") {
        handlers?.onOpen?.();
        return;
      }
      if (ev.type !== "log" || ev.data === undefined) return;
      const parsed = LogEntrySchema.safeParse(ev.data);
      if (parsed.success) onEntry(parsed.data);
    },
    handlers?.onError,
  );
  return () => {
    controller.abort();
  };
}

async function streamEvents(
  url: string,
  signal: AbortSignal,
  onEvent: (ev: SseEvent) => void,
  onError?: (err: unknown) => void,
): Promise<void> {
  let delay = 1000;
  while (!signalAborted(signal)) {
    try {
      const headers = new Headers();
      headers.set("Accept", "text/event-stream");
      const token = authToken.get();
      if (token) headers.set("Authorization", `Bearer ${token}`);
      const resp = await fetch(url, { headers, signal });
      if (resp.status === 401) {
        notifyUnauthorized();
        onError?.(new ApiError(url, 401, "Unauthorized"));
        return; // auth won't recover on retry
      }
      if (!resp.ok || !resp.body) {
        throw new ApiError(url, resp.status, `${String(resp.status)} ${resp.statusText}`);
      }
      delay = 1000; // a healthy connection resets the backoff
      await pumpSse(resp.body, onEvent);
    } catch (err) {
      if (signalAborted(signal)) return;
      onError?.(err);
    }
    await delayWithAbort(delay, signal);
    delay = Math.min(delay * 2, 15_000);
  }
}

async function pumpSse(
  body: ReadableStream<Uint8Array>,
  onEvent: (ev: SseEvent) => void,
): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let boundary = buffer.indexOf("\n\n");
    while (boundary !== -1) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const ev = parseSseFrame(frame);
      if (ev) onEvent(ev);
      boundary = buffer.indexOf("\n\n");
    }
  }
}

function parseSseFrame(frame: string): SseEvent | null {
  const data: string[] = [];
  for (const line of frame.split("\n")) {
    if (line.startsWith("data:")) {
      const v = line.slice(5);
      data.push(v.startsWith(" ") ? v.slice(1) : v);
    }
  }
  if (data.length === 0) return null;
  try {
    return JSON.parse(data.join("\n")) as SseEvent;
  } catch {
    return null;
  }
}

function delayWithAbort(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
}

// Reads signal.aborted through a function call so TypeScript does not flow-narrow
// the value to a constant inside the reconnect loop (it flips asynchronously).
function signalAborted(signal: AbortSignal): boolean {
  return signal.aborted;
}

/** Reads the per-response CSP style nonce injected into the SPA shell. */
export function cspNonce(): string {
  if (typeof document === "undefined") return "";
  return document.querySelector('meta[name="csp-nonce"]')?.getAttribute("content") ?? "";
}

export const PluginUploadResponseSchema = z.object({
  name: z.string(),
  path: z.string(),
  size: z.number(),
});
export type PluginUploadResponse = z.infer<typeof PluginUploadResponseSchema>;

export async function uploadPluginWasm(file: File): Promise<PluginUploadResponse> {
  const form = new FormData();
  form.append("wasm", file);
  const token = authToken.get();
  const headers = new Headers();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch("/api/plugins/upload", { method: "POST", headers, body: form });
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    let msg = `${String(resp.status)} ${resp.statusText}`;
    try {
      const body = (await resp.json()) as { error?: string };
      if (body.error) msg = body.error;
    } catch {
      // ignore
    }
    throw new ApiError("/plugins/upload", resp.status, msg);
  }
  return PluginUploadResponseSchema.parse(await resp.json());
}

// ── Transcode designer (Phase 2) ────────────────────────────────────────────

export const TranscodeMethodSchema = z.object({
  full_name: z.string(),
  service: z.string(),
  method: z.string(),
  http_method: z.string(),
  path: z.string(),
  body: z.string(),
  streaming: z.boolean(),
});
export type TranscodeMethod = z.infer<typeof TranscodeMethodSchema>;

export const TranscodeDescriptorResponseSchema = z.object({
  methods: z.array(TranscodeMethodSchema),
});
export type TranscodeDescriptorResponse = z.infer<typeof TranscodeDescriptorResponseSchema>;

/**
 * Uploads a compiled protobuf FileDescriptorSet (.pb) to the admin backend
 * and returns the list of methods with google.api.http annotations. Used by
 * the Transcode designer to let operators pick which gRPC methods to expose
 * via REST/JSON transcoding.
 */
export async function uploadTranscodeDescriptor(file: File): Promise<TranscodeDescriptorResponse> {
  const form = new FormData();
  form.append("descriptor", file);
  const headers = new Headers();
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch("/api/transcode/descriptor-upload", {
    method: "POST",
    headers,
    body: form,
  });
  if (!resp.ok) {
    let msg = "Descriptor upload failed";
    try {
      msg = await resp.text();
    } catch {
      /* ignore */
    }
    throw new ApiError("/api/transcode/descriptor-upload", resp.status, msg);
  }
  const data = (await resp.json()) as unknown;
  return TranscodeDescriptorResponseSchema.parse(data);
}
