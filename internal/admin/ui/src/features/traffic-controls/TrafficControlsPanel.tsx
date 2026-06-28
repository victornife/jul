import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchTrafficControls } from "@/api/client.ts";
import {
  TrafficControlEditor,
  type TrafficEditorKind,
} from "@/features/traffic-controls/TrafficControlEditor.tsx";
import { TracingEditor } from "@/features/traffic-controls/TracingEditor.tsx";
import { PanelError } from "@/components/PanelError.tsx";

function SectionCard({
  title,
  active,
  onEdit,
  children,
}: {
  readonly title: string;
  readonly active: boolean;
  readonly onEdit: () => void;
  readonly children: React.ReactNode;
}) {
  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      <div className="flex items-center gap-3 border-b border-jul-border px-4 py-3">
        <span className="font-medium text-jul-text">{title}</span>
        <span
          className={`rounded-full px-2 py-0.5 text-xs font-medium ${
            active
              ? "bg-jul-success/15 text-jul-success"
              : "bg-jul-border text-jul-muted"
          }`}
        >
          {active ? "enabled" : "disabled"}
        </span>
        <button
          type="button"
          onClick={onEdit}
          className="ml-auto rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg"
        >
          Edit
        </button>
      </div>
      <div className="px-4 py-3">{children}</div>
    </div>
  );
}

function KV({ k, v }: { readonly k: string; readonly v: string | number | undefined }) {
  if (v === undefined || v === "") return null;
  return (
    <div className="flex gap-3 text-sm">
      <span className="w-28 shrink-0 text-jul-muted">{k}</span>
      <span className="font-mono text-jul-text">{v}</span>
    </div>
  );
}

export function TrafficControlsPanel() {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["traffic-controls"],
    queryFn: fetchTrafficControls,
  });

  const [editing, setEditing] = useState<TrafficEditorKind | null>(null);
  const [tracingEditing, setTracingEditing] = useState(false);

  if (isLoading) return <div className="text-jul-muted">Loading traffic controls…</div>;
  if (isError || !data)
    return <PanelError error={error} resource="traffic controls" onRetry={() => void refetch()} />;

  return (
    <div className="space-y-6">
      {/* Self-explanatory header (Milestone 4.3) */}
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">Traffic Controls</h1>
        <p className="max-w-3xl text-sm text-jul-muted">
          Traffic controls shape how Jul handles requests and responses: compression,
          caching, rate limits, and distributed tracing. Changes here are generated as
          configuration and applied safely through validate → diff → apply, so nothing
          takes effect until you confirm.
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {/* Compression */}
        <SectionCard
          title="Compression"
          active={data.compression?.enabled ?? false}
          onEdit={() => {
            setEditing("compression");
          }}
        >
          {data.compression?.enabled ? (
            <div className="space-y-1">
              <KV
                k="encoders"
                v={
                  data.compression.encoders && data.compression.encoders.length > 0
                    ? data.compression.encoders.join(", ")
                    : "gzip"
                }
              />
            </div>
          ) : (
            <p className="text-xs text-jul-muted">
              Compression is off. Enable it to shrink text, JSON, and SVG responses before
              they are sent to clients.
            </p>
          )}
        </SectionCard>

        {/* Rate Limiting */}
        <SectionCard
          title="Rate Limiting"
          active={data.rate_limit?.enabled ?? false}
          onEdit={() => {
            setEditing("rate_limit");
          }}
        >
          {data.rate_limit?.enabled ? (
            <div className="space-y-1">
              <KV k="key" v={data.rate_limit.key || "ip"} />
              <KV k="rate" v={data.rate_limit.rate !== undefined ? `${String(data.rate_limit.rate)}/s` : undefined} />
              <KV k="burst" v={data.rate_limit.burst} />
            </div>
          ) : (
            <p className="text-xs text-jul-muted">
              No rate limit configured. Add one to protect upstreams from spikes and abuse,
              keyed by client IP, a header, or a JWT claim.
            </p>
          )}
        </SectionCard>

        {/* Cache */}
        <SectionCard
          title="Cache"
          active={data.cache?.enabled ?? false}
          onEdit={() => {
            setEditing("cache");
          }}
        >
          {data.cache?.enabled ? (
            <div className="space-y-1">
              <KV k="default TTL" v={data.cache.default_ttl} />
              <KV k="memory max" v={data.cache.memory_max} />
              {data.cache.disk_path && <KV k="disk" v="enabled" />}
            </div>
          ) : (
            <p className="text-xs text-jul-muted">
              No cache configured. Enable it to serve repeat responses from memory or disk
              and reduce upstream load — avoid caching authenticated or per-user responses.
            </p>
          )}
        </SectionCard>

        {/* Limits & Timeouts (Milestone 3.4) */}
        <SectionCard
          title="Limits & Timeouts"
          active={false}
          onEdit={() => {
            setEditing("limits");
          }}
        >
          <p className="text-xs text-jul-muted">
            Per-server request body limit and read/write/idle timeouts. Configure these to
            protect the server from oversized or slow requests; the generated keys are placed
            under the server block you choose in the editor.
          </p>
        </SectionCard>

        {/* Distributed tracing (Phase 4d) */}
        <SectionCard
          title="Distributed Tracing"
          active={data.tracing?.enabled ?? false}
          onEdit={() => {
            setTracingEditing(true);
          }}
        >
          {data.tracing?.enabled ? (
            <div className="space-y-1">
              <KV k="exporter" v={data.tracing.exporter || "otlp-grpc"} />
              <KV k="endpoint" v={data.tracing.endpoint} />
              <KV
                k="sample ratio"
                v={data.tracing.sample_ratio !== undefined ? String(data.tracing.sample_ratio) : undefined}
              />
              <KV k="service" v={data.tracing.service_name} />
              {data.tracing.insecure && <KV k="transport" v="insecure (plaintext)" />}
            </div>
          ) : (
            <p className="text-xs text-jul-muted">
              Tracing is off. Enable it to export request spans to an OpenTelemetry collector
              (OTLP). Requires a binary built with the otel tag.
            </p>
          )}
        </SectionCard>
      </div>

      {editing && (
        <TrafficControlEditor
          kind={editing}
          current={data}
          onClose={() => {
            setEditing(null);
          }}
        />
      )}

      {tracingEditing && (
        <TracingEditor
          current={data}
          onClose={() => {
            setTracingEditing(false);
          }}
        />
      )}
    </div>
  );
}