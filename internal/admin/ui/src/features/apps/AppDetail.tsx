/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import type { AppProjection, BackendProjection, BackendState } from "@/api/client.ts";
import { usePermission } from "@/auth/usePermission.ts";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";
import { Drawer } from "@/components/Drawer.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { DiscoveryEditor, HealthCheckEditor } from "@/features/apps/AppSettingsEditor.tsx";
import { AppPatchValidationError, buildAppRemovalBatch } from "@/lib/appPatch.ts";
import type { PendingPatchDraft } from "@/lib/configDraftHandoff.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";

const STRATEGIES: ReadonlyArray<{ readonly value: string; readonly label: string }> = [
  { value: "round_robin", label: "Round robin" },
  { value: "weighted_round_robin", label: "Weighted round robin" },
  { value: "least_conn", label: "Least connections" },
];

function Row({ label, value }: { readonly label: string; readonly value: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[150px_1fr] gap-2 py-1.5">
      <span className="text-xs uppercase tracking-wider text-jul-muted">{label}</span>
      <span className="text-sm text-jul-text">{value}</span>
    </div>
  );
}

// Every state is named rather than merged into a colour: "its circuit is open"
// and "the health checker ejected it" call for opposite operator responses, and
// a backend at its concurrency limit is not unhealthy at all.
const BACKEND_STATE_LABEL: Record<BackendState, string> = {
  available: "available",
  circuit_open: "circuit open — recent failures took it out of rotation",
  circuit_half_open: "circuit half-open — being probed for recovery",
  health_unhealthy: "unhealthy — active health checks are failing",
  at_capacity: "at capacity — max_active_per_backend reached",
};

const BACKEND_STATE_COLOUR: Record<BackendState, string> = {
  available: "bg-jul-success",
  circuit_open: "bg-jul-danger",
  circuit_half_open: "bg-jul-warning",
  health_unhealthy: "bg-jul-danger",
  at_capacity: "bg-jul-warning",
};

function HealthState({ state }: { readonly state: BackendState | undefined }) {
  const label = state === undefined ? "state unknown — backend is not live" : BACKEND_STATE_LABEL[state];
  return (
    <>
      <span
        title={label}
        className={`inline-block h-2 w-2 rounded-full ${
          state === undefined ? "bg-jul-muted/50" : BACKEND_STATE_COLOUR[state]
        }`}
      />
      <span className="sr-only">{label}</span>
    </>
  );
}

function BackendRow({
  backend,
  canRemove,
  showRemove,
  canWrite,
  busy,
  onRemove,
}: {
  readonly backend: BackendProjection;
  readonly canRemove: boolean;
  readonly showRemove: boolean;
  readonly canWrite: boolean;
  readonly busy: boolean;
  readonly onRemove: () => void;
}) {
  const removeTitle = !canWrite
    ? "Requires config:write"
    : canRemove
      ? "Remove this backend"
      : "Cannot remove the last backend";
  return (
    <tr className="border-b border-jul-border last:border-b-0">
      <td className="px-3 py-2">
        <div className="flex items-center gap-2">
          <HealthState state={backend.state} />
          <span className="font-mono text-sm text-jul-text">{backend.address}</span>
        </div>
      </td>
      <td className="px-3 py-2 text-sm text-jul-muted">{backend.weight}</td>
      <td className="px-3 py-2 text-sm text-jul-muted">{backend.inflight ?? "—"}</td>
      {showRemove && (
        <td className="px-3 py-2 text-right">
          <button
            type="button"
            disabled={busy || !canWrite || !canRemove}
            title={removeTitle}
            onClick={onRemove}
            className="rounded-md border border-jul-border px-2 py-0.5 text-xs text-jul-danger hover:bg-jul-danger/10 disabled:opacity-40"
          >
            Remove →
          </button>
        </td>
      )}
    </tr>
  );
}

function lifecycleOutcome(draft: PendingPatchDraft): string {
  const lifecycle = draft.lifecycle;
  if (lifecycle === undefined) return "Lifecycle classification is unavailable.";
  if (lifecycle.validation_rejected_paths.length > 0) {
    return "Lifecycle validation rejected this operation.";
  }
  if (lifecycle.can_apply_hot) return "Hot apply is available after review.";
  if (lifecycle.can_stage_restart) return "This deletion must be staged for restart.";
  return "The preview currently offers neither hot apply nor restart staging.";
}

function previewCanBeHandedOff(draft: PendingPatchDraft): boolean {
  return (
    draft.valid &&
    draft.lifecycle !== undefined &&
    draft.lifecycle.validation_rejected_paths.length === 0
  );
}

function AppDeletionConfirmation({
  app,
  draft,
  onConfirm,
  onCancel,
}: {
  readonly app: AppProjection;
  readonly draft: PendingPatchDraft;
  readonly onConfirm: () => void;
  readonly onCancel: () => void;
}) {
  const lifecycle = draft.lifecycle;
  return (
    <ConfirmDialog
      title={`Remove App/upstream ${app.name}?`}
      confirmLabel="Hand off deletion for apply review"
      danger
      confirmDisabled={!previewCanBeHandedOff(draft)}
      onConfirm={onConfirm}
      onCancel={onCancel}
    >
      <div className="space-y-4">
        <p>
          This second confirmation does not apply configuration directly. It hands the exact
          previewed one-operation batch and base version to Configuration for final apply or restart
          staging.
        </p>
        <dl className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3 text-xs">
          <div className="grid grid-cols-[130px_1fr] gap-2">
            <dt className="font-semibold text-jul-muted">App/upstream</dt>
            <dd className="font-mono text-jul-text">{app.name}</dd>
          </div>
          <div className="grid grid-cols-[130px_1fr] gap-2">
            <dt className="font-semibold text-jul-muted">References</dt>
            <dd className="text-jul-text">0 projected routes</dd>
          </div>
          <div className="grid grid-cols-[130px_1fr] gap-2">
            <dt className="font-semibold text-jul-muted">Lifecycle</dt>
            <dd className="text-jul-text">{lifecycleOutcome(draft)}</dd>
          </div>
        </dl>
        <div>
          <p className="mb-1 text-xs font-semibold text-jul-muted">Exact operation</p>
          <pre className="max-h-40 overflow-auto rounded-md border border-jul-border bg-jul-bg p-3 font-mono text-xs text-jul-text">
            {JSON.stringify(draft.ops, null, 2)}
          </pre>
        </div>
        {draft.operationSummaries.length > 0 && (
          <ol className="list-decimal space-y-1 pl-5 text-xs text-jul-text">
            {draft.operationSummaries.map((operation) => (
              <li key={`${String(operation.op_index)}-${operation.op}`}>
                <span className="font-mono">{operation.op}</span>: {operation.summary}
              </li>
            ))}
          </ol>
        )}
        {draft.validationErrors.length > 0 && (
          <div className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3">
            <p className="text-xs font-semibold text-jul-danger">Validation issues</p>
            <ul className="mt-1 list-disc space-y-1 pl-5 text-xs text-jul-danger">
              {draft.validationErrors.map((issue, index) => (
                <li key={`${issue.code}-${String(index)}`}>
                  {issue.path ? `${issue.path}: ` : ""}
                  {issue.summary}
                </li>
              ))}
            </ul>
          </div>
        )}
        {lifecycle !== undefined && (
          <div className="grid gap-1 text-xs text-jul-muted">
            <p>Hot paths: {lifecycle.hot_paths.length}</p>
            <p>Restart-required paths: {lifecycle.restart_required_paths.length}</p>
            <p>Pending subsystems: {lifecycle.pending_subsystems.join(", ") || "none"}</p>
          </div>
        )}
        <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
          Deletion removes only this App/upstream. It never cascades to routes, servers,
          credentials, plugins, discovery-provider resources, or unrelated objects, and it never
          cleans up an external discovery provider. The backend re-checks references during preview;
          a race is rejected and remains visible in this drawer.
        </p>
        {!previewCanBeHandedOff(draft) && (
          <p className="text-xs text-jul-danger">
            Handoff is disabled because the exact preview is invalid, lifecycle-rejected, or lacks
            an authoritative lifecycle classification.
          </p>
        )}
      </div>
    </ConfirmDialog>
  );
}

function AppDangerZone({ app }: { readonly app: AppProjection }) {
  const { has } = usePermission();
  const canWrite = has("config:write");
  const batch = useRunPatchBatch();
  const [preview, setPreview] = useState<PendingPatchDraft | null>(null);
  const [localError, setLocalError] = useState<string | null>(null);
  const references = app.routes_using ?? [];
  const blocked = references.length > 0;
  const shownReferences = references.slice(0, 8);
  const remainingReferences = references.length - shownReferences.length;
  const error = localError ?? describePatchBatchError(batch.error);
  const referenceKey = references.join("\n");

  useEffect(() => {
    setPreview(null);
  }, [app.name, referenceKey]);

  async function previewDeletion(): Promise<void> {
    if (!canWrite || blocked) return;
    setLocalError(null);
    setPreview(null);
    batch.clearError();
    try {
      const ops = buildAppRemovalBatch(app.name, references);
      const draft = await batch.preview(ops);
      if (draft !== null) setPreview(draft);
    } catch (caught) {
      setLocalError(
        caught instanceof AppPatchValidationError
          ? caught.message
          : "The deletion batch could not be built safely.",
      );
    }
  }

  return (
    <section className="space-y-3 rounded-md border border-jul-danger/40 bg-jul-danger/5 p-3">
      <div>
        <span className="text-xs font-semibold uppercase tracking-wider text-jul-danger">
          Danger zone
        </span>
        <p className="mt-1 text-xs text-jul-muted">
          App deletion is reference-aware, no-cascade, validated by backend preview, and requires a
          second confirmation before handoff.
        </p>
      </div>

      {blocked && (
        <div className="space-y-2 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
          <p className="text-xs text-jul-text">
            Delete is blocked because {references.length} projected{" "}
            {references.length === 1 ? "route still references" : "routes still reference"} this
            App. Repoint or remove them first.
          </p>
          <ul className="max-h-36 list-disc space-y-1 overflow-auto pl-5 font-mono text-xs text-jul-text">
            {shownReferences.map((reference, index) => (
              <li key={`${reference}-${String(index)}`}>{reference}</li>
            ))}
            {remainingReferences > 0 && <li>…and {remainingReferences} more</li>}
          </ul>
          <Link
            to="/routes"
            className="text-xs font-medium text-jul-accent underline hover:no-underline"
          >
            Open Routes to repoint dependencies →
          </Link>
        </div>
      )}

      <button
        type="button"
        disabled={batch.busy || !canWrite || blocked}
        onClick={() => {
          void previewDeletion();
        }}
        className="rounded-md border border-jul-danger/60 px-3 py-1.5 text-xs font-medium text-jul-danger hover:bg-jul-danger/10 disabled:opacity-40"
      >
        {batch.busy ? "Previewing App deletion…" : "Delete App / upstream…"}
      </button>
      {error && <p className="text-xs text-jul-danger">{error}</p>}
      <ForbiddenAction permission="config:write" />

      {preview !== null && (
        <AppDeletionConfirmation
          app={app}
          draft={preview}
          onConfirm={() => {
            batch.handoff(preview);
          }}
          onCancel={() => {
            setPreview(null);
          }}
        />
      )}
    </section>
  );
}

export interface AppDetailProps {
  readonly app: AppProjection;
  readonly onClose: () => void;
}

/** App/upstream detail with all one-op edits routed through the shared patch hook. */
export function AppDetail({ app, onClose }: AppDetailProps) {
  const { has } = usePermission();
  const canWrite = has("config:write");
  const patch = useRunPatch();
  const total = app.backends.length;
  const available = app.backends.filter((backend) => backend.state === "available").length;
  const known = app.backends.filter((backend) => backend.state !== undefined).length;
  const backendsValue =
    total === 0
      ? "none"
      : known === 0
        ? `${String(total)} backends · state unknown`
        : `${String(available)}/${String(total)} available${known - available > 0 ? ` · ${String(known - available)} out of rotation` : ""}`;
  const [newAddr, setNewAddr] = useState("");
  const [newWeight, setNewWeight] = useState(1);
  const [strategy, setStrategy] = useState(app.strategy || "round_robin");
  const [editing, setEditing] = useState<null | "health" | "discovery">(null);
  const isStatic = !app.discovery || app.discovery === "static";
  const routesUsing = app.routes_using ?? [];
  const shownRoutesUsing = routesUsing.slice(0, 8);
  const remainingRoutesUsing = routesUsing.length - shownRoutesUsing.length;

  useEffect(() => {
    setStrategy(app.strategy || "round_robin");
  }, [app.name, app.strategy]);

  return (
    <Drawer
      title={app.name}
      subtitle={`${app.strategy} · ${String(app.backends.length)} backend(s)`}
      onClose={onClose}
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          An app is a named pool of backend instances. Routes proxy to it by name, and Jul balances
          traffic across healthy backends.
        </p>

        {app.warnings && app.warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
            <span className="text-xs font-semibold uppercase tracking-wider text-jul-warning">
              Warnings
            </span>
            {app.warnings.map((warning, index) => (
              <p key={`aw-${String(index)}`} className="text-xs text-jul-text">
                {warning}
              </p>
            ))}
          </div>
        )}

        <div className="rounded-md border border-jul-border bg-jul-surface px-4 py-2">
          <Row label="Strategy" value={app.strategy} />
          <Row label="Backends" value={backendsValue} />
          <Row
            label="Health checks"
            value={
              app.health_check
                ? `on${app.health_check_path ? " · " + app.health_check_path : ""}`
                : "off"
            }
          />
          {app.health_check_interval && (
            <Row label="Probe interval" value={app.health_check_interval} />
          )}
          {app.max_fails ? <Row label="Max fails" value={String(app.max_fails)} /> : null}
          {app.fail_timeout && <Row label="Fail timeout" value={app.fail_timeout} />}
          <Row
            label="Discovery"
            value={
              app.discovery
                ? `${app.discovery}${app.discovery_target ? " · " + app.discovery_target : ""}`
                : "static"
            }
          />
        </div>

        <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface p-4">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Pool settings (one operation each)
          </span>
          <div className="flex flex-wrap items-end gap-2">
            <label className="flex-1 space-y-1">
              <span className="text-xs text-jul-muted">Load-balancing strategy</span>
              <select
                value={strategy}
                disabled={!canWrite || patch.busy}
                onChange={(event) => {
                  setStrategy(event.target.value);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent disabled:opacity-50"
              >
                {STRATEGIES.map((candidate) => (
                  <option key={candidate.value} value={candidate.value}>
                    {candidate.label}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              disabled={patch.busy || !canWrite || strategy === (app.strategy || "round_robin")}
              onClick={() => {
                patch.run({
                  op: "upstream_set_strategy",
                  upstream: app.name,
                  strategy,
                });
              }}
              className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              Review →
            </button>
          </div>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              disabled={!canWrite || patch.busy}
              onClick={() => {
                setEditing("health");
              }}
              className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text hover:bg-jul-bg disabled:opacity-40"
            >
              Edit health checks →
            </button>
            <button
              type="button"
              disabled={!canWrite || patch.busy}
              onClick={() => {
                setEditing("discovery");
              }}
              className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text hover:bg-jul-bg disabled:opacity-40"
            >
              Edit discovery →
            </button>
          </div>
          <ForbiddenAction permission="config:write" />
          <span className="text-xs text-jul-muted">
            Each edit uses the shared one-operation preview and Configuration handoff.
          </span>
        </div>

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Backends
          </span>
          {app.backends.length === 0 ? (
            <p className="text-xs text-jul-muted">No backends configured.</p>
          ) : (
            <table className="w-full overflow-hidden rounded-md border border-jul-border bg-jul-surface text-left">
              <thead>
                <tr className="border-b border-jul-border text-xs text-jul-muted">
                  <th className="px-3 py-2">Address</th>
                  <th className="px-3 py-2">Weight</th>
                  <th className="px-3 py-2">In-flight</th>
                  {isStatic && <th className="px-3 py-2" />}
                </tr>
              </thead>
              <tbody>
                {app.backends.map((backend) => (
                  <BackendRow
                    key={backend.address}
                    backend={backend}
                    canRemove={isStatic && app.backends.length > 1}
                    showRemove={isStatic}
                    canWrite={canWrite}
                    busy={patch.busy}
                    onRemove={() => {
                      patch.run({
                        op: "upstream_remove_backend",
                        upstream: app.name,
                        address: backend.address,
                      });
                    }}
                  />
                ))}
              </tbody>
            </table>
          )}

          {isStatic && (
            <div className="space-y-1 rounded-md border border-jul-border bg-jul-surface p-3">
              <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Add backend (one operation)
              </span>
              <div className="flex flex-wrap items-end gap-2">
                <label className="flex-1 space-y-1">
                  <span className="text-xs text-jul-muted">Address</span>
                  <input
                    type="text"
                    value={newAddr}
                    disabled={!canWrite || patch.busy}
                    placeholder="10.0.0.2:8080"
                    onChange={(event) => {
                      setNewAddr(event.target.value);
                    }}
                    className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent disabled:opacity-50"
                  />
                </label>
                <label className="w-24 space-y-1">
                  <span className="text-xs text-jul-muted">Weight</span>
                  <input
                    type="number"
                    min={1}
                    value={newWeight}
                    disabled={!canWrite || patch.busy}
                    onChange={(event) => {
                      setNewWeight(Math.max(1, Number(event.target.value) || 1));
                    }}
                    className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent disabled:opacity-50"
                  />
                </label>
                <button
                  type="button"
                  disabled={patch.busy || !canWrite || newAddr.trim() === ""}
                  onClick={() => {
                    patch.run({
                      op: "upstream_add_backend",
                      upstream: app.name,
                      address: newAddr.trim(),
                      weight: newWeight,
                    });
                  }}
                  className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
                >
                  Review →
                </button>
              </div>
              <ForbiddenAction permission="config:write" />
            </div>
          )}

          {patch.error && <p className="text-xs text-jul-danger">{patch.error}</p>}
        </div>

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Routes using this app
          </span>
          {routesUsing.length > 0 ? (
            <div className="space-y-2">
              <ul className="max-h-36 space-y-1 overflow-auto">
                {shownRoutesUsing.map((reference, index) => (
                  <li key={`ru-${String(index)}`} className="font-mono text-xs text-jul-text">
                    {reference}
                  </li>
                ))}
                {remainingRoutesUsing > 0 && (
                  <li className="text-xs text-jul-muted">…and {remainingRoutesUsing} more</li>
                )}
              </ul>
              <Link
                to="/routes"
                className="text-xs font-medium text-jul-accent underline hover:no-underline"
              >
                Open Routes →
              </Link>
            </div>
          ) : (
            <p className="text-xs text-jul-muted">No routes reference this app yet.</p>
          )}
        </div>

        <AppDangerZone app={app} />
      </div>

      {editing === "health" && (
        <HealthCheckEditor
          app={app}
          onClose={() => {
            setEditing(null);
          }}
        />
      )}
      {editing === "discovery" && (
        <DiscoveryEditor
          app={app}
          onClose={() => {
            setEditing(null);
          }}
        />
      )}
    </Drawer>
  );
}
