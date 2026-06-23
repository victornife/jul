import { useEffect, useRef, useState } from "react";
import type { StatsSnapshot } from "@/api/client";

/**
 * useMetricsHistory tracks metrics over time, maintaining rolling windows
 * for sparkline visualization. Defaults to a 60-sample window (2 minutes at 2s poll).
 */
export function useMetricsHistory(
  stats: StatsSnapshot | undefined,
  windowSize = 60
) {
  const bufferRef = useRef<{
    requestsPerSec: number[];
    latencyAvg: number[];
    errorRate: number[];
    cacheHitRatio: number[];
  }>({
    requestsPerSec: [],
    latencyAvg: [],
    errorRate: [],
    cacheHitRatio: [],
  });

  const [history, setHistory] = useState(bufferRef.current);

  useEffect(() => {
    if (!stats?.available) return;

    const buffer = bufferRef.current;

    // Append new samples and maintain window size
    buffer.requestsPerSec.push(stats.requestsPerSec);
    buffer.latencyAvg.push(stats.latencyAvgMs);
    buffer.errorRate.push(stats.errorRate);
    buffer.cacheHitRatio.push(stats.cacheHitRatio);

    // Trim to window size, keeping most recent samples
    if (buffer.requestsPerSec.length > windowSize) {
      buffer.requestsPerSec = buffer.requestsPerSec.slice(-windowSize);
      buffer.latencyAvg = buffer.latencyAvg.slice(-windowSize);
      buffer.errorRate = buffer.errorRate.slice(-windowSize);
      buffer.cacheHitRatio = buffer.cacheHitRatio.slice(-windowSize);
    }

    // Trigger re-render with new reference
    setHistory({ ...buffer });
  }, [stats, windowSize]);

  return history;
}