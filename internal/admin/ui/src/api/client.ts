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
