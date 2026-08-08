/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import {
  ConfigDiffSchema,
  PatchLifecycleSchema,
  PatchOperationSummarySchema,
  ValidationIssueSchema,
  type ConfigDiff,
  type ConfigPatch,
  type PatchLifecycle,
  type PatchOperationSummary,
  type ValidationIssue,
} from "@/api/client.ts";

/** The secret-safe structured assessment handed from an editor to ConfigPanel. */
export interface PendingPatchDraft {
  readonly kind: "patch";
  readonly ops: ConfigPatch[];
  readonly baseVersion?: string | undefined;
  readonly summary: string;
  readonly operationSummaries: PatchOperationSummary[];
  readonly valid: boolean;
  readonly validationErrors: ValidationIssue[];
  readonly previewDiff: ConfigDiff;
  readonly lifecycle?: PatchLifecycle | undefined;
  /** Populated only in ConfigPanel after the separately authorized candidate request. */
  readonly candidate?: string | undefined;
}

/**
 * Compatibility input for callers and session values created before #78. The
 * new shared hook always supplies the complete assessment; old one-op editors
 * may still supply only ops/base/diff while they migrate. ConfigPanel treats a
 * missing lifecycle as unknown and refreshes the preview before enabling apply.
 */
export interface LegacyPendingPatchDraftInput {
  readonly kind: "patch";
  readonly ops: ConfigPatch[];
  readonly baseVersion?: string | undefined;
  readonly previewDiff: ConfigDiff;
  readonly summary?: string | undefined;
  readonly operationSummaries?: PatchOperationSummary[] | undefined;
  readonly valid?: boolean | undefined;
  readonly validationErrors?: ValidationIssue[] | undefined;
  readonly lifecycle?: PatchLifecycle | undefined;
  /** Ignored. Ordinary preview handoff is never allowed to carry source TOML. */
  readonly candidate?: string | undefined;
}

/**
 * A pending draft handed off between editors and the ConfigPanel.
 * - toml: a raw candidate string for review in the raw editor
 * - patch: an ordered structured batch plus its complete secret-safe preview
 *   assessment. Candidate TOML is deliberately absent from ordinary handoff and
 *   can be populated only after ConfigPanel calls the config:raw-gated endpoint.
 */
export type PendingDraft = { kind: "toml"; toml: string } | PendingPatchDraft;
export type PendingDraftInput = { kind: "toml"; toml: string } | LegacyPendingPatchDraftInput;

const STORAGE_KEY = "__jul_config_pending_draft_v3";
const LEGACY_STORAGE_KEYS = [
  "jul.config.pending-draft.v2",
  "__jul_config_pending_draft_v2",
  "__jul_config_pending_draft",
] as const;
const ALL_STORAGE_KEYS = [STORAGE_KEY, ...LEGACY_STORAGE_KEYS] as const;
let pending: PendingDraft | null = null;

function storage(): Storage | null {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage;
  } catch {
    return null;
  }
}

function fallbackOperationSummaries(ops: readonly ConfigPatch[]): PatchOperationSummary[] {
  return ops.map((op, opIndex) => ({ op_index: opIndex, op: op.op, summary: op.op }));
}

function normalizeInput(draft: PendingDraftInput): PendingDraft {
  if (draft.kind === "toml") return draft;
  const validationErrors = [...(draft.validationErrors ?? [])];
  return {
    kind: "patch",
    ops: [...draft.ops],
    ...(draft.baseVersion !== undefined ? { baseVersion: draft.baseVersion } : {}),
    summary: draft.summary ?? draft.previewDiff.summary,
    operationSummaries:
      draft.operationSummaries !== undefined && draft.operationSummaries.length > 0
        ? [...draft.operationSummaries]
        : fallbackOperationSummaries(draft.ops),
    valid: draft.valid ?? validationErrors.length === 0,
    validationErrors,
    previewDiff: draft.previewDiff,
    ...(draft.lifecycle !== undefined ? { lifecycle: draft.lifecycle } : {}),
    // Deliberately omit candidate even if an obsolete caller supplied it.
  };
}

function clearStoredDrafts(session: Storage | null): void {
  if (session === null) return;
  for (const key of ALL_STORAGE_KEYS) session.removeItem(key);
}

export function setPendingDraft(draft: PendingDraftInput): void {
  // Never accept candidate source through the ordinary preview handoff. The
  // privileged candidate request writes only to ConfigPanel's in-memory state.
  pending = normalizeInput(draft);
  const session = storage();
  try {
    clearStoredDrafts(session);
    if (pending.kind === "patch") {
      // The ordinary preview assessment is value-free and secret-safe. Raw TOML
      // may contain credentials, so raw-editor handoff remains memory-only.
      session?.setItem(STORAGE_KEY, JSON.stringify(pending));
    }
  } catch {
    // Navigation in the current tab still works through the in-memory value.
  }
}

function objectValue(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null ? (value as Record<string, unknown>) : null;
}

function parseOps(value: unknown): ConfigPatch[] | null {
  if (!Array.isArray(value) || value.length === 0) return null;
  if (
    !value.every((item) => {
      const op = objectValue(item);
      return op !== null && typeof op.op === "string" && op.op.trim() !== "";
    })
  ) {
    return null;
  }
  return value as ConfigPatch[];
}

function parseOperationSummaries(value: unknown): PatchOperationSummary[] {
  if (!Array.isArray(value)) return [];
  const parsed: PatchOperationSummary[] = [];
  for (const entry of value) {
    const result = PatchOperationSummarySchema.safeParse(entry);
    if (result.success) parsed.push(result.data);
  }
  return parsed;
}

function parseValidationIssues(value: unknown): ValidationIssue[] {
  if (!Array.isArray(value)) return [];
  const parsed: ValidationIssue[] = [];
  for (const entry of value) {
    const result = ValidationIssueSchema.safeParse(entry);
    if (result.success) parsed.push(result.data);
  }
  return parsed;
}

function parsePatch(value: Record<string, unknown>): PendingPatchDraft | null {
  const ops = parseOps(value.ops);
  const diff = ConfigDiffSchema.safeParse(value.previewDiff);
  if (!ops || !diff.success) return null;

  const baseVersion = typeof value.baseVersion === "string" ? value.baseVersion : undefined;
  const summary = typeof value.summary === "string" ? value.summary : diff.data.summary;
  const operationSummaries = parseOperationSummaries(value.operationSummaries);
  const validationErrors = parseValidationIssues(value.validationErrors);
  const lifecycle = PatchLifecycleSchema.safeParse(value.lifecycle);

  return {
    kind: "patch",
    ops,
    ...(baseVersion !== undefined ? { baseVersion } : {}),
    summary,
    operationSummaries:
      operationSummaries.length > 0 ? operationSummaries : fallbackOperationSummaries(ops),
    valid: typeof value.valid === "boolean" ? value.valid : validationErrors.length === 0,
    validationErrors,
    previewDiff: diff.data,
    ...(lifecycle.success ? { lifecycle: lifecycle.data } : {}),
    // Deliberately ignore any serialized candidate field, including obsolete or
    // malicious values injected into sessionStorage.
  };
}

export function normalizePendingDraft(value: unknown): PendingDraft | null {
  const object = objectValue(value);
  if (!object) return null;
  if (object.kind === "toml" && typeof object.toml === "string") {
    return { kind: "toml", toml: object.toml };
  }
  if (object.kind === "patch") return parsePatch(object);
  return null;
}

function parseStored(raw: string): PendingPatchDraft | null {
  let value: unknown;
  try {
    value = JSON.parse(raw) as unknown;
  } catch {
    return null;
  }
  const normalized = normalizePendingDraft(value);
  // Raw TOML may contain credentials. New handoffs never persist it, and old
  // serialized raw drafts are discarded rather than resurrected from browser
  // storage. Same-tab navigation still uses the in-memory value above.
  return normalized?.kind === "patch" ? normalized : null;
}

/** Returns the pending draft (if any) and clears both memory and session state. */
export function takePendingDraft(): PendingDraft | null {
  const memory = pending;
  pending = null;

  const session = storage();
  try {
    if (memory !== null) {
      clearStoredDrafts(session);
      return memory;
    }

    if (session !== null) {
      for (const key of ALL_STORAGE_KEYS) {
        const serialized = session.getItem(key);
        session.removeItem(key);
        if (serialized === null) continue;
        const parsed = parseStored(serialized);
        if (parsed !== null) {
          clearStoredDrafts(session);
          return parsed;
        }
      }
    }
  } catch {
    return null;
  }
  return null;
}

/** Test/support hook: the key is exported without exposing storage internals. */
export const pendingDraftStorageKey = STORAGE_KEY;
