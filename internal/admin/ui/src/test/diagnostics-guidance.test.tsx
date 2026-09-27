/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import type { StatsSnapshot } from "@/api/client.ts";
import { PermissionContext, type PermissionState } from "@/auth/usePermission.ts";
import { DiagnosticsGuidanceSection } from "@/features/overview/DiagnosticsGuidanceSection.tsx";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";
import {
  buildGuidance,
  profileAccess,
  profileCommand,
  rising,
  type GuidanceItem,
} from "@/lib/diagnostics.ts";

const MiB = 1024 * 1024;

function stats(overrides: Partial<StatsSnapshot> = {}): StatsSnapshot {
  return {
    available: true,
    uptimeSeconds: 10,
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
    ...overrides,
  };
}

function item(items: GuidanceItem[], id: GuidanceItem["id"]): GuidanceItem {
  const found = items.find((i) => i.id === id);
  if (!found) throw new Error(`no ${id} item`);
  return found;
}

// No guidance text may claim a cause.
const DIAGNOSIS =
  /\b(is|has|indicates) a (memory |heap |goroutine )?leak\b|root cause|caused by|the problem is/i;

describe("buildGuidance (#445)", () => {
  it("maps CPU near the scheduler limit to CPU-profile guidance", () => {
    const g = buildGuidance(stats({ cpuCores: 3.5, goMaxProcs: 4 }));
    const cpu = item(g, "cpu");
    expect(cpu.signal).toBe("elevated");
    expect(cpu.profile).toBe("cpu");
    expect(cpu.observation).toContain("of a Go scheduler limit of 4");
    expect(g[0]?.id).toBe("cpu"); // elevated first
    expect(item(buildGuidance(stats({ cpuCores: 0.5, goMaxProcs: 4 })), "cpu").signal).toBe(
      "normal",
    );
    expect(item(buildGuidance(stats({ cpuCores: 0.5 })), "cpu").signal).toBe("unknown");
    expect(item(buildGuidance(stats()), "cpu").observation).toMatch(/not reported/);
  });

  it("keeps RSS and Go heap distinct and never calls non-heap memory a heap leak", () => {
    const heapy = item(
      buildGuidance(stats({ rssBytes: 400 * MiB, goHeapAllocBytes: 300 * MiB })),
      "memory",
    );
    expect(heapy.profile).toBe("heap");
    expect(heapy.observation).toContain("Most resident memory is Go heap");

    const native = item(
      buildGuidance(stats({ rssBytes: 1200 * MiB, goHeapAllocBytes: 100 * MiB })),
      "memory",
    );
    expect(native.profile).toBe("goroutine");
    expect(native.observation).toContain("outside the Go heap");
    expect(native.observation).toContain("does not indicate a Go heap leak");
    expect(native.steps.join(" ")).toMatch(
      /WASM plugin linear memory, goroutine stacks, memory-mapped files/,
    );

    const growing = item(
      buildGuidance(stats({ rssBytes: 900 * MiB, goHeapAllocBytes: 800 * MiB }), {
        rssBytes: [500 * MiB, NaN, 900 * MiB],
        goroutines: [],
      }),
      "memory",
    );
    expect(growing.signal).toBe("elevated");
    expect(growing.observation).toContain("rose over the last two minutes");
    expect(item(buildGuidance(stats({ rssBytes: 0, goHeapAllocBytes: 0 })), "memory").profile).toBe(
      "heap",
    );
    expect(item(buildGuidance(stats({ rssBytes: MiB })), "memory").signal).toBe("unknown");
  });

  it("maps goroutine growth or a high count to goroutine-profile guidance", () => {
    const steady = item(buildGuidance(stats({ goroutines: 40 })), "goroutines");
    expect(steady.signal).toBe("normal");
    const grew = item(
      buildGuidance(stats({ goroutines: 3000 }), { rssBytes: [], goroutines: [1000, 2000, 3000] }),
      "goroutines",
    );
    expect(grew.signal).toBe("elevated");
    expect(grew.profile).toBe("goroutine");
    expect(grew.observation).toContain("rising");
    expect(item(buildGuidance(stats({ goroutines: 20_000 })), "goroutines").signal).toBe(
      "elevated",
    );
    expect(item(buildGuidance(stats()), "goroutines").signal).toBe("unknown");
  });

  it("maps FD pressure to connection/runtime guidance without a profile", () => {
    const fds = item(buildGuidance(stats({ openFDs: 900, maxFDs: 1024 })), "fds");
    expect(fds.signal).toBe("elevated");
    expect(fds.profile).toBeUndefined();
    expect(fds.steps.join(" ")).toMatch(/Listener Connections/);
    expect(fds.steps.join(" ")).toMatch(/LimitNOFILE/);
    expect(item(buildGuidance(stats({ openFDs: 10, maxFDs: 1024 })), "fds").signal).toBe("normal");
    expect(item(buildGuidance(stats({ openFDs: 10 })), "fds").observation).toMatch(
      /limit is not reported/,
    );
    expect(item(buildGuidance(stats()), "fds").signal).toBe("unknown");
  });

  it("maps storage warning/critical and failing writes to storage guidance", () => {
    const low = item(
      buildGuidance(
        stats({
          storage: [
            { category: "access_log", state: "low", availableRatio: 0.08 },
            { category: "cache", state: "ok", availableRatio: 0.5 },
          ],
        }),
      ),
      "storage",
    );
    expect(low.signal).toBe("elevated");
    expect(low.observation).toBe(
      "Access log (low headroom). Writes still succeed; low headroom is an early hint, not a failure.",
    );
    expect(low.steps[0]).toContain("observability.access_log.file");
    expect(low.steps[0]).toContain("never deletes");

    const failing = item(
      buildGuidance(
        stats({ storage: [{ category: "cache", state: "critical", writesFailing: true }] }),
      ),
      "storage",
    );
    expect(failing.observation).toContain("Disk cache (writes failing)");
    expect(failing.observation).toContain("already being lost");

    const fine = item(
      buildGuidance(stats({ storage: [{ category: "cache", state: "ok" }] })),
      "storage",
    );
    expect(fine.signal).toBe("normal");
    expect(
      item(
        buildGuidance(stats({ storage: [{ category: "acme", state: "unavailable" }] })),
        "storage",
      ).signal,
    ).toBe("unknown");
    expect(buildGuidance(stats()).some((i) => i.id === "storage")).toBe(false);
  });

  it("never phrases guidance as a diagnosis", () => {
    const everything = buildGuidance(
      stats({
        cpuCores: 4,
        goMaxProcs: 4,
        rssBytes: 2000 * MiB,
        goHeapAllocBytes: 100 * MiB,
        goroutines: 50_000,
        openFDs: 1000,
        maxFDs: 1024,
        storage: [{ category: "cache", state: "critical", writesFailing: true }],
      }),
    );
    for (const i of everything) {
      expect(`${i.observation} ${i.steps.join(" ")}`).not.toMatch(DIAGNOSIS);
    }
  });

  it("rising needs two finite samples and both a factor and a delta", () => {
    expect(rising([], 1.5, 1)).toBe(false);
    expect(rising([NaN, 5], 1.5, 1)).toBe(false);
    expect(rising([10, 20], 1.5, 5)).toBe(true);
    expect(rising([10, 12], 1.5, 1)).toBe(false);
    expect(rising([1000, 1400], 1.2, 500)).toBe(false);
  });
});

describe("profileAccess and profileCommand", () => {
  it("never offers a command unless capability and permission are known and granted", () => {
    expect(profileAccess(undefined, true, true)).toBe("unknown");
    expect(profileAccess(false, true, true)).toBe("disabled");
    expect(profileAccess(true, false, true)).toBe("unknown");
    expect(profileAccess(true, true, false)).toBe("denied");
    expect(profileAccess(true, true, true)).toBe("available");
  });

  it("targets the existing gated endpoint with a placeholder token only", () => {
    const cpu = profileCommand("cpu", "https://admin.example:9443");
    expect(cpu).toContain('"https://admin.example:9443/debug/pprof/profile?seconds=30"');
    expect(cpu).toContain("$JUL_ADMIN_TOKEN");
    expect(profileCommand("heap", "http://127.0.0.1:9090")).toContain("/debug/pprof/heap");
    expect(profileCommand("goroutine", "http://127.0.0.1:9090")).toContain(
      "/debug/pprof/goroutine",
    );
  });
});

function perm(ready: boolean, granted: string[]): PermissionState {
  return { identity: null, isLoading: false, ready, has: (p) => !ready || granted.includes(p) };
}

function renderSection(
  p: PermissionState,
  pprofEnabled: boolean | undefined,
  s = stats({ cpuCores: 1, goMaxProcs: 1 }),
) {
  return render(
    <MemoryRouter>
      <PermissionContext.Provider value={p}>
        <DiagnosticsGuidanceSection stats={s} pprofEnabled={pprofEnabled} />
      </PermissionContext.Provider>
    </MemoryRouter>,
  );
}

describe("DiagnosticsGuidanceSection", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows the command, caveats and never a stored credential when permitted", async () => {
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: write },
      configurable: true,
    });
    sessionStorage.setItem("jul-admin-token", "super-secret-token");
    renderSection(perm(true, ["admin:manage"]), true);
    const cpu = screen.getByRole("listitem", { name: "CPU guidance" });
    expect(within(cpu).getByText("Elevated now")).toBeInTheDocument();
    expect(within(cpu).getByText(/Capture a CPU profile \(run it yourself\)/)).toBeInTheDocument();
    expect(within(cpu).getByText(/sensitive/)).toBeInTheDocument();
    expect(within(cpu).getByText(/Samples for 30 seconds/)).toBeInTheDocument();
    fireEvent.click(within(cpu).getByRole("button", { name: "Copy the CPU profile command" }));
    await waitFor(() => {
      expect(within(cpu).getByText("Copied")).toBeInTheDocument();
    });
    expect(write.mock.calls[0]?.[0]).toContain("$JUL_ADMIN_TOKEN");
    expect(document.body.textContent).not.toContain("super-secret-token");
    expect(screen.getByText(/not a diagnosis/)).toBeInTheDocument();
    expect(screen.getByText(/jul doctor --config/)).toBeInTheDocument();
    expect(screen.getByText(/jul support-bundle --config/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Operations" })).toHaveAttribute("href", "/operations");
    sessionStorage.removeItem("jul-admin-token");
  });

  it("keeps the command visible when the clipboard refuses", async () => {
    const write = vi.fn().mockRejectedValue(new Error("denied"));
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: write },
      configurable: true,
    });
    renderSection(perm(true, ["admin:manage"]), true);
    const cpu = screen.getByRole("listitem", { name: "CPU guidance" });
    fireEvent.click(within(cpu).getByRole("button", { name: "Copy the CPU profile command" }));
    await waitFor(() => {
      expect(write).toHaveBeenCalled();
    });
    expect(within(cpu).getByText("Copy")).toBeInTheDocument();
  });

  it("shows no executable action when profiling is disabled", () => {
    renderSection(perm(true, ["admin:manage"]), false);
    expect(screen.queryByText(/\/debug\/pprof/)).not.toBeInTheDocument();
    expect(screen.getAllByText(/Profiling is turned off on this server/).length).toBeGreaterThan(0);
  });

  it("shows no executable action when the role lacks admin:manage", () => {
    renderSection(perm(true, ["status:read"]), true);
    expect(screen.queryByText(/\/debug\/pprof/)).not.toBeInTheDocument();
    expect(screen.getAllByText(/requires the/).length).toBeGreaterThan(0);
  });

  it("shows no action while capability or identity is unknown", () => {
    renderSection(perm(false, []), true);
    expect(screen.queryByText(/\/debug\/pprof/)).not.toBeInTheDocument();
    renderSection(perm(true, ["admin:manage"]), undefined);
    expect(screen.getAllByText(/Profiling availability is not known yet/).length).toBeGreaterThan(
      0,
    );
  });
});

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

describe("OverviewPanel diagnostics guidance", () => {
  afterEach(() => vi.restoreAllMocks());

  it("renders guidance from the overview's stats and admin runtime capability", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            product: "Jul.IA",
            version: "2.0.0",
            status: [],
            stats: stats({ openFDs: 1000, maxFDs: 1024 }),
          }),
      }),
    );
    render(<OverviewPanel />, { wrapper: Wrapper });
    expect(
      await screen.findByRole("heading", { name: "Diagnostics guidance" }),
    ).toBeInTheDocument();
    const fds = screen.getByRole("listitem", { name: "File descriptors guidance" });
    expect(within(fds).getByText("Elevated now")).toBeInTheDocument();
    // No admin_runtime in the response: capability unknown, so no command.
    expect(screen.queryByText(/\/debug\/pprof/)).not.toBeInTheDocument();
  });
});
