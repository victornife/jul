import type { AppProjection, DiscoveryPatch, HealthCheckPatch } from "@/api/client.ts";

// appSettings holds the pure draft <-> patch mapping and warning logic for the
// guided Apps editor (Phase 4b): active health checks and dynamic discovery.
// Keeping it free of React makes the round-trip and validation directly unit
// testable, mirroring tracingToml.ts. Secret tokens never appear here — the
// backend preserves them when the provider type is unchanged.

export type HealthCheckType = "http" | "tcp";

export interface HealthCheckDraft {
  enabled: boolean;
  type: HealthCheckType;
  path: string;
  interval: string;
  timeout: string;
  healthyThreshold: string;
  unhealthyThreshold: string;
  expectStatus: string;
  expectBody: string;
}

export function seedHealthCheck(app: AppProjection): HealthCheckDraft {
  return {
    enabled: app.health_check,
    type: app.health_check_type === "tcp" ? "tcp" : "http",
    path: app.health_check_path ?? "",
    interval: app.health_check_interval ?? "",
    timeout: app.health_check_timeout ?? "",
    healthyThreshold: app.health_check_healthy_threshold
      ? String(app.health_check_healthy_threshold)
      : "",
    unhealthyThreshold: app.health_check_unhealthy_threshold
      ? String(app.health_check_unhealthy_threshold)
      : "",
    expectStatus: (app.health_check_expect_status ?? []).join(", "),
    expectBody: app.health_check_expect_body ?? "",
  };
}

function parseIntList(s: string): number[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter((x) => x.length > 0)
    .map((x) => Number(x))
    .filter((n) => Number.isInteger(n));
}

export function healthCheckToPatch(d: HealthCheckDraft): HealthCheckPatch {
  if (!d.enabled) return { enabled: false };
  const status = parseIntList(d.expectStatus);
  const healthy = Number(d.healthyThreshold);
  const unhealthy = Number(d.unhealthyThreshold);
  return {
    enabled: true,
    type: d.type,
    ...(d.path.trim() ? { path: d.path.trim() } : {}),
    ...(d.interval.trim() ? { interval: d.interval.trim() } : {}),
    ...(d.timeout.trim() ? { timeout: d.timeout.trim() } : {}),
    ...(d.healthyThreshold.trim() && Number.isInteger(healthy)
      ? { healthy_threshold: healthy }
      : {}),
    ...(d.unhealthyThreshold.trim() && Number.isInteger(unhealthy)
      ? { unhealthy_threshold: unhealthy }
      : {}),
    ...(status.length > 0 ? { expect_status: status } : {}),
    ...(d.type === "http" && d.expectBody.trim() ? { expect_body: d.expectBody.trim() } : {}),
  };
}

export function healthCheckWarnings(d: HealthCheckDraft): string[] {
  const w: string[] = [];
  if (!d.enabled) return w;
  if (d.type === "http" && d.path.trim() === "") {
    w.push("HTTP probes need a request path (e.g. /healthz).");
  }
  for (const code of parseIntList(d.expectStatus)) {
    if (code < 100 || code > 599) {
      w.push(`Expected status ${String(code)} is out of range (want 100–599).`);
    }
  }
  return w;
}

export type DiscoveryKind = "static" | "dns" | "dns_srv" | "consul" | "kubernetes";

const DISCOVERY_KINDS: readonly DiscoveryKind[] = [
  "static",
  "dns",
  "dns_srv",
  "consul",
  "kubernetes",
];

export interface DiscoveryDraft {
  type: DiscoveryKind;
  target: string;
  refresh: string;
  consulAddress: string;
  consulService: string;
  consulTag: string;
  consulDatacenter: string;
  consulPassingOnly: boolean;
  k8sNamespace: string;
  k8sService: string;
  k8sPort: string;
  k8sApiServer: string;
  k8sCaFile: string;
  k8sInsecure: boolean;
  // hasToken is display-only: a secret token is configured and will be
  // preserved unchanged. It is never sent back.
  hasToken: boolean;
}

export function seedDiscovery(app: AppProjection): DiscoveryDraft {
  const raw = app.discovery ?? "static";
  const type: DiscoveryKind = (DISCOVERY_KINDS as readonly string[]).includes(raw)
    ? (raw as DiscoveryKind)
    : "static";
  const c = app.discovery_consul;
  const k = app.discovery_kubernetes;
  return {
    type,
    target: app.discovery_target ?? "",
    refresh: app.discovery_refresh ?? "",
    consulAddress: c?.address ?? "",
    consulService: c?.service ?? "",
    consulTag: c?.tag ?? "",
    consulDatacenter: c?.datacenter ?? "",
    consulPassingOnly: c?.passing_only ?? true,
    k8sNamespace: k?.namespace ?? "",
    k8sService: k?.service ?? "",
    k8sPort: k?.port ?? "",
    k8sApiServer: k?.api_server ?? "",
    k8sCaFile: k?.ca_file ?? "",
    k8sInsecure: k?.insecure_skip_tls_verify ?? false,
    hasToken: (c?.has_token ?? false) || (k?.has_token ?? false),
  };
}

export function discoveryToPatch(d: DiscoveryDraft): DiscoveryPatch {
  if (d.type === "static") return { type: "static" };
  const patch: DiscoveryPatch = {
    type: d.type,
    ...(d.target.trim() ? { target: d.target.trim() } : {}),
    ...(d.refresh.trim() ? { refresh: d.refresh.trim() } : {}),
  };
  if (d.type === "consul") {
    patch.consul = {
      ...(d.consulAddress.trim() ? { address: d.consulAddress.trim() } : {}),
      ...(d.consulService.trim() ? { service: d.consulService.trim() } : {}),
      ...(d.consulTag.trim() ? { tag: d.consulTag.trim() } : {}),
      ...(d.consulDatacenter.trim() ? { datacenter: d.consulDatacenter.trim() } : {}),
      passing_only: d.consulPassingOnly,
    };
  }
  if (d.type === "kubernetes") {
    patch.kubernetes = {
      ...(d.k8sNamespace.trim() ? { namespace: d.k8sNamespace.trim() } : {}),
      ...(d.k8sService.trim() ? { service: d.k8sService.trim() } : {}),
      ...(d.k8sPort.trim() ? { port: d.k8sPort.trim() } : {}),
      ...(d.k8sApiServer.trim() ? { api_server: d.k8sApiServer.trim() } : {}),
      ...(d.k8sCaFile.trim() ? { ca_file: d.k8sCaFile.trim() } : {}),
      insecure_skip_tls_verify: d.k8sInsecure,
    };
  }
  return patch;
}

export function discoveryWarnings(d: DiscoveryDraft): string[] {
  const w: string[] = [];
  switch (d.type) {
    case "dns":
      if (d.target.trim() === "") w.push("DNS discovery needs a target host:port.");
      else if (!/:\d+$/.test(d.target.trim()))
        w.push("DNS target must be host:port (A/AAAA records carry no port).");
      break;
    case "dns_srv":
      if (d.target.trim() === "") w.push("SRV discovery needs the SRV name as the target.");
      break;
    case "consul":
      if (d.consulService.trim() === "") w.push("Consul discovery needs a service name.");
      if (d.consulAddress.trim() !== "" && !/^https?:\/\//.test(d.consulAddress.trim()))
        w.push("Consul address must be an http(s) URL.");
      break;
    case "kubernetes":
      if (d.k8sNamespace.trim() === "" || d.k8sService.trim() === "")
        w.push("Kubernetes discovery needs a namespace and a service.");
      if (d.k8sApiServer.trim() !== "" && !/^https?:\/\//.test(d.k8sApiServer.trim()))
        w.push("Kubernetes api_server must be an http(s) URL.");
      break;
    default:
      break;
  }
  return w;
}

// discoveryTokenNote surfaces the informational "a secret token is configured
// and will be kept" message. It is intentionally separate from the blocking
// warnings so an edit to a token-bearing provider can still be saved.
export function discoveryTokenNote(d: DiscoveryDraft): string | null {
  if ((d.type === "consul" || d.type === "kubernetes") && d.hasToken) {
    return "A secret token is configured for this provider; it is preserved unchanged.";
  }
  return null;
}

