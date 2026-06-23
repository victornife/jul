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
});
export type StatsSnapshot = z.infer<typeof StatsSnapshotSchema>;

export const OverviewSchema = z.object({
  product: z.string(),
  version: z.string(),
  status: z.array(FeatureStatusSchema),
  stats: StatsSnapshotSchema.optional(),
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

// ── Validate / Apply / Wizard (write flows) ──────────────────────────────────

export const ValidationIssueSchema = z.object({
  code: z.string(),
  path: z.string().optional(),
  summary: z.string(),
  detail: z.string().optional(),
  severity: z.string(),
});
export type ValidationIssue = z.infer<typeof ValidationIssueSchema>;

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
  status: z.array(FeatureStatusSchema),
});
export type ApplyResult = z.infer<typeof ApplyResultSchema>;

/**
 * Applies a candidate config through the authoritative write path and resolves
 * with the post-apply runtime status delta. Rejects with ConfigRejectedError
 * when the backend refuses the draft, or ApiError on transport failure.
 */
export async function applyConfig(candidate: string): Promise<ApplyResult> {
  const headers = new Headers();
  headers.set("Accept", "application/json");
  headers.set("Content-Type", "application/toml");
  const token = authToken.get();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const resp = await fetch("/api/config/apply", { method: "POST", headers, body: candidate });
  let data: unknown = null;
  try {
    data = (await resp.json()) as unknown;
  } catch {
    data = null;
  }
  if (!resp.ok) {
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

export const WizardInputSchema = z.object({
  mode: z.enum(["serve", "proxy"]),
  path: z.string().optional(),
  target: z.string().optional(),
  listen: z.string().optional(),
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
  void streamEvents(controller.signal, onEvent, onError);
  return () => {
    controller.abort();
  };
}

async function streamEvents(
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
      const resp = await fetch("/api/events", { headers, signal });
      if (resp.status === 401) {
        onError?.(new ApiError("/events", 401, "Unauthorized"));
        return; // auth won't recover on retry
      }
      if (!resp.ok || !resp.body) {
        throw new ApiError("/events", resp.status, `${String(resp.status)} ${resp.statusText}`);
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
