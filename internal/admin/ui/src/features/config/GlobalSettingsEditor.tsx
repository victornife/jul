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
  type InspectionSettingsDraft,
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
  readonly configAuthority?: "managed" | "file_owned" | undefined;
  readonly baseVersion?: string | undefined;
  readonly onClose: () => void;
}

export function GlobalSettingsEditor({ current, configAuthority, baseVersion, onClose }: GlobalSettingsEditorProps) {
  const initialRef = useRef<GlobalSettingsDraft>(seedGlobalSettings(current));
  const initialVersion = useRef(baseVersion).current;
  const [draft, setDraft] = useState<GlobalSettingsDraft>(initialRef.current);
  const { has } = usePermission();
  const canWrite = has("config:write");
  const canEditInspection = canWrite && has("config:raw") && configAuthority === "managed" &&
    initialVersion !== undefined && initialVersion.trim() !== "";
  const batch = useRunPatchBatch();
  const slots = draft.inspection?.maxConcurrentEvaluations;
  const slotsValid = slots === undefined || (/^\d+$/.test(slots) && Number.isSafeInteger(Number(slots)));
  const before = initialRef.current.inspection;
  const after = draft.inspection;
  const queueTimeoutValid = after === undefined || before === undefined ||
    after.evaluationQueueTimeout === before.evaluationQueueTimeout || after.evaluationQueueTimeout.trim() !== "";
  const bodyTimeoutValid = after === undefined || before === undefined ||
    after.bodyTimeout === before.bodyTimeout || after.bodyTimeout.trim() !== "";
  const operation = slotsValid && queueTimeoutValid && bodyTimeoutValid
    ? buildGlobalPatch(initialRef.current, draft) : null;
  const workerThreadsValid = draft.workerThreads === "auto" || /^[1-9]\d*$/.test(draft.workerThreads);
  const canReview = canWrite && workerThreadsValid && operation !== null && !batch.busy &&
    (operation.op !== "global_set" || operation.global.inspection === undefined || canEditInspection);
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

  const updateInspection = <K extends keyof InspectionSettingsDraft>(
    key: K,
    value: InspectionSettingsDraft[K],
  ): void => {
    setDraft((previous) => previous.inspection === undefined
      ? previous
      : { ...previous, inspection: { ...previous.inspection, [key]: value } });
    batch.clearError();
  };

  return (
    <Drawer
      title="Edit global settings"
      subtitle="Only changed fields are sent in one sparse global_set operation."
      onClose={onClose}
    >
      <div className="space-y-5 p-4">
        <fieldset disabled={batch.busy} className="space-y-5">
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

        <section className="space-y-3 border-t border-jul-border pt-4" aria-labelledby="inspection-settings-heading">
          <h2 id="inspection-settings-heading" className="text-sm font-semibold">Inspection resources</h2>
          {draft.inspection === undefined ? (
            <p className="text-xs text-jul-muted">Inspection settings are unavailable on this server.</p>
          ) : (
            <>
              {!canEditInspection && (
                <p className="text-xs text-jul-muted">
                  {configAuthority === "file_owned"
                    ? "File-owned configuration is read-only; edit the owning file or pipeline."
                    : configAuthority === undefined
                      ? "Configuration authority is unavailable; inspection settings are read-only."
                      : !canWrite || !has("config:raw")
                        ? "Editing requires config:write and config:raw."
                        : "The editable configuration version is unavailable; inspection settings are read-only."}
                </p>
              )}
              <fieldset disabled={!canEditInspection} className="space-y-4 disabled:opacity-70">
                <FieldShell label="Inspection mode" metadata={current.lifecycle["inspection.mode"]}
                  error={fieldError("global.inspection.mode")}>
                  <select className={inputClass} value={draft.inspection.mode}
                    onChange={(event) => {
                      const value = event.target.value;
                      if (value === "enforce" || value === "observe") updateInspection("mode", value);
                    }}>
                    <option value="enforce">enforce</option>
                    <option value="observe">observe</option>
                  </select>
                </FieldShell>
                <FieldShell label="Inspection byte budget" metadata={current.lifecycle["inspection.max_buffered_bytes"]}
                  error={fieldError("global.inspection.max_buffered_bytes")}
                  hint="Shared logical inspection charges, not a process-memory limit.">
                  <input className={inputClass} value={draft.inspection.maxBufferedBytes}
                    placeholder="Unavailable" onChange={(event) => { updateInspection("maxBufferedBytes", event.target.value); }} />
                </FieldShell>
                <FieldShell label="Guest capacity budget" metadata={current.lifecycle["inspection.max_guest_capacity_bytes"]}
                  error={fieldError("global.inspection.max_guest_capacity_bytes")}
                  hint="Positive logical reservation ceiling, not a process-memory limit.">
                  <input className={inputClass} value={draft.inspection.maxGuestCapacityBytes ?? ""}
                    disabled={draft.inspection.maxGuestCapacityBytes === undefined}
                    placeholder="Unavailable" onChange={(event) => { updateInspection("maxGuestCapacityBytes", event.target.value); }} />
                </FieldShell>
                <FieldShell label="Inspection response-header budget" metadata={current.lifecycle["inspection.max_response_header_bytes"]}
                  error={fieldError("global.inspection.max_response_header_bytes")}>
                  <input className={inputClass} value={draft.inspection.maxResponseHeaderBytes}
                    placeholder="Unavailable" onChange={(event) => { updateInspection("maxResponseHeaderBytes", event.target.value); }} />
                </FieldShell>
                <FieldShell label="Inspection evaluation slots" metadata={current.lifecycle["inspection.max_concurrent_evaluations"]}
                  error={fieldError("global.inspection.max_concurrent_evaluations")}
                  hint="Zero selects the server's automatic slot count.">
                  <input className={inputClass} inputMode="numeric" value={draft.inspection.maxConcurrentEvaluations}
                    onChange={(event) => { updateInspection("maxConcurrentEvaluations", event.target.value); }} />
                  {!slotsValid && <span className="block text-xs text-jul-danger">Enter a non-negative integer.</span>}
                </FieldShell>
                <FieldShell label="Inspection evaluation queue timeout" metadata={current.lifecycle["inspection.evaluation_queue_timeout"]}
                  error={fieldError("global.inspection.evaluation_queue_timeout")}
                  hint="An explicit 0s disables queueing.">
                  <input className={inputClass} value={draft.inspection.evaluationQueueTimeout}
                    placeholder="Unavailable" onChange={(event) => { updateInspection("evaluationQueueTimeout", event.target.value); }} />
                  {!queueTimeoutValid && <span className="block text-xs text-jul-danger">Enter a duration; use 0s to disable queueing.</span>}
                </FieldShell>
                <FieldShell label="Inspection body timeout" metadata={current.lifecycle["inspection.body_timeout"]}
                  error={fieldError("global.inspection.body_timeout")}
                  hint="An explicit 0s disables this deadline.">
                  <input className={inputClass} value={draft.inspection.bodyTimeout}
                    placeholder="Unavailable" onChange={(event) => { updateInspection("bodyTimeout", event.target.value); }} />
                  {!bodyTimeoutValid && <span className="block text-xs text-jul-danger">Enter a duration; use 0s to disable the body deadline.</span>}
                </FieldShell>
              </fieldset>
            </>
          )}
        </section>
        </fieldset>

        {batch.error !== null && (
          <div role="alert" className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-xs text-jul-danger">
            {describePatchBatchError(batch.error)}
            {batch.error instanceof ConfigRejectedError && batch.error.issues.length > 0 && (
              <ul className="mt-1 list-inside list-disc">
                {batch.error.issues.map((issue, index) => (
                  <li key={`${issue.code}-${String(index)}`}>{issue.summary}{issue.detail ? ` — ${issue.detail}` : ""}</li>
                ))}
              </ul>
            )}
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
              if (operation !== null) {
                const inspectionChanged = operation.op === "global_set" && operation.global.inspection !== undefined;
                void batch.run([operation], inspectionChanged ? initialVersion : undefined);
              }
            }}
          >
            {batch.busy ? "Reviewing…" : "Review changes"}
          </button>
        </div>
      </div>
    </Drawer>
  );
}
