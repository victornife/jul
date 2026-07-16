/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Sparkline } from "@/components/Sparkline";
import { Modal } from "@/components/ui.tsx";
import { type MetricKey, METRIC_META } from "@/lib/metricMeta";
import { computeMetricSummary } from "@/lib/computeMetricSummary";

function formatTimestamp(ms: number): string {
  return new Date(ms).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    timeZoneName: "short",
  });
}

const HEALTH_TEXT: Record<string, string> = {
  healthy: "● Healthy",
  degraded: "● Degraded",
  critical: "● Critical",
  unknown: "○ Unknown",
};

const HEALTH_CLASS: Record<string, string> = {
  healthy: "text-jul-success",
  degraded: "text-jul-warning",
  critical: "text-jul-danger",
  unknown: "text-jul-muted",
};

const TREND_TEXT: Record<string, string> = {
  rising: "▲ Rising",
  falling: "▼ Falling",
  stable: "→ Stable",
};

const VOLATILITY_TEXT: Record<string, string> = {
  high: "High",
  medium: "Medium",
  low: "Low",
};

/**
 * ChartDetailPanel renders the expanded view for any Overview sparkline.
 * It receives a MetricKey (not raw strings) and derives all display metadata
 * from METRIC_META, so nothing is hard-coded per metric.
 */
export function ChartDetailPanel({
  metricKey,
  data,
  timestamps,
  onClose,
}: {
  readonly metricKey: MetricKey;
  readonly data: number[];
  readonly timestamps: number[];
  readonly onClose: () => void;
}) {
  const meta = METRIC_META[metricKey];
  const navigate = useNavigate();

  const [hoverInfo, setHoverInfo] = useState<{
    idx: number;
    value: number;
  } | null>(null);
  const [copied, setCopied] = useState(false);

  const summary = computeMetricSummary(data, meta);

  const timeRange =
    timestamps.length > 0
      ? {
          start: formatTimestamp(timestamps[0] ?? 0),
          end: formatTimestamp(timestamps[timestamps.length - 1] ?? 0),
        }
      : null;

  const thresholdLines = meta.thresholds
    ? [
        {
          value: meta.thresholds.warn,
          color: "rgb(234, 179, 8)",
          label: "Warn",
        },
        {
          value: meta.thresholds.danger,
          color: "rgb(239, 68, 68)",
          label: "Critical",
        },
      ]
    : [];

  function handleExport(): void {
    const lines = ["timestamp_ms,timestamp_local,value"];
    for (let i = 0; i < data.length; i++) {
      const ts = timestamps[i];
      lines.push(
        `${ts !== undefined ? String(ts) : ""},${ts !== undefined ? new Date(ts).toISOString() : ""},${String(data[i] ?? 0)}`,
      );
    }
    const csv = lines.join("\n");
    void navigator.clipboard.writeText(csv).then(() => {
      setCopied(true);
      setTimeout(() => {
        setCopied(false);
      }, 2000);
    });
  }

  const footer = (
    <>
      {meta.configRoute !== undefined && (
        <button
          type="button"
          className="rounded-md px-3 py-1.5 text-sm text-jul-accent hover:bg-jul-accent/10 focus:outline-none focus-visible:ring-2 focus-visible:ring-jul-accent"
          onClick={() => {
            const route = meta.configRoute;
            if (route !== undefined) void navigate(route);
          }}
        >
          Configure →
        </button>
      )}
      <button
        type="button"
        className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-muted hover:text-jul-text focus:outline-none focus-visible:ring-2 focus-visible:ring-jul-accent"
        title="Copy chart data as CSV"
        onClick={handleExport}
      >
        {copied ? "Copied ✓" : "Export CSV"}
      </button>
    </>
  );

  return (
    <Modal title={meta.name} onClose={onClose} footer={footer}>
      <div className="flex flex-col gap-4">
        {/* Metric metadata: description, axis labels, time range */}
        <div className="space-y-1 text-xs text-jul-muted">
          <p>{meta.description}</p>
          <p>
            <span>X: {meta.xAxisLabel}</span>
            <span className="mx-2 opacity-40">·</span>
            <span>Y: {meta.yAxisLabel}</span>
          </p>
          {timeRange !== null && (
            <p className="font-mono">
              {timeRange.start} → {timeRange.end}
              <span className="mx-2 opacity-40">·</span>
              {data.length} samples
            </p>
          )}
        </div>

        {/* Chart + hover readout */}
        {data.length < 2 ? (
          <div className="flex h-40 items-center justify-center text-sm text-jul-muted">
            Collecting data…
          </div>
        ) : (
          <div>
            <Sparkline
              data={data}
              height={200}
              width={600}
              color={meta.color}
              className="w-full"
              ariaLabel={`${meta.name} detail chart, ${String(data.length)} data points. Use arrow keys to step through values.`}
              onPointHover={(idx, value) => {
                setHoverInfo(
                  idx !== null && value !== null ? { idx, value } : null,
                );
              }}
              thresholds={thresholdLines}
            />
            {/* aria-live region announces the hovered value to screen readers */}
            <div
              className="mt-1 h-5 text-xs text-jul-muted"
              aria-live="polite"
              aria-atomic="true"
            >
              {hoverInfo !== null &&
              timestamps[hoverInfo.idx] !== undefined
                ? `${formatTimestamp(timestamps[hoverInfo.idx] ?? 0)} · ${meta.formatValue(hoverInfo.value)}`
                : "Hover or use arrow keys to inspect values"}
            </div>
          </div>
        )}

        {/* Summary */}
        <div className="rounded-lg border border-jul-border p-4">
          <h3 className="mb-3 text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Summary
          </h3>

          {summary.insufficientData ? (
            <p className="text-xs text-jul-muted">
              Insufficient data for trend analysis ({String(data.length)}{" "}
              samples — need at least 10).
            </p>
          ) : (
            <div className="space-y-3">
              {/* Current value, trend, volatility, health */}
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                <div>
                  <div className="text-xs text-jul-muted">Current</div>
                  <div className="font-semibold text-jul-text">
                    {meta.formatValue(summary.current)}
                  </div>
                  <div className="text-xs text-jul-muted">
                    {summary.delta >= 0 ? "+" : "−"}
                    {meta.formatValue(Math.abs(summary.delta))}
                    {summary.deltaPercent !== null
                      ? ` (${summary.delta >= 0 ? "+" : "−"}${Math.abs(summary.deltaPercent).toFixed(1)}%)`
                      : ""}{" "}
                    vs start
                  </div>
                </div>
                <div>
                  <div className="text-xs text-jul-muted">Trend</div>
                  <div className="font-semibold text-jul-text">
                    {TREND_TEXT[summary.trend]}
                  </div>
                </div>
                <div>
                  <div className="text-xs text-jul-muted">Volatility</div>
                  <div className="font-semibold text-jul-text">
                    {VOLATILITY_TEXT[summary.volatility]}
                  </div>
                </div>
                <div>
                  <div className="text-xs text-jul-muted">Status</div>
                  <div
                    className={`font-semibold ${HEALTH_CLASS[summary.healthStatus] ?? ""}`}
                  >
                    {HEALTH_TEXT[summary.healthStatus]}
                  </div>
                </div>
              </div>

              {/* Statistical distribution */}
              <div className="grid grid-cols-5 gap-2 border-t border-jul-border pt-3">
                {(
                  [
                    { label: "Min", value: summary.min },
                    { label: "Avg", value: summary.avg },
                    { label: "Median", value: summary.median },
                    { label: "P95", value: summary.p95 },
                    { label: "Max", value: summary.max },
                  ] as const
                ).map(({ label, value }) => (
                  <div key={label}>
                    <div className="text-xs text-jul-muted">{label}</div>
                    <div className="text-sm font-medium text-jul-text">
                      {meta.formatValue(value)}
                    </div>
                  </div>
                ))}
              </div>

              {/* Anomaly counts */}
              {(summary.spikes.length > 0 || summary.drops.length > 0) && (
                <div className="flex gap-4 border-t border-jul-border pt-2 text-xs">
                  {summary.spikes.length > 0 && (
                    <span className="text-jul-warning">
                      {String(summary.spikes.length)} spike
                      {summary.spikes.length !== 1 ? "s" : ""} detected
                    </span>
                  )}
                  {summary.drops.length > 0 && (
                    <span className="text-jul-warning">
                      {String(summary.drops.length)} drop
                      {summary.drops.length !== 1 ? "s" : ""} detected
                    </span>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </Modal>
  );
}
