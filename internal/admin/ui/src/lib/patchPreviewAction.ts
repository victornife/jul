/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { PendingPatchDraft } from "@/lib/configDraftHandoff.ts";

export type PatchApplyAction = "hot" | "stage_restart" | "update_staged" | "none";

export interface PatchActionDecision {
  readonly action: PatchApplyAction;
  readonly reason: string;
  readonly requiresFreshPreview: boolean;
}

/**
 * Selects the UI action from the backend-provided lifecycle booleans. This does
 * not classify paths or infer lifecycle: lifecycle.Classify on the server is
 * the sole authority, and a missing assessment fails closed.
 */
export function decidePatchApplyAction(
  draft: PendingPatchDraft | null,
  hasManagedPendingRestart: boolean,
  stale = false,
): PatchActionDecision {
  if (draft === null) {
    return { action: "none", reason: "No structured patch is ready.", requiresFreshPreview: false };
  }

  if (stale || draft.baseVersion === undefined || draft.baseVersion.trim() === "") {
    return {
      action: "none",
      reason: stale
        ? "The base configuration changed. Generate a fresh preview before applying this patch."
        : "The preview has no pinned base version. Generate a fresh preview before applying this patch.",
      requiresFreshPreview: true,
    };
  }

  if (!draft.valid || draft.validationErrors.length > 0) {
    return {
      action: "none",
      reason: "The structured patch is invalid and cannot be applied.",
      requiresFreshPreview: false,
    };
  }

  const lifecycle = draft.lifecycle;
  if (lifecycle === undefined) {
    return {
      action: "none",
      reason: "A fresh lifecycle-aware preview is required before this patch can be applied.",
      requiresFreshPreview: true,
    };
  }

  if (lifecycle.validation_rejected_paths.length > 0) {
    return {
      action: "none",
      reason: "The authoritative lifecycle assessment rejected one or more paths.",
      requiresFreshPreview: false,
    };
  }

  if (hasManagedPendingRestart) {
    if (lifecycle.can_stage_restart) {
      return {
        action: "update_staged",
        reason: "A managed staged configuration already exists; this patch can update it.",
        requiresFreshPreview: false,
      };
    }
    return {
      action: "none",
      reason: "This patch cannot update the managed staged configuration.",
      requiresFreshPreview: false,
    };
  }

  if (lifecycle.can_apply_hot) {
    return {
      action: "hot",
      reason: "The authoritative lifecycle assessment permits hot apply.",
      requiresFreshPreview: false,
    };
  }

  if (lifecycle.can_stage_restart) {
    return {
      action: "stage_restart",
      reason: "The change is valid but requires a staged process restart.",
      requiresFreshPreview: false,
    };
  }

  return {
    action: "none",
    reason: "The authoritative lifecycle assessment offers neither hot apply nor stage restart.",
    requiresFreshPreview: false,
  };
}
