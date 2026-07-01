import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Drawer } from "@/components/Drawer.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { Loading, MaturityBadge } from "@/components/ui.tsx";
import {
  fetchPlugins,
  fetchRoutes,
  uploadPluginWasm,
  type PluginProjection,
  type PluginAttachment,
  type RouteTarget,
} from "@/api/client.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import {
  emptyPluginDraft,
  seedPluginDraft,
  pluginDraftToPatch,
  pluginDraftWarnings,
  type PluginDraft,
} from "@/lib/plugins.ts";

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
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className={`w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent ${mono ? "font-mono" : ""}`}
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

function TextArea({
  label,
  value,
  placeholder,
  hint,
  rows,
  onChange,
}: {
  readonly label: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly hint?: string;
  readonly rows?: number;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <textarea
        value={value}
        placeholder={placeholder}
        rows={rows ?? 3}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
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
    <label className="flex items-center gap-2">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => {
          onChange(e.target.checked);
        }}
        className="h-4 w-4 rounded border-jul-border bg-jul-surface text-jul-accent focus:ring-jul-accent"
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

function attachmentLabel(a: PluginAttachment): string {
  const where = `${a.listen}${a.path ? ` ${a.path}` : ""}`;
  return `${where} — ${a.scope}/${a.role}`;
}

// PluginEditorDrawer creates or edits a [plugins.NAME] declaration. In create
// mode the name is editable and the source is locked to "path"; in edit mode an
// inline plugin keeps its embedded bytes (source "inline") since the console
// never transmits WASM.
function PluginEditorDrawer({
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
            onChange={(v) => {
              set("name", v);
            }}
          />
        )}

        {canKeepInline && (
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Module source</span>
            <select
              value={draft.source}
              onChange={(e) => {
                set("source", e.target.value === "inline" ? "inline" : "path");
              }}
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
            onChange={(v) => {
              set("path", v);
            }}
          />
        )}

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Type</span>
          <select
            value={draft.type}
            onChange={(e) => {
              set("type", e.target.value === "handler" ? "handler" : "middleware");
            }}
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
            onChange={(v) => {
              set("memoryLimit", v);
            }}
          />
          <TextField
            label="Timeout"
            value={draft.timeout}
            placeholder="100ms"
            mono
            onChange={(v) => {
              set("timeout", v);
            }}
          />
        </div>

        <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
          <p className="text-xs font-medium text-jul-text">Host capabilities</p>
          <Toggle
            label="KV store access"
            checked={draft.kv}
            onChange={(v) => {
              set("kv", v);
            }}
          />
          <Toggle
            label="Outbound fetch"
            checked={draft.fetch}
            onChange={(v) => {
              set("fetch", v);
            }}
          />
          {draft.fetch && (
            <TextField
              label="Allowed hosts"
              value={draft.allowedHosts}
              placeholder="api.example.com, auth.example.com"
              hint="Comma-separated allowlist for outbound fetch (required)."
              onChange={(v) => {
                set("allowedHosts", v);
              }}
            />
          )}
        </div>

        <TextArea
          label="Config"
          value={draft.config}
          placeholder={"key = value\nheader = X-Trace"}
          rows={4}
          hint="One key = value pair per line, passed to the plugin as its [plugins.NAME.config] table."
          onChange={(v) => {
            set("config", v);
          }}
        />

        <Warnings items={warnings} />
      </div>
    </Drawer>
  );
}

// AttachPluginDrawer attaches a middleware plugin to a route's plugin chain. It
// lists every location from the routes projection and runs
// location_attach_plugin against the chosen one.
function AttachPluginDrawer({
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
              onChange={(e) => {
                setSelected(e.target.value);
              }}
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

// UploadPluginDrawer lets an operator upload a compiled .wasm module directly
// to the server. The uploaded file is stored server-side and its path can then
// be referenced when declaring a plugin.
function UploadPluginDrawer({
  onClose,
  onUploaded,
  uploadMaxSizeMB,
}: {
  readonly onClose: () => void;
  readonly onUploaded: (path: string) => void;
  readonly uploadMaxSizeMB: number;
}) {
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  function validateWasm(f: File): string | null {
    if (f.size <= 0) {
      return "File is empty.";
    }
    if (!f.name.endsWith(".wasm")) {
      return "Expected a .wasm file.";
    }
    const limit = uploadMaxSizeMB * 1024 * 1024;
    if (limit > 0 && f.size > limit) {
      return `File exceeds the server upload limit of ${String(uploadMaxSizeMB)} MB.`;
    }
    return null;
  }

  async function submit(): Promise<void> {
    if (!file) return;
    const validation = validateWasm(file);
    if (validation) {
      setErr(validation);
      return;
    }
    setBusy(true);
    setErr(null);
    try {
      const resp = await uploadPluginWasm(file);
      onUploaded(resp.path);
    } catch (e) {
      if (e instanceof Error) {
        setErr(e.message);
      } else {
        setErr("Upload failed");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <Drawer
      title="Upload .wasm"
      subtitle="Choose a compiled WebAssembly module"
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {err && <span className="text-xs text-jul-danger">{err}</span>}
          <button
            type="button"
            disabled={busy || !file}
            onClick={() => { void submit(); }}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            {busy ? "Uploading…" : "Upload"}
          </button>
        </div>
      }
    >
      <div className="space-y-4">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          The module is uploaded to the server and referenced by path in the
          plugin declaration. After upload, you can create a new plugin that
          points to the uploaded file.
        </p>
        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Module file</span>
          <input
            type="file"
            accept=".wasm"
            onChange={(e) => {
              const f = e.target.files?.[0] ?? null;
              setFile(f);
              setErr(null);
            }}
            className="block w-full text-sm text-jul-text file:mr-4 file:rounded-md file:border-0 file:bg-jul-accent file:px-3 file:py-1.5 file:text-sm file:font-medium file:text-jul-bg hover:file:brightness-110"
          />
          <span className="text-xs text-jul-muted">
            {uploadMaxSizeMB > 0
              ? `Accepted: .wasm, up to ${String(uploadMaxSizeMB)} MB.`
              : "Uploads are disabled by admin config."}
          </span>
        </label>
        {file && (
          <div className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
            <span className="font-medium text-jul-text">{file.name}</span>
            <span className="ml-2">{`${(file.size / 1024).toFixed(1)} KB`}</span>
          </div>
        )}
      </div>
    </Drawer>
  );
}

function PluginCard({
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
                  onClick={() => {
                    onDetach(a);
                  }}
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
              onClick={() => {
                setUploading(true);
              }}
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
            onClick={() => {
              setCreating(true);
            }}
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
              onEdit={() => {
                setEditing(p);
              }}
              onAttach={() => {
                setAttaching(p);
              }}
              onRemove={() => {
                remove(p);
              }}
              onDetach={(a) => {
                detach(p, a);
              }}
            />
          ))}
        </div>
      )}

      {creating && (
        <PluginEditorDrawer
          existing={null}
          onClose={() => {
            setCreating(false);
          }}
        />
      )}
      {editing && (
        <PluginEditorDrawer
          existing={editing}
          onClose={() => {
            setEditing(null);
          }}
        />
      )}
      {attaching && (
        <AttachPluginDrawer
          plugin={attaching}
          onClose={() => {
            setAttaching(null);
          }}
        />
      )}
      {uploading && (
        <UploadPluginDrawer
          onClose={() => {
            setUploading(false);
          }}
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
