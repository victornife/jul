/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConfigPatch, RouteProjection } from "@/api/client.ts";
import { AppEditor } from "@/features/apps/AppEditor.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";

const realFetch = globalThis.fetch;
let requestBody: { readonly base_version?: string; readonly ops: ConfigPatch[] } | null = null;

function route(overrides: Partial<RouteProjection> = {}): RouteProjection {
  return {
    listen: ":8080",
    server_names: ["b.example", "a.example"],
    http3: false,
    h2c: false,
    locations: [],
    ...overrides,
  };
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter initialEntries={["/apps"]}>{children}</MemoryRouter>;
}

beforeEach(() => {
  requestBody = null;
  takePendingDraft();
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch/preview");
    requestBody = JSON.parse(typeof init?.body === "string" ? init.body : "null") as {
      base_version?: string;
      ops: ConfigPatch[];
    };
    return Promise.resolve(
      json({
        ok: true,
        summary: "App batch previewed",
        operation_summaries: requestBody.ops.map((operation, opIndex) => ({
          op_index: opIndex,
          op: operation.op,
          summary: `operation ${String(opIndex)}`,
        })),
        diff: { summary: "App change" },
        base_version: "apps-v1",
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
  takePendingDraft();
});

describe("AppEditor ordered structured handoff", () => {
  it("hands off the exact upstream/health/discovery/existing-server batch", async () => {
    const onReview = vi.fn();
    render(
      <AppEditor
        initial={{
          name: "api",
          strategy: "weighted_round_robin",
          backends: [
            { address: "", weight: 9 },
            { address: "10.0.0.1:8080", weight: 2 },
            { address: "10.0.0.2:8080", weight: 3 },
          ],
        }}
        existingRoutes={[route(), route({ server_names: ["sibling.example"], locations: [] })]}
        onReview={onReview}
        onClose={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    fireEvent.click(screen.getByRole("checkbox", { name: "Enable active health checks" }));
    fireEvent.change(screen.getByRole("combobox", { name: "Discovery provider" }), {
      target: { value: "dns" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Target" }), {
      target: { value: "api.internal:8080" },
    });

    fireEvent.click(screen.getByRole("radio", { name: "Existing exact server" }));
    const serverOption = screen.getByRole<HTMLOptionElement>("option", {
      name: ":8080 · a.example, b.example",
    });
    fireEvent.change(screen.getByRole("combobox", { name: "Exact server identity" }), {
      target: { value: serverOption.value },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Route path" }), {
      target: { value: "/api" },
    });

    fireEvent.click(screen.getByRole("button", { name: "Review batch in editor →" }));

    await waitFor(() => {
      expect(requestBody).not.toBeNull();
    });
    expect(requestBody?.ops.map((operation) => operation.op)).toEqual([
      "upstream_add",
      "upstream_add_backend",
      "upstream_set_health_check",
      "upstream_set_discovery",
      "location_add",
    ]);
    expect(requestBody?.ops[0]).toEqual({
      op: "upstream_add",
      upstream: "api",
      address: "10.0.0.1:8080",
      weight: 2,
      strategy: "weighted_round_robin",
    });
    expect(requestBody?.ops[1]).toEqual({
      op: "upstream_add_backend",
      upstream: "api",
      address: "10.0.0.2:8080",
      weight: 3,
    });
    expect(requestBody?.ops[2]).toMatchObject({
      op: "upstream_set_health_check",
      upstream: "api",
      health_check: {
        enabled: true,
        type: "http",
        path: "/healthz",
        interval: "5s",
        timeout: "2s",
        healthy_threshold: 2,
        unhealthy_threshold: 3,
        expect_status: [200],
      },
    });
    expect(requestBody?.ops[3]).toEqual({
      op: "upstream_set_discovery",
      upstream: "api",
      discovery: { type: "dns", target: "api.internal:8080", refresh: "30s" },
    });
    expect(requestBody?.ops[4]).toEqual({
      op: "location_add",
      listen: ":8080",
      server_names: ["a.example", "b.example"],
      match_set: { type: "prefix", path: "/api" },
      action: { kind: "proxy", target: "http://api" },
    });
    expect(requestBody?.ops.some((operation) => operation.op === "server_add")).toBe(false);
    expect(onReview).toHaveBeenCalledWith("api");

    expect(takePendingDraft()).toMatchObject({
      kind: "patch",
      ops: requestBody?.ops,
      baseVersion: "apps-v1",
      summary: "App batch previewed",
      operationSummaries: requestBody?.ops.map((operation, opIndex) => ({
        op_index: opIndex,
        op: operation.op,
        summary: `operation ${String(opIndex)}`,
      })),
      valid: true,
      validationErrors: [],
      previewDiff: { summary: "App change" },
      lifecycle: {
        can_apply_hot: true,
        can_stage_restart: true,
        hot_paths: ["upstreams"],
        restart_required_paths: [],
      },
    });
    expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  });

  it("fails closed before preview when a new discovery token is required", async () => {
    render(
      <AppEditor
        initial={{ name: "secure-api", backends: [{ address: "127.0.0.1:8080", weight: 1 }] }}
        onClose={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    fireEvent.change(screen.getByRole("combobox", { name: "Discovery provider" }), {
      target: { value: "consul" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Service" }), {
      target: { value: "secure-api" },
    });
    fireEvent.click(
      screen.getByRole("checkbox", {
        name: "This new provider requires an authentication token",
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Review batch in editor →" }));

    expect(
      await screen.findByText(/typed workflow stopped without previewing or omitting it/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Open raw configuration editor →" }),
    ).toBeInTheDocument();
    expect(globalThis.fetch).not.toHaveBeenCalled();
    expect(takePendingDraft()).toBeNull();
  });

  it("preserves the operator draft after an authoritative preview rejection", async () => {
    globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
      expect(input).toBe("/api/config/patch/preview");
      requestBody = JSON.parse(typeof init?.body === "string" ? init.body : "null") as {
        base_version?: string;
        ops: ConfigPatch[];
      };
      return Promise.resolve(
        json(
          {
            ok: false,
            message: "an upstream named api already exists",
            errors: [],
            op_index: 0,
            op: "upstream_add",
          },
          400,
        ),
      );
    }) as unknown as typeof fetch;

    render(
      <AppEditor
        initial={{ name: "api", backends: [{ address: "127.0.0.1:8080", weight: 3 }] }}
        onClose={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    fireEvent.change(screen.getByRole("textbox", { name: "App / upstream name" }), {
      target: { value: "api-v2" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Address 1" }), {
      target: { value: "10.0.0.9:8080" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Review batch in editor →" }));

    expect(
      await screen.findByText(
        /Operation 0 \(upstream_add\) was rejected: an upstream named api already exists/i,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "App / upstream name" })).toHaveValue("api-v2");
    expect(screen.getByRole("textbox", { name: "Address 1" })).toHaveValue("10.0.0.9:8080");
    expect(requestBody?.ops).toEqual([
      {
        op: "upstream_add",
        upstream: "api-v2",
        address: "10.0.0.9:8080",
        weight: 3,
        strategy: "round_robin",
      },
    ]);
    expect(takePendingDraft()).toBeNull();
  });

  it("keeps upstream-only creation available when the Routes inventory is unavailable", async () => {
    render(
      <AppEditor
        initial={{ name: "worker", backends: [{ address: "127.0.0.1:9000", weight: 1 }] }}
        routeInventoryReady={false}
        onClose={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    expect(screen.getByRole("radio", { name: "Existing exact server" })).toBeDisabled();
    expect(screen.getByRole("radio", { name: "New exact server" })).toBeDisabled();
    expect(screen.getByText(/upstream-only creation remains available/i)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Review batch in editor →" }));
    await waitFor(() => {
      expect(requestBody?.ops).toEqual([
        {
          op: "upstream_add",
          upstream: "worker",
          address: "127.0.0.1:9000",
          weight: 1,
          strategy: "round_robin",
        },
      ]);
    });
  });
});
