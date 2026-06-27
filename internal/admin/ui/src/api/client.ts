import { z } from "zod";

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

// ── Typed fetch client ───────────────────────────────────────────────────────

export class ApiError extends Error {
  constructor(
    public readonly path: string,
    public readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const token = authToken.get();
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch(`/api${path}`, { ...init, headers });
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    let msg = `${String(resp.status)} ${resp.statusText}`;
    try {
      const body = (await resp.json()) as { error?: string };
      if (body.error) msg = body.error;
    } catch {
      // ignore parse failure
    }
    throw new ApiError(path, resp.status, msg);
  }
  return resp.json() as Promise<T>;
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
  rate_limit_detail: z.object({
    enabled: z.boolean(),
    rate: z.number().optional(),
    burst: z.number().optional(),
    key: z.string().optional(),
  }).optional(),
  secure: z.boolean(),
  require_client_cert: z.boolean().default(false),
  upstream: z.string().optional(),
  waf: LocationWAFStateSchema.optional(),
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
  // Guided-editor seed fields (Phase 4b). Non-secret health-check and discovery
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

export const SecurityProjectionSchema = z.object({
  auth_enabled: z.boolean(),
  client_auth: z.string().optional(),
  body_limit: z.string().optional(),
  require_cert_count: z.number(),
  waf_enabled: z.boolean(),
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
});
export type SecurityProjection = z.infer<typeof SecurityProjectionSchema>;

export const CompressionProjectionSchema = z.object({
  enabled: z.boolean(),
  encoders: z.array(z.string()).optional(),
});

export const RateLimitProjectionSchema = z.object({
  enabled: z.boolean(),
  rate: z.number().optional(),
  burst: z.number().optional(),
  key: z.string().optional(),
});
export type RateLimitPatch = z.infer<typeof RateLimitProjectionSchema>;

export const CacheProjectionSchema = z.object({
  enabled: z.boolean(),
  default_ttl: z.string().optional(),
  memory_max: z.string().optional(),
  disk_path: z.string().optional(),
});

export const TracingProjectionSchema = z.object({
  enabled: z.boolean(),
  exporter: z.string().optional(),
  endpoint: z.string().optional(),
  sample_ratio: z.number().optional(),
  service_name: z.string().optional(),
  insecure: z.boolean().optional(),
});

export const TrafficControlsSchema = z.object({
  compression: CompressionProjectionSchema.optional(),
  rate_limit: RateLimitProjectionSchema.optional(),
  cache: CacheProjectionSchema.optional(),
  tracing: TracingProjectionSchema.optional(),
});
export type TrafficControls = z.infer<typeof TrafficControlsSchema>;

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

export async function rollback(id: string): Promise<void> {
  await api("/config/rollback", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id }),
  });
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

// ── Structured config patch (Wave B) ─────────────────────────────────────────

/**
 * A single structured edit to the running configuration. Each op targets an
 * existing object (a route by listen + server_names + match type + path, or an
 * upstream by name) and the server applies it to the PARSED config model,
 * returning the candidate TOML and full diff for review — it does not persist.
 * The caller then applies the candidate through the existing applyConfig path.
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
  | { kind: "static"; target: string }
  | { kind: "redirect"; target: string; status?: number }
  | { kind: "return"; status: number }
  | { kind: "deny" };

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
  | { op: "server_set_limits"; listen: string; limits: ServerLimitsPatch }
  | { op: "server_toggle_http3"; listen: string; enabled: boolean }
  | { op: "server_toggle_h2c"; listen: string; enabled: boolean }
  | { op: "server_set_client_auth"; listen: string; server_names: string[]; client_auth: ClientAuthPatch }
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

export const PatchResultSchema = z.object({
  ok: z.literal(true),
  summary: z.string(),
  candidate: z.string(),
  diff: ConfigDiffSchema,
  // base_version fingerprints the config this candidate was computed from. The
  // UI echoes it back to applyPatchBatch so a stale edit is rejected (409)
  // instead of silently clobbering a concurrent change.
  base_version: z.string().optional(),
  // validation_errors is present when the candidate fails the cheap preview
  // validation (parse + structural/WAF/auth checks). The edit still produced a
  // diff, but applying it would be rejected — the UI surfaces these as warnings.
  validation_errors: z.array(ValidationIssueSchema).optional(),
});
export type PatchResult = z.infer<typeof PatchResultSchema>;

/**
 * Applies a structured edit server-side and resolves with the candidate TOML +
 * diff for review. Rejects with ConfigRejectedError when the edit cannot be
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
    const rejected = ValidationResultSchema.safeParse(data);
    if (rejected.success) {
      throw new ConfigRejectedError(
        rejected.data.message ?? "The edit was rejected.",
        rejected.data.errors ?? [],
      );
    }
    throw new ApiError("/config/patch", resp.status, `${String(resp.status)} ${resp.statusText}`);
  }
  return PatchResultSchema.parse(data);
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

export const ApplyResultSchema = z.object({
  ok: z.literal(true),
  // pending_reload reflects the server's truthfulness contract: the config has
  // been validated and persisted, but the live runtime swap is asynchronous, so
  // the status below is the configuration taking effect, not a confirmation
  // that the running runtime has already switched. Optional for back-compat.
  pending_reload: z.boolean().optional(),
  // version is the fresh config fingerprint after this apply, used to advance
  // the raw editor's optimistic-concurrency token so a follow-up edit does not
  // trip a spurious conflict.
  version: z.string().optional(),
  message: z.string().optional(),
  status: z.array(FeatureStatusSchema),
});
export type ApplyResult = z.infer<typeof ApplyResultSchema>;

/**
 * Applies a candidate config through the authoritative write path and resolves
 * with the post-apply runtime status delta. Rejects with ConfigRejectedError
 * when the backend refuses the draft, ConfigConflictError when baseVersion is
 * supplied and the live config has since changed (optimistic concurrency), or
 * ApiError on transport failure.
 */
export async function applyConfig(candidate: string, baseVersion?: string): Promise<ApplyResult> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  headers.set("Content-Type", "application/toml");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const url = baseVersion
    ? `/api/config/apply?base_version=${encodeURIComponent(baseVersion)}`
    : "/api/config/apply";
  const resp = await fetch(url, { method: "POST", headers, body: candidate });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401) notifyUnauthorized();
    if (resp.status === 409) {
      const conflict = ConflictBodySchema.safeParse(data);
      if (conflict.success && conflict.data.restart_required) {
        throw new ConfigRestartRequiredError(
          conflict.data.message ?? "This change requires a server restart to take effect.",
        );
      }
      throw new ConfigConflictError(
        conflict.success && conflict.data.message
          ? conflict.data.message
          : "The configuration changed since this edit was prepared; reload and try again.",
        conflict.success ? conflict.data.current_version : undefined,
      );
    }
    const rejected = ValidationResultSchema.safeParse(data);
    if (rejected.success) {
      throw new ConfigRejectedError(
        rejected.data.message ?? "The configuration was rejected.",
        rejected.data.errors ?? [],
      );
    }
    throw new ApiError("/config/apply", resp.status, `${String(resp.status)} ${resp.statusText}`);
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
// on the shared 409 response.
export class ConfigRestartRequiredError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ConfigRestartRequiredError";
  }
}

const ConflictBodySchema = z.object({
  conflict: z.boolean().optional(),
  restart_required: z.boolean().optional(),
  message: z.string().optional(),
  current_version: z.string().optional(),
});

export const PatchApplyResultSchema = z.object({
  ok: z.literal(true),
  // pending_reload mirrors applyConfig: persisted and validated, but the live
  // runtime swap is asynchronous.
  pending_reload: z.boolean().optional(),
  // version is the fresh config fingerprint for the next optimistic-concurrency
  // check after this apply lands.
  version: z.string().optional(),
  summary: z.array(z.string()),
  diff: ConfigDiffSchema,
  // status is the post-apply runtime delta derived from the persisted config.
  status: z.array(FeatureStatusSchema).optional(),
  message: z.string().optional(),
});
export type PatchApplyResult = z.infer<typeof PatchApplyResultSchema>;

/**
 * Applies a batch of structured edits server-side, atomically. The server
 * recomputes every op from a freshly-loaded config and persists the result
 * through the authoritative write path, so the client never renders or trusts a
 * candidate. When baseVersion is supplied and the live config has since changed,
 * the apply is rejected with ConfigConflictError (optimistic concurrency).
 * Rejects with ConfigRejectedError when an op cannot be applied or the result is
 * invalid, or ApiError on transport failure.
 */
export async function applyPatchBatch(
  ops: ConfigPatch[],
  baseVersion?: string,
): Promise<PatchApplyResult> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  headers.set("Content-Type", "application/json");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch("/api/config/patch/apply", {
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
    if (resp.status === 409) {
      const conflict = ConflictBodySchema.safeParse(data);
      if (conflict.success && conflict.data.restart_required) {
        throw new ConfigRestartRequiredError(
          conflict.data.message ?? "This change requires a server restart to take effect.",
        );
      }
      throw new ConfigConflictError(
        conflict.success && conflict.data.message
          ? conflict.data.message
          : "The configuration changed since this edit was prepared; reload and try again.",
        conflict.success ? conflict.data.current_version : undefined,
      );
    }
    const rejected = ValidationResultSchema.safeParse(data);
    if (rejected.success) {
      throw new ConfigRejectedError(
        rejected.data.message ?? "The edit was rejected.",
        rejected.data.errors ?? [],
      );
    }
    throw new ApiError(
      "/config/patch/apply",
      resp.status,
      `${String(resp.status)} ${resp.statusText}`,
    );
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

const WizardResultSchema = z.object({ toml: z.string() });

/** Generates a starter TOML document from wizard inputs (non-mutating). */
export async function generateConfig(input: WizardInput): Promise<string> {
  const data = await api<unknown>("/wizard/generate", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  return WizardResultSchema.parse(data).toml;
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
  remote: z.string().optional(),
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
