import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import { fetchRawConfig } from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import {
  appendFragment,
  generateAppToml,
  type AppDraft,
  type BackendDraft,
} from "@/lib/routeToml.ts";

// Friendly presets (Milestone 2.5). Presets only seed copy and defaults; they
// never create framework-specific magic.
interface Preset {
  readonly id: string;
  readonly label: string;
  readonly placeholder: string;
  readonly hint: string;
}

const PRESETS: Preset[] = [
  { id: "node", label: "Express / Node.js", placeholder: "127.0.0.1:3000", hint: "Typical Node.js apps listen on :3000." },
  { id: "apollo", label: "Apollo GraphQL", placeholder: "127.0.0.1:4000", hint: "Apollo Server defaults to :4000." },
  { id: "fastapi", label: "FastAPI", placeholder: "127.0.0.1:8000", hint: "Uvicorn/FastAPI defaults to :8000." },
  { id: "django", label: "Django / Flask", placeholder: "127.0.0.1:8000", hint: "Django/Flask dev servers use :8000/:5000." },
  { id: "go", label: "Go HTTP app", placeholder: "127.0.0.1:8080", hint: "Go services commonly listen on :8080." },
  { id: "generic", label: "Generic HTTP app", placeholder: "127.0.0.1:8080", hint: "Any HTTP backend." },
  { id: "grpc", label: "gRPC backend", placeholder: "127.0.0.1:50051", hint: "gRPC services commonly listen on :50051." },
];

function TextField({
  label,
  value,
  placeholder,
  hint,
  onChange,
}: {
  readonly label: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly hint?: string;
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

export interface AppEditorProps {
  readonly initial?: Partial<AppDraft>;
  readonly onClose: () => void;
}

/**
 * Guided app/upstream creation (Milestone 2.5). It generates an [[upstreams]]
 * block and hands it to the Config editor through the validated apply path; it
 * never writes directly.
 */
export function AppEditor({ initial, onClose }: AppEditorProps) {
  const navigate = useNavigate();
  const [presetId, setPresetId] = useState("generic");
  const preset = PRESETS.find((p) => p.id === presetId) ?? {
    id: "generic",
    label: "Generic HTTP app",
    placeholder: "127.0.0.1:8080",
    hint: "Any HTTP backend.",
  };

  const [draft, setDraft] = useState<AppDraft>({
    name: initial?.name ?? "",
    strategy: initial?.strategy ?? "round_robin",
    backends: initial?.backends ?? [{ address: "", weight: 1 }],
    healthCheck: initial?.healthCheck ?? false,
    healthCheckPath: initial?.healthCheckPath ?? "/healthz",
    healthCheckInterval: initial?.healthCheckInterval ?? "5s",
  });
  const [error, setError] = useState<string | null>(null);

  const fragment = generateAppToml(draft);

  function set<K extends keyof AppDraft>(key: K, value: AppDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  function setBackend(idx: number, patch: Partial<BackendDraft>): void {
    setDraft((d) => ({
      ...d,
      backends: d.backends.map((b, i) => (i === idx ? { ...b, ...patch } : b)),
    }));
  }

  function addBackend(): void {
    setDraft((d) => ({ ...d, backends: [...d.backends, { address: "", weight: 1 }] }));
  }

  function removeBackend(idx: number): void {
    setDraft((d) => ({ ...d, backends: d.backends.filter((_, i) => i !== idx) }));
  }

  async function openInEditor(): Promise<void> {
    setError(null);
    if (!draft.name.trim()) {
      setError("Give the app a name before continuing.");
      return;
    }
    try {
      const raw = await fetchRawConfig();
      setPendingDraft(appendFragment(raw.raw ?? "", fragment));
      void navigate("/config");
    } catch {
      setError("Could not load the current configuration to merge this app.");
    }
  }

  return (
    <Drawer
      title="New app / upstream"
      subtitle="Put an app behind Jul. Review and apply safely in the editor."
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
        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Framework preset</span>
          <select
            value={presetId}
            onChange={(e) => {
              setPresetId(e.target.value);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {PRESETS.map((p) => (
              <option key={p.id} value={p.id}>
                {p.label}
              </option>
            ))}
          </select>
          <span className="text-xs text-jul-muted">{preset.hint}</span>
        </label>

        <TextField
          label="App name"
          value={draft.name}
          placeholder="api"
          hint="Routes reference this name via proxy_pass to http://<name>."
          onChange={(v) => {
            set("name", v);
          }}
        />

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Load-balancing strategy</span>
          <select
            value={draft.strategy}
            onChange={(e) => {
              set("strategy", e.target.value as AppDraft["strategy"]);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="round_robin">round_robin</option>
            <option value="weighted_round_robin">weighted_round_robin</option>
            <option value="least_conn">least_conn</option>
          </select>
        </label>

        <div className="space-y-2">
          <span className="text-sm font-medium text-jul-text">Backends</span>
          {draft.backends.map((b, i) => (
            <div key={i} className="flex items-center gap-2">
              <input
                type="text"
                value={b.address}
                placeholder={preset.placeholder}
                onChange={(e) => {
                  setBackend(i, { address: e.target.value });
                }}
                className="flex-1 rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
              <input
                type="number"
                min={1}
                value={b.weight}
                title="weight"
                onChange={(e) => {
                  setBackend(i, { weight: Math.max(1, Number(e.target.value) || 1) });
                }}
                className="w-16 rounded-md border border-jul-border bg-jul-surface px-2 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
              <button
                type="button"
                onClick={() => {
                  removeBackend(i);
                }}
                disabled={draft.backends.length <= 1}
                className="rounded-md border border-jul-border px-2 py-1.5 text-sm text-jul-muted hover:text-jul-danger disabled:opacity-30"
              >
                ✕
              </button>
            </div>
          ))}
          <button
            type="button"
            onClick={addBackend}
            className="rounded-md border border-jul-border px-3 py-1 text-xs text-jul-text hover:bg-jul-surface"
          >
            + Add backend
          </button>
        </div>

        <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
          <label className="flex items-center gap-2 text-sm text-jul-text">
            <input
              type="checkbox"
              checked={draft.healthCheck}
              onChange={(e) => {
                set("healthCheck", e.target.checked);
              }}
              className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
            />
            Enable active health checks
          </label>
          {draft.healthCheck && (
            <div className="grid grid-cols-2 gap-3 pt-1">
              <TextField
                label="Probe path"
                value={draft.healthCheckPath}
                placeholder="/healthz"
                onChange={(v) => {
                  set("healthCheckPath", v);
                }}
              />
              <TextField
                label="Interval"
                value={draft.healthCheckInterval}
                placeholder="5s"
                onChange={(v) => {
                  set("healthCheckInterval", v);
                }}
              />
            </div>
          )}
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