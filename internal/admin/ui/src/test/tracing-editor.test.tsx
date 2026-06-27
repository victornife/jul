/**
 * Component test for the guided TracingEditor drawer (Phase 4d). It mounts the
 * editor, seeds it from the traffic-controls projection, and asserts that saving
 * fetches the raw config, upserts the [observability.tracing] table, and stages
 * a TOML draft for the Config editor — never writing directly.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { TracingEditor } from "@/features/traffic-controls/TracingEditor.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { TrafficControls } from "@/api/client.ts";

const realFetch = globalThis.fetch;

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter>{children}</MemoryRouter>;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  takePendingDraft(); // clear any leftover handoff state
  globalThis.fetch = vi.fn((input: string) => {
    expect(input).toBe("/api/config");
    return Promise.resolve(json({ raw: 'listen = ":8080"\n' }));
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("TracingEditor", () => {
  it("seeds from the projection and stages an [observability.tracing] TOML draft", async () => {
    const current: TrafficControls = {
      tracing: {
        enabled: true,
        exporter: "otlp-http",
        endpoint: "http://collector:4318",
        sample_ratio: 0.25,
        service_name: "edge",
        insecure: true,
      },
    };
    render(
      <Wrapper>
        <TracingEditor current={current} onClose={() => undefined} />
      </Wrapper>,
    );

    // Seeded from the projection: the collector endpoint is prefilled.
    expect(screen.getByDisplayValue("http://collector:4318")).toBeTruthy();

    fireEvent.click(screen.getByText("Review in editor →"));

    let staged: string | null = null;
    await waitFor(() => {
      const d = takePendingDraft();
      if (d?.kind === "toml") staged = d.toml;
      expect(staged).not.toBeNull();
    });

    // The edit is staged as a TOML draft for diff review, never applied directly.
    expect(staged).toContain("[observability.tracing]");
    expect(staged).toContain('exporter = "otlp-http"');
    expect(staged).toContain('endpoint = "http://collector:4318"');
    expect(staged).toContain("sample_ratio = 0.25");
    expect(staged).toContain('service_name = "edge"');
    expect(staged).toContain("insecure = true");
  });

  it("warns when enabled without a collector endpoint", () => {
    const current: TrafficControls = {
      tracing: { enabled: true, exporter: "otlp-grpc" },
    };
    render(
      <Wrapper>
        <TracingEditor current={current} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByText(/no collector endpoint is set/)).toBeInTheDocument();
  });
});
