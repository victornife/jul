/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RouteEditor } from "@/features/routes/RouteEditor.tsx";
import { PermissionContext } from "@/auth/usePermission.ts";
import { emptyAuthDraft } from "@/lib/routeToml.ts";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { ConfigPatch, RouteProjection } from "@/api/client.ts";

const realFetch = globalThis.fetch;
let seenBody: unknown = null;

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

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function previewResponse(ops: readonly ConfigPatch[]) {
  return {
    ok: true,
    summary: `${String(ops.length)} operations`,
    operation_summaries: ops.map((op, opIndex) => ({
      op_index: opIndex,
      op: op.op,
      summary: op.op,
    })),
    diff: { summary: `${String(ops.length)} changes` },
    base_version: "v1",
    valid: true,
    validation_errors: [],
    lifecycle: {
      changes: [],
      can_apply_hot: true,
      can_stage_restart: true,
      hot_paths: ["servers.locations"],
      restart_required_paths: [],
      new_listener_only_paths: [],
      ignored_deprecated_paths: [],
      validation_rejected_paths: [],
      pending_subsystems: [],
    },
  };
}

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter>{children}</MemoryRouter>;
}

function WriteOnlyWrapper({ children }: { readonly children: ReactNode }) {
  return (
    <PermissionContext.Provider
      value={{
        identity: {
          principal: "route-author",
          role: "custom",
          token_id: "",
          permissions: ["config:write"],
          legacy: false,
        },
        isLoading: false,
        ready: true,
        has: (permission) => permission === "config:write",
      }}
    >
      <MemoryRouter>{children}</MemoryRouter>
    </PermissionContext.Provider>
  );
}

function ForbiddenWrapper({ children }: { readonly children: ReactNode }) {
  return (
    <PermissionContext.Provider
      value={{
        identity: {
          principal: "reader",
          role: "viewer",
          token_id: "",
          permissions: [],
          legacy: false,
        },
        isLoading: false,
        ready: true,
        has: () => false,
      }}
    >
      <MemoryRouter>{children}</MemoryRouter>
    </PermissionContext.Provider>
  );
}

beforeEach(() => {
  seenBody = null;
  takePendingDraft();
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch/preview");
    seenBody = JSON.parse(typeof init?.body === "string" ? init.body : "null");
    const ops = (seenBody as { ops: ConfigPatch[] }).ops;
    return Promise.resolve(json(previewResponse(ops)));
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
  takePendingDraft();
});

describe("RouteEditor ordered batch creation", () => {
  it("does not offer native gRPC through the generic structured creator", () => {
    render(<RouteEditor existingRoutes={[route()]} onClose={() => undefined} />, {
      wrapper: Wrapper,
    });

    const action = screen.getByRole("combobox", { name: /^Structured action/ });
    expect(within(action).queryByRole("option", { name: /Native gRPC/i })).toBeNull();
    expect(screen.getByText(/protocol-specific workflow or the raw editor/i)).toBeInTheDocument();
  });
  it("adds to one exact existing server without emitting server_add", async () => {
    const onReview = vi.fn();
    render(
      <RouteEditor
        existingRoutes={[route()]}
        initial={{
          path: "/api",
          matchType: "prefix",
          action: "proxy",
          target: "http://app",
          auth: {
            ...emptyAuthDraft(),
            method: "basic",
            basicFile: "/etc/jul/htpasswd",
          },
          cache: true,
          rateLimit: true,
          plugins: "request-id, security-headers",
        }}
        onReview={onReview}
        onClose={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    fireEvent.click(screen.getByRole("button", { name: "Review lifecycle and diff →" }));
    await waitFor(() => {
      expect(seenBody).not.toBeNull();
    });

    const ops = (seenBody as { ops: ConfigPatch[] }).ops;
    expect(ops.map((op) => op.op)).toEqual([
      "location_add",
      "location_set_auth",
      "route_toggle_cache",
      "route_set_rate_limit",
      "location_attach_plugin",
      "location_attach_plugin",
    ]);
    expect(ops).not.toContainEqual(expect.objectContaining({ op: "server_add" }));
    expect(ops[0]).toMatchObject({
      op: "location_add",
      listen: ":8080",
      server_names: ["a.example", "b.example"],
      match_set: { type: "prefix", path: "/api" },
      action: { kind: "proxy", target: "http://app" },
    });
    expect(ops.slice(-2)).toEqual([
      expect.objectContaining({ op: "location_attach_plugin", plugin_name: "request-id" }),
      expect.objectContaining({ op: "location_attach_plugin", plugin_name: "security-headers" }),
    ]);
    expect(onReview).toHaveBeenCalledTimes(1);
    expect(onReview).toHaveBeenCalledWith({
      version: 2,
      server: { listen: ":8080", server_names: ["a.example", "b.example"] },
      location: { match_type: "prefix", path: "/api" },
      base_version: "v1",
    });
    expect(takePendingDraft()).toMatchObject({ kind: "patch", ops });
  });

  it("creates a new server with server_add first and location_add second", async () => {
    render(
      <RouteEditor
        existingRoutes={[route({ listen: ":8080", server_names: [] })]}
        initial={{
          listen: ":9443",
          serverNames: "z.example, A.example",
          path: "/health",
          matchType: "exact",
          action: "return",
          target: "204",
          auth: emptyAuthDraft(),
          cache: false,
          rateLimit: false,
          plugins: "audit",
        }}
        onClose={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    expect(screen.getByRole("radio", { name: /Create a new server/ })).toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: "Review lifecycle and diff →" }));
    await waitFor(() => {
      expect(seenBody).not.toBeNull();
    });

    const ops = (seenBody as { ops: ConfigPatch[] }).ops;
    expect(ops.map((op) => op.op)).toEqual([
      "server_add",
      "location_add",
      "location_attach_plugin",
    ]);
    expect(ops[0]).toEqual({
      op: "server_add",
      listen: ":9443",
      server_names: ["A.example", "z.example"],
    });
    expect(ops[1]).toMatchObject({
      op: "location_add",
      listen: ":9443",
      server_names: ["A.example", "z.example"],
      match_set: { type: "exact", path: "/health" },
      action: { kind: "return", status: 204 },
    });
  });

  it("rejects an exact new-server identity collision instead of switching modes", () => {
    render(
      <RouteEditor existingRoutes={[route({ server_names: [] })]} onClose={() => undefined} />,
      {
        wrapper: Wrapper,
      },
    );

    fireEvent.click(screen.getByRole("radio", { name: /Create a new server/ }));
    expect(
      screen.getByText(/A server with this exact identity already exists/),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Review lifecycle and diff →" })).toBeDisabled();
    expect(screen.getByRole("radio", { name: /Create a new server/ })).toBeChecked();
  });

  it("permits preview with config:write alone", async () => {
    render(
      <RouteEditor
        existingRoutes={[route()]}
        initial={{ path: "/write-only", action: "deny", target: "" }}
        onClose={() => undefined}
      />,
      { wrapper: WriteOnlyWrapper },
    );

    fireEvent.click(screen.getByRole("button", { name: "Review lifecycle and diff →" }));
    await waitFor(() => {
      expect(seenBody).toEqual({
        ops: [
          {
            op: "location_add",
            listen: ":8080",
            server_names: ["a.example", "b.example"],
            match_set: { type: "prefix", path: "/write-only" },
            action: { kind: "deny" },
          },
        ],
      });
    });
  });

  it("disables preview and explains config:write permission", () => {
    render(
      <RouteEditor
        existingRoutes={[route()]}
        initial={{ path: "/api", action: "deny", target: "" }}
        onClose={() => undefined}
      />,
      { wrapper: ForbiddenWrapper },
    );

    expect(screen.getByRole("button", { name: "Review lifecycle and diff →" })).toBeDisabled();
    expect(screen.getByRole("note")).toHaveTextContent("config:write");
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });
});
