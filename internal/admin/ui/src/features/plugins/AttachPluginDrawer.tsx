/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Drawer } from "@/components/Drawer.tsx";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import { fetchRoutes, type PluginProjection, type RouteTarget } from "@/api/client.ts";

/**
 * Attaches a middleware plugin to a route's plugin chain. Lists every location
 * from the routes projection and runs location_attach_plugin against the
 * chosen one.
 */
export function AttachPluginDrawer({
  plugin,
  onClose,
}: {
  readonly plugin: PluginProjection;
  readonly onClose: () => void;
}) {
  const { error, busy, run } = useRunPatch();
  const { data: routes, isLoading } = useQuery({ queryKey: ["routes"], queryFn: fetchRoutes });
  const [selected, setSelected] = useState<string>("");

  const targets: { key: string; label: string; target: RouteTarget }[] = [];
  for (const route of routes ?? []) {
    for (const loc of route.locations) {
      const target: RouteTarget = {
        listen: route.listen,
        server_names: route.server_names ?? [],
        match_type: loc.type,
        path: loc.match,
      };
      const key = `${route.listen}|${(route.server_names ?? []).join(",")}|${loc.type}|${loc.match}`;
      targets.push({
        key,
        label: `${route.listen}${(route.server_names ?? []).length > 0 ? ` (${(route.server_names ?? []).join(", ")})` : ""} — ${loc.type} ${loc.match}`,
        target,
      });
    }
  }

  function save(): void {
    const found = targets.find((t) => t.key === selected);
    if (!found) return;
    run({ op: "location_attach_plugin", plugin_name: plugin.name, ...found.target });
  }

  return (
    <Drawer
      title="Attach plugin to a route"
      subtitle={plugin.name}
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            disabled={busy || selected === ""}
            onClick={save}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            Review in editor →
          </button>
        </div>
      }
    >
      <div className="space-y-4">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          The plugin runs as middleware on the selected route, in declaration order with any other
          plugins on that route.
        </p>
        {isLoading ? (
          <p className="text-sm text-jul-muted">Loading routes…</p>
        ) : targets.length === 0 ? (
          <p className="text-sm text-jul-muted">No routes are defined yet.</p>
        ) : (
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Route</span>
            <select
              value={selected}
              onChange={(e) => { setSelected(e.target.value); }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="">Select a route…</option>
              {targets.map((t) => (
                <option key={t.key} value={t.key}>
                  {t.label}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>
    </Drawer>
  );
}
