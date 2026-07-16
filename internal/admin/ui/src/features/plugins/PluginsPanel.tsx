/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { PanelError } from "@/components/PanelError.tsx";
import { Loading, MaturityBadge } from "@/components/ui.tsx";
import {
  fetchPlugins,
  type PluginProjection,
  type PluginAttachment,
} from "@/api/client.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import { PluginEditorDrawer } from "./PluginEditorDrawer.tsx";
import { AttachPluginDrawer } from "./AttachPluginDrawer.tsx";
import { UploadPluginDrawer } from "./UploadPluginDrawer.tsx";
import { PluginCard } from "./PluginCard.tsx";

export function PluginsPanel() {
  const { run: runDetach } = useRunPatch();
  const queryClient = useQueryClient();
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["plugins"],
    queryFn: fetchPlugins,
  });
  const [editing, setEditing] = useState<PluginProjection | null>(null);
  const [creating, setCreating] = useState(false);
  const [attaching, setAttaching] = useState<PluginProjection | null>(null);
  const [uploading, setUploading] = useState(false);

  if (isLoading) return <Loading label="Loading plugins…" />;
  if (isError || !data)
    return <PanelError error={error} resource="plugins" onRetry={() => void refetch()} />;

  function detach(plugin: PluginProjection, a: PluginAttachment): void {
    runDetach({
      op: "location_detach_plugin",
      plugin_name: plugin.name,
      listen: a.listen,
      server_names: a.server_names ?? [],
      match_type: a.match_type ?? "",
      path: a.path ?? "",
    });
  }

  function remove(plugin: PluginProjection): void {
    runDetach({ op: "plugin_remove", plugin_name: plugin.name });
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-xl font-semibold">Plugins</h1>
            <MaturityBadge level="beta" />
          </div>
          <p className="max-w-3xl text-sm text-jul-muted">
            WASM plugins attached to routes for custom request/response processing.
            Build your own middleware or use third-party modules compiled to WebAssembly.
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          {data.upload_enabled ? (
            <button
              type="button"
              onClick={() => { setUploading(true); }}
              className="rounded-md border border-jul-border px-3 py-1.5 text-sm font-medium text-jul-text hover:border-jul-accent"
            >
              Upload .wasm
            </button>
          ) : (
            <span className="rounded-md border border-jul-border/40 bg-jul-surface px-3 py-1.5 text-sm text-jul-muted">
              Uploads disabled by admin config
            </span>
          )}
          <button
            type="button"
            onClick={() => { setCreating(true); }}
            className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110"
          >
            New plugin
          </button>
        </div>
      </div>

      {!data.compiled && (
        <div className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
          This build does not include the WASM plugin runtime (the <code>wasmplugins</code> tag).
          You can edit plugin declarations here, but applying a config that declares plugins will be
          rejected by the apply preflight until you run a plugin-enabled binary.
        </div>
      )}

      {data.plugins.length === 0 ? (
        <p className="rounded-lg border border-jul-border bg-jul-surface p-4 text-sm text-jul-muted">
          No plugins are declared. Add one to run a WASM module in the request path.
        </p>
      ) : (
        <div className="space-y-3">
          {data.plugins.map((p) => (
            <PluginCard
              key={p.name}
              plugin={p}
              onEdit={() => { setEditing(p); }}
              onAttach={() => { setAttaching(p); }}
              onRemove={() => { remove(p); }}
              onDetach={(a) => { detach(p, a); }}
            />
          ))}
        </div>
      )}

      {creating && (
        <PluginEditorDrawer existing={null} onClose={() => { setCreating(false); }} />
      )}
      {editing && (
        <PluginEditorDrawer existing={editing} onClose={() => { setEditing(null); }} />
      )}
      {attaching && (
        <AttachPluginDrawer plugin={attaching} onClose={() => { setAttaching(null); }} />
      )}
      {uploading && (
        <UploadPluginDrawer
          onClose={() => { setUploading(false); }}
          onUploaded={(_path) => {
            setUploading(false);
            void queryClient.invalidateQueries({ queryKey: ["plugins"] });
            setCreating(true);
          }}
          uploadMaxSizeMB={data.upload_max_size_mb}
        />
      )}
    </div>
  );
}
