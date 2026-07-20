/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  fetchApps,
  fetchRoutes,
  type AppProjection,
  type BackendProjection,
} from "@/api/client.ts";
import { AppDetail } from "@/features/apps/AppDetail.tsx";
import { AppEditor } from "@/features/apps/AppEditor.tsx";
import { PageHeader, Button, EmptyState, Loading } from "@/components/ui.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { usePersistentState } from "@/lib/usePersistentState.ts";

function HealthDot({ healthy }: { readonly healthy: boolean | undefined }) {
  if (healthy === undefined)
    return (
      <span
        title="health unknown — no active checks"
        className="inline-block h-2 w-2 rounded-full bg-jul-muted/50"
      />
    );
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
    <tr className="border-b border-jul-border last:border-b-0 hover:bg-jul-border/30 transition-colors">
      <td className="px-4 py-2 truncate">
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

function AppCard({ app, onOpen }: { readonly app: AppProjection; readonly onOpen: () => void }) {
  const totalCount = app.backends.length;
  const healthyCount = app.backends.filter((b) => b.healthy === true).length;
  const unhealthyCount = app.backends.filter((b) => b.healthy === false).length;
  const known = healthyCount + unhealthyCount;

  return (
    <div className="cursor-pointer rounded-lg border border-jul-border bg-jul-surface transition-colors hover:bg-jul-border/10" onClick={onOpen}>
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
        {app.warnings && app.warnings.length > 0 && (
          <span className="rounded-full bg-jul-warning/15 px-2 py-0.5 text-xs text-jul-warning">
            ⚠ {app.warnings.length}
          </span>
        )}
        <span className="ml-auto text-xs text-jul-muted">
          {totalCount === 0
            ? "no backends"
            : known === 0
              ? `${String(totalCount)} backends · health unknown`
              : `${String(healthyCount)}/${String(totalCount)} healthy${unhealthyCount > 0 ? ` · ${String(unhealthyCount)} down` : ""}`}
        </span>
      </div>

      {app.backends.length === 0 ? (
        <p className="px-4 py-3 text-xs text-jul-muted">No backends configured.</p>
      ) : (
        <table className="w-full table-fixed text-left text-sm">
          <thead>
            <tr className="border-b border-jul-border text-xs text-jul-muted">
              <th className="px-4 py-2 w-1/2">Address</th>
              <th className="px-4 py-2 w-1/4">Weight</th>
              <th className="px-4 py-2 w-1/4">In-flight</th>
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

// Filters narrow the app list (Milestone 4.7): by backend health and by whether
// any route references the app. They run client-side over the projection and
// persist across sessions.
type HealthFilter = "all" | "healthy" | "degraded";
type UsageFilter = "all" | "used" | "unused";

function appMatches(app: AppProjection, health: HealthFilter, usage: UsageFilter): boolean {
  const total = app.backends.length;
  const healthy = app.backends.filter((b) => b.healthy === true).length;
  const unhealthy = app.backends.filter((b) => b.healthy === false).length;
  // "healthy" means every backend is known-healthy; "degraded" means at least
  // one backend is known-unhealthy. Unknown-health backends match neither.
  if (health === "healthy" && (total === 0 || healthy < total)) return false;
  if (health === "degraded" && unhealthy === 0) return false;
  const used = (app.routes_using ?? []).length > 0;
  if (usage === "used" && !used) return false;
  if (usage === "unused" && used) return false;
  return true;
}

export function AppsPanel() {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["apps"],
    queryFn: fetchApps,
    refetchInterval: 5_000,
  });

  const routesQuery = useQuery({
    queryKey: ["routes"],
    queryFn: fetchRoutes,
    staleTime: 5000,
  });

  const [selected, setSelected] = useState<AppProjection | null>(null);
  const [creating, setCreating] = useState(false);
  const [healthFilter, setHealthFilter] = usePersistentState<HealthFilter>(
    "apps_health_filter",
    "all",
  );
  const [usageFilter, setUsageFilter] = usePersistentState<UsageFilter>(
    "apps_usage_filter",
    "all",
  );

  const filtersActive = healthFilter !== "all" || usageFilter !== "all";

  const filtered = useMemo(
    () => (data ?? []).filter((app) => appMatches(app, healthFilter, usageFilter)),
    [data, healthFilter, usageFilter],
  );

  if (isLoading) return <Loading label="Loading apps…" />;
  if (isError || !data)
    return <PanelError error={error} resource="apps" onRetry={() => void refetch()} />;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Apps & Upstreams"
        description="An app is a named pool of backend instances that routes proxy to by name. Jul balances traffic across the healthy backends and can run active health checks. Click an app to see which routes depend on it."
        actions={
          <Button
            variant="primary"
            onClick={() => {
              setCreating(true);
            }}
          >
            New app
          </Button>
        }
      />

      {data.length > 0 && (
        <div className="flex flex-wrap items-end gap-3">
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Health</span>
            <select
              value={healthFilter}
              onChange={(e) => {
                setHealthFilter(e.target.value as HealthFilter);
              }}
              className="rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="all">All apps</option>
              <option value="healthy">All backends healthy</option>
              <option value="degraded">Has unhealthy backend</option>
            </select>
          </label>
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Usage</span>
            <select
              value={usageFilter}
              onChange={(e) => {
                setUsageFilter(e.target.value as UsageFilter);
              }}
              className="rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="all">Any usage</option>
              <option value="used">Used by a route</option>
              <option value="unused">Unused</option>
            </select>
          </label>
          {filtersActive && (
            <Button
              variant="ghost"
              onClick={() => {
                setHealthFilter("all");
                setUsageFilter("all");
              }}
            >
              Clear filters
            </Button>
          )}
        </div>
      )}

      {data.length === 0 ? (
        <EmptyState
          title="No apps are configured yet"
          description="Add an app when you want Jul to send traffic to a backend service such as Express, Apollo, FastAPI, Django, or a Go API. An app groups one or more backend instances that routes can proxy to by name."
          action={
            <Button
              variant="primary"
              onClick={() => {
                setCreating(true);
              }}
            >
              New app
            </Button>
          }
        />
      ) : filtered.length === 0 ? (
        <EmptyState
          title="No apps match these filters"
          description="No app matches the selected health and usage filters. Clear the filters to see every configured app."
          action={
            <Button
              variant="secondary"
              onClick={() => {
                setHealthFilter("all");
                setUsageFilter("all");
              }}
            >
              Clear filters
            </Button>
          }
        />
      ) : (
        <div className="space-y-4">
          {filtered.map((app) => (
            <AppCard
              key={app.name}
              app={app}
              onOpen={() => {
                setSelected(app);
              }}
            />
          ))}
        </div>
      )}

      {selected && (
        <AppDetail
          app={selected}
          onClose={() => {
            setSelected(null);
          }}
        />
      )}

      {creating && (
        <AppEditor
          existingRoutes={routesQuery.data ?? []}
          onClose={() => {
            setCreating(false);
          }}
        />
      )}
    </div>
  );
}
