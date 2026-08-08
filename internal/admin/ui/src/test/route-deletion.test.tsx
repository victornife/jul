/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RouteDetail } from "@/features/routes/RouteDetail.tsx";
import { PermissionContext } from "@/auth/usePermission.ts";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { ConfigPatch, LocationProjection, RouteProjection } from "@/api/client.ts";

const realFetch = globalThis.fetch;
let requests: Array<{ url: string; body: unknown }> = [];
let lifecycleIncluded = true;

function location(
  match: string,
  action: LocationProjection["action"] = "proxy",
): LocationProjection {
  return {
    index: 0,
    match,
    type: "prefix",
    action,
    target: action === "proxy" || action === "grpc" ? "http://app" : undefined,
    auth: false,
    cache: false,
    compression: false,
    rate_limit: false,
    secure: false,
    require_client_cert: false,
  };
}

function route(action: LocationProjection["action"] = "proxy"): RouteProjection {
  return {
    listen: ":8443",
    server_names: ["b.example", "a.example"],
    http3: false,
    h2c: false,
    locations: [location("/api", action), location("/health", "return")],
  };
}

function firstLocation(projection: RouteProjection): LocationProjection {
  const selected = projection.locations[0];
  if (selected === undefined) throw new Error("expected a route location fixture");
  return selected;
}

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter>{children}</MemoryRouter>;
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
  requests = [];
  lifecycleIncluded = true;
  takePendingDraft();
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch/preview");
    const body = JSON.parse(typeof init?.body === "string" ? init.body : "null") as {
      ops: ConfigPatch[];
    };
    requests.push({ url: input, body });
    const op = body.ops[0];
    return Promise.resolve(
      json({
        ok: true,
        summary: `${op?.op ?? "delete"} previewed`,
        operation_summaries: [
          { op_index: 0, op: op?.op ?? "delete", summary: "exact deletion preview" },
        ],
        diff: { summary: "1 removal", removals: [{ kind: "route", name: "/api" }] },
        base_version: "v1",
        valid: true,
        validation_errors: [],
        ...(lifecycleIncluded
          ? {
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
            }
          : {}),
      }),
    );
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
  takePendingDraft();
});

describe("RouteDetail destructive route workflows", () => {
  it("previews an exact location_remove and cancel performs no handoff or apply", async () => {
    const projection = route();
    render(
      <RouteDetail
        route={projection}
        loc={firstLocation(projection)}
        onClose={() => undefined}
        onEdit={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    fireEvent.click(
      screen.getByRole("button", {
        name: /Delete route prefix \/api from :8443 · a\.example, b\.example/,
      }),
    );
    const dialog = await screen.findByRole("dialog", { name: "Remove this exact route?" });

    expect(requests).toHaveLength(1);
    expect(requests[0]?.body).toEqual({
      ops: [
        {
          op: "location_remove",
          listen: ":8443",
          server_names: ["a.example", "b.example"],
          match_type: "prefix",
          path: "/api",
        },
      ],
    });
    expect(within(dialog).getByText("prefix /api")).toBeInTheDocument();
    expect(within(dialog).getByText(/does not cascade to upstreams/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Hot apply is available/)).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog", { name: "Remove this exact route?" })).toBeNull();
    expect(takePendingDraft()).toBeNull();
    expect(requests).toHaveLength(1);
  });

  it("previews server_remove, shows contained routes, and hands off only after confirmation", async () => {
    const projection = route();
    render(
      <RouteDetail
        route={projection}
        loc={firstLocation(projection)}
        onClose={() => undefined}
        onEdit={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    fireEvent.click(
      screen.getByRole("button", {
        name: /Delete server :8443 · a\.example, b\.example with 2 contained routes/,
      }),
    );
    const dialog = await screen.findByRole("dialog", { name: "Remove this exact server?" });

    expect(requests[0]?.body).toEqual({
      ops: [
        {
          op: "server_remove",
          listen: ":8443",
          server_names: ["a.example", "b.example"],
        },
      ],
    });
    expect(within(dialog).getByText("2")).toBeInTheDocument();
    expect(within(dialog).getByText("prefix /api")).toBeInTheDocument();
    expect(within(dialog).getByText("prefix /health")).toBeInTheDocument();
    expect(within(dialog).getByText(/does not delete referenced upstreams/)).toBeInTheDocument();

    fireEvent.click(
      within(dialog).getByRole("button", { name: "Hand off deletion for apply review" }),
    );
    expect(takePendingDraft()).toMatchObject({
      kind: "patch",
      ops: [
        {
          op: "server_remove",
          listen: ":8443",
          server_names: ["a.example", "b.example"],
        },
      ],
      baseVersion: "v1",
    });
    expect(requests).toHaveLength(1);
  });

  it("disables confirmation when the preview lacks lifecycle classification", async () => {
    lifecycleIncluded = false;
    const projection = route();
    render(
      <RouteDetail
        route={projection}
        loc={firstLocation(projection)}
        onClose={() => undefined}
        onEdit={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    fireEvent.click(screen.getByRole("button", { name: /Delete route prefix \/api/ }));
    const dialog = await screen.findByRole("dialog", { name: "Remove this exact route?" });
    expect(
      within(dialog).getByRole("button", { name: "Hand off deletion for apply review" }),
    ).toBeDisabled();
    expect(within(dialog).getByText(/lacks an authoritative lifecycle/)).toBeInTheDocument();
  });

  it("gates deletion on config:write", () => {
    const projection = route();
    render(
      <RouteDetail
        route={projection}
        loc={firstLocation(projection)}
        onClose={() => undefined}
        onEdit={() => undefined}
      />,
      { wrapper: ForbiddenWrapper },
    );

    expect(screen.getByRole("button", { name: /Delete route prefix \/api/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Delete server :8443/ })).toBeDisabled();
    expect(
      screen.getAllByRole("note").some((note) => note.textContent.includes("config:write")),
    ).toBe(true);
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });

  it("does not offer an HTTP clone fallback for unsupported protocols", () => {
    const projection = route("grpc");
    render(
      <RouteDetail
        route={projection}
        loc={firstLocation(projection)}
        onClose={() => undefined}
        onEdit={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    expect(screen.queryByRole("button", { name: /Clone route/ })).toBeNull();
    expect(screen.getByText(/never substitutes a plain HTTP proxy/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open raw configuration/ })).toBeInTheDocument();
    const generated = screen.getByText(
      (content, element) =>
        element?.tagName === "PRE" && content.includes('proxy_pass = "http://app"'),
    );
    expect(generated).toHaveTextContent("grpc = true");
  });
});
