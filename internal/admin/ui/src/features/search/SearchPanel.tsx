import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import {
  fetchRoutes,
  fetchApps,
  type RouteProjection,
  type AppProjection,
} from "@/api/client.ts";
import { PageHeader, TextField, EmptyState, Badge, Card } from "@/components/ui.tsx";
import { usePersistentState } from "@/lib/usePersistentState.ts";

// Unified search and discovery across routes and apps (Milestone 4.7). It runs
// entirely client-side over the structured projections the API already serves,
// so it adds no backend surface: results are ranked by match quality and the
// relationships between routes and apps are surfaced inline (which app a route
// targets, which routes use an app, and which apps are unused).

type ResultKind = "route" | "app";

interface SearchResult {
  kind: ResultKind;
  title: string;
  detail: string;
  to: string;
  score: number;
  badges: { label: string; tone: "neutral" | "success" | "warning" | "accent" }[];
}

function scoreMatch(haystack: string, q: string): number {
  const h = haystack.toLowerCase();
  if (h === q) return 100;
  if (h.startsWith(q)) return 70;
  if (h.includes(q)) return 40;
  return 0;
}

function routeResults(routes: RouteProjection[], q: string): SearchResult[] {
  const out: SearchResult[] = [];
  for (const r of routes) {
    for (const loc of r.locations) {
      const hostList = (r.server_names ?? []).join(", ");
      const fields = [loc.match, r.listen, hostList, loc.action, loc.target ?? "", loc.upstream ?? ""];
      const score = Math.max(...fields.map((f) => scoreMatch(f, q)), 0);
      if (q !== "" && score === 0) continue;
      const badges: SearchResult["badges"] = [{ label: loc.action, tone: "accent" }];
      if (loc.upstream) badges.push({ label: `→ ${loc.upstream}`, tone: "neutral" });
      if (loc.secure) badges.push({ label: "TLS", tone: "success" });
      if ((loc.warnings ?? []).length > 0) badges.push({ label: "warnings", tone: "warning" });
      out.push({
        kind: "route",
        title: `${loc.match} (${loc.type})`,
        detail: `${r.listen}${hostList ? ` · ${hostList}` : ""}${loc.target ? ` · ${loc.target}` : ""}`,
        to: "/routes",
        score: score + 5, // routes ranked slightly above apps on ties
        badges,
      });
    }
  }
  return out;
}

function appResults(apps: AppProjection[], q: string): SearchResult[] {
  return apps
    .map((a): SearchResult | null => {
      const fields = [a.name, a.strategy, ...a.backends.map((b) => b.address)];
      const score = Math.max(...fields.map((f) => scoreMatch(f, q)), 0);
      if (q !== "" && score === 0) return null;
      const usedBy = a.routes_using ?? [];
      const badges: SearchResult["badges"] = [
        { label: `${String(a.backends.length)} backend${a.backends.length === 1 ? "" : "s"}`, tone: "neutral" },
      ];
      if (a.health_check) badges.push({ label: "health check", tone: "success" });
      badges.push(
        usedBy.length === 0
          ? { label: "unused", tone: "warning" }
          : { label: `${String(usedBy.length)} route${usedBy.length === 1 ? "" : "s"}`, tone: "accent" },
      );
      return {
        kind: "app",
        title: a.name,
        detail: `${a.strategy}${usedBy.length > 0 ? ` · used by ${usedBy.join(", ")}` : " · not referenced by any route"}`,
        to: "/apps",
        score,
        badges,
      };
    })
    .filter((r): r is SearchResult => r !== null);
}

export function SearchPanel() {
  const routes = useQuery({ queryKey: ["routes"], queryFn: fetchRoutes });
  const apps = useQuery({ queryKey: ["apps"], queryFn: fetchApps });
  const [query, setQuery] = usePersistentState<string>("search_query", "");
  const [kindFilter, setKindFilter] = useState<"all" | ResultKind>("all");

  const results = useMemo(() => {
    const q = query.trim().toLowerCase();
    const r = routes.data ?? [];
    const a = apps.data ?? [];
    let merged: SearchResult[] = [...routeResults(r, q), ...appResults(a, q)];
    if (kindFilter !== "all") merged = merged.filter((m) => m.kind === kindFilter);
    return merged.sort((x, y) => y.score - x.score).slice(0, 50);
  }, [routes.data, apps.data, query, kindFilter]);

  const unusedApps = useMemo(
    () => (apps.data ?? []).filter((a) => (a.routes_using ?? []).length === 0),
    [apps.data],
  );

  const loading = routes.isLoading || apps.isLoading;
  const failed = routes.isError || apps.isError;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Search & Discovery"
        description="Find any route or app by host, path, action, upstream, or backend address. Results show the relationships between routes and apps so you can navigate large configurations quickly."
      />

      <div className="flex flex-wrap items-end gap-3">
        <div className="min-w-64 flex-1">
          <TextField
            label="Search"
            mono={false}
            value={query}
            placeholder="e.g. /api, example.com, backend-1, proxy"
            onChange={setQuery}
          />
        </div>
        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Type</span>
          <select
            value={kindFilter}
            onChange={(e) => {
              setKindFilter(e.target.value as "all" | ResultKind);
            }}
            className="rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="all">All</option>
            <option value="route">Routes</option>
            <option value="app">Apps</option>
          </select>
        </label>
      </div>

      {unusedApps.length > 0 && kindFilter !== "route" && query.trim() === "" && (
        <Card title="Unused apps">
          <ul className="space-y-1">
            {unusedApps.map((a) => (
              <li key={a.name} className="flex items-center gap-2 text-sm">
                <Badge tone="warning">unused</Badge>
                <span className="font-mono text-jul-text">{a.name}</span>
                <span className="text-jul-muted">— defined but not referenced by any route</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {loading ? (
        <div className="text-jul-muted">Loading…</div>
      ) : failed ? (
        <div className="text-jul-danger">Failed to load routes or apps.</div>
      ) : results.length === 0 ? (
        <EmptyState
          title={query.trim() === "" ? "Start typing to search" : "No matches"}
          description={
            query.trim() === ""
              ? "Search across every route and app. Try a hostname, a path prefix, an upstream name, or a backend address."
              : "Nothing matched your query. Check the spelling, or clear the type filter to widen the search."
          }
        />
      ) : (
        <ul className="space-y-2">
          {results.map((r, i) => (
            <li key={`${r.kind}-${r.title}-${String(i)}`}>
              <Link
                to={r.to}
                className="flex items-center gap-3 rounded-lg border border-jul-border bg-jul-surface px-4 py-3 hover:border-jul-accent"
              >
                <Badge tone={r.kind === "route" ? "accent" : "neutral"}>{r.kind}</Badge>
                <div className="min-w-0 flex-1">
                  <div className="truncate font-mono text-sm text-jul-text">{r.title}</div>
                  <div className="truncate text-xs text-jul-muted">{r.detail}</div>
                </div>
                <div className="flex flex-wrap items-center gap-1">
                  {r.badges.map((b, bi) => (
                    <Badge key={`${b.label}-${String(bi)}`} tone={b.tone}>
                      {b.label}
                    </Badge>
                  ))}
                </div>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}