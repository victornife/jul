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
    latencyP95: number[];
    inFlight: number[];
    errorRate: number[];
    cacheHitRatio: number[];
  }>({
    requestsPerSec: [],
    latencyAvg: [],
    latencyP95: [],
    inFlight: [],
    errorRate: [],
    cacheHitRatio: [],
  });

  const [history, setHistory] = useState(bufferRef.current);

  useEffect(() => {
    if (!stats?.available) return;

    const buffer = bufferRef.current;

    // Append new samples and maintain window size. The plan calls for trends of
    // request rate, error rate, p95 latency, and in-flight requests, so those
    // are tracked alongside the average latency and cache-hit ratio.
    buffer.requestsPerSec.push(stats.requestsPerSec);
    buffer.latencyAvg.push(stats.latencyAvgMs);
    buffer.latencyP95.push(stats.latencyP95Ms);
    buffer.inFlight.push(stats.inFlight);
    buffer.errorRate.push(stats.errorRate);
    buffer.cacheHitRatio.push(stats.cacheHitRatio);

    // Trim to window size, keeping most recent samples
    if (buffer.requestsPerSec.length > windowSize) {
      buffer.requestsPerSec = buffer.requestsPerSec.slice(-windowSize);
      buffer.latencyAvg = buffer.latencyAvg.slice(-windowSize);
      buffer.latencyP95 = buffer.latencyP95.slice(-windowSize);
      buffer.inFlight = buffer.inFlight.slice(-windowSize);
      buffer.errorRate = buffer.errorRate.slice(-windowSize);
      buffer.cacheHitRatio = buffer.cacheHitRatio.slice(-windowSize);
    }

    // Trigger re-render with new reference
    setHistory({ ...buffer });
  }, [stats, windowSize]);

  return history;
}
