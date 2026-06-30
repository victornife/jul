import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { PageHeader, Button, Loading } from "@/components/ui.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { fetchRoutes, uploadTranscodeDescriptor, type TranscodeMethod } from "@/api/client.ts";
import { fetchRawConfig } from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import { generateTranscodeRouteToml, transcodeDraftWarnings } from "@/lib/transcodeToml.ts";
import { appendFragment } from "@/lib/routeToml.ts";

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
        onChange={(e) => onChange(e.target.value)}
        className={`w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent ${mono ? "font-mono" : ""}`}
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
        onChange={(e) => onChange(e.target.checked)}
        className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
      />
      {label}
    </label>
  );
}

function Section({
  title,
  children,
}: {
  readonly title: string;
  readonly children: React.ReactNode;
}) {
  return (
    <section className="space-y-3 rounded-lg border border-jul-border bg-jul-surface p-4">
      <h2 className="text-sm font-semibold text-jul-text">{title}</h2>
      {children}
    </section>
  );
}

export function TranscodeDesignerPanel() {
  const navigate = useNavigate();
  const routesQuery = useQuery({ queryKey: ["routes"], queryFn: fetchRoutes });

  const [listen, setListen] = useState(":8080");
  const [serverNames, setServerNames] = useState("");
  const [path, setPath] = useState("/");
  const [matchType, setMatchType] = useState<"prefix" | "exact" | "regex">("prefix");

  const [descriptorPath, setDescriptorPath] = useState("");
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [methods, setMethods] = useState<TranscodeMethod[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const [target, setTarget] = useState("");
  const [tls, setTls] = useState(false);
  const [preserveNames, setPreserveNames] = useState(false);
  const [streaming, setStreaming] = useState(false);
  const [streamMode, setStreamMode] = useState<"ndjson" | "sse">("ndjson");

  const [editorError, setEditorError] = useState<string | null>(null);

  const servers = routesQuery.data ?? [];
  const serverOptions = Array.from(new Map(servers.map((r) => [r.listen, r])).values());

  async function handleUpload(file: File) {
    setUploading(true);
    setUploadError(null);
    setMethods([]);
    setSelected(new Set());
    try {
      const res = await uploadTranscodeDescriptor(file);
      setMethods(res.methods);
      // Default: select all
      setSelected(new Set(res.methods.map((m) => m.full_name)));
    } catch (err) {
      setUploadError(err instanceof Error ? err.message : "Upload failed.");
    } finally {
      setUploading(false);
    }
  }

  function toggleMethod(fullName: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(fullName)) next.delete(fullName);
      else next.add(fullName);
      return next;
    });
  }

  async function openInEditor() {
    setEditorError(null);
    const draft = {
      listen,
      serverNames,
      path,
      matchType,
      target,
      descriptorSet: descriptorPath,
      selectedMethods: Array.from(selected),
      tls,
      preserveNames,
      streaming,
      streamMode,
    };
    const warnings = transcodeDraftWarnings(draft);
    if (warnings.length > 0) {
      setEditorError(warnings.join(" "));
      return;
    }
    try {
      const raw = await fetchRawConfig();
      const fragment = generateTranscodeRouteToml(draft);
      setPendingDraft({ kind: "toml", toml: appendFragment(raw.raw ?? "", fragment) });
      void navigate("/config");
    } catch {
      setEditorError("Could not load the current configuration to merge this route.");
    }
  }

  if (routesQuery.isLoading) return <Loading />;
  if (routesQuery.isError) return <PanelError error={routesQuery.error} resource="routes" />;

  return (
    <div className="space-y-5">
      <PageHeader
        title="gRPC Route Designer"
        description="Design a gRPC-JSON transcoding route by uploading a compiled protobuf descriptor set, inspecting HTTP bindings, and generating the configuration."
        actions={
          <Button
            variant="secondary"
            onClick={() => {
              navigate("/routes");
            }}
          >
            ← Back to routes
          </Button>
        }
      />

      <Section title="Server &amp; route">
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-2">
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Listen address</span>
              <select
                value={listen}
                onChange={(e) => setListen(e.target.value)}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value=":8080">New server — :8080</option>
                {serverOptions.map((s) => (
                  <option key={s.listen} value={s.listen}>
                    {s.listen}
                    {s.server_names && s.server_names.length > 0 ? ` (${s.server_names.join(", ")})` : ""}
                  </option>
                ))}
              </select>
            </label>
            {listen === ":8080" && (
              <TextField
                label="Custom listen"
                value={listen}
                placeholder=":8080"
                onChange={setListen}
              />
            )}
          </div>
          <TextField
            label="Server names (optional)"
            value={serverNames}
            placeholder="api.example.com"
            hint="Comma-separated. Leave empty for catch-all."
            onChange={setServerNames}
          />
        </div>
        <div className="grid gap-3 sm:grid-cols-3">
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Match type</span>
            <select
              value={matchType}
              onChange={(e) => setMatchType(e.target.value as "prefix" | "exact" | "regex")}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="prefix">Prefix</option>
              <option value="exact">Exact</option>
              <option value="regex">Regex</option>
            </select>
          </label>
          <div className="sm:col-span-2">
            <TextField label="Match path" value={path} placeholder="/" onChange={setPath} />
          </div>
        </div>
      </Section>

      <Section title="Descriptor set">
        <div className="space-y-3">
          <p className="text-xs text-jul-muted">
            Upload a protoc-generated <code className="rounded bg-jul-bg px-1 py-0.5 font-mono">.pb</code> file (
            <code className="rounded bg-jul-bg px-1 py-0.5 font-mono">--descriptor_set_out</code> with{" "}
            <code className="rounded bg-jul-bg px-1 py-0.5 font-mono">--include_imports</code>). Jul
            reads the <code className="rounded bg-jul-bg px-1 py-0.5 font-mono">google.api.http</code>{" "}
            annotations and lists the methods you can expose.
          </p>
          <div className="flex items-center gap-3">
            <label className="inline-flex cursor-pointer">
              <input
                type="file"
                accept=".pb"
                className="hidden"
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  if (f) void handleUpload(f);
                }}
              />
              <span className="rounded-md border border-jul-border px-3 py-1.5 text-sm font-medium text-jul-text transition-[filter,background-color,color] hover:bg-jul-bg focus:outline-none focus:ring-2 focus:ring-jul-accent">
                Upload .pb
              </span>
            </label>
            {uploading && <span className="text-sm text-jul-muted">Parsing…</span>}
            {uploadError && (
              <span className="text-sm text-jul-danger">{uploadError}</span>
            )}
          </div>
        </div>

        {methods.length > 0 && (
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-xs font-medium text-jul-text">
                {selected.size}/{methods.length} methods selected
              </span>
              <div className="flex gap-2">
                <Button
                  variant="ghost"
                  onClick={() => setSelected(new Set(methods.map((m) => m.full_name)))}
                >
                  Select all
                </Button>
                <Button variant="ghost" onClick={() => setSelected(new Set())}>
                  Clear
                </Button>
              </div>
            </div>
            <div className="overflow-x-auto rounded-md border border-jul-border">
              <table className="w-full text-left text-sm">
                <thead className="bg-jul-bg text-xs uppercase text-jul-muted">
                  <tr>
                    <th className="px-3 py-2"></th>
                    <th className="px-3 py-2">Method</th>
                    <th className="px-3 py-2">HTTP</th>
                    <th className="px-3 py-2">Path</th>
                    <th className="px-3 py-2">Body</th>
                    <th className="px-3 py-2">Streaming</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-jul-border">
                  {methods.map((m) => (
                    <tr key={m.full_name} className={selected.has(m.full_name) ? "" : "opacity-60"}>
                      <td className="px-3 py-2">
                        <input
                          type="checkbox"
                          checked={selected.has(m.full_name)}
                          onChange={() => toggleMethod(m.full_name)}
                          className="h-4 w-4 accent-jul-accent"
                        />
                      </td>
                      <td className="px-3 py-2 font-mono text-xs text-jul-text">{m.full_name}</td>
                      <td className="px-3 py-2 text-xs text-jul-text">{m.http_method}</td>
                      <td className="px-3 py-2 font-mono text-xs text-jul-text">{m.path}</td>
                      <td className="px-3 py-2 text-xs text-jul-muted">{m.body || "—"}</td>
                      <td className="px-3 py-2 text-xs">
                        {m.streaming ? (
                          <span className="text-jul-warning">Yes</span>
                        ) : (
                          <span className="text-jul-muted">No</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </Section>

      <Section title="Transcode options">
        <div className="grid gap-3 sm:grid-cols-2">
          <TextField
            label="gRPC backend target"
            value={target}
            placeholder="upstream-name or host:port"
            hint="Upstream name from Apps, or a literal host:port."
            onChange={setTarget}
            mono
          />
          <TextField
            label="Descriptor set path"
            value={descriptorPath}
            placeholder="./descriptors/api.pb"
            hint="Path on the server filesystem where the .pb lives."
            onChange={setDescriptorPath}
            mono
          />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <Toggle label="Dial backend over TLS" checked={tls} onChange={setTls} />
          <Toggle
            label="Preserve proto field names in JSON"
            checked={preserveNames}
            onChange={setPreserveNames}
          />
          <Toggle
            label="Enable streaming (server/client/bidi)"
            checked={streaming}
            onChange={setStreaming}
          />
          {streaming && (
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Stream framing</span>
              <select
                value={streamMode}
                onChange={(e) => setStreamMode(e.target.value as "ndjson" | "sse")}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value="ndjson">NDJSON (newline-delimited JSON)</option>
                <option value="sse">Server-Sent Events</option>
              </select>
            </label>
          )}
        </div>
      </Section>

      <div className="flex items-center justify-between gap-3">
        {editorError && <span className="text-sm text-jul-danger">{editorError}</span>}
        {methods.length > 0 && selected.size === 0 && (
          <span className="text-sm text-jul-warning">
            Select at least one method to generate the route.
          </span>
        )}
        <Button variant="primary" onClick={() => void openInEditor()}>
          Review in editor →
        </Button>
      </div>
    </div>
  );
}
