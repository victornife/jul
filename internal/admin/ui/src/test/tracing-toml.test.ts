import { describe, it, expect } from "vitest";
import {
  emptyTracingDraft,
  generateTracingToml,
  tracingWarnings,
  type TracingDraft,
} from "@/lib/tracingToml.ts";

function draft(over: Partial<TracingDraft>): TracingDraft {
  return { ...emptyTracingDraft(), ...over };
}

describe("generateTracingToml", () => {
  it("defaults to the otlp-grpc exporter with full sampling over TLS", () => {
    const toml = generateTracingToml(draft({ endpoint: "localhost:4317" }));
    expect(toml).toContain("[observability.tracing]");
    expect(toml).toContain("enabled = true");
    expect(toml).toContain('exporter = "otlp-grpc"');
    expect(toml).toContain('endpoint = "localhost:4317"');
    // Full sampling (1.0) is the server default, so it is omitted.
    expect(toml).not.toContain("sample_ratio");
    expect(toml).not.toContain("insecure");
  });

  it("collapses to a single disabled line when disabled", () => {
    expect(generateTracingToml(draft({ enabled: false }))).toBe(
      "[observability.tracing]\nenabled = false",
    );
  });

  it("emits a fractional sample ratio but omits 0 and 1", () => {
    expect(generateTracingToml(draft({ endpoint: "h:1", sampleRatio: 0.1 }))).toContain(
      "sample_ratio = 0.1",
    );
    expect(generateTracingToml(draft({ endpoint: "h:1", sampleRatio: 1 }))).not.toContain(
      "sample_ratio",
    );
    expect(generateTracingToml(draft({ endpoint: "h:1", sampleRatio: 0 }))).not.toContain(
      "sample_ratio",
    );
  });

  it("emits service_name and insecure only when set", () => {
    const toml = generateTracingToml(
      draft({ endpoint: "h:1", serviceName: "edge", insecure: true }),
    );
    expect(toml).toContain('service_name = "edge"');
    expect(toml).toContain("insecure = true");
  });

  it("omits the endpoint when blank so the server reports the missing value", () => {
    expect(generateTracingToml(draft({ endpoint: "" }))).not.toContain("endpoint");
  });
});

describe("tracingWarnings", () => {
  it("returns nothing when tracing is disabled", () => {
    expect(tracingWarnings(draft({ enabled: false }))).toEqual([]);
  });

  it("warns when enabled without an endpoint", () => {
    const w = tracingWarnings(draft({ endpoint: "" }));
    expect(w.some((m) => m.includes("no collector endpoint"))).toBe(true);
  });

  it("warns about insecure transport and the otel build tag", () => {
    const w = tracingWarnings(draft({ endpoint: "h:1", insecure: true }));
    expect(w.some((m) => m.includes("plaintext"))).toBe(true);
    expect(w.some((m) => m.includes("otel"))).toBe(true);
  });
});
