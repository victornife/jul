import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { generateConfig, type WizardInput } from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";

type Mode = "serve" | "proxy";

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

  const generate = useMutation({
    mutationFn: (input: WizardInput) => generateConfig(input),
    onSuccess: (toml) => {
      setPreview(toml);
    },
  });

  const ready = mode === "serve" ? path.trim() !== "" : target.trim() !== "";

  function onGenerate(): void {
    const input: WizardInput = {
      mode,
      listen: listen.trim() || undefined,
      ...(mode === "serve" ? { path: path.trim() } : { target: target.trim() }),
    };
    generate.mutate(input);
  }

  function openInEditor(): void {
    if (preview === null) return;
    setPendingDraft(preview);
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
          <span className="text-sm font-medium text-jul-text">Mode</span>
          <div className="flex gap-2">
            {(["serve", "proxy"] as const).map((m) => (
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
                {m === "serve" ? "Serve a directory" : "Proxy a target"}
              </button>
            ))}
          </div>
        </div>

        {mode === "serve" ? (
          <Field
            label="Directory"
            hint="Absolute path of the static files to serve."
            value={path}
            placeholder="/var/www/site"
            onChange={setPath}
          />
        ) : (
          <Field
            label="Upstream target"
            hint="URL of the backend to proxy to."
            value={target}
            placeholder="http://127.0.0.1:8080"
            onChange={setTarget}
          />
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
