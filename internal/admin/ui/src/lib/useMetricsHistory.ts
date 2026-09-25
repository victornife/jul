/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useRef, useState } from "react";
import type { StatsSnapshot } from "@/api/client";

/**
 * MetricsHistory is the shape returned by useMetricsHistory. The six metric
 * keys match MetricKey in metricMeta.ts so the history object can be indexed
 * directly by a MetricKey value.
 */
export interface MetricsHistory {
  requestsPerSec: number[];
  latencyAvg: number[];
  latencyP95: number[];
  inFlight: number[];
  errorRate: number[];
  cacheHitRatio: number[];
  /** Wall-clock timestamps (Date.now()) aligned with each metric sample. */
  timestamps: number[];
  // ── Runtime resources and capacity (#431) ────────────────────────────────
  // cpuCores/rssBytes/goroutines mirror the corresponding StatsSnapshot
  // fields when available, using NaN as the "no sample this poll" hole so a
  // gap in the sparkline is visible rather than silently interpolated to 0.
  cpuCores: number[];
  rssBytes: number[];
  goroutines: number[];
  // httpBytesPerSec is derived here, client-side, from consecutive
  // httpResponseBytesTotal counter values (#431 §25) rather than the server
  // keeping a second rolling series. 0 on the first sample, on a counter
  // reset, or when the poll interval collapses to ~0.
  httpBytesPerSec: number[];
}

/**
 * useMetricsHistory tracks metrics over time, maintaining rolling windows
 * for sparkline visualization. Defaults to a 60-sample window (2 minutes at 2s poll).
 */
export function useMetricsHistory(
  stats: StatsSnapshot | undefined,
  windowSize = 60,
): MetricsHistory {
  const bufferRef = useRef<MetricsHistory>({
    requestsPerSec: [],
    latencyAvg: [],
    latencyP95: [],
    inFlight: [],
    errorRate: [],
    cacheHitRatio: [],
    timestamps: [],
    cpuCores: [],
    rssBytes: [],
    goroutines: [],
    httpBytesPerSec: [],
  });
  // lastBytes tracks the previous (value, wall-clock-ms) sample for the
  // client-derived HTTP bytes/sec rate, independent of the render-triggering
  // buffer above.
  const lastBytesRef = useRef<{ value: number; atMs: number } | null>(null);

  const [history, setHistory] = useState<MetricsHistory>(bufferRef.current);

  useEffect(() => {
    if (!stats?.available) return;

    const buffer = bufferRef.current;
    const now = Date.now();

    // Append new samples and maintain window size. The plan calls for trends of
    // request rate, error rate, p95 latency, and in-flight requests, so those
    // are tracked alongside the average latency and cache-hit ratio.
    // timestamps is aligned with the other arrays so ChartDetailPanel can
    // show the exact wall-clock time of each data point.
    buffer.requestsPerSec.push(stats.requestsPerSec);
    buffer.latencyAvg.push(stats.latencyAvgMs);
    buffer.latencyP95.push(stats.latencyP95Ms);
    buffer.inFlight.push(stats.inFlight);
    buffer.errorRate.push(stats.errorRate);
    buffer.cacheHitRatio.push(stats.cacheHitRatio);
    buffer.timestamps.push(now);
    buffer.cpuCores.push(stats.cpuCores ?? NaN);
    buffer.rssBytes.push(stats.rssBytes ?? NaN);
    buffer.goroutines.push(stats.goroutines ?? NaN);

    const bytesNow = stats.httpResponseBytesTotal;
    const last = lastBytesRef.current;
    let bytesPerSec = 0;
    if (last && now > last.atMs) {
      const deltaBytes = bytesNow - last.value;
      const deltaSeconds = (now - last.atMs) / 1000;
      if (deltaBytes > 0 && deltaSeconds > 0.001) {
        bytesPerSec = deltaBytes / deltaSeconds;
      }
      // A negative delta is a counter reset (reload); a non-positive interval
      // or no traffic both correctly fall through to the 0 initialized above.
    }
    lastBytesRef.current = { value: bytesNow, atMs: now };
    buffer.httpBytesPerSec.push(bytesPerSec);

    // Trim to window size, keeping most recent samples
    if (buffer.requestsPerSec.length > windowSize) {
      buffer.requestsPerSec = buffer.requestsPerSec.slice(-windowSize);
      buffer.latencyAvg = buffer.latencyAvg.slice(-windowSize);
      buffer.latencyP95 = buffer.latencyP95.slice(-windowSize);
      buffer.inFlight = buffer.inFlight.slice(-windowSize);
      buffer.errorRate = buffer.errorRate.slice(-windowSize);
      buffer.cacheHitRatio = buffer.cacheHitRatio.slice(-windowSize);
      buffer.timestamps = buffer.timestamps.slice(-windowSize);
      buffer.cpuCores = buffer.cpuCores.slice(-windowSize);
      buffer.rssBytes = buffer.rssBytes.slice(-windowSize);
      buffer.goroutines = buffer.goroutines.slice(-windowSize);
      buffer.httpBytesPerSec = buffer.httpBytesPerSec.slice(-windowSize);
    }

    // Trigger re-render with new reference
    setHistory({ ...buffer });
  }, [stats, windowSize]);

  return history;
}
