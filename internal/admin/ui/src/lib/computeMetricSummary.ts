/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { MetricMeta } from "@/lib/metricMeta";

export interface MetricSummary {
  current: number;
  /** Value at the start of the window (oldest valid sample). */
  previous: number;
  delta: number;
  /** Percentage change from previous to current, or null when previous is 0. */
  deltaPercent: number | null;
  trend: "rising" | "falling" | "stable";
  volatility: "high" | "medium" | "low";
  min: number;
  max: number;
  avg: number;
  median: number;
  p95: number;
  /** Indices of samples more than 2 standard deviations above the mean. */
  spikes: number[];
  /** Indices of samples more than 2 standard deviations below the mean. */
  drops: number[];
  healthStatus: "healthy" | "degraded" | "critical" | "unknown";
  /**
   * True when fewer than 10 valid samples exist. Trend and volatility claims
   * should not be shown to avoid misleading the operator with noisy data.
   */
  insufficientData: boolean;
}

function sortedPercentile(sorted: number[], p: number): number {
  if (sorted.length === 0) return 0;
  const idx = (p / 100) * (sorted.length - 1);
  const lo = Math.floor(idx);
  const hi = Math.ceil(idx);
  if (lo === hi) return sorted[lo]!;
  return sorted[lo]! + (sorted[hi]! - sorted[lo]!) * (idx - lo);
}

/**
 * Derives a MetricSummary from a raw data array and its MetricMeta descriptor.
 *
 * Pure function — no side effects. Safe to call on every render at the scale
 * of the 60-sample window used by useMetricsHistory (<0.1 ms).
 */
export function computeMetricSummary(
  data: number[],
  meta: MetricMeta,
): MetricSummary {
  const valid = data.filter((v) => Number.isFinite(v));

  if (valid.length < 2) {
    const only = valid[0] ?? 0;
    return {
      current: only,
      previous: only,
      delta: 0,
      deltaPercent: null,
      trend: "stable",
      volatility: "low",
      min: only,
      max: only,
      avg: only,
      median: only,
      p95: only,
      spikes: [],
      drops: [],
      healthStatus: "unknown",
      insufficientData: true,
    };
  }

  const insufficientData = valid.length < 10;

  const current = valid[valid.length - 1]!;
  const previous = valid[0]!;
  const delta = current - previous;
  const deltaPercent =
    previous !== 0 ? (delta / Math.abs(previous)) * 100 : null;

  const min = Math.min(...valid);
  const max = Math.max(...valid);
  const avg = valid.reduce((a, b) => a + b, 0) / valid.length;

  const sorted = [...valid].sort((a, b) => a - b);
  const median = sortedPercentile(sorted, 50);
  const p95 = sortedPercentile(sorted, 95);

  const variance =
    valid.reduce((acc, v) => acc + (v - avg) ** 2, 0) / valid.length;
  const stddev = Math.sqrt(variance);

  const spikes = valid
    .map((v, i) => ({ v, i }))
    .filter(({ v }) => v > avg + 2 * stddev)
    .map(({ i }) => i);

  const drops = valid
    .map((v, i) => ({ v, i }))
    .filter(({ v }) => stddev > 0 && v < avg - 2 * stddev)
    .map(({ i }) => i);

  const volatility: "high" | "medium" | "low" =
    avg === 0
      ? "low"
      : stddev / Math.abs(avg) >= 0.3
        ? "high"
        : stddev / Math.abs(avg) >= 0.1
          ? "medium"
          : "low";

  // Compare the mean of the first third of the window to the last third to
  // determine overall direction. The threshold prevents calling stable data
  // "trending" due to minor noise.
  const third = Math.max(1, Math.floor(valid.length / 3));
  const firstMean =
    valid.slice(0, third).reduce((a, b) => a + b, 0) / third;
  const lastMean =
    valid.slice(-third).reduce((a, b) => a + b, 0) / third;
  const trendThreshold = Math.max(
    stddev * 0.5,
    Math.abs(avg) * 0.03,
    0.0001,
  );
  const trend: "rising" | "falling" | "stable" =
    lastMean - firstMean > trendThreshold
      ? "rising"
      : firstMean - lastMean > trendThreshold
        ? "falling"
        : "stable";

  let healthStatus: "healthy" | "degraded" | "critical" | "unknown" =
    "healthy";
  if (meta.thresholds) {
    const { warn, danger, invertedScale } = meta.thresholds;
    if (!invertedScale) {
      if (current >= danger) healthStatus = "critical";
      else if (current >= warn) healthStatus = "degraded";
    } else {
      if (current < danger) healthStatus = "critical";
      else if (current < warn) healthStatus = "degraded";
    }
  }

  return {
    current,
    previous,
    delta,
    deltaPercent,
    trend,
    volatility,
    min,
    max,
    avg,
    median,
    p95,
    spikes,
    drops,
    healthStatus,
    insufficientData,
  };
}
