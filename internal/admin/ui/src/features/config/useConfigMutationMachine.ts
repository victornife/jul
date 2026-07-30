/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * AC-13: the React binding for the pure config-write mutation reducer
 * (configMutationMachine.ts). It exposes the exact imperative surface the
 * ConfigPanel relies on so the panel's handlers do not need to change shape:
 *
 *   - operationIDRef / confirmedAdminOperationRef / postApplyPollAttemptsRef —
 *     synchronous refs for the values that async closures (mutationFns and
 *     react-query refetchInterval) read WITHOUT a re-render. operationIDRef and
 *     confirmedAdminOperationRef mirror the reducer's operationGeneration /
 *     adminConfirmedGeneration; postApplyPollAttemptsRef is the sole home of the
 *     post-apply poll counter (the reducer no longer keeps a duplicate). The
 *     reducer is the single source of truth: these refs are only ever written
 *     through the dispatching hook methods below, never mutated ad hoc, so a ref
 *     can never diverge from the reducer state it mirrors.
 *   - startOperation() / cancelOperation() — bump the generation, reset the poll
 *     budget, and clear any admin confirmation (mirror ref + dispatch) so a
 *     superseded operation's late callback is ignored.
 *   - confirmAdminForOperation(generation) — record that the operator confirmed
 *     an admin-affecting change for this exact operation: it dispatches
 *     confirmAdmin AND mirrors the ref, so a same-operation retry re-sends
 *     confirm_admin=true without the panel mutating the ref behind the reducer's
 *     back.
 *   - appliedState / setAppliedState — the correlated apply result, exposed with
 *     the panel's historical `operationID` field name. setAppliedState records a
 *     fresh result (or clears it); the exact-ID terminal merge goes through the
 *     dedicated mergeTerminalRecord so the full ledger record is retained.
 *   - mergeTerminalRecord(record) — merge the full exact-ID terminal ledger
 *     record into the current applied result (generation + id correlated).
 *   - patchDraft / setPatchDraft, baseVersion / setBaseVersion,
 *     conflictVersion / setConflictVersion — thin dispatch wrappers.
 *
 * Keeping the reducer pure and unit-tested while this hook owns only the
 * React/ref plumbing is the whole point of the extraction: the state-transition
 * invariants live in one auditable, test-covered place.
 */

import { useReducer, useRef, useMemo, useCallback } from "react";
import type { ApplyResult, ConfigApplyErrorKind, ManagedApplyRecord } from "@/api/client.ts";
import {
  configMutationReducer,
  initialConfigMutationState,
  type ConfigMutationState,
  type PatchDraftState,
} from "@/features/config/configMutationMachine.ts";

/** The applied-result shape the ConfigPanel consumes (historical field names). */
export interface PanelAppliedState {
  readonly operationID: number;
  readonly result: ApplyResult;
  readonly errorKind: ConfigApplyErrorKind | null;
  readonly wasPatch: boolean;
  readonly patchCandidate: string | null;
  readonly terminalRecord: ManagedApplyRecord | null;
}

// A fresh correlated result the panel records; the terminal ledger record is
// never set here (it starts null and is added only by mergeTerminalRecord).
type SetApplied = Omit<PanelAppliedState, "terminalRecord"> | null;

export interface ConfigMutationMachine {
  readonly state: ConfigMutationState;
  readonly operationIDRef: React.RefObject<number>;
  readonly confirmedAdminOperationRef: React.RefObject<number | null>;
  readonly postApplyPollAttemptsRef: React.RefObject<number>;
  readonly appliedState: PanelAppliedState | null;
  readonly patchDraft: PatchDraftState | null;
  readonly baseVersion: string | undefined;
  readonly conflictVersion: string | undefined;
  // Declared as function-type PROPERTIES (not method signatures) so destructuring
  // them in the panel does not trip @typescript-eslint's unbound-method rule —
  // they are plain closures from useCallback, never methods bound to `this`.
  readonly startOperation: () => number;
  readonly cancelOperation: () => void;
  readonly confirmAdminForOperation: (generation: number) => void;
  readonly setAppliedState: (next: SetApplied) => void;
  readonly mergeTerminalRecord: (record: ManagedApplyRecord) => void;
  readonly setPatchDraft: (draft: PatchDraftState | null) => void;
  readonly setBaseVersion: (baseVersion: string | undefined) => void;
  readonly setConflictVersion: (conflictVersion: string | undefined) => void;
}

function toPanelApplied(state: ConfigMutationState): PanelAppliedState | null {
  const a = state.applied;
  if (a === null) return null;
  return {
    operationID: a.operationGeneration,
    result: a.result,
    errorKind: a.errorKind,
    wasPatch: a.wasPatch,
    patchCandidate: a.patchCandidate,
    terminalRecord: a.terminalRecord,
  };
}

export function useConfigMutationMachine(initialBaseVersion?: string): ConfigMutationMachine {
  const [state, dispatch] = useReducer(
    configMutationReducer,
    initialBaseVersion,
    initialConfigMutationState,
  );

  // Synchronous mirrors for values read inside async closures without a render.
  const operationIDRef = useRef(state.operationGeneration);
  const confirmedAdminOperationRef = useRef<number | null>(state.adminConfirmedGeneration);
  const postApplyPollAttemptsRef = useRef(0);
  // Latest derived applied state, so mergeTerminalRecord can correlate an
  // incoming terminal record against the current operation + apply id without
  // depending on a fresh render.
  const appliedRef = useRef<PanelAppliedState | null>(toPanelApplied(state));
  appliedRef.current = toPanelApplied(state);

  const startOperation = useCallback((): number => {
    const next = operationIDRef.current + 1;
    operationIDRef.current = next;
    confirmedAdminOperationRef.current = null;
    postApplyPollAttemptsRef.current = 0;
    dispatch({ type: "startOperation" });
    return next;
  }, []);

  const cancelOperation = useCallback((): void => {
    operationIDRef.current += 1;
    confirmedAdminOperationRef.current = null;
    postApplyPollAttemptsRef.current = 0;
    dispatch({ type: "cancelOperation" });
  }, []);

  // Record the operator's admin-change confirmation for a specific operation.
  // The reducer stays authoritative (dispatch confirmAdmin) and the ref mirrors
  // it for the synchronous read inside the confirm-dialog click handlers.
  const confirmAdminForOperation = useCallback((generation: number): void => {
    confirmedAdminOperationRef.current = generation;
    dispatch({ type: "confirmAdmin", generation });
  }, []);

  const setAppliedState = useCallback((next: SetApplied): void => {
    if (next === null) {
      dispatch({ type: "clearApplied" });
      return;
    }
    dispatch({
      type: "applyResult",
      generation: next.operationID,
      result: next.result,
      errorKind: next.errorKind,
      wasPatch: next.wasPatch,
      patchCandidate: next.patchCandidate,
    });
  }, []);

  // Merge the full exact-ID terminal ledger record into the current applied
  // result. The reducer enforces the generation + id correlation and the
  // record's own terminal state, so a pending, cross-operation, or unrelated
  // record is dropped; the full record (finalization provenance included) is
  // retained on the applied state rather than projected to only its nested
  // result.
  const mergeTerminalRecord = useCallback((record: ManagedApplyRecord): void => {
    const prev = appliedRef.current;
    if (prev === null || prev.result.apply_id === undefined) return;
    dispatch({
      type: "mergeTerminalRecord",
      generation: prev.operationID,
      applyId: prev.result.apply_id,
      record,
    });
  }, []);

  const setPatchDraft = useCallback((draft: PatchDraftState | null): void => {
    dispatch({ type: "setPatchDraft", draft });
  }, []);
  const setBaseVersion = useCallback((baseVersion: string | undefined): void => {
    dispatch({ type: "setBaseVersion", baseVersion });
  }, []);
  const setConflictVersion = useCallback((conflictVersion: string | undefined): void => {
    dispatch({ type: "setConflictVersion", conflictVersion });
  }, []);

  const appliedState = useMemo(() => toPanelApplied(state), [state]);

  return {
    state,
    operationIDRef,
    confirmedAdminOperationRef,
    postApplyPollAttemptsRef,
    appliedState,
    patchDraft: state.patchDraft,
    baseVersion: state.baseVersion,
    conflictVersion: state.conflictVersion,
    startOperation,
    cancelOperation,
    confirmAdminForOperation,
    setAppliedState,
    mergeTerminalRecord,
    setPatchDraft,
    setBaseVersion,
    setConflictVersion,
  };
}