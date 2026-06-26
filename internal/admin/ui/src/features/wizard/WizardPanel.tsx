import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { generateConfig, type WizardInput } from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";

type Mode = "serve" | "proxy" | "app";

const PRESETS: { id: string; label: string }[] = [
  { id: "generic", label: "Generic HTTP app" },
  { id: "express", label: "Express / Node.js" },
  { id: "apollo", label: "Apollo GraphQL" },
  { id: "fastapi", label: "FastAPI" },
  { id: "django", label: "Django / Flask" },
  { id: "go", label: "Go HTTP app" },
  { id: "grpc", label: "gRPC backend" },
];

function Field({
  label,
  hint,
  value,
  placeholder,
  onChange,
}: {
  readonly label: string;
  readonly hint: string;
  readonly value: string;
  readonly placeholder: string;
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
      <span className="text-xs text-jul-muted">{hint}</span>
    </label>
  );
}

export function WizardPanel() {
  const navigate = useNavigate();
  const [mode, setMode] = useState<Mode>("serve");
  const [path, setPath] = useState("");
  const [target, setTarget] = useState("");
  const [listen, setListen] = useState("");
  const [preview, setPreview] = useState<string | null>(null);

  // App-mode state.
  const [appName, setAppName] = useState("");
  const [backends, setBackends] = useState("");
  const [preset, setPreset] = useState("generic");
  const [routePath, setRoutePath] = useState("/");
  const [healthCheck, setHealthCheck] = useState(true);

  const generate = useMutation({
    mutationFn: (input: WizardInput) => generateConfig(input),
    onSuccess: (toml) => {
      setPreview(toml);
    },
  });

  function backendList(): string[] {
    return backends
      .split(/[\n,]/)
      .map((b) => b.trim())
      .filter((b) => b !== "");
  }

  const ready =
    mode === "serve"
      ? path.trim() !== ""
      : mode === "proxy"
        ? target.trim() !== ""
        : appName.trim() !== "" && backendList().length > 0;

  function onGenerate(): void {
    let input: WizardInput;
    if (mode === "serve") {
      input = { mode, listen: listen.trim() || undefined, path: path.trim() };
    } else if (mode === "proxy") {
      input = { mode, listen: listen.trim() || undefined, target: target.trim() };
    } else {
      input = {
        mode,
        listen: listen.trim() || undefined,
        name: appName.trim(),
        backends: backendList(),
        preset,
        route_path: routePath.trim() || "/",
        health_check: healthCheck,
      };
    }
    generate.mutate(input);
  }

  function openInEditor(): void {
    if (preview === null) return;
    setPendingDraft({ kind: "toml", toml: preview });
    void navigate("/config");
  }

  return (
    <div className="max-w-3xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold">Setup Wizard</h1>
        <p className="text-sm text-jul-muted">
          Generate a starter configuration, then review and apply it in the editor.
        </p>
      </div>

      <div className="space-y-4 rounded-lg border border-jul-border bg-jul-surface p-5">
        <div className="space-y-1">
          <span className="text-sm font-medium text-jul-text">What do you want to do?</span>
          <div className="flex flex-wrap gap-2">
            {(["serve", "proxy", "app"] as const).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => {
                  setMode(m);
                }}
                className={`rounded-md px-4 py-1.5 text-sm font-medium ${
                  mode === m
                    ? "bg-jul-accent text-jul-bg"
                    : "border border-jul-border text-jul-muted hover:text-jul-text"
                }`}
              >
                {m === "serve"
                  ? "Serve a directory"
                  : m === "proxy"
                    ? "Proxy a target"
                    : "Put an app behind Jul"}
              </button>
            ))}
          </div>
        </div>

        {mode === "serve" && (
          <Field
            label="Directory"
            hint="Absolute path of the static files to serve."
            value={path}
            placeholder="/var/www/site"
            onChange={setPath}
          />
        )}

        {mode === "proxy" && (
          <Field
            label="Upstream target"
            hint="URL of the backend to proxy to."
            value={target}
            placeholder="http://127.0.0.1:8080"
            onChange={setTarget}
          />
        )}

        {mode === "app" && (
          <div className="space-y-4">
            <p className="text-xs text-jul-muted">
              An app puts one or more backend instances behind Jul as a load-balanced upstream
              pool, then mounts a reverse-proxy route to it. Presets pick sensible defaults — you
              can edit everything before applying.
            </p>
            <Field
              label="App name"
              hint="Used as the upstream pool name (letters, numbers, dashes)."
              value={appName}
              placeholder="backend"
              onChange={setAppName}
            />
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Backends</span>
              <textarea
                value={backends}
                placeholder={"127.0.0.1:3000\n127.0.0.1:3001"}
                onChange={(e) => {
                  setBackends(e.target.value);
                }}
                rows={3}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
              <span className="text-xs text-jul-muted">
                One <code>host:port</code> per line (or comma-separated). Add several to load-balance.
              </span>
            </label>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Framework preset</span>
              <select
                value={preset}
                onChange={(e) => {
                  setPreset(e.target.value);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                {PRESETS.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.label}
                  </option>
                ))}
              </select>
              <span className="text-xs text-jul-muted">
                Presets only influence defaults (strategy, health-check path) — no framework magic.
              </span>
            </label>
            <Field
              label="Mount path"
              hint="Path prefix the app is served on."
              value={routePath}
              placeholder="/"
              onChange={setRoutePath}
            />
            <label className="flex items-center gap-2 text-sm text-jul-text">
              <input
                type="checkbox"
                checked={healthCheck}
                onChange={(e) => {
                  setHealthCheck(e.target.checked);
                }}
              />
              Enable active health checks
            </label>
          </div>
        )}

        <Field
          label="Listen address (optional)"
          hint="Defaults to the framework's standard listener when left blank."
          value={listen}
          placeholder=":8443"
          onChange={setListen}
        />

        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={onGenerate}
            disabled={!ready || generate.isPending}
            className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            {generate.isPending ? "Generating…" : "Generate config"}
          </button>
          {generate.isError && (
            <span className="text-xs text-jul-danger">
              {generate.error instanceof Error
                ? generate.error.message
                : "Generation failed."}
            </span>
          )}
        </div>
      </div>

      {preview !== null && (
        <div className="space-y-3 rounded-lg border border-jul-border bg-jul-surface">
          <div className="flex items-center justify-between border-b border-jul-border px-4 py-2">
            <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
              Generated configuration
            </span>
            <button
              type="button"
              onClick={openInEditor}
              className="rounded-md bg-jul-accent px-3 py-1 text-xs font-medium text-jul-bg hover:brightness-110"
            >
              Open in editor →
            </button>
          </div>
          <pre className="max-h-[50vh] overflow-auto px-4 pb-4 font-mono text-xs leading-relaxed text-jul-text">
            {preview}
          </pre>
        </div>
      )}
    </div>
  );
}