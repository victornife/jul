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
  type PendingRestartResponse,
  type ValidationIssue,
} from "@/api/client.ts";

export type RecommendedConfigAction = "hot" | "stage_restart" | "update_staged" | "none";
export type CandidateAvailability = "not_requested" | "memory_only" | "hidden" | "unavailable";

/** Value-free state used only to detect a pending-restart race. */
export interface PendingRestartSnapshot {
  readonly state: "none" | "managed_staged" | "external_divergence" | "inconsistent";
  readonly stagedVersion?: string | undefined;
  readonly servingVersion?: string | undefined;
  readonly subsystems: string[];
}

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
  readonly recommendedAction: RecommendedConfigAction;
  readonly pendingRestart?: PendingRestartSnapshot | undefined;
  readonly candidateState: CandidateAvailability;
  readonly requiresFreshPreview: boolean;
  /** Populated only in ConfigPanel after a separately authorized request. */
  readonly candidate?: string | undefined;
}

/** Raw candidates are same-tab, memory-only and always pinned to their source base. */
export interface PendingRawDraft {
  readonly kind: "toml";
  readonly toml: string;
  readonly baseVersion: string;
  readonly previewDiff: ConfigDiff;
  readonly lifecycle: PatchLifecycle;
  readonly recommendedAction: "stage_restart" | "update_staged" | "none";
  readonly pendingRestart: PendingRestartSnapshot;
  readonly candidateState: "memory_only";
}

/**
 * Compatibility input for raw editors that have not yet migrated to the
 * lifecycle-aware candidate preview. It remains same-tab and memory-only and is
 * deliberately never treated as an assessed cache/raw handoff.
 */
export interface LegacyPendingRawDraft {
  readonly kind: "toml";
  readonly toml: string;
}

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
  readonly recommendedAction?: RecommendedConfigAction | undefined;
  readonly pendingRestart?: PendingRestartSnapshot | undefined;
  readonly candidateState?: CandidateAvailability | undefined;
  readonly requiresFreshPreview?: boolean | undefined;
  /** Ignored: source TOML never crosses the ordinary structured handoff. */
  readonly candidate?: string | undefined;
}

export type PendingDraft = PendingRawDraft | LegacyPendingRawDraft | PendingPatchDraft;
export type PendingDraftInput = PendingRawDraft | LegacyPendingRawDraft | LegacyPendingPatchDraftInput;

const STORAGE_KEY = "__jul_config_pending_draft_v4";
const LEGACY_STORAGE_KEYS = [
  "__jul_config_pending_draft_v3",
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

function objectValue(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null ? (value as Record<string, unknown>) : null;
}

function fallbackOperationSummaries(ops: readonly ConfigPatch[]): PatchOperationSummary[] {
  return ops.map((op, opIndex) => ({ op_index: opIndex, op: op.op, summary: op.op }));
}

export function snapshotPendingRestart(response: PendingRestartResponse): PendingRestartSnapshot {
  const status = response.status;
  const state = status?.state ?? "none";
  return {
    state,
    ...(status?.staged_version !== undefined ? { stagedVersion: status.staged_version } : {}),
    ...(status?.serving_version !== undefined ? { servingVersion: status.serving_version } : {}),
    subsystems: [...(status?.subsystems ?? [])].sort(),
  };
}

export function pendingRestartSnapshotEqual(
  left: PendingRestartSnapshot | undefined,
  right: PendingRestartSnapshot | undefined,
): boolean {
  if (left === undefined || right === undefined) return false;
  return (
    left.state === right.state &&
    left.stagedVersion === right.stagedVersion &&
    left.servingVersion === right.servingVersion &&
    left.subsystems.length === right.subsystems.length &&
    left.subsystems.every((value, index) => value === right.subsystems[index])
  );
}

export function recommendPatchAction(
  lifecycle: PatchLifecycle | undefined,
  pendingRestart: PendingRestartSnapshot,
): RecommendedConfigAction {
  if (lifecycle === undefined || lifecycle.validation_rejected_paths.length > 0) return "none";
  if (pendingRestart.state === "managed_staged") {
    return lifecycle.can_stage_restart ? "update_staged" : "none";
  }
  if (pendingRestart.state !== "none") return "none";
  if (lifecycle.can_apply_hot) return "hot";
  return lifecycle.can_stage_restart ? "stage_restart" : "none";
}

function normalizeInput(draft: PendingDraftInput): PendingDraft {
  if (draft.kind === "toml") return draft;
  const validationErrors = [...(draft.validationErrors ?? [])];
  const missingAssessment =
    draft.baseVersion === undefined ||
    draft.baseVersion.trim() === "" ||
    draft.lifecycle === undefined ||
    draft.pendingRestart === undefined;
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
    recommendedAction: draft.recommendedAction ?? "none",
    ...(draft.pendingRestart !== undefined ? { pendingRestart: draft.pendingRestart } : {}),
    candidateState: draft.candidateState ?? "not_requested",
    requiresFreshPreview: draft.requiresFreshPreview ?? missingAssessment,
    // Candidate is deliberately omitted even if a legacy caller supplied it.
  };
}

function clearStoredDrafts(session: Storage | null): void {
  if (session === null) return;
  for (const key of ALL_STORAGE_KEYS) session.removeItem(key);
}

function secretSafeStoredPatch(draft: PendingPatchDraft): Omit<PendingPatchDraft, "candidate"> {
  return {
    kind: draft.kind,
    ops: [...draft.ops],
    ...(draft.baseVersion !== undefined ? { baseVersion: draft.baseVersion } : {}),
    summary: draft.summary,
    operationSummaries: [...draft.operationSummaries],
    valid: draft.valid,
    validationErrors: [...draft.validationErrors],
    previewDiff: draft.previewDiff,
    ...(draft.lifecycle !== undefined ? { lifecycle: draft.lifecycle } : {}),
    recommendedAction: draft.recommendedAction,
    ...(draft.pendingRestart !== undefined ? { pendingRestart: draft.pendingRestart } : {}),
    candidateState: draft.candidateState,
    requiresFreshPreview: draft.requiresFreshPreview,
  };
}

export function setPendingDraft(draft: PendingDraftInput): void {
  pending = normalizeInput(draft);
  const session = storage();
  try {
    clearStoredDrafts(session);
    if (pending.kind === "patch") {
      session?.setItem(STORAGE_KEY, JSON.stringify(secretSafeStoredPatch(pending)));
    }
    // Raw/full candidate TOML remains in memory only. Never write it to browser
    // storage, even for same-tab convenience.
  } catch {
    // In-memory navigation still works when storage is unavailable.
  }
}

function parseOps(value: unknown): ConfigPatch[] | null {
  if (!Array.isArray(value) || value.length === 0) return null;
  if (!value.every((entry) => typeof objectValue(entry)?.op === "string")) return null;
  return value as ConfigPatch[];
}

function parseOperationSummaries(value: unknown): PatchOperationSummary[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((entry) => {
    const parsed = PatchOperationSummarySchema.safeParse(entry);
    return parsed.success ? [parsed.data] : [];
  });
}

function parseValidationIssues(value: unknown): ValidationIssue[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((entry) => {
    const parsed = ValidationIssueSchema.safeParse(entry);
    return parsed.success ? [parsed.data] : [];
  });
}

function parsePendingSnapshot(value: unknown): PendingRestartSnapshot | undefined {
  const object = objectValue(value);
  if (object === null) return undefined;
  const states = ["none", "managed_staged", "external_divergence", "inconsistent"] as const;
  if (!states.includes(object.state as (typeof states)[number])) return undefined;
  const subsystems = Array.isArray(object.subsystems)
    ? object.subsystems.filter((item): item is string => typeof item === "string").sort()
    : [];
  return {
    state: object.state as PendingRestartSnapshot["state"],
    ...(typeof object.stagedVersion === "string" ? { stagedVersion: object.stagedVersion } : {}),
    ...(typeof object.servingVersion === "string" ? { servingVersion: object.servingVersion } : {}),
    subsystems,
  };
}

function parsePatch(value: Record<string, unknown>): PendingPatchDraft | null {
  const ops = parseOps(value.ops);
  const diff = ConfigDiffSchema.safeParse(value.previewDiff);
  if (ops === null || !diff.success) return null;
  const baseVersion = typeof value.baseVersion === "string" ? value.baseVersion : undefined;
  const operationSummaries = parseOperationSummaries(value.operationSummaries);
  const validationErrors = parseValidationIssues(value.validationErrors);
  const lifecycle = PatchLifecycleSchema.safeParse(value.lifecycle);
  const pendingRestart = parsePendingSnapshot(value.pendingRestart);
  const recommended = ["hot", "stage_restart", "update_staged", "none"].includes(
    String(value.recommendedAction),
  )
    ? (value.recommendedAction as RecommendedConfigAction)
    : "none";
  const candidateStates = ["not_requested", "memory_only", "hidden", "unavailable"];
  const candidateState = candidateStates.includes(String(value.candidateState))
    ? (value.candidateState as CandidateAvailability)
    : "not_requested";
  const incomplete =
    baseVersion === undefined || baseVersion.trim() === "" || !lifecycle.success || pendingRestart === undefined;
  return {
    kind: "patch",
    ops,
    ...(baseVersion !== undefined ? { baseVersion } : {}),
    summary: typeof value.summary === "string" ? value.summary : diff.data.summary,
    operationSummaries:
      operationSummaries.length > 0 ? operationSummaries : fallbackOperationSummaries(ops),
    valid: typeof value.valid === "boolean" ? value.valid : validationErrors.length === 0,
    validationErrors,
    previewDiff: diff.data,
    ...(lifecycle.success ? { lifecycle: lifecycle.data } : {}),
    recommendedAction: recommended,
    ...(pendingRestart !== undefined ? { pendingRestart } : {}),
    candidateState,
    requiresFreshPreview:
      typeof value.requiresFreshPreview === "boolean" ? value.requiresFreshPreview || incomplete : true,
    // Ignore candidate/raw TOML even when injected into sessionStorage.
  };
}

export function normalizePendingDraft(value: unknown): PendingDraft | null {
  const object = objectValue(value);
  if (object === null) return null;
  if (object.kind === "toml") {
    // Raw handoffs are accepted only through setPendingDraft's in-memory value;
    // parseStored still rejects every raw form. Metadata-complete handoffs use
    // the lifecycle-aware path; legacy editors retain the historical raw flow.
    if (typeof object.toml !== "string") return null;
    const diff = ConfigDiffSchema.safeParse(object.previewDiff);
    const lifecycle = PatchLifecycleSchema.safeParse(object.lifecycle);
    const pendingRestart = parsePendingSnapshot(object.pendingRestart);
    const action = object.recommendedAction;
    if (
      typeof object.baseVersion === "string" &&
      diff.success &&
      lifecycle.success &&
      pendingRestart !== undefined &&
      (action === "stage_restart" || action === "update_staged" || action === "none")
    ) {
      return {
        kind: "toml",
        toml: object.toml,
        baseVersion: object.baseVersion,
        previewDiff: diff.data,
        lifecycle: lifecycle.data,
        recommendedAction: action,
        pendingRestart,
        candidateState: "memory_only",
      };
    }
    return { kind: "toml", toml: object.toml };
  }
  return object.kind === "patch" ? parsePatch(object) : null;
}

function parseStored(raw: string): PendingPatchDraft | null {
  try {
    const normalized = normalizePendingDraft(JSON.parse(raw) as unknown);
    return normalized?.kind === "patch" ? normalized : null;
  } catch {
    return null;
  }
}

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

export const pendingDraftStorageKey = STORAGE_KEY;
