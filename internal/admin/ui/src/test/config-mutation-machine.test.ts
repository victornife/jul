/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * AC-13: unit tests for the extracted configuration-write mutation reducer. The
 * reducer is the single source of truth for the interlocking write state, so
 * these tests pin down the invariants the audit calls out — generation-guarded
 * results, per-ID terminal correlation, and admin-confirmation scoping — without
 * rendering the panel.
 */
import { describe, it, expect } from "vitest";
import {
  configMutationReducer,
  initialConfigMutationState,
  isAdminConfirmedFor,
  type ConfigMutationState,
} from "@/features/config/configMutationMachine.ts";
import type { ApplyResult } from "@/api/client.ts";

function result(applyId: string, extra: Partial<ApplyResult> = {}): ApplyResult {
  return { apply_id: applyId, ...extra } as ApplyResult;
}

describe("configMutationReducer", () => {
  it("startOperation bumps the generation and clears prior applied + admin confirmation", () => {
    let s = initialConfigMutationState("v1");
    s = configMutationReducer(s, {
      type: "applyResult",
      generation: 0,
      result: result("rl_1"),
      errorKind: null,
      wasPatch: false,
      patchCandidate: null,
    });
    s = configMutationReducer(s, { type: "confirmAdmin", generation: 0 });
    expect(s.applied).not.toBeNull();
    expect(s.adminConfirmedGeneration).toBe(0);

    s = configMutationReducer(s, { type: "startOperation" });
    expect(s.operationGeneration).toBe(1);
    expect(s.applied).toBeNull();
    expect(s.adminConfirmedGeneration).toBeNull();
    expect(s.pollAttempts).toBe(0);
    // base_version is preserved across a new operation.
    expect(s.baseVersion).toBe("v1");
  });

  it("cancelOperation supersedes the generation so a stale result is dropped", () => {
    let s = initialConfigMutationState();
    // Operator kicks off an operation (generation 1), then edits again → cancel
    // bumps to generation 2. A late callback for generation 1 must be ignored.
    s = configMutationReducer(s, { type: "startOperation" }); // gen 1
    s = configMutationReducer(s, { type: "cancelOperation" }); // gen 2
    expect(s.operationGeneration).toBe(2);
    const before = s;
    s = configMutationReducer(s, {
      type: "applyResult",
      generation: 1, // stale
      result: result("rl_stale"),
      errorKind: null,
      wasPatch: false,
      patchCandidate: null,
    });
    expect(s).toBe(before); // unchanged reference: the stale result was dropped
    expect(s.applied).toBeNull();
  });

  it("applyResult records only for the current generation", () => {
    let s = initialConfigMutationState();
    s = configMutationReducer(s, { type: "startOperation" }); // gen 1
    s = configMutationReducer(s, {
      type: "applyResult",
      generation: 1,
      result: result("rl_9", { ok: true }),
      errorKind: null,
      wasPatch: true,
      patchCandidate: 'listen = ":80"\n',
    });
    expect(s.applied?.result.apply_id).toBe("rl_9");
    expect(s.applied?.wasPatch).toBe(true);
    expect(s.applied?.patchCandidate).toContain(":80");
  });

  it("mergeTerminal merges only a matching-generation, matching-id record", () => {
    let s = initialConfigMutationState();
    s = configMutationReducer(s, { type: "startOperation" }); // gen 1
    s = configMutationReducer(s, {
      type: "applyResult",
      generation: 1,
      result: result("rl_7", { ok: true }),
      errorKind: null,
      wasPatch: false,
      patchCandidate: null,
    });

    // A terminal record for a DIFFERENT apply id must never merge.
    const beforeWrong = s;
    s = configMutationReducer(s, {
      type: "mergeTerminal",
      generation: 1,
      applyId: "rl_OTHER",
      terminal: { ok: false, restored: true },
    });
    expect(s).toBe(beforeWrong);
    expect(s.applied?.result.ok).toBe(true);

    // A terminal record for a stale generation must never merge.
    const beforeStale = s;
    s = configMutationReducer(s, {
      type: "mergeTerminal",
      generation: 0,
      applyId: "rl_7",
      terminal: { ok: false },
    });
    expect(s).toBe(beforeStale);

    // The correlated terminal record (same generation + same id) merges.
    s = configMutationReducer(s, {
      type: "mergeTerminal",
      generation: 1,
      applyId: "rl_7",
      terminal: { ok: false, restored: true },
    });
    expect(s.applied?.result.ok).toBe(false);
    expect(s.applied?.result.restored).toBe(true);
    // The apply id is preserved through the merge.
    expect(s.applied?.result.apply_id).toBe("rl_7");
  });

  it("confirmAdmin is scoped to the exact generation", () => {
    let s: ConfigMutationState = initialConfigMutationState();
    s = configMutationReducer(s, { type: "startOperation" }); // gen 1
    s = configMutationReducer(s, { type: "confirmAdmin", generation: 1 });
    expect(isAdminConfirmedFor(s, 1)).toBe(true);
    expect(isAdminConfirmedFor(s, 0)).toBe(false);

    // Starting the next operation drops the confirmation — the operator must
    // reconfirm an admin-affecting change for a new operation.
    s = configMutationReducer(s, { type: "startOperation" }); // gen 2
    expect(isAdminConfirmedFor(s, 2)).toBe(false);
    expect(s.adminConfirmedGeneration).toBeNull();
  });

  it("poll-attempt budget increments and resets", () => {
    let s = initialConfigMutationState();
    s = configMutationReducer(s, { type: "incrementPollAttempts" });
    s = configMutationReducer(s, { type: "incrementPollAttempts" });
    expect(s.pollAttempts).toBe(2);
    s = configMutationReducer(s, { type: "resetPollAttempts" });
    expect(s.pollAttempts).toBe(0);
  });

  it("patch-draft, base-version, and conflict-version transitions are isolated", () => {
    let s = initialConfigMutationState("v1");
    s = configMutationReducer(s, {
      type: "setPatchDraft",
      draft: { ops: [], previewDiff: { summary: "x" }, baseVersion: "v1" },
    });
    expect(s.patchDraft?.baseVersion).toBe("v1");
    s = configMutationReducer(s, { type: "setBaseVersion", baseVersion: "v2" });
    expect(s.baseVersion).toBe("v2");
    // patchDraft is untouched by a base-version change.
    expect(s.patchDraft).not.toBeNull();
    s = configMutationReducer(s, { type: "setConflictVersion", conflictVersion: "v3" });
    expect(s.conflictVersion).toBe("v3");
    s = configMutationReducer(s, { type: "setPatchDraft", draft: null });
    expect(s.patchDraft).toBeNull();
  });
});