/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * MetricKey identifies the six time-series metrics tracked by useMetricsHistory.
 * The values match the buffer field names exactly so MetricsHistory can be
 * indexed by a MetricKey without a cast.
 */
export type MetricKey =
  | "requestsPerSec"
  | "latencyAvg"
  | "latencyP95"
  | "inFlight"
  | "errorRate"
  | "cacheHitRatio";

export interface MetricThreshold {
  warn: number;
  danger: number;
  /** Label shown on the threshold line in the expanded chart. */
  label: string;
  /**
   * When true, values *below* the threshold are bad (e.g. cache hit ratio).
   * Health status logic is inverted accordingly.
   */
  invertedScale?: boolean;
}

/**
 * MetricMeta is the single source of truth for how a metric is displayed.
 * Every chart, tooltip, axis label, and summary derives its strings, units,
 * and thresholds from here — nothing is hard-coded in component files.
 */
export interface MetricMeta {
  key: MetricKey;
  /** Human-readable chart title. */
  name: string;
  /** One-sentence plain-English description shown in the expanded chart. */
  description: string;
  /** Short unit string, e.g. "req/s" or "ms". */
  unit: string;
  /** X-axis label for the expanded chart. */
  xAxisLabel: string;
  /** Y-axis label including unit for the expanded chart. */
  yAxisLabel: string;
  /** Formats a raw value for tooltips, summaries, and the hover readout. */
  formatValue: (v: number) => string;
  /** Shorter formatter for axis ticks where space is limited. */
  formatYAxis: (v: number) => string;
  thresholds?: MetricThreshold;
  /** If set, a Configure action in the expanded chart navigates here. */
  configRoute?: string;
  /** SVG/CSS colour for the polyline and chart accent. */
  color: string;
}

export const METRIC_META: Record<MetricKey, MetricMeta> = {
  requestsPerSec: {
    key: "requestsPerSec",
    name: "Request Rate",
    description:
      "Number of HTTP requests this node is completing per second, averaged over the most recent poll interval.",
    unit: "req/s",
    xAxisLabel: "Time",
    yAxisLabel: "Requests per second (req/s)",
    formatValue: (v) => `${v.toFixed(1)} req/s`,
    formatYAxis: (v) => v.toFixed(1),
    color: "rgb(34, 197, 94)",
  },

  errorRate: {
    key: "errorRate",
    name: "Error Rate",
    description:
      "Proportion of responses returning a 5xx status code; values above 0% indicate server-side failures.",
    unit: "%",
    xAxisLabel: "Time",
    yAxisLabel: "Error rate (%)",
    formatValue: (v) => `${(v * 100).toFixed(2)}%`,
    formatYAxis: (v) => `${(v * 100).toFixed(1)}%`,
    thresholds: { warn: 0.001, danger: 0.05, label: "Error threshold" },
    color: "rgb(239, 68, 68)",
  },

  latencyP95: {
    key: "latencyP95",
    name: "P95 Latency",
    description:
      "The response time below which 95% of requests complete; a reliable indicator of tail latency experienced by most users.",
    unit: "ms",
    xAxisLabel: "Time",
    yAxisLabel: "P95 latency (ms)",
    formatValue: (v) => `${v.toFixed(1)} ms`,
    formatYAxis: (v) => v.toFixed(0),
    thresholds: { warn: 250, danger: 1000, label: "P95 threshold" },
    color: "rgb(59, 130, 246)",
  },

  inFlight: {
    key: "inFlight",
    name: "In-flight Requests",
    description:
      "Number of requests currently being processed; sustained high values may indicate upstream slowness or thread exhaustion.",
    unit: "requests",
    xAxisLabel: "Time",
    yAxisLabel: "In-flight requests",
    formatValue: (v) => `${Math.round(v)} req`,
    formatYAxis: (v) => Math.round(v).toString(),
    color: "rgb(234, 179, 8)",
  },

  latencyAvg: {
    key: "latencyAvg",
    name: "Avg Latency",
    description:
      "Mean response time across all requests in the most recent interval; sensitive to outliers, so compare with P95.",
    unit: "ms",
    xAxisLabel: "Time",
    yAxisLabel: "Average latency (ms)",
    formatValue: (v) => `${v.toFixed(1)} ms`,
    formatYAxis: (v) => v.toFixed(0),
    thresholds: { warn: 200, danger: 800, label: "Latency threshold" },
    color: "rgb(14, 165, 233)",
  },

  cacheHitRatio: {
    key: "cacheHitRatio",
    name: "Cache Hit Ratio",
    description:
      "Fraction of requests served from cache rather than forwarded upstream; a low ratio increases backend load and response times.",
    unit: "%",
    xAxisLabel: "Time",
    yAxisLabel: "Cache hit ratio (%)",
    formatValue: (v) => `${(v * 100).toFixed(1)}%`,
    formatYAxis: (v) => `${(v * 100).toFixed(0)}%`,
    thresholds: { warn: 0.5, danger: 0.2, label: "Hit ratio", invertedScale: true },
    configRoute: "/traffic",
    color: "rgb(168, 85, 247)",
  },
};

/**
 * Ordered list for consistent sparkline card rendering.
 * Matches the previous manual layout: request rate, errors, P95, in-flight,
 * avg latency, cache hit ratio.
 */
export const METRIC_META_LIST: MetricMeta[] = [
  METRIC_META.requestsPerSec,
  METRIC_META.errorRate,
  METRIC_META.latencyP95,
  METRIC_META.inFlight,
  METRIC_META.latencyAvg,
  METRIC_META.cacheHitRatio,
];
