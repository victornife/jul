/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { Overview, StatsSnapshot } from "@/api/client.ts";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";
import { PermissionContext, type PermissionState } from "@/auth/usePermission.ts";

const stats: StatsSnapshot = {
  available: true,
  uptimeSeconds: 30,
  requestsTotal: 0,
  requestsPerSec: 0,
  inFlight: 0,
  connections: 0,
  errorRate: 0,
  latencyAvgMs: 0,
  latencyP50Ms: 0,
  latencyP95Ms: 0,
  latencyP99Ms: 0,
  cacheHitRatio: 0,
  httpResponseBytesTotal: 0,
  cpuCores: 0.1,
  goMaxProcs: 2,
  rssBytes: 100,
  goHeapAllocBytes: 80,
  goroutines: 2,
  openFDs: 5,
  maxFDs: 100,
};

const permission = (manage: boolean): PermissionState => ({
  identity: null,
  isLoading: false,
  ready: true,
  has: (value) => value === "admin:manage" && manage,
});

function show(overview: Overview, perms = permission(false)) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(["overview"], overview);
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <PermissionContext.Provider value={perms}>
          <OverviewPanel />
        </PermissionContext.Provider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}

const overview = (overrides: Partial<Overview> = {}): Overview => ({
  product: "Jul.IA",
  version: "test",
  status: [],
  stats,
  ...overrides,
});

describe("operator-centred Overview", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("shows idle traffic and snapshot while secondary details start closed", async () => {
    show(overview());
    const traffic = screen.getByRole("region", { name: "Traffic and connections" });
    expect(screen.getByText(/Zero traffic can mean/)).toBeVisible();
    expect(within(traffic).getByText("Requests/sec")).toBeVisible();
    expect(
      within(traffic).getByRole("region", { name: "Connection lifecycle across reloads" }),
    ).toBeVisible();
    expect(
      screen.getByRole("button", { name: "Show resource and storage details" }),
    ).toHaveAttribute("aria-expanded", "false");
    const trend = await screen.findByText("2-Minute Trends");
    expect(trend.parentElement).not.toHaveAttribute("open");
    expect(screen.queryByText("Traffic Sources")).not.toBeInTheDocument();
    expect(
      screen.getByText("Show capabilities and configuration").parentElement,
    ).not.toHaveAttribute("open");
    expect(screen.getByText("No guidance is elevated now.")).toBeVisible();
    expect(screen.getByText(/within hints, 0 not judged/)).toBeVisible();
  });

  it("keeps unavailable traffic distinct from an idle snapshot", () => {
    show(overview({ stats: undefined }));
    expect(screen.getByText("Live traffic statistics are unavailable.")).toBeVisible();
    expect(screen.getByText("Resource and storage statistics are unavailable.")).toBeVisible();
    expect(screen.getByText(/Guidance cannot be judged/)).toBeVisible();
    expect(
      within(
        screen.getByRole("region", { name: "Connection lifecycle across reloads" }),
      ).getAllByText("Unavailable"),
    ).toHaveLength(2);
    expect(screen.queryByText("Requests/sec")).not.toBeInTheDocument();
  });

  it("preserves loading and typed overview failures", async () => {
    let respond: (response: Response) => void = () => {
      throw new Error("request not started");
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            respond = resolve;
          }),
      ),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <OverviewPanel />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(screen.getByText("Loading overview…")).toBeVisible();
    respond(new Response(JSON.stringify({ error: "forbidden" }), { status: 403 }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveAttribute("data-error-kind", "forbidden");
    expect(screen.queryByText("Live traffic statistics are unavailable.")).not.toBeInTheDocument();
  });

  it("reveals source details only when supplied", () => {
    show(
      overview({
        traffic_sources: {
          preflight_count: 2,
          same_origin: 3,
          cross_origin: 4,
          hosts: { "example.test": 7 },
        },
      }),
    );
    const summary = screen.getByText("Traffic Sources", { selector: "summary" });
    expect(summary.parentElement).not.toHaveAttribute("open");
    fireEvent.click(summary);
    expect(screen.getByText("CORS Preflight (OPTIONS)")).toBeVisible();
    expect(screen.getByText("example.test")).toBeVisible();
    expect(screen.getByText("Top Referer Hosts")).toBeVisible();
  });

  it("opens on new storage failure and retains named warnings if closed", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    const client = show(overview());
    client.setQueryData(
      ["overview"],
      overview({
        stats: {
          ...stats,
          storage: [{ category: "cache", state: "critical", writesFailing: true }],
        },
      }),
    );
    const toggle = await screen.findByRole("button", { name: "Hide resource and storage details" });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getAllByText(/Disk cache \(writes failing\)/)[0]).toBeVisible();
    expect(screen.getByText(/Disk cache storage: critical headroom/)).toBeVisible();
    fireEvent.click(toggle);
    expect(
      screen.getByRole("button", { name: "Show resource and storage details" }),
    ).toHaveAttribute("aria-expanded", "false");
    expect(screen.getAllByText(/Disk cache \(writes failing\)/)[0]).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: /Storage: writes failing/ }));
    await waitFor(() => {
      expect(scroll).toHaveBeenCalled();
    });
    expect(screen.getByRole("button", { name: "Hide resource and storage details" })).toBeVisible();
  });

  it("never exposes unknown storage categories or unbounded reasons", async () => {
    show(
      overview({
        stats: {
          ...stats,
          storage: [
            {
              category: "cache",
              state: "error",
              reason: "/secret/path",
              sharedWith: ["/device/name"],
            },
            { category: "/unknown/path", state: "low" },
          ],
        },
      }),
    );
    expect(await screen.findByText("capacity reason not reported")).toBeVisible();
    expect(screen.getByText("Unrecognized storage categories are not shown.")).toBeVisible();
    expect(document.body.textContent).not.toContain("/secret/path");
    expect(document.body.textContent).not.toContain("/unknown/path");
    expect(document.body.textContent).not.toContain("/device/name");
  });

  it("distinguishes unjudged guidance and never infers build support", () => {
    show(
      overview({
        stats: { ...stats, cpuCores: undefined, goMaxProcs: undefined },
        status: [
          { group: "Extensibility", name: "WASM plugins", active: true },
          { group: "Protocols", name: "L4 stream proxy", active: false },
          { group: "Observability", name: "Distributed tracing", active: true },
        ],
      }),
    );
    expect(screen.getByText(/1 not judged/)).toBeVisible();
    fireEvent.click(screen.getByText(/Other guidance:/));
    expect(
      within(screen.getByRole("listitem", { name: "CPU guidance" })).getByText("Not judged"),
    ).toBeVisible();
    fireEvent.click(screen.getByText("Show capabilities and configuration"));
    expect(screen.getByText(/Status reflects configuration, not build support/)).toBeVisible();
    expect(screen.getAllByText("configured")).toHaveLength(2);
    expect(screen.getByText("not configured")).toBeVisible();
    expect(screen.getByRole("button", { name: /Manage Plugins — WASM plugins/ })).toBeVisible();
    expect(screen.getByText(/does not export spans/)).toBeVisible();
  });
});
