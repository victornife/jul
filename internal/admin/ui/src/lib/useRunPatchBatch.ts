/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useCallback, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  ConfigRejectedError,
  PatchOperationRejectedError,
  patchConfigBatch,
  type ConfigPatch,
  type PatchResult,
} from "@/api/client.ts";
import { setPendingDraft, type PendingPatchDraft } from "@/lib/configDraftHandoff.ts";

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
    // Preserve the exact ordered request array. No sorting, grouping, or
    // reconstruction is allowed between preview and final apply.
    ops: [...ops],
    ...(baseVersion !== undefined ? { baseVersion } : {}),
    summary: result.summary,
    operationSummaries: result.operation_summaries,
    valid: result.valid,
    validationErrors: result.validation_errors,
    previewDiff: result.diff,
    ...(result.lifecycle !== undefined ? { lifecycle: result.lifecycle } : {}),
    // candidate is intentionally omitted; ordinary preview is secret-safe.
  };
}

export interface RunPatchBatch {
  readonly error: PatchBatchPreviewError | null;
  readonly busy: boolean;
  /** Preview only, for flows that need an explicit confirmation before handoff. */
  readonly preview: (
    ops: readonly ConfigPatch[],
    baseVersion?: string,
  ) => Promise<PendingPatchDraft | null>;
  /** Commit an already previewed secret-safe assessment to ConfigPanel. */
  readonly handoff: (draft: PendingPatchDraft) => void;
  /** Preview and immediately hand off the exact ordered batch. */
  readonly run: (ops: readonly ConfigPatch[], baseVersion?: string) => Promise<void>;
  readonly clearError: () => void;
}

/**
 * The one structured batch-preview handoff used across the Console. It never
 * fetches raw candidate source and never claims final apply success.
 */
export function useRunPatchBatch(): RunPatchBatch {
  const navigate = useNavigate();
  const [error, setError] = useState<PatchBatchPreviewError | null>(null);
  const [busy, setBusy] = useState(false);

  const preview = useCallback(async (ops: readonly ConfigPatch[], baseVersion?: string) => {
    setError(null);
    if (ops.length === 0) {
      setError(new Error("The structured patch batch must contain at least one operation."));
      return null;
    }
    setBusy(true);
    try {
      const result = await patchConfigBatch([...ops], baseVersion);
      return patchResultToPendingDraft(ops, result, baseVersion);
    } catch (caught) {
      setError(toError(caught));
      return null;
    } finally {
      setBusy(false);
    }
  }, []);

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
