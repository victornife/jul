/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { beforeEach, describe, expect, it } from "vitest";
import type { ConfigPatch, PatchLifecycle } from "@/api/client.ts";
import {
  normalizePendingDraft,
  pendingDraftStorageKey,
  setPendingDraft,
  takePendingDraft,
} from "@/lib/configDraftHandoff.ts";

const ops: ConfigPatch[] = [
  { op: "server_add", listen: ":9090", server_names: ["example.com"] },
  {
    op: "location_add",
    listen: ":9090",
    server_names: ["example.com"],
    match_set: { type: "prefix", path: "/" },
    action: { kind: "deny" },
  },
];

const lifecycle: PatchLifecycle = {
  changes: [],
  can_apply_hot: false,
  can_stage_restart: true,
  hot_paths: [],
  restart_required_paths: ["servers[0].listen"],
  new_listener_only_paths: ["servers[0].listen"],
  ignored_deprecated_paths: [],
  validation_rejected_paths: [],
  pending_subsystems: ["listener"],
};

function completePatch(candidate?: string) {
  return {
    kind: "patch" as const,
    ops,
    baseVersion: "base-1",
    summary: "two ordered operations",
    operationSummaries: [
      { op_index: 0, op: "server_add", summary: "server added" },
      { op_index: 1, op: "location_add", summary: "location added" },
    ],
    valid: true,
    validationErrors: [],
    previewDiff: { summary: "2 changes" },
    lifecycle,
    ...(candidate === undefined ? {} : { candidate }),
  };
}

beforeEach(() => {
  takePendingDraft();
  sessionStorage.clear();
});

describe("pending configuration draft handoff", () => {
  it("persists the complete secret-safe patch assessment and drops candidate", () => {
    setPendingDraft(completePatch('token = "must-not-survive"'));

    const serialized = sessionStorage.getItem(pendingDraftStorageKey);
    expect(serialized).not.toBeNull();
    expect(serialized).not.toContain("must-not-survive");

    const restored = takePendingDraft();
    expect(restored).toMatchObject({
      kind: "patch",
      ops,
      baseVersion: "base-1",
      summary: "two ordered operations",
      valid: true,
      lifecycle,
    });
    expect(restored).not.toHaveProperty("candidate");
    expect(sessionStorage.getItem(pendingDraftStorageKey)).toBeNull();
  });

  it("keeps raw TOML handoff in memory only", () => {
    setPendingDraft({ kind: "toml", toml: 'admin_token = "secret"' });
    expect(sessionStorage.length).toBe(0);
    expect(takePendingDraft()).toEqual({ kind: "toml", toml: 'admin_token = "secret"' });
  });

  it("migrates an incomplete legacy patch for lifecycle refresh without trusting candidate", () => {
    sessionStorage.setItem(
      "__jul_config_pending_draft_v2",
      JSON.stringify({
        kind: "patch",
        ops: [ops[0]],
        baseVersion: "legacy-base",
        previewDiff: { summary: "legacy preview" },
        candidate: 'secret = "discard"',
      }),
    );

    const restored = takePendingDraft();
    expect(restored).toMatchObject({
      kind: "patch",
      ops: [ops[0]],
      baseVersion: "legacy-base",
      summary: "legacy preview",
      operationSummaries: [{ op_index: 0, op: "server_add", summary: "server_add" }],
      valid: true,
    });
    expect(restored).not.toHaveProperty("candidate");
    expect(restored).not.toHaveProperty("lifecycle");
    expect(sessionStorage.getItem("__jul_config_pending_draft_v2")).toBeNull();
  });

  it("discards legacy raw TOML from browser storage", () => {
    sessionStorage.setItem(
      "__jul_config_pending_draft",
      JSON.stringify({ kind: "toml", toml: 'admin_token = "must-not-rehydrate"' }),
    );
    expect(takePendingDraft()).toBeNull();
    expect(sessionStorage.getItem("__jul_config_pending_draft")).toBeNull();
  });

  it("clears malformed or obsolete stored data and fails closed", () => {
    sessionStorage.setItem("__jul_config_pending_draft", "{not-json");
    expect(takePendingDraft()).toBeNull();
    expect(sessionStorage.getItem("__jul_config_pending_draft")).toBeNull();
  });

  it("normalizes complete stored data without deserializing candidate", () => {
    const normalized = normalizePendingDraft(completePatch("secret candidate"));
    expect(normalized?.kind).toBe("patch");
    expect(normalized).not.toHaveProperty("candidate");
  });
});
