/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * WS06 Slice 3: the React binding for the config-write mutation reducer must
 * keep the reducer the single source of truth. These tests pin down that every
 * imperative ref update goes through a dispatch, so a synchronous mirror ref can
 * never diverge from reducer state:
 *   - confirmAdminForOperation dispatches confirmAdmin AND mirrors the ref;
 *   - startOperation / cancelOperation reset the post-apply poll ref (the poll
 *     budget lives only in the ref, so cancelling an operation must clear it too).
 */
import { describe, it, expect } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useConfigMutationMachine } from "@/features/config/useConfigMutationMachine.ts";
import { isAdminConfirmedFor } from "@/features/config/configMutationMachine.ts";

describe("useConfigMutationMachine", () => {
  it("confirmAdminForOperation dispatches confirmAdmin and mirrors the ref", () => {
    const { result } = renderHook(() => useConfigMutationMachine());

    let generation = 0;
    act(() => {
      generation = result.current.startOperation();
    });
    expect(result.current.confirmedAdminOperationRef.current).toBeNull();
    expect(result.current.state.adminConfirmedGeneration).toBeNull();

    act(() => {
      result.current.confirmAdminForOperation(generation);
    });
    // The reducer is authoritative: the confirmation lands in reducer state.
    expect(result.current.state.adminConfirmedGeneration).toBe(generation);
    expect(isAdminConfirmedFor(result.current.state, generation)).toBe(true);
    // The synchronous ref mirrors the reducer for async click-handler reads.
    expect(result.current.confirmedAdminOperationRef.current).toBe(generation);

    // Starting the next operation clears both the reducer field and its mirror.
    act(() => {
      result.current.startOperation();
    });
    expect(result.current.state.adminConfirmedGeneration).toBeNull();
    expect(result.current.confirmedAdminOperationRef.current).toBeNull();
  });

  it("cancelOperation resets the post-apply poll ref", () => {
    const { result } = renderHook(() => useConfigMutationMachine());

    act(() => {
      result.current.startOperation();
    });
    // Simulate the legacy-overview poll advancing its budget.
    result.current.postApplyPollAttemptsRef.current = 3;

    act(() => {
      result.current.cancelOperation();
    });
    // Cancelling supersedes the generation and clears the poll budget so a later
    // operation never inherits a stale attempt count.
    expect(result.current.postApplyPollAttemptsRef.current).toBe(0);
    expect(result.current.confirmedAdminOperationRef.current).toBeNull();
  });
});
