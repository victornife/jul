/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { RoutesPanel } from "@/features/routes/RoutesPanel.tsx";
import { LocationRateLimitEditor } from "@/features/routes/LocationRateLimitEditor.tsx";
import { TranscodeQuickEdit } from "@/features/routes/TranscodeQuickEdit.tsx";
import type { LocationProjection, RouteProjection } from "@/api/client.ts";
import { restoreRouteSelection, routeIdentityKey, routeTargetFromProjection, serverIdentityFromRoute, storeRouteSelection } from "@/lib/routePatch.ts";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";

const loc = (ordinal: number): LocationProjection => ({ index: 0, match: "/", type: "prefix", action: "return", target: String(200 + ordinal), match_ordinal: ordinal, selector_version: "global-v1", auth: false, cache: false, compression: false, rate_limit: false, secure: false, require_client_cert: false });
const routes = (): RouteProjection[] => [0, 1].map((ordinal) => ({ base_version: "unchanged-raw", listen: ":8080", server_names: ["a.test"], http3: false, h2c: false, locations: [loc(ordinal)] }));
const response = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
afterEach(() => { vi.unstubAllGlobals(); sessionStorage.clear(); takePendingDraft(); });

describe("cross-block selectors", () => {
  it("echoes zero ordinals and preserves absent legacy coordinates without inventing a selector", () => {
    const route = routes()[0];
    if (!route) throw new Error("missing fixture");
    expect(routeTargetFromProjection(route, loc(0))).toMatchObject({ match_ordinal: 0, selector_version: "global-v1" });
    const unnamed = { ...route };
    delete unnamed.server_names;
    const legacy = loc(0);
    delete legacy.match_ordinal;
    delete legacy.selector_version;
    expect(routeTargetFromProjection(unnamed, legacy)).toEqual({ listen: ":8080", server_names: [], match_type: "prefix", path: "/" });
  });

  it("refuses malformed, missing and ambiguous retained v4 targets", () => {
    const data = routes();
    const route = data[1];
    const location = route?.locations[0];
    if (!route || !location) throw new Error("missing fixture");
    const stored = storeRouteSelection({ route, loc: location }, "unchanged-raw");
    if (stored.version !== 4) throw new Error("expected v4 fixture");
    const invalid = [
      { ...stored, server: null }, { ...stored, location: null },
      { ...stored, server: { ...stored.server, listen: 1 } },
      { ...stored, base_version: 1 }, { ...stored, base_version: "" },
      { ...stored, selector_version: 1 },
      ...[{ match_type: 1 }, { path: 1 }, { match_ordinal: "1" }, { match_ordinal: 1.5 }, { match_ordinal: -1 }].map((change) => ({ ...stored, location: { ...stored.location, ...change } })),
      { ...stored, server: { ...stored.server, server_names: [1] } },
      { ...stored, server: { ...stored.server, listen: ":9090" } },
      { ...stored, location: { ...stored.location, match_type: "exact" } },
      { ...stored, location: { ...stored.location, path: "/removed" } },
      { ...stored, location: { ...stored.location, match_ordinal: 2 } },
      { ...stored, selector_version: "unknown" },
    ];
    for (const value of invalid) expect(restoreRouteSelection(data, value, "unchanged-raw")).toBeNull();
    expect(restoreRouteSelection([], stored, "unchanged-raw")).toBeNull();
    expect(restoreRouteSelection([...data, route], stored, "unchanged-raw")).toBeNull();
  });

  for (const mode of ["enable rate limit", "disable rate limit", "descriptor transcode", "reflection transcode"]) {
    it(`pins ${mode} preview to the second block and its reviewed revision`, async () => {
      let body: { base_version: string; ops: Record<string, unknown>[] } | undefined;
      vi.stubGlobal("fetch", vi.fn((input: string, init?: RequestInit) => {
        expect(input).toBe("/api/config/patch/preview");
        body = JSON.parse(typeof init?.body === "string" ? init.body : "{}") as typeof body;
        return Promise.resolve(response({ ok: true, base_version: "unchanged-raw", valid: true, validation_errors: [], diff: { summary: "1 change" }, lifecycle: { changes: [], can_apply_hot: true } }));
      }));
      const route = routes()[1];
      if (!route) throw new Error("missing fixture");
      const location = { ...loc(1), target: "backend:50051", transcode: { descriptor_set: "/old.pb" } };
      if (mode.endsWith("rate limit")) {
        render(<MemoryRouter><LocationRateLimitEditor route={route} loc={{ ...location, ...(mode.startsWith("disable") ? { rate_limit_detail: { enabled: true, rate: 50, burst: 100, key: "ip" } } : {}) }} onClose={() => undefined} /></MemoryRouter>);
        fireEvent.click(screen.getByLabelText("Enable rate limiting"));
        fireEvent.click(screen.getByRole("button", { name: "Preview change →" }));
      } else {
        render(<MemoryRouter><TranscodeQuickEdit route={route} loc={location} /></MemoryRouter>);
        if (mode.startsWith("reflection")) {
          fireEvent.click(screen.getByLabelText("Use server reflection instead of descriptor file"));
          fireEvent.click(screen.getByLabelText("Streaming"));
          fireEvent.change(screen.getByRole("combobox"), { target: { value: "sse" } });
          fireEvent.change(screen.getByPlaceholderText("e.g. 4m"), { target: { value: " 8m " } });
        } else {
          fireEvent.change(screen.getByPlaceholderText("/path/to/descriptor_set.pb"), { target: { value: " /new.pb " } });
        }
        fireEvent.click(screen.getByRole("button", { name: "Save transcode settings →" }));
      }
      await waitFor(() => { expect(body).toBeDefined(); });
      expect(body?.base_version).toBe("unchanged-raw");
      expect(body?.ops[0]).toMatchObject({ listen: ":8080", server_names: ["a.test"], match_type: "prefix", path: "/", match_ordinal: 1, selector_version: "global-v1" });
      if (mode.endsWith("rate limit")) {
        expect(body?.ops[0]).toMatchObject({ op: "route_set_rate_limit", rate_limit: { enabled: mode.startsWith("enable"), rate: mode.startsWith("enable") ? 100 : 50, burst: 100, key: "ip" } });
      } else {
        expect(body?.ops[0]).toMatchObject({ op: "location_set_transcode", transcode: mode.startsWith("reflection") ? { use_reflection: true, streaming: true, stream_mode: "sse", max_message_size: "8m" } : { descriptor_path: "/new.pb", streaming: false } });
        const payload = body?.ops[0]?.transcode;
        expect(payload).not.toHaveProperty(mode.startsWith("reflection") ? "descriptor_path" : "use_reflection");
      }
    });
  }

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
