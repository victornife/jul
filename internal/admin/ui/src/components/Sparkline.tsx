/**
 * Sparkline is a compact SVG line chart for visualizing small time-series data.
 * Used to display 2-minute windows of metrics like requests/sec or latency.
 */
export function Sparkline({
  data,
  height = 30,
  width = 100,
  strokeWidth = 2,
  color = "currentColor",
  className = "",
}: {
  readonly data: number[];
  readonly height?: number;
  readonly width?: number;
  readonly strokeWidth?: number;
  readonly color?: string;
  readonly className?: string;
}) {
  if (data.length < 2) {
    return (
      <svg width={width} height={height} className={className}>
        <text
          x={width / 2}
          y={height / 2}
          textAnchor="middle"
          dominantBaseline="middle"
          className="text-xs text-jul-muted fill-current"
        >
          —
        </text>
      </svg>
    );
  }

  // Find min and max for scaling
  const min = Math.min(...data);
  const max = Math.max(...data);
  const range = max - min || 1;

  // Build path data
  const points: string[] = [];
  const padding = 2;
  const availableWidth = width - 2 * padding;
  const availableHeight = height - 2 * padding;

  data.forEach((value, i) => {
    const x = padding + (i / (data.length - 1)) * availableWidth;
    // Invert Y because SVG Y increases downward
    const y =
      padding +
      availableHeight -
      ((value - min) / range) * availableHeight;
    points.push(`${x},${y}`);
  });

  return (
    <svg
      width={width}
      height={height}
      className={className}
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      style={{ display: "block" }}
    >
      <polyline
        points={points.join(" ")}
        fill="none"
        stroke={color}
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}