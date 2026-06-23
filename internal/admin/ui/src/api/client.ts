import { z } from "zod";

// ── Design-token helpers ─────────────────────────────────────────────────────

/** Central auth-token store (sessionStorage so it clears on tab close). */
export const authToken = {
  get: () => sessionStorage.getItem("jul_admin_token") ?? "",
  set: (t: string) => sessionStorage.setItem("jul_admin_token", t),
  clear: () => sessionStorage.removeItem("jul_admin_token"),
};

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
  const resp = await fetch(`/api${path}`, {
    ...init,
    headers: {
      Accept: "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers ?? {}),
    },
  });
  if (!resp.ok) {
    let msg = `${resp.status} ${resp.statusText}`;
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

export const OverviewSchema = z.object({
  product: z.string(),
  version: z.string(),
  status: z.array(FeatureStatusSchema),
  stats: z.unknown().optional(),
});
export type Overview = z.infer<typeof OverviewSchema>;

export const TLSProjectionSchema = z.object({
  enabled: z.boolean(),
  acme: z.boolean(),
  client_auth: z.string().optional(),
  min_version: z.string().optional(),
});
export type TLSProjection = z.infer<typeof TLSProjectionSchema>;

export const LocationProjectionSchema = z.object({
  match: z.string(),
  type: z.string(),
  action: z.string(),
  target: z.string().optional(),
  auth: z.boolean(),
  cache: z.boolean(),
  secure: z.boolean(),
});
export type LocationProjection = z.infer<typeof LocationProjectionSchema>;

export const RouteProjectionSchema = z.object({
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

export const SecurityProjectionSchema = z.object({
  auth_enabled: z.boolean(),
  client_auth: z.string().optional(),
  body_limit: z.string().optional(),
  require_cert_count: z.number(),
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

export const CacheProjectionSchema = z.object({
  enabled: z.boolean(),
  default_ttl: z.string().optional(),
  memory_max: z.string().optional(),
  disk_path: z.string().optional(),
});

export const TrafficControlsSchema = z.object({
  compression: CompressionProjectionSchema.optional(),
  rate_limit: RateLimitProjectionSchema.optional(),
  cache: CacheProjectionSchema.optional(),
});
export type TrafficControls = z.infer<typeof TrafficControlsSchema>;

// ── Query functions ──────────────────────────────────────────────────────────

export function fetchOverview(): Promise<Overview> {
  return api<unknown>("/runtime/overview").then((d) => OverviewSchema.parse(d));
}

export function fetchRoutes(): Promise<RouteProjection[]> {
  return api<unknown>("/routes").then((d) => z.array(RouteProjectionSchema).parse(d));
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
  return api<unknown>("/config/history").then((d) =>
    z.array(HistoryEntrySchema).parse(d),
  );
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

// ── Events (SSE) ──────────────────────────────────────────────────────────────

export interface SseEvent {
  type: string;
  time: string;
  data?: unknown;
}

/** Opens a GET /api/events SSE stream and returns a cleanup function. */
export function subscribeEvents(
  onEvent: (ev: SseEvent) => void,
  onError?: (err: Event) => void,
): () => void {
  const token = authToken.get();
  // EventSource doesn't support custom headers natively; for token auth we
  // pass it as a query param only when a token is configured (same-origin
  // admin, not sensitive over TLS). If no token, EventSource is sufficient.
  const url = token
    ? `/api/events?token=${encodeURIComponent(token)}`
    : "/api/events";
  const es = new EventSource(url);
  es.onmessage = (e: MessageEvent) => {
    try {
      const ev = JSON.parse(e.data as string) as SseEvent;
      onEvent(ev);
    } catch {
      // ignore malformed frames
    }
  };
  if (onError) es.onerror = onError;
  return () => es.close();
}
