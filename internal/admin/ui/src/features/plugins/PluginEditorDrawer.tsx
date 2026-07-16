/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import {
  emptyPluginDraft,
  seedPluginDraft,
  pluginDraftToPatch,
  pluginDraftWarnings,
  type PluginDraft,
} from "@/lib/plugins.ts";
import type { PluginProjection } from "@/api/client.ts";

// ── Local form primitives ────────────────────────────────────────────────────
// These lightweight form controls are scoped to the plugin editor and are not
// in the shared design-system ui.tsx (they are opinionated for this drawer).

function TextField({
  label,
  value,
  placeholder,
  hint,
  mono,
  onChange,
}: {
  readonly label: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly hint?: string;
  readonly mono?: boolean;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={(e) => { onChange(e.target.value); }}
        className={`w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent ${mono ? "font-mono" : ""}`}
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

function TextArea({
  label,
  value,
  placeholder,
  rows,
  hint,
  onChange,
}: {
  readonly label: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly rows?: number;
  readonly hint?: string;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <textarea
        value={value}
        placeholder={placeholder}
        rows={rows ?? 3}
        onChange={(e) => { onChange(e.target.value); }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

function Toggle({
  label,
  checked,
  onChange,
}: {
  readonly label: string;
  readonly checked: boolean;
  readonly onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex cursor-pointer items-center gap-2">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => { onChange(e.target.checked); }}
        className="h-4 w-4 rounded border-jul-border accent-jul-accent"
      />
      <span className="text-sm text-jul-text">{label}</span>
    </label>
  );
}

function Warnings({ items }: { readonly items: string[] }) {
  if (items.length === 0) return null;
  return (
    <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
      {items.map((wn, i) => (
        <p key={`w-${String(i)}`} className="text-xs text-jul-text">
          {wn}
        </p>
      ))}
    </div>
  );
}

// ── PluginEditorDrawer ───────────────────────────────────────────────────────

/**
 * Creates or edits a [plugins.NAME] declaration. In create mode the name is
 * editable and the source is locked to "path"; in edit mode an inline plugin
 * keeps its embedded bytes (source "inline") since the console never transmits
 * WASM.
 */
export function PluginEditorDrawer({
  existing,
  onClose,
}: {
  readonly existing: PluginProjection | null;
  readonly onClose: () => void;
}) {
  const { error, busy, run } = useRunPatch();
  const isNew = existing === null;
  const [draft, setDraft] = useState<PluginDraft>(() =>
    existing ? seedPluginDraft(existing) : emptyPluginDraft(),
  );
  const warnings = pluginDraftWarnings(draft, isNew);
  const canKeepInline = existing?.source === "inline";

  function set<K extends keyof PluginDraft>(key: K, val: PluginDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: val }));
  }

  function save(): void {
    const name = draft.name.trim();
    if (name === "") return;
    run({ op: "plugin_set", plugin_name: name, plugin: pluginDraftToPatch(draft) });
  }

  return (
    <Drawer
      title={isNew ? "New plugin" : "Edit plugin"}
      subtitle={existing?.name ?? ""}
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            disabled={busy || warnings.length > 0}
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
          A plugin is a WASM module the proxy runs in the request path. You can either reference an
          existing server-side <code>.wasm</code> path or upload a module through the Upload drawer.
          Edit its type, host capabilities, and limits.
          Attach a middleware plugin to routes from the Plugins list.
        </p>

        {isNew && (
          <TextField
            label="Name"
            value={draft.name}
            placeholder="my-plugin"
            hint="The [plugins.NAME] key referenced when attaching."
            onChange={(v) => { set("name", v); }}
          />
        )}

        {canKeepInline && (
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Module source</span>
            <select
              value={draft.source}
              onChange={(e) => { set("source", e.target.value === "inline" ? "inline" : "path"); }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="inline">Keep embedded module</option>
              <option value="path">Replace with a file path</option>
            </select>
          </label>
        )}

        {draft.source === "path" && (
          <TextField
            label="Module path"
            value={draft.path}
            placeholder="plugins/header-inject.wasm"
            mono
            hint="Path to the compiled .wasm module, relative to the working directory."
            onChange={(v) => { set("path", v); }}
          />
        )}

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Type</span>
          <select
            value={draft.type}
            onChange={(e) => { set("type", e.target.value === "handler" ? "handler" : "middleware"); }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="middleware">Middleware (request filter)</option>
            <option value="handler">Handler (terminal response)</option>
          </select>
          <span className="text-xs text-jul-muted">
            Middleware plugins attach to a route&apos;s chain; handler plugins are wired as a
            route&apos;s action in the config editor.
          </span>
        </label>

        <div className="grid grid-cols-2 gap-3">
          <TextField
            label="Memory limit"
            value={draft.memoryLimit}
            placeholder="16m"
            mono
            onChange={(v) => { set("memoryLimit", v); }}
          />
          <TextField
            label="Timeout"
            value={draft.timeout}
            placeholder="100ms"
            mono
            onChange={(v) => { set("timeout", v); }}
          />
        </div>

        <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
          <p className="text-xs font-medium text-jul-text">Host capabilities</p>
          <Toggle
            label="KV store access"
            checked={draft.kv}
            onChange={(v) => { set("kv", v); }}
          />
          <Toggle
            label="Outbound fetch"
            checked={draft.fetch}
            onChange={(v) => { set("fetch", v); }}
          />
          {draft.fetch && (
            <TextField
              label="Allowed hosts"
              value={draft.allowedHosts}
              placeholder="api.example.com, auth.example.com"
              hint="Comma-separated allowlist for outbound fetch (required)."
              onChange={(v) => { set("allowedHosts", v); }}
            />
          )}
        </div>

        <TextArea
          label="Config"
          value={draft.config}
          placeholder={"key = value\nheader = X-Trace"}
          rows={4}
          hint="One key = value pair per line, passed to the plugin as its [plugins.NAME.config] table."
          onChange={(v) => { set("config", v); }}
        />

        <Warnings items={warnings} />
      </div>
    </Drawer>
  );
}
