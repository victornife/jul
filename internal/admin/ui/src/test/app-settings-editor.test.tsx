/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Component tests for the guided Apps editors (Phase 4b). They mount the
 * HealthCheck and Discovery drawers, seed them from the Apps projection, and
 * assert that saving posts the structured patch op and stages the resulting
 * diff for the Config editor — never writing directly, and never sending a
 * secret token.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { HealthCheckEditor, DiscoveryEditor } from "@/features/apps/AppSettingsEditor.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { AppProjection } from "@/api/client.ts";

const realFetch = globalThis.fetch;
let seenBody = "";

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter>{children}</MemoryRouter>;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function app(over: Partial<AppProjection> = {}): AppProjection {
  return {
    name: "api",
    strategy: "round_robin",
    backends: [],
    health_check: false,
    ...over,
  };
}

beforeEach(() => {
  seenBody = "";
  takePendingDraft(); // clear any leftover handoff state
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch");
    seenBody = typeof init?.body === "string" ? init.body : "";
    return Promise.resolve(
      json({
        ok: true,
        summary: "upstream api health check set",
        candidate: 'listen = ":8080"\n',
        diff: { summary: "1 change" },
        base_version: "deadbeef",
      }),
    );
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("HealthCheckEditor", () => {
  it("seeds from the projection and posts an upstream_set_health_check patch", async () => {
    render(
      <Wrapper>
        <HealthCheckEditor
          app={app({
            health_check: true,
            health_check_type: "http",
            health_check_path: "/healthz",
            health_check_interval: "5s",
          })}
          onClose={() => undefined}
        />
      </Wrapper>,
    );

    // Seeded from the projection: the probe path is prefilled.
    expect(screen.getByDisplayValue("/healthz")).toBeTruthy();

    fireEvent.click(screen.getByText("Review in editor →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "upstream_set_health_check",
      upstream: "api",
      health_check: { enabled: true, type: "http", path: "/healthz", interval: "5s" },
    });
    // The edit is staged for diff review, never applied directly.
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("blocks save while an enabled http probe has no path", () => {
    render(
      <Wrapper>
        <HealthCheckEditor
          app={app({ health_check: true, health_check_type: "http" })}
          onClose={() => undefined}
        />
      </Wrapper>,
    );
    expect(screen.getByText(/request path/)).toBeInTheDocument();
    expect(screen.getByText("Review in editor →")).toBeDisabled();
  });
});

describe("DiscoveryEditor", () => {
  it("posts an upstream_set_discovery patch without ever sending a token", async () => {
    render(
      <Wrapper>
        <DiscoveryEditor
          app={app({
            discovery: "consul",
            discovery_consul: { service: "web", has_token: true },
          })}
          onClose={() => undefined}
        />
      </Wrapper>,
    );

    // Seeded from the projection: the Consul service is prefilled.
    expect(screen.getByDisplayValue("web")).toBeTruthy();
    // A configured token is surfaced as a preserved-unchanged notice.
    expect(screen.getByText(/preserved unchanged/)).toBeInTheDocument();

    fireEvent.click(screen.getByText("Review in editor →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "upstream_set_discovery",
      upstream: "api",
      discovery: { type: "consul", consul: { service: "web" } },
    });
    expect(seenBody).not.toContain("token");
    expect(takePendingDraft()?.kind).toBe("patch");
  });
});
