/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it } from "vitest";
import type { PendingPatchDraft } from "@/lib/configDraftHandoff.ts";
import { decidePatchApplyAction } from "@/lib/patchPreviewAction.ts";
import type { PatchLifecycle } from "@/api/client.ts";

function lifecycle(overrides: Partial<PatchLifecycle> = {}): PatchLifecycle {
  return {
    changes: [],
    can_apply_hot: true,
    can_stage_restart: true,
    hot_paths: ["servers.locations"],
    restart_required_paths: [],
    new_listener_only_paths: [],
    ignored_deprecated_paths: [],
    validation_rejected_paths: [],
    pending_subsystems: [],
    ...overrides,
  };
}

function draft(overrides: Partial<PendingPatchDraft> = {}): PendingPatchDraft {
  return {
    kind: "patch",
    ops: [{ op: "server_toggle_http3", listen: ":443", enabled: true }],
    baseVersion: "v1",
    summary: "HTTP/3 enabled",
    operationSummaries: [{ op_index: 0, op: "server_toggle_http3", summary: "HTTP/3 enabled" }],
    valid: true,
    validationErrors: [],
    previewDiff: { summary: "1 change" },
    lifecycle: lifecycle(),
    ...overrides,
  };
}

describe("decidePatchApplyAction", () => {
  it("chooses hot apply only from the authoritative lifecycle flags", () => {
    expect(decidePatchApplyAction(draft(), false)).toMatchObject({
      action: "hot",
      requiresFreshPreview: false,
    });
  });

  it("routes a known restart-bound patch directly to staged apply", () => {
    expect(
      decidePatchApplyAction(
        draft({
          lifecycle: lifecycle({
            can_apply_hot: false,
            hot_paths: [],
            restart_required_paths: ["servers[0].listen"],
            pending_subsystems: ["listener"],
          }),
        }),
        false,
      ),
    ).toMatchObject({ action: "stage_restart", requiresFreshPreview: false });
  });

  it("updates an existing managed staged configuration when staging is allowed", () => {
    expect(decidePatchApplyAction(draft(), true)).toMatchObject({
      action: "update_staged",
      requiresFreshPreview: false,
    });
  });

  it("fails closed when lifecycle metadata is missing", () => {
    const withoutLifecycle = draft();
    delete (withoutLifecycle as { lifecycle?: PatchLifecycle }).lifecycle;
    expect(decidePatchApplyAction(withoutLifecycle, false)).toEqual({
      action: "none",
      reason: "A fresh lifecycle-aware preview is required before this patch can be applied.",
      requiresFreshPreview: true,
    });
  });

  it("requires a fresh preview when the base version is missing or stale", () => {
    const withoutBase = draft();
    delete (withoutBase as { baseVersion?: string }).baseVersion;
    expect(decidePatchApplyAction(withoutBase, false)).toMatchObject({
      action: "none",
      requiresFreshPreview: true,
    });
    expect(decidePatchApplyAction(draft(), false, true)).toMatchObject({
      action: "none",
      requiresFreshPreview: true,
    });
  });

  it("offers no action for invalid or lifecycle-rejected previews", () => {
    expect(decidePatchApplyAction(draft({ valid: false }), false).action).toBe("none");
    expect(
      decidePatchApplyAction(
        draft({
          lifecycle: lifecycle({ validation_rejected_paths: ["servers[0]"] }),
        }),
        false,
      ).action,
    ).toBe("none");
  });

  it("offers no action when the server permits neither hot apply nor staging", () => {
    expect(
      decidePatchApplyAction(
        draft({
          lifecycle: lifecycle({
            can_apply_hot: false,
            can_stage_restart: false,
            hot_paths: [],
          }),
        }),
        false,
      ).action,
    ).toBe("none");
  });
});
