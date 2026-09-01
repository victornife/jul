/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useRef, useState, type ReactNode } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import {
  ConfigRejectedError,
  type GlobalSettingsProjection,
  type LifecycleFieldProjection,
} from "@/api/client.ts";
import {
  buildGlobalPatch,
  seedGlobalSettings,
  type GlobalSettingsDraft,
} from "@/lib/trafficPatchBuilders.ts";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";

function LifecycleBadge({ metadata }: { readonly metadata: LifecycleFieldProjection | undefined }) {
  if (metadata === undefined) return null;
  const restart = metadata.class === "restart_required" || metadata.class === "new_listener_only";
  return (
    <span
      title={`${metadata.subsystem}: ${metadata.reason}`}
      className={`rounded-full px-2 py-0.5 text-[0.68rem] font-medium ${
        restart
          ? "bg-jul-warning/15 text-jul-warning"
          : "bg-jul-success/15 text-jul-success"
      }`}
    >
      {metadata.class.replaceAll("_", " ")}
    </span>
  );
}

function FieldShell({
  label,
  metadata,
  hint,
  error,
  children,
}: {
  readonly label: string;
  readonly metadata: LifecycleFieldProjection | undefined;
  readonly hint?: string | undefined;
  readonly error?: string | undefined;
  readonly children: ReactNode;
}) {
  return (
    <label className="block space-y-1">
      <span className="flex flex-wrap items-center gap-2 text-sm font-medium text-jul-text">
        {label}
        <LifecycleBadge metadata={metadata} />
      </span>
      {children}
      {hint && <span className="block text-xs text-jul-muted">{hint}</span>}
      {error && <span className="block text-xs text-jul-danger">{error}</span>}
    </label>
  );
}

const inputClass =
  "w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent";

export interface GlobalSettingsEditorProps {
  readonly current: GlobalSettingsProjection;
  readonly onClose: () => void;
}

export function GlobalSettingsEditor({ current, onClose }: GlobalSettingsEditorProps) {
  const initialRef = useRef<GlobalSettingsDraft>(seedGlobalSettings(current));
  const [draft, setDraft] = useState<GlobalSettingsDraft>(initialRef.current);
  const { has } = usePermission();
  const canWrite = has("config:write");
  const batch = useRunPatchBatch();
  const operation = buildGlobalPatch(initialRef.current, draft);
  const workerThreadsValid = draft.workerThreads === "auto" || /^[1-9]\d*$/.test(draft.workerThreads);
  const canReview = canWrite && workerThreadsValid && operation !== null && !batch.busy;
  const fieldError = (...paths: string[]): string | undefined => {
    if (!(batch.error instanceof ConfigRejectedError)) return undefined;
    const issue = batch.error.issues.find((candidate) =>
      paths.some((path) => candidate.path === path || candidate.path?.endsWith(`.${path}`)),
    );
    return issue === undefined
      ? undefined
      : `${issue.summary}${issue.detail ? ` — ${issue.detail}` : ""}`;
  };

  const update = <K extends keyof GlobalSettingsDraft>(
    key: K,
    value: GlobalSettingsDraft[K],
  ): void => {
    setDraft((previous) => ({ ...previous, [key]: value }));
    batch.clearError();
  };

  return (
    <Drawer
      title="Edit global settings"
      subtitle="Only changed fields are sent in one sparse global_set operation."
      onClose={onClose}
    >
      <div className="space-y-5 p-4">
        <FieldShell
          label="Worker threads"
          metadata={current.lifecycle.worker_threads}
          error={fieldError("global.worker_threads", "worker_threads")}
          hint="Use auto or a positive integer. The backend wire value stays a string."
        >
          <input
            className={inputClass}
            value={draft.workerThreads}
            onChange={(event) => {
              update("workerThreads", event.target.value);
            }}
          />
          {!workerThreadsValid && (
            <span className="block text-xs text-jul-danger">Enter auto or a positive integer.</span>
          )}
        </FieldShell>

        <FieldShell
          label="Log level"
          metadata={current.lifecycle.log_level}
          error={fieldError("global.log_level", "log_level")}
        >
          <select
            className={inputClass}
            value={draft.logLevel}
            onChange={(event) => {
              update("logLevel", event.target.value as GlobalSettingsDraft["logLevel"]);
            }}
          >
            <option value="debug">debug</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </select>
        </FieldShell>

        <FieldShell
          label="Log format"
          metadata={current.lifecycle.log_format}
          error={fieldError("global.log_format", "log_format")}
        >
          <select
            className={inputClass}
            value={draft.logFormat}
            onChange={(event) => {
              update("logFormat", event.target.value as GlobalSettingsDraft["logFormat"]);
            }}
          >
            <option value="text">text</option>
            <option value="json">json</option>
          </select>
        </FieldShell>

        <FieldShell
          label="Shutdown timeout"
          metadata={current.lifecycle.shutdown_timeout}
          error={fieldError("global.shutdown_timeout", "shutdown_timeout")}
        >
          <input
            className={inputClass}
            value={draft.shutdownTimeout}
            onChange={(event) => {
              update("shutdownTimeout", event.target.value);
            }}
          />
        </FieldShell>

        <FieldShell
          label="Reload timeout"
          metadata={current.lifecycle.reload_timeout}
          error={fieldError("global.reload_timeout", "reload_timeout")}
          hint="The transaction that changes this field still uses the currently active reload timeout. The new value governs later transactions."
        >
          <input
            className={inputClass}
            value={draft.reloadTimeout}
            onChange={(event) => {
              update("reloadTimeout", event.target.value);
            }}
          />
        </FieldShell>

        <FieldShell
          label="Minimum secret redaction length"
          metadata={current.lifecycle.redact_min_secret_length}
          error={fieldError("global.redact_min_secret_length", "redact_min_secret_length")}
        >
          <input
            type="number"
            min={0}
            className={inputClass}
            value={draft.redactMinSecretLength}
            onChange={(event) => {
              update("redactMinSecretLength", Number(event.target.value));
            }}
          />
        </FieldShell>

        {batch.error !== null && (
          <div role="alert" className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-xs text-jul-danger">
            {describePatchBatchError(batch.error)}
          </div>
        )}

        <div className="flex items-center justify-end gap-3 border-t border-jul-border pt-4">
          <ForbiddenAction permission="config:write" />
          <button
            type="button"
            className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text hover:bg-jul-bg"
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            type="button"
            disabled={!canReview}
            className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg disabled:opacity-40"
            onClick={() => {
              if (operation !== null) void batch.run([operation]);
            }}
          >
            {batch.busy ? "Reviewing…" : "Review changes"}
          </button>
        </div>
      </div>
    </Drawer>
  );
}
