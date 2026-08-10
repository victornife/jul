/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useCallback, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  ConfigRejectedError,
  PatchOperationRejectedError,
  fetchPendingRestart,
  patchConfigBatch,
  type ConfigPatch,
  type PatchResult,
} from "@/api/client.ts";
import {
  recommendPatchAction,
  setPendingDraft,
  snapshotPendingRestart,
  type PendingPatchDraft,
  type PendingRestartSnapshot,
} from "@/lib/configDraftHandoff.ts";

export type PatchBatchPreviewError = Error;

function toError(error: unknown): PatchBatchPreviewError {
  return error instanceof Error ? error : new Error("The structured edit could not be previewed.");
}

export function describePatchBatchError(error: PatchBatchPreviewError | null): string | null {
  if (error === null) return null;
  if (error instanceof PatchOperationRejectedError) {
    return `Operation ${String(error.opIndex)} (${error.op}) was rejected: ${error.message}`;
  }
  if (error instanceof ConfigRejectedError) return error.message;
  return error.message || "The structured edit could not be previewed.";
}

/** Pure conversion from the secret-safe preview response to pending state. */
export function patchResultToPendingDraft(
  ops: readonly ConfigPatch[],
  result: PatchResult,
  requestedBaseVersion?: string,
  pendingRestart?: PendingRestartSnapshot,
): PendingPatchDraft {
  if (
    requestedBaseVersion !== undefined &&
    result.base_version !== undefined &&
    result.base_version !== requestedBaseVersion
  ) {
    throw new Error("The preview response did not match the requested base version.");
  }
  const baseVersion = result.base_version ?? requestedBaseVersion;
  return {
    kind: "patch",
    ops: [...ops],
    ...(baseVersion !== undefined ? { baseVersion } : {}),
    summary: result.summary,
    operationSummaries: result.operation_summaries,
    valid: result.valid,
    validationErrors: result.validation_errors,
    previewDiff: result.diff,
    ...(result.lifecycle !== undefined ? { lifecycle: result.lifecycle } : {}),
    recommendedAction:
      pendingRestart === undefined
        ? "none"
        : recommendPatchAction(result.lifecycle, pendingRestart),
    ...(pendingRestart !== undefined ? { pendingRestart } : {}),
    candidateState: "not_requested",
    requiresFreshPreview:
      pendingRestart === undefined ||
      baseVersion === undefined ||
      baseVersion.trim() === "" ||
      result.lifecycle === undefined,
    // Candidate is intentionally omitted; ordinary preview is secret-safe.
  };
}

/**
 * Capture the value-free pending-restart state before previewing the pinned
 * operation batch. ConfigPanel compares this exact pre-preview snapshot with
 * current state and forces an exact re-preview when anything moved.
 */
export async function previewPatchBatchDraft(
  ops: readonly ConfigPatch[],
  baseVersion?: string,
): Promise<PendingPatchDraft> {
  const pendingResponse = await fetchPendingRestart();
  const pendingSnapshot = snapshotPendingRestart(pendingResponse);
  const result = await patchConfigBatch([...ops], baseVersion);
  return patchResultToPendingDraft(ops, result, baseVersion, pendingSnapshot);
}

export interface RunPatchBatch {
  readonly error: PatchBatchPreviewError | null;
  readonly busy: boolean;
  readonly preview: (
    ops: readonly ConfigPatch[],
    baseVersion?: string,
  ) => Promise<PendingPatchDraft | null>;
  readonly handoff: (draft: PendingPatchDraft) => void;
  readonly run: (ops: readonly ConfigPatch[], baseVersion?: string) => Promise<void>;
  readonly clearError: () => void;
}

/**
 * Shared ordered batch-preview handoff. It captures pending-restart state before
 * the preview and carries that value-free snapshot to ConfigPanel, where a
 * mismatch forces an exact re-preview before any primary action is enabled.
 */
export function useRunPatchBatch(): RunPatchBatch {
  const navigate = useNavigate();
  const [error, setError] = useState<PatchBatchPreviewError | null>(null);
  const [busy, setBusy] = useState(false);

  const preview = useCallback(
    async (ops: readonly ConfigPatch[], baseVersion?: string) => {
      setError(null);
      if (ops.length === 0) {
        setError(new Error("The structured patch batch must contain at least one operation."));
        return null;
      }
      setBusy(true);
      try {
        return await previewPatchBatchDraft(ops, baseVersion);
      } catch (caught) {
        setError(toError(caught));
        return null;
      } finally {
        setBusy(false);
      }
    },
    [],
  );

  const handoff = useCallback(
    (draft: PendingPatchDraft): void => {
      setPendingDraft(draft);
      void navigate("/config");
    },
    [navigate],
  );

  const run = useCallback(
    async (ops: readonly ConfigPatch[], baseVersion?: string): Promise<void> => {
      const draft = await preview(ops, baseVersion);
      if (draft !== null) handoff(draft);
    },
    [handoff, preview],
  );

  const clearError = useCallback((): void => {
    setError(null);
  }, []);

  return { error, busy, preview, handoff, run, clearError };
}
