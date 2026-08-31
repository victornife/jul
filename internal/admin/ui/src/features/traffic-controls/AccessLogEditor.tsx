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
  accessLogWarnings,
  defaultAccessLogDraft,
  generateAccessLogToml,
  type AccessLogDraft,
  type AccessLogFormat,
  type AccessLogSink,
} from "@/lib/accessLogToml.ts";

export interface AccessLogEditorProps {
  readonly current: TrafficControls;
  readonly onClose: () => void;
}

function seedDraft(current: TrafficControls): AccessLogDraft {
  const accessLog = current.access_log;
  if (!accessLog) return defaultAccessLogDraft();
  return {
    enabled: accessLog.enabled,
    sinks: (accessLog.sinks ?? ["stdout"]).filter(
      (sink): sink is AccessLogSink => sink === "stdout" || sink === "file" || sink === "syslog",
    ),
    file: accessLog.file ?? "",
    format: accessLog.format === "json" ? "json" : "text",
    rotateMaxMB: accessLog.rotate_max_mb ?? 100,
    rotateKeep: accessLog.rotate_keep ?? 3,
  };
}

function Toggle({
  label,
  checked,
  onChange,
}: {
  readonly label: string;
  readonly checked: boolean;
  readonly onChange: (checked: boolean) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-sm text-jul-text">
      <input
        type="checkbox"
        checked={checked}
        onChange={(event) => {
          onChange(event.target.checked);
        }}
        className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
      />
      {label}
    </label>
  );
}

export function AccessLogEditor({ current, onClose }: AccessLogEditorProps) {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<AccessLogDraft>(() => seedDraft(current));
  const [error, setError] = useState<string | null>(null);
  const fragment = generateAccessLogToml(draft);
  const warnings = accessLogWarnings(draft);
  const blocking = draft.enabled && draft.sinks.length === 0;

  function set<K extends keyof AccessLogDraft>(key: K, value: AccessLogDraft[K]): void {
    setDraft((currentDraft) => ({ ...currentDraft, [key]: value }));
  }

  function setSink(sink: AccessLogSink, selected: boolean): void {
    setDraft((currentDraft) => ({
      ...currentDraft,
      sinks: selected
        ? Array.from(new Set([...currentDraft.sinks, sink]))
        : currentDraft.sinks.filter((item) => item !== sink),
    }));
  }

  async function openInEditor(): Promise<void> {
    setError(null);
    if (blocking) {
      setError("Select at least one sink or disable access logging.");
      return;
    }
    try {
      const raw = await fetchRawConfig();
      setPendingDraft({
        kind: "toml",
        toml: upsertTopLevelTable(raw.raw ?? "", "observability.access_log", fragment),
      });
      void navigate("/config");
    } catch {
      setError("Could not load the current configuration to merge this access-log change.");
    }
  }

  return (
    <Drawer
      title="Edit access logging"
      subtitle="Control request access records without affecting process, security, or audit logs."
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            disabled={blocking}
            onClick={() => {
              void openInEditor();
            }}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Review in editor →
          </button>
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          Access logging records one event per request. Disabling it stops stdout, file, syslog, and
          Console access-record tail entries only. Application, reload, security, audit, health,
          metrics, and tracing remain independent.
        </p>

        <Toggle
          label="Enable request access logging"
          checked={draft.enabled}
          onChange={(checked) => {
            set("enabled", checked);
          }}
        />

        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-jul-text">Destinations</legend>
          {(["stdout", "file", "syslog"] as const).map((sink) => (
            <Toggle
              key={sink}
              label={
                sink === "stdout"
                  ? "Process stdout"
                  : sink === "file"
                    ? "Rotating file"
                    : "System log"
              }
              checked={draft.sinks.includes(sink)}
              onChange={(checked) => {
                setSink(sink, checked);
              }}
            />
          ))}
          {!draft.enabled && (
            <p className="text-xs text-jul-muted">
              These settings remain stored and validated while dormant so re-enabling is
              deterministic.
            </p>
          )}
        </fieldset>

        {draft.sinks.includes("file") && (
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">File path</span>
            <input
              type="text"
              value={draft.file}
              placeholder="/var/log/jul/access.log"
              onChange={(event) => {
                set("file", event.target.value);
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
            />
          </label>
        )}

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Format</span>
          <select
            value={draft.format}
            onChange={(event) => {
              set("format", event.target.value as AccessLogFormat);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="text">Text</option>
            <option value="json">JSON</option>
          </select>
        </label>

        {draft.sinks.includes("file") && (
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Rotate at MiB</span>
              <input
                type="number"
                min={0}
                value={draft.rotateMaxMB}
                onChange={(event) => {
                  set("rotateMaxMB", Number(event.target.value));
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
            </label>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Files to keep</span>
              <input
                type="number"
                min={0}
                value={draft.rotateKeep}
                onChange={(event) => {
                  set("rotateKeep", Number(event.target.value));
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
            </label>
          </div>
        )}

        {warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2">
            {warnings.map((warning) => (
              <p key={warning} className="text-xs text-jul-warning">
                {warning}
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
