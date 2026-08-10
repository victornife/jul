/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  fetchPendingRestart: vi.fn(),
  patchConfigBatch: vi.fn(),
}));

vi.mock("@/api/client.ts", async () => {
  const actual = await vi.importActual<typeof import("@/api/client.ts")>("@/api/client.ts");
  return {
    ...actual,
    fetchPendingRestart: mocks.fetchPendingRestart,
    patchConfigBatch: mocks.patchConfigBatch,
  };
});

import {
  pendingRestartSnapshotEqual,
  snapshotPendingRestart,
} from "@/lib/configDraftHandoff.ts";
import { evaluateConfigHandoffGuard } from "@/lib/configHandoffGuard.ts";
import { previewPatchBatchDraft } from "@/lib/useRunPatchBatch.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

const lifecycle = {
  changes: [],
  can_apply_hot: true,
  can_stage_restart: true,
  hot_paths: ["compression"],
  restart_required_paths: [],
  new_listener_only_paths: [],
  ignored_deprecated_paths: [],
  validation_rejected_paths: [],
  pending_subsystems: [],
};

const result = {
  ok: true,
  summary: "compression changed",
  operation_summaries: [
    { op_index: 0, op: "compression_set", summary: "compression changed" },
  ],
  diff: { summary: "1 change" },
  base_version: "base-a",
  valid: true,
  validation_errors: [],
  lifecycle,
};

const op = { op: "compression_set" as const, compression: { enabled: true } };

function pendingNone() {
  return {
    pending: false,
    status: {
      state: "none",
      managed: false,
      staged: false,
      discard_available: false,
      inconsistent: false,
      subsystems: [],
    },
  };
}

function managed(stagedVersion: string, subsystems = ["listener"]) {
  return {
    pending: true,
    status: {
      state: "managed_staged",
      managed: true,
      staged: true,
      discard_available: true,
      inconsistent: false,
      staged_version: stagedVersion,
      subsystems,
    },
  };
}

beforeEach(() => {
  mocks.fetchPendingRestart.mockReset();
  mocks.patchConfigBatch.mockReset();
});

describe("issue #82 pending snapshot ordering", () => {
  it("does not start preview until the pre-preview snapshot resolves", async () => {
    const pending = deferred<ReturnType<typeof pendingNone>>();
    mocks.fetchPendingRestart.mockReturnValue(pending.promise);
    mocks.patchConfigBatch.mockResolvedValue(result);

    const draftPromise = previewPatchBatchDraft([op], "base-a");
    await Promise.resolve();
    expect(mocks.patchConfigBatch).not.toHaveBeenCalled();

    pending.resolve(pendingNone());
    const draft = await draftPromise;
    expect(mocks.patchConfigBatch).toHaveBeenCalledWith([op], "base-a");
    expect(draft.pendingRestart).toEqual({ state: "none", subsystems: [] });
  });

  it.each([
    ["another actor stages", pendingNone(), managed("v2")],
    ["another actor discards", managed("v1"), pendingNone()],
    ["staged version changes", managed("v1"), managed("v2")],
    ["pending subsystem changes", managed("v1"), managed("v1", ["listener", "global"])],
  ])("blocks a stale handoff when %s", (_label, before, after) => {
    const left = snapshotPendingRestart(before as never);
    const right = snapshotPendingRestart(after as never);
    expect(pendingRestartSnapshotEqual(left, right)).toBe(false);
    expect(
      evaluateConfigHandoffGuard({
        pendingKnown: true,
        pendingChanged: true,
        baseChanged: false,
        refreshing: false,
        refreshFailed: false,
      }),
    ).toMatchObject({ blocked: true, requiresRefresh: true });
  });

  it("blocks when the pinned base moves instead of substituting a new token", () => {
    expect(
      evaluateConfigHandoffGuard({
        pendingKnown: true,
        pendingChanged: false,
        baseChanged: true,
        refreshing: false,
        refreshFailed: false,
      }),
    ).toMatchObject({ blocked: true, requiresRefresh: false });
  });
});
