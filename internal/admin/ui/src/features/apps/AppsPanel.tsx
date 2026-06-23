import { useQuery } from "@tanstack/react-query";
import { fetchApps, type AppProjection, type BackendProjection } from "@/api/client.ts";

function HealthDot({ healthy }: { readonly healthy: boolean | undefined }) {
  if (healthy === undefined) return null;
  return (
    <span
      title={healthy ? "healthy" : "unhealthy"}
      className={`inline-block h-2 w-2 rounded-full ${
        healthy ? "bg-jul-success" : "bg-jul-danger"
      }`}
    />
  );
}

function BackendRow({ b }: { readonly b: BackendProjection }) {
  return (
    <tr className="border-b border-jul-border last:border-b-0 hover:bg-jul-surface/60">
      <td className="px-4 py-2">
        <div className="flex items-center gap-2">
          <HealthDot healthy={b.healthy} />
          <span className="font-mono text-sm text-jul-text">{b.address}</span>
        </div>
      </td>
      <td className="px-4 py-2 text-sm text-jul-muted">{b.weight}</td>
      <td className="px-4 py-2 text-sm text-jul-muted">
        {b.inflight !== undefined && b.inflight > 0 ? (
          <span className="text-jul-warning">{b.inflight}</span>
        ) : (
          b.inflight ?? "—"
        )}
      </td>
    </tr>
  );
}

function AppCard({ app }: { readonly app: AppProjection }) {
  const activeCount = app.backends.filter((b) => b.healthy !== false).length;
  const totalCount = app.backends.length;

  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      <div className="flex flex-wrap items-center gap-3 border-b border-jul-border px-4 py-3">
        <span className="font-semibold text-jul-text">{app.name}</span>
        <span className="rounded-full bg-jul-border px-2 py-0.5 text-xs text-jul-muted">
          {app.strategy}
        </span>
        {app.discovery && (
          <span className="rounded-full bg-jul-accent/15 px-2 py-0.5 text-xs text-jul-accent">
            discovery:{app.discovery}
          </span>
        )}
        {app.health_check && (
          <span className="rounded-full bg-jul-success/15 px-2 py-0.5 text-xs text-jul-success">
            health-check
          </span>
        )}
        <span className="ml-auto text-xs text-jul-muted">
          {activeCount}/{totalCount} healthy
        </span>
      </div>

      {app.backends.length === 0 ? (
        <p className="px-4 py-3 text-xs text-jul-muted">No backends configured.</p>
      ) : (
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-jul-border text-xs text-jul-muted">
              <th className="px-4 py-2">Address</th>
              <th className="px-4 py-2">Weight</th>
              <th className="px-4 py-2">In-flight</th>
            </tr>
          </thead>
          <tbody>
            {app.backends.map((b) => (
              <BackendRow key={b.address} b={b} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

export function AppsPanel() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["apps"],
    queryFn: fetchApps,
    refetchInterval: 5_000,
  });

  if (isLoading) return <div className="text-jul-muted">Loading apps…</div>;
  if (isError || !data) return <div className="text-jul-danger">Failed to load apps.</div>;

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold">Apps &amp; Upstreams</h1>
      {data.length === 0 ? (
        <p className="text-jul-muted text-sm">No upstream pools configured.</p>
      ) : (
        <div className="space-y-4">
          {data.map((app) => (
            <AppCard key={app.name} app={app} />
          ))}
        </div>
      )}
    </div>
  );
}
