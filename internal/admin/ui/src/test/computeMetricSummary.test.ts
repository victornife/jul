/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Unit tests for computeMetricSummary (AC16). The function is pure, so every
 * case is exercised without any React or DOM setup.
 *
 * Covered cases: stable, rising, falling, spike detection, drop detection,
 * insufficient data, health status (healthy / degraded / critical), inverted
 * scale thresholds, all-identical values, NaN filtering, and the 0- and
 * 1-sample edge cases.
 */

import { describe, it, expect } from "vitest";
import { computeMetricSummary } from "@/lib/computeMetricSummary";
import type { MetricMeta } from "@/lib/metricMeta";

// ── Helpers ────────────────────────────────────────────────────────────────

/** Minimal MetricMeta stub — only the fields computeMetricSummary reads. */
function makeMeta(overrides: Partial<MetricMeta> = {}): MetricMeta {
  return {
    key: "requestsPerSec",
    name: "Request Rate",
    description: "",
    unit: "req/s",
    xAxisLabel: "Time",
    yAxisLabel: "req/s",
    formatValue: (v) => `${v.toFixed(1)} req/s`,
    formatYAxis: (v) => v.toFixed(1),
    color: "green",
    ...overrides,
  };
}

/** Builds an array of `n` linearly spaced values from `start` to `end`. */
function linspace(start: number, end: number, n: number): number[] {
  return Array.from({ length: n }, (_, i) =>
    n === 1 ? start : start + ((end - start) * i) / (n - 1),
  );
}

// ── Insufficient data ──────────────────────────────────────────────────────

describe("computeMetricSummary — insufficient data", () => {
  it("returns insufficientData=true for an empty array", () => {
    const s = computeMetricSummary([], makeMeta());
    expect(s.insufficientData).toBe(true);
    expect(s.healthStatus).toBe("unknown");
  });

  it("returns insufficientData=true for a single sample", () => {
    const s = computeMetricSummary([5], makeMeta());
    expect(s.insufficientData).toBe(true);
    expect(s.current).toBe(5);
    expect(s.delta).toBe(0);
    expect(s.deltaPercent).toBeNull();
  });

  it("returns insufficientData=true for 2–9 samples but still computes min/max/avg", () => {
    const data = [10, 20, 30];
    const s = computeMetricSummary(data, makeMeta());
    expect(s.insufficientData).toBe(true);
    expect(s.min).toBe(10);
    expect(s.max).toBe(30);
    expect(s.avg).toBeCloseTo(20);
    expect(s.current).toBe(30);
    expect(s.previous).toBe(10);
  });

  it("returns insufficientData=false for exactly 10 samples", () => {
    const s = computeMetricSummary(linspace(1, 10, 10), makeMeta());
    expect(s.insufficientData).toBe(false);
  });
});

// ── Delta and direction ────────────────────────────────────────────────────

describe("computeMetricSummary — delta", () => {
  it("computes positive delta and deltaPercent", () => {
    const data = linspace(10, 20, 20);
    const s = computeMetricSummary(data, makeMeta());
    expect(s.delta).toBeCloseTo(10);
    expect(s.deltaPercent).toBeCloseTo(100);
  });

  it("computes negative delta", () => {
    const data = linspace(20, 10, 20);
    const s = computeMetricSummary(data, makeMeta());
    expect(s.delta).toBeCloseTo(-10);
  });

  it("sets deltaPercent to null when previous is 0", () => {
    const data = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10];
    const s = computeMetricSummary(data, makeMeta());
    expect(s.deltaPercent).toBeNull();
  });
});

// ── Trend ──────────────────────────────────────────────────────────────────

describe("computeMetricSummary — trend", () => {
  it("identifies a rising trend", () => {
    const s = computeMetricSummary(linspace(1, 100, 30), makeMeta());
    expect(s.trend).toBe("rising");
  });

  it("identifies a falling trend", () => {
    const s = computeMetricSummary(linspace(100, 1, 30), makeMeta());
    expect(s.trend).toBe("falling");
  });

  it("identifies a stable trend for flat data", () => {
    const s = computeMetricSummary(Array<number>(30).fill(50), makeMeta());
    expect(s.trend).toBe("stable");
  });

  it("identifies a stable trend for data with minor noise", () => {
    // Values oscillate ±0.01 around 50 — should not be called rising or falling.
    const data = Array.from({ length: 30 }, (_, i) =>
      50 + (i % 2 === 0 ? 0.01 : -0.01),
    );
    const s = computeMetricSummary(data, makeMeta());
    expect(s.trend).toBe("stable");
  });
});

// ── Volatility ─────────────────────────────────────────────────────────────

describe("computeMetricSummary — volatility", () => {
  it("reports low volatility for flat data", () => {
    const s = computeMetricSummary(Array<number>(30).fill(100), makeMeta());
    expect(s.volatility).toBe("low");
  });

  it("reports high volatility for wildly varying data", () => {
    // Alternates between 1 and 100 — stddev/mean >> 0.3.
    const data = Array.from({ length: 30 }, (_, i) => (i % 2 === 0 ? 1 : 100));
    const s = computeMetricSummary(data, makeMeta());
    expect(s.volatility).toBe("high");
  });
});

// ── Statistics ─────────────────────────────────────────────────────────────

describe("computeMetricSummary — statistics", () => {
  it("computes correct min, max, avg for a known dataset", () => {
    const data = [2, 4, 6, 8, 10, 2, 4, 6, 8, 10];
    const s = computeMetricSummary(data, makeMeta());
    expect(s.min).toBe(2);
    expect(s.max).toBe(10);
    expect(s.avg).toBeCloseTo(6);
  });

  it("computes p95 correctly for a uniform ascending sequence", () => {
    // 20 values: 1..20. p95 index = 0.95 * 19 = 18.05 → lerp(19, 20, 0.05) ≈ 19.05.
    const data = Array.from({ length: 20 }, (_, i) => i + 1);
    const s = computeMetricSummary(data, makeMeta());
    expect(s.p95).toBeCloseTo(19.05, 1);
  });

  it("computes median correctly for an odd-length dataset", () => {
    const data = [3, 1, 4, 1, 5, 9, 2, 6, 5, 3, 5]; // sorted: 1,1,2,3,3,4,5,5,5,6,9
    const s = computeMetricSummary(data, makeMeta());
    expect(s.median).toBe(4);
  });
});

// ── Spike and drop detection ───────────────────────────────────────────────

describe("computeMetricSummary — spike and drop detection", () => {
  it("detects a spike that is clearly above avg + 2σ", () => {
    // 29 samples at 10, one sample at 200.
    const data = [...Array<number>(29).fill(10), 200];
    const s = computeMetricSummary(data, makeMeta());
    expect(s.spikes.length).toBeGreaterThan(0);
    expect(s.spikes).toContain(29); // index of the spike
  });

  it("detects a drop that is clearly below avg − 2σ", () => {
    // 29 samples at 100, one sample at 1.
    const data = [...Array<number>(29).fill(100), 1];
    const s = computeMetricSummary(data, makeMeta());
    expect(s.drops.length).toBeGreaterThan(0);
  });

  it("reports no spikes or drops for flat data", () => {
    const s = computeMetricSummary(Array<number>(30).fill(50), makeMeta());
    expect(s.spikes).toHaveLength(0);
    expect(s.drops).toHaveLength(0);
  });
});

// ── Health status (standard scale) ─────────────────────────────────────────

describe("computeMetricSummary — health status (standard scale)", () => {
  const meta = makeMeta({
    thresholds: { warn: 10, danger: 50, label: "Test" },
  });

  it("reports healthy when current is below the warn threshold", () => {
    const data = Array<number>(15).fill(5);
    const s = computeMetricSummary(data, meta);
    expect(s.healthStatus).toBe("healthy");
  });

  it("reports degraded when current is at or above warn but below danger", () => {
    const data = [...Array<number>(14).fill(5), 15];
    const s = computeMetricSummary(data, meta);
    expect(s.healthStatus).toBe("degraded");
  });

  it("reports critical when current is at or above the danger threshold", () => {
    const data = [...Array<number>(14).fill(5), 60];
    const s = computeMetricSummary(data, meta);
    expect(s.healthStatus).toBe("critical");
  });

  it("reports healthy when no thresholds are defined", () => {
    const data = Array<number>(15).fill(999);
    const s = computeMetricSummary(data, makeMeta());
    expect(s.healthStatus).toBe("healthy");
  });
});

// ── Health status (inverted scale — cache hit ratio) ───────────────────────

describe("computeMetricSummary — health status (inverted scale)", () => {
  const meta = makeMeta({
    key: "cacheHitRatio",
    thresholds: { warn: 0.5, danger: 0.2, label: "Hit ratio", invertedScale: true },
  });

  it("reports healthy when current is above the warn threshold", () => {
    const data = Array<number>(15).fill(0.8);
    const s = computeMetricSummary(data, meta);
    expect(s.healthStatus).toBe("healthy");
  });

  it("reports degraded when current is below warn but above danger", () => {
    const data = [...Array<number>(14).fill(0.8), 0.35];
    const s = computeMetricSummary(data, meta);
    expect(s.healthStatus).toBe("degraded");
  });

  it("reports critical when current is below the danger threshold", () => {
    const data = [...Array<number>(14).fill(0.8), 0.1];
    const s = computeMetricSummary(data, meta);
    expect(s.healthStatus).toBe("critical");
  });
});

// ── All-identical values ───────────────────────────────────────────────────

describe("computeMetricSummary — all-identical values", () => {
  it("reports stable trend, low volatility, and no anomalies", () => {
    const s = computeMetricSummary(Array<number>(30).fill(42), makeMeta());
    expect(s.trend).toBe("stable");
    expect(s.volatility).toBe("low");
    expect(s.spikes).toHaveLength(0);
    expect(s.drops).toHaveLength(0);
    expect(s.min).toBe(42);
    expect(s.max).toBe(42);
    expect(s.avg).toBe(42);
    expect(s.delta).toBe(0);
  });
});

// ── NaN / non-finite filtering ─────────────────────────────────────────────

describe("computeMetricSummary — NaN and Infinity filtering", () => {
  it("ignores NaN values and computes from the remaining finite samples", () => {
    const data = [10, NaN, 20, Infinity, 30, NaN, 10, 20, 30, 10, 20, 30];
    const s = computeMetricSummary(data, makeMeta());
    expect(s.min).toBe(10);
    expect(s.max).toBe(30);
    expect(Number.isFinite(s.avg)).toBe(true);
  });

  it("returns insufficientData=true when all values are NaN", () => {
    const s = computeMetricSummary([NaN, NaN, NaN], makeMeta());
    expect(s.insufficientData).toBe(true);
    expect(s.healthStatus).toBe("unknown");
  });
});
