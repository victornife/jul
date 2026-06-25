import { useQuery } from "@tanstack/react-query";
import { fetchOverview, type FeatureStatus, type TrafficSources } from "@/api/client.ts";
import { Sparkline } from "@/components/Sparkline";
import { useMetricsHistory } from "@/lib/useMetricsHistory";

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
        active ? "bg-jul-success/15 text-jul-success" : "bg-jul-border text-jul-muted"
      }`}
    >
      {active ? "active" : "inactive"}
    </span>
  );
}

// HealthChip is one signal in the at-a-glance summary band (P3-14): a coarse
// healthy/warn/down tone plus a one-line value, so an operator sees "is anything
// on fire?" before scrolling into the dense metric grids below.
type Tone = "ok" | "warn" | "down" | "idle";

const TONE_CLASS: Record<Tone, string> = {
  ok: "border-jul-success/40 bg-jul-success/10 text-jul-success",
  warn: "border-jul-warning/40 bg-jul-warning/10 text-jul-warning",
  down: "border-jul-danger/40 bg-jul-danger/10 text-jul-danger",
  idle: "border-jul-border bg-jul-surface text-jul-muted",
};

function HealthChip({
  label,
  value,
  tone,
}: {
  readonly label: string;
  readonly value: string;
  readonly tone: Tone;
}) {
  return (
    <div className={`rounded-lg border px-4 py-3 ${TONE_CLASS[tone]}`}>
      <div className="text-[10px] font-semibold uppercase tracking-wider opacity-80">{label}</div>
      <div className="mt-1 text-sm font-semibold">{value}</div>
    </div>
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
      <div className="text-xs font-semibold uppercase tracking-wider text-jul-muted">{label}</div>
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

// TrafficSourcesPanel renders the bounded top-N rollups of where traffic is
// coming from (Milestone 1.4): top hosts, origins, referer hosts, the CORS
// preflight count, and the same/cross-origin split. It answers "who is calling
// me?" without exposing full URLs or any credential.
function TopList({
  title,
  data,
}: {
  readonly title: string;
  readonly data: Record<string, number> | undefined;
}) {
  const entries = Object.entries(data ?? {})
    .sort((a, b) => b[1] - a[1])
    .slice(0, 8);
  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      <div className="border-b border-jul-border px-4 py-2">
        <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
          {title}
        </span>
      </div>
      {entries.length === 0 ? (
        <p className="px-4 py-3 text-xs text-jul-muted">No data yet.</p>
      ) : (
        <ul>
          {entries.map(([key, count]) => (
            <li
              key={key}
              className="flex items-center gap-3 border-b border-jul-border px-4 py-2 last:border-b-0"
            >
              <span className="flex-1 truncate font-mono text-xs text-jul-text" title={key}>
                {key}
              </span>
              <span className="text-xs text-jul-muted">{Math.round(count).toLocaleString()}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function TrafficSourcesPanel({ sources }: { readonly sources: TrafficSources }) {
  const preflight = sources.preflight_count ?? 0;
  const same = sources.same_origin ?? 0;
  const cross = sources.cross_origin ?? 0;
  return (
    <div className="space-y-4">
      <h2 className="text-sm font-semibold text-jul-muted">Traffic Sources</h2>
      <div className="grid gap-4 sm:grid-cols-3">
        <MetricCard
          label="CORS Preflight (OPTIONS)"
          value={Math.round(preflight)}
          unit="requests"
        />
        <MetricCard label="Same-origin" value={Math.round(same)} unit="requests" />
        <MetricCard label="Cross-origin" value={Math.round(cross)} unit="requests" />
      </div>
      <div className="grid gap-4 lg:grid-cols-3">
        <TopList title="Top Hosts" data={sources.hosts} />
        <TopList title="Top Origins" data={sources.origins} />
        <TopList title="Top Referer Hosts" data={sources.referers} />
      </div>
    </div>
  );
}

function StatusGroup({ name, rows }: { readonly name: string; readonly rows: FeatureStatus[] }) {
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

  const history = useMetricsHistory(data?.stats);

  if (isLoading) {
    return <div className="text-jul-muted">Loading overview…</div>;
  }
  if (isError || !data) {
    return <div className="text-jul-danger">Failed to load overview.</div>;
  }

  const groups = groupBy(data.status, (r) => r.group);
  const stats = data.stats;

  // Derive the coarse health signals for the summary band. Each is intentionally
  // simple and defensive (stats may be unavailable): the band answers "healthy /
  // degraded / action needed" at a glance; details live in the grids below.
  const errRate = stats?.errorRate ?? 0;
  const p95 = stats?.latencyP95Ms ?? 0;
  const summary: Array<{ label: string; value: string; tone: Tone }> = [];
  if (stats?.available) {
    summary.push({
      label: "Traffic",
      value: `${(stats.requestsPerSec || 0).toFixed(1)} req/s`,
      tone: (stats.requestsPerSec || 0) > 0 ? "ok" : "idle",
    });
    summary.push({
      label: "Errors (5xx)",
      value: `${(errRate * 100).toFixed(1)}%`,
      tone: errRate >= 0.05 ? "down" : errRate > 0 ? "warn" : "ok",
    });
    summary.push({
      label: "Latency p95",
      value: `${p95.toFixed(0)} ms`,
      tone: p95 >= 1000 ? "down" : p95 >= 250 ? "warn" : "ok",
    });
  }
  // Backend health from the Upstreams status group (counts only; coarse tone).
  const upstreamRows = data.status.filter((r) => r.group === "Upstreams");
  if (upstreamRows.length > 0) {
    const anyInactive = upstreamRows.some((r) => !r.active);
    summary.push({
      label: "Backends",
      value: anyInactive ? "attention" : "healthy",
      tone: anyInactive ? "warn" : "ok",
    });
  }
  // Certificate risk from the Security group detail text, if present.
  const certRow = data.status.find((r) => r.group === "Security" && /cert|tls|acme/i.test(r.name));
  if (certRow) {
    summary.push({
      label: "Certificates",
      value: certRow.active ? "ok" : "off",
      tone: certRow.active ? "ok" : "idle",
    });
  }

  // Format uptime
  const uptimeHours = Math.floor((stats?.uptimeSeconds ?? 0) / 3600);
  const uptimeMinutes = Math.floor(((stats?.uptimeSeconds ?? 0) % 3600) / 60);
  const uptimeDisplay =
    uptimeHours > 0
      ? `${String(uptimeHours)}h ${String(uptimeMinutes)}m`
      : `${String(uptimeMinutes)}m`;

  // Calculate percentage for error rate
  const errorRatePercent = ((stats?.errorRate ?? 0) * 100).toFixed(1);

  // Calculate cache hit percentage
  const cacheHitPercent = ((stats?.cacheHitRatio ?? 0) * 100).toFixed(1);

  return (
    <div className="space-y-6">
      <div className="flex items-baseline gap-3">
        <h1 className="text-xl font-semibold">{data.product}</h1>
        {data.version && <span className="text-xs text-jul-muted">v{data.version}</span>}
      </div>

      {/* At-a-glance health summary (P3-14): coarse signals first, raw metric
          grids below for progressive disclosure. */}
      {summary.length > 0 && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
          {summary.map((s) => (
            <HealthChip key={s.label} label={s.label} value={s.value} tone={s.tone} />
          ))}
        </div>
      )}

      {/* Live Traffic Cards */}
      {stats?.available && (
        <div className="space-y-4">
          <h2 className="text-sm font-semibold text-jul-muted">Live Traffic</h2>

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
            <MetricCard label="In-flight" value={Math.round(stats.inFlight || 0)} unit="requests" />
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
              value={Math.round(stats.statusClasses?.["2xx"] || 0).toLocaleString()}
              unit="responses"
            />
            <MetricCard
              label="4xx Client Errors"
              value={Math.round(stats.statusClasses?.["4xx"] || 0).toLocaleString()}
              unit="responses"
            />
            <MetricCard
              label="3xx Redirects"
              value={Math.round(stats.statusClasses?.["3xx"] || 0).toLocaleString()}
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
              value={Math.round(stats.cacheEvents?.["MISS"] || 0).toLocaleString()}
              unit="events"
            />
            <MetricCard
              label="Cache Bypasses"
              value={Math.round(stats.cacheEvents?.["BYPASS"] || 0).toLocaleString()}
              unit="events"
            />
          </div>

          {/* HTTP Method Breakdown */}
          {stats.methods && Object.keys(stats.methods).length > 0 && (
            <div className="space-y-4">
              <h3 className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Requests by HTTP Method
              </h3>
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-6">
                {Object.entries(stats.methods)
                  .sort((a, b) => b[1] - a[1])
                  .map(([method, count]) => (
                    <MetricCard
                      key={method}
                      label={method}
                      value={Math.round(count).toLocaleString()}
                      unit="requests"
                    />
                  ))}
              </div>
            </div>
          )}

          {/* Sparklines - 2 minute trends */}
          {history.requestsPerSec.length > 0 && (
            <div className="space-y-4 pt-2">
              <h3 className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                2-Minute Trends
              </h3>
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <div className="rounded-lg border border-jul-border bg-jul-surface p-4">
                  <div className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                    Request Rate Trend
                  </div>
                  <div className="mt-2 h-12">
                    <Sparkline
                      data={history.requestsPerSec}
                      height={48}
                      width={100}
                      color="rgb(34, 197, 94)"
                      className="w-full"
                    />
                  </div>
                  <div className="mt-1 text-xs text-jul-muted">
                    {history.requestsPerSec.length} samples
                  </div>
                </div>

                <div className="rounded-lg border border-jul-border bg-jul-surface p-4">
                  <div className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                    Error Rate Trend
                  </div>
                  <div className="mt-2 h-12">
                    <Sparkline
                      data={history.errorRate}
                      height={48}
                      width={100}
                      color="rgb(239, 68, 68)"
                      className="w-full"
                    />
                  </div>
                  <div className="mt-1 text-xs text-jul-muted">
                    {history.errorRate.length} samples
                  </div>
                </div>

                <div className="rounded-lg border border-jul-border bg-jul-surface p-4">
                  <div className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                    P95 Latency Trend
                  </div>
                  <div className="mt-2 h-12">
                    <Sparkline
                      data={history.latencyP95}
                      height={48}
                      width={100}
                      color="rgb(59, 130, 246)"
                      className="w-full"
                    />
                  </div>
                  <div className="mt-1 text-xs text-jul-muted">
                    {history.latencyP95.length} samples
                  </div>
                </div>

                <div className="rounded-lg border border-jul-border bg-jul-surface p-4">
                  <div className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                    In-flight Trend
                  </div>
                  <div className="mt-2 h-12">
                    <Sparkline
                      data={history.inFlight}
                      height={48}
                      width={100}
                      color="rgb(234, 179, 8)"
                      className="w-full"
                    />
                  </div>
                  <div className="mt-1 text-xs text-jul-muted">
                    {history.inFlight.length} samples
                  </div>
                </div>

                <div className="rounded-lg border border-jul-border bg-jul-surface p-4">
                  <div className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                    Avg Latency Trend
                  </div>
                  <div className="mt-2 h-12">
                    <Sparkline
                      data={history.latencyAvg}
                      height={48}
                      width={100}
                      color="rgb(14, 165, 233)"
                      className="w-full"
                    />
                  </div>
                  <div className="mt-1 text-xs text-jul-muted">
                    {history.latencyAvg.length} samples
                  </div>
                </div>

                <div className="rounded-lg border border-jul-border bg-jul-surface p-4">
                  <div className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                    Cache Hit Ratio Trend
                  </div>
                  <div className="mt-2 h-12">
                    <Sparkline
                      data={history.cacheHitRatio}
                      height={48}
                      width={100}
                      color="rgb(168, 85, 247)"
                      className="w-full"
                    />
                  </div>
                  <div className="mt-1 text-xs text-jul-muted">
                    {history.cacheHitRatio.length} samples
                  </div>
                </div>
              </div>
            </div>
          )}
        </div>
      )}

      {/* Traffic Sources (Milestone 1.4) */}
      {data.traffic_sources && <TrafficSourcesPanel sources={data.traffic_sources} />}

      {/* Feature Status */}
      {groups.size === 0 ? (
        <p className="text-jul-muted text-sm">No status rows available.</p>
      ) : (
        <div className="space-y-4">
          <h2 className="text-sm font-semibold text-jul-muted">Capabilities & Configuration</h2>
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
