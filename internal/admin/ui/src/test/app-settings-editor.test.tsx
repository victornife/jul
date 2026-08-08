/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Component tests for the guided Apps settings editors. They mount the
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
import type { AppProjection, ConfigPatch } from "@/api/client.ts";

const realFetch = globalThis.fetch;
let seenBody = "";
let rejectedOp: ConfigPatch["op"] | null = null;

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
  rejectedOp = null;
  takePendingDraft(); // clear any leftover handoff state
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch/preview");
    seenBody = typeof init?.body === "string" ? init.body : "";
    const ops = (JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops;
    const first = ops[0];
    if (first !== undefined && rejectedOp === first.op) {
      return Promise.resolve(
        json(
          {
            ok: false,
            message: "authoritative settings rejection",
            errors: [],
            op_index: 0,
            op: first.op,
          },
          400,
        ),
      );
    }
    return Promise.resolve(
      json({
        ok: true,
        summary: "upstream api health check set",
        operation_summaries: ops.map((op, opIndex) => ({
          op_index: opIndex,
          op: op.op,
          summary: "upstream setting changed",
        })),
        diff: { summary: "1 change" },
        base_version: "deadbeef",
        valid: true,
        validation_errors: [],
        lifecycle: {
          changes: [],
          can_apply_hot: true,
          can_stage_restart: true,
          hot_paths: ["upstreams"],
          restart_required_paths: [],
          new_listener_only_paths: [],
          ignored_deprecated_paths: [],
          validation_rejected_paths: [],
          pending_subsystems: [],
        },
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
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
      op: "upstream_set_health_check",
      upstream: "api",
      health_check: { enabled: true, type: "http", path: "/healthz", interval: "5s" },
    });
    // The edit is staged for diff review, never applied directly.
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("preserves the health draft after an authoritative preview rejection", async () => {
    rejectedOp = "upstream_set_health_check";
    render(
      <Wrapper>
        <HealthCheckEditor
          app={app({
            health_check: true,
            health_check_type: "http",
            health_check_path: "/healthz",
          })}
          onClose={() => undefined}
        />
      </Wrapper>,
    );

    fireEvent.change(screen.getByRole("textbox", { name: "Path" }), {
      target: { value: "/still-here" },
    });
    fireEvent.click(screen.getByText("Review in editor →"));

    expect(
      await screen.findByText(
        /Operation 0 \(upstream_set_health_check\) was rejected: authoritative settings rejection/i,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Path" })).toHaveValue("/still-here");
    expect(takePendingDraft()).toBeNull();
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
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
      op: "upstream_set_discovery",
      upstream: "api",
      discovery: { type: "consul", consul: { service: "web" } },
    });
    expect(seenBody).not.toContain("token");
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("preserves the discovery draft after an authoritative preview rejection", async () => {
    rejectedOp = "upstream_set_discovery";
    render(
      <Wrapper>
        <DiscoveryEditor
          app={app({ discovery: "dns", discovery_target: "api.internal:8080" })}
          onClose={() => undefined}
        />
      </Wrapper>,
    );

    fireEvent.change(screen.getByRole("textbox", { name: /^Target/ }), {
      target: { value: "replacement.internal:8080" },
    });
    fireEvent.click(screen.getByText("Review in editor →"));

    expect(
      await screen.findByText(
        /Operation 0 \(upstream_set_discovery\) was rejected: authoritative settings rejection/i,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: /^Target/ })).toHaveValue(
      "replacement.internal:8080",
    );
    expect(takePendingDraft()).toBeNull();
  });
});
