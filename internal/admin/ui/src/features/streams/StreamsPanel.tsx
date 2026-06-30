import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Drawer } from "@/components/Drawer.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { Loading, MaturityBadge } from "@/components/ui.tsx";
import {
  fetchStreams,
  type StreamProjection,
} from "@/api/client.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import {
  emptyStreamDraft,
  seedStreamDraft,
  streamDraftToPatch,
  streamDraftWarnings,
  streamSummary,
  type StreamDraft,
} from "@/lib/streams.ts";

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

// StreamEditorDrawer creates or edits one [[stream]] L4 listener. In edit mode
// it targets the existing stream by its original protocol + listen identity, so
// changing either re-keys the listener (surfaced as a remove + add in the diff).
function StreamEditorDrawer({
  existing,
  onClose,
}: {
  readonly existing: StreamProjection | null;
  readonly onClose: () => void;
}) {
  const { error, busy, run } = useRunPatch();
  const isNew = existing === null;
  const [draft, setDraft] = useState<StreamDraft>(() =>
    existing ? seedStreamDraft(existing) : emptyStreamDraft(),
  );
  const warnings = streamDraftWarnings(draft);
  const isTCP = draft.protocol === "tcp";

  function set<K extends keyof StreamDraft>(key: K, val: StreamDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: val }));
  }

  function save(): void {
    const patch = streamDraftToPatch(draft);
    if (isNew) {
      run({ op: "stream_add", stream: patch });
    } else {
      run({
        op: "stream_set",
        listen: existing.listen,
        stream_protocol: existing.protocol,
        stream: patch,
      });
    }
  }

  return (
    <Drawer
      title={isNew ? "New L4 stream" : "Edit L4 stream"}
      subtitle={existing ? `${existing.protocol} ${existing.listen}` : ""}
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
          A stream is an L4 (TCP/UDP) reverse-proxy listener. It forwards raw connections or
          datagrams to a backend without parsing the application protocol. For TLS it can route by
          SNI host without terminating it (passthrough).
        </p>

        <div className="grid grid-cols-2 gap-3">
          <TextField
            label="Listen"
            value={draft.listen}
            placeholder="0.0.0.0:5432"
            mono
            hint="The bind address (host:port)."
            onChange={(v) => {
              set("listen", v);
            }}
          />
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Protocol</span>
            <select
              value={draft.protocol}
              onChange={(e) => {
                set("protocol", e.target.value === "udp" ? "udp" : "tcp");
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="tcp">TCP</option>
              <option value="udp">UDP</option>
            </select>
          </label>
        </div>

        <TextField
          label="Default backend"
          value={draft.proxyPass}
          placeholder="db  or  127.0.0.1:5432"
          mono
          hint="A named upstream or a literal host:port, used when no SNI route matches."
          onChange={(v) => {
            set("proxyPass", v);
          }}
        />

        {isTCP && (
          <>
            <TextArea
              label="SNI routes"
              value={draft.sniRoutes}
              placeholder={"app.example.com = app-backend\n*.internal = 10.0.0.5:443"}
              rows={3}
              hint="One host = target per line. Setting any route enables SNI inspection (TLS is not terminated). A * key is a catch-all."
              onChange={(v) => {
                set("sniRoutes", v);
              }}
            />
            <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
              <Toggle
                label="TLS passthrough"
                checked={draft.tlsPassthrough}
                onChange={(v) => {
                  set("tlsPassthrough", v);
                }}
              />
              <label className="block space-y-1">
                <span className="text-sm font-medium text-jul-text">PROXY protocol</span>
                <select
                  value={draft.proxyProtocol}
                  onChange={(e) => {
                    const v = e.target.value;
                    set(
                      "proxyProtocol",
                      v === "in" || v === "out" || v === "both" ? v : "",
                    );
                  }}
                  className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                >
                  <option value="">Off</option>
                  <option value="in">Parse from client (in)</option>
                  <option value="out">Emit to backend (out)</option>
                  <option value="both">Both</option>
                </select>
                <span className="text-xs text-jul-muted">
                  Preserves the real client address across the proxy hop (TCP only).
                </span>
              </label>
            </div>
          </>
        )}

        <div className="grid grid-cols-2 gap-3">
          <TextField
            label="Connect timeout"
            value={draft.connectTimeout}
            placeholder="10s"
            mono
            hint="Bounds dialing the backend (default 10s)."
            onChange={(v) => {
              set("connectTimeout", v);
            }}
          />
          <TextField
            label="Idle timeout"
            value={draft.idleTimeout}
            placeholder="5m"
            mono
            hint="Closes an idle relay (default 5m)."
            onChange={(v) => {
              set("idleTimeout", v);
            }}
          />
        </div>

        <Warnings items={warnings} />
      </div>
    </Drawer>
  );
}

function StreamCard({
  stream,
  onEdit,
  onRemove,
}: {
  readonly stream: StreamProjection;
  readonly onEdit: () => void;
  readonly onRemove: () => void;
}) {
  return (
    <div className="space-y-3 rounded-lg border border-jul-border bg-jul-surface p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm font-semibold text-jul-text">{stream.listen}</span>
            <span className="rounded-full bg-jul-accent/15 px-2 py-0.5 text-xs font-medium uppercase text-jul-accent">
              {stream.protocol}
            </span>
            {stream.tls_passthrough && (
              <span className="rounded-full bg-jul-success/15 px-2 py-0.5 text-xs font-medium text-jul-success">
                TLS passthrough
              </span>
            )}
          </div>
          <p className="mt-1 truncate text-xs text-jul-muted">{streamSummary(stream)}</p>
        </div>
        <div className="flex shrink-0 gap-2">
          <button
            type="button"
            onClick={onEdit}
            className="rounded-md border border-jul-border px-2 py-1 text-xs text-jul-text hover:border-jul-accent"
          >
            Edit
          </button>
          <button
            type="button"
            onClick={onRemove}
            className="rounded-md border border-jul-border px-2 py-1 text-xs text-jul-danger hover:border-jul-danger"
          >
            Remove
          </button>
        </div>
      </div>
    </div>
  );
}

export function StreamsPanel() {
  const { run: runRemove } = useRunPatch();
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["streams"],
    queryFn: fetchStreams,
  });
  const [editing, setEditing] = useState<StreamProjection | null>(null);
  const [creating, setCreating] = useState(false);

  if (isLoading) return <Loading label="Loading streams…" />;
  if (isError || !data)
    return <PanelError error={error} resource="streams" onRetry={() => void refetch()} />;

  function remove(stream: StreamProjection): void {
    runRemove({ op: "stream_remove", listen: stream.listen, stream_protocol: stream.protocol });
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-xl font-semibold">L4 streams</h1>
            <MaturityBadge level="beta" />
          </div>
          <p className="text-sm text-jul-muted">
            Raw TCP and UDP reverse-proxy listeners operating below the HTTP layer.
          </p>
        </div>
        <button
          type="button"
          onClick={() => {
            setCreating(true);
          }}
          className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110"
        >
          New stream
        </button>
      </div>

      {!data.compiled && (
        <div className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
          This build does not include the L4 stream proxy (the <code>stream</code> tag). You can
          edit stream declarations here, but a lean binary refuses to start with them — run a
          stream-enabled binary to serve them.
        </div>
      )}

      {data.streams.length === 0 ? (
        <p className="rounded-lg border border-jul-border bg-jul-surface p-4 text-sm text-jul-muted">
          No L4 streams are declared. Add one to reverse-proxy raw TCP or UDP traffic.
        </p>
      ) : (
        <div className="space-y-3">
          {data.streams.map((s) => (
            <StreamCard
              key={`${s.protocol}/${s.listen}`}
              stream={s}
              onEdit={() => {
                setEditing(s);
              }}
              onRemove={() => {
                remove(s);
              }}
            />
          ))}
        </div>
      )}

      {creating && (
        <StreamEditorDrawer
          existing={null}
          onClose={() => {
            setCreating(false);
          }}
        />
      )}
      {editing && (
        <StreamEditorDrawer
          existing={editing}
          onClose={() => {
            setEditing(null);
          }}
        />
      )}
    </div>
  );
}
