import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import { fetchRawConfig } from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import {
  appendFragment,
  generateRouteToml,
  type RouteAction,
  type RouteDraft,
} from "@/lib/routeToml.ts";

function TextField({
  label,
  hint,
  value,
  placeholder,
  onChange,
}: {
  readonly label: string;
  readonly hint?: string;
  readonly value: string;
  readonly placeholder?: string;
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
    <label className="flex items-center gap-2 text-sm text-jul-text">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => {
          onChange(e.target.checked);
        }}
        className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
      />
      {label}
    </label>
  );
}

const ACTIONS: { value: RouteAction; label: string }[] = [
  { value: "static", label: "Serve static files" },
  { value: "proxy", label: "Reverse proxy" },
  { value: "redirect", label: "Redirect" },
  { value: "deny", label: "Deny (403)" },
  { value: "return", label: "Return status" },
];

function targetHint(action: RouteAction): { label: string; placeholder: string } | null {
  switch (action) {
    case "static":
      return { label: "Root directory", placeholder: "/var/www/site" };
    case "proxy":
      return { label: "Upstream target", placeholder: "http://app or http://127.0.0.1:3000" };
    case "redirect":
      return { label: "Redirect URL", placeholder: "https://example.com/new" };
    case "return":
      return { label: "Status code", placeholder: "200" };
    case "deny":
      return null;
  }
}

export interface RouteEditorProps {
  readonly initial?: Partial<RouteDraft>;
  readonly onClose: () => void;
}

/**
 * Guided route creation/editing (Milestone 2.2). It never writes directly: it
 * generates a candidate [[servers]] block, appends it to the running config,
 * and hands the draft to the Config editor where it flows through
 * Validate → Diff → Apply → Rollback.
 */
export function RouteEditor({ initial, onClose }: RouteEditorProps) {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<RouteDraft>({
    listen: initial?.listen ?? ":8080",
    serverNames: initial?.serverNames ?? "",
    path: initial?.path ?? "/",
    matchType: initial?.matchType ?? "prefix",
    action: initial?.action ?? "proxy",
    target: initial?.target ?? "",
    auth: initial?.auth ?? false,
    cache: initial?.cache ?? false,
    compression: initial?.compression ?? false,
    rateLimit: initial?.rateLimit ?? false,
  });
  const [error, setError] = useState<string | null>(null);

  const fragment = generateRouteToml(draft);
  const th = targetHint(draft.action);

  function set<K extends keyof RouteDraft>(key: K, value: RouteDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  async function openInEditor(): Promise<void> {
    setError(null);
    try {
      const raw = await fetchRawConfig();
      setPendingDraft(appendFragment(raw.raw ?? "", fragment));
      void navigate("/config");
    } catch {
      setError("Could not load the current configuration to merge this route.");
    }
  }

  return (
    <Drawer
      title="New route"
      subtitle="Generate a route, then review and apply it safely in the editor."
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            onClick={() => {
              void openInEditor();
            }}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110"
          >
            Review in editor →
          </button>
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          A route tells Jul what to do when an incoming request matches a host and path.
          This editor builds the configuration for you; nothing is applied until you
          review the diff and confirm in the editor.
        </p>

        <TextField
          label="Listener"
          hint="The address this server block binds to."
          value={draft.listen}
          placeholder=":8080"
          onChange={(v) => {
            set("listen", v);
          }}
        />
        <TextField
          label="Host names (optional)"
          hint="Comma-separated. Leave blank to match any host."
          value={draft.serverNames}
          placeholder="example.com, www.example.com"
          onChange={(v) => {
            set("serverNames", v);
          }}
        />

        <div className="grid grid-cols-[1fr_2fr] gap-3">
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Match type</span>
            <select
              value={draft.matchType}
              onChange={(e) => {
                set("matchType", e.target.value as RouteDraft["matchType"]);
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="prefix">prefix</option>
              <option value="exact">exact</option>
              <option value="regex">regex</option>
            </select>
          </label>
          <TextField
            label="Path"
            value={draft.path}
            placeholder="/api/"
            onChange={(v) => {
              set("path", v);
            }}
          />
        </div>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Action</span>
          <select
            value={draft.action}
            onChange={(e) => {
              set("action", e.target.value as RouteAction);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {ACTIONS.map((a) => (
              <option key={a.value} value={a.value}>
                {a.label}
              </option>
            ))}
          </select>
        </label>

        {th && (
          <TextField
            label={th.label}
            value={draft.target}
            placeholder={th.placeholder}
            onChange={(v) => {
              set("target", v);
            }}
          />
        )}

        <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Edge rules
          </span>
          <Toggle label="Require auth" checked={draft.auth} onChange={(v) => { set("auth", v); }} />
          <Toggle label="Cache responses" checked={draft.cache} onChange={(v) => { set("cache", v); }} />
          <Toggle label="Rate limit" checked={draft.rateLimit} onChange={(v) => { set("rateLimit", v); }} />
        </div>

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Generated TOML
          </span>
          <pre className="overflow-auto rounded-md border border-jul-border bg-jul-surface p-3 font-mono text-xs leading-relaxed text-jul-text">
            {fragment}
          </pre>
        </div>
      </div>
    </Drawer>
  );
}