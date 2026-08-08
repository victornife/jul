/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  fetchApps,
  fetchRoutes,
  type AppProjection,
  type BackendProjection,
} from "@/api/client.ts";
import { usePermission } from "@/auth/usePermission.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { Button, EmptyState, Loading, PageHeader } from "@/components/ui.tsx";
import { AppDetail } from "@/features/apps/AppDetail.tsx";
import { AppEditor } from "@/features/apps/AppEditor.tsx";
import { usePersistentState } from "@/lib/usePersistentState.ts";

const APP_REOPEN_KEY = "jul-apps-reopen-selection";

function HealthDot({ healthy }: { readonly healthy: boolean | undefined }) {
  const label =
    healthy === undefined ? "health unknown — no active checks" : healthy ? "healthy" : "unhealthy";
  return (
    <span className="inline-flex items-center">
      <span
        aria-hidden
        title={label}
        className={`inline-block h-2 w-2 rounded-full ${
          healthy === undefined ? "bg-jul-muted/50" : healthy ? "bg-jul-success" : "bg-jul-danger"
        }`}
      />
      <span className="sr-only">{label}</span>
    </span>
  );
}

function BackendRow({ backend }: { readonly backend: BackendProjection }) {
  return (
    <tr className="border-b border-jul-border transition-colors last:border-b-0 hover:bg-jul-border/30">
      <td className="truncate px-4 py-2">
        <div className="flex items-center gap-2">
          <HealthDot healthy={backend.healthy} />
          <span className="font-mono text-sm text-jul-text">{backend.address}</span>
        </div>
      </td>
      <td className="px-4 py-2 text-sm text-jul-muted">{backend.weight}</td>
      <td className="px-4 py-2 text-sm text-jul-muted">
        {backend.inflight !== undefined && backend.inflight > 0 ? (
          <span className="text-jul-warning">{backend.inflight}</span>
        ) : (
          (backend.inflight ?? "—")
        )}
      </td>
    </tr>
  );
}

function AppCard({ app, onOpen }: { readonly app: AppProjection; readonly onOpen: () => void }) {
  const totalCount = app.backends.length;
  const healthyCount = app.backends.filter((backend) => backend.healthy === true).length;
  const unhealthyCount = app.backends.filter((backend) => backend.healthy === false).length;
  const known = healthyCount + unhealthyCount;

  return (
    <article className="rounded-lg border border-jul-border bg-jul-surface transition-colors hover:bg-jul-border/10">
      <button
        type="button"
        aria-label={`Open App ${app.name}`}
        onClick={onOpen}
        className="flex w-full flex-wrap items-center gap-3 border-b border-jul-border px-4 py-3 text-left focus:outline-none focus:ring-2 focus:ring-inset focus:ring-jul-accent"
      >
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
      </button>

      {app.backends.length === 0 ? (
        <p className="px-4 py-3 text-xs text-jul-muted">No backends configured.</p>
      ) : (
        <table className="w-full table-fixed text-left text-sm">
          <thead>
            <tr className="border-b border-jul-border text-xs text-jul-muted">
              <th className="w-1/2 px-4 py-2">Address</th>
              <th className="w-1/4 px-4 py-2">Weight</th>
              <th className="w-1/4 px-4 py-2">In-flight</th>
            </tr>
          </thead>
          <tbody>
            {app.backends.map((backend) => (
              <BackendRow key={backend.address} backend={backend} />
            ))}
          </tbody>
        </table>
      )}
    </article>
  );
}

// Filters narrow the App list by live health and route usage. They run over the
// projection and persist across sessions; they never alter exact App selection.
type HealthFilter = "all" | "healthy" | "degraded";
type UsageFilter = "all" | "used" | "unused";

function appMatches(app: AppProjection, health: HealthFilter, usage: UsageFilter): boolean {
  const total = app.backends.length;
  const healthy = app.backends.filter((backend) => backend.healthy === true).length;
  const unhealthy = app.backends.filter((backend) => backend.healthy === false).length;
  if (health === "healthy" && (total === 0 || healthy < total)) return false;
  if (health === "degraded" && unhealthy === 0) return false;
  const used = (app.routes_using ?? []).length > 0;
  if (usage === "used" && !used) return false;
  if (usage === "unused" && used) return false;
  return true;
}

export function AppsPanel() {
  const { has } = usePermission();
  const canWrite = has("config:write");
  const { data, isLoading, isFetching, isError, error, refetch } = useQuery({
    queryKey: ["apps"],
    queryFn: fetchApps,
    refetchInterval: 5_000,
  });
  const routesQuery = useQuery({
    queryKey: ["routes"],
    queryFn: fetchRoutes,
    staleTime: 5_000,
  });

  const [selectedName, setSelectedName] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [healthFilter, setHealthFilter] = usePersistentState<HealthFilter>(
    "apps_health_filter",
    "all",
  );
  const [usageFilter, setUsageFilter] = usePersistentState<UsageFilter>("apps_usage_filter", "all");

  const filtersActive = healthFilter !== "all" || usageFilter !== "all";
  const selected = useMemo(
    () =>
      selectedName === null
        ? null
        : ((data ?? []).find((app) => app.name === selectedName) ?? null),
    [data, selectedName],
  );
  const filtered = useMemo(
    () => (data ?? []).filter((app) => appMatches(app, healthFilter, usageFilter)),
    [data, healthFilter, usageFilter],
  );

  useEffect(() => {
    if (!data || isFetching || typeof sessionStorage === "undefined") return;
    try {
      const intendedName = sessionStorage.getItem(APP_REOPEN_KEY);
      if (intendedName === null) return;
      sessionStorage.removeItem(APP_REOPEN_KEY);
      if (data.some((app) => app.name === intendedName)) {
        setSelectedName(intendedName);
      }
    } catch {
      // Selection restoration is a convenience. Invalid or unavailable browser
      // storage never changes the authoritative Apps projection.
    }
  }, [data, isFetching]);

  useEffect(() => {
    if (selectedName !== null && data && !data.some((app) => app.name === selectedName)) {
      setSelectedName(null);
    }
  }, [data, selectedName]);

  if (isLoading) return <Loading label="Loading apps…" />;
  if (isError || !data) {
    return <PanelError error={error} resource="apps" onRetry={() => void refetch()} />;
  }

  const newAppAction = (
    <div className="space-y-1 text-right">
      <Button
        variant="primary"
        disabled={!canWrite}
        title={canWrite ? "Create an App/upstream" : "Requires config:write"}
        onClick={() => {
          if (canWrite) setCreating(true);
        }}
      >
        New app
      </Button>
      <ForbiddenAction permission="config:write" className="justify-end text-left" />
    </div>
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title="Apps & Upstreams"
        description="An app is a named pool of backend instances that routes proxy to by name. Jul balances traffic across healthy backends and can run active health checks. Open an exact App name to see its settings and dependencies."
        actions={newAppAction}
      />

      {data.length > 0 && (
        <div className="flex flex-wrap items-end gap-3">
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Health</span>
            <select
              value={healthFilter}
              onChange={(event) => {
                setHealthFilter(event.target.value as HealthFilter);
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
              onChange={(event) => {
                setUsageFilter(event.target.value as UsageFilter);
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
          description="Add an app when Jul should send traffic to a backend service. The typed creator can make only the upstream, mount it on one exact existing server, or add one exact new server in the same reviewed batch."
          action={newAppAction}
        />
      ) : filtered.length === 0 ? (
        <EmptyState
          title="No apps match these filters"
          description="No App matches the selected health and usage filters. Clear the filters to see every configured App."
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
                setSelectedName(app.name);
              }}
            />
          ))}
        </div>
      )}

      {selected !== null && (
        <AppDetail
          app={selected}
          onClose={() => {
            setSelectedName(null);
          }}
        />
      )}

      {creating && (
        <AppEditor
          existingApps={data}
          existingRoutes={routesQuery.data ?? []}
          routeInventoryReady={routesQuery.isSuccess}
          onReview={(appName) => {
            if (typeof sessionStorage === "undefined") return;
            try {
              sessionStorage.setItem(APP_REOPEN_KEY, appName);
            } catch {
              // Final apply remains authoritative even when restoration cannot
              // be persisted for the next visit to this panel.
            }
          }}
          onClose={() => {
            setCreating(false);
          }}
        />
      )}
    </div>
  );
}
