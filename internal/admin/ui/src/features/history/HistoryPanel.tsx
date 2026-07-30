/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useRef, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  fetchHistory,
  fetchHistorySnapshot,
  diffConfig,
  fetchManagedApply,
  rollback,
  describeApiError,
  ConfigAdminChangeError,
  type HistoryEntry,
} from "@/api/client.ts";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { EmptyState, Loading } from "@/components/ui.tsx";
import { DiffView } from "@/features/config/DiffView.tsx";

function formatBytes(n: number): string {
  if (n < 1024) return `${String(n)} B`;
  return `${(n / 1024).toFixed(1)} KB`;
}

function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

// finalizationAdvisory renders the AC-14 finalization provenance of a terminal
// managed apply as a single advisory sentence, or null when the sidecar
// finalized cleanly. These fields are orthogonal to reload success/failure: a
// committed (ok=true) rollback can still have degraded its history sidecar, and
// that must be surfaced as an advisory — never as an apply failure and never as
// a readiness signal.
function finalizationAdvisory(o: {
  readonly history_error?: string | undefined;
  readonly finalization_error?: string | undefined;
}): string | null {
  const parts: string[] = [];
  if (o.history_error)
    parts.push(`The configuration history snapshot degraded: ${o.history_error}.`);
  if (o.finalization_error) parts.push(`Finalization reported: ${o.finalization_error}.`);
  return parts.length > 0 ? parts.join(" ") : null;
}

function SnapshotViewer({ id, onClose }: { readonly id: string; readonly onClose: () => void }) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["history-snap", id],
    queryFn: () => fetchHistorySnapshot(id),
  });

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="flex w-full max-w-3xl flex-col gap-4 rounded-lg border border-jul-border bg-jul-bg p-6 shadow-xl">
        <div className="flex items-center justify-between">
          <span className="font-mono text-sm text-jul-muted">{id}</span>
          <button
            onClick={onClose}
            className="text-jul-muted hover:text-jul-text text-lg leading-none"
          >
            ✕
          </button>
        </div>
        {isLoading && <div className="text-jul-muted text-sm">Loading…</div>}
        {isError && (
          <div className="text-jul-danger text-sm">
            {describeApiError(error, "the snapshot").message}
          </div>
        )}
        {data && (
          <pre className="max-h-96 overflow-auto rounded-md border border-jul-border bg-jul-surface p-4 text-xs text-jul-text">
            {data.raw}
          </pre>
        )}
      </div>
    </div>
  );
}

function RollbackConfirm({
  id,
  busy,
  onConfirm,
  onCancel,
  adminChanges,
  error,
  pending,
  pollingExpired,
}: {
  readonly id: string;
  readonly busy: boolean;
  readonly onConfirm: () => void;
  readonly onCancel: () => void;
  readonly adminChanges: readonly string[];
  readonly error: Error | null;
  readonly pending: boolean;
  readonly pollingExpired: boolean;
}) {
  const snap = useQuery({
    queryKey: ["history-snap", id],
    queryFn: () => fetchHistorySnapshot(id),
  });
  const diff = useQuery({
    queryKey: ["history-rollback-diff", id],
    queryFn: () => diffConfig(snap.data?.raw ?? ""),
    enabled: snap.data !== undefined,
    staleTime: 0,
    refetchInterval: false,
    refetchOnWindowFocus: false,
    retry: false,
  });
  return (
    <ConfirmDialog
      title={
        adminChanges.length > 0 ? "Confirm admin access rollback?" : "Roll back to this snapshot?"
      }
      confirmLabel={adminChanges.length > 0 ? "Confirm and roll back" : "Roll back"}
      danger
      busy={busy}
      confirmDisabled={pending}
      cancelDisabled={pending && !pollingExpired}
      onConfirm={onConfirm}
      onCancel={onCancel}
    >
      <p>
        Re-applies snapshot <span className="font-mono text-jul-text">{id}</span> as the live
        configuration and triggers a reload. The current config is snapshotted first, so this is
        reversible.
      </p>
      <div className="mt-3">
        {adminChanges.length > 0 && (
          <ul className="mb-3 list-disc space-y-1 pl-5 text-xs text-jul-warning">
            {adminChanges.map((change) => (
              <li key={change}>{change}</li>
            ))}
          </ul>
        )}
        {snap.isLoading && <p className="text-xs">Loading snapshot…</p>}
        {diff.isFetching && <p className="text-xs text-jul-muted">Computing changes vs running…</p>}
        {diff.data && <DiffView diff={diff.data} />}
        {diff.isError && <p className="text-xs text-jul-danger">Unable to compute diff preview.</p>}
        {error && <p className="mt-2 text-xs text-jul-danger">{error.message}</p>}
        {pending && !pollingExpired && (
          <p className="mt-2 text-xs text-jul-warning">
            The rollback was saved, but its live result is still pending. This dialog will stay open
            until the matching operation finishes.
          </p>
        )}
        {pollingExpired && (
          <p className="mt-2 text-xs text-jul-warning">
            The final rollback result is still unavailable. Check Runtime Overview before making
            another configuration change.
          </p>
        )}
      </div>
    </ConfirmDialog>
  );
}

function EntryRow({
  entry,
  onView,
  onRollback,
  rolling,
}: {
  readonly entry: HistoryEntry;
  readonly onView: (id: string) => void;
  readonly onRollback: (id: string) => void;
  readonly rolling: boolean;
}) {
  return (
    <tr className="border-b border-jul-border last:border-b-0 hover:bg-jul-surface/60">
      <td className="px-4 py-3 font-mono text-xs text-jul-muted">{entry.id}</td>
      <td className="px-4 py-3 text-sm text-jul-text">{formatTime(entry.time)}</td>
      <td className="px-4 py-3 text-sm text-jul-muted">{formatBytes(entry.size)}</td>
      <td className="px-4 py-3">
        <div className="flex gap-2">
          <button
            onClick={() => {
              onView(entry.id);
            }}
            className="rounded-md border border-jul-border px-2 py-0.5 text-xs text-jul-muted hover:text-jul-text"
          >
            View
          </button>
          <button
            onClick={() => {
              onRollback(entry.id);
            }}
            disabled={rolling}
            className="rounded-md border border-jul-danger/40 px-2 py-0.5 text-xs text-jul-danger hover:bg-jul-danger/10 disabled:opacity-50"
          >
            {rolling ? "Rolling back…" : "Rollback"}
          </button>
        </div>
      </td>
    </tr>
  );
}

export function HistoryPanel() {
  const qc = useQueryClient();
  const [viewing, setViewing] = useState<string | null>(null);
  const [confirmId, setConfirmId] = useState<string | null>(null);
  const [rollingId, setRollingId] = useState<string | null>(null);
  const [confirmAdmin, setConfirmAdmin] = useState(false);
  const [adminChanges, setAdminChanges] = useState<string[]>([]);
  const [pendingApplyID, setPendingApplyID] = useState<string | null>(null);
  const [terminalError, setTerminalError] = useState<Error | null>(null);
  const [pollingExpired, setPollingExpired] = useState(false);
  // AC-10: a committed-but-degraded rollback surfaces here as a persistent,
  // dismissable warning banner (distinct from an error) with no retry action.
  const [degradedNotice, setDegradedNotice] = useState<string | null>(null);
  // AC-14: advisory finalization/history-sidecar provenance, orthogonal to the
  // reload outcome. Rendered as a non-blocking banner. Never affects readiness.
  const [finalizationNotice, setFinalizationNotice] = useState<string | null>(null);
  const rollbackAttemptRef = useRef(0);
  const rollbackPollAttemptsRef = useRef(0);

  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["history"],
    queryFn: fetchHistory,
  });

  const rollbackMutation = useMutation({
    mutationFn: ({ id, confirmed }: { id: string; confirmed: boolean; attempt: number }) =>
      rollback(id, confirmed),
    onMutate: ({ id }) => {
      setRollingId(id);
      setTerminalError(null);
    },
    onSuccess: (result, variables) => {
      if (rollbackAttemptRef.current !== variables.attempt) return;
      setRollingId(null);
      if (result.reload?.outcome === "saved_not_live" && result.apply_id) {
        rollbackPollAttemptsRef.current = 0;
        setPollingExpired(false);
        setPendingApplyID(result.apply_id);
        setTerminalError(null);
        return;
      }
      if (result.reload?.outcome === "applied_degraded") {
        // AC-10: applied_degraded is a COMMITTED rollback, not a failure. The
        // snapshot is now live; some subsystems reloaded degraded. Close the
        // dialog (and its repeatable rollback action), refresh dependent views,
        // and leave a separate, persistent warning banner — never a retry.
        setConfirmId(null);
        setConfirmAdmin(false);
        setAdminChanges([]);
        setPendingApplyID(null);
        setTerminalError(null);
        setDegradedNotice(
          result.reload.error ??
            "Rollback applied with a degraded reload. The snapshot is now the live configuration, but one or more subsystems reloaded degraded — review Runtime Overview.",
        );
        void qc.invalidateQueries();
        return;
      }
      setConfirmId(null);
      setConfirmAdmin(false);
      setAdminChanges([]);
      setPendingApplyID(null);
      void qc.invalidateQueries();
    },
    onError: (error, variables) => {
      if (rollbackAttemptRef.current !== variables.attempt) return;
      setRollingId(null);
      if (error instanceof ConfigAdminChangeError) {
        setConfirmAdmin(true);
        setAdminChanges([...error.changes]);
      }
    },
  });

  // AC-09: poll the terminal ledger by the EXACT apply ID rather than reading
  // the runtime overview's global last_managed_apply. The overview reflects the
  // most recent managed apply process-wide, so a newer, unrelated apply could
  // masquerade as this rollback's result; the exact-ID endpoint cannot. A 404
  // (record not yet visible) resolves to null and keeps polling — a missing
  // record is never mistaken for success.
  const rollbackTerminal = useQuery({
    queryKey: ["rollback-apply-record", pendingApplyID],
    queryFn: async () => {
      rollbackPollAttemptsRef.current += 1;
      if (rollbackPollAttemptsRef.current >= 20) setPollingExpired(true);
      if (!pendingApplyID) return null;
      const lookup = await fetchManagedApply(pendingApplyID);
      // A missing (404) record maps to null so the terminal-only resolution
      // below keeps waiting; a missing record is never mistaken for success.
      return lookup.kind === "record" ? lookup.record : null;
    },
    enabled: pendingApplyID !== null,
    retry: false,
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchInterval: (query) => {
      const record = query.state.data;
      // Stop once the exact-ID record is terminal, or polling has expired; a
      // null (404) or pending record keeps the interval alive.
      return record?.state === "terminal" || rollbackPollAttemptsRef.current >= 20
        ? false
        : 1500;
    },
  });

  useEffect(() => {
    if (!pendingApplyID || !confirmId) return;
    const record = rollbackTerminal.data;
    // Only a terminal record for the exact ID resolves the wait. null (404, not
    // yet recorded) and state==="pending" both keep the dialog open — the
    // console never claims success from a missing or in-flight record.
    if (!record || record.id !== pendingApplyID || record.state !== "terminal") return;
    setPendingApplyID(null);
    setPollingExpired(false);
    setConfirmAdmin(false);
    setAdminChanges([]);
    // AC-14: surface finalization provenance whenever the terminal record
    // carries it, including on a fully-live rollback — it is advisory and
    // independent of the reload outcome (a committed rollback can be ok=true
    // while its history sidecar degraded). Never readiness-affecting.
    setFinalizationNotice(finalizationAdvisory(record));
    const result = record.result;
    if (result.ok && result.reload?.outcome === "applied_live") {
      setConfirmId(null);
      setTerminalError(null);
      void qc.invalidateQueries();
      return;
    }
    if (result.ok) {
      // AC-10: committed-but-degraded. Dismiss the dialog and its retry action,
      // refresh dependent views, and leave a persistent, separate warning
      // banner rather than presenting a committed rollback as a failure.
      setConfirmId(null);
      setTerminalError(null);
      setDegradedNotice(
        result.reload?.error ??
          "Rollback applied with a degraded reload. The snapshot is now the live configuration, but one or more subsystems reloaded degraded — review Runtime Overview.",
      );
      void qc.invalidateQueries();
      return;
    }
    setTerminalError(
      new Error(
        result.restore_error
          ? `Rollback failed and restoration failed: ${result.restore_error}`
          : result.restored
            ? "Rollback was rejected; the previous configuration was restored."
            : "Rollback was rejected and restoration could not be confirmed. Check Runtime Overview.",
      ),
    );
    void qc.invalidateQueries();
  }, [confirmId, pendingApplyID, qc, rollbackTerminal.data]);

  if (isLoading) return <Loading label="Loading history…" />;
  if (isError || !data)
    return <PanelError error={error} resource="the history" onRetry={() => void refetch()} />;

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">Config History</h1>
        <p className="max-w-3xl text-sm text-jul-muted">
          Saved configuration snapshots with diffs, attribution, and one-click rollback. Review what
          changed, who changed it, and restore a previous working state safely.
        </p>
      </div>

      {degradedNotice && (
        <div className="flex items-start justify-between gap-4 rounded-lg border border-jul-warning/40 bg-jul-warning/5 p-4">
          <div className="space-y-1">
            <p className="text-sm font-semibold text-jul-warning">
              Rollback applied — degraded reload
            </p>
            <p className="text-xs text-jul-muted">{degradedNotice}</p>
            {finalizationNotice && <p className="text-xs text-jul-muted">{finalizationNotice}</p>}
          </div>
          <button
            onClick={() => {
              setDegradedNotice(null);
              setFinalizationNotice(null);
            }}
            className="text-jul-muted hover:text-jul-text text-lg leading-none"
            aria-label="Dismiss"
          >
            ✕
          </button>
        </div>
      )}

      {!degradedNotice && finalizationNotice && (
        <div className="flex items-start justify-between gap-4 rounded-lg border border-jul-warning/40 bg-jul-warning/5 p-4">
          <div className="space-y-1">
            <p className="text-sm font-semibold text-jul-warning">Finalization advisory</p>
            <p className="text-xs text-jul-muted">{finalizationNotice}</p>
          </div>
          <button
            onClick={() => {
              setFinalizationNotice(null);
            }}
            className="text-jul-muted hover:text-jul-text text-lg leading-none"
            aria-label="Dismiss"
          >
            ✕
          </button>
        </div>
      )}

      {viewing && (
        <SnapshotViewer
          id={viewing}
          onClose={() => {
            setViewing(null);
          }}
        />
      )}

      {data.length === 0 ? (
        <EmptyState
          title="No snapshots yet"
          description="Apply a config change to create the first snapshot. Snapshots let you review and roll back to any previous configuration."
        />
      ) : (
        <div className="rounded-lg border border-jul-border bg-jul-surface overflow-hidden">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-jul-border text-xs text-jul-muted">
                <th className="px-4 py-2">ID</th>
                <th className="px-4 py-2">Time</th>
                <th className="px-4 py-2">Size</th>
                <th className="px-4 py-2">Actions</th>
              </tr>
            </thead>
            <tbody>
              {data.map((entry) => (
                <EntryRow
                  key={entry.id}
                  entry={entry}
                  onView={(id) => {
                    setViewing(id);
                  }}
                  onRollback={(id) => {
                    rollbackAttemptRef.current += 1;
                    setConfirmId(id);
                    setConfirmAdmin(false);
                    setAdminChanges([]);
                    setPendingApplyID(null);
                    setTerminalError(null);
                    setPollingExpired(false);
                    setDegradedNotice(null);
                    setFinalizationNotice(null);
                    rollbackPollAttemptsRef.current = 0;
                    rollbackMutation.reset();
                  }}
                  rolling={rollingId === entry.id}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {confirmId && (
        <RollbackConfirm
          id={confirmId}
          busy={rollbackMutation.isPending}
          onConfirm={() => {
            rollbackMutation.mutate({
              id: confirmId,
              confirmed: confirmAdmin,
              attempt: rollbackAttemptRef.current,
            });
          }}
          onCancel={() => {
            rollbackAttemptRef.current += 1;
            setConfirmId(null);
            setConfirmAdmin(false);
            setAdminChanges([]);
            setPendingApplyID(null);
            setTerminalError(null);
            setPollingExpired(false);
            rollbackMutation.reset();
          }}
          adminChanges={adminChanges}
          error={
            terminalError ??
            (rollbackMutation.error instanceof ConfigAdminChangeError
              ? null
              : rollbackMutation.error instanceof Error
                ? rollbackMutation.error
                : null)
          }
          pending={pendingApplyID !== null}
          pollingExpired={pendingApplyID !== null && pollingExpired}
        />
      )}
    </div>
  );
}