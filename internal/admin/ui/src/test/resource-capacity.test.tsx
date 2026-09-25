/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { renderHook, act } from "@testing-library/react";

import { formatBytes, formatBytesPerSec, formatCores } from "@/lib/formatBytes.ts";
import { useMetricsHistory } from "@/lib/useMetricsHistory.ts";
import type { StatsSnapshot } from "@/api/client.ts";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

// ── formatBytes / formatBytesPerSec / formatCores ────────────────────────────

describe("formatBytes", () => {
  it("renders bytes below 1024 with no fractional digits", () => {
    expect(formatBytes(512)).toBe("512 B");
  });
  it("renders KiB/MiB/GiB with one fractional digit", () => {
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(1024 * 1024 * 2.25)).toBe("2.3 MiB");
    expect(formatBytes(1024 ** 3 * 3)).toBe("3.0 GiB");
  });
  it("never fabricates a value for negative/non-finite input", () => {
    expect(formatBytes(-1)).toBe("—");
    expect(formatBytes(NaN)).toBe("—");
    expect(formatBytes(Infinity)).toBe("—");
  });
});

describe("formatBytesPerSec", () => {
  it("appends a /s suffix", () => {
    expect(formatBytesPerSec(2048)).toBe("2.0 KiB/s");
  });
});

describe("formatCores", () => {
  it("renders cores to two decimal places, never a percentage", () => {
    expect(formatCores(1.7)).toBe("1.70 cores");
    expect(formatCores(0)).toBe("0.00 cores");
  });
  it("never fabricates a value for negative/non-finite input", () => {
    expect(formatCores(-1)).toBe("—");
  });
});

// ── useMetricsHistory: resource/capacity additions ───────────────────────────

function baseStats(overrides: Partial<StatsSnapshot> = {}): StatsSnapshot {
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

describe("useMetricsHistory resource/capacity fields", () => {
  it("pushes NaN holes for unavailable resource fields rather than 0", () => {
    const { result } = renderHook(({ stats }: { stats: StatsSnapshot }) => useMetricsHistory(stats), {
      initialProps: { stats: baseStats() }, // no cpuCores/rssBytes/goroutines
    });
    expect(result.current.cpuCores.at(-1)).toBeNaN();
    expect(result.current.rssBytes.at(-1)).toBeNaN();
    expect(result.current.goroutines.at(-1)).toBeNaN();
  });

  it("carries real resource values through when present", () => {
    const { result } = renderHook(({ stats }: { stats: StatsSnapshot }) => useMetricsHistory(stats), {
      initialProps: { stats: baseStats({ cpuCores: 1.5, rssBytes: 1024, goroutines: 42 }) },
    });
    expect(result.current.cpuCores.at(-1)).toBe(1.5);
    expect(result.current.rssBytes.at(-1)).toBe(1024);
    expect(result.current.goroutines.at(-1)).toBe(42);
  });

  it("derives httpBytesPerSec as 0 on the first sample", () => {
    const { result } = renderHook(({ stats }: { stats: StatsSnapshot }) => useMetricsHistory(stats), {
      initialProps: { stats: baseStats({ httpResponseBytesTotal: 1000 }) },
    });
    expect(result.current.httpBytesPerSec.at(-1)).toBe(0);
  });

  it("derives a positive httpBytesPerSec from a growing counter", () => {
    vi.useFakeTimers();
    try {
      const { result, rerender } = renderHook(
        ({ stats }: { stats: StatsSnapshot }) => useMetricsHistory(stats),
        { initialProps: { stats: baseStats({ httpResponseBytesTotal: 1000 }) } },
      );
      act(() => {
        vi.advanceTimersByTime(2000);
      });
      rerender({ stats: baseStats({ httpResponseBytesTotal: 5000 }) });
      // 4000 bytes over ~2s ~= 2000 B/s
      expect(result.current.httpBytesPerSec.at(-1)).toBeGreaterThan(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("treats a counter reset (value decreased) as 0, never negative", () => {
    vi.useFakeTimers();
    try {
      const { result, rerender } = renderHook(
        ({ stats }: { stats: StatsSnapshot }) => useMetricsHistory(stats),
        { initialProps: { stats: baseStats({ httpResponseBytesTotal: 5000 }) } },
      );
      act(() => {
        vi.advanceTimersByTime(1000);
      });
      rerender({ stats: baseStats({ httpResponseBytesTotal: 100 }) }); // reload reset the counter
      expect(result.current.httpBytesPerSec.at(-1)).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });
});

// ── OverviewPanel: Runtime Resources / Capacity rendering ────────────────────

function mockOverviewFetch(stats: StatsSnapshot) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: () =>
        Promise.resolve({
          product: "Jul.IA",
          version: "2.0.0",
          status: [],
          stats,
        }),
    }),
  );
}

describe("OverviewPanel runtime resources and capacity", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows 'unavailable' for a resource field the platform did not report", async () => {
    mockOverviewFetch(baseStats()); // no cpuCores/rssBytes/etc at all
    render(<OverviewPanel />, { wrapper: Wrapper });
    const cards = await screen.findAllByText("unavailable");
    expect(cards.length).toBeGreaterThan(0);
  });

  it("renders real resource values with correct units, not raw bytes", async () => {
    mockOverviewFetch(
      baseStats({ cpuCores: 1.7, rssBytes: 1024 * 1024 * 50, goHeapAllocBytes: 1024 * 1024 * 10, goroutines: 120 }),
    );
    render(<OverviewPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("1.70 cores")).toBeInTheDocument();
    expect(await screen.findByText("50.0 MiB")).toBeInTheDocument();
    expect(await screen.findByText("10.0 MiB")).toBeInTheDocument();
  });

  it("renders a max-FD-unavailable subtext rather than a fake percentage", async () => {
    mockOverviewFetch(baseStats({ openFDs: 42 })); // no maxFDs
    render(<OverviewPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("limit unavailable on this platform")).toBeInTheDocument();
  });

  it("renders the no-eligible-backend warning list when populated", async () => {
    mockOverviewFetch(baseStats({ upstreamNoEligible: ["api", "billing"] }));
    render(<OverviewPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("No eligible backend")).toBeInTheDocument();
    expect(await screen.findByText("api")).toBeInTheDocument();
    expect(await screen.findByText("billing")).toBeInTheDocument();
  });

  it("does not render the no-eligible-backend list when empty", async () => {
    mockOverviewFetch(baseStats());
    render(<OverviewPanel />, { wrapper: Wrapper });
    await screen.findByText("Runtime Resources"); // wait for the section to mount
    expect(screen.queryByText("No eligible backend")).not.toBeInTheDocument();
  });

  it("renders cache occupancy percentage only when a max is configured", async () => {
    mockOverviewFetch(
      baseStats({
        cacheTiers: [
          { tier: "memory", bytes: 50, maxBytes: 100, entries: 1, evictions: 0, occupancyRatio: 0.5 },
          { tier: "disk", bytes: 200, entries: 2, evictions: 0 },
        ],
      }),
    );
    render(<OverviewPanel />, { wrapper: Wrapper });
    expect(await screen.findByText(/50% full/)).toBeInTheDocument();
    expect(await screen.findByText("unbounded/unavailable — no configured cap")).toBeInTheDocument();
  });
});
