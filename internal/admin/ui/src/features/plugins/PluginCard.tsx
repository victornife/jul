/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { PluginProjection, PluginAttachment } from "@/api/client.ts";

function attachmentLabel(a: PluginAttachment): string {
  const where = `${a.listen}${a.path ? ` ${a.path}` : ""}`;
  return `${where} — ${a.scope}/${a.role}`;
}

/** Displays a declared plugin with its capabilities, attachments, and action buttons. */
export function PluginCard({
  plugin,
  onEdit,
  onAttach,
  onRemove,
  onDetach,
}: {
  readonly plugin: PluginProjection;
  readonly onEdit: () => void;
  readonly onAttach: () => void;
  readonly onRemove: () => void;
  readonly onDetach: (a: PluginAttachment) => void;
}) {
  const attachments = plugin.attachments ?? [];
  const caps: string[] = [];
  if (plugin.kv) caps.push("kv");
  if (plugin.fetch) caps.push("fetch");
  const fetchHosts = plugin.allowed_hosts ?? [];

  return (
    <div className="space-y-3 rounded-lg border border-jul-border bg-jul-surface p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm font-semibold text-jul-text">{plugin.name}</span>
            <span
              className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                plugin.type === "handler"
                  ? "bg-jul-accent/15 text-jul-accent"
                  : "bg-jul-success/15 text-jul-success"
              }`}
            >
              {plugin.type}
            </span>
          </div>
          <p className="mt-1 truncate text-xs text-jul-muted">
            {plugin.source === "inline" ? "embedded module" : (plugin.path ?? "(no path)")}
            {caps.length > 0 ? ` · ${caps.join("+")}` : ""}
          </p>
          {plugin.fetch && (
            <p className="mt-0.5 truncate text-xs text-jul-muted">
              egress:{" "}
              <span className="font-mono text-jul-warning">
                {fetchHosts.length > 0 ? fetchHosts.join(", ") : "no hosts allowed"}
              </span>
            </p>
          )}
        </div>
        <div className="flex shrink-0 gap-2">
          {plugin.type === "middleware" && (
            <button
              type="button"
              onClick={onAttach}
              className="rounded-md border border-jul-border px-2 py-1 text-xs text-jul-text hover:border-jul-accent"
            >
              Attach
            </button>
          )}
          <button
            type="button"
            onClick={onEdit}
            className="rounded-md border border-jul-border px-2 py-1 text-xs text-jul-text hover:border-jul-accent"
          >
            Edit
          </button>
          <button
            type="button"
            disabled={attachments.length > 0}
            title={
              attachments.length > 0
                ? "Detach this plugin from every route before removing it."
                : undefined
            }
            onClick={onRemove}
            className="rounded-md border border-jul-border px-2 py-1 text-xs text-jul-danger hover:border-jul-danger disabled:opacity-40"
          >
            Remove
          </button>
        </div>
      </div>
      {attachments.length > 0 && (
        <div className="space-y-1 border-t border-jul-border pt-2">
          <p className="text-xs text-jul-muted">Attached to</p>
          {attachments.map((a, i) => (
            <div key={`a-${String(i)}`} className="flex items-center justify-between gap-2">
              <span className="truncate text-xs text-jul-text">{attachmentLabel(a)}</span>
              {a.scope === "location" && a.role === "middleware" && (
                <button
                  type="button"
                  onClick={() => { onDetach(a); }}
                  className="shrink-0 rounded-md border border-jul-border px-2 py-0.5 text-xs text-jul-muted hover:text-jul-text"
                >
                  Detach
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
