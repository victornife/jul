/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchTrafficControls } from "@/api/client.ts";
import { GlobalSettingsEditor } from "@/features/config/GlobalSettingsEditor.tsx";
import {
  TrafficControlEditor,
  type TrafficEditorKind,
} from "@/features/traffic-controls/TrafficControlEditor.tsx";
import { TracingEditor } from "@/features/traffic-controls/TracingEditor.tsx";
import { AccessLogEditor } from "@/features/traffic-controls/AccessLogEditor.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { Loading } from "@/components/ui.tsx";

function SectionCard({
  title,
  active,
  onEdit,
  children,
}: {
  readonly title: string;
  readonly active?: boolean;
  readonly onEdit: () => void;
  readonly children: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      <div className="flex items-center gap-3 border-b border-jul-border px-4 py-3">
        <span className="font-medium text-jul-text">{title}</span>
        {active !== undefined && (
          <span
            className={`rounded-full px-2 py-0.5 text-xs font-medium ${
              active ? "bg-jul-success/15 text-jul-success" : "bg-jul-border text-jul-muted"
            }`}
          >
            {active ? "enabled" : "disabled"}
          </span>
        )}
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
      <span className="w-32 shrink-0 text-jul-muted">{k}</span>
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
  const [globalEditing, setGlobalEditing] = useState(false);
  const [tracingEditing, setTracingEditing] = useState(false);
  const [accessLogEditing, setAccessLogEditing] = useState(false);

  if (isLoading) return <Loading label="Loading traffic controls…" />;
  if (isError || !data) {
    return <PanelError error={error} resource="traffic controls" onRetry={() => void refetch()} />;
  }

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">Global & Traffic Controls</h1>
        <p className="max-w-3xl text-sm text-jul-muted">
          Guided global, compression, rate-limit, and server-limit edits use sparse typed patches.
          Cache stays a complete raw table that is saved for the next restart. Every action is chosen
          from the server lifecycle assessment and finalized through the correlated Config workflow.
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <SectionCard title="Global settings" onEdit={() => { setGlobalEditing(true); }}>
          <div className="space-y-1">
            <KV k="worker threads" v={data.global?.worker_threads} />
            <KV k="log level" v={data.global?.log_level} />
            <KV k="log format" v={data.global?.log_format} />
            <KV k="reload timeout" v={data.global?.reload_timeout} />
          </div>
        </SectionCard>

        <SectionCard
          title="Compression"
          active={data.compression?.enabled ?? false}
          onEdit={() => { setEditing("compression"); }}
        >
          <div className="space-y-1">
            <KV k="encoders" v={(data.compression?.encoders ?? []).join(", ")} />
            <KV k="level" v={data.compression?.level} />
            <KV k="minimum size" v={data.compression?.min_size} />
          </div>
        </SectionCard>

        <SectionCard
          title="Rate limiting"
          active={data.rate_limit?.enabled ?? false}
          onEdit={() => { setEditing("rate_limit"); }}
        >
          <div className="space-y-1">
            <KV k="key" v={data.rate_limit?.key} />
            <KV k="rate" v={data.rate_limit?.rate} />
            <KV k="burst" v={data.rate_limit?.burst} />
            <KV k="max connections" v={data.rate_limit?.max_conns} />
          </div>
        </SectionCard>

        <SectionCard
          title="Cache"
          active={data.cache?.enabled ?? false}
          onEdit={() => { setEditing("cache"); }}
        >
          <div className="space-y-1">
            <KV k="default TTL" v={data.cache?.default_ttl} />
            <KV k="memory max" v={data.cache?.memory_max_size ?? data.cache?.memory_max} />
            <KV k="disk max" v={data.cache?.disk_max_size} />
            <KV k="stale if error" v={data.cache?.stale_if_error} />
          </div>
        </SectionCard>

        <SectionCard title="Limits & Timeouts" onEdit={() => { setEditing("limits"); }}>
          <p className="text-xs text-jul-muted">
            Per-server body/read/write/idle values are seeded from the selected server and sent only
            when changed. Listener-bound lifecycle remains authoritative on the server.
          </p>
        </SectionCard>

        <SectionCard
          title="Access Logging"
          active={data.access_log?.enabled ?? true}
          onEdit={() => { setAccessLogEditing(true); }}
        >
          <div className="space-y-1">
            <KV k="sinks" v={(data.access_log?.sinks ?? ["stdout"]).join(", ")} />
            <KV k="format" v={data.access_log?.format || "text"} />
          </div>
        </SectionCard>

        <SectionCard
          title="Distributed Tracing"
          active={data.tracing?.enabled ?? false}
          onEdit={() => { setTracingEditing(true); }}
        >
          <div className="space-y-1">
            <KV k="exporter" v={data.tracing?.exporter} />
            <KV k="endpoint" v={data.tracing?.endpoint} />
            <KV k="service" v={data.tracing?.service_name} />
          </div>
        </SectionCard>
      </div>

      {globalEditing && data.global && (
        <GlobalSettingsEditor current={data.global} onClose={() => { setGlobalEditing(false); }} />
      )}
      {editing && (
        <TrafficControlEditor kind={editing} current={data} onClose={() => { setEditing(null); }} />
      )}
      {tracingEditing && (
        <TracingEditor current={data} onClose={() => { setTracingEditing(false); }} />
      )}
      {accessLogEditing && (
        <AccessLogEditor current={data} onClose={() => { setAccessLogEditing(false); }} />
      )}
    </div>
  );
}
