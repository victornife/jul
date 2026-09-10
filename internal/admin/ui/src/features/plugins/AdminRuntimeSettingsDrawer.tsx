/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  fetchAdminRuntimeSettings,
  type ConfigPatch,
  type LifecycleFieldProjection,
} from "@/api/client.ts";
import { PanelError } from "@/components/PanelError.tsx";
import { Loading } from "@/components/ui.tsx";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";

interface Props {
  onClose: () => void;
}

function LifecycleBadge({ field }: { field: LifecycleFieldProjection | undefined }) {
  if (!field) return null;
  const hot = field.class === "hot_reload";
  return (
    <span
      title={field.reason}
      className={`rounded border px-1.5 py-0.5 text-[10px] font-medium ${
        hot
          ? "border-jul-success/40 bg-jul-success/10 text-jul-success"
          : "border-jul-warning/40 bg-jul-warning/10 text-jul-warning"
      }`}
    >
      {hot ? "Hot reload" : field.class.replaceAll("_", " ")}
    </span>
  );
}

function SettingLabel({
  title,
  field,
}: {
  title: string;
  field: LifecycleFieldProjection | undefined;
}) {
  return (
    <div className="mb-1.5 flex items-center gap-2">
      <span className="text-sm font-medium text-jul-text">{title}</span>
      <LifecycleBadge field={field} />
    </div>
  );
}

function integerValue(value: string): number | null {
  const parsed = Number(value);
  return Number.isInteger(parsed) ? parsed : null;
}

export function AdminRuntimeSettingsDrawer({ onClose }: Props) {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["admin-runtime-settings"],
    queryFn: fetchAdminRuntimeSettings,
  });
  const runner = useRunPatchBatch();

  const [consoleEnabled, setConsoleEnabled] = useState(false);
  const [uploadEnabled, setUploadEnabled] = useState(false);
  const [maxSize, setMaxSize] = useState("0");
  const [directory, setDirectory] = useState("");
  const [readPerMin, setReadPerMin] = useState("240");
  const [writePerMin, setWritePerMin] = useState("60");
  const [applyPerMin, setApplyPerMin] = useState("30");
  const [maxEventConns, setMaxEventConns] = useState("4");
  const [confirmDisable, setConfirmDisable] = useState(false);

  useEffect(() => {
    if (!data) return;
    setConsoleEnabled(data.console);
    setUploadEnabled(data.plugin_upload_enabled);
    setMaxSize(String(data.plugin_upload_max_size_mb));
    setDirectory(data.plugin_upload_dir);
    setReadPerMin(String(data.rate_limit_read_per_min));
    setWritePerMin(String(data.rate_limit_write_per_min));
    setApplyPerMin(String(data.rate_limit_apply_per_min));
    setMaxEventConns(String(data.max_event_conns));
    setConfirmDisable(false);
  }, [data]);

  const consoleDisabling = Boolean(data?.console && !consoleEnabled);
  const directoryChanging = Boolean(data && directory.trim() !== data.plugin_upload_dir.trim());
  const uploadDisabling = Boolean(data?.plugin_upload_enabled && !uploadEnabled);
  const parsedMax = Number(maxSize);
  const maxValid = Number.isInteger(parsedMax) && parsedMax >= 0;
  const parsedRead = integerValue(readPerMin);
  const parsedWrite = integerValue(writePerMin);
  const parsedApply = integerValue(applyPerMin);
  const parsedConns = integerValue(maxEventConns);
  const limitsValid = parsedRead !== null && parsedWrite !== null && parsedApply !== null && parsedConns !== null && parsedConns > 0;
  const loweringSSE = Boolean(data && parsedConns !== null && parsedConns > 0 && parsedConns < data.max_event_conns);

  const ops = useMemo<ConfigPatch[]>(() => {
    if (!data || !maxValid || !limitsValid || parsedRead === null || parsedWrite === null || parsedApply === null || parsedConns === null) return [];
    const next: ConfigPatch[] = [];
    if (consoleEnabled !== data.console) {
      next.push({ op: "admin_console_set", enabled: consoleEnabled });
    }
    const upload: { enabled?: boolean; max_size_mb?: number; directory?: string } = {};
    if (uploadEnabled !== data.plugin_upload_enabled) upload.enabled = uploadEnabled;
    if (parsedMax !== data.plugin_upload_max_size_mb) upload.max_size_mb = parsedMax;
    if (directory.trim() !== data.plugin_upload_dir.trim()) upload.directory = directory.trim();
    if (Object.keys(upload).length > 0) {
      next.push({ op: "admin_plugin_upload_set", plugin_upload: upload });
    }
    const limits: { read_per_min?: number; write_per_min?: number; apply_per_min?: number; max_event_conns?: number } = {};
    if (parsedRead !== data.rate_limit_read_per_min) limits.read_per_min = parsedRead;
    if (parsedWrite !== data.rate_limit_write_per_min) limits.write_per_min = parsedWrite;
    if (parsedApply !== data.rate_limit_apply_per_min) limits.apply_per_min = parsedApply;
    if (parsedConns !== data.max_event_conns) limits.max_event_conns = parsedConns;
    if (Object.keys(limits).length > 0) {
      next.push({ op: "admin_limits_set", admin_limits: limits });
    }
    return next;
  }, [applyPerMin, consoleEnabled, data, directory, limitsValid, maxEventConns, maxValid, parsedApply, parsedConns, parsedMax, parsedRead, parsedWrite, readPerMin, uploadEnabled, writePerMin]);

  if (isLoading) {
    return (
      <div className="fixed inset-0 z-50 bg-black/40">
        <div className="ml-auto h-full w-full max-w-xl bg-jul-bg p-6 shadow-xl">
          <Loading label="Loading admin runtime settings…" />
        </div>
      </div>
    );
  }
  if (isError || !data) {
    return (
      <div className="fixed inset-0 z-50 bg-black/40">
        <div className="ml-auto h-full w-full max-w-xl bg-jul-bg p-6 shadow-xl">
          <PanelError error={error} resource="admin runtime settings" onRetry={() => void refetch()} />
          <button type="button" className="mt-4 text-sm text-jul-muted underline" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    );
  }

  const previewError = describePatchBatchError(runner.error);
  const saveDisabled =
    runner.busy || ops.length === 0 || !maxValid || !limitsValid || directory.trim() === "" || (consoleDisabling && !confirmDisable);

  return (
    <div className="fixed inset-0 z-50 bg-black/40" role="dialog" aria-modal="true" aria-label="Admin runtime settings">
      <div className="ml-auto flex h-full w-full max-w-xl flex-col overflow-y-auto border-l border-jul-border bg-jul-bg shadow-xl">
        <div className="flex items-start justify-between border-b border-jul-border p-5">
          <div>
            <h2 className="text-lg font-semibold text-jul-text">Admin runtime settings</h2>
            <p className="mt-1 text-xs text-jul-muted">
              Console, upload and admission policy publish as one request-generation snapshot.
            </p>
          </div>
          <button type="button" onClick={onClose} className="text-sm text-jul-muted hover:text-jul-text">
            Close
          </button>
        </div>

        <div className="space-y-6 p-5">
          <section className="rounded-lg border border-jul-border bg-jul-surface p-4">
            <SettingLabel title="Web Console" field={data.lifecycle.console} />
            <label className="flex items-center gap-3 text-sm text-jul-text">
              <input type="checkbox" checked={consoleEnabled} onChange={(event) => { setConsoleEnabled(event.target.checked); setConfirmDisable(false); }} />
              Serve the embedded Console on the existing admin listener
            </label>
            <p className="mt-2 text-xs text-jul-muted">
              Build: {data.console_compiled ? "Console assets compiled" : "Console assets not compiled"}. Effective now: {data.console_effective ? "enabled" : "disabled"}.
            </p>
            {consoleDisabling && (
              <div className="mt-3 rounded-md border border-jul-warning/50 bg-jul-warning/10 p-3 text-xs text-jul-text">
                <strong>Disabling the Console removes this web UI after the apply is terminal.</strong>{" "}
                The authenticated admin API remains available on the same listener. Re-enable it by setting
                <code className="mx-1">[admin] console = true</code> in the configuration and reloading, or by using an authenticated config apply/patch API request.
                <label className="mt-3 flex items-start gap-2">
                  <input type="checkbox" checked={confirmDisable} onChange={(event) => { setConfirmDisable(event.target.checked); }} />
                  <span>I understand how to re-enable the Console.</span>
                </label>
              </div>
            )}
          </section>

          <section className="rounded-lg border border-jul-border bg-jul-surface p-4">
            <div className="mb-3 flex items-center justify-between gap-2">
              <span className="text-sm font-medium text-jul-text">Admin request admission</span>
              <LifecycleBadge field={data.lifecycle.rate_limit_read_per_min} />
            </div>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              {[
                ["Read / min", readPerMin, setReadPerMin],
                ["Write / min", writePerMin, setWritePerMin],
                ["Apply / min", applyPerMin, setApplyPerMin],
              ].map(([label, value, setter]) => (
                <label key={label as string} className="text-xs text-jul-muted">
                  {label as string}
                  <input
                    type="number"
                    step={1}
                    value={value as string}
                    onChange={(event) => (setter as (value: string) => void)(event.target.value)}
                    className="mt-1 w-full rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 text-sm text-jul-text"
                  />
                </label>
              ))}
            </div>
            <p className="mt-2 text-xs text-jul-muted">
              Positive values set an explicit limit. Zero means the canonical default. A negative request-rate value disables that class. Reload preserves accumulated client state; tighter limits govern the first new admission after Publish without resetting quotas.
            </p>
          </section>

          <section className="rounded-lg border border-jul-border bg-jul-surface p-4">
            <SettingLabel title="Concurrent event/log streams per client" field={data.lifecycle.max_event_conns} />
            <input
              type="number"
              min={1}
              step={1}
              value={maxEventConns}
              onChange={(event) => { setMaxEventConns(event.target.value); }}
              className="w-32 rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 text-sm text-jul-text"
            />
            {parsedConns !== null && parsedConns <= 0 && <p className="mt-1 text-xs text-jul-danger">Use a positive whole number. This setting has no unlimited mode.</p>}
            <p className="mt-2 text-xs text-jul-muted">The cap is shared by event and live-log SSE streams for each transport peer.</p>
            {loweringSSE && (
              <p className="mt-3 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2 text-xs text-jul-text">
                Existing event/log streams remain connected. The new per-client cap applies to new connections; clients already above the cap cannot open another stream until their active count falls below the configured limit.
              </p>
            )}
          </section>

          <section className="rounded-lg border border-jul-border bg-jul-surface p-4">
            <SettingLabel title="Plugin uploads" field={data.lifecycle.plugin_upload_enabled} />
            <label className="flex items-center gap-3 text-sm text-jul-text">
              <input type="checkbox" checked={uploadEnabled} onChange={(event) => { setUploadEnabled(event.target.checked); }} />
              Accept authenticated WASM uploads
            </label>
            <p className="mt-2 text-xs text-jul-muted">
              Effective now: {data.plugin_upload_effective ? "enabled" : "disabled"}. Directory health: {data.upload_directory_health}.
            </p>
            {uploadDisabling && (
              <p className="mt-3 rounded-md border border-jul-border p-2 text-xs text-jul-muted">
                Disabling uploads blocks new request bodies before multipart parsing. Existing uploaded files are retained; Jul does not delete them.
              </p>
            )}
          </section>

          <section className="rounded-lg border border-jul-border bg-jul-surface p-4">
            <SettingLabel title="Maximum upload size" field={data.lifecycle.plugin_upload_max_size} />
            <div className="flex items-center gap-2">
              <input type="number" min={0} step={1} value={maxSize} onChange={(event) => { setMaxSize(event.target.value); }} className="w-32 rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 text-sm text-jul-text" />
              <span className="text-sm text-jul-muted">MB</span>
            </div>
            {!maxValid && <p className="mt-1 text-xs text-jul-danger">Use a non-negative whole number.</p>}
          </section>

          <section className="rounded-lg border border-jul-border bg-jul-surface p-4">
            <SettingLabel title="Upload directory" field={data.lifecycle.plugin_upload_dir} />
            <input type="text" value={directory} onChange={(event) => { setDirectory(event.target.value); }} className="w-full rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 font-mono text-sm text-jul-text" />
            {directoryChanging && (
              <p className="mt-3 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2 text-xs text-jul-text">
                Jul validates the candidate directory before Publish, but does not copy, migrate, or delete files between directories. In-flight uploads finish against the directory generation they captured.
              </p>
            )}
          </section>

          <p className="text-xs text-jul-muted">
            Save opens the authoritative server-side lifecycle preview. No setting is persisted until you review and apply that preview; mixed restart-bound admin changes are never partially published.
          </p>
          {previewError && <p className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-2 text-xs text-jul-danger">{previewError}</p>}
        </div>

        <div className="mt-auto flex justify-end gap-2 border-t border-jul-border p-5">
          <button type="button" onClick={onClose} className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text">Cancel</button>
          <button type="button" disabled={saveDisabled} onClick={() => void runner.run(ops)} className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg disabled:opacity-50">
            {runner.busy ? "Preparing preview…" : "Review changes"}
          </button>
        </div>
      </div>
    </div>
  );
}
