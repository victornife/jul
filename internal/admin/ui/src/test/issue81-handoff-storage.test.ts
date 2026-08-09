/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { beforeEach, describe, expect, it } from "vitest";
import type { PatchLifecycle } from "@/api/client.ts";
import {
  normalizePendingDraft,
  pendingDraftStorageKey,
  pendingRestartSnapshotEqual,
  setPendingDraft,
  takePendingDraft,
  type PendingRestartSnapshot,
} from "@/lib/configDraftHandoff.ts";

const lifecycle: PatchLifecycle = {
  can_apply_hot: false,
  can_stage_restart: true,
  changes: [],
  hot_paths: [],
  restart_required_paths: ["cache.memory_max_size"],
  new_listener_only_paths: [],
  ignored_deprecated_paths: [],
  validation_rejected_paths: [],
  pending_subsystems: ["cache"],
};
const snapshot: PendingRestartSnapshot = { state: "none", subsystems: [] };
const diff = { summary: "cache.memory_max_size changed" };

describe("issue #81 handoff storage boundary", () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
    void takePendingDraft();
  });

  it("keeps a raw/cache candidate and its pinned base in memory only", () => {
    const candidate = '[cache]\nenabled = true\ndisk_path = "/secret-bearing/path"\n';
    setPendingDraft({
      kind: "toml",
      toml: candidate,
      baseVersion: "version-a",
      previewDiff: diff,
      lifecycle,
      recommendedAction: "stage_restart",
      pendingRestart: snapshot,
      candidateState: "memory_only",
    });
    expect(sessionStorage.length).toBe(0);
    expect(localStorage.length).toBe(0);
    for (let index = 0; index < sessionStorage.length; index += 1) {
      expect(sessionStorage.getItem(sessionStorage.key(index) ?? "")).not.toContain(candidate);
    }
    for (let index = 0; index < localStorage.length; index += 1) {
      expect(localStorage.getItem(localStorage.key(index) ?? "")).not.toContain(candidate);
    }
    expect(takePendingDraft()).toMatchObject({
      kind: "toml",
      toml: candidate,
      baseVersion: "version-a",
    });
  });

  it("persists only secret-safe structured metadata and drops injected candidate source", () => {
    setPendingDraft({
      kind: "patch",
      ops: [{ op: "compression_set", compression: { enabled: false } }],
      baseVersion: "version-a",
      previewDiff: diff,
      lifecycle,
      pendingRestart: snapshot,
      recommendedAction: "stage_restart",
      candidateState: "not_requested",
      requiresFreshPreview: false,
      candidate: "raw-secret-candidate",
    });
    const stored = sessionStorage.getItem(pendingDraftStorageKey);
    expect(stored).not.toBeNull();
    expect(stored).not.toContain("raw-secret-candidate");
    expect(stored).not.toContain("candidate\"");
  });

  it("fails old or incomplete structured handoffs closed", () => {
    const migrated = normalizePendingDraft({
      kind: "patch",
      ops: [{ op: "compression_set", compression: { enabled: true } }],
      previewDiff: diff,
    });
    expect(migrated).toMatchObject({ kind: "patch", requiresFreshPreview: true });
  });

  it("detects any pending-restart state change", () => {
    expect(pendingRestartSnapshotEqual(snapshot, { ...snapshot })).toBe(true);
    expect(
      pendingRestartSnapshotEqual(snapshot, {
        state: "managed_staged",
        stagedVersion: "stage-b",
        subsystems: ["cache"],
      }),
    ).toBe(false);
  });
});
