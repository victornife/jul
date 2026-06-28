import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { searchConfig, describeApiError, type SearchResult } from "@/api/client.ts";
import { PageHeader, TextField, EmptyState, Badge } from "@/components/ui.tsx";
import { usePersistentState } from "@/lib/usePersistentState.ts";
import { useDebouncedValue } from "@/lib/useDebouncedValue.ts";

// Unified search and discovery across routes and apps (Milestone 4.7). It is
// backed by the server-side GET /api/search endpoint, which ranks results and
// reflects route↔app relationships (which app a route targets, which routes use
// an app, and which apps are unused) so the SPA does not re-derive them. The
// query is debounced and persisted across sessions.

type KindFilter = "all" | "route" | "app";

function badgeTone(label: string): "neutral" | "success" | "warning" | "accent" {
  if (label === "TLS" || label === "health check") return "success";
  if (label === "warnings" || label === "unused") return "warning";
  if (label.startsWith("→")) return "neutral";
  return "accent";
}

export function SearchPanel() {
  const [query, setQuery] = usePersistentState<string>("search_query", "");
  const [kindFilter, setKindFilter] = useState<KindFilter>("all");
  const debounced = useDebouncedValue(query, 200);

  const type = kindFilter === "all" ? "all" : kindFilter === "route" ? "routes" : "apps";
  const search = useQuery({
    queryKey: ["search", debounced, type],
    queryFn: () => searchConfig(debounced.trim(), type),
  });

  const results: SearchResult[] = search.data ?? [];
  const unusedApps = results.filter(
    (r) => r.kind === "app" && (r.badges ?? []).includes("unused"),
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title="Search & Discovery"
        description="Find any route or app by host, path, action, upstream, or backend address. Results are ranked by the server and show the relationships between routes and apps so you can navigate large configurations quickly."
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
              setKindFilter(e.target.value as KindFilter);
            }}
            className="rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="all">All</option>
            <option value="route">Routes</option>
            <option value="app">Apps</option>
          </select>
        </label>
      </div>

      {search.isLoading ? (
        <div className="text-jul-muted">Loading…</div>
      ) : search.isError ? (
        <div className="text-jul-danger">{describeApiError(search.error, "search results").message}</div>
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
        <>
          {unusedApps.length > 0 && query.trim() === "" && (
            <div className="rounded-lg border border-jul-border bg-jul-surface px-4 py-3 text-sm">
              <span className="font-medium text-jul-text">Unused apps:</span>{" "}
              <span className="text-jul-muted">
                {unusedApps.map((a) => a.title).join(", ")} — defined but not referenced by any route.
              </span>
            </div>
          )}
          <ul className="space-y-2">
            {results.map((r, i) => (
              <li key={`${r.kind}-${r.title}-${String(i)}`}>
                <Link
                  to={r.kind === "route" ? "/routes" : "/apps"}
                  className="flex items-center gap-3 rounded-lg border border-jul-border bg-jul-surface px-4 py-3 hover:border-jul-accent"
                >
                  <Badge tone={r.kind === "route" ? "accent" : "neutral"}>{r.kind}</Badge>
                  <div className="min-w-0 flex-1">
                    <div className="truncate font-mono text-sm text-jul-text">{r.title}</div>
                    <div className="truncate text-xs text-jul-muted">{r.detail}</div>
                  </div>
                  <div className="flex flex-wrap items-center gap-1">
                    {(r.badges ?? []).map((b, bi) => (
                      <Badge key={`${b}-${String(bi)}`} tone={badgeTone(b)}>
                        {b}
                      </Badge>
                    ))}
                  </div>
                </Link>
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}