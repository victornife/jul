/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { StatsSnapshotSchema, type StatsSnapshot, type StorageHeadroom } from "@/api/client.ts";
import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";
import { StorageSection } from "@/features/overview/StorageSection.tsx";
import { storageAttention, storageLabel, storageSummary } from "@/lib/storage.ts";

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

const hints = { lowRatio: 0.1, criticalRatio: 0.05 };
const GiB = 1024 ** 3;

function renderSection(storage: StorageHeadroom[] | undefined, withHints = true) {
  return render(
    <MemoryRouter>
      <StorageSection storage={storage} hints={withHints ? hints : undefined} />
    </MemoryRouter>,
  );
}

function card(label: string) {
  return screen.getByRole("listitem", { name: `${label} storage` });
}

describe("StorageSection (#437)", () => {
  it("renders nothing when no Jul-owned storage is configured", () => {
    const { container } = renderSection(undefined);
    expect(container).toBeEmptyDOMElement();
    const { container: empty } = renderSection([]);
    expect(empty).toBeEmptyDOMElement();
  });

  it("renders healthy headroom with real free/total and percentage", () => {
    renderSection([
      {
        category: "cache",
        state: "ok",
        availableBytes: 40 * GiB,
        totalBytes: 100 * GiB,
        availableRatio: 0.4,
      },
    ]);
    const c = card("Disk cache");
    expect(within(c).getByText("40.0 GiB free")).toBeInTheDocument();
    expect(within(c).getByText(/of 100\.0 GiB — 40\.0% available/)).toBeInTheDocument();
    expect(within(c).queryByText(/headroom/i)).not.toBeInTheDocument();
    expect(within(c).getByRole("link", { name: /configure Disk cache/ })).toHaveAttribute(
      "href",
      "/traffic",
    );
    expect(within(c).getByText("cache.disk_path")).toBeInTheDocument();
  });

  it("labels low and critical headroom in text, not colour alone", () => {
    renderSection([
      {
        category: "access_log",
        state: "low",
        availableBytes: 8 * GiB,
        totalBytes: 100 * GiB,
        availableRatio: 0.08,
      },
      {
        category: "audit_log",
        state: "critical",
        availableBytes: GiB,
        totalBytes: 100 * GiB,
        availableRatio: 0.01,
      },
    ]);
    expect(within(card("Access log")).getByText("Low headroom")).toBeInTheDocument();
    expect(within(card("Audit log")).getByText("Critical headroom")).toBeInTheDocument();
    expect(screen.getByText(/Low below 10% available, critical below 5%/)).toBeInTheDocument();
    expect(screen.getByText(/never affect readiness/)).toBeInTheDocument();
  });

  it("distinguishes writes already failing from advisory low headroom", () => {
    renderSection([
      {
        category: "cache",
        state: "ok",
        availableBytes: 50 * GiB,
        totalBytes: 100 * GiB,
        availableRatio: 0.5,
        writeFailures: 12,
        writesFailing: true,
      },
      {
        category: "access_log",
        state: "ok",
        availableBytes: 50 * GiB,
        totalBytes: 100 * GiB,
        availableRatio: 0.5,
        writeFailures: 1,
        writesFailing: false,
      },
      {
        category: "audit_log",
        state: "ok",
        availableBytes: 50 * GiB,
        totalBytes: 100 * GiB,
        availableRatio: 0.5,
        writeFailures: 3,
      },
    ]);
    const cache = card("Disk cache");
    expect(within(cache).getByText("Writes failing")).toBeInTheDocument();
    expect(within(cache).getByText(/already losing writes/)).toBeInTheDocument();
    expect(within(cache).queryByText("Low headroom")).not.toBeInTheDocument();
    expect(
      within(card("Access log")).getByText(/1 earlier write failure since start/),
    ).toBeInTheDocument();
    expect(
      within(card("Audit log")).getByText(/3 earlier write failures since start/),
    ).toBeInTheDocument();
  });

  it("shows unavailable and error states truthfully — never 0, never NaN", () => {
    renderSection(
      [
        { category: "acme", state: "unavailable", reason: "unsupported_platform" },
        { category: "plugin_upload", state: "error", reason: "permission_denied" },
        { category: "config", state: "error", reason: "stat_failed" },
        { category: "config_history", state: "unavailable", reason: "path_unresolved" },
        { category: "mystery", state: "future_state" },
        { category: "cache", state: "unavailable", availableBytes: 5 },
      ],
      false,
    );
    expect(within(card("ACME certificates")).getByText("unavailable")).toBeInTheDocument();
    expect(
      within(card("ACME certificates")).getByText(/cannot report filesystem capacity/),
    ).toBeInTheDocument();
    expect(within(card("Plugin uploads")).getByText("error")).toBeInTheDocument();
    expect(within(card("Plugin uploads")).getByText("Error")).toBeInTheDocument();
    expect(within(card("Plugin uploads")).getByText(/permission denied/)).toBeInTheDocument();
    expect(
      within(card("Configuration file")).getByText(/capacity call failed/),
    ).toBeInTheDocument();
    expect(within(card("Configuration history")).getByText(/dangling link/)).toBeInTheDocument();
    expect(
      within(card("mystery")).getByText("capacity not reported for this location"),
    ).toBeInTheDocument();
    // An available count without a trustworthy state is not shown as free space.
    expect(within(card("Disk cache")).getByText("unavailable")).toBeInTheDocument();
    const text = document.body.textContent;
    expect(text).not.toMatch(/NaN|undefined|0 B free|0\.0% available/);
    expect(screen.queryByText(/Low below/)).not.toBeInTheDocument();
  });

  it("names shared filesystems and not-yet-created locations", () => {
    renderSection([
      {
        category: "cache",
        state: "ok",
        availableBytes: GiB,
        totalBytes: 2 * GiB,
        availableRatio: 0.5,
        sharedWith: ["access_log", "config_history"],
        pendingCreation: true,
      },
    ]);
    const c = card("Disk cache");
    expect(
      within(c).getByText("shares a filesystem with Access log, Configuration history"),
    ).toBeInTheDocument();
    expect(within(c).getByText(/not created yet/)).toBeInTheDocument();
  });

  it("parses the server shape and never needs a path", () => {
    const parsed = StatsSnapshotSchema.parse({
      uptimeSeconds: 1,
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
      storage: [
        { category: "cache", state: "ok", availableBytes: 1, totalBytes: 2, availableRatio: 0.5 },
      ],
      storageHints: hints,
    });
    expect(parsed.storage?.[0]?.category).toBe("cache");
    expect(parsed.storageHints?.lowRatio).toBe(0.1);
  });
});

describe("storage helpers", () => {
  const ok: StorageHeadroom = { category: "cache", state: "ok", availableRatio: 0.5 };
  it("storageAttention covers low, critical and failing writes only", () => {
    expect(storageAttention(ok)).toBe(false);
    expect(storageAttention({ ...ok, state: "low" })).toBe(true);
    expect(storageAttention({ ...ok, state: "critical" })).toBe(true);
    expect(storageAttention({ ...ok, writesFailing: true })).toBe(true);
    expect(storageAttention({ ...ok, state: "error" })).toBe(false);
  });

  it("storageLabel falls back to the raw bounded category", () => {
    expect(storageLabel("acme")).toBe("ACME certificates");
    expect(storageLabel("other")).toBe("other");
  });

  it("storageSummary reports the worst condition", () => {
    expect(
      storageSummary([ok, { ...ok, category: "audit_log", writesFailing: true }]),
    ).toMatchObject({
      value: "writes failing",
      tone: "down",
      tooltip: "Writes failing: Audit log",
    });
    expect(storageSummary([ok, { ...ok, category: "acme", state: "critical" }])).toMatchObject({
      value: "critical",
      tone: "down",
    });
    expect(storageSummary([ok, { ...ok, category: "acme", state: "low" }])).toMatchObject({
      value: "low",
      tone: "warn",
    });
    expect(storageSummary([{ ...ok, state: "unavailable" }])).toMatchObject({
      value: "unknown",
      tone: "idle",
    });
    expect(storageSummary([ok, { ...ok, category: "acme", state: "error" }])).toMatchObject({
      value: "ok",
      tooltip: "Headroom ok; unavailable for ACME certificates",
    });
    expect(storageSummary([ok]).tooltip).toBe("Headroom ok for every Jul-owned storage category");
  });
});

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

describe("OverviewPanel storage integration", () => {
  afterEach(() => vi.restoreAllMocks());

  it("adds a Storage summary chip and section when storage is reported", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            product: "Jul.IA",
            version: "2.0.0",
            status: [],
            stats: baseStats({
              storage: [
                {
                  category: "cache",
                  state: "low",
                  availableBytes: GiB,
                  totalBytes: 20 * GiB,
                  availableRatio: 0.05,
                },
              ],
              storageHints: hints,
            }),
          }),
      }),
    );
    render(<OverviewPanel />, { wrapper: Wrapper });
    const chip = await screen.findByRole("button", { name: /Storage: low/ });
    chip.click();
    expect(scroll).toHaveBeenCalled();
    expect(await screen.findByRole("heading", { name: "Storage", level: 2 })).toBeInTheDocument();
  });

  it("omits the Storage chip when nothing is configured", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({ product: "Jul.IA", version: "2.0.0", status: [], stats: baseStats() }),
      }),
    );
    render(<OverviewPanel />, { wrapper: Wrapper });
    await screen.findByText("Runtime Resources");
    expect(screen.queryByRole("button", { name: /Storage:/ })).not.toBeInTheDocument();
  });
});
