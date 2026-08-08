/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AppProjection, ConfigPatch } from "@/api/client.ts";
import { AppDetail } from "@/features/apps/AppDetail.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";

const realFetch = globalThis.fetch;
let requests: ConfigPatch[][] = [];
let rejectRemoval = false;

function app(overrides: Partial<AppProjection> = {}): AppProjection {
  return {
    name: "api",
    strategy: "round_robin",
    backends: [
      { address: "10.0.0.1:8080", weight: 1 },
      { address: "10.0.0.2:8080", weight: 2 },
    ],
    health_check: false,
    routes_using: [],
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
  requests = [];
  rejectRemoval = false;
  takePendingDraft();
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch/preview");
    const body = JSON.parse(typeof init?.body === "string" ? init.body : "null") as {
      ops: ConfigPatch[];
    };
    requests.push(body.ops);
    const operation = body.ops[0];
    if (rejectRemoval && operation?.op === "upstream_remove") {
      return Promise.resolve(
        json(
          {
            ok: false,
            message: 'upstream "api" is now referenced by :8080 /api',
            errors: [],
            op_index: 0,
            op: "upstream_remove",
          },
          400,
        ),
      );
    }
    return Promise.resolve(
      json({
        ok: true,
        summary: `${operation?.op ?? "App operation"} previewed`,
        operation_summaries: [
          {
            op_index: 0,
            op: operation?.op ?? "unknown",
            summary: "exact App operation",
          },
        ],
        diff: { summary: "1 App change" },
        base_version: "apps-detail-v1",
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

describe("AppDetail one-operation handoff", () => {
  it("adds a backend through the shared one-operation preview", async () => {
    render(<AppDetail app={app()} onClose={() => undefined} />, { wrapper: Wrapper });

    fireEvent.change(screen.getByPlaceholderText("10.0.0.2:8080"), {
      target: { value: "10.0.0.3:8080" },
    });
    const reviewButtons = screen.getAllByRole("button", { name: "Review →" });
    fireEvent.click(reviewButtons[reviewButtons.length - 1] as HTMLButtonElement);

    await waitFor(() => {
      expect(requests).toEqual([
        [
          {
            op: "upstream_add_backend",
            upstream: "api",
            address: "10.0.0.3:8080",
            weight: 1,
          },
        ],
      ]);
    });
    expect(takePendingDraft()).toMatchObject({
      kind: "patch",
      ops: requests[0],
      baseVersion: "apps-detail-v1",
      lifecycle: { can_apply_hot: true },
    });
  });

  it("removes one backend through the shared one-operation preview", async () => {
    render(<AppDetail app={app()} onClose={() => undefined} />, { wrapper: Wrapper });

    fireEvent.click(screen.getAllByRole("button", { name: "Remove →" })[0] as HTMLButtonElement);
    await waitFor(() => {
      expect(requests[0]).toEqual([
        {
          op: "upstream_remove_backend",
          upstream: "api",
          address: "10.0.0.1:8080",
        },
      ]);
    });
  });

  it("does not expose manual backend mutation for a discovery-owned pool", () => {
    render(
      <AppDetail
        app={app({ discovery: "dns", discovery_target: "api.internal:8080" })}
        onClose={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    expect(screen.queryByRole("button", { name: "Remove →" })).toBeNull();
    expect(screen.queryByText("Add backend (one operation)")).toBeNull();
  });

  it("keeps the last static backend removal disabled", () => {
    render(
      <AppDetail
        app={app({ backends: [{ address: "10.0.0.1:8080", weight: 1 }] })}
        onClose={() => undefined}
      />,
      { wrapper: Wrapper },
    );

    expect(screen.getByRole("button", { name: "Remove →" })).toBeDisabled();
    expect(screen.getByTitle("Cannot remove the last backend")).toBeInTheDocument();
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });
});

describe("AppDetail reference-aware deletion", () => {
  it("previews exactly one no-cascade removal and cancel performs no handoff", async () => {
    render(<AppDetail app={app()} onClose={() => undefined} />, { wrapper: Wrapper });

    fireEvent.click(screen.getByRole("button", { name: "Delete App / upstream…" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove App/upstream api?" });

    expect(requests).toEqual([[{ op: "upstream_remove", upstream: "api" }]]);
    expect(within(dialog).getByText(/0 projected routes/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/never cascades to routes, servers/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/Hot apply is available after review/i)).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog", { name: "Remove App/upstream api?" })).toBeNull();
    expect(takePendingDraft()).toBeNull();
    expect(requests).toHaveLength(1);
  });

  it("hands off the exact preview only after strong confirmation", async () => {
    render(<AppDetail app={app()} onClose={() => undefined} />, { wrapper: Wrapper });

    fireEvent.click(screen.getByRole("button", { name: "Delete App / upstream…" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove App/upstream api?" });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Hand off deletion for apply review" }),
    );

    expect(takePendingDraft()).toMatchObject({
      kind: "patch",
      ops: [{ op: "upstream_remove", upstream: "api" }],
      baseVersion: "apps-detail-v1",
      summary: "upstream_remove previewed",
      valid: true,
      lifecycle: { can_apply_hot: true, can_stage_restart: true },
    });
    expect(requests).toHaveLength(1);
  });

  it("blocks referenced deletion, lists bounded references, and emits no cascade", () => {
    const references = Array.from({ length: 10 }, (_, index) => `:8080 route-${String(index)}`);
    render(<AppDetail app={app({ routes_using: references })} onClose={() => undefined} />, {
      wrapper: Wrapper,
    });

    expect(screen.getByRole("button", { name: "Delete App / upstream…" })).toBeDisabled();
    expect(screen.getByText(/10 projected routes still reference this App/i)).toBeInTheDocument();
    expect(screen.getAllByText(":8080 route-0")).toHaveLength(2);
    expect(screen.getAllByText("…and 2 more")).toHaveLength(2);
    expect(
      screen.getByRole("link", { name: "Open Routes to repoint dependencies →" }),
    ).toHaveAttribute("href", "/routes");
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });

  it("surfaces an authoritative reference race with op index and discriminator", async () => {
    rejectRemoval = true;
    render(<AppDetail app={app()} onClose={() => undefined} />, { wrapper: Wrapper });

    fireEvent.click(screen.getByRole("button", { name: "Delete App / upstream…" }));
    expect(
      await screen.findByText(
        /Operation 0 \(upstream_remove\) was rejected: upstream "api" is now referenced/i,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "Remove App/upstream api?" })).toBeNull();
    expect(takePendingDraft()).toBeNull();
  });
});
