import { useQuery } from "@tanstack/react-query";
import { fetchOverview, type FeatureStatus } from "@/api/client.ts";

// Group status rows by their `group` field.
function groupBy<T>(items: T[], key: (item: T) => string): Map<string, T[]> {
  const m = new Map<string, T[]>();
  for (const item of items) {
    const k = key(item);
    const existing = m.get(k) ?? [];
    existing.push(item);
    m.set(k, existing);
  }
  return m;
}

function StatusBadge({ active }: { readonly active: boolean }) {
  return (
    <span
      className={`inline-block rounded-full px-2 py-0.5 text-xs font-medium ${
        active
          ? "bg-jul-success/15 text-jul-success"
          : "bg-jul-border text-jul-muted"
      }`}
    >
      {active ? "active" : "inactive"}
    </span>
  );
}

function MetricCard({
  label,
  value,
  unit,
  subtext,
}: {
  readonly label: string;
  readonly value: number | string;
  readonly unit?: string;
  readonly subtext?: string;
}) {
  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface p-4">
      <div className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
        {label}
      </div>
      <div className="mt-2 flex items-baseline gap-2">
        <div className="text-2xl font-bold text-jul-text">
          {typeof value === "number" ? value.toLocaleString() : value}
        </div>
        {unit && <div className="text-sm text-jul-muted">{unit}</div>}
      </div>
      {subtext && <div className="mt-1 text-xs text-jul-muted">{subtext}</div>}
    </div>
  );
}

function StatusGroup({
  name,
  rows,
}: {
  readonly name: string;
  readonly rows: FeatureStatus[];
}) {
  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      <div className="border-b border-jul-border px-4 py-2">
        <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
          {name}
        </span>
      </div>
      <ul>
        {rows.map((row) => (
          <li
            key={row.name}
            className="flex items-center gap-3 border-b border-jul-border px-4 py-3 last:border-b-0"
          >
            <StatusBadge active={row.active} />
            <span className="flex-1 text-sm text-jul-text">{row.name}</span>
            {row.detail !== undefined && (
              <span className="text-xs text-jul-muted">{row.detail}</span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

export function OverviewPanel() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["overview"],
    queryFn: fetchOverview,
    refetchInterval: 2000, // Poll every 2 seconds per Milestone 1.1
  });

  if (isLoading) {
    return <div className="text-jul-muted">Loading overview…</div>;
  }
  if (isError || !data) {
    return <div className="text-jul-danger">Failed to load overview.</div>;
  }

  const groups = groupBy(data.status, (r) => r.group);
  const stats = data.stats;

  // Format uptime
  const uptimeHours = Math.floor((stats?.uptimeSeconds ?? 0) / 3600);
  const uptimeMinutes = Math.floor(((stats?.uptimeSeconds ?? 0) % 3600) / 60);
  const uptimeDisplay =
    uptimeHours > 0
      ? `${uptimeHours}h ${uptimeMinutes}m`
      : `${uptimeMinutes}m`;

  // Calculate percentage for error rate
  const errorRatePercent = ((stats?.errorRate ?? 0) * 100).toFixed(1);

  // Calculate cache hit percentage
  const cacheHitPercent = ((stats?.cacheHitRatio ?? 0) * 100).toFixed(1);

  return (
    <div className="space-y-6">
      <div className="flex items-baseline gap-3">
        <h1 className="text-xl font-semibold">{data.product}</h1>
        {data.version && (
          <span className="text-xs text-jul-muted">v{data.version}</span>
        )}
      </div>

      {/* Live Traffic Cards */}
      {stats?.available && (
        <div className="space-y-4">
          <h2 className="text-sm font-semibold text-jul-muted">
            Live Traffic
          </h2>

          {/* Top Row: Key Metrics */}
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <MetricCard
              label="Uptime"
              value={uptimeDisplay}
              subtext={`${(stats.uptimeSeconds || 0).toFixed(0)}s`}
            />
            <MetricCard
              label="Requests/sec"
              value={(stats.requestsPerSec || 0).toFixed(2)}
              unit="req/s"
              subtext={`${(stats.requestsTotal || 0).toLocaleString()} total`}
            />
            <MetricCard
              label="In-flight"
              value={Math.round(stats.inFlight || 0)}
              unit="requests"
            />
            <MetricCard
              label="Active Connections"
              value={Math.round(stats.connections || 0)}
              unit="conns"
            />
          </div>

          {/* Latency Row */}
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <MetricCard
              label="Avg Latency"
              value={(stats.latencyAvgMs || 0).toFixed(1)}
              unit="ms"
            />
            <MetricCard
              label="P50 Latency"
              value={(stats.latencyP50Ms || 0).toFixed(1)}
              unit="ms"
            />
            <MetricCard
              label="P95 Latency"
              value={(stats.latencyP95Ms || 0).toFixed(1)}
              unit="ms"
            />
            <MetricCard
              label="P99 Latency"
              value={(stats.latencyP99Ms || 0).toFixed(1)}
              unit="ms"
            />
          </div>

          {/* Error Rate and Status Classes */}
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <MetricCard
              label="Error Rate (5xx)"
              value={errorRatePercent}
              unit="%"
              subtext={`${Math.round(stats.statusClasses?.["5xx"] || 0).toLocaleString()} errors`}
            />
            <MetricCard
              label="2xx Success"
              value={Math.round(
                stats.statusClasses?.["2xx"] || 0
              ).toLocaleString()}
              unit="responses"
            />
            <MetricCard
              label="4xx Client Errors"
              value={Math.round(
                stats.statusClasses?.["4xx"] || 0
              ).toLocaleString()}
              unit="responses"
            />
            <MetricCard
              label="3xx Redirects"
              value={Math.round(
                stats.statusClasses?.["3xx"] || 0
              ).toLocaleString()}
              unit="responses"
            />
          </div>

          {/* Cache Stats */}
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <MetricCard
              label="Cache Hit Ratio"
              value={cacheHitPercent}
              unit="%"
              subtext={`${Math.round(stats.cacheEvents?.["HIT"] || 0).toLocaleString()} hits`}
            />
            <MetricCard
              label="Cache Misses"
              value={Math.round(
                stats.cacheEvents?.["MISS"] || 0
              ).toLocaleString()}
              unit="events"
            />
            <MetricCard
              label="Cache Bypasses"
              value={Math.round(
                stats.cacheEvents?.["BYPASS"] || 0
              ).toLocaleString()}
              unit="events"
            />
          </div>
        </div>
      )}

      {/* Feature Status */}
      {groups.size === 0 ? (
        <p className="text-jul-muted text-sm">No status rows available.</p>
      ) : (
        <div className="space-y-4">
          <h2 className="text-sm font-semibold text-jul-muted">
            Capabilities & Configuration
          </h2>
          <div className="grid gap-4 lg:grid-cols-2">
            {Array.from(groups.entries()).map(([group, rows]) => (
              <StatusGroup key={group} name={group} rows={rows} />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

