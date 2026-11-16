/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { OverviewSchema, type Overview, type StatsSnapshot } from "@/api/client.ts";
import { App } from "@/app/App.tsx";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";
import { openResourceDisclosure } from "@/features/overview/resourceDisclosure.ts";
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

  it("shows observe and draining inspection charges without calling them process memory", () => {
    show(
      overview({
        inspection: {
          configured: { max_buffered_bytes: 4096, max_concurrent_evaluations: 0 },
          minimum_request_bytes: 1048578,
          minimum_request_route: "servers[0].locations[0]",
          effective: { mode: "observe", max_buffered_bytes: 4096, max_concurrent_evaluations: 4 },
          usage: { base_bytes: 1, buffer_bytes: 2, waf_state_bytes: 3, audit_bytes: 4 },
          audit_output: {
            in_flight_calls: 2,
            abandoned_calls: 1,
            abandoned_bytes: 3,
            max_calls: 64,
            max_bytes: 4096,
            drops: 5,
          },
          active_leases: 1,
          active_evaluations: 2,
          waiting_evaluations: 1,
          draining: true,
          request_path_integration: "partial",
        },
      }),
    );
    fireEvent.click(screen.getByText("Show inspection resources"));
    const card = screen.getByRole("region", { name: "Inspection resources" });
    expect(within(card).getByText(/Mode: observe.*Draining/)).toBeVisible();
    expect(within(card).getByText(/without governor-driven request refusal/)).toBeVisible();
    expect(within(card).getByText(/Audit records can still be dropped/)).toBeVisible();
    expect(within(card).getByText("WAF state estimate")).toBeVisible();
    expect(within(card).getByText(/Configured evaluation slots: automatic/)).toBeVisible();
    expect(within(card).getByText(/Limiting route: servers\[0\].locations\[0\]/)).toBeVisible();
    expect(within(card).getByText(/1,048,578 B/)).toBeVisible();
    expect(within(card).getByText(/not a concurrency or process-memory recommendation/)).toBeVisible();
    expect(within(card).getByText(/1 past deadline and still running/)).toBeVisible();
    expect(
      within(card).getByText(/Governor-dropped audit records since process start: 5/),
    ).toBeVisible();
    expect(within(card).getByText(/drop count excludes formatter and writer failures/)).toBeVisible();
    expect(within(card).getByText(/not an additional charge/)).toBeVisible();
    expect(within(card).getByText(/not process memory/)).toBeVisible();
  });

  it("shows an enforce snapshot without claiming it is draining", () => {
    show(
      overview({
        inspection: {
          effective: { mode: "enforce", max_buffered_bytes: 4096, max_concurrent_evaluations: 4 },
          usage: { base_bytes: 0, buffer_bytes: 0, waf_state_bytes: 0, audit_bytes: 0 },
          active_leases: 0,
          active_evaluations: 0,
          waiting_evaluations: 0,
          draining: false,
          request_path_integration: "partial",
        },
      }),
    );
    fireEvent.click(screen.getByText("Show inspection resources"));
    const card = screen.getByRole("region", { name: "Inspection resources" });
    expect(within(card).getByText(/Mode: enforce/)).toBeVisible();
    expect(within(card).queryByText(/Draining/)).not.toBeInTheDocument();
    expect(
      within(card).queryByText(/without governor-driven request refusal/),
    ).not.toBeInTheDocument();
  });

  it("surfaces inspection errors without losing the overview", () => {
    show(overview({ inspection_error: "could not report plugin guest capacity" }));
    expect(screen.getByText("Inspection status unavailable")).toBeVisible();
    fireEvent.click(screen.getByText("Show inspection resources"));
    const card = screen.getByRole("region", { name: "Inspection resources" });
    expect(within(card).getByText(/Inspection status unavailable/)).toBeVisible();
    expect(screen.getByText("Requests/sec")).toBeVisible();
  });

  it("distinguishes missing inspection data from an idle snapshot", () => {
    show(overview());
    fireEvent.click(screen.getByText("Show inspection resources"));
    expect(
      within(screen.getByRole("region", { name: "Inspection resources" })).getByText(
        "Inspection status unavailable.",
      ),
    ).toBeVisible();
  });

  it("reports missing older audit fields as unavailable rather than zero", () => {
    show(
      overview({
        inspection: {
          effective: { mode: "enforce", max_buffered_bytes: 4096, max_concurrent_evaluations: 4 },
          usage: { base_bytes: 0, buffer_bytes: 0, waf_state_bytes: 0 },
          active_leases: 0,
          active_evaluations: 0,
          waiting_evaluations: 0,
          draining: false,
          request_path_integration: "partial",
        },
      }),
    );
    fireEvent.click(screen.getByText("Show inspection resources"));
    const card = screen.getByRole("region", { name: "Inspection resources" });
    expect(within(card).getByText("Audit output status unavailable.")).toBeVisible();
    expect(within(card).getByText("Audit").nextElementSibling).toHaveTextContent("Unavailable");
    expect(within(card).getByText(/Single-request minimum: unavailable/)).toBeVisible();
  });

  it.each([
    [0, "no inspected routes"],
    [2048, "Limiting route: unavailable or restricted"],
  ])("reports a minimum of %i without a route as %s", (minimum, message) => {
    show(
      overview({
        inspection: {
          effective: { mode: "enforce", max_buffered_bytes: 4096, max_concurrent_evaluations: 4 },
          usage: { base_bytes: 0, buffer_bytes: 0, waf_state_bytes: 0 },
          minimum_request_bytes: minimum,
          active_leases: 0,
          active_evaluations: 0,
          waiting_evaluations: 0,
          draining: false,
          request_path_integration: "partial",
        },
      }),
    );
    fireEvent.click(screen.getByText("Show inspection resources"));
    expect(within(screen.getByRole("region", { name: "Inspection resources" })).getByText(
      new RegExp(message),
    )).toBeVisible();
  });

  it("isolates incomplete audit output from otherwise valid inspection status", () => {
    show(
      OverviewSchema.parse({
        ...overview(),
        inspection: {
          effective: { mode: "enforce", max_buffered_bytes: 4096, max_concurrent_evaluations: 4 },
          usage: { base_bytes: 0, buffer_bytes: 0, waf_state_bytes: 0 },
          audit_output: { abandoned_calls: 1 },
          active_leases: 0,
          active_evaluations: 0,
          waiting_evaluations: 0,
          draining: false,
          request_path_integration: "partial",
        },
      }),
    );
    fireEvent.click(screen.getByText("Show inspection resources"));
    const card = screen.getByRole("region", { name: "Inspection resources" });
    expect(within(card).getByText(/Mode: enforce/)).toBeVisible();
    expect(within(card).getByText("Audit output status unavailable.")).toBeVisible();
  });

  it("isolates an incompatible inspection projection from the rest of the overview", () => {
    show(OverviewSchema.parse({ ...overview(), inspection: { effective: {} } }));
    expect(screen.getByText("Inspection status unavailable")).toBeVisible();
    expect(screen.getByText("Requests/sec")).toBeVisible();
    fireEvent.click(screen.getByText("Show inspection resources"));
    expect(
      within(screen.getByRole("region", { name: "Inspection resources" })).getByText(
        "Inspection status unavailable.",
      ),
    ).toBeVisible();
  });

  it("opens an available resource disclosure and tolerates an absent one", () => {
    const details = document.createElement("details");
    expect(details.open).toBe(false);
    openResourceDisclosure(details);
    expect(details.open).toBe(true);
    openResourceDisclosure(details);
    expect(details.open).toBe(true);
    expect(() => {
      openResourceDisclosure(null);
    }).not.toThrow();
  });

  it("shows idle traffic and snapshot while secondary details start closed", async () => {
    show(overview());
    const traffic = screen.getByRole("region", { name: "Traffic and connections" });
    expect(screen.getByText(/Zero traffic can mean/)).toBeVisible();
    expect(within(traffic).getByText("Requests/sec")).toBeVisible();
    expect(
      within(traffic).getByRole("region", { name: "Connection lifecycle across reloads" }),
    ).toBeVisible();
    const resourceSummary = screen.getByText("Show resource and storage details", {
      selector: "summary",
    });
    expect(resourceSummary.parentElement).not.toHaveAttribute("open");
    fireEvent.click(resourceSummary);
    expect(resourceSummary.parentElement).toHaveAttribute("open");
    expect(screen.getByText("Runtime Resources")).toBeVisible();
    fireEvent.click(resourceSummary);
    expect(resourceSummary.parentElement).not.toHaveAttribute("open");
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
    const toggle = screen.getByText("Show resource and storage details", { selector: "summary" });
    await waitFor(() => {
      expect(toggle.parentElement).toHaveAttribute("open");
    });
    expect(screen.getAllByText(/Disk cache \(writes failing\)/)[0]).toBeVisible();
    expect(screen.getByText(/Disk cache storage: critical headroom/)).toBeVisible();
    fireEvent.click(toggle);
    expect(toggle.parentElement).not.toHaveAttribute("open");
    expect(screen.getAllByText(/Disk cache \(writes failing\)/)[0]).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: /Storage: writes failing/ }));
    await waitFor(() => {
      expect(scroll).toHaveBeenCalled();
    });
    await waitFor(() => {
      expect(toggle.parentElement).toHaveAttribute("open");
    });
  });

  it("surfaces capacity warnings at the threshold and reopens their details", async () => {
    const scroll = vi.fn();
    show(
      overview({
        stats: {
          ...stats,
          cacheTiers: [
            {
              tier: "memory",
              bytes: 90,
              maxBytes: 100,
              entries: 3,
              evictions: 0,
              occupancyRatio: 0.9,
            },
            {
              tier: "disk",
              bytes: 89,
              maxBytes: 100,
              entries: 4,
              evictions: 0,
              occupancyRatio: 0.89,
            },
          ],
          upstreamWorstActive: { pool: "api", current: 9, max: 10, ratio: 0.9 },
          upstreamWorstPending: { pool: "billing", current: 10, max: 10, ratio: 1 },
          upstreamBudgetExhausted: ["api", "billing"],
          storage: [{ category: "access_log", state: "low", writesFailing: true }],
        },
      }),
    );
    const heading = screen.getByRole("heading", { name: "Resources and storage" });
    Object.defineProperty(heading, "scrollIntoView", { configurable: true, value: scroll });
    const toggle = screen.getByText("Show resource and storage details", { selector: "summary" });
    await waitFor(() => {
      expect(toggle.parentElement).toHaveAttribute("open");
    });
    const warnings = [
      "Cache memory is near capacity.",
      "Upstream active pressure is near its configured limit.",
      "Upstream pending pressure is near its configured limit.",
      "Retry budget exhausted: api, billing.",
      "Access log storage: low headroom.",
    ];
    for (const warning of warnings) expect(screen.getByText(warning)).toBeVisible();
    expect(screen.queryByText("Cache disk is near capacity.")).not.toBeInTheDocument();

    fireEvent.click(toggle);
    for (const warning of warnings) expect(screen.getByText(warning)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "View resource details and diagnostics" }));
    await waitFor(() => {
      expect(toggle.parentElement).toHaveAttribute("open");
    });
    expect(scroll).toHaveBeenCalledWith({ behavior: "smooth" });
  });

  it("orders the HTTP method breakdown by request count", () => {
    show(overview({ stats: { ...stats, methods: { POST: 12, DELETE: 1, GET: 42 } } }));
    const section = screen.getByText("Requests by HTTP Method").parentElement;
    if (!section) throw new Error("Missing HTTP method breakdown");
    const cards = within(section).getAllByText(/^(GET|POST|DELETE)$/);
    expect(cards.map((card) => card.textContent)).toEqual(["GET", "POST", "DELETE"]);
  });

  it("opens a trend detail, exports samples, and restores focus on Escape", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    const client = show(overview({ stats: { ...stats, requestsPerSec: 2 } }));
    await screen.findByText("2-Minute Trends");
    act(() => {
      client.setQueryData(["overview"], overview({ stats: { ...stats, requestsPerSec: 4 } }), {
        updatedAt: Date.now() + 1000,
      });
    });
    fireEvent.click(screen.getByText("2-Minute Trends"));
    const trigger = await screen.findByRole("button", { name: /Request Rate trend\. 2 samples/ });
    trigger.focus();
    fireEvent.click(trigger);
    const dialog = await screen.findByRole("dialog", { name: "Request Rate" });
    expect(within(dialog).getByText(/Insufficient data for trend analysis/)).toBeVisible();
    const chart = within(dialog).getByRole("img", { name: /Request Rate detail chart/ });
    fireEvent.keyDown(chart, { key: "ArrowRight" });
    expect(within(dialog).getByText(/2\.0 req\/s/)).toBeVisible();
    fireEvent.pointerLeave(chart);
    expect(within(dialog).getByText("Hover or use arrow keys to inspect values")).toBeVisible();
    fireEvent.click(within(dialog).getByRole("button", { name: "Export CSV" }));
    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith(
        expect.stringMatching(/^timestamp_ms,timestamp_local,value\n.+,2\n.+,4$/),
      );
    });
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Request Rate" })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("loads the setup wizard through its app route", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/wizard"]}>
          <PermissionContext.Provider value={permission(false)}>
            <App />
          </PermissionContext.Provider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByRole("heading", { name: "Setup Wizard" })).toBeVisible();
  });

  it("renders a trend summary once sufficient samples have been collected", async () => {
    const { ChartDetailPanel } = await import("@/components/ChartDetailPanel.tsx");
    render(
      <MemoryRouter>
        <ChartDetailPanel
          metricKey="requestsPerSec"
          data={[1, 2, 3, 4, 5, 6, 7, 8, 9, 10]}
          timestamps={[1, 2, 3, 4, 5, 6, 7, 8, 9, 10]}
          onClose={() => undefined}
        />
      </MemoryRouter>,
    );
    const dialog = screen.getByRole("dialog", { name: "Request Rate" });
    const current = within(dialog).getByText("Current").parentElement;
    if (!current) throw new Error("Missing current trend summary");
    expect(within(current).getByText("10.0 req/s")).toBeVisible();
    expect(within(dialog).getByText(/Healthy/)).toBeVisible();
    expect(within(dialog).queryByText(/Insufficient data/)).not.toBeInTheDocument();
  });

  it.each([
    { ok: true, restored: false, outcome: "applied_live" as const, label: "applied_live" },
    { ok: false, restored: true, outcome: "not_applied" as const, label: "failed — restored" },
    { ok: false, restored: false, outcome: "not_applied" as const, label: "failed" },
  ])("retains the last managed apply outcome: $label", ({ ok, restored, outcome, label }) => {
    show(
      overview({
        stats: { ...stats, uptimeSeconds: 0 },
        last_managed_apply: {
          id: "apply-1",
          mode: "hot",
          ok,
          restored,
          outcome,
          completed_at: "2026-01-01T00:00:00Z",
          final_disk_version: "v1",
          restore_error: ok ? undefined : "restore failed",
        },
      }),
    );
    expect(screen.getByText("Last apply")).toBeVisible();
    expect(screen.getByText(label, { exact: true })).toBeVisible();
    expect(screen.getByText("v1")).toBeVisible();
    if (!ok) expect(screen.getByText("Restore error: restore failed")).toBeVisible();
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

  it.each([true, false])("shows the reported plugin build availability: %s", (compiled) => {
    show(
      overview({
        status: [
          { group: "Extensibility", name: "WASM plugins", active: true, detail: "Configured" },
        ],
        admin_runtime: {
          generation: "test",
          console_compiled: true,
          console_configured: true,
          console_effective: true,
          plugins_compiled: compiled,
          upload_enabled: false,
          upload_max_size_mb: 0,
          upload_directory_health: "unknown",
          history_keep: 0,
          history_retention_health: "unknown",
          rate_limit_read_per_min: 0,
          rate_limit_write_per_min: 0,
          rate_limit_apply_per_min: 0,
          max_event_conns: 1,
          tracked_limiter_clients: 0,
          sse_active_total: 0,
          sse_active_clients: 0,
          sse_over_cap_clients: 0,
          sse_max_per_client: 0,
          rate_read_rejected: 0,
          rate_write_rejected: 0,
          rate_apply_rejected: 0,
          sse_rejected: 0,
        },
      }),
    );
    fireEvent.click(screen.getByText("Show capabilities and configuration"));
    expect(screen.getByText(compiled ? "build available" : "build unavailable")).toBeVisible();
    expect(screen.getByTitle("Configured")).toBeVisible();
  });
});
