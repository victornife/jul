import { useQuery } from "@tanstack/react-query";
import { fetchRoutes, type RouteProjection, type LocationProjection } from "@/api/client.ts";

const ACTION_COLORS: Record<string, string> = {
  proxy: "bg-jul-accent/15 text-jul-accent",
  grpc: "bg-purple-500/15 text-purple-300",
  grpc_transcode: "bg-indigo-500/15 text-indigo-300",
  fastcgi: "bg-yellow-500/15 text-yellow-300",
  static: "bg-jul-success/15 text-jul-success",
  redirect: "bg-jul-warning/15 text-jul-warning",
  deny: "bg-jul-danger/15 text-jul-danger",
  return: "bg-jul-border text-jul-muted",
};

function ActionBadge({ action }: { readonly action: string }) {
  const cls = ACTION_COLORS[action] ?? "bg-jul-border text-jul-muted";
  return (
    <span className={`inline-block rounded-full px-2 py-0.5 text-xs font-medium ${cls}`}>
      {action}
    </span>
  );
}

function LocationRow({ loc }: { readonly loc: LocationProjection }) {
  return (
    <tr className="border-b border-jul-border last:border-b-0 hover:bg-jul-surface/60">
      <td className="px-4 py-2">
        <span className="mr-1 text-xs text-jul-muted">{loc.type}</span>
        <span className="font-mono text-sm text-jul-text">{loc.match}</span>
      </td>
      <td className="px-4 py-2">
        <ActionBadge action={loc.action} />
      </td>
      <td className="px-4 py-2 font-mono text-xs text-jul-muted">
        {loc.target ?? "—"}
      </td>
      <td className="px-4 py-2 text-center">
        {loc.auth && (
          <span className="inline-block rounded-full bg-jul-warning/15 px-2 py-0.5 text-xs text-jul-warning">
            auth
          </span>
        )}
      </td>
      <td className="px-4 py-2 text-center">
        {loc.cache && (
          <span className="inline-block rounded-full bg-jul-accent/15 px-2 py-0.5 text-xs text-jul-accent">
            cache
          </span>
        )}
      </td>
    </tr>
  );
}

function RouteCard({ route }: { readonly route: RouteProjection }) {
  const tags: string[] = [];
  if (route.tls?.enabled) tags.push("TLS");
  if (route.tls?.acme) tags.push("ACME");
  if (route.tls?.client_auth) tags.push(`mTLS:${route.tls.client_auth}`);
  if (route.http3) tags.push("HTTP/3");
  if (route.h2c) tags.push("h2c");

  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      {/* header */}
      <div className="flex flex-wrap items-center gap-2 border-b border-jul-border px-4 py-3">
        <span className="font-mono text-sm font-semibold text-jul-text">{route.listen}</span>
        {route.server_names && route.server_names.length > 0 && (
          <span className="text-xs text-jul-muted">
            {route.server_names.join(", ")}
          </span>
        )}
        <span className="ml-auto flex gap-1">
          {tags.map((t) => (
            <span
              key={t}
              className="rounded-full bg-jul-accent/15 px-2 py-0.5 text-xs text-jul-accent"
            >
              {t}
            </span>
          ))}
        </span>
      </div>

      {/* location table */}
      {route.locations.length === 0 ? (
        <p className="px-4 py-3 text-xs text-jul-muted">No locations configured.</p>
      ) : (
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-jul-border text-xs text-jul-muted">
              <th className="px-4 py-2">Path</th>
              <th className="px-4 py-2">Action</th>
              <th className="px-4 py-2">Target</th>
              <th className="px-4 py-2 text-center">Auth</th>
              <th className="px-4 py-2 text-center">Cache</th>
            </tr>
          </thead>
          <tbody>
            {route.locations.map((loc, i) => (
              // index key is stable here (order matches config)
              <LocationRow key={i} loc={loc} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

export function RoutesPanel() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["routes"],
    queryFn: fetchRoutes,
  });

  if (isLoading) return <div className="text-jul-muted">Loading routes…</div>;
  if (isError || !data) return <div className="text-jul-danger">Failed to load routes.</div>;

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold">Routes</h1>
      {data.length === 0 ? (
        <p className="text-jul-muted text-sm">No server blocks configured.</p>
      ) : (
        <div className="space-y-4">
          {data.map((route, i) => (
            <RouteCard key={`${route.listen}-${i}`} route={route} />
          ))}
        </div>
      )}
    </div>
  );
}
