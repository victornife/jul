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
  fetchOverview,
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
        setTerminalError(
          new Error(
            result.reload.error ??
              "Rollback completed with a degraded reload. Review Runtime Overview before continuing.",
          ),
        );
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

  const rollbackTerminal = useQuery({
    queryKey: ["rollback-apply-overview", pendingApplyID],
    queryFn: async () => {
      rollbackPollAttemptsRef.current += 1;
      if (rollbackPollAttemptsRef.current >= 20) setPollingExpired(true);
      return fetchOverview();
    },
    enabled: pendingApplyID !== null,
    retry: false,
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchInterval: (query) => {
      const terminal = query.state.data?.last_managed_apply;
      return terminal?.id === pendingApplyID || rollbackPollAttemptsRef.current >= 20
        ? false
        : 1500;
    },
  });

  useEffect(() => {
    if (!pendingApplyID || !confirmId) return;
    const terminal = rollbackTerminal.data?.last_managed_apply;
    if (!terminal || terminal.id !== pendingApplyID) return;
    setPendingApplyID(null);
    setPollingExpired(false);
    setConfirmAdmin(false);
    setAdminChanges([]);
    if (terminal.ok && terminal.outcome === "applied_live") {
      setConfirmId(null);
      setTerminalError(null);
      void qc.invalidateQueries();
      return;
    }
    if (terminal.ok) {
      setTerminalError(
        new Error(
          "Rollback completed with a degraded reload. Review Runtime Overview before continuing.",
        ),
      );
      void qc.invalidateQueries();
      return;
    }
    setTerminalError(
      new Error(
        terminal.restore_error
          ? `Rollback failed and restoration failed: ${terminal.restore_error}`
          : terminal.restored
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
