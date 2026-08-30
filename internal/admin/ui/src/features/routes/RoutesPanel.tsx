/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { fetchRoutes, type RouteProjection, type LocationProjection } from "@/api/client.ts";
import { RouteDetail } from "@/features/routes/RouteDetail.tsx";
import { RouteEditor, type RouteEditorInitial } from "@/features/routes/RouteEditor.tsx";
import { RouteTester } from "@/features/routes/RouteTester.tsx";
import { PageHeader, Button, EmptyState, Loading } from "@/components/ui.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { usePersistentState } from "@/lib/usePersistentState.ts";
import { emptyAuthDraft, type AuthDraft } from "@/lib/routeToml.ts";
import { usePermission } from "@/auth/usePermission.ts";
import {
  canonicalServerNames,
  restoreRouteSelection,
  routeIdentityKey,
  serverIdentityFromRoute,
  serverIdentityKey,
  type RouteSelection,
} from "@/lib/routePatch.ts";

const ACTION_COLORS: Record<string, string> = {
  proxy: "bg-jul-accent/15 text-jul-accent",
  grpc: "bg-purple-500/15 text-purple-300",
  grpc_transcode: "bg-indigo-500/15 text-indigo-300",
  fastcgi: "bg-yellow-500/15 text-yellow-300",
  uwsgi: "bg-orange-500/15 text-orange-300",
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
      <td className="px-4 py-2 truncate">
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
      <td className="px-4 py-2 font-mono text-xs text-jul-muted truncate">{loc.target ?? "—"}</td>
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
            <span className="font-mono text-sm font-semibold text-jul-text">{route.name}</span>
            <span className="text-xs text-jul-muted">{route.listen}</span>
          </>
        ) : (
          <span className="font-mono text-sm font-semibold text-jul-text">{route.listen}</span>
        )}
        {route.server_names && route.server_names.length > 0 && (
          <span className="text-xs text-jul-muted">
            {canonicalServerNames(route.server_names).join(", ")}
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
        <table className="w-full table-fixed text-left text-sm">
          <thead>
            <tr className="border-b border-jul-border text-xs text-jul-muted">
              <th className="px-4 py-2 w-[35%]">Path</th>
              <th className="px-4 py-2 w-[15%]">Action</th>
              <th className="px-4 py-2 w-[35%]">Target</th>
              <th className="px-4 py-2 text-center w-[7.5%]">Auth</th>
              <th className="px-4 py-2 text-center w-[7.5%]">Cache</th>
            </tr>
          </thead>
          <tbody>
            {locations.map((loc) => (
              <LocationRow
                key={routeIdentityKey(serverIdentityFromRoute(route), {
                  matchType: loc.type,
                  path: loc.match,
                  ...(loc.route_id ? { routeId: loc.route_id } : {}),
                })}
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

// Filters narrow the route list (Milestone 4.7): by action/type and by the
// edge features applied to a location. They run entirely client-side over the
// projection the API already serves and persist across sessions.
type ActionFilter =
  | "all"
  | "proxy"
  | "grpc"
  | "grpc_transcode"
  | "fastcgi"
  | "uwsgi"
  | "static"
  | "redirect"
  | "deny"
  | "return";
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

const ROUTE_EDITOR_REOPEN_KEY = "__jul_routeEditor_open";
const ROUTE_EDITOR_SELECTION_KEY = "__jul_routeEditor_state";

function authDraftFromLocation(loc: LocationProjection): AuthDraft {
  const draft = emptyAuthDraft();
  if (!loc.auth || loc.auth_detail === undefined) return draft;
  const detail = loc.auth_detail;
  switch (detail.method) {
    case "basic":
      return {
        ...draft,
        method: "basic",
        basicFile: detail.basic_file ?? "",
        basicRealm: detail.basic_realm ?? "",
      };
    case "jwt":
      return {
        ...draft,
        method: "jwt",
        jwtJwksUrl: detail.jwt_jwks_url ?? "",
        jwtIssuer: detail.jwt_issuer ?? "",
        jwtAudience: detail.jwt_audience ?? "",
      };
    case "forward":
      return { ...draft, method: "forward", forwardUrl: detail.forward_url ?? "" };
    case "cidr":
      return {
        ...draft,
        method: "cidr",
        allow: (detail.allow ?? []).join(", "),
        deny: (detail.deny ?? []).join(", "),
      };
    default:
      // The projection is intentionally open-ended for compatibility. Unknown
      // auth methods are not silently re-created as a different policy.
      return draft;
  }
}

function cloneDraft(route: RouteProjection, loc: LocationProjection): RouteEditorInitial | null {
  if (
    loc.action !== "proxy" &&
    loc.action !== "static" &&
    loc.action !== "redirect" &&
    loc.action !== "deny" &&
    loc.action !== "return"
  ) {
    return null;
  }
  return {
    listen: route.listen,
    serverNames: canonicalServerNames(route.server_names ?? []).join(", "),
    path: loc.match,
    matchType: loc.type === "exact" || loc.type === "regex" ? loc.type : "prefix",
    action: loc.action,
    target: loc.target ?? "",
    auth: authDraftFromLocation(loc),
    cache: loc.cache,
    rateLimit: loc.rate_limit,
  };
}

export function RoutesPanel() {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["routes"],
    queryFn: fetchRoutes,
  });

  const [selected, setSelected] = useState<RouteSelection | null>(null);
  const [creating, setCreating] = useState<RouteEditorInitial | null>(null);
  const [testing, setTesting] = useState(false);
  const navigate = useNavigate();
  const { has } = usePermission();
  const canWrite = has("config:write");

  useEffect(() => {
    if (!data || sessionStorage.getItem(ROUTE_EDITOR_REOPEN_KEY) === null) return;
    sessionStorage.removeItem(ROUTE_EDITOR_REOPEN_KEY);
    const stored = sessionStorage.getItem(ROUTE_EDITOR_SELECTION_KEY);
    sessionStorage.removeItem(ROUTE_EDITOR_SELECTION_KEY);
    if (stored === null) return;
    try {
      const restored = restoreRouteSelection(data, JSON.parse(stored) as unknown);
      if (restored === null) return;
      setSelected(restored);
      const draft = cloneDraft(restored.route, restored.loc);
      if (draft !== null) setCreating(draft);
    } catch {
      // Invalid or obsolete session data fails closed. In particular, a legacy
      // listen-only selection never falls through to the first sibling vhost.
    }
  }, [data]);

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

  if (isLoading) return <Loading label="Loading routes…" />;
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
              variant="secondary"
              disabled={!canWrite}
              {...(!canWrite ? { title: "Requires config:write." } : {})}
              onClick={() => {
                void navigate("/transcode");
              }}
            >
              New transcode
            </Button>
            <Button
              variant="primary"
              disabled={!canWrite}
              {...(!canWrite ? { title: "Requires config:write." } : {})}
              onClick={() => {
                setCreating({});
              }}
            >
              New route
            </Button>
          </>
        }
      />

      <ForbiddenAction permission="config:write" />

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
              <option value="grpc">gRPC</option>
              <option value="grpc_transcode">gRPC transcode</option>
              <option value="fastcgi">FastCGI</option>
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
              disabled={!canWrite}
              {...(!canWrite ? { title: "Requires config:write." } : {})}
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
          {filtered.map(({ route, locations }) => (
            <RouteCard
              key={serverIdentityKey(serverIdentityFromRoute(route))}
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
            const draft = cloneDraft(selected.route, selected.loc);
            if (draft !== null) setCreating(draft);
          }}
        />
      )}

      {creating && (
        <RouteEditor
          initial={creating}
          existingRoutes={data}
          closeLabel={selected ? "Back" : "Close"}
          onReview={(targetSelection) => {
            sessionStorage.setItem(ROUTE_EDITOR_REOPEN_KEY, "1");
            sessionStorage.setItem(ROUTE_EDITOR_SELECTION_KEY, JSON.stringify(targetSelection));
          }}
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
