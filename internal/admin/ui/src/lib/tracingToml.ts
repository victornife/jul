// Client-side generator for the guided tracing editor (Phase 4d). It produces
// the global [observability.tracing] table, which the editor upserts into the
// running config and routes through the authoritative Validate → Diff → Apply →
// Rollback pipeline; it never writes directly.
//
// Tracing governs what telemetry leaves the process and where it is sent. The
// editor defaults to full sampling and TLS transport, warns when an endpoint is
// missing (the server rejects an enabled block without one), and warns when the
// insecure (plaintext) transport is selected.

export type TracingExporter = "otlp-grpc" | "otlp-http";

export interface TracingDraft {
  enabled: boolean;
  exporter: TracingExporter;
  endpoint: string; // collector address; "host:port" for gRPC, URL/host for HTTP
  sampleRatio: number; // head-based sampling probability in [0,1]
  serviceName: string; // OpenTelemetry resource service.name
  insecure: boolean; // send spans over plaintext instead of TLS
}

export function emptyTracingDraft(): TracingDraft {
  return {
    enabled: false,
    exporter: "otlp-grpc",
    endpoint: "",
    sampleRatio: 1,
    serviceName: "jul",
    insecure: false,
  };
}

function tomlString(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

// formatRatio renders a sampling probability as a valid TOML float. Values are
// only emitted in [0,1); the parser treats a zero as "unset" and defaults to
// full sampling, so a fraction such as 0.1 is the meaningful case.
function formatRatio(n: number): string {
  const clamped = Math.min(1, Math.max(0, n));
  return Number.isInteger(clamped) ? `${String(clamped)}.0` : String(clamped);
}

// tracingWarnings reports human-readable risks before the operator opens the
// diff. The server validates authoritatively; these are near-side hints.
export function tracingWarnings(d: TracingDraft): string[] {
  const w: string[] = [];
  if (!d.enabled) return w;
  if (!d.endpoint.trim()) {
    w.push(
      "Tracing is enabled but no collector endpoint is set; the server rejects an enabled block without one.",
    );
  }
  if (d.insecure) {
    w.push(
      "Insecure mode sends spans over plaintext instead of TLS; only use it for a local collector on a trusted network.",
    );
  }
  if (d.sampleRatio <= 0) {
    w.push(
      "A sample ratio of 0 is treated as unset and falls back to full sampling; set a fraction such as 0.1 to sample less.",
    );
  }
  w.push("Tracing is only active in binaries built with the `otel` build tag.");
  return w;
}

/** Generates the global [observability.tracing] table for the tracing editor. */
export function generateTracingToml(d: TracingDraft): string {
  const lines: string[] = ["[observability.tracing]"];
  lines.push(`enabled = ${d.enabled ? "true" : "false"}`);
  if (!d.enabled) {
    return lines.join("\n");
  }
  lines.push(`exporter = ${tomlString(d.exporter)}`);
  if (d.endpoint.trim()) {
    lines.push(`endpoint = ${tomlString(d.endpoint.trim())}`);
  }
  // Only emit a fraction; full sampling (1.0) is the server default, so omit it
  // to keep the config minimal and round-trip a default-sampled block.
  if (d.sampleRatio > 0 && d.sampleRatio < 1) {
    lines.push(`sample_ratio = ${formatRatio(d.sampleRatio)}`);
  }
  if (d.serviceName.trim()) {
    lines.push(`service_name = ${tomlString(d.serviceName.trim())}`);
  }
  if (d.insecure) {
    lines.push(`insecure = true`);
  }
  return lines.join("\n");
}
