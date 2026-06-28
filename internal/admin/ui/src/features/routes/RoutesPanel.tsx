import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchRoutes, type RouteProjection, type LocationProjection } from "@/api/client.ts";
import { RouteDetail } from "@/features/routes/RouteDetail.tsx";
import { RouteEditor } from "@/features/routes/RouteEditor.tsx";
import { RouteTester } from "@/features/routes/RouteTester.tsx";
import { PageHeader, Button, EmptyState } from "@/components/ui.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { usePersistentState } from "@/lib/usePersistentState.ts";
import { emptyAuthDraft, type RouteDraft } from "@/lib/routeToml.ts";

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

function LocationRow({
  loc,
  onOpen,
}: {
  readonly loc: LocationProjection;
  readonly onOpen: () => void;
}) {
  return (
    <tr
      onClick={onOpen}
      className="cursor-pointer border-b border-jul-border last:border-b-0 hover:bg-jul-border/30 transition-colors"
    >
      <td className="px-4 py-2">
        <span className="mr-1 text-xs text-jul-muted">{loc.type}</span>
        <span className="font-mono text-sm text-jul-text">{loc.match}</span>
        {loc.warnings && loc.warnings.length > 0 && (
          <span
            title={loc.warnings.join("\n")}
            className="ml-2 inline-block rounded-full bg-jul-warning/15 px-1.5 text-xs text-jul-warning"
          >
            ⚠ {loc.warnings.length}
          </span>
        )}
      </td>
      <td className="px-4 py-2">
        <ActionBadge action={loc.action} />
      </td>
      <td className="px-4 py-2 font-mono text-xs text-jul-muted">{loc.target ?? "—"}</td>
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

function RouteCard({
  route,
  locations,
  onOpen,
}: {
  readonly route: RouteProjection;
  readonly locations: LocationProjection[];
  readonly onOpen: (loc: LocationProjection) => void;
}) {
  const tags: string[] = [];
  if (route.tls?.enabled) tags.push("TLS");
  if (route.tls?.acme) tags.push("ACME");
  if (route.tls?.client_auth) tags.push(`mTLS:${route.tls.client_auth}`);
  if (route.http3) tags.push("HTTP/3");
  if (route.h2c) tags.push("h2c");

  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      <div className="flex flex-wrap items-center gap-2 border-b border-jul-border px-4 py-3">
        {route.name ? (
          <>
            <span className="font-mono text-sm font-semibold text-jul-text">
              {route.name}
            </span>
            <span className="text-xs text-jul-muted">
              {route.listen}
            </span>
          </>
        ) : (
          <span className="font-mono text-sm font-semibold text-jul-text">
            {route.listen}
          </span>
        )}
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

      {locations.length === 0 ? (
        <p className="px-4 py-3 text-xs text-jul-muted">No locations match the current filter.</p>
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
            {locations.map((loc, i) => (
              <LocationRow
                key={i}
                loc={loc}
                onOpen={() => {
                  onOpen(loc);
                }}
              />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

interface Selection {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
}

// Filters narrow the route list (Milestone 4.7): by action/type and by the
// edge features applied to a location. They run entirely client-side over the
// projection the API already serves and persist across sessions.
type ActionFilter = "all" | "proxy" | "static" | "redirect" | "deny" | "return";
type FeatureFilter = "all" | "auth" | "cache" | "compression" | "rate_limit" | "warnings";

function locationMatches(
  loc: LocationProjection,
  action: ActionFilter,
  feature: FeatureFilter,
): boolean {
  if (action !== "all" && loc.action !== action) return false;
  switch (feature) {
    case "auth":
      return loc.auth;
    case "cache":
      return loc.cache;
    case "compression":
      return loc.compression;
    case "rate_limit":
      return loc.rate_limit;
    case "warnings":
      return (loc.warnings ?? []).length > 0;
    default:
      return true;
  }
}

export function RoutesPanel() {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["routes"],
    queryFn: fetchRoutes,
  });

  const [selected, setSelected] = useState<Selection | null>(null);
  const [creating, setCreating] = useState<Partial<RouteDraft> | null>(null);
  const [testing, setTesting] = useState(false);
  const [actionFilter, setActionFilter] = usePersistentState<ActionFilter>(
    "routes_action_filter",
    "all",
  );
  const [featureFilter, setFeatureFilter] = usePersistentState<FeatureFilter>(
    "routes_feature_filter",
    "all",
  );

  const filtersActive = actionFilter !== "all" || featureFilter !== "all";

  const filtered = useMemo(() => {
    const src = data ?? [];
    return src
      .map((route) => ({
        route,
        locations: route.locations.filter((loc) =>
          locationMatches(loc, actionFilter, featureFilter),
        ),
      }))
      .filter((r) => !filtersActive || r.locations.length > 0);
  }, [data, actionFilter, featureFilter, filtersActive]);

  if (isLoading) return <div className="text-jul-muted">Loading routes…</div>;
  if (isError || !data)
    return <PanelError error={error} resource="routes" onRetry={() => void refetch()} />;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Routes"
        description="Routes decide what Jul does with incoming requests. A route can serve files, proxy to an app, redirect, deny, or connect to a protocol adapter. Click any route to inspect its effective configuration, or create one through the guided editor."
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => {
                setTesting(true);
              }}
            >
              Test route
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                setCreating({});
              }}
            >
              New route
            </Button>
          </>
        }
      />

      {data.length > 0 && (
        <div className="flex flex-wrap items-end gap-3">
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Action</span>
            <select
              value={actionFilter}
              onChange={(e) => {
                setActionFilter(e.target.value as ActionFilter);
              }}
              className="rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="all">All actions</option>
              <option value="proxy">Proxy</option>
              <option value="static">Static</option>
              <option value="redirect">Redirect</option>
              <option value="deny">Deny</option>
              <option value="return">Return</option>
            </select>
          </label>
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Feature</span>
            <select
              value={featureFilter}
              onChange={(e) => {
                setFeatureFilter(e.target.value as FeatureFilter);
              }}
              className="rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="all">Any feature</option>
              <option value="auth">Auth enabled</option>
              <option value="cache">Cache enabled</option>
              <option value="compression">Compression enabled</option>
              <option value="rate_limit">Rate limited</option>
              <option value="warnings">Has warnings</option>
            </select>
          </label>
          {filtersActive && (
            <Button
              variant="ghost"
              onClick={() => {
                setActionFilter("all");
                setFeatureFilter("all");
              }}
            >
              Clear filters
            </Button>
          )}
        </div>
      )}

      {data.length === 0 ? (
        <EmptyState
          title="No routes are configured yet"
          description="A route tells Jul what to do when a request matches a host and path — serve a folder, proxy to an app such as Express, FastAPI, or a Go API, redirect, or deny. Create your first route to start handling traffic."
          action={
            <Button
              variant="primary"
              onClick={() => {
                setCreating({});
              }}
            >
              New route
            </Button>
          }
        />
      ) : filtered.length === 0 ? (
        <EmptyState
          title="No routes match these filters"
          description="No location matches the selected action and feature filters. Clear the filters to see every configured route."
          action={
            <Button
              variant="secondary"
              onClick={() => {
                setActionFilter("all");
                setFeatureFilter("all");
              }}
            >
              Clear filters
            </Button>
          }
        />
      ) : (
        <div className="space-y-4">
          {filtered.map(({ route, locations }, i) => (
            <RouteCard
              key={`${route.listen}-${String(i)}`}
              route={route}
              locations={locations}
              onOpen={(loc) => {
                setSelected({ route, loc });
              }}
            />
          ))}
        </div>
      )}

      {selected && (
        <RouteDetail
          route={selected.route}
          loc={selected.loc}
          onClose={() => {
            setSelected(null);
          }}
          onEdit={() => {
            const { route, loc } = selected;
            setSelected(null);
            setCreating({
              listen: route.listen,
              serverNames: route.server_names?.join(", ") ?? "",
              path: loc.match,
              matchType: loc.type === "exact" || loc.type === "regex" ? loc.type : "prefix",
              action:
                loc.action === "proxy" ||
                loc.action === "static" ||
                loc.action === "redirect" ||
                loc.action === "deny" ||
                loc.action === "return"
                  ? loc.action
                  : "proxy",
              target: loc.target ?? "",
              // The route projection only reports whether auth is present, not
              // which method or its parameters. When the source location had
              // auth, preselect the CIDR method so the editor's warning prompts
              // the operator to re-enter a concrete policy rather than silently
              // carrying over an empty (allow-all) block; otherwise leave it off.
              auth: loc.auth ? { ...emptyAuthDraft(), method: "cidr" } : emptyAuthDraft(),
              cache: loc.cache,
              rateLimit: loc.rate_limit,
            });
          }}
        />
      )}

      {creating && (
        <RouteEditor
          initial={creating}
          onClose={() => {
            setCreating(null);
          }}
        />
      )}

      {testing && (
        <RouteTester
          onClose={() => {
            setTesting(false);
          }}
        />
      )}
    </div>
  );
}
