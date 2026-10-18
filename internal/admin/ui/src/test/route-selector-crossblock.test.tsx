/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { RoutesPanel } from "@/features/routes/RoutesPanel.tsx";
import type { LocationProjection, RouteProjection } from "@/api/client.ts";
import { restoreRouteSelection, routeIdentityKey, serverIdentityFromRoute, storeRouteSelection } from "@/lib/routePatch.ts";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";

const loc = (ordinal: number): LocationProjection => ({ index: 0, match: "/", type: "prefix", action: "return", target: String(200 + ordinal), match_ordinal: ordinal, selector_version: "global-v1", auth: false, cache: false, compression: false, rate_limit: false, secure: false, require_client_cert: false });
const routes = (): RouteProjection[] => [0, 1].map((ordinal) => ({ base_version: "unchanged-raw", listen: ":8080", server_names: ["a.test"], http3: false, h2c: false, locations: [loc(ordinal)] }));
const response = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
afterEach(() => { vi.unstubAllGlobals(); sessionStorage.clear(); takePendingDraft(); });

describe("cross-block selectors", () => {
  it("restores the second block only in its exact projection revision", () => {
    const data = routes();
    const second = data[1];
    const selectedLoc = second?.locations[0];
    if (!second || !selectedLoc) throw new Error("missing fixture");
    const selected = { route: second, loc: selectedLoc };
    const stored = storeRouteSelection(selected, "unchanged-raw");
    expect(stored.version).toBe(4);
    expect(restoreRouteSelection(data, stored, "unchanged-raw")).toEqual(selected);
    expect(restoreRouteSelection(data, stored, "newer")).toBeNull();
    expect(restoreRouteSelection(data, stored)).toBeNull();
    expect(restoreRouteSelection(data.map((r) => ({ ...r, base_version: "older" })), stored, "unchanged-raw")).toBeNull();
    const server = serverIdentityFromRoute(second);
    expect(routeIdentityKey(server, { matchType: "prefix", path: "/", matchOrdinal: 0 })).not.toBe(routeIdentityKey(server, { matchType: "prefix", path: "/", matchOrdinal: 1 }));
  });

  for (const editor of ["predicates", "response headers", "CORS"]) {
    it(`selects block 2 and pins ${editor} edits to its published selector`, async () => {
      let body: { base_version: string; ops: Record<string, unknown>[] } | undefined;
      vi.stubGlobal("fetch", vi.fn((input: string, init?: RequestInit) => {
        if (input === "/api/routes") return Promise.resolve(response(routes()));
        if (input === "/api/config/patch/preview") {
          body = JSON.parse(typeof init?.body === "string" ? init.body : "{}") as typeof body;
          return Promise.resolve(response({ ok: true, summary: "shadowed edited", operation_summaries: [], diff: { summary: "1 change" }, base_version: "unchanged-raw", valid: true, validation_errors: [], lifecycle: { changes: [], can_apply_hot: true, can_stage_restart: true, hot_paths: [], restart_required_paths: [], new_listener_only_paths: [], ignored_deprecated_paths: [], validation_rejected_paths: [], pending_subsystems: [] } }));
        }
        return Promise.resolve(response({}));
      }));
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(<QueryClientProvider client={client}><MemoryRouter><RoutesPanel /></MemoryRouter></QueryClientProvider>);
      await screen.findAllByText("/");
      const secondPath = screen.getAllByText("/")[1];
      if (!secondPath) throw new Error("second block missing");
      fireEvent.click(secondPath);
      fireEvent.click(screen.getByRole("button", { name: `Add ${editor} →` }));
      if (editor === "predicates") {
        fireEvent.change(screen.getByLabelText(/Methods/), { target: { value: "GET" } });
      } else if (editor === "response headers") {
        fireEvent.click(screen.getByText("+ Add operation"));
        fireEvent.change(screen.getByLabelText("Row 1 header name"), { target: { value: "X-Shadowed" } });
        fireEvent.change(screen.getByLabelText("Row 1 value"), { target: { value: "yes" } });
      } else {
        fireEvent.change(screen.getByPlaceholderText(/app.example.test, https/i), { target: { value: "https://app.example.test" } });
      }
      fireEvent.click(screen.getByText("Review lifecycle and diff →"));
      await waitFor(() => { expect(body).toBeDefined(); });
      expect(body?.base_version).toBe("unchanged-raw");
      expect(body?.ops[0]).toMatchObject({ listen: ":8080", server_names: ["a.test"], match_type: "prefix", path: "/", match_ordinal: 1, selector_version: "global-v1" });
      client.clear();
    });
  }
});
