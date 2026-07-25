/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * AC-13: the configuration-write mutation state machine, extracted from
 * ConfigPanel into a pure, exhaustively-typed reducer so the many interlocking
 * pieces of write state live in one auditable place instead of a scatter of
 * useState/useRef calls. The reducer owns exactly the fields the audit calls
 * out:
 *   - operationGeneration — a monotonic token that identifies the current
 *     in-flight operation. Every async result is checked against it so a stale
 *     callback from a superseded operation can never mutate live state.
 *   - applyId / appliedResult / errorKind — the correlated result of the most
 *     recent apply (per-ID, never reconstructed) plus its classification.
 *   - wasPatch / patchCandidate — the operation kind (raw vs structured patch)
 *     and the candidate TOML that a patch apply was reviewing.
 *   - patchDraft — the pending structured patch (ops + preview + base version).
 *   - baseVersion / conflictVersion — the optimistic-concurrency tokens.
 *   - mode — hot vs stage_restart for the current operation.
 *   - adminConfirmedGeneration — the generation for which the operator has
 *     already confirmed an admin-affecting change, so a retry re-sends
 *     confirm_admin=true for the SAME operation and nothing else.
 *   - pollAttempts — bounded post-apply poll budget.
 *
 * The reducer is intentionally free of React and network concerns: ConfigPanel
 * keeps owning the react-query mutations and effects and dispatches typed
 * actions here. This makes the state transitions unit-testable in isolation
 * (configMutationMachine.test.ts) without rendering the panel.
 */

import type { ApplyResult, ConfigApplyErrorKind, ConfigPatch, ConfigDiff } from "@/api/client.ts";

/** The pending structured patch handed off to the panel for review + apply. */
export interface PatchDraftState {
  readonly ops: ConfigPatch[];
  readonly baseVersion?: string | undefined;
  readonly previewDiff: ConfigDiff;
  readonly candidate?: string | undefined;
}

/** The correlated result of the most recent apply, once one exists. */
export interface AppliedState {
  readonly operationGeneration: number;
  readonly result: ApplyResult;
  readonly errorKind: ConfigApplyErrorKind | null;
  readonly wasPatch: boolean;
  readonly patchCandidate: string | null;
}

export type ApplyMode = "hot" | "stage_restart";

export interface ConfigMutationState {
  /** Monotonic token identifying the current operation (supersedes stale ones). */
  readonly operationGeneration: number;
  /** The correlated apply result for the current/last operation, or null. */
  readonly applied: AppliedState | null;
  /** The pending structured patch draft, or null in raw mode. */
  readonly patchDraft: PatchDraftState | null;
  /** Optimistic-concurrency token the loaded config was read at. */
  readonly baseVersion: string | undefined;
  /** The current serving version reported by a 409 conflict, if any. */
  readonly conflictVersion: string | undefined;
  /** The generation for which an admin-affecting change was confirmed. */
  readonly adminConfirmedGeneration: number | null;
  /** Bounded post-apply poll attempts for the current operation. */
  readonly pollAttempts: number;
}

export function initialConfigMutationState(baseVersion?: string): ConfigMutationState {
  return {
    operationGeneration: 0,
    applied: null,
    patchDraft: null,
    baseVersion,
    conflictVersion: undefined,
    adminConfirmedGeneration: null,
    pollAttempts: 0,
  };
}

export type ConfigMutationAction =
  // Begin a new operation: bump the generation, clear the prior applied result
  // and any admin confirmation, and reset the poll budget. Returns the new
  // generation via the reducer (read state.operationGeneration after dispatch).
  | { type: "startOperation" }
  // Abandon the in-flight operation without recording a result (e.g. the editor
  // text changed). Bumps the generation so late callbacks are ignored.
  | { type: "cancelOperation" }
  // Record a correlated apply result for a specific generation. Ignored if the
  // generation is stale (a newer operation already superseded it).
  | {
      type: "applyResult";
      generation: number;
      result: ApplyResult;
      errorKind: ConfigApplyErrorKind | null;
      wasPatch: boolean;
      patchCandidate: string | null;
    }
  // Merge a terminal per-ID ledger record into the applied result. Only applies
  // when the generation matches AND the currently-held result's apply id equals
  // the record's id (never cross-correlate a different operation).
  | {
      type: "mergeTerminal";
      generation: number;
      applyId: string;
      terminal: Partial<ApplyResult>;
    }
  // Clear the applied result (e.g. discard, or editor reset).
  | { type: "clearApplied" }
  | { type: "setPatchDraft"; draft: PatchDraftState | null }
  | { type: "setBaseVersion"; baseVersion: string | undefined }
  | { type: "setConflictVersion"; conflictVersion: string | undefined }
  // Mark the current generation as admin-confirmed so a retry re-sends
  // confirm_admin=true for this exact operation.
  | { type: "confirmAdmin"; generation: number }
  | { type: "incrementPollAttempts" }
  | { type: "resetPollAttempts" };

export function configMutationReducer(
  state: ConfigMutationState,
  action: ConfigMutationAction,
): ConfigMutationState {
  switch (action.type) {
    case "startOperation":
      return {
        ...state,
        operationGeneration: state.operationGeneration + 1,
        applied: null,
        adminConfirmedGeneration: null,
        pollAttempts: 0,
      };
    case "cancelOperation":
      return {
        ...state,
        operationGeneration: state.operationGeneration + 1,
        adminConfirmedGeneration: null,
      };
    case "applyResult":
      // Drop a result from a superseded operation.
      if (action.generation !== state.operationGeneration) return state;
      return {
        ...state,
        applied: {
          operationGeneration: action.generation,
          result: action.result,
          errorKind: action.errorKind,
          wasPatch: action.wasPatch,
          patchCandidate: action.patchCandidate,
        },
      };
    case "mergeTerminal": {
      const current = state.applied;
      if (
        action.generation !== state.operationGeneration ||
        current === null ||
        current.operationGeneration !== action.generation ||
        current.result.apply_id !== action.applyId
      ) {
        return state;
      }
      return {
        ...state,
        applied: {
          ...current,
          result: { ...current.result, ...action.terminal, apply_id: action.applyId },
        },
      };
    }
    case "clearApplied":
      return { ...state, applied: null };
    case "setPatchDraft":
      return { ...state, patchDraft: action.draft };
    case "setBaseVersion":
      return { ...state, baseVersion: action.baseVersion };
    case "setConflictVersion":
      return { ...state, conflictVersion: action.conflictVersion };
    case "confirmAdmin":
      return { ...state, adminConfirmedGeneration: action.generation };
    case "incrementPollAttempts":
      return { ...state, pollAttempts: state.pollAttempts + 1 };
    case "resetPollAttempts":
      return { ...state, pollAttempts: 0 };
    default:
      // Exhaustiveness guard: a new action variant without a case is a type
      // error at the assignment below.
      return assertNever(action);
  }
}

/** True when the current operation still holds an admin confirmation. */
export function isAdminConfirmedFor(state: ConfigMutationState, generation: number): boolean {
  return state.adminConfirmedGeneration === generation;
}

/** Compile-time exhaustiveness guard for the action union. */
function assertNever(action: never): never {
  throw new Error(`unhandled config mutation action: ${JSON.stringify(action)}`);
}
