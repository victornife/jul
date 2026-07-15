/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useRef, useState } from "react";

export interface SparklineThreshold {
  value: number;
  color: string;
  label: string;
}

/**
 * Sparkline is a compact SVG line chart for visualizing small time-series data.
 *
 * Compact mode (default): renders a static polyline — no interaction. The
 * wrapper element is responsible for click / focus handling.
 *
 * Interactive mode: activated by supplying an `onPointHover` callback. Adds
 * pointer tracking, a vertical hairline, a value circle, and ArrowLeft /
 * ArrowRight keyboard navigation. Threshold lines are rendered when supplied.
 */
export function Sparkline({
  data,
  height = 30,
  width = 100,
  strokeWidth = 2,
  color = "currentColor",
  className = "",
  ariaLabel,
  onPointHover,
  thresholds,
}: {
  readonly data: number[];
  readonly height?: number;
  readonly width?: number;
  readonly strokeWidth?: number;
  readonly color?: string;
  readonly className?: string;
  /** Required when the chart is interactive (onPointHover supplied). */
  readonly ariaLabel?: string;
  /**
   * Called with the nearest data index and value when the pointer moves over
   * the chart, or (null, null) when the pointer leaves. Activates interactive
   * mode when provided.
   */
  readonly onPointHover?: (index: number | null, value: number | null) => void;
  /** Optional horizontal threshold lines drawn across the chart. */
  readonly thresholds?: SparklineThreshold[];
}) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [activeIdx, setActiveIdx] = useState<number | null>(null);

  const isInteractive = Boolean(onPointHover);

  if (data.length < 2) {
    return (
      <svg
        width={width}
        height={height}
        className={className}
        role="img"
        aria-label={ariaLabel ?? "No data"}
      >
        <text
          x={width / 2}
          y={height / 2}
          textAnchor="middle"
          dominantBaseline="middle"
          className="fill-current text-xs text-jul-muted"
        >
          —
        </text>
      </svg>
    );
  }

  const padding = 2;
  const availableWidth = width - 2 * padding;
  const availableHeight = height - 2 * padding;

  // Scale including threshold values so lines are visible even when all data
  // is above or below a threshold.
  const thresholdValues = thresholds?.map((t) => t.value) ?? [];
  const allValues = [...data, ...thresholdValues];
  const min = Math.min(...allValues);
  const max = Math.max(...allValues);
  const range = max - min || 1;

  function toY(value: number): number {
    return padding + availableHeight - ((value - min) / range) * availableHeight;
  }

  function toX(i: number): number {
    return padding + (i / (data.length - 1)) * availableWidth;
  }

  const points: string[] = data.map((v, i) => `${String(toX(i))},${String(toY(v))}`);

  function nearestIndex(clientX: number): number {
    if (!svgRef.current) return 0;
    const rect = svgRef.current.getBoundingClientRect();
    const ratio = (clientX - rect.left) / rect.width;
    return Math.max(0, Math.min(data.length - 1, Math.round(ratio * (data.length - 1))));
  }

  function triggerHover(idx: number | null): void {
    setActiveIdx(idx);
    onPointHover?.(idx, idx !== null ? (data[idx] ?? null) : null);
  }

  function handlePointerMove(e: React.PointerEvent<SVGSVGElement>): void {
    triggerHover(nearestIndex(e.clientX));
  }

  function handlePointerLeave(): void {
    triggerHover(null);
  }

  function handleKeyDown(e: React.KeyboardEvent<SVGSVGElement>): void {
    if (e.key === "ArrowRight") {
      e.preventDefault();
      const next =
        activeIdx === null ? 0 : Math.min(data.length - 1, activeIdx + 1);
      triggerHover(next);
    } else if (e.key === "ArrowLeft") {
      e.preventDefault();
      const prev =
        activeIdx === null
          ? data.length - 1
          : Math.max(0, activeIdx - 1);
      triggerHover(prev);
    } else if (e.key === "Escape") {
      triggerHover(null);
      svgRef.current?.blur();
    }
  }

  const hoverX = activeIdx !== null ? toX(activeIdx) : null;
  const hoverY = activeIdx !== null ? toY(data[activeIdx]!) : null;

  return (
    <svg
      ref={svgRef}
      width={width}
      height={height}
      className={`${isInteractive ? "cursor-crosshair" : ""} ${className}`}
      viewBox={`0 0 ${String(width)} ${String(height)}`}
      preserveAspectRatio="none"
      style={{ display: "block" }}
      role="img"
      aria-label={ariaLabel}
      tabIndex={isInteractive ? 0 : undefined}
      onPointerMove={isInteractive ? handlePointerMove : undefined}
      onPointerLeave={isInteractive ? handlePointerLeave : undefined}
      onKeyDown={isInteractive ? handleKeyDown : undefined}
    >
      {/* Threshold lines */}
      {thresholds?.map((t) => {
        const ty = toY(t.value);
        return (
          <g key={t.label}>
            <line
              x1={padding}
              y1={ty}
              x2={width - padding}
              y2={ty}
              stroke={t.color}
              strokeWidth={1}
              strokeDasharray="3,3"
              vectorEffect="non-scaling-stroke"
            />
            <text
              x={width - padding - 1}
              y={ty - 2}
              textAnchor="end"
              fontSize={7}
              fill={t.color}
              style={{ pointerEvents: "none" }}
            >
              {t.label}
            </text>
          </g>
        );
      })}

      {/* Data line */}
      <polyline
        points={points.join(" ")}
        fill="none"
        stroke={color}
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />

      {/* Hover indicator */}
      {isInteractive && hoverX !== null && hoverY !== null && (
        <g style={{ pointerEvents: "none" }}>
          <line
            x1={hoverX}
            y1={padding}
            x2={hoverX}
            y2={height - padding}
            stroke="currentColor"
            strokeWidth={1}
            strokeOpacity={0.25}
            vectorEffect="non-scaling-stroke"
          />
          <circle
            cx={hoverX}
            cy={hoverY}
            r={3}
            fill={color}
            stroke="var(--color-jul-surface, white)"
            strokeWidth={1.5}
            vectorEffect="non-scaling-stroke"
          />
        </g>
      )}
    </svg>
  );
}