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
 *     react-query refetchInterval) read WITHOUT a re-render. These mirror the
 *     reducer's operationGeneration / adminConfirmedGeneration / pollAttempts.
 *   - startOperation() / cancelOperation() — bump the generation (and mirror ref)
 *     so a superseded operation's late callback is ignored.
 *   - appliedState / setAppliedState — the correlated apply result, exposed with
 *     the panel's historical `operationID` field name. setAppliedState accepts a
 *     value (a fresh correlated result → applyResult) or an updater function (the
 *     per-ID terminal merge → mergeTerminal), matching React's setState contract.
 *   - patchDraft / setPatchDraft, baseVersion / setBaseVersion,
 *     conflictVersion / setConflictVersion — thin dispatch wrappers.
 *
 * Keeping the reducer pure and unit-tested while this hook owns only the
 * React/ref plumbing is the whole point of the extraction: the state-transition
 * invariants live in one auditable, test-covered place.
 */

import { useReducer, useRef, useMemo, useCallback } from "react";
import type { ApplyResult, ConfigApplyErrorKind } from "@/api/client.ts";
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
}

type SetApplied = PanelAppliedState | null | ((prev: PanelAppliedState | null) => PanelAppliedState | null);

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
  readonly setAppliedState: (next: SetApplied) => void;
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
  const postApplyPollAttemptsRef = useRef(state.pollAttempts);
  // Latest derived applied state, so a functional setAppliedState updater can be
  // resolved against the current value without depending on a fresh render.
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
    dispatch({ type: "cancelOperation" });
  }, []);

  const setAppliedState = useCallback((next: SetApplied): void => {
    const value = typeof next === "function" ? next(appliedRef.current) : next;
    if (value === null) {
      dispatch({ type: "clearApplied" });
      return;
    }
    const prev = appliedRef.current;
    // A functional update that keeps the same operation + apply id is the per-ID
    // terminal merge; route it through mergeTerminal so the reducer enforces the
    // generation + id correlation invariant. Anything else is a fresh result.
    if (
      typeof next === "function" &&
      prev !== null &&
      prev.operationID === value.operationID &&
      prev.result.apply_id !== undefined &&
      prev.result.apply_id === value.result.apply_id
    ) {
      dispatch({
        type: "mergeTerminal",
        generation: value.operationID,
        applyId: value.result.apply_id,
        terminal: value.result,
      });
      return;
    }
    dispatch({
      type: "applyResult",
      generation: value.operationID,
      result: value.result,
      errorKind: value.errorKind,
      wasPatch: value.wasPatch,
      patchCandidate: value.patchCandidate,
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
    setAppliedState,
    setPatchDraft,
    setBaseVersion,
    setConflictVersion,
  };
}