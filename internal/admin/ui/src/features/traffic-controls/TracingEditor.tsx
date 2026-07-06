/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import { fetchRawConfig, type TrafficControls } from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import { upsertTopLevelTable } from "@/lib/trafficToml.ts";
import {
  emptyTracingDraft,
  generateTracingToml,
  tracingWarnings,
  type TracingDraft,
  type TracingExporter,
} from "@/lib/tracingToml.ts";

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

export interface TracingEditorProps {
  readonly current: TrafficControls;
  readonly onClose: () => void;
}

// seedDraft maps the projected [observability.tracing] config into an editor
// draft so every field round-trips: opening and re-saving without changes
// reproduces the same block rather than dropping the exporter, endpoint,
// sampling ratio, or service name.
function seedDraft(current: TrafficControls): TracingDraft {
  const t = current.tracing;
  if (!t) return emptyTracingDraft();
  return {
    enabled: t.enabled,
    exporter: t.exporter === "otlp-http" ? "otlp-http" : "otlp-grpc",
    endpoint: t.endpoint ?? "",
    sampleRatio: t.sample_ratio ?? 1,
    serviceName: t.service_name ?? "jul",
    insecure: t.insecure ?? false,
  };
}

/**
 * Guided tracing editor (Phase 4d). It edits the global [observability.tracing]
 * table, upserts it into the running config, and hands the draft to the Config
 * editor where it flows through Validate → Diff → Apply → Rollback. It never
 * writes directly. The editor defaults to full sampling over TLS and warns when
 * the collector endpoint is missing or plaintext transport is selected.
 */
export function TracingEditor({ current, onClose }: TracingEditorProps) {
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState<TracingDraft>(() => seedDraft(current));

  const fragment = generateTracingToml(draft);
  const warnings = tracingWarnings(draft);

  function set<K extends keyof TracingDraft>(key: K, value: TracingDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  async function openInEditor(): Promise<void> {
    setError(null);
    try {
      const raw = await fetchRawConfig();
      setPendingDraft({
        kind: "toml",
        toml: upsertTopLevelTable(raw.raw ?? "", "observability.tracing", fragment),
      });
      void navigate("/config");
    } catch {
      setError("Could not load the current configuration to merge this tracing change.");
    }
  }

  return (
    <Drawer
      title="Edit distributed tracing"
      subtitle="Configure OpenTelemetry tracing, then review and apply it safely in the editor."
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
          Distributed tracing exports request spans to an OpenTelemetry collector over OTLP.
          Tracing is only active in binaries built with the <strong>otel</strong> build tag; a
          binary without it rejects an enabled block at startup.
        </p>

        <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-warning">
          The OpenTelemetry tracer is wired once at startup, so tracing changes cannot be
          hot-applied. Applying one reports <strong>restart required</strong>: the process must be
          restarted with the new configuration for it to take effect.
        </p>

        <Toggle
          label="Enable distributed tracing"
          checked={draft.enabled}
          onChange={(v) => {
            set("enabled", v);
          }}
        />

        {draft.enabled && (
          <>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Exporter</span>
              <select
                value={draft.exporter}
                onChange={(e) => {
                  set("exporter", e.target.value as TracingExporter);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value="otlp-grpc">OTLP gRPC (default — host:port)</option>
                <option value="otlp-http">OTLP HTTP (URL or host)</option>
              </select>
            </label>

            <TextField
              label="Collector endpoint"
              hint={
                draft.exporter === "otlp-http"
                  ? "Collector URL or host, e.g. http://collector:4318."
                  : "Collector address host:port, e.g. localhost:4317."
              }
              value={draft.endpoint}
              placeholder={draft.exporter === "otlp-http" ? "http://collector:4318" : "localhost:4317"}
              onChange={(v) => {
                set("endpoint", v);
              }}
            />

            <TextField
              label="Sample ratio (optional)"
              hint="Head-based sampling probability 0–1. Blank or 1 samples everything; 0.1 samples 10%."
              value={draft.sampleRatio === 1 ? "" : String(draft.sampleRatio)}
              placeholder="1.0"
              onChange={(v) => {
                const n = Number(v);
                set("sampleRatio", v.trim() === "" || Number.isNaN(n) ? 1 : Math.min(1, Math.max(0, n)));
              }}
            />

            <TextField
              label="Service name (optional)"
              hint="OpenTelemetry resource service.name. Blank = jul."
              value={draft.serviceName}
              placeholder="jul"
              onChange={(v) => {
                set("serviceName", v);
              }}
            />

            <div className="space-y-1">
              <Toggle
                label="Insecure transport (plaintext, no TLS)"
                checked={draft.insecure}
                onChange={(v) => {
                  set("insecure", v);
                }}
              />
              <span className="block text-xs text-jul-muted">
                Sends spans over plaintext instead of TLS. Only for a local collector on a trusted
                network, e.g. localhost:4317.
              </span>
            </div>
          </>
        )}

        {warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2">
            {warnings.map((wn, i) => (
              <p key={`tw-${String(i)}`} className="text-xs text-jul-warning">
                {wn}
              </p>
            ))}
          </div>
        )}

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
